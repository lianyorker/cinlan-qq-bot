package bot

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/chatprefs"
	"github.com/lianyorker/cinlan-qq-bot/internal/domain"
	"github.com/lianyorker/cinlan-qq-bot/internal/persona"
	"github.com/lianyorker/cinlan-qq-bot/internal/session"
)

func testNoticeLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestSanitizeModerationVoicePicksFirstLine(t *testing.T) {
	got := sanitizeModerationVoice("\n\n再发广告就禁言你了哦\n多余的解释\n")
	if got != "再发广告就禁言你了哦" {
		t.Fatalf("sanitize = %q, want first non-empty line", got)
	}
}

func TestSanitizeModerationVoiceStripsQuotesAndMarkers(t *testing.T) {
	if got := sanitizeModerationVoice(`"广告我撤了"`); got != "广告我撤了" {
		t.Fatalf("quotes not stripped: %q", got)
	}
	if got := sanitizeModerationVoice("[[AT]]广告别发了"); got != "" {
		t.Fatalf("marker reply must be rejected: %q", got)
	}
	if got := sanitizeModerationVoice("   \n  "); got != "" {
		t.Fatalf("blank reply must be empty: %q", got)
	}
}

func TestSanitizeModerationVoiceCapsLength(t *testing.T) {
	long := make([]rune, 200)
	for i := range long {
		long[i] = '啊'
	}
	if got := []rune(sanitizeModerationVoice(string(long))); len(got) != 80 {
		t.Fatalf("len = %d, want capped 80", len(got))
	}
}

func TestPersonaModerationNoticeFallsBackWithoutPersona(t *testing.T) {
	svc := &Service{personas: persona.NewRegistry()}
	got := svc.personaModerationNotice(
		context.Background(), &flowState{personaName: "ghost"}, "模板警告",
	)
	if got != "模板警告" {
		t.Fatalf("missing persona must fall back, got %q", got)
	}
}

func TestPersonaModerationNoticeFallsBackOnError(t *testing.T) {
	cfg := testBotConfig(t)
	svc := &Service{
		cfg:      cfg,
		agent:    &fakeAgent{err: errors.New("down")},
		personas: persona.NewRegistry(),
		logger:   testNoticeLogger(),
	}
	_ = svc.personas.Register(persona.Profile{
		Name: "p", SystemPrompt: "你是七七",
	})
	got := svc.personaModerationNotice(
		context.Background(), &flowState{personaName: "p"}, "模板警告",
	)
	if got != "模板警告" {
		t.Fatalf("agent error must fall back, got %q", got)
	}
}

func TestFeelEventWritesMoodAndObservation(t *testing.T) {
	prefs, err := chatprefs.Load(filepath.Join(t.TempDir(), "prefs.json"))
	if err != nil {
		t.Fatalf("chatprefs.Load: %v", err)
	}
	sessions := session.New(10, time.Hour)
	svc := &Service{
		chatPrefs: prefs,
		sessions:  sessions,
		logger:    testNoticeLogger(),
		now:       time.Now,
	}
	state := &flowState{
		sessionID: "qq:self:1:group:2",
		chatType:  "group",
		chatID:    "2",
	}
	svc.feelEvent(context.Background(), state, -5, "刚撤了条广告，有点无语")
	if score := prefs.MoodScore("group", "2", time.Now()); score != -5 {
		t.Fatalf("mood = %d, want -5", score)
	}
	found := false
	for _, m := range sessions.SnapshotState(state.sessionID).History {
		if strings.Contains(m.Content, "[心情] 刚撤了条广告") {
			found = true
		}
	}
	if !found {
		t.Fatal("session history missing [心情] observation")
	}
}

func TestFeelEventSkipsEmptySession(t *testing.T) {
	svc := &Service{logger: testNoticeLogger()}
	// no sessionID — must not panic or touch stores
	svc.feelEvent(context.Background(), &flowState{}, -5, "test")
}

func TestPersonaModerationNoticeUsesVoice(t *testing.T) {
	cfg := testBotConfig(t)
	svc := &Service{
		cfg:      cfg,
		agent:    &fakeAgent{response: domain.AgentResponse{Reply: "广告我撤了哈，再来就禁言了"}},
		personas: persona.NewRegistry(),
		logger:   testNoticeLogger(),
	}
	_ = svc.personas.Register(persona.Profile{
		Name: "p", SystemPrompt: "你是七七",
	})
	got := svc.personaModerationNotice(
		context.Background(), &flowState{personaName: "p"}, "模板警告",
	)
	if got != "广告我撤了哈，再来就禁言了" {
		t.Fatalf("voiced notice = %q", got)
	}
}
