package provider

import (
	"context"
	"testing"

	"github.com/lianyorker/cinlan-qq-bot/internal/domain"
)

type fakeProvider struct {
	name string
	kind string
}

func (p fakeProvider) Name() string { return p.name }
func (p fakeProvider) Kind() string { return p.kind }
func (p fakeProvider) Reply(context.Context, domain.AgentRequest) (domain.AgentResponse, error) {
	return domain.AgentResponse{Reply: p.name}, nil
}

func TestRegistryDefaultAndReplacement(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(fakeProvider{name: "a", kind: "test"}); err != nil {
		t.Fatalf("Register(a) error = %v", err)
	}
	if err := registry.Register(fakeProvider{name: "b", kind: "test"}); err != nil {
		t.Fatalf("Register(b) error = %v", err)
	}
	if err := registry.SetDefault("b"); err != nil {
		t.Fatalf("SetDefault() error = %v", err)
	}
	response, err := registry.Reply(context.Background(), domain.AgentRequest{})
	if err != nil || response.Reply != "b" {
		t.Fatalf("Reply() = %#v, %v", response, err)
	}
	if len(registry.List()) != 2 || !registry.List()[1].Default {
		t.Fatalf("List() = %#v", registry.List())
	}
}
