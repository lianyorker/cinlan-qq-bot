package media

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/domain"
	"github.com/lianyorker/cinlan-qq-bot/internal/message"
	"github.com/lianyorker/cinlan-qq-bot/internal/security"
	"github.com/lianyorker/cinlan-qq-bot/internal/tool"
)

const (
	maxScreenshotURLBytes = 2048
	maxBrowserOutputBytes = 64 << 10
)

type WebScreenshotConfig struct {
	BrowserPath  string
	AllowedHosts []string
	OutputDir    string
	AllowedRoot  string
	MaxBytes     int64
	Timeout      time.Duration
	Retention    time.Duration
	Width        int
	Height       int
	Wait         time.Duration
}

type WebScreenshot struct {
	browserPath  string
	allowedHosts map[string]struct{}
	outputDir    string
	maxBytes     int64
	timeout      time.Duration
	retention    time.Duration
	width        int
	height       int
	wait         time.Duration
	now          func() time.Time
	preflight    func(context.Context, string) (string, error)
	runBrowser   func(context.Context, string, []string, io.Writer) error
}

type screenshotArguments struct {
	URL string `json:"url"`
}

func NewWebScreenshot(cfg WebScreenshotConfig) (*WebScreenshot, error) {
	browserPath := strings.TrimSpace(cfg.BrowserPath)
	if !filepath.IsAbs(browserPath) {
		return nil, errors.New("web screenshot browser path must be absolute")
	}
	browserPath, err := filepath.Abs(browserPath)
	if err != nil {
		return nil, fmt.Errorf("resolve web screenshot browser: %w", err)
	}
	info, err := os.Stat(browserPath)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("web screenshot browser is not a regular file")
	}
	allowedHosts, err := normalizeAllowedScreenshotHosts(cfg.AllowedHosts)
	if err != nil {
		return nil, err
	}
	if cfg.MaxBytes <= 0 {
		return nil, errors.New("web screenshot maximum size must be positive")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 45 * time.Second
	}
	if cfg.Retention <= 0 {
		cfg.Retention = 24 * time.Hour
	}
	if cfg.Width <= 0 || cfg.Width > maxImageDimension ||
		cfg.Height <= 0 || cfg.Height > maxImageDimension {
		return nil, fmt.Errorf(
			"web screenshot dimensions must be between 1 and %d",
			maxImageDimension,
		)
	}
	if cfg.Wait < 0 || cfg.Wait > 10*time.Second {
		return nil, errors.New("web screenshot wait must be between 0s and 10s")
	}
	boundary, err := security.NewBoundary(cfg.AllowedRoot)
	if err != nil {
		return nil, fmt.Errorf("configure web screenshot boundary: %w", err)
	}
	outputDir, err := boundary.Resolve(cfg.OutputDir)
	if err != nil {
		return nil, fmt.Errorf("resolve web screenshot directory: %w", err)
	}
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		return nil, fmt.Errorf("create web screenshot directory: %w", err)
	}
	outputDir, err = boundary.Resolve(outputDir)
	if err != nil {
		return nil, fmt.Errorf("verify web screenshot directory: %w", err)
	}
	info, err = os.Stat(outputDir)
	if err != nil || !info.IsDir() {
		return nil, errors.New("web screenshot output is not a directory")
	}

	screenshot := &WebScreenshot{
		browserPath:  browserPath,
		allowedHosts: allowedHosts,
		outputDir:    outputDir,
		maxBytes:     cfg.MaxBytes,
		timeout:      cfg.Timeout,
		retention:    cfg.Retention,
		width:        cfg.Width,
		height:       cfg.Height,
		wait:         cfg.Wait,
		now:          time.Now,
		runBrowser:   executeBrowser,
	}
	screenshot.preflight = screenshot.preflightURL
	return screenshot, nil
}

func (s *WebScreenshot) OutputDir() string {
	if s == nil {
		return ""
	}
	return s.outputDir
}

