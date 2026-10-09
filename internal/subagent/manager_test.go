package subagent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/agent"
	"github.com/lianyorker/cinlan-qq-bot/internal/domain"
	"github.com/lianyorker/cinlan-qq-bot/internal/tool"
)

func TestManagerRegistersSynchronousHandoffTool(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		if !strings.Contains(string(body), "refund") {
			t.Errorf("subagent request did not contain task: %s", body)
		}
		if !strings.Contains(string(body), `"platform":"qq-onebot"`) ||
			!strings.Contains(string(body), `"chat_type":"group"`) ||
			!strings.Contains(string(body), `"chat_id":"3"`) {
			t.Errorf("subagent request did not preserve chat context: %s", body)
		}
		if strings.Contains(string(body), "previous question") {
			t.Errorf("subagent inherited history without opt-in: %s", body)
		}
		_, _ = io.WriteString(writer, `{"reply":"售后子 Agent 已处理"}`)
	}))
	defer server.Close()
	base := agent.NewHTTPClient(agent.Config{
		Mode:         "custom",
		URL:          server.URL,
		SystemPrompt: "main",
		AuthHeader:   "Authorization",
		AuthScheme:   "Bearer",
		Timeout:      time.Second,
	}, nil)
	registry := tool.NewRegistry()
	manager, err := NewManager([]Config{{
		Name:         "after_sales",
		Description:  "售后",
		SystemPrompt: "你是售后专员",
		Active:       true,
		Permission:   tool.PermissionEveryone,
	}}, base, registry, nil, 2)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	if !registry.Has("transfer_to_after_sales") {
		t.Fatalf("handoff tool was not registered: %#v", registry.List())
	}
	result, err := registry.Execute(context.Background(), tool.Call{
		Name:      "transfer_to_after_sales",
		Arguments: []byte(`{"task":"refund order 100"}`),
		Actor: tool.Actor{
			UserID:    "2",
			Platform:  "qq-onebot",
			ChatType:  "group",
			ChatID:    "3",
			GroupID:   "3",
			SelfID:    "1",
			SessionID: "qq:self:1:group:3:user:2",
			Role:      "member",
			History:   []domain.ChatMessage{{Role: "user", Content: "previous question"}},
		},
	})
	if err != nil {
		t.Fatalf("handoff Execute() error = %v", err)
	}
	content, ok := result.Content.(map[string]any)
	if !ok || content["reply"] != "售后子 Agent 已处理" {
		t.Fatalf("handoff result = %#v", result)
	}
	if list := manager.List(); len(list) != 1 || list[0].Tool != "transfer_to_after_sales" {
		t.Fatalf("manager list = %#v", list)
	}
}

func TestManagerUsesConfiguredProvider(t *testing.T) {
	primaryServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(writer, `{"reply":"primary"}`)
	}))
	defer primaryServer.Close()
	backupServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(writer, `{"reply":"backup"}`)
	}))
	defer backupServer.Close()
	newClient := func(endpoint string) *agent.HTTPClient {
		return agent.NewHTTPClient(agent.Config{
			Mode:       "custom",
			URL:        endpoint,
			AuthHeader: "Authorization",
			Timeout:    time.Second,
		}, nil)
	}
	registry := tool.NewRegistry()
	manager, err := NewManagerWithProviders(
		[]Config{{
			Name:         "specialist",
			SystemPrompt: "specialist",
			Active:       true,
			Provider:     "backup",
		}},
		map[string]*agent.HTTPClient{
			"primary": newClient(primaryServer.URL),
			"backup":  newClient(backupServer.URL),
		},
		"primary",
		registry,
		nil,
		1,
	)
	if err != nil {
		t.Fatalf("NewManagerWithProviders() error = %v", err)
	}
	result, err := registry.Execute(context.Background(), tool.Call{
		Name:      "transfer_to_specialist",
		Arguments: []byte(`{"task":"handle"}`),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	content := result.Content.(map[string]any)
	if content["reply"] != "backup" || manager.List()[0].Provider != "backup" {
		t.Fatalf("result=%#v info=%#v", result, manager.List())
	}
}

func TestManagerReloadKeepsOldToolsOnInvalidConfig(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(writer, `{"reply":"ok"}`)
	}))
	defer server.Close()
	base := agent.NewHTTPClient(agent.Config{
		Mode: "custom", URL: server.URL, AuthHeader: "Authorization", Timeout: time.Second,
	}, nil)
	registry := tool.NewRegistry()
	manager, err := NewManager([]Config{{
		Name: "one", Description: "one", SystemPrompt: "one", Active: true,
	}}, base, registry, nil, 1)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	manager.path = filepathForTest(t, `{"agents":[{"name":"invalid name","description":"x"}]}`)
	if err := manager.Reload(context.Background()); err == nil {
		t.Fatal("Reload() error = nil, want invalid config")
	}
	if !registry.Has("transfer_to_one") {
		t.Fatalf("old handoff tool was removed after failed reload")
	}
}

func filepathForTest(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "subagents.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

func TestSubagentHistoryRequiresOptInAndAppliesLimit(t *testing.T) {
	history := []domain.ChatMessage{
		{Role: "user", Content: "one"},
		{Role: "assistant", Content: "two"},
		{Role: "user", Content: "three"},
	}
	if got := subagentHistory(Config{}, history); got != nil {
		t.Fatalf("default history = %#v", got)
	}
	got := subagentHistory(Config{InheritHistory: true, HistoryLimit: 2}, history)
	if len(got) != 2 || got[0].Content != "two" || got[1].Content != "three" {
		t.Fatalf("limited history = %#v", got)
	}
}
