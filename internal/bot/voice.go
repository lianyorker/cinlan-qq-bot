package bot

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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

// voicePromptNote covers ordinary voice plus the privileged "say it for
// me" case — merged from the old voicePromptNote+voicePrivilegedRequestNote
// pair so the prompt stays short.
const voicePromptNote = `你可以发语音：台词写进 [[VOICE]]词[[/VOICE]]（最多两段、每段200字内、口语化可带语气词），微调语速用 [[VOICE:1.1]] 这种；对方明说"用语音说/叫声xx给我听"时必须真的打标记念出来。标记里只放被念的台词——禁（温柔地说）这类舞台指示、禁"这是一段语音"这类说明文字，文字泡别复述/转述语音内容；闲聊少发保持稀罕感，被连着要可以按人设傲娇拒。`

// singPromptNote is appended only when the chat has singing enabled —
// it teaches the [[SING]] marker the model otherwise must not emit.
const singPromptNote = `- 唱歌：对方让你唱歌时用 [[SING]]歌词[[/SING]] 唱一小段（自己编不照抄真歌词，每段200字内，可带～拖音）；语速按歌定：抒情慢歌 [[SING:0.7]]、流行中速 [[SING:1.0]]、欢快快歌 [[SING:1.2]]、说唱 [[SING:1.3]]，歌词里的 ... 是间奏停顿；没说清唱什么先反问。顺序：最多一条寒暄→唱→最多一条收尾，歌词只进标记文字泡严禁复述、收尾只一遍、整回合文字泡≤3条；你用平时说话的声音真唱，被夸可得意，唱砸了嘴硬。`

// singDisabledNote is appended while singing is off — the model must
// admit it cannot sing instead of silently emitting a dropped marker.
const singDisabledNote = `- 唱歌功能暂未开放：对方要你唱歌就直说不会唱/还没学会，严禁打 [[SING]] 标记；可以主动提"要不我给你念一段"改走 [[VOICE]]。`

// voiceDisabledNote replaces voicePromptNote when the chat's voice switch
// is off — the model must admit it cannot speak instead of emitting
// markers that get dropped on the floor.
const voiceDisabledNote = `语音功能当前已关闭：对方要语音就直说现在开不了/没开，严禁打 [[VOICE]] 或 [[SING]] 标记假装发过——这些标记会被直接丢掉，对方什么都听不到。`

