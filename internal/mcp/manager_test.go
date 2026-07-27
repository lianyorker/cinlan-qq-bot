package mcp

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/tool"
)

func TestManagerDiscoversAndRegistersNamespacedTools(t *testing.T) {
	server := &testMCPServer{}
	httpServer := httptest.NewServer(http.HandlerFunc(server.handler))
	defer httpServer.Close()

	registry := tool.NewRegistry()
	manager, err := NewManager([]ServerConfig{{
		Name:        "crm",
		URL:         httpServer.URL,
		Active:      true,
		Permission:  tool.PermissionEveryone,
		Timeout:     time.Second,
		ToolTimeout: time.Second,
	}}, registry, slog.Default())
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	if err := manager.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	list := registry.List()
	if len(list) != 2 || list[0].Name != "mcp_crm_get_status" || list[1].Name != "mcp_crm_search_customer" {
		t.Fatalf("registry list = %#v", list)
	}
	result, err := registry.Execute(context.Background(), tool.Call{
		Name:      "mcp_crm_search_customer",
		Arguments: []byte(`{"query":"alice"}`),
		Actor:     tool.Actor{UserID: "1", GroupID: "2", Role: "member"},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	content, ok := result.Content.(map[string]any)
	if !ok || content["content"] == nil {
		t.Fatalf("tool result = %#v", result)
	}
	status := manager.List()
	if len(status) != 1 || status[0].State != "ready" || status[0].ToolCount != 2 {
		t.Fatalf("manager status = %#v", status)
	}
}

func TestManagerKeepsLastKnownToolsWhenRefreshFails(t *testing.T) {
	server := &testMCPServer{}
	httpServer := httptest.NewServer(http.HandlerFunc(server.handler))
	defer httpServer.Close()

	registry := tool.NewRegistry()
	manager, err := NewManager([]ServerConfig{
		{
			Name:        "crm",
			URL:         httpServer.URL,
			Active:      true,
			Permission:  tool.PermissionEveryone,
			Timeout:     time.Second,
			ToolTimeout: time.Second,
		},
	}, registry, slog.Default())
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	if err := manager.Refresh(context.Background()); err != nil {
		t.Fatalf("initial Refresh() error = %v", err)
	}
	httpServer.Close()
	if err := manager.Refresh(context.Background()); err == nil {
		t.Fatal("Refresh() error = nil, want connection failure")
	}
	if len(registry.List()) != 2 {
		t.Fatalf("registry list after refresh = %#v", registry.List())
	}
	status := manager.List()
	if len(status) != 1 || status[0].State != "degraded" {
		t.Fatalf("manager status after failed refresh = %#v", status)
	}
}
