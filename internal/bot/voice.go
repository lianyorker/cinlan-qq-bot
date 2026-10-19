package bot

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/message"
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
	Synthesize(ctx context.Context, text, outPath string) error
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
	s.sendVoiceClips(
		ctx, state.selfID, state.chatType, state.chatID, clips,
	)
}

// sendVoiceClips synthesizes each clip and sends it as a record message;
// failures fall back to sending the spoken text as a normal message so the
// content still reaches the chat.
func (s *Service) sendVoiceClips(
	ctx context.Context,
	selfID, chatType, chatID string,
	clips []string,
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
		if err := s.voiceProvider.Synthesize(ctx, clip, path); err != nil {
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