// atPromptNote (group chats only) teaches the marker that becomes a real
// @ notification — the human touch for calling someone out or stirring
// the pot. The cap and "don't spam" hint keep it from pinging everyone.
const atPromptNote = `- 回复里写 [[AT]]@当前说话的人、[[AT:QQ号]]@指定成员，对方真收@提醒；点名回怼、叫人围观时用，一条消息最多@3次别见人就@。`

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
	if s.providers == nil && s.agent == nil {
		return true
	}
	decisionCtx, cancel := context.WithTimeout(ctx, s.cfg.AttentionTimeout)
	defer cancel()
	response, err := s.judgeReply(decisionCtx, "", domain.AgentRequest{
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
	voice, rate := profile.Voice, profile.VoiceRate
	// Mood nudges the tempo: sour chats speak slower, warm chats pick up
	// the pace — the way a real person's speech follows their mood.
	if rate > 0 && s.cfg.MoodEnabled && s.chatPrefs != nil {
		rate = moodRate(rate, s.chatPrefs.MoodScore(
			state.platform, state.selfID,
			state.chatType, state.chatID, s.now(),
		))
	}
	return voice, rate
}

// moodRate maps the -100..+100 mood score onto a gentle tempo factor:
// -100 → ×0.85, 0 → ×1.0, +100 → ×1.15, then clamps to 0.5..2.0 so a
// broken mood input can never garble the clip.
func moodRate(rate float64, mood int) float64 {
	rate *= 1.0 + float64(mood)/666.0
	if rate < 0.5 {
		return 0.5
	}
	if rate > 2.0 {
		return 2.0
	}
	return rate
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
		if !s.chatPrefs.VoiceEnabled(state.platform, state.selfID, state.chatType, state.chatID) {
			return
		}
		// Belt-and-braces: even if the model ignored the disabled note
		// and emitted [[SING]], drop those clips unless sing is on.
		if !s.chatPrefs.SingEnabled(state.platform, state.selfID, state.chatType, state.chatID) {
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
	s.feelAmbient(
		ctx, state, "voice", 1, "发了条语音", 10*time.Minute,
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
		provider := s.voiceProvider
		clipVoice := voiceName
		if clip.Singing && s.singProvider != nil {
			// Singing rides the dedicated engine (Breeze) with its
			// own cloned voice — the persona's plain-voice name is
			// meaningless there and would 404. The (sings) tag is
			// Breeze's audio-tag contract for vocalized singing.
			provider = s.singProvider
			clipVoice = ""
			text = "(sings) " + text
		}
		path := fmt.Sprintf(
			"%s/%d-%s.mp3",
			strings.TrimRight(s.cfg.VoiceDir, "/\\"),
			time.Now().UnixNano(),
			opaqueAttentionID(selfID, chatID, text),
		)
		// Disk cache: the same spoken text reuses the rendered file
		// instead of paying API quota again. Singing is skipped — lyrics
		// rarely repeat and each performance should differ anyway.
		useCache := !clip.Singing && strings.TrimSpace(s.cfg.VoiceCachePath) != ""
		cachePath := ""
		cacheHit := false
		if useCache {
			cachePath = filepath.Join(
				s.cfg.VoiceCachePath, voiceCacheKey(text, clipVoice, rate),
			)
			if _, statErr := os.Stat(cachePath); statErr == nil {
				now := time.Now()
				_ = os.Chtimes(cachePath, now, now)
				path = cachePath
				cacheHit = true
			}
		}
		// NapCat needs a real file:// URI — a relative path like
		// "data/voice/x.mp3" is rejected as 识别URL失败 (retcode 1200).
		absPath, absErr := filepath.Abs(path)
		if absErr != nil {
			absPath = path
		}
		chain := message.Chain{
			message.Record("file://" + filepath.ToSlash(absPath)),
		}
		if !cacheHit {
			err := provider.Synthesize(
				ctx, text, path, clipVoice, rate,
			)
			if err != nil && clipVoice != "" {
				// A broken or expired custom voice shouldn't silence the
				// feature — retry once with the provider default voice.
				s.logger.Warn(
					"voice synthesis failed, retrying default voice",
					"chat", chatID,
					"voice", clipVoice, "error", err.Error(),
				)
				err = provider.Synthesize(ctx, text, path, "", rate)
			}
			if err != nil && provider != s.voiceProvider {
				// Singing engine down — keep the lyrics audible on the
				// plain voice rather than losing the clip entirely.
				err = s.voiceProvider.Synthesize(ctx, clip.Text, path, voiceName, rate)
			}
			if err != nil {
				s.logger.Warn(
					"voice synthesis failed, falling back to text",
					"chat", chatID, "error", err.Error(),
				)
				chain = message.Chain{message.Text(text)}
			} else if useCache {
				if mkErr := os.MkdirAll(s.cfg.VoiceCachePath, 0o755); mkErr == nil {
					if cpErr := copyFile(path, cachePath); cpErr != nil {
						s.logger.Warn(
							"voice cache write failed",
							"chat", chatID, "error", cpErr.Error(),
						)
					}
				}
			}
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

// voiceCacheKey hashes the clip inputs into a stable filename — the same
// text+voice+rate always lands on the same cache entry.
func voiceCacheKey(text, voice string, rate float64) string {
	h := sha256.New()
	h.Write([]byte(text))
	h.Write([]byte{0})
	h.Write([]byte(voice))
	h.Write([]byte{0})
	_ = binary.Write(h, binary.LittleEndian, rate)
	return hex.EncodeToString(h.Sum(nil)[:16]) + ".mp3"
}

// copyFile duplicates a rendered clip into the cache; Rename would move
// the temp file NapCat still needs to read for upload.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

// voiceCacheLoop sweeps expired files once at startup, then hourly.
// No stop channel needed — the process outlives it.
func (s *Service) voiceCacheLoop() {
	dir := strings.TrimSpace(s.cfg.VoiceCachePath)
	if dir == "" {
		return
	}
	sweepVoiceCache(dir, int64(s.cfg.VoiceCacheMaxMB)<<20, s.cfg.VoiceCacheTTL)
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		sweepVoiceCache(dir, int64(s.cfg.VoiceCacheMaxMB)<<20, s.cfg.VoiceCacheTTL)
	}
}

// sweepVoiceCache drops files older than ttl, then evicts oldest-first
// until the directory fits under maxBytes. Zero values skip that bound.
func sweepVoiceCache(dir string, maxBytes int64, ttl time.Duration) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type fileInfo struct {
		path  string
		size  int64
		mtime time.Time
	}
	var files []fileInfo
	var totalSize int64
	now := time.Now()
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if ttl > 0 && now.Sub(info.ModTime()) > ttl {
			_ = os.Remove(path)
			continue
		}
		files = append(files, fileInfo{path, info.Size(), info.ModTime()})
		totalSize += info.Size()
	}
	if maxBytes <= 0 || totalSize <= maxBytes {
		return
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].mtime.Before(files[j].mtime)
	})
	for _, f := range files {
		if totalSize <= maxBytes {
			break
		}
		if err := os.Remove(f.path); err == nil {
			totalSize -= f.size
		}
	}
}
