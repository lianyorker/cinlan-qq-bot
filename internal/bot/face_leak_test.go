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

func TestBareEmotionTagResolvesToFace(t *testing.T) {
	chain, plain, found := parseFaceMarkers("贴贴[亲亲]")
	if !found {
		t.Fatalf("found = false, plain = %q", plain)
	}
	var sawFace bool
	for _, c := range chain {
		if c.Type == "face" {
			if id, _ := c.Data["id"].(string); id == "109" {
				sawFace = true
			}
		}
	}
	if !sawFace {
		t.Fatalf("no face component in %#v", chain)
	}
	if plain != "贴贴" {
		t.Fatalf("plain = %q, want 贴贴", plain)
	}
}

func TestBareEmotionTagFallsBackToEmoji(t *testing.T) {
	chain, plain, found := parseFaceMarkers("哼[傲娇]")
	if !found {
		t.Fatalf("found = false, plain = %q", plain)
	}
	if plain != "哼\U0001f624" {
		t.Fatalf("plain = %q, want 哼+emoji", plain)
	}
	for _, c := range chain {
		if c.Type == "text" {
			if s, _ := c.Data["text"].(string); s == "[傲娇]" {
				t.Fatal("bare emotion tag leaked as text")
			}
		}
	}
}
