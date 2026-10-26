package bot

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/session"
)

// fakeSynth records each Synthesize call and writes a plausible mp3
// so the record chain still goes out.
type fakeSynth struct {
	mu    sync.Mutex
	calls []synthCall
	err   error
}

type synthCall struct {
	text  string
	voice string
	rate  float64
}

func (f *fakeSynth) Synthesize(
	_ context.Context, text, outPath, voice string, rate float64,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, synthCall{text: text, voice: voice, rate: rate})
	if f.err != nil {
		return f.err
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(outPath, []byte("fake-mp3-audio"), 0o600)
}

func TestSingClipsRouteToSingProviderWithTag(t *testing.T) {
	cfg := testBotConfig(t)
	cfg.VoiceDir = t.TempDir()
	service := New(
		cfg, &fakeAgent{}, &fakeSender{},
		session.New(10, time.Hour), testLogger(),
	)
	voice := &fakeSynth{}
	sing := &fakeSynth{}
	service.SetVoiceProvider(voice)
	service.SetSingProvider(sing)

	clips := []voiceClip{
		{Text: "说话内容", Singing: false},
		{Text: "啦啦啦唱歌词", Singing: true, Rate: 0.8},
	}
	service.sendVoiceClips(
		context.Background(), "qq", "10001", "private", "20002",
		clips, "volc_default_voice", 1.0,
	)

	if len(voice.calls) != 1 || voice.calls[0].voice != "volc_default_voice" {
		t.Fatalf("voice calls = %#v", voice.calls)
	}
	if len(sing.calls) != 1 {
		t.Fatalf("sing calls = %#v", sing.calls)
	}
	got := sing.calls[0]
	if !strings.HasPrefix(got.text, "(sings) ") || !strings.Contains(got.text, "啦啦啦") {
		t.Fatalf("sing text missing (sings) tag: %q", got.text)
	}
	if got.voice != "" {
		t.Fatalf("sing voice leaked: %q", got.voice)
	}
	if got.rate != 0.8 {
		t.Fatalf("sing rate = %v", got.rate)
	}
}

func TestSingClipFallsBackToVoiceProviderWhenNoSingEngine(t *testing.T) {
	cfg := testBotConfig(t)
	cfg.VoiceDir = t.TempDir()
	service := New(
		cfg, &fakeAgent{}, &fakeSender{},
		session.New(10, time.Hour), testLogger(),
	)
	voice := &fakeSynth{}
	service.SetVoiceProvider(voice)

	service.sendVoiceClips(
		context.Background(), "qq", "10001", "private", "20002",
		[]voiceClip{{Text: "歌词", Singing: true}},
		"volc_default_voice", 1.0,
	)
	if len(voice.calls) != 1 ||
		voice.calls[0].voice != "volc_default_voice" ||
		strings.HasPrefix(voice.calls[0].text, "(sings)") {
		t.Fatalf("fallback calls = %#v", voice.calls)
	}
}
