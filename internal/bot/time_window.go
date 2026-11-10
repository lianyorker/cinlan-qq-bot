package bot

import (
	"regexp"
	"strconv"
	"time"
)

// timeWindow bounds a recall query to a named period — "上周说的瑞幸"
// should search last week's turns, not the whole archive. Inspired by
// Hindsight's temporal retrieval arm: the time word becomes a filter
// BEFORE semantic scoring runs.
type timeWindow struct {
	start time.Time
	end   time.Time
}

func (w *timeWindow) contains(t time.Time) bool {
	return !t.Before(w.start) && t.Before(w.end)
}

// dayRange returns [day 00:00, next day 00:00) in t's location.
func dayRange(t time.Time) timeWindow {
	start := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	return timeWindow{start: start, end: start.AddDate(0, 0, 1)}
}

// weekRange returns the Monday-start week containing t: [Mon, next Mon).
func weekRange(t time.Time) timeWindow {
	day := dayRange(t).start
	offset := (int(day.Weekday()) + 6) % 7 // Mon=0 … Sun=6
	monday := day.AddDate(0, 0, -offset)
	return timeWindow{start: monday, end: monday.AddDate(0, 0, 7)}
}

// monthRange returns the calendar month containing t.
func monthRange(t time.Time) timeWindow {
	start := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
	return timeWindow{start: start, end: start.AddDate(0, 1, 0)}
}

// yearRange returns the calendar year containing t.
func yearRange(t time.Time) timeWindow {
	start := time.Date(t.Year(), 1, 1, 0, 0, 0, 0, t.Location())
	return timeWindow{start: start, end: start.AddDate(1, 0, 0)}
}

var cnNum = map[rune]int{
	'一': 1, '二': 2, '两': 2, '三': 3, '四': 4, '五': 5,
	'六': 6, '七': 7, '八': 8, '九': 9, '十': 10,
}

var (
	// n units ago: "3天前" "两个星期前" "半个月前" "三个月前"
	reDaysAgo   = regexp.MustCompile(`([0-9]+|[一二两三四五六七八九十半])\s*天前`)
	reWeeksAgo  = regexp.MustCompile(`([0-9]+|[一二两三四五六七八九十半])\s*个?(?:星期|礼拜|周)前`)
	reMonthsAgo = regexp.MustCompile(`([0-9]+|[一二两三四五六七八九十半])\s*个月前`)
	reYearsAgo  = regexp.MustCompile(`([0-9]+|[一二两三四五六七八九十半])\s*年前`)
)

func cnToN(s string) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	if s == "半" {
		return 0 // handled by caller — half means "midpoint of unit"
	}
	r := []rune(s)
	if len(r) == 1 {
		return cnNum[r[0]]
	}
	return 0
}

// recallTimeWindow parses loose Chinese time words in the query into a
// concrete window. Returns nil when the query names no period — vague
// "以前/之前" isn't a window, it's the whole archive's job anyway.
// Longest/most-specific patterns win so "大前天" can't be eaten by 前天.
func recallTimeWindow(query string, now time.Time) *timeWindow {
	day := func(n int) timeWindow { return dayRange(now.AddDate(0, 0, -n)) }

	switch {
	case matchAny(query, "今天", "今日", "今早", "今晨", "今天上午", "今早"):
		w := dayRange(now)
		return &w
	case matchAny(query, "大前天"):
		w := day(3)
		return &w
	case matchAny(query, "前天"):
		w := day(2)
		return &w
	case matchAny(query, "昨晚", "昨夜", "昨天晚上"):
		w := day(1)
		return &w
	case matchAny(query, "昨天", "昨日"):
		w := day(1)
		return &w
	case matchAny(query, "上个星期", "上个礼拜", "上周", "上礼拜", "上一周"):
		cur := weekRange(now)
		w := timeWindow{start: cur.start.AddDate(0, 0, -7), end: cur.start}
		return &w
	case matchAny(query, "这个星期", "这礼拜", "这周", "本周", "这星期"):
		w := weekRange(now)
		w.end = now.Add(24 * time.Hour) // through tomorrow for timezone slop
		return &w
	case matchAny(query, "上个月"):
		cur := monthRange(now)
		w := timeWindow{start: cur.start.AddDate(0, -1, 0), end: cur.start}
		return &w
	case matchAny(query, "这个月", "本月"):
		w := monthRange(now)
		return &w
	case matchAny(query, "前年"):
		cur := yearRange(now)
		w := timeWindow{start: cur.start.AddDate(-2, 0, 0), end: cur.start.AddDate(-1, 0, 0)}
		return &w
	case matchAny(query, "去年"):
		cur := yearRange(now)
		w := timeWindow{start: cur.start.AddDate(-1, 0, 0), end: cur.start}
		return &w
	}

	if m := reDaysAgo.FindStringSubmatch(query); m != nil {
		n := cnToN(m[1])
		if m[1] == "半" {
			w := dayRange(now.Add(-12 * time.Hour))
			return &w
		}
		if n >= 1 {
			w := day(n)
			return &w
		}
	}
	if m := reWeeksAgo.FindStringSubmatch(query); m != nil {
		if m[1] == "半" {
			w := dayRange(now.Add(-84 * time.Hour))
			return &w
		}
		if n := cnToN(m[1]); n >= 1 {
			cur := weekRange(now)
			w := timeWindow{start: cur.start.AddDate(0, 0, -7*n), end: cur.start.AddDate(0, 0, -7*(n-1))}
			return &w
		}
	}
	if m := reMonthsAgo.FindStringSubmatch(query); m != nil {
		if m[1] == "半" {
			w := dayRange(now.Add(-15 * 24 * time.Hour))
			return &w
		}
		if n := cnToN(m[1]); n >= 1 {
			cur := monthRange(now)
			w := timeWindow{start: cur.start.AddDate(0, -n, 0), end: cur.start.AddDate(0, -(n - 1), 0)}
			return &w
		}
	}
	if m := reYearsAgo.FindStringSubmatch(query); m != nil {
		if n := cnToN(m[1]); n >= 1 {
			cur := yearRange(now)
			w := timeWindow{start: cur.start.AddDate(-n, 0, 0), end: cur.start.AddDate(-(n - 1), 0, 0)}
			return &w
		}
	}
	return nil
}

func matchAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if len(sub) > 0 && containsFold(s, sub) {
			return true
		}
	}
	return false
}

func containsFold(s, sub string) bool {
	return len(s) >= len(sub) && indexFold(s, sub) >= 0
}

func indexFold(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
