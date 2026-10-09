package binding

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFilePreservesSmartAttention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bindings.json")
	if err := os.WriteFile(path, []byte(`{
  "bindings": [{
    "name": "support",
    "platform": "*",
    "self_id": "1",
    "chat_type": "group",
    "chat_id": "2",
    "persona": "support",
    "smart_attention": true
  }]
}`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	registry, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	rule, ok := registry.Match("qq-native", "1", "group", "2")
	if !ok || rule.SmartAttention == nil || !*rule.SmartAttention {
		t.Fatalf("loaded rule = %#v", rule)
	}
}

func TestLoadFileSupportsMultipleChatAndUserSelectors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bindings.json")
	if err := os.WriteFile(path, []byte(`{
  "bindings": [{
    "name": "support",
    "platform": "*",
    "self_id": "1",
    "chat_type": "group",
    "chat_ids": ["2", "3"],
    "user_ids": ["9"],
    "persona": "support"
  }]
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.MatchActor("qq-native", "1", "group", "3", "9"); !ok {
		t.Fatal("multi-selector binding did not match")
	}
}

func TestExampleChatBindingsLoad(t *testing.T) {
	registry, err := LoadFile(filepath.Join("..", "..", "examples", "chat-bindings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if rules := registry.List(); len(rules) != 2 || rules[0].ReplyPolicy == nil || rules[1].ReplyPolicy == nil {
		t.Fatalf("example bindings = %#v", rules)
	}
}
