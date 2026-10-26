package bot

import (
	"reflect"
	"strings"
	"testing"
)

func TestPlanReplyPartsFoldsBlankLinesWithoutSplitting(t *testing.T) {
	parts := planReplyParts("嗯 抱着了\n\n[[MEME:害羞]]", 4)
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
	reply := "回来啦！快把门关好，晚上起风还是蛮凉的，没被吹到吧？赶紧换身宽松衣服。茉莉花茶买的是常温的，没别大口灌哦，坐下歇会儿陪我聊聊天好不好嘛，今天在外面跑了一整天肯定累坏了。"
	if l := len([]rune(reply)); l < 80 {
		t.Skipf("test text must reach the split band, got %d runes", l)
	}
	parts := softSplitReply(reply, 0.1)
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
	if got := softSplitReply(reply, 0.9); len(got) != 1 {
		t.Fatalf("comma-only reply should stay whole, got %#v", got)
	}
}

func TestSoftSplitReplyKeepsShortAndBoundaryFree(t *testing.T) {
	if got := softSplitReply("好的知道啦", 0.9); len(got) != 1 {
		t.Fatalf("short reply should stay whole, got %#v", got)
	}
	long := strings.Repeat("无标点流水", 15)
	if got := softSplitReply(long, 0.9); len(got) != 1 {
		t.Fatalf("no-boundary reply should stay whole, got %#v", got)
	}
}

func TestSoftSplitReplyJitterKeepsMidWhole(t *testing.T) {
	reply := "还有一个多小时呢你慢慢来别着急，路上注意安全不要跑。摸会儿鱼就能混过去了，你先把手里的事情忙完，我就在这里等你不着急。"
	if l := len([]rune(reply)); l < 80 || l >= 160 {
		t.Skipf("test text must stay in the mid band, got %d runes", l)
	}
	if got := softSplitReply(reply, 0.9); len(got) != 1 {
		t.Fatalf("high roll should keep mid reply whole, got %#v", got)
	}
	if got := softSplitReply(reply, 0.2); len(got) < 2 {
		t.Fatalf("low roll should split mid reply, got %#v", got)
	}
}

func TestSoftSplitReplyThirdPartOnLongTail(t *testing.T) {
	// A long first segment pushes the mid-point boundary early, leaving
	// a tail big enough to split once more — three bubbles total.
	reply := strings.Repeat("很长的一段啊，", 22) + "。" +
		strings.Repeat("中间一截短些，", 8) + "。" +
		strings.Repeat("最后一截也短，", 8) + "。"
	if l := len([]rune(reply)); l < 240 {
		t.Skipf("test text must reach the third-bubble band, got %d runes", l)
	}
	got := softSplitReply(reply, 0.5)
	if len(got) != 3 {
		t.Fatalf("long tail should reach 3 parts, got %#v", got)
	}
}

func TestFilterSelfEchoTrimsVerbatimRepeat(t *testing.T) {
	sent := []string{"快下班没 熬着等天黑呢"}
	parts := []string{"还有一个多小时 摸会儿鱼就混过去了", "好慢 熬着等天黑呢 快下班来找我"}
	got := filterSelfEcho(parts, sent)
	if len(got) != 2 {
		t.Fatalf("filterSelfEcho() dropped too much: %#v", got)
	}
	if strings.Contains(got[1], "熬着等天黑呢") {
		t.Fatalf("verbatim echo survived: %q", got[1])
	}
	if !strings.Contains(got[1], "快下班来找我") {
		t.Fatalf("fresh content lost: %q", got[1])
	}
}

func TestFilterSelfEchoDropsPureRepeatButKeepsFallback(t *testing.T) {
	sent := []string{"快下班没 熬着等天黑呢"}
	if got := filterSelfEcho([]string{"快下班没 熬着等天黑呢"}, sent); len(got) != 1 {
		t.Fatalf("all-echo parts must keep the first as fallback, got %#v", got)
	}
	if got := filterSelfEcho([]string{"昂"}, sent); len(got) != 1 || got[0] != "昂" {
		t.Fatalf("short fresh part wrongly filtered: %#v", got)
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

func TestScrubMarkdownStripsSelfTag(t *testing.T) {
	got := scrubMarkdown("给你复制：[SELF] 蓝发微卷披散的年轻女孩")
	if strings.Contains(got, "SELF") || strings.Contains(got, "[") {
		t.Fatalf("self tag leaked: %q", got)
	}
	if !strings.Contains(got, "蓝发微卷披散的年轻女孩") {
		t.Fatalf("prompt content lost: %q", got)
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
