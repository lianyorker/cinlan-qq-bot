package mcp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/tool"
)

type managedServer struct {
	config ServerConfig
	client *Client

	enabled       bool
	state         string
	lastError     string
	serverName    string
	serverVersion string
	toolNames     []string
	definitions   []tool.Definition
	refreshedAt   time.Time
}

// ServerInfo is the safe, non-secret status exposed through the admin API.
type ServerInfo struct {
	Name          string    `json:"name"`
	Enabled       bool      `json:"enabled"`
	State         string    `json:"state"`
	Protocol      string    `json:"protocol,omitempty"`
	ServerName    string    `json:"server_name,omitempty"`
	ServerVersion string    `json:"server_version,omitempty"`
	ToolCount     int       `json:"tool_count"`
	Tools         []string  `json:"tools,omitempty"`
	LastError     string    `json:"last_error,omitempty"`
	RefreshedAt   time.Time `json:"refreshed_at,omitempty"`
}

// Manager discovers MCP tools and owns their lifecycle in a Tool Registry.
// A failed refresh keeps the last known-good definitions so a transient MCP
// outage does not silently remove customer-service capabilities.
type Manager struct {
	registry *tool.Registry
	logger   *slog.Logger

	mu        sync.RWMutex
	servers   map[string]*managedServer
	refreshMu sync.Mutex
}

func NewManager(configs []ServerConfig, registry *tool.Registry, logger *slog.Logger) (*Manager, error) {
	if registry == nil {
		return nil, errors.New("MCP manager requires a tool registry")
	}
	if logger == nil {
		logger = slog.Default()
	}
	manager := &Manager{
		registry: registry,
		logger:   logger,
		servers:  make(map[string]*managedServer, len(configs)),
	}
	for _, config := range configs {
		if strings.TrimSpace(config.Name) == "" {
			return nil, errors.New("MCP server name is empty")
		}
		if _, exists := manager.servers[config.Name]; exists {
			return nil, fmt.Errorf("MCP server %q is duplicated", config.Name)
		}
		if config.Timeout <= 0 {
			config.Timeout = defaultRPCTimeout
		}
		if config.ToolTimeout <= 0 {
			config.ToolTimeout = defaultToolTimeout
		}
		if config.Permission == "" {
			config.Permission = tool.PermissionAdmin
		}
		manager.servers[config.Name] = &managedServer{
			config:  config,
			client:  NewClient(config, slogLogger{logger: logger}),
			enabled: config.Active,
			state:   stateDisabled(config.Active),
		}
	}
	return manager, nil
}

// Open loads a manager from a mcpServers JSON file.
func Open(path string, registry *tool.Registry, logger *slog.Logger) (*Manager, error) {
	configs, err := LoadFile(path)
	if err != nil {
		return nil, err
	}
	return NewManager(configs, registry, logger)
}

func (m *Manager) Refresh(ctx context.Context) error {
	m.refreshMu.Lock()
	defer m.refreshMu.Unlock()

	m.mu.RLock()
	names := make([]string, 0, len(m.servers))
	for name := range m.servers {
		names = append(names, name)
	}
	sort.Strings(names)
	servers := make([]*managedServer, 0, len(names))
	for _, name := range names {
		servers = append(servers, m.servers[name])
	}
	m.mu.RUnlock()

	var failures []error
	for _, server := range servers {
		if !server.enabled {
			m.setFailure(server, nil)
			continue
		}
		if err := m.refreshServer(ctx, server); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", server.config.Name, err))
			m.logger.Warn("MCP server refresh failed", "server", server.config.Name, "error", err)
		}
	}
	return errors.Join(failures...)
}

func (m *Manager) refreshServer(parent context.Context, server *managedServer) error {
	if err := server.client.Initialize(parent); err != nil {
		m.setFailure(server, err)
		return err
	}
	tools, err := server.client.ListTools(parent)
	if err != nil {
		m.setFailure(server, err)
		return err
	}
	definitions, names, err := m.buildDefinitions(server, tools)
	if err != nil {
		m.setFailure(server, err)
		return err
	}
	if err := m.installDefinitions(server, definitions, names); err != nil {
		m.setFailure(server, err)
		return err
	}
	serverName, serverVersion, _ := server.client.ServerInfo()
	m.mu.Lock()
	server.state = "ready"
	server.lastError = ""
	server.serverName = serverName
	server.serverVersion = serverVersion
	server.refreshedAt = time.Now()
	m.mu.Unlock()
	return nil
}

