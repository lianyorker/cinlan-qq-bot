package bot

import (
	"testing"

	"github.com/lianyorker/cinlan-qq-bot/internal/config"
)

func TestShouldSkipReplyDisabled(t *testing.T) {
	s := &Service{cfg: config.Config{ReadNoReply: false}}
	st := &flowState{text: "嗯"}
	if s.shouldSkipReply(st) {
		t.Fatal("ReadNoReply off must never skip")
	}
}

func TestShouldSkipReplyNeverSkipsSubstantive(t *testing.T) {
	s := &Service{cfg: config.Config{ReadNoReply: true}}
	// Long message.
	if s.shouldSkipReply(&flowState{text: "今天公司发生了好多事情我慢慢跟你讲"}) {
		t.Fatal("long message must not skip")
	}
	// Question.
	if s.shouldSkipReply(&flowState{text: "在吗"}) {
		t.Fatal("question must not skip")
	}
	if s.shouldSkipReply(&flowState{text: "吃了没？"}) {
		t.Fatal("question mark must not skip")
	}
}

func TestShouldSkipReplyFillerHasChance(t *testing.T) {
	s := &Service{cfg: config.Config{ReadNoReply: true}}
	// Statistical: "嗯" should skip at least once over many rolls.
	skipped := false
	for i := 0; i < 200; i++ {
		if s.shouldSkipReply(&flowState{text: "嗯"}) {
			skipped = true
			break
		}
	}
	if !skipped {
		t.Fatal("bare filler should skip at least once in 200 rolls")
	}
	// And should NOT skip every time (chance capped at 0.5).
	allSkipped := true
	for i := 0; i < 200; i++ {
		if !s.shouldSkipReply(&flowState{text: "嗯"}) {
			allSkipped = false
			break
		}
	}
	if allSkipped {
		t.Fatal("filler must not skip every single time")
	}
}
