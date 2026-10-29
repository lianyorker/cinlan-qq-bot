package bot

import (
	"strings"
	"testing"

	"github.com/lianyorker/cinlan-qq-bot/internal/domain"
)

func userMsg(text string) domain.ChatMessage {
	return domain.ChatMessage{Role: "user", Content: text}
}

func botMsg(text string) domain.ChatMessage {
	return domain.ChatMessage{Role: "assistant", Content: text}
}

func TestFoldSpamKeepsShortRun(t *testing.T) {
	history := []domain.ChatMessage{
		userMsg("1"), userMsg("2"), userMsg("3"), userMsg("4"),
	}
	out := foldSpam(history)
	if len(out) != 4 {
		t.Fatalf("short run must stay verbatim, got %d messages", len(out))
	}
}

func TestFoldSpamFoldsMiddleOfLongRun(t *testing.T) {
	history := []domain.ChatMessage{
		userMsg("a1"), userMsg("a2"), userMsg("a3"), userMsg("a4"),
		userMsg("a5"), userMsg("a6"), userMsg("a7"), userMsg("a8"),
	}
	out := foldSpam(history)
	// 8 user messages → first2 + fold marker + last2 = 5
	if len(out) != 5 {
		t.Fatalf("expected 5 messages after folding, got %d", len(out))
	}
	if out[0].Content != "a1" || out[1].Content != "a2" {
		t.Fatalf("head edge must stay verbatim: %+v", out[:2])
	}
	if out[3].Content != "a7" || out[4].Content != "a8" {
		t.Fatalf("tail edge must stay verbatim: %+v", out[3:])
	}
	if !strings.Contains(out[2].Content, "4 条连发消息已省略") {
		t.Fatalf("fold marker must count the dropped middle: %q", out[2].Content)
	}
}

func TestFoldSpamBreaksRunAtAssistant(t *testing.T) {
	history := []domain.ChatMessage{
		userMsg("u1"), userMsg("u2"), userMsg("u3"),
		userMsg("u4"), userMsg("u5"), userMsg("u6"),
		botMsg("b1"),
		userMsg("v1"),
	}
	out := foldSpam(history)
	// 6-user run → head2 + fold(2) + tail2 = 5, then b1 + v1 = 7
	if len(out) != 7 {
		t.Fatalf("expected 7 messages after folding, got %d", len(out))
	}
	if out[5].Content != "b1" || out[6].Content != "v1" {
		t.Fatalf("assistant message must break the run: %+v", out[5:])
	}
	if out[0].Content != "u1" || out[1].Content != "u2" ||
		out[3].Content != "u5" || out[4].Content != "u6" {
		t.Fatalf("run edges must stay verbatim: %+v", out[:5])
	}
}

func TestFoldSpamEmpty(t *testing.T) {
	if out := foldSpam(nil); len(out) != 0 {
		t.Fatalf("nil history must stay empty")
	}
}

func TestStripUnrecognizedMarkers(t *testing.T) {
	cases := map[string]string{
		"你好[[SECRET:abc]]再见": "你好再见",
		"正常文本":              "正常文本",
		"[[A]][[B:xyz]]全删":   "全删",
		"[[小写]]不算":          "[[小写]]不算",
		"[单方括号]也不删":        "[单方括号]也不删",
		"":                    "",
	}
	for in, want := range cases {
		if got := stripUnrecognizedMarkers(in); got != want {
			t.Errorf("stripUnrecognizedMarkers(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReframeInternalNotes(t *testing.T) {
	history := []domain.ChatMessage{
		userMsg("[10-30 00:29] 给我文字 你是不是搞错了"),
		botMsg("[10-30 00:30] 知道啦凶什么嘛"),
		userMsg("[10-30 00:42] [心情] 全记小本本上了，某人随口提过的事我可都记着呢"),
		userMsg("[10-30 00:44] 休息了"),
		userMsg("[10-30 00:44] [动作] 发了个表情"),
		userMsg("[10-30 00:44] [其他机器人 3457] 哈哈哈"),
	}
	out := reframeInternalNotes(history)

	if got := out[0].Content; got != "[10-30 00:29] 给我文字 你是不是搞错了" {
		t.Fatalf("real user text must pass through: %q", got)
	}
	if got := out[3].Content; got != "[10-30 00:44] 休息了" {
		t.Fatalf("real user text must pass through: %q", got)
	}
	if got := out[1].Content; !strings.HasPrefix(got, "[10-30 00:30] 知道啦") {
		t.Fatalf("assistant turn must stay untouched: %q", got)
	}
	for _, idx := range []int{2, 4, 5} {
		if !strings.Contains(out[idx].Content, "（备注·非对方消息）") {
			t.Fatalf("internal note at %d must be re-tagged: %q", idx, out[idx].Content)
		}
	}
	if !strings.Contains(out[2].Content, "[心情] 全记小本本上了") {
		t.Fatalf("tag and body must survive reframing: %q", out[2].Content)
	}
}

func TestReframeInternalNotesUnstampedLookalike(t *testing.T) {
	// A user literally typing "[心情] hi" has no storage stamp — it must
	// not be mistaken for a stored annotation.
	history := []domain.ChatMessage{
		userMsg("[心情] 我自己打的"),
		userMsg("[中间的 4 条连发消息已省略]"),
	}
	out := reframeInternalNotes(history)
	for i, m := range out {
		if strings.Contains(m.Content, "非对方消息") {
			t.Fatalf("unstamped lookalike at %d must not be re-tagged: %q", i, m.Content)
		}
	}
}
