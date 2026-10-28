package bot

import (
	"testing"
	"time"
)

func TestPromiseDueAtRelativeDays(t *testing.T) {
	now := time.Date(2026, 10, 28, 15, 0, 0, 0, time.Local) // Wednesday
	cases := []struct {
		text string
		want time.Time
	}{
		{"我明天给你带", time.Date(2026, 10, 29, 19, 0, 0, 0, time.Local)},
		{"我后天帮你搬", time.Date(2026, 10, 30, 19, 0, 0, 0, time.Local)},
		{"我大后天还你", time.Date(2026, 10, 31, 19, 0, 0, 0, time.Local)},
		{"我明天上午给你送", time.Date(2026, 10, 29, 9, 0, 0, 0, time.Local)},
		{"我明天晚上请你吃", time.Date(2026, 10, 29, 20, 0, 0, 0, time.Local)},
		// 周三 said on Wednesday means next week, not today.
		{"我周三交作业", time.Date(2026, 11, 4, 19, 0, 0, 0, time.Local)},
		{"我周五来接你", time.Date(2026, 10, 30, 19, 0, 0, 0, time.Local)},
		{"我周日去看你", time.Date(2026, 11, 1, 19, 0, 0, 0, time.Local)},
		{"我10月30日给你", time.Date(2026, 10, 30, 19, 0, 0, 0, time.Local)},
		{"我10-31还书", time.Date(2026, 10, 31, 19, 0, 0, 0, time.Local)},
		{"我到时候给你带", time.Time{}}, // no date — no reminder
		{"我保证下次注意", time.Time{}},
	}
	for _, c := range cases {
		got := promiseDueAt(c.text, now)
		if !got.Equal(c.want) {
			t.Errorf("promiseDueAt(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

func TestPromiseDueAtPastDateRollsToNextYear(t *testing.T) {
	now := time.Date(2026, 10, 28, 15, 0, 0, 0, time.Local)
	got := promiseDueAt("我9月1日给你", now)
	want := time.Date(2027, 9, 1, 19, 0, 0, 0, time.Local)
	if !got.Equal(want) {
		t.Fatalf("promiseDueAt = %v, want %v", got, want)
	}
}
