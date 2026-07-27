package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/agent"
	"github.com/lianyorker/cinlan-qq-bot/internal/domain"
	"github.com/lianyorker/cinlan-qq-bot/internal/tool"
)

const (
	maxTaskRunes = 8000
	maxToolName  = 64
)

type managed struct {
	config     Config
	client     *agent.HTTPClient
	definition tool.Definition
}

type Info struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Enabled     bool            `json:"enabled"`
	Permission  tool.Permission `json:"permission"`
	Tool        string          `json:"tool"`
	Provider    string          `json:"provider,omitempty"`
}

// Manager implements synchronous, config-defined handoff tools. It uses a
// scoped provider client for each agent and never mutates the main client's
// tool registry.
type Manager struct {
	path            string
	providers       map[string]*agent.HTTPClient
	defaultProvider string
	registry        *tool.Registry
	logger          *slog.Logger
	maxRounds       int
	nextID          atomic.Uint64

	mu       sync.RWMutex
	agents   map[string]managed
	reloadMu sync.Mutex
}

func NewManager(configs []Config, base *agent.HTTPClient, registry *tool.Registry, logger *slog.Logger, maxRounds int) (*Manager, error) {
	if base == nil {
		return nil, errors.New("subagent manager requires an agent client")
	}
	return NewManagerWithProviders(
		configs,
		map[string]*agent.HTTPClient{"default": base},
		"default",
		registry,
		logger,
		maxRounds,
	)
}

func NewManagerWithProviders(
	configs []Config,
	providers map[string]*agent.HTTPClient,
	defaultProvider string,
	registry *tool.Registry,
	logger *slog.Logger,
	maxRounds int,
) (*Manager, error) {
	if registry == nil {
		return nil, errors.New("subagent manager requires a tool registry")
	}
	defaultProvider = strings.TrimSpace(defaultProvider)
	base := providers[defaultProvider]
	if defaultProvider == "" || base == nil {
		return nil, fmt.Errorf("subagent default provider %q is unavailable", defaultProvider)
	}
	if logger == nil {
		logger = slog.Default()
	}
	if maxRounds <= 0 {
		maxRounds = 4
	}
	manager := &Manager{
		providers:       cloneProviders(providers),
		defaultProvider: defaultProvider,
		registry:        registry,
		logger:          logger,
		maxRounds:       maxRounds,
		agents:          make(map[string]managed),
	}
	if err := manager.install(configs); err != nil {
		return nil, err
	}
	return manager, nil
}

func Open(path string, base *agent.HTTPClient, registry *tool.Registry, logger *slog.Logger, maxRounds int) (*Manager, error) {
	configs, err := LoadFile(path)
	if err != nil {
		return nil, err
	}
	manager, err := NewManager(nil, base, registry, logger, maxRounds)
	if err != nil {
		return nil, err
	}
	manager.path = strings.TrimSpace(path)
	if err := manager.install(configs); err != nil {
		return nil, err
	}
	return manager, nil
}

func OpenWithProviders(
	path string,
	providers map[string]*agent.HTTPClient,
	defaultProvider string,
	registry *tool.Registry,
	logger *slog.Logger,
	maxRounds int,
) (*Manager, error) {
	configs, err := LoadFile(path)
	if err != nil {
		return nil, err
	}
	manager, err := NewManagerWithProviders(
		nil,
		providers,
		defaultProvider,
		registry,
		logger,
		maxRounds,
	)
	if err != nil {
		return nil, err
	}
	manager.path = strings.TrimSpace(path)
	if err := manager.install(configs); err != nil {
		return nil, err
	}
	return manager, nil
}

func (m *Manager) Reload(context.Context) error {
	m.reloadMu.Lock()
	defer m.reloadMu.Unlock()
	if m.path == "" {
		return errors.New("subagent config path is empty")
	}
	configs, err := LoadFile(m.path)
	if err != nil {
		return err
	}
	return m.install(configs)
}

func (m *Manager) List() []Info {
	m.mu.RLock()
	defer m.mu.RUnlock()
	names := make([]string, 0, len(m.agents))
	for name := range m.agents {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]Info, 0, len(names))
	for _, name := range names {
		current := m.agents[name]
		result = append(result, Info{
			Name:        current.config.Name,
			Description: current.config.Description,
			Enabled:     current.config.Active,
			Permission:  current.config.Permission,
			Tool:        current.definition.Name,
			Provider:    current.config.Provider,
		})
	}
	return result
}

