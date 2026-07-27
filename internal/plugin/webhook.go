package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/platform"
)

const (
	maxWebhookConfigBytes   = 512 << 10
	maxWebhookRequestBytes  = 5 << 20
	maxWebhookResponseBytes = 1 << 20
	defaultWebhookTimeout   = 5 * time.Second
	maxWebhookTimeout       = 2 * time.Minute

	HookBeforeMessage = "before_message"
	HookAfterMessage  = "after_message"
	HookEvent         = "event"
)

var (
	webhookNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
	webhookEnvPattern  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

type WebhookConfig struct {
	Name     string
	URL      string
	Enabled  bool
	Hooks    map[string]struct{}
	Headers  map[string]string
	Timeout  time.Duration
	FailOpen bool
}

type rawWebhookFile struct {
	Plugins []rawWebhookConfig `json:"plugins"`
}

type rawWebhookConfig struct {
	Name      string            `json:"name"`
	URL       string            `json:"url"`
	Enabled   *bool             `json:"enabled"`
	Active    *bool             `json:"active"`
	Hooks     []string          `json:"hooks"`
	Headers   map[string]string `json:"headers"`
	HeaderEnv map[string]string `json:"header_env"`
	Timeout   string            `json:"timeout"`
	FailOpen  *bool             `json:"fail_open"`
}

type RuntimeInfo struct {
	Name         string    `json:"name"`
	Enabled      bool      `json:"enabled"`
	Hooks        []string  `json:"hooks,omitempty"`
	FailOpen     bool      `json:"fail_open"`
	Calls        uint64    `json:"calls"`
	Failures     uint64    `json:"failures"`
	LastError    string    `json:"last_error,omitempty"`
	LastCalledAt time.Time `json:"last_called_at,omitempty"`
}

type managedWebhook struct {
	config WebhookConfig
	plugin *WebhookPlugin
}

// WebhookManager owns config-defined HTTP plugins and installs them into the
// common plugin registry with rollback on a failed reload.
type WebhookManager struct {
	path     string
	registry *Registry
	logger   *slog.Logger

	mu       sync.RWMutex
	plugins  map[string]managedWebhook
	reloadMu sync.Mutex
}

type WebhookPlugin struct {
	config WebhookConfig
	logger *slog.Logger
	client *http.Client

	statusMu     sync.Mutex
	calls        uint64
	failures     uint64
	lastError    string
	lastCalledAt time.Time
}

type webhookRequest struct {
	Version   string         `json:"version"`
	Plugin    string         `json:"plugin"`
	Hook      string         `json:"hook"`
	Event     platform.Event `json:"event"`
	SessionID string         `json:"session_id,omitempty"`
	Text      string         `json:"text,omitempty"`
	Reply     string         `json:"reply,omitempty"`
}

type webhookResponse struct {
	Handled bool    `json:"handled"`
	Ignore  bool    `json:"ignore"`
	Reply   *string `json:"reply"`
	Handoff bool    `json:"handoff"`
}

func LoadWebhookFile(path string) ([]WebhookConfig, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("plugin webhook config path is empty")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open plugin webhook config: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxWebhookConfigBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read plugin webhook config: %w", err)
	}
	if len(data) > maxWebhookConfigBytes {
		return nil, fmt.Errorf("plugin webhook config exceeds %d bytes", maxWebhookConfigBytes)
	}
	var decoded rawWebhookFile
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, fmt.Errorf("decode plugin webhook config: %w", err)
	}
	if len(decoded.Plugins) > 128 {
		return nil, errors.New("plugin webhook config contains more than 128 plugins")
	}
	result := make([]WebhookConfig, 0, len(decoded.Plugins))
	seen := make(map[string]struct{}, len(decoded.Plugins))
	for _, raw := range decoded.Plugins {
		config, err := resolveWebhookConfig(raw)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[config.Name]; exists {
			return nil, fmt.Errorf("plugin webhook %q is duplicated", config.Name)
		}
		seen[config.Name] = struct{}{}
		result = append(result, config)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func OpenWebhookManager(path string, registry *Registry, logger *slog.Logger) (*WebhookManager, error) {
	configs, err := LoadWebhookFile(path)
	if err != nil {
		return nil, err
	}
	manager, err := NewWebhookManager(configs, registry, logger)
	if err != nil {
		return nil, err
	}
	manager.path = strings.TrimSpace(path)
	return manager, nil
}

func NewWebhookManager(configs []WebhookConfig, registry *Registry, logger *slog.Logger) (*WebhookManager, error) {
	if registry == nil {
		return nil, errors.New("plugin webhook manager requires a registry")
	}
	if logger == nil {
		logger = slog.Default()
	}
	manager := &WebhookManager{
		registry: registry,
		logger:   logger,
		plugins:  make(map[string]managedWebhook),
	}
	if err := manager.install(configs); err != nil {
		return nil, err
	}
	return manager, nil
}

func (m *WebhookManager) Reload(ctx context.Context) error {
	m.reloadMu.Lock()
	defer m.reloadMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.path == "" {
		return errors.New("plugin webhook config path is empty")
	}
	configs, err := LoadWebhookFile(m.path)
	if err != nil {
		return err
	}
	return m.install(configs)
}

func (m *WebhookManager) List() []RuntimeInfo {
	m.mu.RLock()
	names := make([]string, 0, len(m.plugins))
	for name := range m.plugins {
		names = append(names, name)
	}
	sort.Strings(names)
	plugins := make([]managedWebhook, 0, len(names))
	for _, name := range names {
		plugins = append(plugins, m.plugins[name])
	}
	m.mu.RUnlock()

	result := make([]RuntimeInfo, 0, len(plugins))
	for _, current := range plugins {
		hooks := make([]string, 0, len(current.config.Hooks))
		for hook := range current.config.Hooks {
			hooks = append(hooks, hook)
		}
		sort.Strings(hooks)
		info := RuntimeInfo{
			Name:     current.config.Name,
			Enabled:  current.config.Enabled,
			Hooks:    hooks,
			FailOpen: current.config.FailOpen,
		}
		if current.plugin != nil {
			info.Calls, info.Failures, info.LastError, info.LastCalledAt = current.plugin.status()
		}
		result = append(result, info)
	}
	return result
}

func (m *WebhookManager) install(configs []WebhookConfig) error {
	candidates := make(map[string]managedWebhook, len(configs))
	for _, config := range configs {
		if _, exists := candidates[config.Name]; exists {
			return fmt.Errorf("plugin webhook %q is duplicated", config.Name)
		}
		current := managedWebhook{config: config}
		if config.Enabled {
			current.plugin = &WebhookPlugin{
				config: config,
				logger: m.logger.With("plugin", config.Name),
				client: &http.Client{},
			}
		}
		candidates[config.Name] = current
	}

	m.mu.RLock()
	old := make(map[string]managedWebhook, len(m.plugins))
	oldEnabled := make(map[string]struct{}, len(m.plugins))
	for name, current := range m.plugins {
		old[name] = current
		if current.plugin != nil {
			oldEnabled[name] = struct{}{}
		}
	}
	m.mu.RUnlock()
	for name, current := range candidates {
		if current.plugin == nil || !m.registry.Has(name) {
			continue
		}
		if _, replacing := oldEnabled[name]; !replacing {
			return fmt.Errorf("plugin name %q is already registered", name)
		}
	}
	for name := range oldEnabled {
		m.registry.Remove(name)
	}
	installed := make([]string, 0, len(candidates))
	for name, current := range candidates {
		if current.plugin == nil {
			continue
		}
		if err := m.registry.Register(current.plugin); err != nil {
			for _, installedName := range installed {
				m.registry.Remove(installedName)
			}
			for _, oldPlugin := range old {
				if oldPlugin.plugin != nil {
					_ = m.registry.Register(oldPlugin.plugin)
				}
			}
			return err
		}
		installed = append(installed, name)
	}
	m.mu.Lock()
	m.plugins = candidates
	m.mu.Unlock()
	return nil
}

func (p *WebhookPlugin) Name() string {
	return p.config.Name
}

func (p *WebhookPlugin) BeforeMessage(ctx context.Context, event *MessageContext) (Decision, error) {
	if !p.enabled(HookBeforeMessage) {
		return Decision{}, nil
	}
	response, err := p.invoke(ctx, webhookRequest{
		Version:   "1",
		Plugin:    p.config.Name,
		Hook:      HookBeforeMessage,
		Event:     event.Event,
		SessionID: event.SessionID,
		Text:      event.Text,
	})
	if err != nil {
		if p.config.FailOpen {
			p.warn("plugin webhook before hook failed", "error", err)
			return Decision{}, nil
		}
		return Decision{}, err
	}
	reply := ""
	if response.Reply != nil {
		reply = *response.Reply
	}
	return Decision{
		Handled: response.Handled,
		Ignore:  response.Ignore,
		Reply:   reply,
		Handoff: response.Handoff,
	}, nil
}

func (p *WebhookPlugin) AfterMessage(ctx context.Context, event *MessageContext) error {
	if !p.enabled(HookAfterMessage) {
		return nil
	}
	response, err := p.invoke(ctx, webhookRequest{
		Version:   "1",
		Plugin:    p.config.Name,
		Hook:      HookAfterMessage,
		Event:     event.Event,
		SessionID: event.SessionID,
		Text:      event.Text,
		Reply:     event.Reply,
	})
	if err != nil {
		if p.config.FailOpen {
			p.warn("plugin webhook after hook failed", "error", err)
			return nil
		}
		return err
	}
	if response.Reply != nil {
		event.Reply = *response.Reply
	}
	return nil
}

func (p *WebhookPlugin) OnEvent(ctx context.Context, event platform.Event) error {
	if !p.enabled(HookEvent) {
		return nil
	}
	_, err := p.invoke(ctx, webhookRequest{
		Version: "1",
		Plugin:  p.config.Name,
		Hook:    HookEvent,
		Event:   event,
	})
	if err != nil && p.config.FailOpen {
		p.warn("plugin webhook event hook failed", "error", err)
		return nil
	}
	return err
}

func (p *WebhookPlugin) enabled(hook string) bool {
	_, ok := p.config.Hooks[hook]
	return p.config.Enabled && ok
}

func (p *WebhookPlugin) invoke(parent context.Context, payload webhookRequest) (webhookResponse, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		p.record(errors.New("plugin webhook request cannot be encoded"))
		return webhookResponse{}, errors.New("plugin webhook request cannot be encoded")
	}
	if len(encoded) > maxWebhookRequestBytes {
		err := fmt.Errorf("plugin webhook request exceeds %d bytes", maxWebhookRequestBytes)
		p.record(err)
		return webhookResponse{}, err
	}
	ctx, cancel := context.WithTimeout(parent, p.config.Timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.config.URL, bytes.NewReader(encoded))
	if err != nil {
		err = errors.New("plugin webhook request cannot be built")
		p.record(err)
		return webhookResponse{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	for name, value := range p.config.Headers {
		request.Header.Set(name, value)
	}
	client := p.client
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = errors.New("plugin webhook request timed out")
		} else if errors.Is(ctx.Err(), context.Canceled) {
			err = errors.New("plugin webhook request canceled")
		} else {
			err = errors.New("plugin webhook network request failed")
		}
		p.record(err)
		return webhookResponse{}, err
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, maxWebhookResponseBytes+1))
	if readErr != nil {
		err = errors.New("plugin webhook response cannot be read")
		p.record(err)
		return webhookResponse{}, err
	}
	if len(body) > maxWebhookResponseBytes {
		err = fmt.Errorf("plugin webhook response exceeds %d bytes", maxWebhookResponseBytes)
		p.record(err)
		return webhookResponse{}, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		err = fmt.Errorf("plugin webhook returned HTTP %d", response.StatusCode)
		p.record(err)
		return webhookResponse{}, err
	}
	if response.StatusCode == http.StatusNoContent || len(bytes.TrimSpace(body)) == 0 {
		p.record(nil)
		return webhookResponse{}, nil
	}
	var decoded webhookResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		err = errors.New("plugin webhook returned invalid JSON")
		p.record(err)
		return webhookResponse{}, err
	}
	p.record(nil)
	return decoded, nil
}

