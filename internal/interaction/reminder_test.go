package interaction

import (
	"testing"
	"time"
)

func TestParseDueFormats(t *testing.T) {
	now := time.Date(2026, 10, 28, 15, 0, 0, 0, time.Local)
	cases := []struct {
		in   string
		want time.Time
	}{
		{"30m", now.Add(30 * time.Minute)},
		{"2h", now.Add(2 * time.Hour)},
		{"+1d", now.Add(24 * time.Hour)},
		{"20:30", time.Date(2026, 10, 28, 20, 30, 0, 0, time.Local)},
		{"08:00", time.Date(2026, 10, 29, 8, 0, 0, 0, time.Local)}, // past today -> tomorrow
		{"2026-10-30 08:00", time.Date(2026, 10, 30, 8, 0, 0, 0, time.Local)},
		{"2026-10-30", time.Date(2026, 10, 30, 9, 0, 0, 0, time.Local)}, // bare date -> 9am
	}
	for _, c := range cases {
		got, err := parseDue(c.in, now)
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if !got.Equal(c.want) {
			t.Fatalf("%q: got %v want %v", c.in, got, c.want)
		}
	}
	if _, err := parseDue("下周三早上", now); err == nil {
		t.Fatal("nonsense accepted")
	}
}