func (m *Manager) install(configs []Config) error {
	candidates := make(map[string]managed, len(configs))
	names := make([]string, 0, len(configs))
	seenTools := make(map[string]struct{}, len(configs))
	for _, config := range configs {
		if _, exists := candidates[config.Name]; exists {
			return fmt.Errorf("subagent %q is duplicated", config.Name)
		}
		if !config.Active {
			candidates[config.Name] = managed{config: config}
			continue
		}
		providerName := strings.TrimSpace(config.Provider)
		if providerName == "" {
			providerName = m.defaultProvider
		}
		base := m.providers[providerName]
		if base == nil {
			return fmt.Errorf("subagent %q references unknown provider %q", config.Name, providerName)
		}
		config.Provider = providerName
		toolName := publicToolName(config.Name)
		if _, exists := seenTools[toolName]; exists {
			return fmt.Errorf("subagent tool namespace collision for %q", config.Name)
		}
		seenTools[toolName] = struct{}{}
		for _, allowedTool := range config.Tools {
			if !m.registry.Has(allowedTool) {
				return fmt.Errorf("subagent %q references unknown tool %q", config.Name, allowedTool)
			}
		}
		client := base.Scoped(config.SystemPrompt)
		if len(config.Tools) > 0 {
			client.SetTools(m.registry.Select(config.Tools), m.maxRounds)
		}
		currentConfig := config
		currentClient := client
		definition := tool.Definition{
			Name:        toolName,
			Description: fmt.Sprintf("[Subagent %s] %s", config.Name, fallbackDescription(config)),
			Permission:  config.Permission,
			Timeout:     30 * time.Second,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"task": map[string]any{
						"type":        "string",
						"description": "具体交给该子 Agent 处理的任务",
					},
					"context": map[string]any{
						"type":        "string",
						"description": "可选的业务上下文",
					},
				},
				"required":             []string{"task"},
				"additionalProperties": false,
			},
			Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
				return executeHandoff(ctx, call, currentConfig, currentClient, &m.nextID)
			},
		}
		candidates[config.Name] = managed{
			config:     config,
			client:     client,
			definition: definition,
		}
		names = append(names, toolName)
	}

	m.mu.RLock()
	old := make(map[string]managed, len(m.agents))
	oldToolNames := make([]string, 0, len(m.agents))
	for name, current := range m.agents {
		old[name] = current
		if current.definition.Name != "" {
			oldToolNames = append(oldToolNames, current.definition.Name)
		}
	}
	m.mu.RUnlock()
	oldNameSet := make(map[string]struct{}, len(oldToolNames))
	for _, name := range oldToolNames {
		oldNameSet[name] = struct{}{}
	}
	for _, name := range names {
		if m.registry.Has(name) {
			if _, isOld := oldNameSet[name]; !isOld {
				return fmt.Errorf("subagent tool %q is already registered", name)
			}
		}
	}
	for _, name := range oldToolNames {
		m.registry.Remove(name)
	}
	installed := make([]string, 0, len(names))
	for _, name := range names {
		for _, current := range candidates {
			if current.definition.Name != name {
				continue
			}
			if err := m.registry.Register(current.definition); err != nil {
				for _, installedName := range installed {
					m.registry.Remove(installedName)
				}
				for _, oldAgent := range old {
					if oldAgent.definition.Name != "" {
						_ = m.registry.Register(oldAgent.definition)
					}
				}
				return err
			}
			installed = append(installed, name)
			break
		}
	}
	m.mu.Lock()
	m.agents = candidates
	m.mu.Unlock()
	return nil
}

func executeHandoff(
	ctx context.Context,
	call tool.Call,
	config Config,
	client *agent.HTTPClient,
	nextID *atomic.Uint64,
) (tool.Result, error) {
	var input struct {
		Task    string `json:"task"`
		Context string `json:"context"`
	}
	if len(call.Arguments) == 0 {
		return tool.Result{}, errors.New("subagent task is required")
	}
	if err := json.Unmarshal(call.Arguments, &input); err != nil {
		return tool.Result{}, errors.New("subagent arguments are invalid JSON")
	}
	input.Task = strings.TrimSpace(input.Task)
	if input.Task == "" {
		return tool.Result{}, errors.New("subagent task is required")
	}
	if len([]rune(input.Task)) > maxTaskRunes {
		return tool.Result{}, fmt.Errorf("subagent task exceeds %d runes", maxTaskRunes)
	}
	parentSession := strings.TrimSpace(call.Actor.SessionID)
	if parentSession == "" {
		parentSession = fmt.Sprintf("group:%s:user:%s", call.Actor.GroupID, call.Actor.UserID)
	}
	request := domain.AgentRequest{
		RequestID:     fmt.Sprintf("subagent-%s-%d", config.Name, nextID.Add(1)),
		SessionID:     fmt.Sprintf("subagent:%s:%s", config.Name, parentSession),
		Text:          input.Task,
		UserID:        call.Actor.UserID,
		Platform:      call.Actor.Platform,
		ChatType:      call.Actor.ChatType,
		ChatID:        call.Actor.ChatID,
		GroupID:       call.Actor.GroupID,
		SelfID:        call.Actor.SelfID,
		SenderRole:    call.Actor.Role,
		History:       append([]domain.ChatMessage(nil), call.Actor.History...),
		SystemPrompt:  config.SystemPrompt,
		PromptContext: strings.TrimSpace(input.Context),
	}
	response, err := client.Reply(ctx, request)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Content: map[string]any{
		"agent":   config.Name,
		"reply":   response.Reply,
		"handoff": response.Handoff,
	}}, nil
}

func cloneProviders(source map[string]*agent.HTTPClient) map[string]*agent.HTTPClient {
	result := make(map[string]*agent.HTTPClient, len(source))
	for name, client := range source {
		if strings.TrimSpace(name) != "" && client != nil {
			result[name] = client
		}
	}
	return result
}

func fallbackDescription(config Config) string {
	if config.Description != "" {
		return config.Description
	}
	return truncate(config.SystemPrompt, 160)
}

func publicToolName(name string) string {
	var builder strings.Builder
	builder.WriteString("transfer_to_")
	for _, current := range name {
		if current >= 'a' && current <= 'z' ||
			current >= 'A' && current <= 'Z' ||
			current >= '0' && current <= '9' ||
			current == '_' || current == '-' {
			builder.WriteRune(current)
		} else {
			builder.WriteByte('_')
		}
	}
	result := builder.String()
	if len([]rune(result)) <= maxToolName {
		return result
	}
	return string([]rune(result)[:maxToolName-9]) + "_" + shortHash(result)
}

func shortHash(value string) string {
	var hash uint32 = 2166136261
	for _, current := range value {
		hash ^= uint32(current)
		hash *= 16777619
	}
	return fmt.Sprintf("%08x", hash)
}
