package bot

import (
	"regexp"
	"strings"
)

// voiceNumPattern matches a digit run optionally followed by a decimal
// tail or a percent sign — the pieces a voice should say in words.
var voiceNumPattern = regexp.MustCompile(`\d+(?:\.\d+)?%?`)

// speechDigits rewrites Arabic numerals into spoken Chinese so TTS reads
// "三点" instead of the machine "3". Two reading styles are used:
//
//   numeric — 3→三, 100→一百, 1234→一千二百三十四 (counts, times)
//   serial  — 2026→二零二六, 10086→幺零零八六 (years, numbers, ids)
//
// Serial reading kicks in for 5+ digit runs (phone/QQ/id territory), a
// 4-digit year before 年, or a number before 号 (dates/numbers). Decimal
// tails always serial-read digit by digit.
func speechDigits(text string) string {
	var b strings.Builder
	b.Grow(len(text) + 8)
	rest := text
	for {
		loc := voiceNumPattern.FindStringIndex(rest)
		if loc == nil {
			b.WriteString(rest)
			return b.String()
		}
		b.WriteString(rest[:loc[0]])
		m := rest[loc[0]:loc[1]]
		rest = rest[loc[1]:]

		percent := strings.HasSuffix(m, "%")
		if percent {
			m = strings.TrimSuffix(m, "%")
		}
		var out string
		if i := strings.IndexByte(m, '.'); i >= 0 {
			// Decimal: numeric whole + 点 + serial fraction.
			out = numericCN(m[:i]) + "点" + serialCN(m[i+1:])
		} else {
			// Peek at the rune after the digits to pick the style —
			// 2026年 reads serial (二零二六), 3点 reads numeric (三点).
			var after rune
			for _, r := range rest {
				after = r
				break
			}
			if shouldSerial(m, after) {
				out = serialCN(m)
			} else {
				out = numericCN(m)
			}
		}
		if percent {
			out = "百分之" + out
		}
		b.WriteString(out)
	}
}

// serialCN reads each digit in place — phone numbers, years, ids.
// One is 幺 here because that is how strings of digits get spoken.
func serialCN(digits string) string {
	var b strings.Builder
	for _, r := range digits {
		switch r {
		case '0':
			b.WriteString("零")
		case '1':
			b.WriteString("幺")
		default:
			b.WriteString(digitCN(byte(r - '0')))
		}
	}
	return b.String()
}

var cnDigit = []string{"零", "一", "二", "三", "四", "五", "六", "七", "八", "九"}

func digitCN(d byte) string {
	if d > 9 {
		return ""
	}
	return cnDigit[d]
}

// numericCN renders an integer as spoken Chinese: 0→零, 5→五,
// 10→十, 100→一百, 1234→一千二百三十四, 10000→一万. Internal zeros
// collapse to a single 零; trailing zeros drop their units.
func numericCN(digits string) string {
	digits = strings.TrimLeft(digits, "0")
	if digits == "" {
		return "零"
	}
	runes := []byte(digits)
	n := len(runes)
	var b strings.Builder
	for i := 0; i < n; i++ {
		d := runes[i] - '0'
		unit := n - 1 - i // power of ten position
		if d == 0 {
			// One 零 per zero run, and only when a later digit is
			// nonzero — 1001 → 一千零一, 120 → 一百二十 (trailing
			// zero falls silent).
			if i > 0 && runes[i-1] != '0' {
				for j := i + 1; j < n; j++ {
					if runes[j] != '0' {
						b.WriteString("零")
						break
					}
				}
			}
			continue
		}
		// Leading 十 drops its 一 — 10 reads 十 not 一十.
		if i == 0 && d == 1 && unit == 1 {
			b.WriteString("十")
			continue
		}
		b.WriteString(digitCN(d))
		switch unit {
		case 0:
		case 1:
			b.WriteString("十")
		case 2:
			b.WriteString("百")
		case 3:
			b.WriteString("千")
		case 4:
			b.WriteString("万")
		case 5:
			b.WriteString("十")
		case 6:
			b.WriteString("百")
		case 7:
			b.WriteString("千")
		case 8:
			b.WriteString("亿")
		default:
			// Beyond 亿 fall back to serial — a billion-digit
			// number is an id string, not a count.
			return serialCN(digits)
		}
	}
	return b.String()
}

// shouldSerial reports whether a bare digit run reads better digit-by-
// digit: long numbers are identifiers (QQ, phone), 4-digit runs before 年
// are years, and anything before 号 is a date or number tag.
func shouldSerial(digits string, after rune) bool {
	switch after {
	case '年':
		return len(digits) == 4
	case '号':
		return len(digits) >= 4
	}
	return len(digits) >= 5
}
