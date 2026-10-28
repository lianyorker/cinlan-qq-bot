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

func TestPlainTextRendersReadableSegments(t *testing.T) {
	cases := []struct {
		name string
		data map[string]any
		typ  Type
		want string
	}{
		{"face id maps to name", map[string]any{"id": "28"}, TypeFace, "[表情:憨笑]"},
		{"unknown face falls back", map[string]any{"id": "9999"}, TypeFace, "[表情]"},
		{"share keeps title", map[string]any{"title": "B站视频", "url": "https://b23.tv/x"}, TypeShare, "[链接:B站视频]"},
		{"share without title shows url", map[string]any{"url": "https://example.com/a"}, TypeShare, "[链接:https://example.com/a]"},
		{"music keeps title", map[string]any{"title": "晴天"}, TypeMusic, "[音乐:晴天]"},
		{"location joins title+content", map[string]any{"title": "星巴克", "content": "上海静安"}, TypeLocation, "[位置:星巴克 上海静安]"},
		{"dice keeps value", map[string]any{"value": "6"}, TypeDice, "[骰子:6]"},
		{"rps maps value", map[string]any{"value": "2"}, TypeRPS, "[猜拳:石头]"},
		{"mface uses summary", map[string]any{"summary": "[摸鱼]"}, TypeMFace, "[表情:摸鱼]"},
		{"json card uses prompt", map[string]any{"data": `{"prompt":"[QQ小程序]某卡片"}`}, TypeJson, "[JSON:[QQ小程序]某卡片]"},
		{"file keeps filename", map[string]any{"name": "期末卷子.pdf"}, TypeFile, "[文件:期末卷子.pdf]"},
		{"video keeps filename", map[string]any{"name": "meme.mp4"}, TypeVideo, "[视频:meme.mp4]"},
		{"red bag keeps title", map[string]any{"title": "恭喜发财"}, TypeRedBag, "[红包:恭喜发财]"},
		{"red bag falls back", map[string]any{}, TypeRedBag, "[红包]"},
	}
	for _, tc := range cases {
		chain := Chain{{Type: tc.typ, Data: tc.data}}
		text, _ := chain.PlainText("10001")
		if text != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, text, tc.want)
		}
	}
}

func TestVideoThumbReferencesCollectsCoverOnly(t *testing.T) {
	chain := Chain{
		Attachment(TypeVideo, map[string]any{
			"file":  "https://cdn.example.com/v/a.mp4",
			"thumb": "https://cdn.example.com/v/a.jpg",
		}),
		Text("看这个"),
		Attachment(TypeVideo, map[string]any{
			"file": "https://cdn.example.com/v/b.mp4",
			// no thumb — must not emit the mp4 url itself
		}),
	}
	refs := chain.VideoThumbReferences()
	if len(refs) != 1 || refs[0] != "https://cdn.example.com/v/a.jpg" {
		t.Fatalf("VideoThumbReferences = %v", refs)
	}
	if imgs := chain.ImageReferences(); len(imgs) != 0 {
		t.Fatalf("ImageReferences must not leak video = %v", imgs)
	}
}