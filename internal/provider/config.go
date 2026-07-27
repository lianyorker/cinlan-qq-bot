package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/agent"
)

const (
	maxProviderConfigBytes = 512 << 10
	maxProviderTimeout     = 10 * time.Minute
)

var providerEnvPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type Config struct {
	Name    string
	Kind    string
	Enabled bool
	Agent   agent.Config
}

type rawProviderFile struct {
	DefaultProvider string              `json:"default_provider"`
	Providers       []rawProviderConfig `json:"providers"`
}

type rawProviderConfig struct {
	Name         string `json:"name"`
	Kind         string `json:"kind"`
	Mode         string `json:"mode"`
	URL          string `json:"url"`
	APIKeyEnv    string `json:"api_key_env"`
	Model        string `json:"model"`
	SystemPrompt string `json:"system_prompt"`
	AuthHeader   string `json:"auth_header"`
	AuthScheme   string `json:"auth_scheme"`
	Timeout      string `json:"timeout"`
	MaxRetries   *int   `json:"max_retries"`
	RetryBase    string `json:"retry_base"`
	RetryMax     string `json:"retry_max"`
	Enabled      *bool  `json:"enabled"`
	Active       *bool  `json:"active"`
}

func LoadFile(path string) ([]Config, string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, "", errors.New("provider config path is empty")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, "", fmt.Errorf("open provider config: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxProviderConfigBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("read provider config: %w", err)
	}
	if len(data) > maxProviderConfigBytes {
		return nil, "", fmt.Errorf("provider config exceeds %d bytes", maxProviderConfigBytes)
	}
	var decoded rawProviderFile
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, "", fmt.Errorf("decode provider config: %w", err)
	}
	if len(decoded.Providers) == 0 {
		return nil, "", errors.New("provider config contains no providers")
	}
	if len(decoded.Providers) > 32 {
		return nil, "", errors.New("provider config contains more than 32 providers")
	}
	configs := make([]Config, 0, len(decoded.Providers))
	seen := make(map[string]struct{}, len(decoded.Providers))
	for _, raw := range decoded.Providers {
		config, err := resolveProviderConfig(raw)
		if err != nil {
			return nil, "", err
		}
		if _, exists := seen[config.Name]; exists {
			return nil, "", fmt.Errorf("provider %q is duplicated", config.Name)
		}
		seen[config.Name] = struct{}{}
		configs = append(configs, config)
	}
	sort.Slice(configs, func(i, j int) bool { return configs[i].Name < configs[j].Name })

	defaultName := strings.TrimSpace(decoded.DefaultProvider)
	if defaultName == "" {
		for _, config := range configs {
			if config.Enabled {
				defaultName = config.Name
				break
			}
		}
	}
	found := false
	for _, config := range configs {
		if config.Name == defaultName && config.Enabled {
			found = true
			break
		}
	}
	if !found {
		return nil, "", fmt.Errorf("default provider %q is not enabled", defaultName)
	}
	return configs, defaultName, nil
}

func resolveProviderConfig(raw rawProviderConfig) (Config, error) {
	name := strings.TrimSpace(raw.Name)
	if name == "" || len(name) > 64 {
		return Config{}, fmt.Errorf("provider name %q is invalid", name)
	}
	for _, current := range name {
		if !((current >= 'a' && current <= 'z') ||
			(current >= 'A' && current <= 'Z') ||
			(current >= '0' && current <= '9') ||
			current == '_' || current == '-' || current == '.') {
			return Config{}, fmt.Errorf("provider name %q is invalid", name)
		}
	}
	enabled := true
	if raw.Enabled != nil {
		enabled = *raw.Enabled
	}
	if raw.Active != nil {
		if raw.Enabled != nil && *raw.Enabled != *raw.Active {
			return Config{}, fmt.Errorf("provider %q sets conflicting enabled and active values", name)
		}
		enabled = *raw.Active
	}
	mode := strings.ToLower(strings.TrimSpace(raw.Mode))
	if mode == "" {
		mode = "openai"
	}
	if mode != "openai" && mode != "custom" {
		return Config{}, fmt.Errorf("provider %q has unsupported mode %q", name, raw.Mode)
	}
	endpoint := strings.TrimSpace(raw.URL)
	if enabled {
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return Config{}, fmt.Errorf("provider %q url must be an absolute HTTP(S) URL", name)
		}
		if parsed.User != nil {
			return Config{}, fmt.Errorf("provider %q url userinfo is not allowed", name)
		}
	}
	model := strings.TrimSpace(raw.Model)
	if enabled && mode == "openai" && model == "" {
		return Config{}, fmt.Errorf("provider %q requires model in openai mode", name)
	}
	apiKey := ""
	envName := strings.TrimSpace(raw.APIKeyEnv)
	if enabled && envName != "" {
		if !providerEnvPattern.MatchString(envName) {
			return Config{}, fmt.Errorf("provider %q has invalid api_key_env %q", name, envName)
		}
		var ok bool
		apiKey, ok = os.LookupEnv(envName)
		if !ok || apiKey == "" {
			return Config{}, fmt.Errorf("provider %q API key environment variable %q is not set", name, envName)
		}
	}
	timeout, err := parseProviderDuration(raw.Timeout, 30*time.Second, "timeout")
	if err != nil {
		return Config{}, fmt.Errorf("provider %q: %w", name, err)
	}
	retryBase, err := parseProviderDuration(raw.RetryBase, 500*time.Millisecond, "retry_base")
	if err != nil {
		return Config{}, fmt.Errorf("provider %q: %w", name, err)
	}
	retryMax, err := parseProviderDuration(raw.RetryMax, 5*time.Second, "retry_max")
	if err != nil {
		return Config{}, fmt.Errorf("provider %q: %w", name, err)
	}
	if retryMax < retryBase {
		return Config{}, fmt.Errorf("provider %q retry_max must be greater than or equal to retry_base", name)
	}
	maxRetries := 3
	if raw.MaxRetries != nil {
		maxRetries = *raw.MaxRetries
		if maxRetries < 0 || maxRetries > 10 {
			return Config{}, fmt.Errorf("provider %q max_retries must be between 0 and 10", name)
		}
	}
	authHeader := strings.TrimSpace(raw.AuthHeader)
	if authHeader == "" {
		authHeader = "Authorization"
	}
	if strings.ContainsAny(authHeader, "\r\n: \t") {
		return Config{}, fmt.Errorf("provider %q auth_header is invalid", name)
	}
	authScheme := strings.TrimSpace(raw.AuthScheme)
	if authScheme == "" {
		authScheme = "Bearer"
	}
	kind := strings.TrimSpace(raw.Kind)
	if kind == "" {
		kind = mode
	}
	return Config{
		Name:    name,
		Kind:    kind,
		Enabled: enabled,
		Agent: agent.Config{
			Mode:         mode,
			URL:          endpoint,
			APIKey:       apiKey,
			Model:        model,
			SystemPrompt: strings.TrimSpace(raw.SystemPrompt),
			AuthHeader:   authHeader,
			AuthScheme:   authScheme,
			Timeout:      timeout,
			MaxRetries:   maxRetries,
			RetryBase:    retryBase,
			RetryMax:     retryMax,
		},
	}, nil
}

func parseProviderDuration(value string, fallback time.Duration, field string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 || parsed > maxProviderTimeout {
		return 0, fmt.Errorf("%s must be between 1ns and %s", field, maxProviderTimeout)
	}
	return parsed, nil
}
