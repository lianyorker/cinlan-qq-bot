package knowledge

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lianyorker/cinlan-qq-bot/internal/plugin"
)

func TestSearchRanksRelevantChineseDocument(t *testing.T) {
	store := NewStore()
	if err := store.Add(Document{
		ID:      "refund",
		Title:   "退款规则",
		Content: "订单支付后七天内可以申请退款，审核通过后原路退回。",
	}); err != nil {
		t.Fatalf("Add(refund) error = %v", err)
	}
	if err := store.Add(Document{
		ID:      "shipping",
		Title:   "配送规则",
		Content: "工作日下单后通常在二十四小时内发货。",
	}); err != nil {
		t.Fatalf("Add(shipping) error = %v", err)
	}
	hits := store.Search("如何申请退款", 1)
	if len(hits) != 1 || hits[0].DocumentID != "refund" {
		t.Fatalf("Search() = %#v", hits)
	}
}

func TestLoadDirAndPluginContext(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "faq.md")
	if err := os.WriteFile(path, []byte("# FAQ\n登录失败请联系人工客服。"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	store := NewStore()
	count, err := store.LoadDir(root)
	if err != nil || count != 1 {
		t.Fatalf("LoadDir() = %d, %v", count, err)
	}
	values := make(map[string]any)
	values["scope.knowledge_bases"] = []string{"default"}
	event := &plugin.MessageContext{Text: "登录失败", Values: values}
	if _, err := (Plugin{Store: store, TopK: 2}).BeforeMessage(context.Background(), event); err != nil {
		t.Fatalf("BeforeMessage() error = %v", err)
	}
	if values[PromptContextKey] == nil {
		t.Fatalf("prompt context missing: %#v", values)
	}
}

func TestSearchScopedDoesNotLeakAcrossCollections(t *testing.T) {
	store := NewStore()
	for _, document := range []Document{
		{ID: "a", Collection: "group-a", Content: "仅 A 群可见的退款规则"},
		{ID: "b", Collection: "group-b", Content: "仅 B 群可见的退款规则"},
	} {
		if err := store.Add(document); err != nil {
			t.Fatalf("Add() error = %v", err)
		}
	}
	hits := store.SearchScoped("退款规则", 10, []string{"group-a"})
	if len(hits) != 1 || hits[0].DocumentID != "a" {
		t.Fatalf("SearchScoped() = %#v", hits)
	}
	if hits := store.SearchScoped("退款规则", 10, nil); len(hits) != 0 {
		t.Fatalf("empty scope returned hits: %#v", hits)
	}
}
