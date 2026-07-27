package qqnt

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lianyorker/cinlan-qq-bot/internal/message"
	"github.com/lianyorker/cinlan-qq-bot/internal/platform"
)

type nativeEvent struct {
	ID          string         `json:"id"`
	Platform    string         `json:"platform"`
	PostType    string         `json:"post_type"`
	MessageType string         `json:"message_type"`
	SubType     string         `json:"sub_type"`
	MessageID   string         `json:"message_id"`
	SelfID      string         `json:"self_id"`
	UserID      string         `json:"user_id"`
	ChatID      string         `json:"chat_id"`
	SenderName  string         `json:"sender_name"`
	SenderRole  string         `json:"sender_role"`
	Chain       message.Chain  `json:"chain"`
	RawMessage  string         `json:"raw_message"`
	Metadata    map[string]any `json:"metadata"`
}

func decodeNativeEvent(raw json.RawMessage) (platform.Event, error) {
	var current nativeEvent
	if err := decodePayload(raw, &current); err != nil {
		return platform.Event{}, err
	}
	if current.Platform != platform.PlatformQQNative {
		return platform.Event{}, fmt.Errorf("unexpected native platform %q", current.Platform)
	}
	if current.PostType == "message" {
		if current.MessageType != platform.ChatGroup &&
			current.MessageType != platform.ChatPrivate {
			return platform.Event{}, fmt.Errorf(
				"unsupported native message type %q",
				current.MessageType,
			)
		}
		if strings.TrimSpace(current.MessageID) == "" ||
			strings.TrimSpace(current.SelfID) == "" ||
			strings.TrimSpace(current.UserID) == "" ||
			strings.TrimSpace(current.ChatID) == "" {
			return platform.Event{}, fmt.Errorf("native message event is missing required IDs")
		}
	}
	return platform.Event{
		ID:          current.ID,
		Platform:    current.Platform,
		PostType:    current.PostType,
		MessageType: current.MessageType,
		SubType:     current.SubType,
		MessageID:   current.MessageID,
		SelfID:      current.SelfID,
		UserID:      current.UserID,
		ChatID:      current.ChatID,
		SenderName:  current.SenderName,
		SenderRole:  current.SenderRole,
		Chain:       current.Chain,
		RawMessage:  current.RawMessage,
		Metadata:    current.Metadata,
	}, nil
}
