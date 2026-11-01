package proactive

import (
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/chatprefs"
)

func TestShelveThoughtStripsMarkersAndStores(t *testing.T) {
	prefs, err := chatprefs.Load(filepath.Join(t.TempDir(), "prefs.json"), "qq-onebot")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	now := time.Date(2026, 11, 3, 9, 0, 0, 0, time.Local)
	e := New(Config{}, Deps{
		ChatPrefs: prefs,
		Now:       func() time.Time { return now },
	})

	e.shelveThought("qq-onebot", "10001", "group", "1",
		"想问他昨天说的事成了没 [[MEME:猫猫]]\n[[OOPS]]别发这条[[/OOPS]]")

	got := prefs.PendingThought("qq-onebot", "10001", "group", "1", now)
	if got != "想问他昨天说的事成了没 别发这条" {
		t.Fatalf("PendingThought = %q", got)
	}
	// An all-marker reply shelves nothing.
	e.shelveThought("qq-onebot", "10001", "group", "2", "[[SELFIE:开心]]")
	if got := prefs.PendingThought("qq-onebot", "10001", "group", "2", now); got != "" {
		t.Fatalf("marker-only PendingThought = %q", got)
	}
}

func TestOccasionDayCoversBirthdayAndAnniversary(t *testing.T) {
	cases := []struct {
		pattern *regexp.Regexp
		text    string
		want    string
	}{
		{birthdayPattern, "阿茶生日10.28", "10-28"},
		{birthdayPattern, "生日10月28日的群友", "10-28"},
		{birthdayPattern, "10.28生日", "10-28"},
		{anniversaryPattern, "纪念日10.28", "10-28"},
		{anniversaryPattern, "认识3.15 两周年", "03-15"},
		{anniversaryPattern, "在一起6月1日", "06-01"},
		{anniversaryPattern, "11.03周年", "11-03"},
		{anniversaryPattern, "他说今天天气不错", ""},
		{birthdayPattern, "他喜欢看电影", ""},
	}
	for _, c := range cases {
		if got := occasionDay(c.pattern, c.text); got != c.want {
			t.Errorf("occasionDay(%q) = %q, want %q", c.text, got, c.want)
		}
	}
}
