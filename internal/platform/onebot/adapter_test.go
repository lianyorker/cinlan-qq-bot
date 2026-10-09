package onebot

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/lianyorker/cinlan-qq-bot/internal/onebot"
)

func TestConvertEventKeepsRichChainAndIdentity(t *testing.T) {
	raw, _ := json.Marshal([]map[string]any{
		{"type": "at", "data": map[string]any{"qq": "10001"}},
		{"type": "text", "data": map[string]any{"text": "hello"}},
	})
	event, err := convertEvent(onebot.Event{
		SelfID:      "10001",
		PostType:    "message",
		MessageType: "group",
		MessageID:   "8",
		UserID:      "20002",
		GroupID:     "30003",
		Message:     raw,
		Sender:      onebot.Sender{Card: "card", Nickname: "nick"},
	})
	if err != nil {
		t.Fatalf("convertEvent() error = %v", err)
	}
	if event.ChatID != "30003" || event.SenderName != "card" ||
		len(event.Chain) != 2 || event.Chain[0].Type != "at" {
		t.Fatalf("event = %#v", event)
	}
}

func TestAdapterAutoAcceptsFriendRequest(t *testing.T) {
	actions := make(chan map[string]any, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		conn, err := upgrader.Upgrade(writer, request, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteJSON(map[string]any{
			"time":         100,
			"self_id":      10001,
			"post_type":    "request",
			"request_type": "friend",
			"user_id":      20002,
			"flag":         "friend-flag",
			"comment":      "please add me",
		})
		for {
			_, payload, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var action map[string]any
			if err := json.Unmarshal(payload, &action); err != nil {
				continue
			}
			if action["action"] == "set_friend_add_request" {
				actions <- action
				_ = conn.WriteJSON(map[string]any{
					"status":  "ok",
					"retcode": 0,
					"echo":    action["echo"],
				})
			}
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := onebot.NewClient(onebot.ClientConfig{
		URL:           "ws" + strings.TrimPrefix(server.URL, "http"),
		AccessToken:   "token",
		ActionTimeout: time.Second,
	}, logger)
	adapter := NewAdapter(client, logger)
	adapter.AutoAcceptFriend = true
	done := make(chan error, 1)
	go func() {
		done <- adapter.Run(ctx)
	}()

	select {
	case action := <-actions:
		params, _ := action["params"].(map[string]any)
		if params["flag"] != "friend-flag" || params["approve"] != true {
			t.Fatalf("action params = %#v", params)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for set_friend_add_request")
	}

	select {
	case event := <-adapter.Events():
		t.Fatalf("friend request should not be forwarded, got %#v", event)
	case <-time.After(300 * time.Millisecond):
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("adapter did not stop")
	}
}
