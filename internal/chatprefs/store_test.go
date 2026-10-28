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
	s, err := Load(filepath.Join(t.TempDir(), "prefs.json"), "qq-onebot")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	return s
}

func TestFactRoundTripWithTimestamps(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 10, 24, 15, 0, 0, 0, time.Local)
	s.SetClock(func() time.Time { return now })
	if err := s.AddFact("qq-onebot", "10001", "group", "1", "阿茶生日10.28"); err != nil {
		t.Fatalf("AddFact() error = %v", err)
	}
	facts := s.FactsSnapshot("qq-onebot", "10001", "group", "1")
	if len(facts) != 1 || facts[0].Text != "阿茶生日10.28" ||
		facts[0].CreatedAt.IsZero() {
		t.Fatalf("facts = %#v", facts)
	}
}

func TestReplaceFactUpdatesMatchOrAppendsNew(t *testing.T) {
	s := newTestStore(t)
	_ = s.AddFact("qq-onebot", "10001", "group", "1", "阿茶生日10.28")
	if err := s.ReplaceFact("qq-onebot", "10001", "group", "1", "阿茶生日", "阿茶生日11.02"); err != nil {
		t.Fatalf("ReplaceFact() error = %v", err)
	}
	facts := s.FactsSnapshot("qq-onebot", "10001", "group", "1")
	if len(facts) != 1 || facts[0].Text != "阿茶生日11.02" {
		t.Fatalf("after replace facts = %#v", facts)
	}
	// No match — replace degrades to add instead of deadlocking.
	if err := s.ReplaceFact("qq-onebot", "10001", "group", "1", "完全不相干的东西", "新的记忆事实"); err != nil {
		t.Fatalf("ReplaceFact(fallback) error = %v", err)
	}
	if len(s.FactsSnapshot("qq-onebot", "10001", "group", "1")) != 2 {
		t.Fatalf("fallback should append: %#v", s.FactsSnapshot("qq-onebot", "10001", "group", "1"))
	}
}

func TestForgetFactNeedsStrongMatch(t *testing.T) {
	s := newTestStore(t)
	_ = s.AddFact("qq-onebot", "10001", "group", "1", "阿茶生日10.28")
	// A vague target must not erase a loosely related memory.
	if err := s.ForgetFact("qq-onebot", "10001", "group", "1", "随便说点什么"); err == nil {
		t.Fatal("vague forget should fail")
	}
	if err := s.ForgetFact("qq-onebot", "10001", "group", "1", "阿茶生日10.28"); err != nil {
		t.Fatalf("exact forget error = %v", err)
	}
	if len(s.FactsSnapshot("qq-onebot", "10001", "group", "1")) != 0 {
		t.Fatal("fact should be forgotten")
	}
}

func TestRecallFactsRanksAndMarksSeen(t *testing.T) {
	s := newTestStore(t)
	base := time.Date(2026, 10, 24, 15, 0, 0, 0, time.Local)
	clock := base
	s.SetClock(func() time.Time { return clock })
	_ = s.AddFact("qq-onebot", "10001", "group", "1", "阿茶生日10.28")
	_ = s.AddFact("qq-onebot", "10001", "group", "1", "群友都不吃辣")
	_ = s.AddFact("qq-onebot", "10001", "group", "1", "阿茶喜欢喝冰美式咖啡")
	clock = base.Add(2 * time.Hour)

	// Query mentioning 阿茶+咖啡 should rank the coffee fact top.
	hits := s.RecallFacts("qq-onebot", "10001", "group", "1", "阿茶今天还喝咖啡吗", 5)
	if len(hits) == 0 {
		t.Fatal("no recall hits")
	}
	if hits[0].Text != "阿茶喜欢喝冰美式咖啡" {
		t.Fatalf("top hit = %q, want coffee fact", hits[0].Text)
	}
	// Unrelated query gets nothing.
	if hits := s.RecallFacts("qq-onebot", "10001", "group", "1", "今天天气怎么样", 5); len(hits) != 0 {
		t.Fatalf("unrelated recall = %#v", hits)
	}
	// Recall bumped LastSeenAt beyond CreatedAt.
	for _, f := range s.FactsSnapshot("qq-onebot", "10001", "group", "1") {
		if f.Text == "阿茶喜欢喝冰美式咖啡" && !f.LastSeenAt.After(f.CreatedAt) {
			t.Fatal("recalled fact LastSeenAt not bumped")
		}
	}
}

