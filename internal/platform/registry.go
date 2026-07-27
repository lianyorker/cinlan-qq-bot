package platform

import (
	"errors"
	"fmt"
	"sort"
	"sync"
)

type AdapterInfo struct {
	Name      string `json:"name"`
	Connected bool   `json:"connected"`
}

type Registry struct {
	mu       sync.RWMutex
	adapters map[string]Adapter
}

func NewRegistry() *Registry {
	return &Registry{adapters: make(map[string]Adapter)}
}

func (r *Registry) Register(adapter Adapter) error {
	if adapter == nil {
		return errors.New("platform adapter is nil")
	}
	name := adapter.Name()
	if name == "" {
		return errors.New("platform adapter name is empty")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.adapters[name]; exists {
		return fmt.Errorf("platform adapter %q is already registered", name)
	}
	r.adapters[name] = adapter
	return nil
}

func (r *Registry) Remove(name string) {
	r.mu.Lock()
	delete(r.adapters, name)
	r.mu.Unlock()
}

func (r *Registry) Get(name string) (Adapter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	adapter, ok := r.adapters[name]
	return adapter, ok
}

func (r *Registry) List() []AdapterInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]AdapterInfo, 0, len(r.adapters))
	for name, adapter := range r.adapters {
		result = append(result, AdapterInfo{Name: name, Connected: adapter.Connected()})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}
