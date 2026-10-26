package tts

import (
	"strings"
	"testing"
)

func TestXmlEscapeSpeechBreaksEllipsis(t *testing.T) {
	out := xmlEscapeSpeech("唱一半...停一下…继续")
	if !strings.Contains(out, `<break time="800ms"/>`) {
		t.Fatalf("ellipsis must become <break>: %q", out)
	}
	// Both … (single rune) and ... (ASCII) produce a break each.
	if strings.Count(out, "<break") != 2 {
		t.Fatalf("expected 2 breaks, got %q", out)
	}
	if strings.Contains(out, "...") || strings.Contains(out, "…") {
		t.Fatalf("raw ellipsis must not survive: %q", out)
	}
}

func TestXmlEscapeSpeechStillEscapes(t *testing.T) {
	out := xmlEscapeSpeech(`a<b & "c"...`)
	if strings.Contains(out, `<b `) {
		t.Fatalf("user markup must stay escaped: %q", out)
	}
	if !strings.Contains(out, "&lt;b") {
		t.Fatalf("lt escape missing: %q", out)
	}
	if !strings.Contains(out, `<break time="800ms"/>`) {
		t.Fatalf("break must survive escaping: %q", out)
	}
}
