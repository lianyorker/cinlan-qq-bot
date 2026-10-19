package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/domain"
	"github.com/lianyorker/cinlan-qq-bot/internal/message"
	"github.com/lianyorker/cinlan-qq-bot/internal/persona"
	"github.com/lianyorker/cinlan-qq-bot/internal/platform"
)

// The model can embed [[VOICE]]spoken text[[/VOICE]] in a reply; the voice
// clips are synthesized (Edge TTS by default) and sent as QQ record
// segments after the text parts. Text is always delivered even when
// synthesis fails, so content is never lost.
var voiceMarkerPattern = regexp.MustCompile(
	`(?s)\[\[VOICE\]\](.*?)\[\[/VOICE\]\]`,
)

// voiceSynth is the text-to-speech seam; internal/tts providers implement it.
type voiceSynth interface {
	Synthesize(
		ctx context.Context,
		text, outPath, voice string,
		rate float64,
	) error
}

const voicePromptNote = `你可以发语音：在回复里嵌入 [[VOICE]]要说或唱的词[[/VOICE]]（最多两段，每段不超过200字，要口语化，可以带语气词、拟声词、唱歌词）。

使用规则：
- 对方让你唱歌/发语音时就可以用 [[VOICE]] 唱一小段；没说清楚唱什么时可以先反问。
- 唱歌时用歌词形式写进 [[VOICE]]（可以是你自己编的小段，不用是真实歌词）。
- 对方连续反复要求唱歌/语音时可以按你的人设傲娇拒绝，不必每次都唱。
- 平时闲聊不要随便用语音，保持稀罕感。`

// extractVoiceMarkers returns the reply with all voice markers removed and
// the list of voice-clip texts inside them (capped at 2 clips).
func extractVoiceMarkers(reply string) (string, []string) {
	matches := voiceMarkerPattern.FindAllStringSubmatch(reply, -1)
	if len(matches) == 0 {
		return reply, nil
	}
	clips := make([]string, 0, 2)
	for _, match := range matches {
		if len(clips) >= 2 {
			break
		}
		if text := strings.TrimSpace(match[1]); text != "" {
			clips = append(clips, text)
		}
	}
	return strings.TrimSpace(
		voiceMarkerPattern.ReplaceAllString(reply, ""),
	), clips
}

type voiceBudget struct {
	mu   sync.Mutex
	last map[string]time.Time
}

// allowVoice enforces a per-chat cooldown so voice replies stay rare.
func (v *voiceBudget) allowVoice(key string, cooldown time.Duration) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.last == nil {
		v.last = make(map[string]time.Time)
	}
	if since, ok := v.last[key]; ok &&
		time.Since(since) < cooldown {
		return false
	}
	v.last[key] = time.Now()
	return true
}

// voiceObviousEroticPattern catches voice requests that are clearly sexual —
// these are denied without spending an LLM call.
var voiceObviousEroticPattern = regexp.MustCompile(
	`(?i)喘|娇喘|叫床|呻吟|发情|舔耳|吹气|色气|娇媚|ASMR|asmr|媚|淫|裸|口活|调教|主人.*舒服|亲我|吻我`,
)

const voiceSafetyPrompt = `你是语音内容安全审核员。判断下面这段将被TTS合成的语音文本是否包含色情、情色暗示、娇喘呻吟、NSFW或不适合群聊播放的内容。
只输出JSON：{"safe":true|false,"reason":"一句话"}
正常说话、唱歌、撒娇、情绪表达都算安全；涉及性暗示、喘息呻吟、低俗内容才判unsafe。宁可误判为safe也不冤枉正常内容。`

type voiceSafetyVerdict struct {
	Safe   bool   `json:"safe"`
	Reason string `json:"reason"`
}

// voiceClipSafe lets the model judge whether a voice clip is safe to
// synthesize — erotic panting/moaning requests get dropped here.
func (s *Service) voiceClipSafe(ctx context.Context, clip string) bool {
	if voiceObviousEroticPattern.MatchString(clip) {
		return false
	}
	if s.agent == nil {
		return true
	}
	decisionCtx, cancel := context.WithTimeout(ctx, s.cfg.AttentionTimeout)
	defer cancel()
	response, err := s.agent.Reply(decisionCtx, domain.AgentRequest{
		RequestID:     "voice-safety:" + opaqueAttentionID(clip),
		SessionID:     "maintenance:voice-safety",
		Text:          clip,
		RestrictTools: true,
		SystemPrompt:  voiceSafetyPrompt,
	})
	if err != nil {
		return true // judge unavailable — allow, cooldown keeps volume low
	}
	var verdict voiceSafetyVerdict
	raw := strings.TrimSpace(response.Reply)
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start ||
		json.Unmarshal([]byte(raw[start:end+1]), &verdict) != nil {
		return true
	}
	return verdict.Safe
}