func (m *Manager) buildDefinitions(server *managedServer, remoteTools []Tool) ([]tool.Definition, []string, error) {
	sort.Slice(remoteTools, func(i, j int) bool {
		return remoteTools[i].Name < remoteTools[j].Name
	})
	definitions := make([]tool.Definition, 0, len(remoteTools))
	names := make([]string, 0, len(remoteTools))
	seen := make(map[string]struct{}, len(remoteTools))
	for _, remote := range remoteTools {
		if len(server.config.AllowTools) > 0 {
			if _, allowed := server.config.AllowTools[remote.Name]; !allowed {
				continue
			}
		}
		publicName := publicToolName(server.config.Name, remote.Name)
		if _, exists := seen[publicName]; exists {
			publicName = fitToolName(publicName + "_" + hashSuffix(server.config.Name+"\x00"+remote.Name))
		}
		if _, exists := seen[publicName]; exists {
			return nil, nil, fmt.Errorf("MCP tool namespace collision for %q", remote.Name)
		}
		seen[publicName] = struct{}{}
		remoteName := remote.Name
		description := remote.Description
		if description == "" {
			description = "MCP remote tool"
		}
		description = fmt.Sprintf("[MCP %s] %s", server.config.Name, description)
		definitions = append(definitions, tool.Definition{
			Name:        publicName,
			Description: description,
			Parameters:  remote.InputSchema,
			Permission:  server.config.Permission,
			Timeout:     server.config.ToolTimeout,
			Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
				result, err := server.client.CallTool(ctx, remoteName, call.Arguments)
				if err != nil {
					return tool.Result{}, err
				}
				content := map[string]any{
					"content": result.Content,
				}
				if result.StructuredContent != nil {
					content["structured_content"] = result.StructuredContent
				}
				if result.IsError {
					return tool.Result{
						Content: content,
						Error:   toolResultError(result),
					}, nil
				}
				return tool.Result{Content: content}, nil
			},
		})
		names = append(names, publicName)
	}
	return definitions, names, nil
}

func (m *Manager) installDefinitions(server *managedServer, definitions []tool.Definition, names []string) error {
	m.mu.RLock()
	oldDefinitions := append([]tool.Definition(nil), server.definitions...)
	oldNames := append([]string(nil), server.toolNames...)
	m.mu.RUnlock()
	oldNameSet := make(map[string]struct{}, len(oldNames))
	for _, name := range oldNames {
		oldNameSet[name] = struct{}{}
	}
	for _, definition := range definitions {
		if _, isOld := oldNameSet[definition.Name]; !isOld && m.registry.Has(definition.Name) {
			return fmt.Errorf("tool name %q is already registered", definition.Name)
		}
	}
	for _, name := range oldNames {
		m.registry.Remove(name)
	}
	installed := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		if err := m.registry.Register(definition); err != nil {
			for _, name := range installed {
				m.registry.Remove(name)
			}
			for _, old := range oldDefinitions {
				_ = m.registry.Register(old)
			}
			return err
		}
		installed = append(installed, definition.Name)
	}
	m.mu.Lock()
	server.definitions = append([]tool.Definition(nil), definitions...)
	server.toolNames = append([]string(nil), names...)
	m.mu.Unlock()
	return nil
}

func (m *Manager) setFailure(server *managedServer, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !server.enabled {
		server.state = "disabled"
		server.lastError = ""
		return
	}
	server.lastError = ""
	if err != nil {
		server.lastError = err.Error()
	}
	if len(server.definitions) > 0 {
		server.state = "degraded"
	} else {
		server.state = "error"
	}
	server.refreshedAt = time.Now()
}

func (m *Manager) List() []ServerInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	names := make([]string, 0, len(m.servers))
	for name := range m.servers {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]ServerInfo, 0, len(names))
	for _, name := range names {
		server := m.servers[name]
		info := ServerInfo{
			Name:          server.config.Name,
			Enabled:       server.enabled,
			State:         server.state,
			Protocol:      server.client.ProtocolVersion(),
			ServerName:    server.serverName,
			ServerVersion: server.serverVersion,
			ToolCount:     len(server.toolNames),
			Tools:         append([]string(nil), server.toolNames...),
			LastError:     server.lastError,
			RefreshedAt:   server.refreshedAt,
		}
		result = append(result, info)
	}
	return result
}

func (m *Manager) Close(ctx context.Context) error {
	m.mu.RLock()
	servers := make([]*managedServer, 0, len(m.servers))
	for _, server := range m.servers {
		servers = append(servers, server)
	}
	m.mu.RUnlock()
	var failures []error
	for _, server := range servers {
		if err := server.client.Close(ctx); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", server.config.Name, err))
		}
	}
	return errors.Join(failures...)
}

func publicToolName(serverName, toolName string) string {
	serverName = sanitizeName(serverName)
	toolName = sanitizeName(toolName)
	return fitToolName("mcp_" + serverName + "_" + toolName)
}

func fitToolName(value string) string {
	if len([]rune(value)) <= 64 {
		return value
	}
	suffix := "_" + hashSuffix(value)
	runes := []rune(value)
	limit := 64 - len([]rune(suffix))
	return string(runes[:limit]) + suffix
}

func sanitizeName(value string) string {
	var builder strings.Builder
	for _, current := range value {
		if current >= 'a' && current <= 'z' ||
			current >= 'A' && current <= 'Z' ||
			current >= '0' && current <= '9' ||
			current == '_' || current == '-' {
			builder.WriteRune(current)
		} else {
			builder.WriteByte('_')
		}
	}
	if builder.Len() == 0 {
		return "tool"
	}
	return builder.String()
}

func stateDisabled(enabled bool) string {
	if enabled {
		return "pending"
	}
	return "disabled"
}

func toolResultError(result ToolResult) string {
	var texts []string
	for _, block := range result.Content {
		if value, ok := block["text"].(string); ok && strings.TrimSpace(value) != "" {
			texts = append(texts, strings.TrimSpace(value))
		}
	}
	if len(texts) == 0 {
		return "MCP tool returned an execution error"
	}
	return truncateRunes(strings.Join(texts, "\n"), 2048)
}

type slogLogger struct {
	logger *slog.Logger
}

func (l slogLogger) Debug(msg string, args ...any) {
	l.logger.Debug(msg, args...)
}

func (l slogLogger) Warn(msg string, args ...any) {
	l.logger.Warn(msg, args...)
}
