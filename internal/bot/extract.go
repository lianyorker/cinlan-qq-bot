package bot

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/domain"
	"github.com/lianyorker/cinlan-qq-bot/internal/security"
)

// extractMinInterval throttles the retain pass — one extraction burst
// per session per window. A busy group could otherwise fan out a judge
// call per reply; memory accretion doesn't need that granularity.
const extractMinInterval = 60 * time.Second

// maybeExtractMemory queues a background retain pass after a recorded
// exchange. Cheap judge model, throttled per session, failures silent —
// this is accretion, not on the reply path.
func (s *Service) maybeExtractMemory(state *flowState) {
	if !s.cfg.MemoryExtract || s.chatPrefs == nil ||
		(s.providers == nil && s.agent == nil) {
		return
	}
	if strings.TrimSpace(state.promptText) == "" ||
		strings.TrimSpace(state.reply) == "" {
		return
	}
	if last, ok := s.extractLast.Load(state.sessionID); ok {
		if t, isTime := last.(time.Time); isTime && s.now().Sub(t) < extractMinInterval {
			return
		}
	}
	s.extractLast.Store(state.sessionID, s.now())
	// Copy the fields the goroutine needs — state gets mutated by the
	// next message's flow while extraction is still in flight.
	go s.extractMemory(
		state.platform, state.selfID, state.chatType, state.chatID,
		state.sessionID, state.userID, state.event.SenderName,
		state.promptText, state.reply,
	)
}

// extractMemory asks the judge model which lines of this exchange are
// worth keeping long-term, then feeds them through the normal fact
// gate — quality filter, observation dedup/refine, archive overflow.
func (s *Service) extractMemory(
	platform, selfID, chatType, chatID, sessionID,
	userID, senderName, userText, botReply string,
) {
	who := senderName
	if strings.TrimSpace(who) == "" {
		who = "对方"
	}
	prompt := "你是聊天记录里的记忆提取器。下面是「" + who + "」和群聊机器人的一轮对话。\n" +
		"判断这轮里有没有值得长期记住的事——对方的偏好、习惯、个人情况、重要事件、承诺、关系、长期计划。\n" +
		"口水话、客套、一次性情绪、已经明显过时的话题不算。\n" +
		"每条一行，格式：内容|关于谁（昵称，不针对特定人就空着）\n" +
		"最多3条，直接说事别加序号别解释。没有值得记的就只回：无\n\n" +
		who + "：" + truncateRunes(userText, 300) + "\n" +
		"她：" + truncateRunes(botReply, 200)
	req := domain.AgentRequest{
		RequestID: "extract-" + sessionID + "-" + s.now().Format("150405"),
		SessionID: "extract",
		Text:      prompt,
		Platform:  platform,
		ChatType:  chatType,
		ChatID:    chatID,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	resp, err := s.judgeReply(ctx, "", req)
	if err != nil {
		s.logger.Debug("memory extract failed",
			"chat", chatType+":"+chatID, "error", err.Error())
		return
	}
	added := 0
	for _, line := range strings.Split(resp.Reply, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "无" || added >= 3 {
			continue
		}
		content, about := line, ""
		if i := strings.LastIndex(line, "|"); i >= 0 {
			content, about = strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:])
		}
		content = strings.Trim(content, "·-— ")
		if len([]rune(content)) < 4 {
			continue
		}
		if !security.TextSafeForPrompt(content) {
			s.logger.Warn("extracted fact blocked by safety gate",
				"chat", chatType+":"+chatID)
			continue
		}
		// userID tag stays empty — about carries a display name only;
		// MemberMemory resolves name-tagged facts without a card.
		if err := s.chatPrefs.AddFactWithContext(
			platform, selfID, chatType, chatID,
			"", about, "", content,
		); err == nil {
			added++
		}
	}
	if added > 0 {
		s.logger.Info("memory extract retained",
			"chat", chatType+":"+chatID, "added", added)
	}
}

// reflectMinInterval bounds the on-demand synthesis — evaluative
// questions are rare, but back-to-back "那他呢？" shouldn't each burn
// a judge call for what's usually the same bundle.
const reflectMinInterval = 10 * time.Minute

var evaluativePatterns = []*regexp.Regexp{
	// "最近/近来/这几天" + how-is-he words
	regexp.MustCompile(`(?:最近|近来|这几天|近期|这段时间)` +
		`[^。！？]{0,10}(?:咋样|怎么样|状态|情绪|心情|还好吗|变了|不对劲|怎么了)`),
	// opinion-of-him words + person reference
	regexp.MustCompile(`(?:觉得|认为|怎么看|印象里|印象中)` +
		`[^。！？]{0,8}(?:他|她|TA|这人|这个人|这家伙)`),
	regexp.MustCompile(`(?:他|她|TA|这人|这个人)` +
		`[^。！？]{0,8}(?:人怎么样|什么样的人|人如何|靠不靠谱|什么性格)`),
}

// evaluativeMemberQuery reports whether the query asks for a take on a
// person — the reflect arm's trigger. Plain mentions ("阿茶借我50")
// pull the member bundle raw; only "你觉得他怎么样" earns a synthesis.
func evaluativeMemberQuery(query string) bool {
	for _, p := range evaluativePatterns {
		if p.MatchString(query) {
			return true
		}
	}
	return false
}

// reflectOnMember synthesizes a one-breath take from the member bundle —
// Hindsight's reflect arm, on-demand edition. The raw lines stay in the
// prompt either way; this adds the "so what" a person would form from
// them. Throttled per session, short timeout, failure silent.
func (s *Service) reflectOnMember(
	ctx context.Context, state *flowState, query string, bundle []string,
) string {
	if s.providers == nil && s.agent == nil {
		return ""
	}
	if last, ok := s.reflectLast.Load(state.sessionID); ok {
		if t, isTime := last.(time.Time); isTime && s.now().Sub(t) < reflectMinInterval {
			return ""
		}
	}
	s.reflectLast.Store(state.sessionID, s.now())
	prompt := "下面是关于一个人的记忆片段（印象+具体记忆）。有人问：" +
		"「" + truncateRunes(query, 60) + "」\n" +
		"综合成一到两句你对他的整体感觉/近况判断，像她自己的真实看法，" +
		"口语化，别罗列事实别复述原文：\n" + strings.Join(bundle, "\n")
	req := domain.AgentRequest{
		RequestID: "reflect-" + state.sessionID + "-" + s.now().Format("150405"),
		SessionID: "reflect-ondemand",
		Text:      prompt,
		Platform:  state.platform,
		ChatType:  state.chatType,
		ChatID:    state.chatID,
	}
	callCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	resp, err := s.judgeReply(callCtx, "", req)
	if err != nil {
		s.logger.Debug("member reflect failed",
			"chat", state.chatType+":"+state.chatID, "error", err.Error())
		return ""
	}
	insight := strings.TrimSpace(resp.Reply)
	if insight == "" || insight == "无" {
		return ""
	}
	// One breath, not an essay — clip runaway synthesis.
	if r := []rune(insight); len(r) > 80 {
		insight = string(r[:80])
	}
	if !security.TextSafeForPrompt(insight) {
		return ""
	}
	return insight
}
