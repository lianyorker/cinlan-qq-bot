package platform

import (
	"context"

	"github.com/lianyorker/cinlan-qq-bot/internal/message"
)

const (
	PlatformQQOneBot = "qq-onebot"
	PlatformQQNative = "qq-native"
	ChatGroup        = "group"
	ChatPrivate      = "private"
)

type Event struct {
	ID          string
	Platform    string
	PostType    string
	MessageType string
	SubType     string
	MessageID   string
	SelfID      string
	UserID      string
	ChatID      string
	SenderName  string
	SenderRole  string
	Chain       message.Chain
	RawMessage  string
	Metadata    map[string]any
}

func (e Event) IsMessage() bool {
	return e.PostType == "message"
}

func (e Event) IsGroupMessage() bool {
	return e.IsMessage() && e.MessageType == ChatGroup && e.ChatID != ""
}

func (e Event) IsPrivateMessage() bool {
	return e.IsMessage() && e.MessageType == ChatPrivate && e.ChatID != ""
}

func (e Event) IsChatMessage() bool {
	return e.IsGroupMessage() || e.IsPrivateMessage()
}

type Outbound struct {
	ChatType string
	ChatID   string
	// SelfID selects the QQ account used to send the message. It is optional
	// for single-account deployments and required when a multi-account
	// adapter cannot infer a default account.
	SelfID  string
	ReplyTo string
	Quote   bool
	Chain   message.Chain
}

type Adapter interface {
	Name() string
	Connected() bool
	Events() <-chan Event
	Run(context.Context) error
	Send(context.Context, Outbound) error
	Call(context.Context, string, map[string]any) (any, error)
}
