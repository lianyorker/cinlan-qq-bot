package onebot

import (
	"encoding/json"
	"testing"
)

func TestParseSegmentMessage(t *testing.T) {
	message := json.RawMessage(`[
		{"type":"at","data":{"qq":"10001"}},
		{"type":"text","data":{"text":" 请问退款规则 "}},
		{"type":"image","data":{"file":"image.jpg"}}
	]`)

	parsed := ParseMessage(message, "", "10001")
	if !parsed.Mentioned {
		t.Fatalf("Mentioned = false, want true")
	}
	if parsed.Text != "请问退款规则  [图片]" {
		t.Fatalf("Text = %q", parsed.Text)
	}
}

func TestParseCQMessage(t *testing.T) {
	message := json.RawMessage(`"[CQ:reply,id=8][CQ:at,qq=10001] 查询&amp;售后&#91;规则&#93;"`)

	parsed := ParseMessage(message, "", "10001")
	if !parsed.Mentioned {
		t.Fatalf("Mentioned = false, want true")
	}
	if parsed.Text != "查询&售后[规则]" {
		t.Fatalf("Text = %q", parsed.Text)
	}
}

func TestParseMessagePreservesOtherMention(t *testing.T) {
	message := json.RawMessage(`[
		{"type":"at","data":{"qq":20002}},
		{"type":"text","data":{"text":" 帮他查询"}}
	]`)

	parsed := ParseMessage(message, "", "10001")
	if parsed.Mentioned {
		t.Fatalf("Mentioned = true, want false")
	}
	if parsed.Text != "@20002  帮他查询" {
		t.Fatalf("Text = %q", parsed.Text)
	}
}
