package bot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/domain"
	"github.com/lianyorker/cinlan-qq-bot/internal/session"
)

// A bare checkout has no data/rules/base-human.md — the binary must still
// load the embedded behavior contract instead of running promptless.
func TestBasePromptFallsBackToEmbedded(t *testing.T) {
	cfg := testBotConfig(t)
	cfg.BasePromptFile = filepath.Join(t.TempDir(), "no-such-file.md")
	service := New(cfg, &fakeAgent{response: domain.AgentResponse{Reply: "ok"}}, &fakeSender{}, session.New(10, time.Hour), testLogger())
	if service.basePrompt != strings.TrimSpace(embeddedBasePrompt) {
		t.Fatalf("missing file must fall back to embedded base prompt")
	}
	if !strings.Contains(service.basePrompt, "NEXT") {
		t.Fatalf("embedded base prompt looks truncated")
	}
}

// An operator-supplied file always wins over the baked-in default.
func TestBasePromptFileOverridesEmbedded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "base.md")
	if err := os.WriteFile(path, []byte("custom base rules"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := testBotConfig(t)
	cfg.BasePromptFile = path
	service := New(cfg, &fakeAgent{response: domain.AgentResponse{Reply: "ok"}}, &fakeSender{}, session.New(10, time.Hour), testLogger())
	if service.basePrompt != "custom base rules" {
		t.Fatalf("file override lost to embedded default: %q", service.basePrompt)
	}
}

// The marker contract ships with the binary so a stripped-down deployment
// still knows which [[ACTION]] markers the reply parser understands.
func TestProtocolBlockCoversMarkers(t *testing.T) {
	block := buildProtocolBlock(resolveMarkersSpec("*", nil), true)
	for _, want := range []string{"[[AT]]", "[[POKE]]", "[[OOPS]]", "[[PASS]]", "[[NEXT]]", "[[MEME", "表情:"} {
		if !strings.Contains(block, want) {
			t.Fatalf("protocol block missing %q", want)
		}
	}
}
