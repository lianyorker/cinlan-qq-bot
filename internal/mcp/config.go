package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/tool"
)

const (
	maxConfigBytes     = 1 << 20
	defaultRPCTimeout  = 10 * time.Second
	defaultToolTimeout = 30 * time.Second
	maxRPCTimeout      = 5 * time.Minute
	maxToolTimeout     = 10 * time.Minute
)

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ServerConfig is the resolved configuration for one Streamable HTTP MCP
// server. HeaderEnv is resolved by LoadFile and is kept only for diagnostics
// and programmatic construction; it is never returned by the admin API.
type ServerConfig struct {
	Name        string
	URL         string
	Active      bool
	Headers     map[string]string
	HeaderEnv   map[string]string
	Timeout     time.Duration
	ToolTimeout time.Duration
	Permission  tool.Permission
	AllowTools  map[string]struct{}
}

type fileConfig struct {
	MCPServers map[string]rawServerConfig `json:"mcpServers"`
}

type rawServerConfig struct {
	URL         string            `json:"url"`
	Type        string            `json:"type"`
	Transport   string            `json:"transport"`
	Command     string            `json:"command"`
	Active      *bool             `json:"active"`
	Enabled     *bool             `json:"enabled"`
	Headers     map[string]string `json:"headers"`
	HeaderEnv   map[string]string `json:"header_env"`
	Timeout     string            `json:"timeout"`
	ToolTimeout string            `json:"tool_timeout"`
	Permission  tool.Permission   `json:"permission"`
	AllowTools  []string          `json:"allow_tools"`
}

// LoadFile reads the common mcpServers JSON shape used by MCP clients. The
// function resolves only explicit environment references from header_env;
// arbitrary shell expansion is intentionally not supported.
func LoadFile(path string) ([]ServerConfig, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("MCP server config path is empty")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open MCP server config: %w", err)
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxConfigBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read MCP server config: %w", err)
	}
	if len(data) > maxConfigBytes {
		return nil, fmt.Errorf("MCP server config exceeds %d bytes", maxConfigBytes)
	}
	var decoded fileConfig
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, fmt.Errorf("decode MCP server config: %w", err)
	}
	if len(decoded.MCPServers) == 0 {
		return nil, nil
	}

	names := make([]string, 0, len(decoded.MCPServers))
	for name := range decoded.MCPServers {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]ServerConfig, 0, len(names))
	for _, name := range names {
		config, err := resolveServerConfig(name, decoded.MCPServers[name])
		if err != nil {
			return nil, err
		}
		result = append(result, config)
	}
	return result, nil
}

func resolveServerConfig(name string, raw rawServerConfig) (ServerConfig, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return ServerConfig{}, errors.New("MCP server name is empty")
	}
	if strings.TrimSpace(raw.Command) != "" {
		return ServerConfig{}, fmt.Errorf("MCP server %q uses stdio command; only Streamable HTTP is supported in this batch", name)
	}
	transport := strings.ToLower(strings.TrimSpace(raw.Transport))
	if transport == "" {
		transport = strings.ToLower(strings.TrimSpace(raw.Type))
	}
	switch transport {
	case "", "http", "streamable_http", "streamable-http", "streamablehttp":
	default:
		return ServerConfig{}, fmt.Errorf("MCP server %q has unsupported transport %q; use streamable_http", name, transport)
	}

	endpoint, err := parseEndpoint(raw.URL)
	if err != nil {
		return ServerConfig{}, fmt.Errorf("MCP server %q: %w", name, err)
	}
	headers, err := resolveHeaders(raw.Headers, raw.HeaderEnv)
	if err != nil {
		return ServerConfig{}, fmt.Errorf("MCP server %q: %w", name, err)
	}
	timeout, err := parseTimeout(raw.Timeout, defaultRPCTimeout, maxRPCTimeout, "timeout")
	if err != nil {
		return ServerConfig{}, fmt.Errorf("MCP server %q: %w", name, err)
	}
	toolTimeout, err := parseTimeout(raw.ToolTimeout, defaultToolTimeout, maxToolTimeout, "tool_timeout")
	if err != nil {
		return ServerConfig{}, fmt.Errorf("MCP server %q: %w", name, err)
	}

	active := true
	if raw.Active != nil {
		active = *raw.Active
	}
	if raw.Enabled != nil {
		if raw.Active != nil && *raw.Active != *raw.Enabled {
			return ServerConfig{}, fmt.Errorf("MCP server %q sets conflicting active and enabled values", name)
		}
		active = *raw.Enabled
	}
	permission := raw.Permission
	if permission == "" {
		// Remote tools are powerful by default. A deployment must explicitly
		// opt into exposing them to ordinary group members.
		permission = tool.PermissionAdmin
	}
	if permission != tool.PermissionAdmin && permission != tool.PermissionEveryone {
		return ServerConfig{}, fmt.Errorf("MCP server %q has unsupported permission %q", name, permission)
	}
	allowTools := make(map[string]struct{}, len(raw.AllowTools))
	for _, item := range raw.AllowTools {
		item = strings.TrimSpace(item)
		if item != "" {
			allowTools[item] = struct{}{}
		}
	}

	return ServerConfig{
		Name:        name,
		URL:         endpoint,
		Active:      active,
		Headers:     headers,
		HeaderEnv:   cloneStringMap(raw.HeaderEnv),
		Timeout:     timeout,
		ToolTimeout: toolTimeout,
		Permission:  permission,
		AllowTools:  allowTools,
	}, nil
}

