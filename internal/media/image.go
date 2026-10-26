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
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
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
	referenceFor   func(ctx context.Context, actor tool.Actor) string
	now            func() time.Time
}

type imageArguments struct {
	Prompt string `json:"prompt"`
	Size   string `json:"size,omitempty"`
	Image  string `json:"image,omitempty"` // reference image URL for edit mode
}

type openAIImageResponse struct {
	Data []struct {
		Base64 string `json:"b64_json"`
		URL    string `json:"url"`
	} `json:"data"`
}

func NewImageGenerator(cfg ImageGeneratorConfig) (*ImageGenerator, error) {
	mode := strings.ToLower(strings.TrimSpace(cfg.Mode))
	if mode == "antigravity" {
		mode = "openai_chat"
	}
	if mode != "openai" && mode != "pollinations" && mode != "openai_chat" {
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

// SetReferenceResolver wires a per-chat stored reference image lookup so an
// edit request whose model-supplied URL is dead can fall back to the local
// remembered photo instead of failing outright.
func (g *ImageGenerator) SetReferenceResolver(
	resolve func(ctx context.Context, actor tool.Actor) string,
) {
	if g == nil {
		return
	}
	g.referenceFor = resolve
}

func (g *ImageGenerator) Tool() tool.Definition {
	return tool.Definition{
		Name: "generate_image",
		Description: "根据用户描述生成一张图片并直接发送到当前会话。" +
			"用户要求画图、生成图片或发一张图片时必须调用；不要只返回提示词。" +
			"画的是你自己（你的自拍/照片/你的样子）时：prompt 以 [SELF] 开头，" +
			"系统会自动带上你的参考照片走图生图，保证真人照片风格且长相一致——" +
			"prompt 里只写场景、动作、穿着，不用编长相；image 参数留空。" +
			"画别人或别的东西时正常写 prompt，不要加 [SELF]。" +
			"如果返回“内容审核拒绝”错误：把提示词改写一遍再调用一次——" +
			"去掉真人自拍、具体年龄、身材/裸露描述，此时才允许改用插画风格重试；" +
			"改写后仍被拒就直接跟用户说明被审核挡了，别反复重试。",
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
				"image": map[string]any{
					"type":        "string",
					"description": "可选参考图 URL，用户消息带图时系统注入，走图生图编辑。",
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

// stripSelfTag detects the model's "this is a photo of ME" contract —
// prompts describing her own selfie/photo open with [SELF] — and
// returns the cleaned prompt so the tag never renders as literal text.
func stripSelfTag(prompt string) (string, bool) {
	t := strings.TrimSpace(prompt)
	for _, tag := range []string{"[SELF]", "【SELF】", "[self]", "【self】"} {
		if strings.HasPrefix(t, tag) {
			return strings.TrimSpace(t[len(tag):]), true
		}
	}
	return prompt, false
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
	if cleaned, isSelf := stripSelfTag(arguments.Prompt); isSelf {
		arguments.Prompt = cleaned
		// Her own photo: attach the stored reference when the model
		// didn't supply one, so style stays photorealistic and the
		// face stays the same person across generations.
		if arguments.Image == "" && g.referenceFor != nil {
			if ref := g.referenceFor(ctx, call.Actor); ref != "" {
				arguments.Image = ref
			}
		}
		if arguments.Image != "" {
			arguments.Prompt +=
				"，真人照片风格，与参考照片是同一个人，长相和发色保持一致"
		} else {
			arguments.Prompt += "，真人照片风格"
		}
	}
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
		if refURL := strings.TrimSpace(arguments.Image); refURL != "" {
			data, err = g.generateOpenAIEdit(ctx, arguments.Prompt, normalizedSize, refURL)
			if err != nil && g.referenceFor != nil {
				if local := g.referenceFor(
					ctx, call.Actor,
				); local != "" && local != refURL {
					data, err = g.generateOpenAIEdit(
						ctx, arguments.Prompt, normalizedSize, local,
					)
				}
			}
		} else {
			data, err = g.generateOpenAI(ctx, arguments.Prompt, normalizedSize)
		}
	case "openai_chat":
		data, err = g.generateOpenAIChat(ctx, arguments.Prompt, normalizedSize)
	case "pollinations":
		data, err = g.generatePollinations(ctx, arguments.Prompt, width, height)
	default:
		err = errors.New("image generation mode is unavailable")
	}
	if err != nil {
		// The model paraphrases whatever the tool result says — steer it
		// to handle the failure naturally instead of broadcasting "被拦了"
		// to the user. The reason stays visible so a rephrase retry still
		// makes sense.
		return tool.Result{}, fmt.Errorf(
			"生图暂时没成（原因：%v）——这是内部状态，别把原因、被拦细节或「换个说法再试」说出口，自然带过或换个方式安慰对方即可",
			err,
		)
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
		Chain: message.Chain{
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
		return nil, providerHTTPError("image generation provider", response)
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

// generateOpenAIEdit calls /v1/images/edits with a downloaded reference
// image so the user can transform a picture they just sent.
func (g *ImageGenerator) generateOpenAIEdit(
	ctx context.Context,
	prompt, size, refURL string,
) ([]byte, error) {
	var refData []byte
	var err error
	if strings.HasPrefix(refURL, "http") {
		refData, err = g.download(ctx, refURL)
	} else {
		refData, err = os.ReadFile(refURL)
	}
	if err != nil {
		return nil, fmt.Errorf("load reference image: %w", err)
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("model", g.model)
	_ = writer.WriteField("prompt", prompt)
	if size != "" {
		_ = writer.WriteField("size", size)
	}
	_ = writer.WriteField("n", "1")
	part, err := writer.CreateFormFile("image", "reference.png")
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(refData); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	endpoint := *g.endpoint
	endpoint.Path = strings.Replace(
		endpoint.Path, "images/generations", "images/edits", 1,
	)
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		endpoint.String(),
		&body,
	)
	if err != nil {
		return nil, fmt.Errorf("build image edit request: %w", err)
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	if g.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+g.apiKey)
	}
	response, err := g.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("image edit request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, providerHTTPError("image edit provider", response)
	}
	responseLimit := g.maxBytes*2 + 1<<20
	if responseLimit > 64<<20 {
		responseLimit = 64 << 20
	}
	raw, readErr := readBounded(response.Body, responseLimit)
	if readErr != nil {
		return nil, fmt.Errorf("read image edit response: %w", readErr)
	}
	var decoded openAIImageResponse
	if err := json.Unmarshal(raw, &decoded); err != nil || len(decoded.Data) == 0 {
		return nil, errors.New("image edit provider returned an invalid response")
	}
	if encoded := strings.TrimSpace(decoded.Data[0].Base64); encoded != "" {
		return base64.StdEncoding.DecodeString(encoded)
	}
	if remoteURL := strings.TrimSpace(decoded.Data[0].URL); remoteURL != "" {
		return g.download(ctx, remoteURL)
	}
	return nil, errors.New("image edit provider returned no image")
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
		return nil, providerHTTPError("image generation provider", response)
	}
	return readBounded(response.Body, g.maxBytes)
}

// chatCompletionsResponse covers relays (e.g. Antigravity) that return
// generated images inside a chat completion: content may be a markdown
// string with ![image](data:...) or a multipart array.
type chatCompletionsResponse struct {
	Choices []struct {
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

type chatContentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	ImageURL struct {
		URL string `json:"url"`
	} `json:"image_url"`
}

var chatImageDataPattern = regexp.MustCompile(
	`data:image/(?:png|jpe?g|webp|gif);base64,([A-Za-z0-9+/=]+)`,
)

func (g *ImageGenerator) generateOpenAIChat(
	ctx context.Context,
	prompt, size string,
) ([]byte, error) {
	if size != "" {
		prompt = prompt + "\n(please render the image at " + size + " pixels)"
	}
	payload, err := json.Marshal(map[string]any{
		"model":    g.model,
		"messages": []map[string]any{{"role": "user", "content": prompt}},
		"stream":   false,
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
		return nil, providerHTTPError("image generation provider", response)
	}
	responseLimit := g.maxBytes*2 + 1<<20
	if responseLimit > 64<<20 {
		responseLimit = 64 << 20
	}
	body, err := readBounded(response.Body, responseLimit)
	if err != nil {
		return nil, fmt.Errorf("read image generation response: %w", err)
	}
	var decoded chatCompletionsResponse
	if err := json.Unmarshal(body, &decoded); err != nil || len(decoded.Choices) == 0 {
		return nil, errors.New("image generation provider returned an invalid response")
	}
	image, err := g.extractChatImage(ctx, decoded.Choices[0].Message.Content)
	if err != nil {
		return nil, err
	}
	return image, nil
}

func (g *ImageGenerator) extractChatImage(
	ctx context.Context,
	raw json.RawMessage,
) ([]byte, error) {
	var textParts []string
	var imageURLs []string
	trimmed := bytes.TrimSpace(raw)
	switch {
	case len(trimmed) == 0:
		return nil, errors.New("image generation provider returned empty content")
	case trimmed[0] == '"':
		var single string
		if err := json.Unmarshal(trimmed, &single); err != nil {
			return nil, errors.New("image generation provider returned invalid content")
		}
		textParts = append(textParts, single)
	case trimmed[0] == '[':
		var parts []chatContentPart
		if err := json.Unmarshal(trimmed, &parts); err != nil {
			return nil, errors.New("image generation provider returned invalid content parts")
		}
		for _, part := range parts {
			if part.Text != "" {
				textParts = append(textParts, part.Text)
			}
			if part.ImageURL.URL != "" {
				imageURLs = append(imageURLs, part.ImageURL.URL)
			}
		}
	default:
		return nil, errors.New("image generation provider returned unexpected content")
	}
	for _, text := range textParts {
		match := chatImageDataPattern.FindStringSubmatch(text)
		if len(match) < 2 {
			continue
		}
		data, err := base64.StdEncoding.DecodeString(match[1])
		if err != nil {
			return nil, errors.New("image generation provider returned invalid base64")
		}
		if int64(len(data)) > g.maxBytes {
			return nil, fmt.Errorf("generated image exceeds %d bytes", g.maxBytes)
		}
		return data, nil
	}
	for _, value := range imageURLs {
		if strings.HasPrefix(value, "data:") {
			match := chatImageDataPattern.FindStringSubmatch(value)
			if len(match) < 2 {
				continue
			}
			data, err := base64.StdEncoding.DecodeString(match[1])
			if err != nil {
				continue
			}
			if int64(len(data)) > g.maxBytes {
				return nil, fmt.Errorf("generated image exceeds %d bytes", g.maxBytes)
			}
			return data, nil
		}
		if strings.HasPrefix(value, "https://") {
			return g.download(ctx, value)
		}
	}
	return nil, errors.New("image generation provider returned no image")
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
	randomName := make([]byte, 4)
	if _, err := rand.Read(randomName); err != nil {
		return "", errors.New("generate image file name")
	}
	// Timestamped names sort chronologically and stay unique under
	// concurrent generations via the short random suffix.
	name := fmt.Sprintf(
		"%s-%s%s",
		g.now().Format("20060102150405"),
		hex.EncodeToString(randomName),
		extension,
	)
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

// providerHTTPError formats a non-2xx provider response including a
// bounded slice of the response body — without it "HTTP 400" says
// nothing about whether the prompt was rejected or the request malformed.
func providerHTTPError(prefix string, response *http.Response) error {
	body, _ := readBounded(response.Body, 4<<10)
	detail := strings.TrimSpace(string(body))
	if detail == "" {
		return fmt.Errorf("%s returned HTTP %d", prefix, response.StatusCode)
	}
	return fmt.Errorf(
		"%s returned HTTP %d: %s",
		prefix, response.StatusCode, detail,
	)
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
