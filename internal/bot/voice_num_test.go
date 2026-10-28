package bot

import "testing"

func TestNumericCN(t *testing.T) {
	cases := map[string]string{
		"0":     "零",
		"3":     "三",
		"10":    "十",
		"25":    "二十五",
		"100":   "一百",
		"101":   "一百零一",
		"120":   "一百二十",
		"1000":  "一千",
		"1001":  "一千零一",
		"1234":  "一千二百三十四",
		"2026":  "二千零二十六",
		"10000": "一万",
		"50":    "五十",
		"008":   "八",
	}
	for in, want := range cases {
		if got := numericCN(in); got != want {
			t.Errorf("numericCN(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSerialCN(t *testing.T) {
	if got := serialCN("2026"); got != "二零二六" {
		t.Errorf("serialCN(2026) = %q", got)
	}
	if got := serialCN("10086"); got != "幺零零八六" {
		t.Errorf("serialCN(10086) = %q", got)
	}
}

func TestSpeechDigitsContext(t *testing.T) {
	cases := map[string]string{
		"3点":       "三点",
		"100个":     "一百个",
		"25次":      "二十五次",
		"2026年":    "二零二六年",
		"10086号":   "幺零零八六号",
		"1.5倍":     "一点五倍",
		"50%":      "百分之五十",
		"12345":    "幺二三四五", // 5+ digits = id string
		"他说了100遍": "他说了一百遍",
		"明天3点见":  "明天三点见",
	}
	for in, want := range cases {
		if got := speechDigits(in); got != want {
			t.Errorf("speechDigits(%q) = %q, want %q", in, got, want)
		}
	}
}
