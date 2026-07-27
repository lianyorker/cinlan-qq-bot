package provider

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/lianyorker/cinlan-qq-bot/internal/domain"
)

type Client interface {
	Reply(context.Context, domain.AgentRequest) (domain.AgentResponse, error)
}

type Provider interface {
	Name() string
	Kind() string
	Reply(context.Context, domain.AgentRequest) (domain.AgentResponse, error)
}

type ClientProvider struct {
	providerName string
	providerKind string
	client       Client
}

func Wrap(name, kind string, client Client) *ClientProvider {
	return &ClientProvider{
		providerName: strings.TrimSpace(name),
		providerKind: strings.TrimSpace(kind),
		client:       client,
	}
}

func (p *ClientProvider) Name() string {
	return p.providerName
}

func (p *ClientProvider) Kind() string {
	return p.providerKind
}

func (p *ClientProvider) Reply(ctx context.Context, request domain.AgentRequest) (domain.AgentResponse, error) {
	if p == nil || p.client == nil {
		return domain.AgentResponse{}, errors.New("provider client is not configured")
	}
	return p.client.Reply(ctx, request)
}

type Info struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Default bool   `json:"default"`
}

type Registry struct {
	mu          sync.RWMutex
	providers   map[string]Provider
	defaultName string
}

func NewRegistry() *Registry {
	return &Registry{providers: make(map[string]Provider)}
}

func (r *Registry) Register(provider Provider) error {
	if provider == nil {
		return errors.New("provider is nil")
	}
	name := strings.TrimSpace(provider.Name())
	if name == "" {
		return errors.New("provider name is empty")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.providers[name]; exists {
		return fmt.Errorf("provider %q is already registered", name)
	}
	r.providers[name] = provider
	if r.defaultName == "" {
		r.defaultName = name
	}
	return nil
}

func (r *Registry) Replace(provider Provider) error {
	if provider == nil {
		return errors.New("provider is nil")
	}
	name := strings.TrimSpace(provider.Name())
	if name == "" {
		return errors.New("provider name is empty")
	}
	r.mu.Lock()
	r.providers[name] = provider
	if r.defaultName == "" {
		r.defaultName = name
	}
	r.mu.Unlock()
	return nil
}

func (r *Registry) Remove(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.providers, name)
	if r.defaultName == name {
		r.defaultName = ""
		for candidate := range r.providers {
			r.defaultName = candidate
			break
		}
	}
}

func (r *Registry) SetDefault(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.providers[name]; !ok {
		return fmt.Errorf("provider %q is not registered", name)
	}
	r.defaultName = name
	return nil
}

func (r *Registry) DefaultName() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.defaultName
}

func (r *Registry) Has(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.providers[strings.TrimSpace(name)]
	return ok
}

func (r *Registry) Reply(ctx context.Context, request domain.AgentRequest) (domain.AgentResponse, error) {
	r.mu.RLock()
	current := r.providers[r.defaultName]
	r.mu.RUnlock()
	if current == nil {
		return domain.AgentResponse{}, errors.New("no agent provider is registered")
	}
	return current.Reply(ctx, request)
}

func (r *Registry) ReplyWith(ctx context.Context, name string, request domain.AgentRequest) (domain.AgentResponse, error) {
	r.mu.RLock()
	current := r.providers[name]
	r.mu.RUnlock()
	if current == nil {
		return domain.AgentResponse{}, fmt.Errorf("provider %q is not registered", name)
	}
	return current.Reply(ctx, request)
}

func (r *Registry) List() []Info {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]Info, 0, len(names))
	for _, name := range names {
		current := r.providers[name]
		result = append(result, Info{
			Name:    current.Name(),
			Kind:    current.Kind(),
			Default: name == r.defaultName,
		})
	}
	return result
}
