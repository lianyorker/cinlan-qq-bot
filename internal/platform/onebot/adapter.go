package onebot

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/lianyorker/cinlan-qq-bot/internal/message"
	"github.com/lianyorker/cinlan-qq-bot/internal/onebot"
	"github.com/lianyorker/cinlan-qq-bot/internal/platform"
)

type Adapter struct {
	client           *onebot.Client
	events           chan platform.Event
	logger           *slog.Logger
	AutoAcceptFriend bool
	// FriendWelcomeText / FriendFilePath are delivered to a user right after
	// their friend request is auto-accepted (e.g. a guide message plus the
	// requested resource file).
	FriendWelcomeText string
	FriendFilePath    string
}

func NewAdapter(client *onebot.Client, logger *slog.Logger) *Adapter {
	if logger == nil {
		logger = slog.Default()
	}
	return &Adapter{
		client: client,
		events: make(chan platform.Event, 512),
		logger: logger,
	}
}

func (a *Adapter) Name() string {
	return platform.PlatformQQOneBot
}

func (a *Adapter) Connected() bool {
	return a.client != nil && a.client.Connected()
}

func (a *Adapter) Events() <-chan platform.Event {
	return a.events
}

func (a *Adapter) Run(ctx context.Context) error {
	if a.client == nil {
		return fmt.Errorf("onebot adapter has no client")
	}
	defer close(a.events)
	var forwardWG sync.WaitGroup
	forwardWG.Add(1)
	go func() {
		defer forwardWG.Done()
		for event := range a.client.Events() {
			if a.AutoAcceptFriend && isFriendRequest(event) {
				a.autoAcceptFriendRequest(ctx, event)
				continue
			}
			converted, err := convertEvent(event)
			if err != nil {
				a.logger.Warn("ignored onebot event", "reason", err.Error())
				continue
			}
			select {
			case <-ctx.Done():
				return
			case a.events <- converted:
			}
		}
	}()
	err := a.client.Run(ctx)
	forwardWG.Wait()
	return err
}

func (a *Adapter) Send(ctx context.Context, outbound platform.Outbound) error {
	if a.client == nil {
		return fmt.Errorf("onebot adapter has no client")
	}
	chain := outbound.Chain.Clone()
	if outbound.Quote && outbound.ReplyTo != "" && chain.ReplyID() == "" {
		chain = append(message.Chain{message.Reply(outbound.ReplyTo)}, chain...)
	}
	switch outbound.ChatType {
	case platform.ChatPrivate:
		return a.client.SendPrivateMessage(ctx, outbound.ChatID, chain)
	case platform.ChatGroup:
		return a.client.SendGroupMessage(ctx, outbound.ChatID, chain)
	default:
		return fmt.Errorf("unsupported OneBot chat type %q", outbound.ChatType)
	}
}

func (a *Adapter) Call(ctx context.Context, action string, params map[string]any) (any, error) {
	if a.client == nil {
		return nil, fmt.Errorf("onebot adapter has no client")
	}
	return a.client.CallAction(ctx, action, params)
}

// UploadPrivateFile uploads a local file to a private chat with userID.
// The selfID argument is ignored on single-account adapters.
func (a *Adapter) UploadPrivateFile(
	ctx context.Context,
	selfID, userID, filePath, name string,
) error {
	if a.client == nil {
		return fmt.Errorf("onebot adapter has no client")
	}
	_, err := a.client.UploadPrivateFile(ctx, userID, filePath, name)
	return err
}

// FetchMessageImages returns image references inside a message fetched via
// get_msg. The selfID argument is ignored on single-account adapters.
func (a *Adapter) FetchMessageImages(
	ctx context.Context,
	selfID, messageID string,
) ([]string, error) {
	raw, err := a.Call(ctx, "get_msg", map[string]any{
		"message_id": messageID,
	})
	if err != nil {
		return nil, err
	}
	return extractImageRefs(raw)
}

