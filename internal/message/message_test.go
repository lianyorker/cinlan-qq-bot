package message

import (
	"encoding/json"
	"testing"
)

func TestParseOneBotChainAndPlainText(t *testing.T) {
	raw := json.RawMessage(`[
		{"type":"reply","data":{"id":8}},
		{"type":"at","data":{"qq":"10001"}},
		{"type":"text","data":{"text":" 你好"}},
		{"type":"image","data":{"file":"a.jpg"}}
	]`)
	chain, err := ParseOneBot(raw, "", "10001")
	if err != nil {
		t.Fatalf("ParseOneBot() error = %v", err)
	}
	text, mentioned := chain.PlainText("10001")
	if !mentioned || text != "你好 [图片]" {
		t.Fatalf("plain text = %q, mentioned=%v", text, mentioned)
	}
	if chain.ReplyID() != "8" {
		t.Fatalf("ReplyID() = %q", chain.ReplyID())
	}
}

func TestParseCQEscapesAndOtherMention(t *testing.T) {
	chain, err := ParseCQ("[CQ:at,qq=20002] hi&amp;there")
	if err != nil {
		t.Fatalf("ParseCQ() error = %v", err)
	}
	text, mentioned := chain.PlainText("10001")
	if mentioned || text != "@20002  hi&there" {
		t.Fatalf("plain text = %q, mentioned=%v", text, mentioned)
	}
}

func TestCloneDoesNotShareDataMap(t *testing.T) {
	original := Chain{Text("hello")}
	clone := original.Clone()
	clone[0].Data["text"] = "changed"
	if original[0].Data["text"] != "hello" {
		t.Fatal("Clone() shares component data")
	}
}
