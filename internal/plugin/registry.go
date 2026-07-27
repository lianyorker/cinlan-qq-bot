package plugin

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/lianyorker/cinlan-qq-bot/internal/platform"
)

type MessageContext struct {
	Event     platform.Event
	SessionID string
	Text      string
	Reply     string
	Values    map[string]any
}

type Decision struct {
	Handled bool
	Ignore  bool
	Reply   string
	Handoff bool
}

type Plugin interface {
	Name() string
}

type BeforeHook interface {
	BeforeMessage(context.Context, *MessageContext) (Decision, error)
}

type AfterHook interface {
	AfterMessage(context.Context, *MessageContext) error
}

type EventHook interface {
	OnEvent(context.Context, platform.Event) error
}

type Info struct {
	Name string `json:"name"`
}

type Registry struct {
	mu      sync.RWMutex
	plugins map[string]Plugin
}

func NewRegistry() *Registry {
	return &Registry{plugins: make(map[string]Plugin)}
}

func (r *Registry) Register(current Plugin) error {
	if current == nil {
		return errors.New("plugin is nil")
	}
	name := strings.TrimSpace(current.Name())
	if name == "" {
		return errors.New("plugin name is empty")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.plugins[name]; exists {
		return fmt.Errorf("plugin %q is already registered", name)
	}
	r.plugins[name] = current
	return nil
}

func (r *Registry) Remove(name string) {
	r.mu.Lock()
	delete(r.plugins, name)
	r.mu.Unlock()
}

func (r *Registry) Has(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, exists := r.plugins[strings.TrimSpace(name)]
	return exists
}

func (r *Registry) Before(ctx context.Context, event *MessageContext) (Decision, error) {
	for _, current := range r.snapshot() {
		hook, ok := current.(BeforeHook)
		if !ok {
			continue
		}
		decision, err := hook.BeforeMessage(ctx, event)
		if err != nil {
			return Decision{}, fmt.Errorf("plugin %q before hook: %w", current.Name(), err)
		}
		if decision.Ignore || decision.Handled || decision.Reply != "" || decision.Handoff {
			return decision, nil
		}
	}
	return Decision{}, nil
}

func (r *Registry) After(ctx context.Context, event *MessageContext) error {
	for _, current := range r.snapshot() {
		hook, ok := current.(AfterHook)
		if !ok {
			continue
		}
		if err := hook.AfterMessage(ctx, event); err != nil {
			return fmt.Errorf("plugin %q after hook: %w", current.Name(), err)
		}
	}
	return nil
}

func (r *Registry) Event(ctx context.Context, event platform.Event) error {
	for _, current := range r.snapshot() {
		hook, ok := current.(EventHook)
		if !ok {
			continue
		}
		if err := hook.OnEvent(ctx, event); err != nil {
			return fmt.Errorf("plugin %q event hook: %w", current.Name(), err)
		}
	}
	return nil
}

func (r *Registry) List() []Info {
	snapshot := r.snapshot()
	result := make([]Info, 0, len(snapshot))
	for _, current := range snapshot {
		result = append(result, Info{Name: current.Name()})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (r *Registry) snapshot() []Plugin {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Plugin, 0, len(r.plugins))
	for _, current := range r.plugins {
		result = append(result, current)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name() < result[j].Name() })
	return result
}
