package media

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lianyorker/cinlan-qq-bot/internal/domain"
	"github.com/lianyorker/cinlan-qq-bot/internal/message"
	"github.com/lianyorker/cinlan-qq-bot/internal/security"
	"github.com/lianyorker/cinlan-qq-bot/internal/tool"
)

const (
	maxImagePromptRunes = 800
	maxImageDimension   = 4096
)

type ImageGeneratorConfig struct {
	Mode        string
	APIURL      string
	APIKey      string
	Model       string
	OutputDir   string
	AllowedRoot string
	MaxBytes    int64
	Timeout     time.Duration
	Retention   time.Duration
	Enhance     bool
}

type ImageGenerator struct {
	mode           string
	endpoint       *url.URL
	apiKey         string
	model          string
	outputDir      string
	maxBytes       int64
	retention      time.Duration
	enhance        bool
	client         *http.Client
	downloadClient *http.Client
	now            func() time.Time
}

type imageArguments struct {
	Prompt string `json:"prompt"`
	Size   string `json:"size,omitempty"`
}

type openAIImageResponse struct {
	Data []struct {
		Base64 string `json:"b64_json"`
		URL    string `json:"url"`
	} `json:"data"`
}

func NewImageGenerator(cfg ImageGeneratorConfig) (*ImageGenerator, error) {
	mode := strings.ToLower(strings.TrimSpace(cfg.Mode))
	if mode != "openai" && mode != "pollinations" {
		return nil, fmt.Errorf("unsupported image generation mode %q", mode)
	}
	endpoint, err := url.Parse(strings.TrimSpace(cfg.APIURL))
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" {
		return nil, errors.New("image generation API URL is invalid")
	}
	if cfg.MaxBytes <= 0 {
		return nil, errors.New("image generation maximum size must be positive")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 2 * time.Minute
	}
	if cfg.Retention <= 0 {
		cfg.Retention = 24 * time.Hour
	}
	boundary, err := security.NewBoundary(cfg.AllowedRoot)
	if err != nil {
		return nil, fmt.Errorf("configure generated image boundary: %w", err)
	}
	outputDir, err := boundary.Resolve(cfg.OutputDir)
	if err != nil {
		return nil, fmt.Errorf("resolve generated image directory: %w", err)
	}
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		return nil, fmt.Errorf("create generated image directory: %w", err)
	}
	outputDir, err = boundary.Resolve(outputDir)
	if err != nil {
		return nil, fmt.Errorf("verify generated image directory: %w", err)
	}
	info, err := os.Stat(outputDir)
	if err != nil || !info.IsDir() {
		return nil, errors.New("generated image output is not a directory")
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		return nil, errors.New("image generation model is empty")
	}
	return &ImageGenerator{
		mode:           mode,
		endpoint:       endpoint,
		apiKey:         strings.TrimSpace(cfg.APIKey),
		model:          model,
		outputDir:      outputDir,
		maxBytes:       cfg.MaxBytes,
		retention:      cfg.Retention,
		enhance:        cfg.Enhance,
		client:         &http.Client{Timeout: cfg.Timeout},
		downloadClient: publicHTTPClient(cfg.Timeout),
		now:            time.Now,
	}, nil
}

func (g *ImageGenerator) OutputDir() string {
	if g == nil {
		return ""
	}
	return g.outputDir
}

func (g *ImageGenerator) Tool() tool.Definition {
	return tool.Definition{
		Name: "generate_image",
		Description: "根据用户描述生成一张图片并直接发送到当前会话。" +
			"用户要求画图、生成图片或发一张图片时必须调用；不要只返回提示词。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"prompt": map[string]any{
					"type":        "string",
					"description": "要生成的画面描述，不包含文件路径或命令。",
				},
				"size": map[string]any{
					"type":        "string",
					"enum":        []string{"1024x1024", "1536x1024", "1024x1536"},
					"description": "可选画布尺寸，默认 1024x1024。",
				},
			},
			"required":             []string{"prompt"},
			"additionalProperties": false,
		},
		Permission: tool.PermissionEveryone,
		Timeout:    g.client.Timeout + 5*time.Second,
		Handler:    g.generate,
	}
}

