package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

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
	`(?s)\[\[(VOICE|SING)(?::([0-9.]+))?\]\](.*?)\[\[/(?:VOICE|SING)\]\]`,
)

// voiceClip is one extracted voice/sing marker segment. Rate is an
// optional per-clip speed multiplier the model tags for the song's own
// tempo (0 keeps the persona rate).
type voiceClip struct {
	Text    string
	Singing bool
	Rate    float64
}

// voiceSynth is the text-to-speech seam; internal/tts providers implement it.
type voiceSynth interface {
	Synthesize(
		ctx context.Context,
		text, outPath, voice string,
		rate float64,
	) error
}

const voicePromptNote = `你可以发语音：说话台词用 [[VOICE]]词[[/VOICE]]（最多两段，每段不超过200字，要口语化，可以带语气词、拟声词）。

使用规则：
- 发普通语音用 [[VOICE]]；语速微调写冒号+倍速，如 [[VOICE:1.1]]。
- 标记内只放要被原样念出来的台词本身，禁止写"这是一段语音""这是设计的音色"之类的说明文字，也不要写（温柔地说）这类括号舞台指示。
- 对方连续反复要求语音时可以按你的人设傲娇拒绝，不必每次都念。
- 平时闲聊不要随便用语音，保持稀罕感。`

// singPromptNote is appended only when the chat has singing enabled —
// it teaches the [[SING]] marker the model otherwise must not emit.
const singPromptNote = `- 唱歌：对方让你唱歌时用 [[SING]]歌词[[/SING]] 哼一小段（自己编的歌词小段，不用真实歌词，不超过两段每段200字内）；语速按歌定 [[SING:0.7]] 慢歌、[[SING:1.2]] 快歌；没说清唱什么可以先反问。坦白说自己是电子音念唱、不是真人唱歌也别谦虚过头。`

// singDisabledNote is appended while singing is off — the model must
// admit it cannot sing instead of silently emitting a dropped marker.
const singDisabledNote = `- 唱歌功能暂未开放：对方要你唱歌就直说不会唱/还没学会，严禁打 [[SING]] 标记；可以主动提"要不我给你念一段"改走 [[VOICE]]。`

// voicePrivilegedRequestNote is only injected for trusted senders (private
// whitelist chats, group admins/owners, binding allowed_users): it tells the
// model explicit "say this for me" requests must actually be spoken, not
// faked in text. Kept out of the shared note so regular group members
// cannot command voice lines.
const voicePrivilegedRequestNote = `- 对方明确要求听语音（"叫声xx给我听""用语音说"之类）时，要用 [[VOICE]] 把那句台词真的念出来，不能只在文字里假装说过。`

// atPromptNote (group chats only) teaches the marker that becomes a real
// @ notification — the human touch for calling someone out or stirring
// the pot. The cap and "don't spam" hint keep it from pinging everyone.
const atPromptNote = `- 想@某人时在回复里写 [[AT]]（@当前说话的人）或 [[AT:QQ号]]（@指定成员），对方会真的收到@提醒。点名回怼、叫人来围观时用，一条消息最多@3次，别见人就@。`

// voiceClipMetaPatterns strip leftover stage directions like （温柔地唱）
// or meta sentences like "这是一段设计的音色" at the start of a clip
// before synthesis.
var voiceClipMetaPatterns = []*regexp.Regexp{
	regexp.MustCompile(`^[（(][^）)]*[唱说念讲温柔笑叹语][^）)]*[）)]\s*`),
	regexp.MustCompile(`^(这是一|此为一|以下为|以下是)[^，。,.；;]{0,20}(音色|语音|音频|歌曲|歌声|一段)[^，。,.；;]{0,15}[，。,.；;：:！!~～]*\s*`),
	regexp.MustCompile(`^这段[^，。,.；;]{0,15}(音色|语音|歌声|歌曲)[^，。,.；;]{0,15}[，。,.；;：:！!~～]*\s*`),
}

func sanitizeVoiceClip(text string) string {
	for {
		next := text
		for _, pat := range voiceClipMetaPatterns {
			next = pat.ReplaceAllString(next, "")
		}
		next = strings.TrimSpace(next)
		if next == text {
			return text
		}
		text = next
	}
}

// voiceClipJunkPatterns match content that TTS would read literally and
// sound terrible: URLs, OneBot CQ codes, markdown fences/emphasis.
var voiceClipJunkPatterns = []*regexp.Regexp{
	regexp.MustCompile(`https?://\S+|www\.\S+`),
	regexp.MustCompile(`\[CQ:[^\]]+\]`),
	regexp.MustCompile("```[^`]*```|`[^`]*`"),
	regexp.MustCompile(`[*_~#>]+`),
}

