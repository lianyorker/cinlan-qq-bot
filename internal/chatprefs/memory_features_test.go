package chatprefs

import (
	"strings"
	"testing"
	"time"
)

func TestBumpMoodBleedsNegativeIntoSelfMood(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 11, 3, 10, 0, 0, 0, time.Local)

	if got, err := s.BumpMood("qq-onebot", "10001", "group", "1", -8, now); err != nil || got != -8 {
		t.Fatalf("BumpMood() = %d, %v; want -8", got, err)
	}
	// The room keeps its own meter; the account carries half.
	if got := s.MoodScore("qq-onebot", "10001", "group", "1", now); got != -8 {
		t.Fatalf("MoodScore(group) = %d, want -8", got)
	}
	if got := s.EffectiveMood("qq-onebot", "10001", "private", "9", now); got != -4 {
		t.Fatalf("EffectiveMood(private) = %d, want -4", got)
	}
	if got := s.EffectiveMood("qq-onebot", "10001", "group", "1", now); got != -12 {
		t.Fatalf("EffectiveMood(group) = %d, want -12", got)
	}
	// Positive bumps never leave the room.
	if _, err := s.BumpMood("qq-onebot", "10001", "group", "1", 10, now); err != nil {
		t.Fatalf("BumpMood() error = %v", err)
	}
	if got := s.EffectiveMood("qq-onebot", "10001", "private", "9", now); got != -4 {
		t.Fatalf("EffectiveMood after positive bump = %d, want -4", got)
	}
	// Self mood decays like chat mood: 12h * 6pts washes out -4.
	later := now.Add(12 * time.Hour)
	if got := s.EffectiveMood("qq-onebot", "10001", "private", "9", later); got != 0 {
		t.Fatalf("EffectiveMood decayed = %d, want 0", got)
	}
	// Another account's mood stays its own.
	if got := s.EffectiveMood("qq-onebot", "99999", "private", "9", now); got != 0 {
		t.Fatalf("other account EffectiveMood = %d, want 0", got)
	}
}

func TestPendingThoughtLifecycle(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 11, 3, 8, 0, 0, 0, time.Local)

	if got := s.PendingThought("qq-onebot", "10001", "group", "1", now); got != "" {
		t.Fatalf("empty PendingThought = %q", got)
	}
	s.SetPendingThought("qq-onebot", "10001", "group", "1", "想问他昨天说的事成了没", now)
	if got := s.PendingThought("qq-onebot", "10001", "group", "1", now.Add(5*time.Hour)); got == "" {
		t.Fatal("PendingThought dropped a warm thought")
	}
	// A shelved line counts as thought material.
	if !s.HasThoughtMaterial("qq-onebot", "10001", "group", "1", now) {
		t.Fatal("HasThoughtMaterial ignored pending thought")
	}
	s.ClearPendingThought("qq-onebot", "10001", "group", "1")
	if got := s.PendingThought("qq-onebot", "10001", "group", "1", now); got != "" {
		t.Fatalf("cleared PendingThought = %q", got)
	}
	// Stale thoughts expire on read.
	s.SetPendingThought("qq-onebot", "10001", "group", "1", "两天前的念头", now)
	if got := s.PendingThought("qq-onebot", "10001", "group", "1", now.Add(40*time.Hour)); got != "" {
		t.Fatalf("stale PendingThought = %q", got)
	}
}

func TestAddFactMarksLikelyConflict(t *testing.T) {
	s := newTestStore(t)

	if err := s.AddFact("qq-onebot", "10001", "group", "1", "他不喜欢喝咖啡"); err != nil {
		t.Fatalf("AddFact() error = %v", err)
	}
	if err := s.AddFact("qq-onebot", "10001", "group", "1", "他现在喜欢喝咖啡"); err != nil {
		t.Fatalf("AddFact() error = %v", err)
	}
	facts := s.FactsSnapshot("qq-onebot", "10001", "group", "1")
	if len(facts) != 2 {
		t.Fatalf("facts = %d, want 2", len(facts))
	}
	if facts[1].Prev != "他不喜欢喝咖啡" {
		t.Fatalf("Prev = %q, want 他不喜欢喝咖啡", facts[1].Prev)
	}
	// The older memory is left intact — the note rides on the new one.
	if facts[0].Text != "他不喜欢喝咖啡" || facts[0].Prev != "" {
		t.Fatalf("old fact mutated: %#v", facts[0])
	}
	// Ledger-style lines are exempt from contradiction marking.
	if err := s.AddFact("qq-onebot", "10001", "group", "1", "承诺·明天叫他起床"); err != nil {
		t.Fatalf("AddFact() error = %v", err)
	}
	facts = s.FactsSnapshot("qq-onebot", "10001", "group", "1")
	if facts[len(facts)-1].Prev != "" {
		t.Fatalf("promise fact got Prev %q", facts[len(facts)-1].Prev)
	}
}

