package bot

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestVoiceCacheKeyDeterministic(t *testing.T) {
	a := voiceCacheKey("你好呀", "zh-CN-XiaoyiNeural", 1.0, "")
	b := voiceCacheKey("你好呀", "zh-CN-XiaoyiNeural", 1.0, "")
	if a != b {
		t.Fatalf("same inputs must yield same key: %q vs %q", a, b)
	}
	if filepath.Ext(a) != ".mp3" {
		t.Fatalf("key must carry .mp3 extension, got %q", a)
	}
	c := voiceCacheKey("你好呀", "zh-CN-XiaoyiNeural", 1.5, "")
	if c == a {
		t.Fatalf("different rate must yield different key")
	}
	d := voiceCacheKey("换一句", "zh-CN-XiaoyiNeural", 1.0, "")
	if d == a {
		t.Fatalf("different text must yield different key")
	}
}

func TestSweepVoiceCacheTTL(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "old.mp3")
	fresh := filepath.Join(dir, "fresh.mp3")
	for _, p := range []string{old, fresh} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stale := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(old, stale, stale); err != nil {
		t.Fatal(err)
	}
	sweepVoiceCache(dir, 1<<20, time.Hour)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("expired file must be removed")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh file must survive, got %v", err)
	}
}

func TestSweepVoiceCacheEvictsOldest(t *testing.T) {
	dir := t.TempDir()
	files := make([]string, 0, 4)
	for i := 0; i < 4; i++ {
		p := filepath.Join(dir, string(rune('a'+i))+".mp3")
		if err := os.WriteFile(p, make([]byte, 100), 0o644); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-time.Duration(4-i) * time.Hour)
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
		files = append(files, p)
	}
	// 400 bytes total, cap 250 → evict 2 oldest (a,b)
	sweepVoiceCache(dir, 250, 0)
	if _, err := os.Stat(files[0]); !os.IsNotExist(err) {
		t.Fatalf("oldest file must be evicted")
	}
	if _, err := os.Stat(files[1]); !os.IsNotExist(err) {
		t.Fatalf("second-oldest file must be evicted")
	}
	if _, err := os.Stat(files[3]); err != nil {
		t.Fatalf("newest file must survive, got %v", err)
	}
}

func TestSweepVoiceCacheMissingDir(t *testing.T) {
	// Must not panic on a directory that doesn't exist yet.
	sweepVoiceCache(filepath.Join(t.TempDir(), "nope"), 1<<20, time.Hour)
}
