package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/binding"
	"github.com/lianyorker/cinlan-qq-bot/internal/domain"
	"github.com/lianyorker/cinlan-qq-bot/internal/message"
	"github.com/lianyorker/cinlan-qq-bot/internal/pipeline"
	"github.com/lianyorker/cinlan-qq-bot/internal/platform"
)

// moderationSuspectPattern triggers the LLM spam judge on messages that look
// like common spam vectors but did not match the strict recall patterns. A
// hit only costs one cheap classification call — the judge decides.
var moderationSuspectPattern = regexp.MustCompile(
	`(?i)代充|代练|代挂|代购|代写|代做|代理|低价|联系我|私聊|微信|加[我vV][xX]?|vx[:：]|v信|` +
		`扫码|二维码|倍率|中转|手机号|色情|裸|陪玩|陪聊|资源|福利|进群|群号|` +
		`推广|引流|接单|赚钱|兼职|优惠|特价|免费领|出售|转让|回收|卖号|刷单|日结`,
)

const spamJudgeSystemPrompt = `你是群消息反垃圾审核员。判断下面这条群聊消息是否为垃圾广告、引流、诈骗、色情、刷屏或转发垃圾内容。

只输出JSON：{"spam":true|false,"confidence":0.0~1.0,"reason":"一句话"}

判定垃圾的常见特征：代充/代练/卖号、低价出售、留联系方式引流、不明链接、兼职刷单、色情资源、转发广告卡片、纯图片广告、连续营销话术。
正常聊天、提问、技术讨论、分享资源给群友、表情包互动不算垃圾。
宁可漏判不可误判：拿不准就 spam:false 且 confidence 低于0.5。`

type spamVerdict struct {
	Spam       bool     `json:"spam"`
	Confidence *float64 `json:"confidence"`
	Reason     string   `json:"reason"`
}

