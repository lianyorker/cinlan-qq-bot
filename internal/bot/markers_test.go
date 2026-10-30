package bot

import (
	"strings"
	"testing"
)

func TestResolveMarkersSpec(t *testing.T) {
	all := resolveMarkersSpec("*", nil)
	for _, id := range markerIDs {
		if !all[id] {
			t.Fatalf("spec * must enable %s", id)
		}
	}
	if got := resolveMarkersSpec("none", nil); len(got) != 0 {
		t.Fatalf("spec none must empty the set, got %v", got)
	}
	if got := resolveMarkersSpec("at,poke", nil); !got["at"] || !got["poke"] || got["dice"] {
		t.Fatalf("csv allowlist wrong: %v", got)
	}
	got := resolveMarkersSpec("*", []string{"poke", "oops"})
	if got["poke"] || got["oops"] || !got["dice"] {
		t.Fatalf("off list must subtract only the named ids: %v", got)
	}
	if got := resolveMarkersSpec("bogus", nil); len(got) != 0 {
		t.Fatalf("unknown ids must not widen the set: %v", got)
	}
}

func TestResolveMarkersForBindingOverride(t *testing.T) {
	base := resolveMarkersSpec("*", nil)
	// Binding allowlist replaces the global set entirely.
	got := resolveMarkersFor(base, []string{"dice"}, nil)
	if !got["dice"] || got["poke"] {
		t.Fatalf("binding allowlist must replace, not merge: %v", got)
	}
	// actions_off subtracts from the inherited set.
	got = resolveMarkersFor(base, nil, []string{"at", "react"})
	if got["at"] || got["react"] || !got["poke"] {
		t.Fatalf("binding off list wrong: %v", got)
	}
	// Both at once: allowlist wins, then off subtracts.
	got = resolveMarkersFor(base, []string{"at", "poke"}, []string{"poke"})
	if !got["at"] || got["poke"] {
		t.Fatalf("allow+off combine wrong: %v", got)
	}
}

func TestBuildProtocolBlockRespectsEnabledSet(t *testing.T) {
	set := resolveMarkersSpec("dice,rps", nil)
	block := buildProtocolBlock(set, true)
	if strings.Contains(block, "[[POKE]]") || strings.Contains(block, "[[REACT") {
		t.Fatalf("disabled gestures leaked into protocol block")
	}
	if !strings.Contains(block, "[[DICE]]") || !strings.Contains(block, "[[RPS]]") {
		t.Fatalf("enabled gestures missing from protocol block")
	}
	if !strings.Contains(block, "[[PASS]]") {
		t.Fatalf("control-plane PASS must always be documented")
	}
}

func TestBuildProtocolBlockHidesCallGesturesWithoutProfiler(t *testing.T) {
	set := resolveMarkersSpec("*", nil)
	block := buildProtocolBlock(set, false)
	for _, want := range []string{"[[REACT", "[[POKE]]", "[[OOPS]]"} {
		if strings.Contains(block, want) {
			t.Fatalf("call-gated %s shown without a Call surface", want)
		}
	}
	if !strings.Contains(block, "[[DICE]]") {
		t.Fatalf("segment-only gestures must survive without a Call surface")
	}
}

func TestStripUnrecognizedMarkersKeepAT(t *testing.T) {
	if got := stripUnrecognizedMarkersKeepAT("回他[[AT]]别理[[SECRET:x]]的"); got != "回他[[AT]]别理的" {
		t.Fatalf("keepAT strip = %q", got)
	}
	if got := stripUnrecognizedMarkersKeepAT("点他[[AT:12345]]走[[POKE]]"); got != "点他[[AT:12345]]走" {
		t.Fatalf("keepAT strip with arg = %q", got)
	}
	// Disabled path: base pattern eats [[AT]] like any dead marker.
	if got := stripUnrecognizedMarkers("回他[[AT]]好"); got != "回他好" {
		t.Fatalf("base strip must eat AT = %q", got)
	}
	// Stray closing tags get stripped too.
	if got := stripUnrecognizedMarkers("说[[OOPS]]错话[[/OOPS]]"); got != "说错话" {
		t.Fatalf("closing tags must not leak = %q", got)
	}
}
