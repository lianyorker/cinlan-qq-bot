package tts

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestManualEdgeSynth is a manual probe: TTSCHECK=1 go test ./internal/tts/ -run ManualEdge -v
// Synthesizes a real clip through the edge websocket to verify a voice/style
// combo is accepted by the consumer endpoint before deploying it.
func TestManualEdgeSynth(t *testing.T) {
	if os.Getenv("TTSCHECK") == "" {
		t.Skip("manual probe — set TTSCHECK=1 to run")
	}
	voice := os.Getenv("TTS_VOICE")
	if voice == "" {
		voice = "zh-CN-XiaoyiMultilingualNeural"
	}
	style := os.Getenv("TTS_STYLE")
	out := filepath.Join(os.TempDir(), "ttscheck.mp3")
	p := &edgeProvider{
		voice:   voice,
		style:   style,
		timeout: 20 * time.Second,
	}
	err := p.Synthesize(
		context.Background(),
		"七七上线啦！今天也要元气满满哦，想听点什么尽管说。",
		out, voice, 1.0,
	)
	if err != nil {
		t.Fatalf("synthesize failed: %v", err)
	}
	info, statErr := os.Stat(out)
	if statErr != nil {
		t.Fatalf("stat: %v", statErr)
	}
	t.Logf("voice=%s style=%q -> %s (%d bytes)", voice, style, out, info.Size())
	if info.Size() < 2000 {
		t.Fatalf("suspiciously small audio: %d bytes", info.Size())
	}
}