// stageModeration enforces the binding's spam ladder:
// strict pattern match → recall + warn + action on limit (no LLM involved);
// with llm_check enabled, suspicious-but-unmatched messages go to the LLM
// spam judge, which only recalls when confident — the ban/kick decision
// still comes from the deterministic warning counter, never the model.
// Group admins, owners and whitelisted users are never moderated.
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

	detected, reason := pol.Matches(state.text), ""
	if !detected && pol.LLMCheck &&
		moderationSuspicious(state.event.Chain, state.text) {
		spam, judgeReason, err := s.classifySpam(
			ctx, state, pol.EffectiveLLMThreshold(),
		)
		if err != nil {
			s.logger.Warn(
				"spam classification failed",
				"chat", state.chatID,
				"user", state.userID,
				"error", err.Error(),
			)
			return nil
		}
		if spam {
			detected = true
			reason = judgeReason
		}
	}
	if !detected {
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
		} else {
			s.feelEvent(state, -5, "刚撤了条广告，有点无语")
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
		"via", map[bool]string{true: "llm", false: "pattern"}[reason != ""],
		"reason", reason,
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
			if action == binding.ModerationActionKick {
				s.feelEvent(state, -6, "刚把人移出群了，闹心")
			} else {
				s.feelEvent(state, -6, "刚禁言了个人，群里清净了点但有点烦")
			}
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
	notice = s.personaModerationNotice(ctx, state, notice)
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

// moderationSuspicious reports whether a message carries spam vectors —
// links, forward/share cards, image-only content, or spam-adjacent
// vocabulary — worth one LLM classification call.
func moderationSuspicious(chain message.Chain, text string) bool {
	if outboundLinkPattern.MatchString(text) {
		return true
	}
	images := 0
	for _, component := range chain {
		switch component.Type {
		case message.TypeImage:
			images++
		case message.TypeJson, message.TypeXml:
			// Rich cards/forwards are a classic spam vector.
			return true
		}
	}
	trimmed := strings.TrimSpace(text)
	if images > 0 && len([]rune(trimmed)) <= 8 {
		// Image-only or image plus a couple of words — often ad pictures.
		return true
	}
	if images >= 3 {
		return true
	}
	if trimmed == "" {
		return false
	}
	return moderationSuspectPattern.MatchString(trimmed)
}

// classifySpam asks the model whether the message is spam. The verdict only
// ever recalls/warns — ban/kick decisions stay with the warning counter.
func (s *Service) classifySpam(
	ctx context.Context,
	state *flowState,
	threshold float64,
) (bool, string, error) {
	if s.agent == nil {
		return false, "", errors.New("no agent available for spam classification")
	}
	decisionCtx, cancel := context.WithTimeout(ctx, s.cfg.AttentionTimeout)
	defer cancel()
	request := domain.AgentRequest{
		RequestID: "moderation:" + opaqueAttentionID(
			state.sessionID, state.messageID,
		),
		SessionID: "maintenance:moderation:" + opaqueAttentionID(
			state.sessionID, "",
		),
		Text:          state.text,
		Platform:      state.platform,
		ChatType:      state.chatType,
		RestrictTools: true,
		SystemPrompt:  spamJudgeSystemPrompt,
		Chain:         state.event.Chain.Clone(),
	}
	response, err := s.agent.Reply(decisionCtx, request)
	if err != nil {
		return false, "", err
	}
	verdict, err := decodeSpamVerdict(response.Reply)
	if err != nil {
		return false, "", err
	}
	if verdict.Confidence == nil ||
		*verdict.Confidence < threshold {
		return false, "", nil
	}
	return verdict.Spam, strings.TrimSpace(verdict.Reason), nil
}

// moderationVoicePrompt asks the persona to rewrite the system-banner
// warning in her own voice — a person scolding an ad spammer, not a
// notification template.
const moderationVoicePrompt = `你是群里的普通人，刚替群撤回了一条广告/违规消息。把下面的系统提示改写成一句你自己的话：像真人在群里说话，短、直接、带你的性格，不要“检测到”“第X次警告”这种系统腔，但语义必须保留（警告了几次、再犯会被禁言或移除）。只输出改写后的一句话，不要引号不要解释。`

// personaModerationNotice swaps the fixed template for a persona-voiced
// line. Model failure, empty output, or anything that still smells like a
// marker/template falls back to the original template.
func (s *Service) personaModerationNotice(
	ctx context.Context,
	state *flowState,
	template string,
) string {
	profile, ok := s.personas.Get(state.personaName)
	if !ok || strings.TrimSpace(profile.SystemPrompt) == "" {
		return template
	}
	callCtx, cancel := context.WithTimeout(ctx, s.cfg.AttentionTimeout)
	defer cancel()
	request := domain.AgentRequest{
		RequestID: "moderation-notice:" + opaqueAttentionID(
			state.sessionID, state.messageID,
		),
		SessionID: "maintenance:moderation:" + opaqueAttentionID(
			state.sessionID, "",
		),
		Text:          template,
		SystemPrompt:  profile.SystemPrompt + "\n\n" + moderationVoicePrompt,
		RestrictTools: true,
	}
	var response domain.AgentResponse
	var err error
	if s.providers != nil {
		if state.providerName != "" {
			response, err = s.providers.ReplyWith(callCtx, state.providerName, request)
		} else {
			response, err = s.providers.Reply(callCtx, request)
		}
	} else if s.agent != nil {
		response, err = s.agent.Reply(callCtx, request)
	} else {
		return template
	}
	if err != nil {
		s.logger.Warn("moderation notice voice failed", "error", err.Error())
		return template
	}
	if voiced := sanitizeModerationVoice(response.Reply); voiced != "" {
		return voiced
	}
	return template
}

// sanitizeModerationVoice keeps the first non-empty line of the model's
// answer, strips wrapping quotes and stray [[..]] markers, and caps length
// so a rambling model can't turn a warning into a speech.
func sanitizeModerationVoice(reply string) string {
	line := ""
	for _, candidate := range strings.Split(reply, "\n") {
		candidate = strings.TrimSpace(candidate)
		if candidate != "" {
			line = candidate
			break
		}
	}
	if line == "" || strings.Contains(line, "[[") {
		return ""
	}
	line = strings.Trim(line, "\"'「」“”")
	runes := []rune(line)
	if len(runes) > 80 {
		line = string(runes[:80])
	}
	if strings.TrimSpace(line) == "" {
		return ""
	}
	return line
}

// decodeSpamVerdict parses the judge's JSON reply, tolerating surrounding
// prose by extracting the first JSON object.
func decodeSpamVerdict(value string) (spamVerdict, error) {
	value = strings.TrimSpace(value)
	start := strings.Index(value, "{")
	end := strings.LastIndex(value, "}")
	if start < 0 || end <= start {
		return spamVerdict{}, errors.New("spam verdict is not JSON")
	}
	var verdict spamVerdict
	if err := json.Unmarshal(
		[]byte(value[start:end+1]), &verdict,
	); err != nil {
		return spamVerdict{}, fmt.Errorf("decode spam verdict: %w", err)
	}
	return verdict, nil
}