func (p *WebhookPlugin) record(err error) {
	p.statusMu.Lock()
	defer p.statusMu.Unlock()
	p.calls++
	p.lastCalledAt = time.Now()
	p.lastError = ""
	if err != nil {
		p.failures++
		p.lastError = err.Error()
	}
}

func (p *WebhookPlugin) status() (uint64, uint64, string, time.Time) {
	p.statusMu.Lock()
	defer p.statusMu.Unlock()
	return p.calls, p.failures, p.lastError, p.lastCalledAt
}

func (p *WebhookPlugin) warn(message string, args ...any) {
	logger := p.logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.Warn(message, args...)
}

func resolveWebhookConfig(raw rawWebhookConfig) (WebhookConfig, error) {
	name := strings.TrimSpace(raw.Name)
	if !webhookNamePattern.MatchString(name) {
		return WebhookConfig{}, fmt.Errorf("plugin webhook name %q is invalid", name)
	}
	enabled := true
	if raw.Enabled != nil {
		enabled = *raw.Enabled
	}
	if raw.Active != nil {
		if raw.Enabled != nil && *raw.Enabled != *raw.Active {
			return WebhookConfig{}, fmt.Errorf("plugin webhook %q sets conflicting enabled and active values", name)
		}
		enabled = *raw.Active
	}
	endpoint := strings.TrimSpace(raw.URL)
	if enabled {
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return WebhookConfig{}, fmt.Errorf("plugin webhook %q url must be an absolute HTTP(S) URL", name)
		}
		if parsed.User != nil {
			return WebhookConfig{}, fmt.Errorf("plugin webhook %q url userinfo is not allowed", name)
		}
		if parsed.Fragment != "" {
			return WebhookConfig{}, fmt.Errorf("plugin webhook %q url fragment is not allowed", name)
		}
	}
	hooks, err := resolveWebhookHooks(raw.Hooks)
	if err != nil {
		return WebhookConfig{}, fmt.Errorf("plugin webhook %q: %w", name, err)
	}
	headers, err := resolveWebhookHeaders(raw.Headers, raw.HeaderEnv, enabled)
	if err != nil {
		return WebhookConfig{}, fmt.Errorf("plugin webhook %q: %w", name, err)
	}
	timeout := defaultWebhookTimeout
	if value := strings.TrimSpace(raw.Timeout); value != "" {
		timeout, err = time.ParseDuration(value)
		if err != nil || timeout <= 0 || timeout > maxWebhookTimeout {
			return WebhookConfig{}, fmt.Errorf("plugin webhook %q timeout must be between 1ns and %s", name, maxWebhookTimeout)
		}
	}
	failOpen := true
	if raw.FailOpen != nil {
		failOpen = *raw.FailOpen
	}
	return WebhookConfig{
		Name:     name,
		URL:      endpoint,
		Enabled:  enabled,
		Hooks:    hooks,
		Headers:  headers,
		Timeout:  timeout,
		FailOpen: failOpen,
	}, nil
}