func parseEndpoint(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("url is required")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("url must be an absolute HTTP(S) URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("url scheme %q is not supported", parsed.Scheme)
	}
	if parsed.User != nil {
		return "", errors.New("url userinfo is not allowed")
	}
	if parsed.Fragment != "" {
		return "", errors.New("url fragment is not allowed")
	}
	return parsed.String(), nil
}

func resolveHeaders(static, fromEnv map[string]string) (map[string]string, error) {
	result := make(map[string]string, len(static)+len(fromEnv))
	for name, value := range static {
		if err := validateHeader(name, value); err != nil {
			return nil, err
		}
		result[http.CanonicalHeaderKey(strings.TrimSpace(name))] = value
	}
	for name, envName := range fromEnv {
		name = strings.TrimSpace(name)
		envName = strings.TrimSpace(envName)
		if err := validateHeaderName(name); err != nil {
			return nil, err
		}
		if isReservedHeader(name) {
			return nil, fmt.Errorf("header %q is reserved", name)
		}
		if !envNamePattern.MatchString(envName) {
			return nil, fmt.Errorf("header %q references invalid environment variable %q", name, envName)
		}
		value, ok := os.LookupEnv(envName)
		if !ok || value == "" {
			return nil, fmt.Errorf("environment variable %q for header %q is not set", envName, name)
		}
		if err := validateHeader(name, value); err != nil {
			return nil, err
		}
		result[http.CanonicalHeaderKey(name)] = value
	}
	return result, nil
}

func validateHeader(name, value string) error {
	if err := validateHeaderName(name); err != nil {
		return err
	}
	if isReservedHeader(name) {
		return fmt.Errorf("header %q is reserved", name)
	}
	if strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("header %q contains control characters", name)
	}
	return nil
}

func validateHeaderName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("header name is empty")
	}
	for _, current := range name {
		if current <= 0x20 || current >= 0x7f {
			return fmt.Errorf("header name %q contains invalid characters", name)
		}
		if !strings.ContainsRune("!#$%&'*+-.^_`|~0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ", current) {
			return fmt.Errorf("header name %q contains invalid characters", name)
		}
	}
	return nil
}

func isReservedHeader(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "accept", "content-type", "content-length", "host", "mcp-session-id", "mcp-protocol-version":
		return true
	default:
		return false
	}
}

func parseTimeout(value string, fallback, maximum time.Duration, field string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 || parsed > maximum {
		return 0, fmt.Errorf("%s must be a duration between 1ns and %s", field, maximum)
	}
	return parsed, nil
}

func cloneStringMap(source map[string]string) map[string]string {
	if len(source) == 0 {
		return nil
	}
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
