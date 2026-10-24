package bot

import "testing"

func TestRemoveLinksDropsWholeSentence(t *testing.T) {
	text := "接码看这个 https://example.com/item/1 里面自己看吧。行吧。"
	cleaned, removed := removeLinks(text)
	if !removed {
		t.Fatal("removeLinks() removed = false, want true")
	}
	if cleaned != "行吧。" {
		t.Fatalf("removeLinks() = %q, want %q", cleaned, "行吧。")
	}
}

func TestRemoveLinksCollapsesAllLinkReply(t *testing.T) {
	cleaned, removed := removeLinks("卡网是这个 wzyp.cn/item/6i2fcg")
	if !removed {
		t.Fatal("removeLinks() removed = false, want true")
	}
	if cleaned != "" {
		t.Fatalf("removeLinks() = %q, want empty", cleaned)
	}
}

func TestRemoveLinksKeepsCleanText(t *testing.T) {
	text := "今天好热啊。"
	cleaned, removed := removeLinks(text)
	if removed {
		t.Fatal("removeLinks() removed = true, want false")
	}
	if cleaned != text {
		t.Fatalf("removeLinks() = %q, want %q", cleaned, text)
	}
}
