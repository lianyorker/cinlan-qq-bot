package bot

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lianyorker/cinlan-qq-bot/internal/domain"
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
	parts := softSplitReply(reply, 0.1, 0)
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
	if got := softSplitReply(reply, 0.9, 0); len(got) != 1 {
		t.Fatalf("comma-only reply should stay whole, got %#v", got)
	}
}

func TestSoftSplitReplyKeepsShortAndBoundaryFree(t *testing.T) {
	if got := softSplitReply("好的知道啦", 0.9, 0); len(got) != 1 {
		t.Fatalf("short reply should stay whole, got %#v", got)
	}
	long := strings.Repeat("无标点流水", 15)
	if got := softSplitReply(long, 0.9, 0); len(got) != 1 {
		t.Fatalf("no-boundary reply should stay whole, got %#v", got)
	}
}

func TestSoftSplitReplyJitterKeepsMidWhole(t *testing.T) {
	reply := "还有一个多小时呢你慢慢来别着急，路上注意安全不要跑。摸会儿鱼就能混过去了，你先把手里的事情忙完，我就在这里等你不着急。"
	if l := len([]rune(reply)); l < 80 || l >= 160 {
		t.Skipf("test text must stay in the mid band, got %d runes", l)
	}
	if got := softSplitReply(reply, 0.9, 0); len(got) != 1 {
		t.Fatalf("high roll should keep mid reply whole, got %#v", got)
	}
	if got := softSplitReply(reply, 0.2, 0); len(got) < 2 {
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
	got := softSplitReply(reply, 0.5, 0)
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

func TestFilterSelfEchoDropsSemanticRepeat(t *testing.T) {
	sent := []string{"我今晚要去健身房撸铁"}
	// Same meaning, different words — verbatim trimming can't see it.
	parts := []string{"今晚去健身房锻炼一下", "今天天气不错"}
	got := filterSelfEcho(parts, sent)
	if len(got) != 1 || got[0] != "今天天气不错" {
		t.Fatalf("semantic echo survived: %#v", got)
	}
}

func TestSemanticEchoExemptsShortLines(t *testing.T) {
	sent := []string{"好的没问题"}
	if semanticEcho("嗯嗯好的", sent) {
		t.Fatal("short acknowledgement wrongly flagged as echo")
	}
	if !semanticEcho("我今晚要去健身房撸铁", []string{"今晚去健身房锻炼一下"}) {
		t.Fatal("near-paraphrase not detected")
	}
	// Topic-adjacent but not a repeat — below the cutoff.
	if semanticEcho("熬到天黑下班 等得好慢", []string{"快下班没 熬着等天黑呢"}) {
		t.Fatal("topic-adjacent line wrongly flagged as echo")
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

func TestMaybeTypoPart(t *testing.T) {
	base := []string{"阳光明媚特别适合出门散步"}
	if got := maybeTypoPart(base, 0.5, true); len(got) != 1 || got[0] != base[0] {
		t.Fatalf("high roll must pass through: %#v", got)
	}
	got := maybeTypoPart(base, 0, true)
	if len(got) != 2 {
		t.Fatalf("typo with room should append correction: %#v", got)
	}
	if got[0] == base[0] {
		t.Fatal("first part should carry the mutation")
	}
	if !strings.HasPrefix(got[1], "*") || len([]rune(got[1])) < 3 {
		t.Fatalf("correction bubble malformed: %q", got[1])
	}
	got = maybeTypoPart(base, 0, false)
	if len(got) != 1 || got[0] == base[0] {
		t.Fatalf("no room: typo flies bare, got %#v", got)
	}
	if got := maybeTypoPart([]string{"[[MEME:嘲笑]]", "嗯嗯"}, 0, true); len(got) != 2 {
		t.Fatalf("markers and short parts are not typo candidates: %#v", got)
	}
}

func TestHangingTail(t *testing.T) {
	hist := []domain.ChatMessage{
		{Role: "user", Content: "周末要不要一起去爬山？"},
		{Role: "assistant", Content: "去呀，你想走哪条线？"},
	}
	if frag, ok := hangingTail(hist); !ok || !strings.Contains(frag, "爬山") {
		t.Fatalf("unanswered question should hang: %q %v", frag, ok)
	}

	left := []domain.ChatMessage{
		{Role: "user", Content: "新出的游戏看着还行"},
		{Role: "assistant", Content: "确实，画风蛮合我胃口"},
		{Role: "user", Content: "我先去忙了回头再说"},
	}
	if frag, ok := hangingTail(left); !ok || !strings.Contains(frag, "忙") {
		t.Fatalf("mid-topic exit should hang: %q %v", frag, ok)
	}

	closed := []domain.ChatMessage{
		{Role: "user", Content: "晚安"},
		{Role: "assistant", Content: "晚安好梦"},
	}
	if _, ok := hangingTail(closed); ok {
		t.Fatal("clean closing should not hang")
	}
	empty := []domain.ChatMessage{{Role: "assistant", Content: "嗯"}}
	if _, ok := hangingTail(empty); ok {
		t.Fatal("sub-4-rune content should be skipped")
	}
}

func TestStripQuoteMarker(t *testing.T) {
	if text, ok := stripQuoteMarker("[[QUOTE]]确实有点东西"); !ok || text != "确实有点东西" {
		t.Fatalf("stripQuoteMarker() = %q, %v", text, ok)
	}
	if text, ok := stripQuoteMarker("[[quote]]"); !ok || text != "" {
		t.Fatalf("bare lowercase marker = %q, %v", text, ok)
	}
	if _, ok := stripQuoteMarker("普通回复"); ok {
		t.Fatal("plain text should not tag a quote")
	}
	if !isMarkerPart("[[QUOTE]]") {
		t.Fatal("bare QUOTE must survive the marker split")
	}
	if isMarkerPart("[[QUOTE]]确实") {
		t.Fatal("QUOTE glued to text is not a marker part")
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

func TestEnforceShortBubblesChopsLongCasual(t *testing.T) {
	// 50-rune single bubble stuffing several intentions must chop into
	// multiple short bubbles at punctuation.
	long := "学什么，学怎么把你治得服服帖帖吗，哼，脑子要放空，带我玩呢，宝快去搬砖别摸鱼了"
	parts := enforceShortBubbles(long, 15)
	if len(parts) < 2 {
		t.Fatalf("expected chop into >=2 bubbles, got %#v", parts)
	}
	for _, p := range parts {
		if len([]rune(p)) > 20 { // 15 cap + small slack for boundary seek
			t.Fatalf("bubble too long: %q (%d runes)", p, len([]rune(p)))
		}
	}
}

func TestEnforceShortBubblesLeavesShortWhole(t *testing.T) {
	if got := enforceShortBubbles("学什么", 15); len(got) != 1 || got[0] != "学什么" {
		t.Fatalf("short text should pass through: %#v", got)
	}
}

func TestEnforceShortBubblesLeavesMarkerWhole(t *testing.T) {
	// Marker-bearing parts must never be chopped even if long.
	m := "这是一段很长很长的话但是里面有标记所以不能被切断处理好吗[[MEME:嘲笑]]"
	if got := enforceShortBubbles(m, 15); len(got) != 1 {
		t.Fatalf("marker part must stay whole: %#v", got)
	}
}

func TestEnforceShortBubblesBusinessCapWider(t *testing.T) {
	// 60-rune technical answer under an 80 cap stays one bubble.
	tech := strings.Repeat("部署步骤说明", 10) // 60 runes
	if got := enforceShortBubbles(tech, 80); len(got) != 1 {
		t.Fatalf("business reply under cap should stay whole: %d parts", len(got))
	}
}