// IsFriend reports whether userID is on the account's friend list.
func (a *Adapter) IsFriend(ctx context.Context, selfID, userID string) (bool, error) {
	raw, err := a.Call(ctx, "get_friend_list", map[string]any{})
	if err != nil {
		return false, err
	}
	id, err := strconv.ParseInt(strings.TrimSpace(userID), 10, 64)
	if err != nil {
		return false, err
	}
	return friendListContains(raw, id)
}

func friendListContains(raw any, userID int64) (bool, error) {
	data, err := json.Marshal(raw)
	if err != nil {
		return false, err
	}
	var friends []struct {
		UserID int64 `json:"user_id"`
	}
	if err := json.Unmarshal(data, &friends); err != nil {
		return false, err
	}
	for _, friend := range friends {
		if friend.UserID == userID {
			return true, nil
		}
	}
	return false, nil
}

func (a *Adapter) SendGroupText(ctx context.Context, groupID, replyTo, text string, quote bool) error {
	return a.client.SendGroupText(ctx, groupID, replyTo, text, quote)
}

func isFriendRequest(event onebot.Event) bool {
	return event.PostType == "request" && event.RequestType == "friend"
}

func (a *Adapter) autoAcceptFriendRequest(ctx context.Context, event onebot.Event) {
	userID := event.UserID.String()
	if event.Flag == "" {
		a.logger.Warn("onebot friend request missing flag; not approved", "user_id", userID)
		return
	}
	if err := a.client.SetFriendAddRequest(ctx, event.Flag, true, ""); err != nil {
		a.logger.Error("failed to auto-accept friend request",
			"user_id", userID, "error", err.Error())
		return
	}
	a.logger.Info("auto-accepted friend request", "user_id", userID)
	a.deliverFriendWelcome(ctx, userID)
}

func (a *Adapter) deliverFriendWelcome(ctx context.Context, userID string) {
	if text := strings.TrimSpace(a.FriendWelcomeText); text != "" {
		if err := a.client.SendPrivateText(ctx, userID, text); err != nil {
			a.logger.Warn("failed to send friend welcome text",
				"user_id", userID, "error", err.Error())
		}
	}
	if path := strings.TrimSpace(a.FriendFilePath); path != "" {
		name := filepath.Base(path)
		if _, err := a.client.UploadPrivateFile(ctx, userID, path, name); err != nil {
			a.logger.Warn("failed to send friend welcome file",
				"user_id", userID, "error", err.Error())
			return
		}
		a.logger.Info("friend welcome file sent", "user_id", userID, "file", name)
	}
}

func convertEvent(event onebot.Event) (platform.Event, error) {
	chain, err := message.ParseOneBot(event.Message, event.RawMessage, event.SelfID.String())
	if err != nil {
		return platform.Event{}, err
	}
	chatID := event.GroupID.String()
	if event.MessageType == platform.ChatPrivate {
		chatID = event.UserID.String()
	}
	return platform.Event{
		ID:          fmt.Sprintf("%s:%s", event.SelfID.String(), event.MessageID.String()),
		Platform:    platform.PlatformQQOneBot,
		PostType:    event.PostType,
		MessageType: event.MessageType,
		SubType:     event.SubType,
		MessageID:   event.MessageID.String(),
		SelfID:      event.SelfID.String(),
		UserID:      event.UserID.String(),
		ChatID:      chatID,
		SenderName:  senderName(event.Sender.Card, event.Sender.Nickname),
		SenderRole:  event.Sender.Role,
		Chain:       chain,
		RawMessage:  event.RawMessage,
		Metadata: map[string]any{
			"time":            event.Time,
			"sender":          event.Sender,
			"notice_type":     event.NoticeType,
			"request_type":    event.RequestType,
			"meta_event_type": event.MetaEventType,
			"raw_payload":     append([]byte(nil), event.RawPayload...),
		},
	}, nil
}

func senderName(card, nickname string) string {
	if card != "" {
		return card
	}
	return nickname
}

func EncodeEvent(event platform.Event) ([]byte, error) {
	return json.Marshal(event)
}
