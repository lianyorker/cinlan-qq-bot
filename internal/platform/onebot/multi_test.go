package onebot

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/lianyorker/cinlan-qq-bot/internal/message"
	coreonebot "github.com/lianyorker/cinlan-qq-bot/internal/onebot"
	"github.com/lianyorker/cinlan-qq-bot/internal/platform"
)

func TestLoadAccountsFileResolvesTokenEnvironment(t *testing.T) {
	t.Setenv("SUPPORT_QQ_TOKEN", "secret")
	path := filepath.Join(t.TempDir(), "accounts.json")
	data := []byte(`{
		"default_account": "support",
		"accounts": [
			{
				"name": "support",
				"self_id": "10001",
				"url": "ws://127.0.0.1:3001",
				"access_token_env": "SUPPORT_QQ_TOKEN"
			},
			{
				"name": "disabled",
				"self_id": "10002",
				"enabled": false
			}
		]
	}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	configs, defaultAccount, err := LoadAccountsFile(path)
	if err != nil {
		t.Fatalf("LoadAccountsFile() error = %v", err)
	}
	if len(configs) != 2 || defaultAccount != "support" {
		t.Fatalf("configs = %#v, default = %q", configs, defaultAccount)
	}
	for _, config := range configs {
		if config.Name == "support" && config.AccessToken != "secret" {
			t.Fatalf("support token was not resolved")
		}
	}
}

func TestLoadAccountsFileAcceptsHTTPTransports(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.json")
	data := []byte(`{
		"default_account": "sse",
		"accounts": [
			{
				"name": "sse",
				"self_id": "10001",
				"transport": "http_sse",
				"url": "http://127.0.0.1:3000"
			},
			{
				"name": "reverse",
				"self_id": "10002",
				"transport": "reverse_http",
				"url": "http://127.0.0.1:3001",
				"listen_addr": "127.0.0.1:3101",
				"path": "/events"
			}
		]
	}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	configs, defaultAccount, err := LoadAccountsFile(path)
	if err != nil {
		t.Fatalf("LoadAccountsFile() error = %v", err)
	}
	if len(configs) != 2 || defaultAccount != "sse" ||
		configs[0].Transport != coreonebot.TransportReverseHTTP ||
		configs[1].Transport != coreonebot.TransportHTTPSSE {
		t.Fatalf("configs = %#v, default = %q", configs, defaultAccount)
	}
}

func TestMultiAdapterRoutesOutboundBySelfID(t *testing.T) {
	firstURL, firstActions, closeFirst := newOneBotActionServer(t)
	defer closeFirst()
	secondURL, secondActions, closeSecond := newOneBotActionServer(t)
	defer closeSecond()

	adapter, err := NewMultiAdapter([]AccountConfig{
		{
			Name:      "first",
			SelfID:    "10001",
			URL:       firstURL,
			Transport: coreonebot.TransportForwardWebSocket,
			Enabled:   true,
		},
		{
			Name:      "second",
			SelfID:    "10002",
			URL:       secondURL,
			Transport: coreonebot.TransportForwardWebSocket,
			Enabled:   true,
		},
	}, "first", time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewMultiAdapter() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- adapter.Run(ctx)
	}()
	waitForAccounts(t, adapter, 2)

	sendDone := make(chan error, 1)
	go func() {
		sendDone <- adapter.Send(context.Background(), platform.Outbound{
			ChatType: platform.ChatGroup,
			ChatID:   "30003",
			SelfID:   "10002",
			Chain:    message.Chain{message.Text("hello")},
		})
	}()
	select {
	case action := <-secondActions:
		if action["action"] != "send_group_msg" {
			t.Fatalf("second action = %#v", action)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for second account action")
	}
	select {
	case action := <-firstActions:
		t.Fatalf("unexpected first account action: %#v", action)
	default:
	}
	if err := <-sendDone; err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("MultiAdapter did not stop")
	}
}

func newOneBotActionServer(t *testing.T) (string, <-chan map[string]any, func()) {
	t.Helper()
	actions := make(chan map[string]any, 4)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		conn, err := upgrader.Upgrade(writer, request, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, payload, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var action map[string]any
			if err := json.Unmarshal(payload, &action); err != nil {
				continue
			}
			actions <- action
			_ = conn.WriteJSON(map[string]any{
				"status":  "ok",
				"retcode": 0,
				"data":    map[string]any{"message_id": 1},
				"echo":    action["echo"],
			})
		}
	}))
	return "ws" + strings.TrimPrefix(server.URL, "http"), actions, server.Close
}

func waitForAccounts(t *testing.T, adapter *MultiAdapter, count int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		connected := 0
		for _, account := range adapter.Accounts() {
			if account.Connected {
				connected++
			}
		}
		if connected == count {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("connected accounts = %#v", adapter.Accounts())
}
