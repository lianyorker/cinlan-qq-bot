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
	"time"

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
	// FriendFileGroups, when non-empty, restricts the welcome file to
	// requesters who are members of at least one listed group — keeps a
	// community-specific file (e.g. zkmall SQL) from leaking to anyone
	// who merely adds the bot.
	FriendFileGroups []string
	// GroupWelcomeImage / GroupWelcomeText / GroupWelcomeGroups deliver an
	// at-mention plus a welcome message when a member joins a listed group.
	GroupWelcomeImage  string
	GroupWelcomeText   string
	GroupWelcomeGroups map[string]bool
	// WelcomeSkipUserIDs never gets friend/group welcomes — peer bot
	// accounts and our own self IDs shouldn't be greeted like members.
	WelcomeSkipUserIDs map[string]bool
	// OnSelfAction reports adapter-side sends the persona should remember —
	// welcomes fire outside the pipeline, so without this hook she'd forget
	// greeting anyone. Set by main once the bot service exists.
	OnSelfAction func(selfID, chatType, chatID, desc string)
}

// emitSelfAction forwards an adapter-side send to the persona's recall hook
// when one is wired.
func (a *Adapter) emitSelfAction(selfID, chatType, chatID, desc string) {
	if a.OnSelfAction == nil {
		return
	}
	a.OnSelfAction(selfID, chatType, chatID, desc)
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
			if event.PostType != "message" {
				a.logger.Debug("onebot non-message event",
					"post_type", event.PostType,
					"request_type", event.RequestType,
					"notice_type", event.NoticeType,
					"user_id", event.UserID.String(),
				)
			}
			if a.AutoAcceptFriend && isFriendRequest(event) {
				a.autoAcceptFriendRequest(ctx, event)
				continue
			}
			if event.PostType == "notice" && event.NoticeType == "friend_add" {
				if a.welcomeSkipped(event.UserID.String()) {
					continue
				}
				a.logger.Info("friend_add notice", "user_id", event.UserID.String())
				a.deliverFriendWelcome(ctx, event.SelfID.String(), event.UserID.String())
				continue
			}
			if event.PostType == "notice" && event.NoticeType == "group_increase" {
				groupID := event.GroupID.String()
				userID := event.UserID.String()
				if a.GroupWelcomeGroups[groupID] &&
					userID != event.SelfID.String() &&
					!a.welcomeSkipped(userID) {
					a.logger.Info("group_increase notice",
						"group_id", groupID, "user_id", userID)
					a.deliverGroupWelcome(
						ctx, event.SelfID.String(), groupID, userID,
					)
				}
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

// RecallMessage withdraws a message via delete_msg. Requires admin rights.
func (a *Adapter) RecallMessage(
	ctx context.Context,
	selfID, messageID string,
) error {
	if a.client == nil {
		return fmt.Errorf("onebot adapter has no client")
	}
	return a.client.DeleteMessage(ctx, messageID)
}

// BanMember mutes a group member for duration via set_group_ban.
func (a *Adapter) BanMember(
	ctx context.Context,
	selfID, groupID, userID string,
	duration time.Duration,
) error {
	if a.client == nil {
		return fmt.Errorf("onebot adapter has no client")
	}
	return a.client.SetGroupBan(ctx, groupID, userID, duration)
}

// KickMember removes a group member via set_group_kick (rejoin allowed).
func (a *Adapter) KickMember(
	ctx context.Context,
	selfID, groupID, userID string,
) error {
	if a.client == nil {
		return fmt.Errorf("onebot adapter has no client")
	}
	return a.client.SetGroupKick(ctx, groupID, userID, false)
}

// FetchMessage returns the parsed chain of a message fetched via get_msg.
// The selfID argument is ignored on single-account adapters.
func (a *Adapter) FetchMessage(
	ctx context.Context,
	selfID, messageID string,
) (message.Chain, error) {
	raw, err := a.Call(ctx, "get_msg", map[string]any{
		"message_id": messageID,
	})
	if err != nil {
		return nil, err
	}
	return extractQuotedChain(raw)
}

// IsGroupMember reports whether userID belongs to groupID via
// get_group_member_info. Any lookup failure is treated as not a member.
func (a *Adapter) IsGroupMember(
	ctx context.Context,
	selfID, groupID, userID string,
) (bool, error) {
	if a.client == nil {
		return false, fmt.Errorf("onebot adapter has no client")
	}
	info, err := a.client.GetGroupMemberInfo(ctx, groupID, userID, false)
	if err != nil {
		return false, nil
	}
	return len(info) > 0, nil
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
	a.deliverFriendWelcome(ctx, event.SelfID.String(), userID)
}

// friendWelcomeSent dedupes welcome delivery across the request-accept and
// friend_add-notice paths (both can fire for one approval).
type friendWelcomeSent struct {
	mu   sync.Mutex
	last map[string]time.Time
}

var friendWelcomeRecent friendWelcomeSent

func (w *friendWelcomeSent) mark(userID string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.last == nil {
		w.last = make(map[string]time.Time)
	}
	if t, ok := w.last[userID]; ok && time.Since(t) < 5*time.Minute {
		return false
	}
	w.last[userID] = time.Now()
	return true
}

var groupWelcomeRecent friendWelcomeSent

// welcomeSkipped reports whether this account should never receive a
// welcome — its own bot IDs and configured peer accounts.
func (a *Adapter) welcomeSkipped(userID string) bool {
	return a.WelcomeSkipUserIDs[strings.TrimSpace(userID)]
}

// deliverGroupWelcome sends an at-mention plus the configured welcome text
// and image to a group that just gained a member. The newest group
// announcement is read first so the welcome can point at it. Deduped per
// member like friend welcomes.
func (a *Adapter) deliverGroupWelcome(ctx context.Context, selfID, groupID, userID string) {
	text := strings.TrimSpace(a.GroupWelcomeText)
	path := strings.TrimSpace(a.GroupWelcomeImage)
	if (text == "" && path == "") || !groupWelcomeRecent.mark(groupID+":"+userID) {
		return
	}
	chain := message.Chain{message.At(userID)}
	if text != "" {
		if notice := a.groupNotice(ctx, groupID); notice != "" {
			text += "\n群公告：" + notice
		}
		chain = append(chain, message.Text(text))
	}
	if path != "" {
		chain = append(chain, message.Image(path))
	}
	if err := a.client.SendGroupMessage(ctx, groupID, chain); err != nil {
		a.logger.Warn("failed to send group welcome",
			"group_id", groupID, "user_id", userID, "error", err.Error())
		return
	}
	a.logger.Info("group welcome sent",
		"group_id", groupID, "user_id", userID)
	a.emitSelfAction(
		selfID, platform.ChatGroup, groupID,
		"欢迎了新成员进群",
	)
}

// groupNotice returns the newest group announcement text (truncated), so a
// welcome can reference the real rules instead of a canned line. Any
// failure degrades to "" — the welcome still goes out.
func (a *Adapter) groupNotice(ctx context.Context, groupID string) string {
	if a.client == nil {
		return ""
	}
	data, err := a.client.CallAction(ctx, "get_group_notice", map[string]any{
		"group_id": groupID,
	})
	if err != nil {
		a.logger.Warn("failed to read group notice",
			"group_id", groupID, "error", err.Error())
		return ""
	}
	type noticeItem struct {
		Message struct {
			Text string `json:"text"`
		} `json:"message"`
		Text string `json:"text"`
	}
	var notices []noticeItem
	if err := json.Unmarshal(data, &notices); err != nil {
		// Some implementations wrap the list: {feed:[...]} or {list:[...]}.
		var wrapped struct {
			Feed []noticeItem `json:"feed"`
			List []noticeItem `json:"list"`
			Data []noticeItem `json:"data"`
		}
		if err2 := json.Unmarshal(data, &wrapped); err2 != nil {
			return ""
		}
		notices = append(notices, wrapped.Feed...)
		notices = append(notices, wrapped.List...)
		notices = append(notices, wrapped.Data...)
	}
	for index := len(notices) - 1; index >= 0; index-- {
		text := strings.TrimSpace(notices[index].Message.Text)
		if text == "" {
			text = strings.TrimSpace(notices[index].Text)
		}
		if text == "" {
			continue
		}
		runes := []rune(text)
		if len(runes) > 120 {
			text = string(runes[:120]) + "…"
		}
		return text
	}
	return ""
}

func (a *Adapter) deliverFriendWelcome(ctx context.Context, selfID, userID string) {
	if !friendWelcomeRecent.mark(userID) {
		return
	}
	if text := strings.TrimSpace(a.FriendWelcomeText); text != "" {
		if err := a.client.SendPrivateText(ctx, userID, text); err != nil {
			a.logger.Warn("failed to send friend welcome text",
				"user_id", userID, "error", err.Error())
		} else {
			a.emitSelfAction(
				selfID, platform.ChatPrivate, userID,
				"给新好友发了欢迎消息",
			)
		}
	}
	if path := strings.TrimSpace(a.FriendFilePath); path != "" {
		allowed := len(a.FriendFileGroups) == 0
		for _, groupID := range a.FriendFileGroups {
			if member, err := a.IsGroupMember(ctx, "", groupID, userID); err == nil && member {
				allowed = true
				break
			}
		}
		if !allowed {
			a.logger.Warn(
				"friend welcome file skipped — user not in allowed groups",
				"user_id", userID, "groups", a.FriendFileGroups)
			return
		}
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
	msgType := event.MessageType
	if msgType == "" && event.PostType == "notice" {
		if chatID != "" {
			msgType = platform.ChatGroup
		} else if event.UserID.String() != "" {
			// Private notices (friend_recall, friend poke) carry no
			// group_id — keep them in the private lane so bindings and
			// session keys resolve like a normal DM.
			msgType = platform.ChatPrivate
		}
	}
	if msgType == platform.ChatPrivate {
		chatID = event.UserID.String()
	}
	return platform.Event{
		ID:          fmt.Sprintf("%s:%s", event.SelfID.String(), event.MessageID.String()),
		Platform:    platform.PlatformQQOneBot,
		PostType:    event.PostType,
		MessageType: msgType,
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
			"target_id":       event.TargetID.String(),
			"operator_id":     event.OperatorID.String(),
			"honor_type":      event.HonorType,
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