func TestCorrectFactFragmentPatchesInPlace(t *testing.T) {
	s := newTestStore(t)
	_ = s.AddFact("qq-onebot", "10001", "group", "1", "他约的是周五看电影")

	// A fragment correction keeps the rest of the memory intact.
	if !s.CorrectFactFragment("qq-onebot", "10001", "group", "1", "周五", "周六") {
		t.Fatal("CorrectFactFragment missed a literal fragment")
	}
	facts := s.FactsSnapshot("qq-onebot", "10001", "group", "1")
	if facts[0].Text != "他约的是周六看电影" {
		t.Fatalf("patched fact = %q", facts[0].Text)
	}
	// A fragment nobody remembered touches nothing.
	if s.CorrectFactFragment("qq-onebot", "10001", "group", "1", "周日", "周一") {
		t.Fatal("CorrectFactFragment matched a phantom fragment")
	}
	// Resolving a conflict drops the Prev hedge.
	_ = s.AddFact("qq-onebot", "10001", "group", "1", "他现在约的是周六看电影")
	facts = s.FactsSnapshot("qq-onebot", "10001", "group", "1")
	if facts[1].Prev == "" {
		t.Fatal("expected Prev on the conflicting line")
	}
	// Patch the newer line through a fragment unique to it — the
	// corrected fact drops its Prev hedge since the tension is resolved.
	s.CorrectFactFragment("qq-onebot", "10001", "group", "1", "现在", "昨天")
	facts = s.FactsSnapshot("qq-onebot", "10001", "group", "1")
	if facts[1].Prev != "" || !strings.Contains(facts[1].Text, "昨天") {
		t.Fatalf("correction left Prev=%q text=%q", facts[1].Prev, facts[1].Text)
	}
}

func TestFirstContactOfDayStampsOnce(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 11, 3, 8, 30, 0, 0, time.Local)

	if !s.FirstContactOfDay("qq-onebot", "10001", "private", "9", now) {
		t.Fatal("first message of the day not reported")
	}
	if s.FirstContactOfDay("qq-onebot", "10001", "private", "9", now.Add(3*time.Hour)) {
		t.Fatal("same-day message double-stamped")
	}
	if !s.FirstContactOfDay("qq-onebot", "10001", "private", "9", now.Add(25*time.Hour)) {
		t.Fatal("next-day first message not reported")
	}
	// Stamps are per-chat — another chat still owes its greeting.
	if !s.FirstContactOfDay("qq-onebot", "10001", "group", "1", now) {
		t.Fatal("other chat first contact not reported")
	}
}

func TestObserveUserStyleAndLine(t *testing.T) {
	s := newTestStore(t)
	for i := 0; i < 20; i++ {
		s.ObserveUserStyle("qq-onebot", "42", "哈哈笑死")
	}
	card, ok := s.UserCard("qq-onebot", "42")
	if !ok {
		t.Fatal("UserCard missing after observations")
	}
	if card.Style.Samples != 20 {
		t.Fatalf("Samples = %d, want 20", card.Style.Samples)
	}
	if card.Style.AvgLen <= 0 || card.Style.AvgLen > 6 {
		t.Fatalf("AvgLen = %v, want ~4", card.Style.AvgLen)
	}
	line := UserStyleLine(card.Style)
	if !strings.Contains(line, "说话偏短") || !strings.Contains(line, "笑死") {
		t.Fatalf("UserStyleLine = %q, want 偏短+笑死", line)
	}
	// Too few samples stays silent.
	if got := UserStyleLine(UserStyle{Samples: 3, AvgLen: 4}); got != "" {
		t.Fatalf("early UserStyleLine = %q", got)
	}
	// Profile refresh keeps the learned voice like it keeps badges.
	if err := s.SetUserCard("qq-onebot", "42", UserCard{Nick: "阿茶", FetchedAt: time.Now()}); err != nil {
		t.Fatalf("SetUserCard() error = %v", err)
	}
	card, _ = s.UserCard("qq-onebot", "42")
	if card.Style.Samples != 20 {
		t.Fatalf("Style lost on refresh: %#v", card.Style)
	}
}
