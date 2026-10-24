package chatprefs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Load(filepath.Join(t.TempDir(), "prefs.json"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	return s
}

func TestFactRoundTripWithTimestamps(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 10, 24, 15, 0, 0, 0, time.Local)
	s.SetClock(func() time.Time { return now })
	if err := s.AddFact("group", "1", "阿茶生日10.28"); err != nil {
		t.Fatalf("AddFact() error = %v", err)
	}
	facts := s.FactsSnapshot("group", "1")
	if len(facts) != 1 || facts[0].Text != "阿茶生日10.28" ||
		facts[0].CreatedAt.IsZero() {
		t.Fatalf("facts = %#v", facts)
	}
}

func TestReplaceFactUpdatesMatchOrAppendsNew(t *testing.T) {
	s := newTestStore(t)
	_ = s.AddFact("group", "1", "阿茶生日10.28")
	if err := s.ReplaceFact("group", "1", "阿茶生日", "阿茶生日11.02"); err != nil {
		t.Fatalf("ReplaceFact() error = %v", err)
	}
	facts := s.FactsSnapshot("group", "1")
	if len(facts) != 1 || facts[0].Text != "阿茶生日11.02" {
		t.Fatalf("after replace facts = %#v", facts)
	}
	// No match — replace degrades to add instead of deadlocking.
	if err := s.ReplaceFact("group", "1", "完全不相干的东西", "新事实"); err != nil {
		t.Fatalf("ReplaceFact(fallback) error = %v", err)
	}
	if len(s.FactsSnapshot("group", "1")) != 2 {
		t.Fatalf("fallback should append: %#v", s.FactsSnapshot("group", "1"))
	}
}

func TestForgetFactNeedsStrongMatch(t *testing.T) {
	s := newTestStore(t)
	_ = s.AddFact("group", "1", "阿茶生日10.28")
	// A vague target must not erase a loosely related memory.
	if err := s.ForgetFact("group", "1", "随便说点什么"); err == nil {
		t.Fatal("vague forget should fail")
	}
	if err := s.ForgetFact("group", "1", "阿茶生日10.28"); err != nil {
		t.Fatalf("exact forget error = %v", err)
	}
	if len(s.FactsSnapshot("group", "1")) != 0 {
		t.Fatal("fact should be forgotten")
	}
}

func TestRecallFactsRanksAndMarksSeen(t *testing.T) {
	s := newTestStore(t)
	base := time.Date(2026, 10, 24, 15, 0, 0, 0, time.Local)
	clock := base
	s.SetClock(func() time.Time { return clock })
	_ = s.AddFact("group", "1", "阿茶生日10.28")
	_ = s.AddFact("group", "1", "群友都不吃辣")
	_ = s.AddFact("group", "1", "阿茶喜欢喝冰美式咖啡")
	clock = base.Add(2 * time.Hour)

	// Query mentioning 阿茶+咖啡 should rank the coffee fact top.
	hits := s.RecallFacts("group", "1", "阿茶今天还喝咖啡吗", 5)
	if len(hits) == 0 {
		t.Fatal("no recall hits")
	}
	if hits[0].Text != "阿茶喜欢喝冰美式咖啡" {
		t.Fatalf("top hit = %q, want coffee fact", hits[0].Text)
	}
	// Unrelated query gets nothing.
	if hits := s.RecallFacts("group", "1", "今天天气怎么样", 5); len(hits) != 0 {
		t.Fatalf("unrelated recall = %#v", hits)
	}
	// Recall bumped LastSeenAt beyond CreatedAt.
	for _, f := range s.FactsSnapshot("group", "1") {
		if f.Text == "阿茶喜欢喝冰美式咖啡" && !f.LastSeenAt.After(f.CreatedAt) {
			t.Fatal("recalled fact LastSeenAt not bumped")
		}
	}
}

func TestPersonaProfileScopedByPersona(t *testing.T) {
	s := newTestStore(t)
	_ = s.SetProfile("七七", "10001", "爱讲冷笑话")
	if got := s.Profile("七七", "10001").Text; got != "爱讲冷笑话" {
		t.Fatalf("profile = %q", got)
	}
	if got := s.Profile("闺女", "10001").Text; got != "" {
		t.Fatalf("profile leaked across personas: %q", got)
	}
}

func TestUserCardPersistsBadgesAcrossRefreshes(t *testing.T) {
	s := newTestStore(t)
	if err := s.SetUserBadge("10001", "30003",
		GroupBadge{Role: "admin", Title: "大佬"}); err != nil {
		t.Fatal(err)
	}
	// Profile refresh keeps group badges — SetUserCard must not wipe them.
	if err := s.SetUserCard("10001", UserCard{
		Nick: "阿茶", Sex: "female", Age: 24, Friend: true,
		FetchedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	card, ok := s.UserCard("10001")
	if !ok || card.Nick != "阿茶" || !card.Friend {
		t.Fatalf("card = %#v ok=%v", card, ok)
	}
	if card.Badges["30003"].Role != "admin" || card.Badges["30003"].Title != "大佬" {
		t.Fatalf("badge lost on refresh: %#v", card.Badges)
	}
	// A badge in another group stays isolated.
	if _, ok := card.Badges["99999"]; ok {
		t.Fatal("badge leaked across groups")
	}
}

func TestLegacyJSONMigratesStringFacts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prefs.json")
	legacy := `{"group:1":{"facts":["老事实一","老事实二"],` +
		`"impressions":{"10001":"老印象"}}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	facts := s.FactsSnapshot("group", "1")
	if len(facts) != 2 || facts[0].Text != "老事实一" {
		t.Fatalf("migrated facts = %#v", facts)
	}
	if got := s.Impression("group", "1", "10001").Text; got != "老印象" {
		t.Fatalf("migrated impression = %q", got)
	}
	// Rewrite persists the new v2 layout without losing data.
	if err := s.AddFact("group", "1", "新事实"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"chats"`) {
		t.Fatal("expected v2 wrapped layout after save")
	}
}
