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

func TestImageReferencesSupportsQQNTSnakeCaseFields(t *testing.T) {
	chain := Chain{Attachment(TypeImage, map[string]any{
		"file_path":        `D:\qq-cache\error.png`,
		"origin_image_url": "https://example.test/error.png",
	})}

	references := chain.ImageReferences()

	if len(references) != 1 ||
		references[0] != "https://example.test/error.png" {
		t.Fatalf("image references = %#v", references)
	}
}

func TestImageReferencesPrefersLocalCacheOverSenderFileURI(t *testing.T) {
	chain := Chain{Attachment(TypeImage, map[string]any{
		"origin_image_url": "file://C:\\Users\\Example\\Documents\\Tencent Files\\1000000000\\nt_qq\\nt_data\\Pic\\2026-07\\Ori\\error.png",
		"file_path":        `C:\Users\Example\Documents\Tencent Files\2000000000\nt_qq\nt_data\Pic\2026-07\Ori\error.png`,
	})}

	references := chain.ImageReferences()

	if len(references) != 1 ||
		references[0] != `C:\Users\Example\Documents\Tencent Files\2000000000\nt_qq\nt_data\Pic\2026-07\Ori\error.png` {
		t.Fatalf("image references = %#v", references)
	}
}

func TestImageReferencesPrefersLocalFieldWhenBothFieldsAreFileURI(t *testing.T) {
	chain := Chain{Attachment(TypeImage, map[string]any{
		"origin_image_url": "file://C:\\Users\\Example\\Documents\\Tencent Files\\1000000000\\nt_qq\\nt_data\\Pic\\2026-07\\Ori\\error.png",
		"file_path":        "file://C:\\Users\\Example\\Documents\\Tencent Files\\2000000000\\nt_qq\\nt_data\\Pic\\2026-07\\Ori\\error.png",
	})}

	references := chain.ImageReferences()

	if len(references) != 1 ||
		references[0] != "file://C:\\Users\\Example\\Documents\\Tencent Files\\2000000000\\nt_qq\\nt_data\\Pic\\2026-07\\Ori\\error.png" {
		t.Fatalf("image references = %#v", references)
	}
}

func TestRecordTranscriptBecomesPromptText(t *testing.T) {
	chain := Chain{Attachment(TypeRecord, map[string]any{
		"file_path": `D:\qq-cache\voice.amr`,
		"text":      "hello from voice",
	})}

	text, mentioned := chain.PlainText("10001")

	if mentioned || text != "hello from voice" {
		t.Fatalf("plain text = %q, mentioned=%v", text, mentioned)
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