func TestPersonaProfileScopedByPersona(t *testing.T) {
	s := newTestStore(t)
	_ = s.SetProfile("qq-onebot", "七七", "10001", "爱讲冷笑话")
	if got := s.Profile("qq-onebot", "七七", "10001").Text; got != "爱讲冷笑话" {
		t.Fatalf("profile = %q", got)
	}
	if got := s.Profile("qq-onebot", "闺女", "10001").Text; got != "" {
		t.Fatalf("profile leaked across personas: %q", got)
	}
}

func TestProfileStripsLinksSoPromosCantTravel(t *testing.T) {
	s := newTestStore(t)
	// A promo link learned in group A must not resurface when this
	// user's profile is injected in another chat.
	if err := s.SetProfile("qq-onebot", "七七", "10001",
		"卖卡的，找他买 https://shop.example.com/x 或者 wzyp.cn/item/1"); err != nil {
		t.Fatal(err)
	}
	got := s.Profile("qq-onebot", "七七", "10001").Text
	if strings.Contains(got, "http") || strings.Contains(got, "wzyp.cn") {
		t.Fatalf("link leaked into profile: %q", got)
	}
	if got != "卖卡的，找他买 或者" {
		t.Fatalf("profile text = %q", got)
	}
}

func TestUserCardPersistsBadgesAcrossRefreshes(t *testing.T) {
	s := newTestStore(t)
	if err := s.SetUserBadge("qq-onebot", "10001", "30003",
		GroupBadge{Role: "admin", Title: "大佬"}); err != nil {
		t.Fatal(err)
	}
	// Profile refresh keeps group badges — SetUserCard must not wipe them.
	if err := s.SetUserCard("qq-onebot", "10001", UserCard{
		Nick: "阿茶", Sex: "female", Age: 24, Friend: true,
		FetchedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	card, ok := s.UserCard("qq-onebot", "10001")
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

func TestSameIDsAreIsolatedAcrossPlatforms(t *testing.T) {
	s := newTestStore(t)
	// Same group id and user id, two different platforms — writes to one
	// must never surface in the other.
	_ = s.AddFact("qq-onebot", "10001", "group", "1", "QQ 侧的记忆")
	_ = s.AddFact("douyin", "9999", "group", "1", "抖音侧的记忆")
	if got := s.FactsSnapshot("qq-onebot", "10001", "group", "1"); len(got) != 1 ||
		got[0].Text != "QQ 侧的记忆" {
		t.Fatalf("qq facts polluted: %#v", got)
	}
	if got := s.FactsSnapshot("douyin", "9999", "group", "1"); len(got) != 1 ||
		got[0].Text != "抖音侧的记忆" {
		t.Fatalf("douyin facts polluted: %#v", got)
	}
	_ = s.SetImpression("qq-onebot", "10001", "group", "1", "10001", "QQ 印象")
	if got := s.Impression("douyin", "9999", "group", "1", "10001").Text; got != "" {
		t.Fatalf("impression leaked across platforms: %q", got)
	}
	_ = s.SetProfile("qq-onebot", "七七", "10001", "QQ 画像")
	if got := s.Profile("douyin", "七七", "10001").Text; got != "" {
		t.Fatalf("profile leaked across platforms: %q", got)
	}
	if err := s.SetUserBadge("qq-onebot", "10001", "1",
		GroupBadge{Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.UserCard("douyin", "10001"); ok {
		t.Fatal("user card leaked across platforms")
	}
}

func TestLegacyMigrationKeepsScopedDataOnCollision(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prefs.json")
	// File already has a scoped entry AND a legacy entry for the same
	// chat — the scoped (newer) copy must win.
	legacy := `{"chats":{` +
		`"qq-onebot:group:1":{"facts":["scoped 新数据"]},` +
		`"group:1":{"facts":["legacy 旧数据"]}}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path, "qq-onebot")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	facts := s.FactsSnapshot("qq-onebot", "10001", "group", "1")
	if len(facts) != 1 || facts[0].Text != "scoped 新数据" {
		t.Fatalf("collision should keep scoped data, got %#v", facts)
	}
	// And the migration persisted: a reload must still see only scoped.
	s2, err := Load(path, "qq-onebot")
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.FactsSnapshot("qq-onebot", "10001", "group", "1"); len(got) != 1 {
		t.Fatalf("migration did not persist: %#v", got)
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
	s, err := Load(path, "qq-onebot")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	facts := s.FactsSnapshot("qq-onebot", "10001", "group", "1")
	if len(facts) != 2 || facts[0].Text != "老事实一" {
		t.Fatalf("migrated facts = %#v", facts)
	}
	if got := s.Impression("qq-onebot", "10001", "group", "1", "10001").Text; got != "老印象" {
		t.Fatalf("migrated impression = %q", got)
	}
	// Rewrite persists the new v2 layout without losing data.
	if err := s.AddFact("qq-onebot", "10001", "group", "1", "新的记忆事实"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"chats"`) {
		t.Fatal("expected v2 wrapped layout after save")
	}
}

func TestRecallFactsSemanticAndArchivePool(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return now })

	// First fact: sleep/fatigue domain, will be pushed out of the 40-slot
	// active Facts ring into the long-term Archive.
	if err := s.AddFact("qq-onebot", "10001", "private", "u1", "他经常熬夜通宵写代码"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxFactsPerChat+5; i++ {
		now = now.Add(time.Minute)
		if err := s.AddFact("qq-onebot", "10001", "private", "u1", "日常琐事编号"+string(rune('A'+i%20))+string(rune('0'+i/20))); err != nil {
			t.Fatal(err)
		}
	}

	// Active Facts is capped at 40, so the earliest sleep fact is now in Archive.
	if active := s.FactsSnapshot("qq-onebot", "10001", "private", "u1"); len(active) != maxFactsPerChat {
		t.Fatalf("active facts = %d, want %d", len(active), maxFactsPerChat)
	}

	// Query has zero literal bigram overlap with "他经常熬夜通宵写代码",
	// but hits the sleep/fatigue concept cluster and searches Archive.
	recalled := s.RecallFacts("qq-onebot", "10001", "private", "u1", "最近睡眠不足好困想早点休息", 5)
	found := false
	for _, f := range recalled {
		if f.Text == "他经常熬夜通宵写代码" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected archived semantic memory to be recalled, got %#v", recalled)
	}
}


func TestArchiveFadeFoldsOldestIntoLegacy(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return now })
	e := s.entry("qq-onebot", "10001", "private", "u1")

	// Fill the archive to capacity with distinct old memories.
	for i := 0; i < maxArchivePerChat; i++ {
		stamp := now.Add(-time.Duration(10000-i) * time.Minute)
		e.Archive = append(e.Archive, fact{
			Text:       "旧记忆片段" + string(rune('A'+i%26)) + string(rune('a'+i/26%26)),
			CreatedAt:  stamp,
			LastSeenAt: stamp,
		})
	}
	before := len(e.Archive)
	s.mu.Lock()
	archiveFactLocked(e, fact{Text: "刚挤进来的新记忆", CreatedAt: now, LastSeenAt: now})
	s.mu.Unlock()

	// 10 oldest folded into 1 legacy line, then the evicted appended:
	// before - 10 + 1 + 1.
	want := before - 10 + 2
	if len(e.Archive) != want {
		t.Fatalf("archive len = %d, want %d", len(e.Archive), want)
	}
	legacy := 0
	for _, f := range e.Archive {
		if strings.HasPrefix(f.Text, "很久以前:") {
			legacy++
		}
	}
	if legacy != 1 {
		t.Fatalf("legacy folded lines = %d, want 1", legacy)
	}
}

func TestForgetFactReachesArchive(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return now })
	e := s.entry("qq-onebot", "10001", "private", "u1")
	e.Archive = append(e.Archive, fact{
		Text: "他讨厌吃香菜这件事", CreatedAt: now, LastSeenAt: now,
	})

	if err := s.ForgetFact("qq-onebot", "10001", "private", "u1", "讨厌吃香菜"); err != nil {
		t.Fatalf("ForgetFact archive miss: %v", err)
	}
	if len(e.Archive) != 0 {
		t.Fatalf("archive still holds %d facts", len(e.Archive))
	}
}