func (g *ImageGenerator) generate(ctx context.Context, call tool.Call) (tool.Result, error) {
	var arguments imageArguments
	decoder := json.NewDecoder(bytes.NewReader(call.Arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&arguments); err != nil {
		return tool.Result{}, fmt.Errorf("decode image generation arguments: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return tool.Result{}, errors.New("decode image generation arguments: trailing JSON data")
	}
	arguments.Prompt = strings.TrimSpace(arguments.Prompt)
	if arguments.Prompt == "" {
		return tool.Result{}, errors.New("image prompt is empty")
	}
	if utf8.RuneCountInString(arguments.Prompt) > maxImagePromptRunes {
		return tool.Result{}, fmt.Errorf(
			"image prompt exceeds %d characters",
			maxImagePromptRunes,
		)
	}
	width, height, normalizedSize, err := parseImageSize(arguments.Size)
	if err != nil {
		return tool.Result{}, err
	}
	_ = g.cleanupExpired()

	var data []byte
	switch g.mode {
	case "openai":
		data, err = g.generateOpenAI(ctx, arguments.Prompt, normalizedSize)
	case "pollinations":
		data, err = g.generatePollinations(ctx, arguments.Prompt, width, height)
	default:
		err = errors.New("image generation mode is unavailable")
	}
	if err != nil {
		return tool.Result{}, err
	}
	extension, _, _, err := validateGeneratedImage(data)
	if err != nil {
		return tool.Result{}, err
	}
	path, err := g.store(data, extension)
	if err != nil {
		return tool.Result{}, err
	}
	response := domain.AgentResponse{
		Reply: "给你生成好了。",
		Chain: message.Chain{
			message.Text("给你生成好了。"),
			message.Image(path),
		},
	}
	return tool.Result{
		Content: map[string]any{
			"status": "sent",
			"size":   normalizedSize,
		},
		Response: &response,
	}, nil
}

func (g *ImageGenerator) generateOpenAI(
	ctx context.Context,
	prompt, size string,
) ([]byte, error) {
	payload, err := json.Marshal(map[string]any{
		"model":  g.model,
		"prompt": prompt,
		"size":   size,
		"n":      1,
	})
	if err != nil {
		return nil, fmt.Errorf("encode image generation request: %w", err)
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		g.endpoint.String(),
		bytes.NewReader(payload),
	)
	if err != nil {
		return nil, fmt.Errorf("build image generation request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	if g.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+g.apiKey)
	}
	response, err := g.client.Do(request)
	if err != nil {
		return nil, errors.New("image generation provider request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf(
			"image generation provider returned HTTP %d",
			response.StatusCode,
		)
	}
	responseLimit := g.maxBytes*2 + 1<<20
	if responseLimit > 64<<20 {
		responseLimit = 64 << 20
	}
	body, err := readBounded(response.Body, responseLimit)
	if err != nil {
		return nil, fmt.Errorf("read image generation response: %w", err)
	}
	var decoded openAIImageResponse
	if err := json.Unmarshal(body, &decoded); err != nil || len(decoded.Data) == 0 {
		return nil, errors.New("image generation provider returned an invalid response")
	}
	if encoded := strings.TrimSpace(decoded.Data[0].Base64); encoded != "" {
		data, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, errors.New("image generation provider returned invalid base64")
		}
		if int64(len(data)) > g.maxBytes {
			return nil, fmt.Errorf("generated image exceeds %d bytes", g.maxBytes)
		}
		return data, nil
	}
	if remoteURL := strings.TrimSpace(decoded.Data[0].URL); remoteURL != "" {
		return g.download(ctx, remoteURL)
	}
	return nil, errors.New("image generation provider returned no image")
}

func (g *ImageGenerator) generatePollinations(
	ctx context.Context,
	prompt string,
	width, height int,
) ([]byte, error) {
	endpoint := *g.endpoint
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/" + prompt
	query := endpoint.Query()
	query.Set("width", strconv.Itoa(width))
	query.Set("height", strconv.Itoa(height))
	query.Set("model", g.model)
	query.Set("nologo", "true")
	query.Set("safe", "true")
	if g.enhance {
		query.Set("enhance", "true")
	}
	query.Set("seed", strconv.FormatUint(randomUint64(), 10))
	endpoint.RawQuery = query.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build image generation request: %w", err)
	}
	request.Header.Set("Accept", "image/*")
	if g.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+g.apiKey)
	}
	response, err := g.client.Do(request)
	if err != nil {
		return nil, errors.New("image generation provider request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf(
			"image generation provider returned HTTP %d",
			response.StatusCode,
		)
	}
	return readBounded(response.Body, g.maxBytes)
}