func (s *WebScreenshot) Tool() tool.Definition {
	return tool.Definition{
		Name: "capture_webpage",
		Description: "访问管理员允许的 HTTPS 网站并截取当前网页，随后把真实截图直接发送到当前会话。" +
			"用户要求访问网站、查看官网页面或截网页时必须调用；不要改用 generate_image。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{
					"type":        "string",
					"description": "需要截图的完整 HTTPS URL，host 必须在管理员白名单中。",
				},
			},
			"required":             []string{"url"},
			"additionalProperties": false,
		},
		Permission: tool.PermissionEveryone,
		Timeout:    s.timeout + 5*time.Second,
		Handler:    s.capture,
	}
}

func (s *WebScreenshot) capture(
	ctx context.Context,
	call tool.Call,
) (tool.Result, error) {
	var arguments screenshotArguments
	decoder := json.NewDecoder(bytes.NewReader(call.Arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&arguments); err != nil {
		return tool.Result{}, fmt.Errorf("decode web screenshot arguments: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return tool.Result{}, errors.New("decode web screenshot arguments: trailing JSON data")
	}
	target, err := normalizeScreenshotURL(arguments.URL, s.allowedHosts)
	if err != nil {
		return tool.Result{}, err
	}
	target, err = s.preflight(ctx, target)
	if err != nil {
		return tool.Result{}, err
	}
	_ = s.cleanupExpired()

	proxy, err := startPublicWebProxy()
	if err != nil {
		return tool.Result{}, errors.New("start restricted web screenshot network")
	}
	defer proxy.Close()

	profileDir, err := os.MkdirTemp(s.outputDir, ".chrome-profile-")
	if err != nil {
		return tool.Result{}, fmt.Errorf("create web screenshot browser profile: %w", err)
	}
	defer os.RemoveAll(profileDir)
	if err := os.Chmod(profileDir, 0o700); err != nil {
		return tool.Result{}, fmt.Errorf("protect web screenshot browser profile: %w", err)
	}

	outputPath, err := s.newOutputPath()
	if err != nil {
		return tool.Result{}, err
	}
	defer func() {
		if _, statErr := os.Stat(outputPath); statErr == nil {
			return
		}
		_ = os.Remove(outputPath)
	}()
	args := []string{
		"--headless=new",
		"--disable-gpu",
		"--hide-scrollbars",
		"--incognito",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-extensions",
		"--disable-sync",
		"--disable-background-networking",
		"--disable-component-update",
		"--disable-client-side-phishing-detection",
		"--disable-features=Translate,MediaRouter",
		"--disable-dev-shm-usage",
		"--run-all-compositor-stages-before-draw",
		"--proxy-server=http://" + proxy.Address(),
		"--proxy-bypass-list=<-loopback>",
		"--user-data-dir=" + profileDir,
		fmt.Sprintf("--window-size=%d,%d", s.width, s.height),
		"--virtual-time-budget=" + strconv.FormatInt(s.wait.Milliseconds(), 10),
		"--screenshot=" + outputPath,
		target,
	}
	commandContext, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	var browserOutput boundedBuffer
	browserOutput.limit = maxBrowserOutputBytes
	if err := s.runBrowser(
		commandContext,
		s.browserPath,
		args,
		&browserOutput,
	); err != nil {
		_ = os.Remove(outputPath)
		if errors.Is(commandContext.Err(), context.DeadlineExceeded) {
			return tool.Result{}, errors.New("web screenshot browser timed out")
		}
		return tool.Result{}, errors.New("web screenshot browser failed")
	}
	if err := s.validateOutput(outputPath); err != nil {
		_ = os.Remove(outputPath)
		return tool.Result{}, err
	}

	response := domain.AgentResponse{
		Reply: "网页截图好了。",
		Chain: message.Chain{
			message.Text("网页截图好了。"),
			message.Image(outputPath),
		},
	}
	return tool.Result{
		Content: map[string]any{
			"status": "sent",
			"url":    target,
			"width":  s.width,
			"height": s.height,
		},
		Response: &response,
	}, nil
}

func (s *WebScreenshot) preflightURL(
	ctx context.Context,
	value string,
) (string, error) {
	client := newRestrictedHTTPClient(s.timeout, func(candidate *url.URL) error {
		_, err := normalizeScreenshotURL(candidate.String(), s.allowedHosts)
		return err
	})
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, value, nil)
	if err != nil {
		return "", errors.New("build web screenshot preflight request")
	}
	request.Header.Set(
		"User-Agent",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "+
			"(KHTML, like Gecko) Chrome/150.0.0.0 Safari/537.36",
	)
	request.Header.Set("Accept", "text/html,application/xhtml+xml")
	response, err := client.Do(request)
	if err != nil {
		return "", errors.New("web screenshot target is unavailable or unsafe")
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK ||
		response.StatusCode >= http.StatusBadRequest {
		return "", fmt.Errorf(
			"web screenshot target returned HTTP %d",
			response.StatusCode,
		)
	}
	target, err := normalizeScreenshotURL(
		response.Request.URL.String(),
		s.allowedHosts,
	)
	if err != nil {
		return "", errors.New("web screenshot redirect target is not allowed")
	}
	return target, nil
}

func (s *WebScreenshot) newOutputPath() (string, error) {
	randomName := make([]byte, 16)
	if _, err := rand.Read(randomName); err != nil {
		return "", errors.New("generate web screenshot file name")
	}
	return filepath.Join(s.outputDir, hex.EncodeToString(randomName)+".png"), nil
}

func (s *WebScreenshot) validateOutput(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() ||
		info.Mode()&os.ModeSymlink != 0 {
		return errors.New("web screenshot output is not a regular file")
	}
	if info.Size() <= 0 || info.Size() > s.maxBytes {
		return fmt.Errorf("web screenshot exceeds %d bytes", s.maxBytes)
	}
	file, err := os.Open(path)
	if err != nil {
		return errors.New("open web screenshot output")
	}
	data, readErr := readBounded(file, s.maxBytes)
	closeErr := file.Close()
	if readErr != nil {
		return fmt.Errorf("read web screenshot output: %w", readErr)
	}
	if closeErr != nil {
		return errors.New("close web screenshot output")
	}
	extension, width, height, err := validateGeneratedImage(data)
	if err != nil || extension != ".png" {
		return errors.New("web screenshot output is not a valid PNG")
	}
	if width != s.width || height != s.height {
		return fmt.Errorf(
			"web screenshot dimensions are %dx%d; want %dx%d",
			width,
			height,
			s.width,
			s.height,
		)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return errors.New("protect web screenshot output")
	}
	return nil
}

func (s *WebScreenshot) cleanupExpired() error {
	cutoff := s.now().Add(-s.retention)
	entries, err := os.ReadDir(s.outputDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 ||
			!strings.EqualFold(filepath.Ext(entry.Name()), ".png") {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() ||
			!info.ModTime().Before(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(s.outputDir, entry.Name()))
	}
	return nil
}

func normalizeAllowedScreenshotHosts(values []string) (map[string]struct{}, error) {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		host, err := normalizeScreenshotHostname(value)
		if err != nil {
			return nil, fmt.Errorf("invalid web screenshot allowed host %q", value)
		}
		result[host] = struct{}{}
	}
	if len(result) == 0 {
		return nil, errors.New("web screenshot allowed hosts are empty")
	}
	return result, nil
}

func normalizeScreenshotURL(
	value string,
	allowedHosts map[string]struct{},
) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxScreenshotURLBytes {
		return "", errors.New("web screenshot URL is empty or too long")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Opaque != "" ||
		!strings.EqualFold(parsed.Scheme, "https") ||
		parsed.Host == "" {
		return "", errors.New("web screenshot URL must be an absolute HTTPS URL")
	}
	if parsed.User != nil {
		return "", errors.New("web screenshot URL credentials are not allowed")
	}
	if parsed.Fragment != "" || parsed.RawFragment != "" {
		return "", errors.New("web screenshot URL fragments are not allowed")
	}
	if port := parsed.Port(); port != "" && port != "443" {
		return "", errors.New("web screenshot URL must use port 443")
	}
	host, err := normalizeScreenshotHostname(parsed.Hostname())
	if err != nil {
		return "", errors.New("web screenshot URL host is invalid")
	}
	if _, ok := allowedHosts[host]; !ok {
		return "", errors.New("web screenshot URL host is not allowed")
	}
	parsed.Scheme = "https"
	parsed.Host = host
	return parsed.String(), nil
}

func normalizeScreenshotHostname(value string) (string, error) {
	host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
	if host == "" || len(host) > 253 || net.ParseIP(host) != nil ||
		host == "localhost" || strings.HasSuffix(host, ".localhost") ||
		strings.HasSuffix(host, ".local") ||
		strings.ContainsAny(host, ":/%\\@[]") {
		return "", errors.New("invalid host")
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return "", errors.New("host must be a fully qualified domain name")
	}
	for _, label := range labels {
		if label == "" || len(label) > 63 ||
			label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("invalid host label")
		}
		for _, current := range label {
			if (current < 'a' || current > 'z') &&
				(current < '0' || current > '9') &&
				current != '-' {
				return "", errors.New("host must use ASCII DNS labels")
			}
		}
	}
	return host, nil
}

func executeBrowser(
	ctx context.Context,
	executable string,
	args []string,
	output io.Writer,
) error {
	command := exec.CommandContext(ctx, executable, args...)
	command.Stdout = output
	command.Stderr = output
	return command.Run()
}

type boundedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (b *boundedBuffer) Write(value []byte) (int, error) {
	if remaining := b.limit - b.buffer.Len(); remaining > 0 {
		_, _ = b.buffer.Write(value[:min(len(value), remaining)])
	}
	return len(value), nil
}

type publicWebProxy struct {
	listener net.Listener
	server   *http.Server
}

func startPublicWebProxy() (*publicWebProxy, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	proxy := &publicWebProxy{listener: listener}
	proxy.server = &http.Server{
		Handler:           http.HandlerFunc(proxy.serveHTTP),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
	go func() {
		_ = proxy.server.Serve(listener)
	}()
	return proxy, nil
}

func (p *publicWebProxy) Address() string {
	if p == nil || p.listener == nil {
		return ""
	}
	return p.listener.Addr().String()
}

func (p *publicWebProxy) Close() {
	if p == nil {
		return
	}
	if p.server != nil {
		_ = p.server.Close()
	}
	if p.listener != nil {
		_ = p.listener.Close()
	}
}

func (p *publicWebProxy) serveHTTP(
	writer http.ResponseWriter,
	request *http.Request,
) {
	if request.Method == http.MethodConnect {
		p.connect(writer, request)
		return
	}
	if request.Method != http.MethodGet &&
		request.Method != http.MethodHead &&
		request.Method != http.MethodOptions {
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if request.URL == nil || request.URL.Scheme != "http" {
		http.Error(writer, "plain HTTP proxy request required", http.StatusBadRequest)
		return
	}
	outbound := request.Clone(request.Context())
	outbound.RequestURI = ""
	removeProxyHeaders(outbound.Header)
	transport := restrictedTransport()
	response, err := transport.RoundTrip(outbound)
	if err != nil {
		http.Error(writer, "upstream unavailable", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	removeProxyHeaders(response.Header)
	for key, values := range response.Header {
		for _, value := range values {
			writer.Header().Add(key, value)
		}
	}
	writer.WriteHeader(response.StatusCode)
	_, _ = io.Copy(writer, response.Body)
}

func (p *publicWebProxy) connect(
	writer http.ResponseWriter,
	request *http.Request,
) {
	upstream, err := dialPublicAddress(
		request.Context(),
		"tcp",
		request.Host,
		"443",
	)
	if err != nil {
		http.Error(writer, "upstream unavailable", http.StatusBadGateway)
		return
	}
	hijacker, ok := writer.(http.Hijacker)
	if !ok {
		_ = upstream.Close()
		http.Error(writer, "proxy tunnel unavailable", http.StatusInternalServerError)
		return
	}
	client, buffered, err := hijacker.Hijack()
	if err != nil {
		_ = upstream.Close()
		return
	}
	if _, err := buffered.WriteString(
		"HTTP/1.1 200 Connection Established\r\n\r\n",
	); err != nil {
		_ = client.Close()
		_ = upstream.Close()
		return
	}
	if err := buffered.Flush(); err != nil {
		_ = client.Close()
		_ = upstream.Close()
		return
	}
	go func() {
		defer client.Close()
		defer upstream.Close()
		finished := make(chan struct{}, 1)
		go func() {
			_, _ = io.Copy(upstream, client)
			finished <- struct{}{}
		}()
		_, _ = io.Copy(client, upstream)
		<-finished
	}()
}

func newRestrictedHTTPClient(
	timeout time.Duration,
	validateRedirect func(*url.URL) error,
) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: restrictedTransport(),
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("redirected too many times")
			}
			if validateRedirect != nil {
				return validateRedirect(request.URL)
			}
			return nil
		},
	}
}

func restrictedTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(
		ctx context.Context,
		network, address string,
	) (net.Conn, error) {
		return dialPublicAddress(ctx, network, address, "443")
	}
	return transport
}

