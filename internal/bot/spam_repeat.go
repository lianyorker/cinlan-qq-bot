package bot

import (
	"context"
	"crypto/rand"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/pipeline"
	"github.com/lianyorker/cinlan-qq-bot/internal/platform"
)

// spamRepeatEntry tracks one user's repeated inbound text inside a chat
// so command/copy-paste spam can be penalized instead of just nagged at.
// penalized marks whether this streak already cost them affinity — the
// deduction lands once per streak, not on every repeated message.
type spamRepeatEntry struct {
	norm      string
	count     int
	first     time.Time
	penalized bool
}

func normalizeRepeatText(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// noteRepeatSpam records the inbound text and returns how many times in a
// row the same normalized message arrived inside the window. The counter
// resets when the text changes or the window lapses.
func (s *Service) noteRepeatSpam(key, text string, window time.Duration) int {
	if window <= 0 {
		window = 2 * time.Minute
	}
	norm := normalizeRepeatText(text)
	if norm == "" {
		return 0
	}
	s.spamMu.Lock()
	defer s.spamMu.Unlock()
	if s.spamRepeats == nil {
		s.spamRepeats = make(map[string]spamRepeatEntry)
	}
	now := s.now()
	// Lazy sweep keeps the map bounded under sustained traffic.
	if len(s.spamRepeats) > 512 {
		for k, entry := range s.spamRepeats {
			if now.Sub(entry.first) >= window {
				delete(s.spamRepeats, k)
			}
		}
	}
	entry, ok := s.spamRepeats[key]
	if !ok || entry.norm != norm || now.Sub(entry.first) >= window {
		s.spamRepeats[key] = spamRepeatEntry{norm: norm, count: 1, first: now}
		return 1
	}
	entry.count++
	s.spamRepeats[key] = entry
	return entry.count
}

// markSpamPenalized reports whether this streak already paid the affinity
// penalty; first trigger returns false and marks it paid.
func (s *Service) markSpamPenalized(key string) bool {
	s.spamMu.Lock()
	defer s.spamMu.Unlock()
	entry, ok := s.spamRepeats[key]
	if !ok || entry.penalized {
		return true
	}
	entry.penalized = true
	s.spamRepeats[key] = entry
	return false
}

// pickSpamMode resolves SPAM_REPEAT_MODE. A single keyword ("hint",
// "voice", "ignore", "off") always wins; a weighted mix like
// "hint:50,voice:30,ignore:20" rolls a crypto-random pick per trigger so
// the response isn't the same scolding every time.
func pickSpamMode(spec string) string {
	type weighted struct {
		mode   string
		weight int
	}
	var (
		options []weighted
		total   int
	)
	for _, piece := range strings.Split(spec, ",") {
		piece = strings.TrimSpace(strings.ToLower(piece))
		if piece == "" {
			continue
		}
		mode, weight := piece, 1
		if i := strings.LastIndex(piece, ":"); i >= 0 {
			if w, err := strconv.Atoi(strings.TrimSpace(piece[i+1:])); err == nil && w > 0 {
				mode = strings.TrimSpace(piece[:i])
				weight = w
			}
		}
		switch mode {
		case "hint", "voice", "ignore", "off":
			options = append(options, weighted{mode, weight})
			total += weight
		}
	}
	if len(options) == 0 {
		return "hint"
	}
	if total <= 0 {
		return options[0].mode
	}
	roll, err := rand.Int(rand.Reader, big.NewInt(int64(total)))
	if err != nil {
		return "hint"
	}
	cursor := roll.Int64()
	for _, option := range options {
		cursor -= int64(option.weight)
		if cursor < 0 {
			return option.mode
		}
	}
	return options[len(options)-1].mode
}

// fatigueEntry tracks how many replies in a row a chat got for the same
// user — a human stops engaging when someone keeps poking after being
// answered, so past a threshold we start dropping their messages.
type fatigueEntry struct {
	userID string
	streak int
	at     time.Time
}

// noteReplySent records that the bot answered userID in this chat. The
// streak resets when a different user gets the reply.
func (s *Service) noteReplySent(chatKey, userID string) {
	s.fatigueMu.Lock()
	defer s.fatigueMu.Unlock()
	if s.chatFatigue == nil {
		s.chatFatigue = make(map[string]fatigueEntry)
	}
	entry := s.chatFatigue[chatKey]
	if entry.userID == userID {
		entry.streak++
	} else {
		entry = fatigueEntry{userID: userID, streak: 1}
	}
	entry.at = s.now()
	s.chatFatigue[chatKey] = entry
}

// checkMentionFatigue drops further messages from a user who already got
// several replies in a row — same person nagging a refused/done topic
// gets ignored the way a human would drift off. Only group chats; a
// private chat's only partner shouldn't be stonewalled.
func (s *Service) checkMentionFatigue(
	ctx context.Context,
	event *pipeline.Context,
	state *flowState,
) bool {
	count := s.cfg.MentionFatigueCount
	if count <= 0 || state.chatType != platform.ChatGroup {
		return false
	}
	key := state.platform + ":" + state.chatType + ":" + state.chatID
	window := s.cfg.MentionFatigueWindow
	if window <= 0 {
		window = 10 * time.Minute
	}
	s.fatigueMu.Lock()
	entry, ok := s.chatFatigue[key]
	if ok && s.now().Sub(entry.at) >= window {
		delete(s.chatFatigue, key)
		ok = false
	}
	s.fatigueMu.Unlock()
	if !ok || entry.userID != state.userID || entry.streak < count {
		return false
	}
	pct := s.cfg.MentionFatigueIgnorePct
	if pct <= 0 {
		pct = 65
	}
	if pct > 100 {
		pct = 100
	}
	roll, err := rand.Int(rand.Reader, big.NewInt(100))
	if err != nil || roll.Int64() >= int64(pct) {
		return false
	}
	s.logger.Info(
		"mention fatigue ignore",
		"chat_type", state.chatType,
		"chat_id", state.chatID,
		"user_id", state.userID,
		"streak", entry.streak,
	)
	state.ignored = true
	event.Stop("mention_fatigue_ignored")
	return true
}

// checkRepeatSpam penalizes repeat spam and dispatches the configured
// response. Returns true when the event is fully handled (dropped or
// answered) and the pipeline should stop.
func (s *Service) checkRepeatSpam(
	ctx context.Context,
	event *pipeline.Context,
	state *flowState,
) bool {
	count := s.cfg.SpamRepeatCount
	if count <= 0 || strings.EqualFold(
		strings.TrimSpace(s.cfg.SpamRepeatMode), "off",
	) {
		return false
	}
	key := state.platform + ":" + state.chatType + ":" +
		state.chatID + ":" + state.userID
	repeats := s.noteRepeatSpam(key, state.text, s.cfg.SpamRepeatWindow)
	if repeats < count {
		return false
	}
	// One deduction per streak — a long spam run costs them once, not
	// once per message.
	if s.affinity != nil && s.cfg.SpamRepeatPenalty > 0 &&
		!s.markSpamPenalized(key) {
		s.affinity.Add(state.selfID, state.userID, -s.cfg.SpamRepeatPenalty)
		state.spamPenalty += s.cfg.SpamRepeatPenalty
	}
	mode := pickSpamMode(s.cfg.SpamRepeatMode)
	s.logger.Info(
		"repeat spam penalized",
		"chat_type", state.chatType,
		"chat_id", state.chatID,
		"user_id", state.userID,
		"repeats", repeats,
		"penalty", state.spamPenalty,
		"mode", mode,
	)
	switch mode {
	case "ignore", "off":
		state.ignored = true
		event.Stop("repeat_spam_ignored")
		return true
	case "voice":
		text := strings.TrimSpace(s.cfg.SpamRepeatVoiceText)
		if text == "" {
			text = "别刷了行不行啊，再刷我真不理你了"
		}
		// Goes through the normal voice gates — chats with voice off
		// just get the message dropped.
		s.deliverVoiceClips(ctx, state, []voiceClip{{Text: text}})
		state.ignored = true
		event.Stop("repeat_spam_voice")
		return true
	default:
		// "hint" — continue the pipeline; the prompt builder adds a
		// scold nudge so the persona calls them out in character.
		return false
	}
}
