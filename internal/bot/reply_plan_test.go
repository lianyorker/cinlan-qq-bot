package bot

import (
	"reflect"
	"strings"
	"testing"
)

func TestPlanReplyPartsFoldsBlankLinesWithoutSplitting(t *testing.T) {
	parts := planReplyParts("嗯 抱着了\n\n[[MEME:害羞]]")
	want := []string{"嗯 抱着了\n[[MEME:害羞]]"}
	if !reflect.DeepEqual(parts, want) {
		t.Fatalf("planReplyParts() = %#v, want %#v", parts, want)
	}
}

func TestSplitMarkerLinesExtractsMarkersFromText(t *testing.T) {
	parts := splitMarkerLines("嗯 抱着了\n[[MEME:害羞]]")
	want := []string{"嗯 抱着了", "[[MEME:害羞]]"}
	if !reflect.DeepEqual(parts, want) {
		t.Fatalf("splitMarkerLines() = %#v, want %#v", parts, want)
	}
}

func TestSplitMarkerLinesKeepsPlainTextIntact(t *testing.T) {
	parts := splitMarkerLines("第一行\n第二行")
	want := []string{"第一行\n第二行"}
	if !reflect.DeepEqual(parts, want) {
		t.Fatalf("splitMarkerLines() = %#v, want %#v", parts, want)
	}
}

func TestSoftSplitReplySplitsLongAtSentenceBoundary(t *testing.T) {
	reply := "回来啦！快把门关好晚上起风还是蛮凉的没被吹到吧赶紧换身宽松衣服。茉莉花茶买的是常温的没别大口灌哦，坐下歇会儿陪我聊聊天好不好嘛"
	parts := softSplitReply(reply)
	if len(parts) != 2 {
		t.Fatalf("softSplitReply() = %#v, want 2 parts", parts)
	}
	if strings.Join(parts, "") != strings.ReplaceAll(reply, " ", "") &&
		parts[0]+parts[1] != strings.ReplaceAll(reply, " ", "") {
		t.Fatalf("split lost text: %#v", parts)
	}
	for _, p := range parts {
		if len([]rune(p)) < 8 {
			t.Fatalf("part too short: %q", p)
		}
	}
}

func TestSoftSplitReplyDoesNotSplitAtCommas(t *testing.T) {
	// Prompt dumps and list-style text are comma-heavy with no sentence
	// terminators — cutting at a comma splits the content mid-clause.
	reply := "提示词给你：蓝发微卷披散的年轻女孩，坐在卧室床边，套着一件宽松微垮的米白色针织毛衣，衣领倾斜露出锁骨"
	if got := softSplitReply(reply); len(got) != 1 {
		t.Fatalf("comma-only reply should stay whole, got %#v", got)
	}
}

func TestSoftSplitReplyKeepsShortAndBoundaryFree(t *testing.T) {
	if got := softSplitReply("好的知道啦"); len(got) != 1 {
		t.Fatalf("short reply should stay whole, got %#v", got)
	}
	long := strings.Repeat("无标点流水", 15)
	if got := softSplitReply(long); len(got) != 1 {
		t.Fatalf("no-boundary reply should stay whole, got %#v", got)
	}
}

func TestParseMemeMarker(t *testing.T) {
	if query, ok := parseMemeMarker("[[MEME:害羞]]"); !ok || query != "害羞" {
		t.Fatalf("parseMemeMarker() = %q, %v", query, ok)
	}
	if _, ok := parseMemeMarker("嗯 [[MEME:害羞]]"); ok {
		t.Fatal("parseMemeMarker() matched a mixed part")
	}
}

func TestScrubMarkdownStripsDocumentSyntax(t *testing.T) {
	in := "刚才的提示词给你：\n**中文提示词：**\n> 蓝发微卷披散的女孩\n" +
		"## 场景\n- 毛衣\n1. 锁骨\n用 `seedream` 生成就行"
	got := scrubMarkdown(in)
	for _, junk := range []string{"**", ">", "#", "- 毛衣", "1. ", "`"} {
		if strings.Contains(got, junk) {
			t.Fatalf("scrubMarkdown() left %q in %q", junk, got)
		}
	}
	for _, keep := range []string{"中文提示词：", "蓝发微卷披散的女孩", "毛衣", "锁骨"} {
		if !strings.Contains(got, keep) {
			t.Fatalf("scrubMarkdown() lost content %q in %q", keep, got)
		}
	}
}

func TestScrubMarkdownKeepsPlainChat(t *testing.T) {
	in := "嗯 抱着了 [[MEME:害羞]]"
	if got := scrubMarkdown(in); got != in {
		t.Fatalf("scrubMarkdown() mangled plain text: %q -> %q", in, got)
	}
}

func TestScrubMarkdownFencedCodeKeepsContent(t *testing.T) {
	in := "看这个\n```go\nfmt.Println(\"hi\")\n```"
	got := scrubMarkdown(in)
	if strings.Contains(got, "```") {
		t.Fatalf("fence leaked: %q", got)
	}
	if !strings.Contains(got, `fmt.Println("hi")`) {
		t.Fatalf("code content lost: %q", got)
	}
}

func TestSplitReplyCutsAtSentenceBoundary(t *testing.T) {
	// A reply longer than maxRunes used to slice mid-word at the cap;
	// now the chunk must end at the last terminator inside the cap.
	text := "第一句话说完啦。第二句话也很长很长它一直说到中间去。第三句话收尾。"
	parts, truncated := splitReply(text, 20, 4)
	if len(parts) == 0 {
		t.Fatal("splitReply() returned nothing")
	}
	for _, p := range parts {
		if len([]rune(p)) > 20 {
			t.Fatalf("chunk exceeds cap: %q", p)
		}
	}
	if strings.HasSuffix(parts[0], "中间") || strings.HasSuffix(parts[0], "完") {
		t.Fatalf("chunk cut mid-word: %q", parts[0])
	}
	if !strings.HasSuffix(parts[0], "。") {
		t.Fatalf("first chunk should end at 。: %q", parts[0])
	}
	_ = truncated
}

func TestSplitReplyNoBoundaryHardCuts(t *testing.T) {
	// No terminator at all inside the cap: fall back to the hard cut so
	// nothing is dropped.
	text := strings.Repeat("连", 50)
	parts, _ := splitReply(text, 20, 2)
	if len(parts) != 2 {
		t.Fatalf("expected 2 chunks, got %#v", parts)
	}
}
