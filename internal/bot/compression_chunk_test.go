package bot

import (
	"strings"
	"testing"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/domain"
)

func TestChunkHistoryForIndexMergesSameRoleRuns(t *testing.T) {
	now := time.Date(2026, 10, 28, 10, 0, 0, 0, time.Local)
	dropped := []domain.ChatMessage{
		{Role: "user", Content: "[10-27 22:00] 在吗"},
		{Role: "user", Content: "[10-27 22:01] 问你个事"},
		{Role: "assistant", Content: "[10-27 22:02] 在的你说"},
	}
	chunks := chunkHistoryForIndex(dropped, "span1", now)
	if len(chunks) != 2 {
		t.Fatalf("chunks = %d, want 2 (user run + assistant)", len(chunks))
	}
	if chunks[0].Role != "user" ||
		!strings.Contains(chunks[0].Text, "在吗") ||
		!strings.Contains(chunks[0].Text, "问你个事") {
		t.Fatalf("merged user chunk = %#v", chunks[0])
	}
	// Stamp carries over — the year is inferred, not stamped.
	if chunks[0].TS.Year() != 2026 || chunks[0].TS.Month() != 10 ||
		chunks[0].TS.Day() != 27 {
		t.Fatalf("chunk TS = %v", chunks[0].TS)
	}
}

func TestChunkHistoryForIndexYearRollover(t *testing.T) {
	// January 2nd compressing a turn stamped December 30th — naive
	// year inference lands in the future, must roll back.
	now := time.Date(2027, 1, 2, 10, 0, 0, 0, time.Local)
	dropped := []domain.ChatMessage{
		{Role: "user", Content: "[12-30 23:00] 跨年前的消息"},
	}
	chunks := chunkHistoryForIndex(dropped, "span1", now)
	if len(chunks) != 1 || chunks[0].TS.Year() != 2026 {
		t.Fatalf("rollover TS = %#v", chunks)
	}
}

func TestChunkHistoryForIndexSplitsLongMessage(t *testing.T) {
	now := time.Now()
	long := strings.Repeat("很长的一句话", 60) // 360 runes > chunkMaxRunes
	chunks := chunkHistoryForIndex(
		[]domain.ChatMessage{{Role: "user", Content: long}}, "s", now,
	)
	if len(chunks) < 2 {
		t.Fatalf("long message should split, got %d chunks", len(chunks))
	}
	for _, c := range chunks {
		if len([]rune(c.Text)) > chunkMaxRunes {
			t.Fatalf("oversized chunk %d runes", len([]rune(c.Text)))
		}
	}
}

func TestExpandRecallQueryAppendsHistoryOnAnaphora(t *testing.T) {
	history := []domain.ChatMessage{
		{Role: "user", Content: "[10-27 20:00] 瑞幸那个新品你喝了没"},
		{Role: "assistant", Content: "[10-27 20:01] 喝了，有点甜"},
	}
	q := expandRecallQuery("上次说那个事后来咋样了", history)
	if !strings.Contains(q, "瑞幸") {
		t.Fatalf("anaphora query should pull in recent turns, got %q", q)
	}
	// Plain questions stay untouched — no history noise.
	if q2 := expandRecallQuery("今晚吃啥", history); q2 != "今晚吃啥" {
		t.Fatalf("non-anaphora query changed: %q", q2)
	}
}
