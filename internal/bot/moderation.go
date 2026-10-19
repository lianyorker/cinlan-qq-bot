package bot

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/binding"
	"github.com/lianyorker/cinlan-qq-bot/internal/message"
	"github.com/lianyorker/cinlan-qq-bot/internal/pipeline"
	"github.com/lianyorker/cinlan-qq-bot/internal/platform"
)

// stageModeration enforces the binding's deterministic spam ladder:
// pattern match → recall the message → warn the sender → on the Nth warning
// apply the configured action (mute/kick). Group admins, owners and
// whitelisted users are never moderated, and the classifier is never asked —
// detection is pattern-only so it can't hallucinate a kick.
func (s *Service) stageModeration(
	ctx context.Context,
	event *pipeline.Context,
) error {
	state := stateFrom(event)
	if state.ignored || state.securityDenied ||
		state.chatType != platform.ChatGroup {
		return nil
	}
	pol := state.moderation
	if pol == nil || s.groupModerator == nil || s.warnStore == nil {
		return nil
	}
	senderRole := strings.ToLower(strings.TrimSpace(state.event.SenderRole))
	if senderRole == "admin" || senderRole == "owner" ||
		state.replyPolicy.AllowsUser(state.userID) {
		return nil
	}
	if !pol.Matches(state.text) {
		return nil
	}

	if state.messageID != "" {
		if err := s.groupModerator.RecallMessage(
			ctx, state.selfID, state.messageID,
		); err != nil {
			s.logger.Warn(
				"moderation recall failed",
				"chat", state.chatID,
				"user", state.userID,
				"message_id", state.messageID,
				"error", err.Error(),
			)
		}
	}

	key := state.chatType + ":" + state.chatID + ":" + state.userID
	count := s.warnStore.Warn(key)
	limit := pol.EffectiveWarnLimit()
	action := pol.EffectiveAction()
	s.logger.Info(
		"spam message recalled",
		"chat", state.chatID,
		"user", state.userID,
		"warning", fmt.Sprintf("%d/%d", count, limit),
		"action", action,
	)

	var notice string
	if count >= limit && action != binding.ModerationActionWarnOnly {
		label := "禁言处理"
		var actErr error
		switch action {
		case binding.ModerationActionKick:
			label = "移出群聊"
			actErr = s.groupModerator.KickMember(
				ctx, state.selfID, state.chatID, state.userID,
			)
		default:
			label = fmt.Sprintf(
				"禁言 %d 小时",
				pol.EffectiveBanSeconds()/3600,
			)
			actErr = s.groupModerator.BanMember(
				ctx, state.selfID, state.chatID, state.userID,
				time.Duration(pol.EffectiveBanSeconds())*time.Second,
			)
		}
		if actErr != nil {
			s.logger.Warn(
				"moderation action failed",
				"action", action,
				"chat", state.chatID,
				"user", state.userID,
				"error", actErr.Error(),
			)
			notice = fmt.Sprintf(
				"检测到疑似广告内容已撤回（第 %d/%d 次警告，自动处理失败请管理员介入）",
				count, limit,
			)
		} else {
			s.warnStore.Reset(key)
			notice = fmt.Sprintf(
				"多次发布广告内容，已%s，警告记录已清零。",
				label,
			)
		}
	} else {
		notice = fmt.Sprintf(
			"检测到疑似广告内容已撤回（第 %d/%d 次警告，达到 %d 次将被处理）",
			count, limit, limit,
		)
	}
	if !s.sendChain(
		ctx,
		state.selfID,
		state.chatType,
		state.chatID,
		state.userID,
		state.event.SenderName,
		"",
		message.Chain{message.At(state.userID), message.Text(" " + notice)},
	) {
		s.stats.sendErrors.Add(1)
	}
	state.ignored = true
	event.Stop("moderation")
	return nil
}