// personaVoice resolves the TTS voice/rate for the active persona so a
// gentle persona does not suddenly sound like a drill sergeant. Empty
// values fall back to the provider's configured defaults.
func (s *Service) personaVoice(state *flowState) (string, float64) {
	if s.personas == nil || state == nil {
		return "", 0
	}
	var (
		profile persona.Profile
		ok      bool
	)
	if state.personaName != "" {
		profile, ok = s.personas.Get(state.personaName)
	}
	if !ok {
		profile, ok = s.personas.Default()
	}
	if !ok {
		return "", 0
	}
	return profile.Voice, profile.VoiceRate
}

func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

// voiceQuota is a global daily TTS cap persisted to a JSON file — the
// aliyun key is pay-per-use, so the bot stops after the day's budget.
type voiceQuota struct {
	mu    sync.Mutex
	path  string
	limit int
}

type voiceQuotaFile struct {
	Date string `json:"date"`
	Used int    `json:"used"`
}

// spend returns true when the day's budget still has room and records one
// synthesis against it.
func (q *voiceQuota) spend() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	state := voiceQuotaFile{}
	if raw, err := os.ReadFile(q.path); err == nil {
		_ = json.Unmarshal(raw, &state)
	}
	today := time.Now().Format("2006-01-02")
	if state.Date != today {
		state = voiceQuotaFile{Date: today}
	}
	if state.Used >= q.limit {
		return false
	}
	state.Used++
	raw, err := json.Marshal(state)
	if err == nil {
		_ = os.MkdirAll(filepath.Dir(q.path), 0o755)
		_ = os.WriteFile(q.path, raw, 0o600)
	}
	return true
}

// deliverVoiceClips gates voice output on a per-chat cooldown before
// sending so a chatty model cannot spam voice messages.
func (s *Service) deliverVoiceClips(
	ctx context.Context,
	state *flowState,
	clips []string,
) {
	if len(clips) == 0 || s.voiceProvider == nil {
		return
	}
	if s.chatPrefs != nil &&
		!s.chatPrefs.VoiceEnabled(state.chatType, state.chatID) {
		return
	}
	cooldown := s.cfg.VoiceCooldown
	if cooldown <= 0 {
		cooldown = 45 * time.Second
	}
	key := state.chatType + ":" + state.chatID
	if !s.voiceBudget.allowVoice(key, cooldown) {
		s.logger.Info(
			"voice clip skipped by cooldown",
			"chat", state.chatID,
		)
		return
	}
	voiceName, voiceRate := s.personaVoice(state)
	s.sendVoiceClips(
		ctx, state.selfID, state.chatType, state.chatID,
		clips, voiceName, voiceRate,
	)
}

// sendVoiceClips synthesizes each clip and sends it as a record message;
// failures fall back to sending the spoken text as a normal message so the
// content still reaches the chat.
func (s *Service) sendVoiceClips(
	ctx context.Context,
	selfID, chatType, chatID string,
	clips []string,
	voiceName string, voiceRate float64,
) {
	if s.voiceProvider == nil || len(clips) == 0 {
		return
	}
	maxRunes := s.cfg.VoiceMaxRunes
	if maxRunes <= 0 {
		maxRunes = 200
	}
	for index, clip := range clips {
		runes := []rune(clip)
		if len(runes) > maxRunes {
			clip = string(runes[:maxRunes])
		}
		path := fmt.Sprintf(
			"%s/%d-%s.mp3",
			strings.TrimRight(s.cfg.VoiceDir, "/\\"),
			time.Now().UnixNano(),
			opaqueAttentionID(selfID, chatID, clip),
		)
		chain := message.Chain{message.Record(path)}
		if err := s.voiceProvider.Synthesize(
			ctx, clip, path, voiceName, voiceRate,
		); err != nil {
			s.logger.Warn(
				"voice synthesis failed, falling back to text",
				"chat", chatID, "error", err.Error(),
			)
			chain = message.Chain{message.Text("（语音合成失败）" + clip)}
		}
		if index > 0 && !waitForReplyPart(
			ctx, s.replyPartDelay(clip),
		) {
			return
		}
		if err := s.sender.Send(ctx, platform.Outbound{
			ChatType: chatType,
			ChatID:   chatID,
			SelfID:   selfID,
			Chain:    chain,
		}); err != nil {
			s.logger.Warn(
				"voice record send failed",
				"chat", chatID, "error", err.Error(),
			)
			s.stats.sendErrors.Add(1)
		}
	}
}