func TestFactForUserTagsSpeaker(t *testing.T) {
	s := newTestStore(t)
	if err := s.AddFactForUser("qq-onebot", "10001", "group", "g1",
		"u42", "阿伟", "他喜欢猫"); err != nil {
		t.Fatal(err)
	}
	facts := s.FactsSnapshot("qq-onebot", "10001", "group", "g1")
	if len(facts) != 1 {
		t.Fatalf("facts=%d", len(facts))
	}
	if facts[0].UserID != "u42" || facts[0].UserName != "阿伟" {
		t.Fatalf("speaker lost: %+v", facts[0])
	}
	// Plain AddFact stays untagged.
	if err := s.AddFact("qq-onebot", "10001", "group", "g1", "群公告周五截止"); err != nil {
		t.Fatal(err)
	}
	for _, f := range s.FactsSnapshot("qq-onebot", "10001", "group", "g1") {
		if f.Text == "群公告周五截止" && (f.UserID != "" || f.UserName != "") {
			t.Fatalf("anonymous fact got tagged: %+v", f)
		}
	}
}

// A memory she never re-lives loses retrieval strength: the same line
// scores half after one half-life and eventually drops below the recall
// floor — she half-forgets, exactly like a person.
func TestRecallStrengthDecaysWithAge(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	query := "阿茶喝咖啡"
	grams := textBigrams(query)
	fresh := fact{Text: "阿茶最近爱上手冲咖啡", CreatedAt: now, LastSeenAt: now}
	s1 := scoreFactHybrid(fresh, query, grams, nil, now)
	stale := fact{Text: fresh.Text, CreatedAt: now.Add(-factRecallHalfLife),
		LastSeenAt: now.Add(-factRecallHalfLife)}
	s2 := scoreFactHybrid(stale, query, grams, nil, now)
	if s1 <= 0 {
		t.Fatalf("fresh score=%d, want positive", s1)
	}
	if s2 >= s1 || s2 > s1/2+1 {
		t.Fatalf("one half-life should ~halve the score: fresh=%d stale=%d", s1, s2)
	}
	// Four half-lives (~120d) on an ordinary chat memory must sink below
	// the recall floor entirely.
	s4 := newTestStore(t)
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	clock := base
	s4.SetClock(func() time.Time { return clock })
	if err := s4.AddFact("qq-onebot", "10001", "group", "g1", "阿茶爱喝手冲咖啡"); err != nil {
		t.Fatal(err)
	}
	if got := s4.RecallFacts("qq-onebot", "10001", "group", "g1", "咖啡", 3); len(got) == 0 {
		t.Fatal("fresh fact should recall")
	}
	clock = base.Add(4*factRecallHalfLife + time.Hour)
	if got := s4.RecallFacts("qq-onebot", "10001", "group", "g1", "咖啡", 3); len(got) != 0 {
		t.Fatalf("stale fact should sink below recall floor, got %+v", got)
	}
}