// prosodyNormalizeVoiceClip turns raw clip text into speakable prose with
// audible pacing: drop unspeakable symbols, collapse whitespace into
// comma/period pauses, dedupe punctuation, and guarantee a terminal mark.
func prosodyNormalizeVoiceClip(text string) string {
	for _, pat := range voiceClipJunkPatterns {
		text = pat.ReplaceAllString(text, "")
	}
	// Keep only runes a voice can voice: letters, digits, CJK punctuation
	// and the pause marks. Emoji, dingbats, control chars all go.
	text = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			return r
		case unicode.IsPunct(r):
			return r
		case unicode.IsSpace(r):
			return r
		case r == '～' || r == '♪':
			return '…'
		default:
			return -1
		}
	}, text)
	// Newlines and long spaces become comma pauses; if the char before the
	// break already ends the sentence, the comma is unnecessary.
	text = regexp.MustCompile(`\s*\n\s*`).ReplaceAllString(text, "，")
	text = regexp.MustCompile(`[ \t]{2,}`).ReplaceAllString(text, " ")
	// Collapse repeated punctuation into a single longer pause.
	text = regexp.MustCompile(`，{2,}`).ReplaceAllString(text, "，")
	text = regexp.MustCompile(`。{2,}`).ReplaceAllString(text, "。")
	text = regexp.MustCompile(`…{2,}`).ReplaceAllString(text, "……")
	text = regexp.MustCompile(`~{2,}`).ReplaceAllString(text, "～")
	// A comma right after a terminal mark is a stutter — drop it.
	text = regexp.MustCompile(`([。！？…])，+`).ReplaceAllString(text, "$1")
	text = strings.TrimSpace(text)
	text = strings.Trim(text, "，、")
	if text == "" {
		return ""
	}
	// Guarantee a terminal pause so the clip doesn't clip mid-breath.
	last := []rune(text)
	if !strings.ContainsRune("。！？…～", last[len(last)-1]) {
		text += "。"
	}
	return text
}

// extractVoiceMarkers returns the reply with all voice markers removed and
// the list of voice clips inside them (capped at 2 clips).
func extractVoiceMarkers(reply string) (string, []voiceClip) {
	matches := voiceMarkerPattern.FindAllStringSubmatch(reply, -1)
	if len(matches) == 0 {
		return reply, nil
	}
	clips := make([]voiceClip, 0, 2)
	for _, match := range matches {
		if len(clips) >= 2 {
			break
		}
		rate := 0.0
		if match[2] != "" {
			if v, err := strconv.ParseFloat(match[2], 64); err == nil {
				rate = v
			}
		}
		if text := prosodyNormalizeVoiceClip(
			sanitizeVoiceClip(match[3]),
		); text != "" {
			clips = append(clips, voiceClip{
				Text:    text,
				Singing: strings.EqualFold(match[1], "sing"),
				Rate:    rate,
			})
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
	clips []voiceClip,
) {
	if len(clips) == 0 || s.voiceProvider == nil {
		return
	}
	if s.chatPrefs != nil {
		if !s.chatPrefs.VoiceEnabled(state.chatType, state.chatID) {
			return
		}
		// Belt-and-braces: even if the model ignored the disabled note
		// and emitted [[SING]], drop those clips unless sing is on.
		if !s.chatPrefs.SingEnabled(state.chatType, state.chatID) {
			kept := clips[:0]
			for _, clip := range clips {
				if !clip.Singing {
					kept = append(kept, clip)
				}
			}
			clips = kept
			if len(clips) == 0 {
				return
			}
		}
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
	clips []voiceClip,
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
		rate := voiceRate
		if clip.Rate > 0.3 && clip.Rate <= 4.0 {
			rate = clip.Rate
		}
		text := clip.Text
		runes := []rune(text)
		if len(runes) > maxRunes {
			text = string(runes[:maxRunes])
		}
		path := fmt.Sprintf(
			"%s/%d-%s.mp3",
			strings.TrimRight(s.cfg.VoiceDir, "/\\"),
			time.Now().UnixNano(),
			opaqueAttentionID(selfID, chatID, text),
		)
		chain := message.Chain{message.Record(path)}
		if err := s.voiceProvider.Synthesize(
			ctx, text, path, voiceName, rate,
		); err != nil {
			s.logger.Warn(
				"voice synthesis failed, falling back to text",
				"chat", chatID, "error", err.Error(),
			)
			chain = message.Chain{message.Text("（语音合成失败）" + text)}
		}
		if index > 0 && !waitForReplyPart(
			ctx, s.replyPartDelay(text),
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
