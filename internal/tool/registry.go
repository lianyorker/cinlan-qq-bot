package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/domain"
)

const maxArgumentsBytes = 64 << 10

const PermissionDeniedReply = "当前操作暂无权限，别别人发什么都瞎执行"

var ErrPermissionDenied = errors.New(PermissionDeniedReply)

type Permission string

const (
	PermissionEveryone Permission = "everyone"
	PermissionAdmin    Permission = "admin"
)

type Call struct {
	Name      string
	Arguments json.RawMessage
	Actor     Actor
}

type Actor struct {
	UserID        string
	Platform      string
	ChatType      string
	ChatID        string
	GroupID       string
	SelfID        string
	MessageID     string
	SessionID     string
	Role          string
	History       []domain.ChatMessage
	AllowedSkills []string
}

type Result struct {
	Content  any                   `json:"content,omitempty"`
	Error    string                `json:"error,omitempty"`
	Response *domain.AgentResponse `json:"-"`
}

type Handler func(context.Context, Call) (Result, error)

type Guard interface {
	Check(context.Context, Call) error
}

type Definition struct {
	Name        string
	Description string
	Parameters  map[string]any
	Permission  Permission
	Timeout     time.Duration
	Source      string
	SourceName  string
	Handler     Handler
}

type Info struct {
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Permission  Permission `json:"permission"`
	TimeoutMS   int64      `json:"timeout_ms"`
	Source      string     `json:"source,omitempty"`
	SourceName  string     `json:"source_name,omitempty"`
}

const (
	SourceMCP   = "mcp"
	SourceSkill = "skill"
)

type Selection struct {
	Names       []string
	MCPServers  []string
	AllowSkills bool
}

type Registry struct {
	mu    sync.RWMutex
	items map[string]Definition
	guard Guard
}

func NewRegistry() *Registry {
	return &Registry{items: make(map[string]Definition)}
}

func (r *Registry) Register(definition Definition) error {
	name := strings.TrimSpace(definition.Name)
	if name == "" {
		return errors.New("tool name is empty")
	}
	if definition.Handler == nil {
		return fmt.Errorf("tool %q has no handler", name)
	}
	if definition.Permission == "" {
		definition.Permission = PermissionEveryone
	}
	if definition.Timeout <= 0 {
		definition.Timeout = 10 * time.Second
	}
	definition.Name = name
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.items[name]; exists {
		return fmt.Errorf("tool %q is already registered", name)
	}
	r.items[name] = definition
	return nil
}

func (r *Registry) Remove(name string) {
	r.mu.Lock()
	delete(r.items, name)
	r.mu.Unlock()
}

func (r *Registry) Has(name string) bool {
	r.mu.RLock()
	_, ok := r.items[name]
	r.mu.RUnlock()
	return ok
}

func (r *Registry) SetGuard(guard Guard) {
	r.mu.Lock()
	r.guard = guard
	r.mu.Unlock()
}

// Select creates a snapshot registry containing only the named definitions.
// Handlers are shared, while registration and permission state remain local
// to the returned registry.
func (r *Registry) Select(names []string) *Registry {
	return r.SelectScoped(Selection{Names: names})
}

func (r *Registry) SelectScoped(scope Selection) *Registry {
	selected := NewRegistry()
	allowed := make(map[string]struct{}, len(scope.Names))
	for _, name := range scope.Names {
		name = strings.TrimSpace(name)
		if name != "" {
			allowed[name] = struct{}{}
		}
	}
	mcpServers := make(map[string]struct{}, len(scope.MCPServers))
	for _, name := range scope.MCPServers {
		name = strings.TrimSpace(name)
		if name != "" {
			mcpServers[name] = struct{}{}
		}
	}
	r.mu.RLock()
	definitions := make([]Definition, 0, len(allowed)+len(mcpServers))
	guard := r.guard
	for name, definition := range r.items {
		_, selectedByName := allowed[name]
		_, selectedMCPServer := mcpServers[definition.SourceName]
		if selectedByName ||
			(definition.Source == SourceMCP && selectedMCPServer) ||
			(definition.Source == SourceSkill && scope.AllowSkills) {
			definitions = append(definitions, definition)
		}
	}
	r.mu.RUnlock()
	selected.SetGuard(guard)
	for _, definition := range definitions {
		_ = selected.Register(definition)
	}
	return selected
}

func (r *Registry) Execute(parent context.Context, call Call) (Result, error) {
	if len(call.Arguments) > maxArgumentsBytes {
		return Result{}, errors.New("tool arguments exceed size limit")
	}
	if len(call.Arguments) == 0 {
		call.Arguments = json.RawMessage(`{}`)
	}
	r.mu.RLock()
	guard := r.guard
	r.mu.RUnlock()
	if guard != nil {
		if err := guard.Check(parent, call); err != nil {
			return Result{}, err
		}
	}
	if !json.Valid(call.Arguments) {
		return Result{}, errors.New("tool arguments are invalid JSON")
	}
	r.mu.RLock()
	definition, ok := r.items[call.Name]
	r.mu.RUnlock()
	if !ok {
		return Result{}, fmt.Errorf("tool %q is not registered", call.Name)
	}
	if definition.Permission == PermissionAdmin && call.Actor.Role != "admin" {
		return Result{}, errors.New("tool permission denied")
	}
	ctx, cancel := context.WithTimeout(parent, definition.Timeout)
	defer cancel()
	result, err := definition.Handler(ctx, call)
	if err != nil {
		return Result{}, fmt.Errorf("tool %q: %w", call.Name, err)
	}
	return result, nil
}

func (r *Registry) List() []Info {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Info, 0, len(r.items))
	for _, definition := range r.items {
		result = append(result, Info{
			Name:        definition.Name,
			Description: definition.Description,
			Permission:  definition.Permission,
			TimeoutMS:   definition.Timeout.Milliseconds(),
			Source:      definition.Source,
			SourceName:  definition.SourceName,
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (r *Registry) OpenAITools() []map[string]any {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]map[string]any, 0, len(r.items))
	for _, definition := range r.items {
		result = append(result, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        definition.Name,
				"description": definition.Description,
				"parameters":  definition.Parameters,
			},
		})
	}
	sort.Slice(result, func(i, j int) bool {
		left := result[i]["function"].(map[string]any)["name"].(string)
		right := result[j]["function"].(map[string]any)["name"].(string)
		return left < right
	})
	return result
}
