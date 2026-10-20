package bot

import (
	"reflect"
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

func TestParseMemeMarker(t *testing.T) {
	if query, ok := parseMemeMarker("[[MEME:害羞]]"); !ok || query != "害羞" {
		t.Fatalf("parseMemeMarker() = %q, %v", query, ok)
	}
	if _, ok := parseMemeMarker("嗯 [[MEME:害羞]]"); ok {
		t.Fatal("parseMemeMarker() matched a mixed part")
	}
}