func (g *ImageGenerator) download(ctx context.Context, value string) ([]byte, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return nil, errors.New("image generation provider returned an unsafe image URL")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, errors.New("build generated image download request")
	}
	request.Header.Set("Accept", "image/*")
	response, err := g.downloadClient.Do(request)
	if err != nil {
		return nil, errors.New("generated image download failed")
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("generated image download returned HTTP %d", response.StatusCode)
	}
	return readBounded(response.Body, g.maxBytes)
}

func (g *ImageGenerator) store(data []byte, extension string) (string, error) {
	randomName := make([]byte, 16)
	if _, err := rand.Read(randomName); err != nil {
		return "", errors.New("generate image file name")
	}
	name := hex.EncodeToString(randomName) + extension
	path := filepath.Join(g.outputDir, name)
	temp, err := os.CreateTemp(g.outputDir, ".generated-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create generated image file: %w", err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return "", fmt.Errorf("protect generated image file: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return "", fmt.Errorf("write generated image file: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return "", fmt.Errorf("sync generated image file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return "", fmt.Errorf("close generated image file: %w", err)
	}
	if err := os.Rename(tempName, path); err != nil {
		return "", fmt.Errorf("publish generated image file: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", fmt.Errorf("protect generated image: %w", err)
	}
	return filepath.Abs(path)
}

func (g *ImageGenerator) cleanupExpired() error {
	cutoff := g.now().Add(-g.retention)
	entries, err := os.ReadDir(g.outputDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || !info.ModTime().Before(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(g.outputDir, entry.Name()))
	}
	return nil
}

func parseImageSize(value string) (int, int, string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = "1024x1024"
	}
	switch value {
	case "1024x1024":
		return 1024, 1024, value, nil
	case "1536x1024":
		return 1536, 1024, value, nil
	case "1024x1536":
		return 1024, 1536, value, nil
	default:
		return 0, 0, "", fmt.Errorf("unsupported image size %q", value)
	}
}

func validateGeneratedImage(data []byte) (string, int, int, error) {
	if len(data) < 12 {
		return "", 0, 0, errors.New("generated image is empty or truncated")
	}
	mediaType := http.DetectContentType(data)
	extension := ""
	switch mediaType {
	case "image/jpeg":
		extension = ".jpg"
	case "image/png":
		extension = ".png"
	case "image/gif":
		extension = ".gif"
	default:
		return "", 0, 0, fmt.Errorf(
			"generated image has unsupported media type %q",
			mediaType,
		)
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return "", 0, 0, errors.New("generated image cannot be decoded")
	}
	if config.Width <= 0 || config.Height <= 0 ||
		config.Width > maxImageDimension || config.Height > maxImageDimension {
		return "", 0, 0, errors.New("generated image dimensions are invalid")
	}
	return extension, config.Width, config.Height, nil
}

func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("response exceeds %d bytes", limit)
	}
	return data, nil
}

func randomUint64() uint64 {
	var value [8]byte
	if _, err := rand.Read(value[:]); err != nil {
		return uint64(time.Now().UnixNano())
	}
	return binary.LittleEndian.Uint64(value[:])
}

func publicHTTPClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = func(
		ctx context.Context,
		network, address string,
	) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, errors.New("invalid generated image download address")
		}
		addresses, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
		if err != nil {
			return nil, errors.New("resolve generated image download host")
		}
		for _, current := range addresses {
			if !current.IsLoopback() && !current.IsPrivate() &&
				!current.IsUnspecified() && !current.IsMulticast() &&
				!current.IsLinkLocalUnicast() {
				return dialer.DialContext(
					ctx,
					network,
					net.JoinHostPort(current.String(), port),
				)
			}
		}
		return nil, errors.New("generated image download host is not public")
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("generated image download redirected too many times")
			}
			if request.URL.Scheme != "https" {
				return errors.New("generated image download redirect is not HTTPS")
			}
			return nil
		},
	}
}
