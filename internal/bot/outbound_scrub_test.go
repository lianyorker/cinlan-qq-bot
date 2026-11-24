package bot

import (
	"strings"
	"testing"
)

// The real leak that started this: the model wrapped its own aside in an
// XML tag it invented. The tag must die, the words must survive, and no
// stray space may be left behind between the Chinese characters.
func TestSanitizeOutboundUnwrapsInventedTag(t *testing.T) {
	in := "@止水 法治社会救了你 你这话说得跟要动手似的<recently>你说的治法社会我猜是法治社会哈哈</recently>"
	got, hits := sanitizeOutboundText(in)
	want := "@止水 法治社会救了你 你这话说得跟要动手似的你说的治法社会我猜是法治社会哈哈"
	if got != want {
		t.Fatalf("sanitize = %q, want %q", got, want)
	}
	if !containsRule(hits, "stray_tag") {
		t.Fatalf("hits = %v, want stray_tag", hits)
	}
}

func TestSanitizeOutboundFullWidthTag(t *testing.T) {
	got, _ := sanitizeOutboundText("行吧＜recently＞那就这样＜/recently＞")
	if got != "行吧那就这样" {
		t.Fatalf("full-width tag = %q", got)
	}
}

// Reasoning containers drop their body — unwrapping would publish the
// model's private thinking as chat.
func TestSanitizeOutboundDropsReasoningBlock(t *testing.T) {
	got, hits := sanitizeOutboundText("好的<thinking>他问的是价格 我得查一下</thinking>我看看")
	if got != "好的我看看" {
		t.Fatalf("reasoning block = %q", got)
	}
	if !containsRule(hits, "reasoning_block") {
		t.Fatalf("hits = %v", hits)
	}
}

func TestSanitizeOutboundDropsUnclosedReasoningTail(t *testing.T) {
	got, hits := sanitizeOutboundText("行<analysis>先算一下 3+4")
	if got != "行" {
		t.Fatalf("unclosed reasoning = %q", got)
	}
	if !containsRule(hits, "reasoning_tail") {
		t.Fatalf("hits = %v", hits)
	}
}

// Tool envelopes carry a JSON body; stripping only the tags would ship it.
func TestSanitizeOutboundDropsToolEnvelopeBody(t *testing.T) {
	in := `我查一下<tool_call>{"name":"web_search","args":{"q":"价格"}}</tool_call>好了`
	got, hits := sanitizeOutboundText(in)
	if got != "我查一下好了" {
		t.Fatalf("tool envelope = %q", got)
	}
	if !containsRule(hits, "tool_block") {
		t.Fatalf("hits = %v", hits)
	}
}

func TestSanitizeOutboundStripsControlTokens(t *testing.T) {
	got, hits := sanitizeOutboundText("<|im_start|>assistant 在的<|im_end|>")
	if strings.Contains(got, "im_start") || strings.Contains(got, "im_end") {
		t.Fatalf("control token survived: %q", got)
	}
	if !containsRule(hits, "control_token") {
		t.Fatalf("hits = %v", hits)
	}
}

// The catch-all must stay narrow: comparison operators and emoticons are
// prose, not tags.
func TestSanitizeOutboundKeepsProseAngleBrackets(t *testing.T) {
	for _, in := range []string{"x < 5 就行", "我 <3 你", "a<b 这个不等式"} {
		if got, _ := sanitizeOutboundText(in); got != in {
			t.Fatalf("prose %q was mangled to %q", in, got)
		}
	}
}

// English words on either side of a removed tag keep their separation.
func TestSanitizeOutboundKeepsEnglishSpacing(t *testing.T) {
	got, _ := sanitizeOutboundText("run <b>this</b> now")
	if got != "run this now" {
		t.Fatalf("english spacing = %q", got)
	}
}

func TestSanitizeOutboundStillScrubsMarkdown(t *testing.T) {
	got, _ := sanitizeOutboundText("**重点** 是这个\n- 第一条\n- 第二条")
	if strings.Contains(got, "**") || strings.Contains(got, "- ") {
		t.Fatalf("markdown survived: %q", got)
	}
}

func TestSanitizeOutboundDropsMetaLine(t *testing.T) {
	got, hits := sanitizeOutboundText("作为一个AI语言模型 我不能这样\n行吧那算了")
	if strings.Contains(got, "作为一个AI") {
		t.Fatalf("meta line survived: %q", got)
	}
	if got != "行吧那算了" {
		t.Fatalf("meta line = %q", got)
	}
	if !containsRule(hits, "meta_line") {
		t.Fatalf("hits = %v", hits)
	}
}

func TestSanitizeOutboundLeavesCleanTextAlone(t *testing.T) {
	in := "今天好热啊 你那边下雨没"
	got, hits := sanitizeOutboundText(in)
	if got != in {
		t.Fatalf("clean text = %q", got)
	}
	if len(hits) != 0 {
		t.Fatalf("clean text reported hits: %v", hits)
	}
}

func containsRule(hits []string, want string) bool {
	for _, h := range hits {
		if h == want {
			return true
		}
	}
	return false
}
