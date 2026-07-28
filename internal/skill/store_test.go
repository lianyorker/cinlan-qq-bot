package skill

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lianyorker/cinlan-qq-bot/internal/platform"
	"github.com/lianyorker/cinlan-qq-bot/internal/plugin"
	"github.com/lianyorker/cinlan-qq-bot/internal/tool"
)

func TestLoadDirProgressiveDisclosureAndTools(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "refund")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	content := "---\nname: refund\ndescription: 处理退款问题\n---\n# Refund\n详细退款流程。"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	store, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir() error = %v", err)
	}
	list := store.List()
	if len(list) != 1 || list[0].Name != "refund" || list[0].Description != "处理退款问题" {
		t.Fatalf("list = %#v", list)
	}
	if !strings.Contains(store.Prompt(), "refund") || strings.Contains(store.Prompt(), "详细退款流程") {
		t.Fatalf("prompt did not remain progressive: %q", store.Prompt())
	}

	registry := tool.NewRegistry()
	if err := store.RegisterTools(registry); err != nil {
		t.Fatalf("RegisterTools() error = %v", err)
	}
	result, err := registry.Execute(context.Background(), tool.Call{
		Name:      "read_skill",
		Arguments: []byte(`{"name":"refund"}`),
		Actor:     tool.Actor{Role: "member", AllowedSkills: []string{"refund"}},
	})
	if err != nil {
		t.Fatalf("read_skill Execute() error = %v", err)
	}
	contentMap, ok := result.Content.(map[string]any)
	if !ok || !strings.Contains(contentMap["content"].(string), "详细退款流程") {
		t.Fatalf("read_skill result = %#v", result)
	}

	values := map[string]any{}
	values["scope.skills"] = []string{"refund"}
	_, err = (PromptPlugin{Store: store}).BeforeMessage(context.Background(), &plugin.MessageContext{
		Event:  platform.Event{},
		Values: values,
	})
	if err != nil {
		t.Fatalf("PromptPlugin error = %v", err)
	}
	if _, ok := values["agent.system_prompt_append"].(string); !ok {
		t.Fatalf("prompt values = %#v", values)
	}
}

func TestReloadKeepsPreviousSkillsOnFailure(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "delivery")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	path := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(path, []byte("# Delivery\n配送说明"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	store, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", maxSkillFileBytes+1)), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := store.Reload(context.Background()); err == nil {
		t.Fatal("Reload() error = nil, want oversized skill error")
	}
	if len(store.List()) != 1 || store.List()[0].Name != "delivery" {
		t.Fatalf("skills after failed reload = %#v", store.List())
	}
}
