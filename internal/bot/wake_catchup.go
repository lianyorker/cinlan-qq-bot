package bot

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/binding"
	"github.com/lianyorker/cinlan-qq-bot/internal/domain"
	"github.com/lianyorker/cinlan-qq-bot/internal/message"
	"github.com/lianyorker/cinlan-qq-bot/internal/platform"
)

// mutedInboundTag marks group inbounds dropped by notify_off — the
// look-back turn scans for it; attention still reads the line as plain
// context.
const mutedInboundTag = " [静音未接]"

// wakeCatchUpDelay lets the "通知已开启" confirmation land first — her
// looking back right after the ack reads like a person who just came
// back, not a stacked auto-send. Package var so tests can zero it.
var wakeCatchUpDelay = 1500 * time.Millisecond

// CatchUpAfterWake fires a synthetic "look back" turn right after an
// operator's /开启通知 flipped the chat live again. Anything muted
// dropped is already in history tagged [静音未接], so the turn walks
// the full normal pipeline — reply anchoring via [[REPLY:#N]], bubble
// shaping, [[PASS]] to stay silent when nothing missed was worth a word.
//
// It no-ops when:
//   - the chat is private (notify_off is inert there),
//   - history holds no [静音未接] line (nothing was actually missed),
//   - the binding's own policy is MentionOnly (an unmute must not make
//     an always-mention-only room suddenly answer ambient chatter),
//   - notify was flipped back off before the deferred turn ran.
func (s *Service) CatchUpAfterWake(
	ctx context.Context,
	plat, selfID, chatType, chatID, userID, senderName string,
) {
	if chatType != platform.ChatGroup || s.sessions == nil {
		return
	}
	sessionID := plat + ":self:" + selfID + ":" + chatType + ":" + chatID
	history, _ := s.sessions.Snapshot(sessionID)
	if !hasUnansweredMuted(history) {
		return
	}
	// A MentionOnly binding never answers ambient chatter — /开启通知
	// can't stretch it into answering missed ambient lines either.
	if s.bindings != nil {
		if rule, ok := s.bindings.MatchActor(
			plat, selfID, chatType, chatID, userID, nil,
		); ok {
			if rule.EffectiveReplyPolicy(
				chatType, s.cfg.RequireMention,
			).Mode == binding.ReplyModeMentionOnly {
				return
			}
		}
	}
	if wakeCatchUpDelay > 0 {
		timer := time.NewTimer(wakeCatchUpDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
	// Flipped back off during the delay — stay silent, don't speak over
	// an operator's fresh mute.
	if s.chatPrefs != nil &&
		!s.chatPrefs.NotifyEnabled(plat, selfID, chatType, chatID) {
		return
	}
	s.handlePlatformEvent(ctx, platform.Event{
		ID:          fmt.Sprintf("%s:wake-%d", selfID, time.Now().UnixNano()),
		Platform:    plat,
		PostType:    "message",
		MessageType: chatType,
		SelfID:      selfID,
		UserID:      userID,
		ChatID:      chatID,
		SenderName:  senderName,
		Chain: message.Chain{
			message.At(selfID),
			message.Text(wakeCatchUpCue),
		},
		Metadata: map[string]any{"synthetic_wake": true},
	})
	s.logger.Info("wake catch-up turn queued",
		"chat_type", chatType, "chat_id", chatID)
}

// wakeCatchUpCue is the operator-side nudge she sees as an @-addressed
// line. [[REPLY:#N]] and [[PASS]] are part of her normal marker contract.
const wakeCatchUpCue = " 通知刚开回来——看看你静音期间错过的消息" +
	"（标着 [静音未接] 的那些）：有该接的就挑一条用 [[REPLY:#N]] 接一句，" +
	"都是闲聊就过去了就当没看见，回 [[PASS]]"

// Observed lines arrive as "[MM-DD HH:MM] #N ..." — the timestamp
// prefix is optional here so stored and synthetic lines both parse.
var unansweredRefPattern = regexp.MustCompile(
	`^(?:\[[^\]]*\]\s*)?#(\d+)`,
)

// hasUnansweredMuted reports whether history holds an inbound tagged
// [静音未接] that never got answered — every #N line a (回#N) assistant
// line cites counts as handled.
func hasUnansweredMuted(messages []domain.ChatMessage) bool {
	answered := map[string]bool{}
	for _, m := range messages {
		if m.Role != "assistant" {
			continue
		}
		if ref := answeredRefOf(m.Content); ref != "" {
			answered[ref] = true
		}
	}
	for _, m := range messages {
		if m.Role != "user" || !strings.Contains(m.Content, mutedInboundTag) {
			continue
		}
		if ref := inboundRefOf(m.Content); ref != "" && !answered[ref] {
			return true
		}
	}
	return false
}

func inboundRefOf(text string) string {
	if m := unansweredRefPattern.FindStringSubmatch(
		strings.TrimSpace(text),
	); m != nil {
		return m[1]
	}
	return ""
}

// answeredRefOf pulls the N out of a "(回#N)" assistant tag. Kept
// format-loose — full/half width parens, stray spaces, same optional
// timestamp prefix observations carry.
var assistantAnsweredPattern = regexp.MustCompile(
	`^(?:\[[^\]]*\]\s*)?[（(]\s*回\s*#\s*(\d+)\s*[）)]`,
)

func answeredRefOf(text string) string {
	if m := assistantAnsweredPattern.FindStringSubmatch(
		strings.TrimSpace(text),
	); m != nil {
		return m[1]
	}
	return ""
}
