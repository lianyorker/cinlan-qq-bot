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

func TestSearchScopedInterleavesCollections(t *testing.T) {
	store := NewStore()
	// A fat corpus whose every document mentions the term crowds the
	// global ranking; the small collection still must surface a hit.
	for i := 0; i < 30; i++ {
		if err := store.Add(Document{
			ID:         "big-" + string(rune('a'+i)),
			Collection: "big",
			Content:    "购买 下单 支付 退款 订单流程说明 购买 下单 支付 退款",
		}); err != nil {
			t.Fatalf("Add(big) error = %v", err)
		}
	}
	if err := store.Add(Document{
		ID:         "small",
		Collection: "small",
		Content:    "卡网（购买 / 在线支付）地址见文档。",
	}); err != nil {
		t.Fatalf("Add(small) error = %v", err)
	}
	hits := store.SearchScoped("购买", 3, []string{"big", "small"})
	found := false
	for _, hit := range hits {
		if hit.DocumentID == "small" {
			found = true
		}
	}
	if !found {
		t.Fatalf("small collection starved out of top-k: %#v", hits)
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

func TestSearchSemanticConceptRecallWithoutLiteralMatch(t *testing.T) {
	store := NewStore()
	_ = store.Add(Document{
		ID:         "faq-shipping",
		Collection: "zkmall",
		Title:      "物流发货时效说明",
		Content:    "下单后48小时内仓库安排快递寄出。",
	})
	_ = store.Add(Document{
		ID:         "faq-tech",
		Collection: "zkmall",
		Title:      "服务器部署排错",
		Content:    "接口报错时请检查数据库配置与编译日志。",
	})

	// Query uses "买东西多久能收到货" / "商城售后" which shares semantic
	// commerce/shipping cluster with faq-shipping.
	hits := store.SearchScoped("商城优惠券付款后多久发货", 2, []string{"zkmall"})
	if len(hits) == 0 || hits[0].DocumentID != "faq-shipping" {
		t.Fatalf("expected faq-shipping top hit via semantic concept + BM25, got %#v", hits)
	}
}
