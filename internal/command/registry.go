package command

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/lianyorker/cinlan-qq-bot/internal/platform"
)

type Permission string

const (
	PermissionEveryone Permission = "everyone"
	PermissionAdmin    Permission = "admin"
)

type Context struct {
	Event     platform.Event
	SessionID string
	Name      string
	Args      []string
	Raw       string
	Values    map[string]any
}

type Result struct {
	Handled bool
	Reply   string
	Handoff bool
}

type Handler func(context.Context, *Context) (Result, error)

type Definition struct {
	Name        string
	Aliases     []string
	Description string
	Permission  Permission
	Handler     Handler
}

type Info struct {
	Name        string     `json:"name"`
	Aliases     []string   `json:"aliases,omitempty"`
	Description string     `json:"description,omitempty"`
	Permission  Permission `json:"permission"`
}

type Registry struct {
	mu       sync.RWMutex
	commands map[string]Definition
}

func NewRegistry() *Registry {
	return &Registry{commands: make(map[string]Definition)}
}

func (r *Registry) Register(definition Definition) error {
	name := normalize(definition.Name)
	if name == "" {
		return errors.New("command name is empty")
	}
	if definition.Handler == nil {
		return fmt.Errorf("command %q has no handler", name)
	}
	if definition.Permission == "" {
		definition.Permission = PermissionEveryone
	}
	definition.Name = name
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, key := range append([]string{name}, definition.Aliases...) {
		key = normalize(key)
		if key == "" {
			continue
		}
		if _, exists := r.commands[key]; exists {
			return fmt.Errorf("command %q is already registered", key)
		}
	}
	r.commands[name] = definition
	for _, alias := range definition.Aliases {
		if key := normalize(alias); key != "" {
			r.commands[key] = definition
		}
	}
	return nil
}

func (r *Registry) Unregister(name string) {
	name = normalize(name)
	r.mu.Lock()
	defer r.mu.Unlock()
	definition, ok := r.commands[name]
	if !ok {
		return
	}
	for key, current := range r.commands {
		if current.Name == definition.Name {
			delete(r.commands, key)
		}
	}
}

func (r *Registry) Execute(ctx context.Context, event platform.Event, sessionID, raw string, values map[string]any) (Result, bool, error) {
	name, args, ok := Parse(raw)
	if !ok {
		return Result{}, false, nil
	}
	r.mu.RLock()
	definition, exists := r.commands[name]
	r.mu.RUnlock()
	if !exists {
		return Result{}, false, nil
	}
	if definition.Permission == PermissionAdmin &&
		strings.ToLower(event.SenderRole) != "admin" &&
		strings.ToLower(event.SenderRole) != "owner" {
		return Result{Handled: true, Reply: "权限不足。"}, true, nil
	}
	result, err := definition.Handler(ctx, &Context{
		Event:     event,
		SessionID: sessionID,
		Name:      name,
		Args:      args,
		Raw:       raw,
		Values:    values,
	})
	return result, true, err
}

func (r *Registry) List() []Info {
	r.mu.RLock()
	defer r.mu.RUnlock()
	unique := make(map[string]Definition)
	for _, definition := range r.commands {
		unique[definition.Name] = definition
	}
	result := make([]Info, 0, len(unique))
	for _, definition := range unique {
		aliases := append([]string(nil), definition.Aliases...)
		sort.Strings(aliases)
		result = append(result, Info{
			Name:        definition.Name,
			Aliases:     aliases,
			Description: definition.Description,
			Permission:  definition.Permission,
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func Parse(raw string) (string, []string, bool) {
	fields := strings.Fields(strings.TrimSpace(raw))
	if len(fields) == 0 {
		return "", nil, false
	}
	name := normalize(fields[0])
	if !strings.HasPrefix(name, "/") {
		return "", nil, false
	}
	name = strings.TrimPrefix(name, "/")
	if name == "" {
		return "", nil, false
	}
	return name, fields[1:], true
}

func normalize(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
