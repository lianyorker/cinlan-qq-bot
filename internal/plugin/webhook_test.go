package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/platform"
)

func TestWebhookPluginRunsBeforeAfterAndEventHooks(t *testing.T) {
	hooks := make(chan string, 3)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload webhookRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode webhook request: %v", err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		hooks <- payload.Hook
		writer.Header().Set("Content-Type", "application/json")
		switch payload.Hook {
		case HookBeforeMessage:
			_, _ = writer.Write([]byte(`{"handled":true,"reply":"before reply"}`))
		case HookAfterMessage:
			_, _ = writer.Write([]byte(`{"reply":"after reply"}`))
		default:
			writer.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()

	registry := NewRegistry()
	manager, err := NewWebhookManager([]WebhookConfig{{
		Name:     "customer",
		URL:      server.URL,
		Enabled:  true,
		Hooks:    map[string]struct{}{HookBeforeMessage: {}, HookAfterMessage: {}, HookEvent: {}},
		Timeout:  time.Second,
		FailOpen: false,
	}}, registry, nil)
	if err != nil {
		t.Fatalf("NewWebhookManager() error = %v", err)
	}
	event := &MessageContext{
		Event:     platform.Event{PostType: "message", UserID: "20002"},
		SessionID: "session",
		Text:      "question",
	}
	decision, err := registry.Before(context.Background(), event)
	if err != nil || !decision.Handled || decision.Reply != "before reply" {
		t.Fatalf("Before() = %#v, error=%v", decision, err)
	}
	event.Reply = decision.Reply
	if err := registry.After(context.Background(), event); err != nil {
		t.Fatalf("After() error = %v", err)
	}
	if event.Reply != "after reply" {
		t.Fatalf("reply = %q", event.Reply)
	}
	if err := registry.Event(context.Background(), event.Event); err != nil {
		t.Fatalf("Event() error = %v", err)
	}
	for _, want := range []string{HookBeforeMessage, HookAfterMessage, HookEvent} {
		if got := <-hooks; got != want {
			t.Fatalf("hook = %q, want %q", got, want)
		}
	}
	info := manager.List()
	if len(info) != 1 || info[0].Calls != 3 || info[0].Failures != 0 {
		t.Fatalf("runtime info = %#v", info)
	}
}

func TestWebhookPluginFailOpen(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Error(writer, "failure", http.StatusBadGateway)
	}))
	defer server.Close()
	plugin := &WebhookPlugin{
		config: WebhookConfig{
			Name:     "optional",
			URL:      server.URL,
			Enabled:  true,
			Hooks:    map[string]struct{}{HookBeforeMessage: {}},
			Timeout:  time.Second,
			FailOpen: true,
		},
		client: server.Client(),
	}
	if _, err := plugin.BeforeMessage(context.Background(), &MessageContext{}); err != nil {
		t.Fatalf("BeforeMessage() error = %v", err)
	}
	_, failures, _, _ := plugin.status()
	if failures != 1 {
		t.Fatalf("failures = %d", failures)
	}
}

func TestLoadWebhookFileResolvesHeaderEnvironment(t *testing.T) {
	t.Setenv("CRM_PLUGIN_TOKEN", "secret")
	path := filepath.Join(t.TempDir(), "plugins.json")
	data := []byte(`{
		"plugins": [{
			"name": "crm",
			"url": "http://127.0.0.1:9001/hook",
			"hooks": ["before", "event"],
			"header_env": {"Authorization": "CRM_PLUGIN_TOKEN"},
			"timeout": "2s",
			"fail_open": false
		}]
	}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	configs, err := LoadWebhookFile(path)
	if err != nil {
		t.Fatalf("LoadWebhookFile() error = %v", err)
	}
	if len(configs) != 1 || configs[0].Headers["Authorization"] != "secret" ||
		configs[0].FailOpen || configs[0].Timeout != 2*time.Second {
		t.Fatalf("configs = %#v", configs)
	}
}
