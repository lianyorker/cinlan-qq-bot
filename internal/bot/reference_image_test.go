package bot

import (
	"context"
	"testing"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/binding"
	"github.com/lianyorker/cinlan-qq-bot/internal/chatprefs"
	"github.com/lianyorker/cinlan-qq-bot/internal/persona"
	"github.com/lianyorker/cinlan-qq-bot/internal/session"
	"github.com/lianyorker/cinlan-qq-bot/internal/tool"
)

func TestReferenceImageForFallsThroughToPersonaDefault(t *testing.T) {
	cfg := testBotConfig(t)
	service := New(cfg, &fakeAgent{}, &fakeSender{}, session.New(10, time.Hour), testLogger())
	prefs, err := chatprefs.Load("", "qq-onebot")
	if err != nil {
		t.Fatalf("chatprefs.Load() error = %v", err)
	}
	service.SetChatPrefs(prefs)
	if err := service.PersonaRegistry().Register(persona.Profile{
		Name: "gf", SystemPrompt: "p",
		DefaultImage: "data/chat-images/qiqi-face.png",
	}); err != nil {
		t.Fatalf("Register persona: %v", err)
	}
	bindings, err := binding.NewRegistry([]binding.Rule{{
		Name: "bound", Platform: "*", SelfID: "10001",
		ChatType: "group", ChatID: "30003", Persona: "gf",
	}})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	service.SetChatBindings(bindings)

	actor := tool.Actor{
		Platform: "qq-onebot", SelfID: "10001",
		ChatType: "group", ChatID: "30003", UserID: "20002",
	}

	// No chat note: bound persona's default_image is the global anchor.
	if got := service.ReferenceImageFor(context.Background(), actor); got != "data/chat-images/qiqi-face.png" {
		t.Fatalf("persona default fallback = %q", got)
	}

	// A remembered per-chat image must win over the persona default.
	if err := prefs.AddImageNote("qq-onebot", "10001", "group", "30003", "自定义", "/tmp/chat-note.png"); err != nil {
		t.Fatalf("AddImageNote() error = %v", err)
	}
	if got := service.ReferenceImageFor(context.Background(), actor); got != "/tmp/chat-note.png" {
		t.Fatalf("chat note override = %q", got)
	}
}

func TestReferenceImageForEmptyWithoutDefault(t *testing.T) {
	cfg := testBotConfig(t)
	service := New(cfg, &fakeAgent{}, &fakeSender{}, session.New(10, time.Hour), testLogger())
	prefs, err := chatprefs.Load("", "qq-onebot")
	if err != nil {
		t.Fatalf("chatprefs.Load() error = %v", err)
	}
	service.SetChatPrefs(prefs)
	if err := service.PersonaRegistry().Register(persona.Profile{Name: "plain", SystemPrompt: "p"}); err != nil {
		t.Fatalf("Register persona: %v", err)
	}
	bindings, err := binding.NewRegistry([]binding.Rule{{
		Name: "bound", Platform: "*", SelfID: "10001",
		ChatType: "group", ChatID: "30003", Persona: "plain",
	}})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	service.SetChatBindings(bindings)

	got := service.ReferenceImageFor(context.Background(), tool.Actor{
		Platform: "qq-onebot", SelfID: "10001",
		ChatType: "group", ChatID: "30003", UserID: "20002",
	})
	if got != "" {
		t.Fatalf("no note + no default should be empty, got %q", got)
	}
}
