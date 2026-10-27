package bot

import (
	"context"
	"crypto/rand"
	"math/big"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lianyorker/cinlan-qq-bot/internal/message"
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

// gameInviteWindow is how long an outbound dice/rps move keeps the
// opponent's bare counter-move reachable — long enough to throw back,
// short enough to not stay open for random sticker spam.
const gameInviteWindow = 2 * time.Minute

// noteGameMove records that she just sent a dice/rps segment in this
// chat, so the opponent's bare reply move can skip mention/attention
// gates instead of dying at the door.
func (s *Service) noteGameMove(chatKey string) {
	s.gameMu.Lock()
	defer s.gameMu.Unlock()
	if s.gameInvites == nil {
		s.gameInvites = map[string]time.Time{}
	}
	s.gameInvites[chatKey] = s.now()
}

// pendingGameMove reports whether this event is a bare game move that
// answers her recent one — chain carries an rps/dice segment and an
// invite is still warm in this chat.
func (s *Service) pendingGameMove(state *flowState) bool {
	hasMove := false
	for _, component := range state.event.Chain {
		if component.Type == message.TypeRPS || component.Type == message.TypeDice {
			hasMove = true
			break
		}
	}
	if !hasMove {
		return false
	}
	key := state.platform + ":" + state.chatType + ":" + state.chatID
	s.gameMu.Lock()
	defer s.gameMu.Unlock()
	at, ok := s.gameInvites[key]
	return ok && s.now().Sub(at) < gameInviteWindow
}

// followUpWindow is how long she stays tuned to the last user she
// replied to — their follow-ups don't need another mention to reach
// her, the way a person already in conversation keeps listening.
const followUpWindow = 3 * time.Minute

// pendingFollowUp reports whether this event is a continuation from the
// user she just answered in this group chat. Past the fatigue streak
// the drift-off gate owns that user again, and messages aimed at other
// members never count as her continuation.
func (s *Service) pendingFollowUp(state *flowState) bool {
	if state.chatType != platform.ChatGroup {
		return false
	}
	if mentionsAnotherUser(state.event.Chain, state.selfID) ||
		containsTextualMention(state.text) {
		return false
	}
	key := state.platform + ":" + state.chatType + ":" + state.chatID
	s.fatigueMu.Lock()
	entry, ok := s.chatFatigue[key]
	s.fatigueMu.Unlock()
	if !ok || entry.userID != state.userID {
		return false
	}
	if s.now().Sub(entry.at) >= followUpWindow {
		return false
	}
	if count := s.cfg.MentionFatigueCount; count > 0 && entry.streak >= count {
		return false
	}
	return true
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
	// Whitelisted users are exempt — stonewalling them looks human to
	// everyone else but breaks moderation and testing. Group owner/admin
	// titles alone no longer confer this exemption.
	if state.replyPolicy.AllowsUser(state.userID) {
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
		s.affinity.Add(state.platform, state.selfID, state.userID, -s.cfg.SpamRepeatPenalty)
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

// echoChainEntry tracks a live meme run: several DIFFERENT users echoing
// the same normalized line in one chat inside a short window. Unlike
// spamRepeatEntry — one user spamming — a chain is a group bit she may
// choose to tag along on, mood willing, exactly once per run.
type echoChainEntry struct {
	norm   string
	users  map[string]struct{}
	count  int
	first  time.Time
	joined bool
}

const (
	// echoChainWindow is how close the echoes have to land to feel like
	// one spontaneous run instead of a coincidence.
	echoChainWindow = 30 * time.Second
	// echoChainMinUsers is the crowd size that makes it a bit — one
	// repeat is a coincidence, two plus is a chain.
	echoChainMinUsers = 2
	// echoChainMinRunes keeps stray one-word agreements ("好"/"行"/"6")
	// from registering as memes; real chains echo a phrase.
	echoChainMinRunes = 5
	// echoJoinCooldown stops her from tagging every passing meme — a
	// person who's already chimed in lets the next one go.
	echoJoinCooldown = 2 * time.Minute
)

// noteEchoChain records one echo and returns how many distinct users are
// in the current run. Counter resets when the line changes or the
// window lapses.
func (s *Service) noteEchoChain(key, userID, text string) int {
	norm := normalizeRepeatText(text)
	if norm == "" || utf8.RuneCountInString(norm) < echoChainMinRunes {
		return 0
	}
	s.echoMu.Lock()
	defer s.echoMu.Unlock()
	if s.echoChains == nil {
		s.echoChains = make(map[string]echoChainEntry)
	}
	now := s.now()
	if len(s.echoChains) > 512 {
		for k, entry := range s.echoChains {
			if now.Sub(entry.first) >= echoChainWindow {
				delete(s.echoChains, k)
			}
		}
	}
	entry, ok := s.echoChains[key]
	if !ok || entry.norm != norm || now.Sub(entry.first) >= echoChainWindow {
		entry = echoChainEntry{
			norm:  norm,
			users: make(map[string]struct{}),
			first: now,
		}
	}
	entry.users[userID] = struct{}{}
	entry.count++
	s.echoChains[key] = entry
	return len(entry.users)
}

// rollEchoChance maps her current mood to a participation chance out of
// 100: upbeat she jumps on the bit more often, grumpy mostly lets it
// pass, mid-tier is a coin flip. Mood carries the vibe — no schedule.
func (s *Service) rollEchoChance(selfID, chatType, chatID string) int {
	if !s.cfg.MoodEnabled || s.chatPrefs == nil {
		return 30
	}
	switch mood := s.chatPrefs.MoodScore(
		platform.PlatformQQOneBot, selfID, chatType, chatID, s.now(),
	); {
	case mood >= 40:
		return 45
	case mood >= 0:
		return 25
	case mood > -40:
		return 10
	default:
		return 0
	}
}

// tryEchoJoin decides whether she tags along on the meme chain running
// in this chat. The chance rides her mood; she joins at most once per
// run and then cools off so she doesn't echo every bit. Returns the
// echoed line when she joins ("" = she sits this one out) — the caller
// annotates the prompt so the persona can riff, echo, or lob a line of
// her own rather than parroting verbatim.
func (s *Service) tryEchoJoin(state *flowState) string {
	if state.chatType != platform.ChatGroup {
		return ""
	}
	key := state.platform + ":" + state.chatType + ":" + state.chatID
	if s.noteEchoChain(key, state.userID, state.text) < echoChainMinUsers {
		return ""
	}
	s.echoMu.Lock()
	entry, ok := s.echoChains[key]
	if !ok || entry.joined {
		s.echoMu.Unlock()
		return ""
	}
	if last, has := s.echoJoinAt[key]; has &&
		s.now().Sub(last) < echoJoinCooldown {
		s.echoMu.Unlock()
		return ""
	}
	s.echoMu.Unlock()

	chance := s.rollEchoChance(state.selfID, state.chatType, state.chatID)
	if chance <= 0 {
		return ""
	}
	roll := 0
	if s.echoRoll != nil {
		roll = s.echoRoll()
	} else if r, err := rand.Int(rand.Reader, big.NewInt(100)); err == nil {
		roll = int(r.Int64())
	}
	if roll >= chance {
		return ""
	}
	s.echoMu.Lock()
	if entry, ok := s.echoChains[key]; ok {
		entry.joined = true
		s.echoChains[key] = entry
	}
	if s.echoJoinAt == nil {
		s.echoJoinAt = make(map[string]time.Time)
	}
	s.echoJoinAt[key] = s.now()
	s.echoMu.Unlock()
	s.logger.Info(
		"echo chain join",
		"chat_type", state.chatType,
		"chat_id", state.chatID,
		"echoed", normalizeRepeatText(state.text),
	)
	return state.text
}
