package bot

import (
	"testing"
)

func TestParseFaceMarkersUnknownNameDoesNotLeak(t *testing.T) {
	chain, plain, found := parseFaceMarkers("好耶[表情:无语]")
	if !found {
		t.Fatalf("found = false, plain = %q", plain)
	}
	if plain != "好耶" {
		t.Fatalf("plain = %q, want 好耶", plain)
	}
	for _, c := range chain {
		if c.Type == "text" {
			if s, _ := c.Data["text"].(string); s == "[表情:无语]" {
				t.Fatal("unknown face marker leaked as text")
			}
		}
	}
}