func dialPublicAddress(
	ctx context.Context,
	network, address, defaultPort string,
) (net.Conn, error) {
	host, port, err := splitNetworkAddress(address, defaultPort)
	if err != nil {
		return nil, err
	}
	if port != "80" && port != "443" {
		return nil, errors.New("web screenshot network port is not allowed")
	}
	addresses, err := resolvePublicAddresses(ctx, host)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	var lastErr error
	for _, current := range addresses {
		connection, dialErr := dialer.DialContext(
			ctx,
			network,
			net.JoinHostPort(current.String(), port),
		)
		if dialErr == nil {
			return connection, nil
		}
		lastErr = dialErr
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("web screenshot host has no public address")
}

func splitNetworkAddress(address, defaultPort string) (string, string, error) {
	address = strings.TrimSpace(address)
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		if strings.Contains(address, ":") {
			return "", "", errors.New("invalid web screenshot network address")
		}
		host = address
		port = defaultPort
	}
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" || port == "" {
		return "", "", errors.New("invalid web screenshot network address")
	}
	return host, port, nil
}

func resolvePublicAddresses(
	ctx context.Context,
	host string,
) ([]netip.Addr, error) {
	if address, err := netip.ParseAddr(host); err == nil {
		address = address.Unmap()
		if !isPublicAddress(address) {
			return nil, errors.New("web screenshot network address is not public")
		}
		return []netip.Addr{address}, nil
	}
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addresses) == 0 {
		return nil, errors.New("resolve web screenshot host")
	}
	result := make([]netip.Addr, 0, len(addresses))
	for _, address := range addresses {
		address = address.Unmap()
		if !isPublicAddress(address) {
			return nil, errors.New("web screenshot host resolved to a non-public address")
		}
		result = append(result, address)
	}
	return result, nil
}

func isPublicAddress(address netip.Addr) bool {
	if !address.IsValid() || !address.IsGlobalUnicast() ||
		address.IsPrivate() || address.IsLoopback() ||
		address.IsLinkLocalUnicast() || address.IsMulticast() ||
		address.IsUnspecified() {
		return false
	}
	for _, prefix := range nonPublicAddressPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

var nonPublicAddressPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2001:10::/28"),
}

func removeProxyHeaders(headers http.Header) {
	for _, key := range []string{
		"Connection",
		"Proxy-Connection",
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"Keep-Alive",
		"TE",
		"Trailer",
		"Transfer-Encoding",
		"Upgrade",
	} {
		headers.Del(key)
	}
}