func resolveWebhookHooks(values []string) (map[string]struct{}, error) {
	if len(values) == 0 {
		values = []string{HookBeforeMessage, HookAfterMessage}
	}
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "before", HookBeforeMessage:
			result[HookBeforeMessage] = struct{}{}
		case "after", HookAfterMessage:
			result[HookAfterMessage] = struct{}{}
		case HookEvent, "on_event":
			result[HookEvent] = struct{}{}
		default:
			return nil, fmt.Errorf("unsupported hook %q", value)
		}
	}
	return result, nil
}

func resolveWebhookHeaders(static, fromEnv map[string]string, enabled bool) (map[string]string, error) {
	result := make(map[string]string, len(static)+len(fromEnv))
	for name, value := range static {
		canonical, err := validateWebhookHeader(name, value)
		if err != nil {
			return nil, err
		}
		result[canonical] = value
	}
	for name, envName := range fromEnv {
		envName = strings.TrimSpace(envName)
		if !webhookEnvPattern.MatchString(envName) {
			return nil, fmt.Errorf("header %q references invalid environment variable %q", name, envName)
		}
		value := ""
		if enabled {
			var ok bool
			value, ok = os.LookupEnv(envName)
			if !ok || value == "" {
				return nil, fmt.Errorf("environment variable %q for header %q is not set", envName, name)
			}
		}
		canonical, err := validateWebhookHeader(name, value)
		if err != nil {
			return nil, err
		}
		result[canonical] = value
	}
	return result, nil
}

func validateWebhookHeader(name, value string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("header name is empty")
	}
	if strings.ContainsAny(name, "\r\n: \t") {
		return "", fmt.Errorf("header name %q is invalid", name)
	}
	switch strings.ToLower(name) {
	case "content-type", "content-length", "accept", "host":
		return "", fmt.Errorf("header %q is reserved", name)
	}
	if strings.ContainsAny(value, "\r\n\x00") {
		return "", fmt.Errorf("header %q contains control characters", name)
	}
	return http.CanonicalHeaderKey(name), nil
}
