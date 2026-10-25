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
	reply := "回来啦！快把门关好晚上起风还是蛮凉的没被吹到吧赶紧换身宽松衣服，茉莉花茶买的是常温的没别大口灌哦，坐下歇会儿陪我聊天"
	parts := softSplitReply(reply)
	if len(parts) != 2 {
		t.Fatalf("softSplitReply() = %#v, want 2 parts", parts)
	}
	if parts[0]+parts[1] != strings.ReplaceAll(reply, " ", "") &&
		strings.Join(parts, "") != strings.ReplaceAll(reply, " ", "") {
		t.Fatalf("split lost text: %#v", parts)
	}
	for _, p := range parts {
		if len([]rune(p)) < 8 {
			t.Fatalf("part too short: %q", p)
		}
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
