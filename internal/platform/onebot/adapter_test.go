package onebot

import (
	"encoding/json"
	"testing"

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
