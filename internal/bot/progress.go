package bot

import (
	"context"
	"strings"

	"github.com/lianyorker/cinlan-qq-bot/internal/message"
	"github.com/lianyorker/cinlan-qq-bot/internal/platform"
)

func progressReply(allowedTools []string, text string) string {
	tools := make(map[string]struct{}, len(allowedTools))
	for _, name := range allowedTools {
		tools[strings.TrimSpace(name)] = struct{}{}
	}
	text = strings.ToLower(strings.TrimSpace(text))
	if _, ok := tools["generate_image"]; ok && containsProgressWord(
		text,
		"生成图片", "生成一张", "画一张", "画个", "图片回复", "发张图",
	) {
		return "我给你生成一下。"
	}
	if _, ok := tools["capture_webpage"]; ok && containsProgressWord(
		text,
		"网页", "网站", "官网", "截图",
	) {
		return "我打开网页看一下。"
	}
	return ""
}

func containsProgressWord(value string, words ...string) bool {
	for _, word := range words {
		if strings.Contains(value, word) {
			return true
		}
	}
	return false
}

func (s *Service) sendProgress(
	ctx context.Context,
	state *flowState,
	text string,
) {
	if s == nil || s.sender == nil || state == nil || strings.TrimSpace(text) == "" {
		return
	}
	chain := message.Chain{message.Text(text)}
	if s.cfg.GroupAtSender && state.chatType == platform.ChatGroup {
		chain = append(
			message.Chain{message.AtNamed(state.userID, state.event.SenderName)},
			chain...,
		)
	}
	err := s.sender.Send(ctx, platform.Outbound{
		ChatType: state.chatType,
		ChatID:   state.chatID,
		SelfID:   state.selfID,
		Chain:    chain,
	})
	if err != nil {
		s.stats.sendErrors.Add(1)
		s.logger.Warn(
			"failed to send progress reply",
			"chat_type", state.chatType,
			"chat_id", state.chatID,
			"message_id", state.messageID,
			"error", err,
		)
		return
	}
	s.stats.replied.Add(1)
}
