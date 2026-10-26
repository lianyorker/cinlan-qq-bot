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
