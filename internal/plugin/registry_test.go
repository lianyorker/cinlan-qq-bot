package plugin

import (
	"context"
	"testing"

	"github.com/lianyorker/cinlan-qq-bot/internal/platform"
)

type testPlugin struct {
	name   string
	events *int
}

func (p testPlugin) Name() string { return p.name }

func (p testPlugin) BeforeMessage(context.Context, *MessageContext) (Decision, error) {
	return Decision{Reply: "plugin reply", Handled: true}, nil
}

func (p testPlugin) OnEvent(context.Context, platform.Event) error {
	if p.events != nil {
		*p.events++
	}
	return nil
}

func TestRegistryRunsBeforeHook(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(testPlugin{name: "faq"}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	decision, err := registry.Before(context.Background(), &MessageContext{
		Event: platform.Event{Platform: platform.PlatformQQOneBot},
		Text:  "question",
	})
	if err != nil || !decision.Handled || decision.Reply != "plugin reply" {
		t.Fatalf("decision = %#v, error = %v", decision, err)
	}
}

func TestRegistryDispatchesRawEvents(t *testing.T) {
	count := 0
	registry := NewRegistry()
	if err := registry.Register(testPlugin{name: "events", events: &count}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if err := registry.Event(context.Background(), platform.Event{PostType: "notice"}); err != nil {
		t.Fatalf("Event() error = %v", err)
	}
	if count != 1 {
		t.Fatalf("event count = %d", count)
	}
}
