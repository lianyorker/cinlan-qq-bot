package affinity

import (
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Load(filepath.Join(t.TempDir(), "affinity.json"), "qq")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	return store
}

func TestAddDailyCapPositive(t *testing.T) {
	s := newTestStore(t)
	// 20 calls of +5 — only the first 15 points should land.
	for i := 0; i < 20; i++ {
		s.Add("qq", "bot1", "u1", 5)
	}
	if got := s.Score("qq", "bot1", "u1"); got != dailyDeltaCap {
		t.Fatalf("score = %d, want %d (daily cap)", got, dailyDeltaCap)
	}
}

func TestAddDailyCapPartialClamp(t *testing.T) {
	s := newTestStore(t)
	s.Add("qq", "bot1", "u1", 12)
	if got := s.Add("qq", "bot1", "u1", 5); got != 15 {
		t.Fatalf("Add past cap = %d, want 15 (5 trimmed to 3)", got)
	}
}

func TestAddDailyCapResetsNextDay(t *testing.T) {
	s := newTestStore(t)
	for i := 0; i < 5; i++ {
		s.Add("qq", "bot1", "u1", 5) // hits the 15 cap
	}
	if got := s.Score("qq", "bot1", "u1"); got != 15 {
		t.Fatalf("score = %d, want 15", got)
	}
	// Pretend it is tomorrow — the cap must roll over.
	s.dailyDate = time.Now().Add(-24 * time.Hour).Format("2006-01-02")
	if got := s.Add("qq", "bot1", "u1", 5); got != 20 {
		t.Fatalf("Add after rollover = %d, want 20", got)
	}
}

func TestAddDailyCapNegative(t *testing.T) {
	s := newTestStore(t)
	s.Add("qq", "bot1", "u1", 100) // seed above the cap first via direct score
	// Bypass the daily cap for the seed: set the raw score.
	s.scores[Key("qq", "bot1", "u1")] = 100
	s.dailyDelta[Key("qq", "bot1", "u1")] = 0

	got := 100
	for i := 0; i < 10; i++ {
		got = s.Add("qq", "bot1", "u1", -5)
	}
	// Net -15/day; floor 0 → score stops at 85, not lower.
	if got != 85 {
		t.Fatalf("negative cap: score = %d, want 85", got)
	}
	// Further deductions this "day" do nothing.
	if again := s.Add("qq", "bot1", "u1", -5); again != 85 {
		t.Fatalf("post-cap deduction = %d, want 85", again)
	}
}

func TestAddDailyCapIsPerUser(t *testing.T) {
	s := newTestStore(t)
	for i := 0; i < 5; i++ {
		s.Add("qq", "bot1", "u1", 5)
	}
	if got := s.Score("qq", "bot1", "u1"); got != 15 {
		t.Fatalf("u1 score = %d, want 15", got)
	}
	// A different user still has their own budget.
	if got := s.Add("qq", "bot1", "u2", 5); got != 5 {
		t.Fatalf("u2 first add = %d, want 5", got)
	}
}