// Spontaneous recall prefers the half-forgotten band over the truly
// ancient: a person goes "哦对了" about something on the edge of fading,
// not about something buried for a year.
func TestColdFactPrefersHalfForgottenBand(t *testing.T) {
	s := newTestStore(t)
	base := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	clock := base.Add(-200 * 24 * time.Hour)
	s.SetClock(func() time.Time { return clock })
	_ = s.AddFact("qq-onebot", "10001", "group", "g1", "很久前的事")
	clock = base.Add(-5 * 24 * time.Hour)
	_ = s.AddFact("qq-onebot", "10001", "group", "g1", "五天前的事")
	clock = base.Add(-24 * time.Hour)
	_ = s.AddFact("qq-onebot", "10001", "group", "g1", "昨天的事")

	got, ok := s.ColdFact("qq-onebot", "10001", "group", "g1", base, 72*time.Hour)
	if !ok || got.Text != "五天前的事" {
		t.Fatalf("band pick=%+v ok=%v, want 五天前的事", got, ok)
	}

	// No band candidates → falls back to the genuinely coldest trace.
	s2 := newTestStore(t)
	clock = base.Add(-200 * 24 * time.Hour)
	s2.SetClock(func() time.Time { return clock })
	_ = s2.AddFact("qq-onebot", "10001", "group", "g1", "远古记忆")
	clock = base.Add(-100 * 24 * time.Hour)
	_ = s2.AddFact("qq-onebot", "10001", "group", "g1", "较老的记忆")
	got, ok = s2.ColdFact("qq-onebot", "10001", "group", "g1", base, 72*time.Hour)
	if !ok || got.Text != "远古记忆" {
		t.Fatalf("fallback pick=%+v ok=%v, want 远古记忆", got, ok)
	}
}
