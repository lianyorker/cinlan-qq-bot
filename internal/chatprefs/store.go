// Package chatprefs keeps per-chat operator preferences: a notification
// switch for proactive replies and a list of admin "remember" notes that
// get injected into the model's system prompt.
package chatprefs

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/vecstore"
)

const maxNotesPerChat = 50
const maxNoteRunes = 2000

// profileLinkPattern strips URLs from cross-chat identity records.
// A persona profile follows the user everywhere — a link learned in
// group A must never resurface as an ad in group B.
var profileLinkPattern = regexp.MustCompile(
	`(?i)(?:https?://|www\.)[a-z0-9._~:/?#@!$&'()*+,;=%-]+|` +
		`(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,}` +
		`(?:/[a-z0-9._~:/?#@!$&'()*+,;=%-]*)?`,
)

type imageNote struct {
	Key  string `json:"key"`
	Path string `json:"path"`
}

// fact is one remembered line with time anchors. CreatedAt marks when
// the persona first learned it; LastSeenAt bumps every time the line
// resurfaces in a prompt — stale, never-recalled memories fade first.
// Weight is memory heat: facts that keep getting recalled score harder,
// so the lines she actually uses stay reachable. 0 means default 1.
// Mood is the emotional color a memory carries — the chat mood when it
// was formed (-100..100). A thing learned mid-fight recalls sour; a
// warm moment recalls fondly. Re-experiencing it in a new mood drifts
// the tag toward the present, the way real memory reconsolidates.
type fact struct {
	Text       string    `json:"text"`
	CreatedAt  time.Time `json:"created_at,omitempty"`
	LastSeenAt time.Time `json:"last_seen_at,omitempty"`
	Weight     float64   `json:"weight,omitempty"`
	Mood       int       `json:"mood,omitempty"`
	// UserID/UserName tag who the memory is about in group chats —
	// empty for self notes and mixed-source distillations. Snapshotted
	// at write time so a rename doesn't rewrite history.
	UserID   string `json:"user_id,omitempty"`
	UserName string `json:"user_name,omitempty"`
	// Context binds the memory to the scene it was formed in — what
	// they were talking about at the time, so recall can say "当时
	// 你们在聊火锅" instead of the bare fact.
	Context string `json:"context,omitempty"`
	// Self marks autobiographical facts — "我那天挺难过" instead of
	// "他那天骂我". Recall renders it as something she lived through,
	// not something she observed about someone else.
	Self bool `json:"self,omitempty"`
	// TriggerAt is only set on Reminders entries — the moment this
	// prospective memory should surface on its own.
	TriggerAt time.Time `json:"trigger_at,omitempty"`
}

// factWeight normalizes the heat multiplier; stored zero / corrupt
// values fall back to neutral.
func factWeight(f fact) float64 {
	if f.Weight <= 0 || f.Weight > maxFactWeight {
		if f.Weight > maxFactWeight {
			return maxFactWeight
		}
		return 1
	}
	return f.Weight
}

// memoryWorthy gates writes: filler with no information content
// ("嗯", "好的", bare punctuation) never becomes a fact.
func memoryWorthy(text string) bool {
	t := strings.TrimSpace(text)
	runes := []rune(t)
	if len(runes) < minFactRunes {
		return false
	}
	for _, w := range fillerBlacklist {
		if t == w {
			return false
		}
	}
	return true
}

// factMarkerPattern strips the bot's own output markers ([[AFFINITY:+2]],
// [[NEXT]], [[VOICE]]…) that leak into a remembered line. They are send-
// time directives, never part of the memory, and a stored marker later
// trips the prompt-injection guard on recall.
var factMarkerPattern = regexp.MustCompile(`\s*\[\[[^\[\]]{0,60}\]\]\s*`)

// cleanFactText removes leftover [[...]] markers while preserving the
// rest of the line (including a leading 承诺·/日记· prefix).
func cleanFactText(text string) string {
	return strings.TrimSpace(factMarkerPattern.ReplaceAllString(text, " "))
}

var fillerBlacklist = []string{
	"嗯", "哦", "啊", "好的", "好的好的", "收到", "知道了", "可以", "行",
	"okk", "ok", "yes", "嗯嗯", "哈哈", "2333", "666", "在的", "在", "在吗",
}

func (f *fact) UnmarshalJSON(raw []byte) error {
	if len(raw) > 0 && raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return err
		}
		*f = fact{Text: s}
		return nil
	}
	type alias fact
	return json.Unmarshal(raw, (*alias)(f))
}

type chatEntry struct {
	NotifyOff bool `json:"notify_off,omitempty"`
	VoiceOff  bool `json:"voice_off,omitempty"`
	// SingOff gates [[SING]] clips — enabled by default like voice;
	// the model decides when singing fits the moment.
	SingOff    bool        `json:"sing_off,omitempty"`
	Persona    string      `json:"persona,omitempty"`
	Notes      []string    `json:"notes,omitempty"`
	ImageNotes []imageNote `json:"image_notes,omitempty"`
	Mood       int         `json:"mood,omitempty"`
	MoodAt     time.Time   `json:"mood_at,omitempty"`
	MuteUntil  time.Time   `json:"mute_until,omitempty"`
	// Facts are self-learned chat facts written by the remember tool —
	// trusted less than operator Notes.
	Facts []fact `json:"facts,omitempty"`
	// Archive holds older facts rotated out of the active 40-slot Facts
	// ring so long-term semantic/vector recall can still resurface them
	// months later instead of permanently forgetting.
	Archive []fact `json:"archive,omitempty"`
	// Impressions maps a QQ user id to the persona's remembered take on
	// that member ("爱抬杠","生日10.28"), updated by the remember tool.
	Impressions map[string]fact `json:"impressions,omitempty"`
	// ToneBans are phrases the chat told her to stop saying
	// ("别老说emo" → "emo" lands here). Outbound replies get scrubbed
	// before send — negative feedback that actually sticks.
	ToneBans []string `json:"tone_bans,omitempty"`
	// Reminders is the prospective-memory shelf: facts with a
	// TriggerAt the reminder loop fires when due ("周三他考试" →
	// Wednesday morning she remembers on her own). Separate from
	// Facts so a pending reminder never leaks into ordinary recall
	// before its time.
	Reminders []fact `json:"reminders,omitempty"`
}

// persistedStore wraps the chat map with persona-scoped user profiles.
// Profiles let the same persona keep one impression per member across
// bindings that share her — scoped by persona name, never cross-domain.
type persistedStore struct {
	Chats    map[string]*chatEntry `json:"chats"`
	Profiles map[string]fact       `json:"profiles,omitempty"` // key = persona+"|"+userID
	Cards    map[string]UserCard   `json:"cards,omitempty"`    // key = userID
}

// GroupBadge is what a member is inside one group — public group data,
// keyed by groupID so badges never bleed across groups.
type GroupBadge struct {
	Role  string `json:"role,omitempty"`  // owner / admin / member
	Title string `json:"title,omitempty"` // 专属头衔
}

// UserCard caches the QQ profile the bot fetched for one person — the
// stuff a human glances at: nick, sex, age, signature, whether they're
// already a friend, plus per-group role/title. Public profile data, so
// a single global cache per userID is correct (not conversation content).
type UserCard struct {
	Nick string `json:"nick,omitempty"`
	Sex  string `json:"sex,omitempty"` // male / female / unknown
	Age  int    `json:"age,omitempty"`
	Sign string `json:"sign,omitempty"`
	// Region is the QQ profile's self-declared location (地区), e.g.
	// "浙江 杭州" — she knows where he lives without ever asking.
	Region    string                `json:"region,omitempty"`
	Friend    bool                  `json:"friend,omitempty"`
	Badges    map[string]GroupBadge `json:"badges,omitempty"`
	FetchedAt time.Time             `json:"fetched_at"`
}

// Store is a small JSON-file-backed per-chat preference store keyed by
// "<platform>:<chat_type>:<chat_id>" so the same chat id on two
// platforms never shares memory.
type Store struct {
	path     string
	mu       sync.RWMutex
	data     map[string]*chatEntry
	profiles map[string]fact
	cards    map[string]UserCard
	now      func() time.Time
	vec      *vecstore.Engine

	// Debounced persistence: saveLocked only marks the store dirty and
	// nudges saveCh; a background loop coalesces bursts and flushLocked
	// does the real disk write. Close/Flush force the pending write out.
	dirty     bool
	saveCh    chan struct{}
	closeCh   chan struct{}
	closeOnce sync.Once
	flushWG   sync.WaitGroup
}

// SetVecEngine attaches an optional dual-mode vector engine for semantic recall.
func (s *Store) SetVecEngine(v *vecstore.Engine) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.vec = v
}

// VecEngine exposes the attached engine read-only — callers outside
// the package (history-chunk scoring) need it for the hybrid ranker.
func (s *Store) VecEngine() *vecstore.Engine {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.vec
}

func Load(path, legacyPlatform string) (*Store, error) {
	store := &Store{
		path:     path,
		data:     map[string]*chatEntry{},
		profiles: map[string]fact{},
		cards:    map[string]UserCard{},
		now:      time.Now,
		saveCh:   make(chan struct{}, 1),
		closeCh:  make(chan struct{}),
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return store, nil
		}
		return nil, err
	}
	// v2 layout wraps chats under "chats"; v1 files are the bare chat map.
	var wrapped map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return nil, err
	}
	if _, ok := wrapped["chats"]; ok {
		var p persistedStore
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		store.data = p.Chats
		store.profiles = p.Profiles
		store.cards = p.Cards
	} else if err := json.Unmarshal(raw, &store.data); err != nil {
		return nil, err
	}
	if store.data == nil {
		store.data = map[string]*chatEntry{}
	}
	if store.profiles == nil {
		store.profiles = map[string]fact{}
	}
	if store.cards == nil {
		store.cards = map[string]UserCard{}
	}
	if store.migrateLegacy(legacyPlatform) {
		// Rewrite the file in the new layout right away — otherwise the
		// migration only lives in memory until the next coincidental save.
		store.dirty = true
		if saveErr := store.flushLocked(); saveErr != nil {
			return store, saveErr
		}
	}
	if path != "" {
		store.flushWG.Add(1)
		go store.flushLoop()
	}
	return store, nil
}

// flushLoop coalesces bursts of writes: every saveLocked drops one
// signal into saveCh, and a short debounce window lets a burst of
// mood/fact updates land in a single disk rewrite instead of one
// full-file marshal per message.
func (s *Store) flushLoop() {
	defer s.flushWG.Done()
	for {
		select {
		case <-s.saveCh:
			timer := time.NewTimer(2 * time.Second)
			select {
			case <-timer.C:
			case <-s.closeCh:
				timer.Stop()
				s.mu.Lock()
				if s.dirty {
					_ = s.flushLocked()
				}
				s.mu.Unlock()
				return
			}
			s.mu.Lock()
			if s.dirty {
				_ = s.flushLocked()
			}
			s.mu.Unlock()
		case <-s.closeCh:
			s.mu.Lock()
			if s.dirty {
				_ = s.flushLocked()
			}
			s.mu.Unlock()
			return
		}
	}
}

// Flush forces any pending debounced write to disk immediately.
func (s *Store) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dirty {
		return nil
	}
	return s.flushLocked()
}

// Close stops the background flusher and writes out any pending state —
// call it on shutdown so the debounce window never swallows the last
// seconds of mood/fact updates on a normal exit.
func (s *Store) Close() error {
	var err error
	s.closeOnce.Do(func() {
		close(s.closeCh)
		s.flushWG.Wait()
		s.mu.Lock()
		if s.dirty {
			err = s.flushLocked()
		}
		s.mu.Unlock()
	})
	return err
}

// migrateLegacy rewrites keys saved before the platform scope existed
// ("group:123", "persona|user", bare card ids) onto the legacy platform
// so an upgrade keeps old memories instead of orphaning them. Scoped
// keys already present always win — a collision drops the legacy copy
// rather than overwriting newer platform-scoped data.
func (s *Store) migrateLegacy(legacyPlatform string) (migrated bool) {
	lp := strings.TrimSpace(legacyPlatform)
	if lp == "" {
		return false
	}
	for k, e := range s.data {
		if strings.Count(k, ":") != 1 {
			continue
		}
		dst := lp + ":" + k
		if _, clash := s.data[dst]; !clash {
			s.data[dst] = e
		}
		delete(s.data, k)
		migrated = true
	}
	for k, f := range s.profiles {
		i := strings.IndexByte(k, '|')
		if i < 0 {
			continue
		}
		if user := k[i+1:]; !strings.Contains(user, ":") {
			dst := k[:i+1] + lp + ":" + user
			if _, clash := s.profiles[dst]; !clash {
				s.profiles[dst] = f
			}
			delete(s.profiles, k)
			migrated = true
		}
	}
	for k, c := range s.cards {
		if strings.Contains(k, ":") {
			continue
		}
		dst := lp + ":" + k
		if _, clash := s.cards[dst]; !clash {
			s.cards[dst] = c
		}
		delete(s.cards, k)
		migrated = true
	}
	return migrated
}

// SetClock overrides the time source (tests); production keeps time.Now.
func (s *Store) SetClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
}

func key(platform, selfID, chatType, chatID string) string {
	return strings.TrimSpace(platform) + ":" +
		strings.TrimSpace(selfID) + ":" +
		strings.TrimSpace(chatType) + ":" + strings.TrimSpace(chatID)
}

// legacyKey is the pre-selfID 3-part shape kept as a read/write
// fallback so memories stored before account scoping are still seen.
func legacyKey(platform, chatType, chatID string) string {
	return strings.TrimSpace(platform) + ":" +
		strings.TrimSpace(chatType) + ":" + strings.TrimSpace(chatID)
}

// lookup is the read-only variant: scoped key first, then the legacy
// shared entry — nil when neither exists. No creation, no claim.
func (s *Store) lookup(platform, selfID, chatType, chatID string) *chatEntry {
	if e := s.data[key(platform, selfID, chatType, chatID)]; e != nil {
		return e
	}
	return s.data[legacyKey(platform, chatType, chatID)]
}

// userKey scopes a bare user id to its platform — a douyin uid and a QQ
// number are never the same person.
func userKey(platform, userID string) string {
	return strings.TrimSpace(platform) + ":" + strings.TrimSpace(userID)
}

// profileKey keeps the persona prefix so per-persona caps still count,
// and scopes the trailing user id to its platform.
func profileKey(platform, persona, userID string) string {
	return strings.TrimSpace(persona) + "|" + userKey(platform, userID)
}

// sanitizeProfileText strips URLs from profile content. Profiles are
// cross-chat identity notes — "这人是卖挂的" is a fact she can carry,
// but the storefront link must not travel with it into other chats.
func sanitizeProfileText(text string) string {
	text = profileLinkPattern.ReplaceAllString(text, "")
	return strings.Join(strings.Fields(text), " ")
}

func splitKey(k string) (platform, selfID, chatType, chatID string) {
	parts := strings.SplitN(k, ":", 4)
	switch len(parts) {
	case 4:
		return parts[0], parts[1], parts[2], parts[3]
	case 3:
		// legacy platform:chatType:chatID — account-unscoped.
		return parts[0], "", parts[1], parts[2]
	case 2:
		// oldest legacy: chatType:chatID with no platform.
		return "", "", parts[0], parts[1]
	case 1:
		return "", "", k, ""
	}
	return "", "", "", ""
}

func (s *Store) entry(platform, selfID, chatType, chatID string) *chatEntry {
	sk := key(platform, selfID, chatType, chatID)
	if e := s.data[sk]; e != nil {
		return e
	}
	// Claim-on-touch: a legacy shared entry becomes this account's own
	// the first time that account touches the chat — first claim wins.
	if e := s.data[legacyKey(platform, chatType, chatID)]; e != nil && selfID != "" {
		delete(s.data, legacyKey(platform, chatType, chatID))
		s.data[sk] = e
		return e
	}
	e := &chatEntry{}
	s.data[sk] = e
	return e
}

// SetNotifyEnabled toggles proactive replies for the chat. Enabled is the
// default; when disabled the bot only answers explicit mentions.
func (s *Store) SetNotifyEnabled(platform, selfID, chatType, chatID string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entry(platform, selfID, chatType, chatID).NotifyOff = !enabled
	return s.saveLocked()
}

func (s *Store) NotifyEnabled(platform, selfID, chatType, chatID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e := s.lookup(platform, selfID, chatType, chatID)
	return e == nil || !e.NotifyOff
}

// SetVoiceEnabled toggles [[VOICE]] replies for the chat. Enabled is the
// default; disabled strips the model's voice markers into silence.
func (s *Store) SetVoiceEnabled(platform, selfID, chatType, chatID string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entry(platform, selfID, chatType, chatID).VoiceOff = !enabled
	return s.saveLocked()
}

// VoiceEnabled reports whether voice clips are allowed for the chat.
func (s *Store) VoiceEnabled(platform, selfID, chatType, chatID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e := s.lookup(platform, selfID, chatType, chatID)
	return e == nil || !e.VoiceOff
}

// SetSingEnabled toggles [[SING]] clips for the chat. Enabled is the
// default; the flag remains as a dormant kill switch.
func (s *Store) SetSingEnabled(platform, selfID, chatType, chatID string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entry(platform, selfID, chatType, chatID).SingOff = !enabled
	return s.saveLocked()
}

// SingEnabled reports whether [[SING]] clips are allowed for the chat.
func (s *Store) SingEnabled(platform, selfID, chatType, chatID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e := s.lookup(platform, selfID, chatType, chatID)
	return e == nil || !e.SingOff
}

// SetPersona pins the active persona for the chat; empty restores the
// binding default.
func (s *Store) SetPersona(platform, selfID, chatType, chatID, persona string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entry(platform, selfID, chatType, chatID).Persona = strings.TrimSpace(persona)
	return s.saveLocked()
}

// PersonaName returns the chat's pinned persona, or "" for the default.
func (s *Store) PersonaName(platform, selfID, chatType, chatID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e := s.lookup(platform, selfID, chatType, chatID)
	if e == nil {
		return ""
	}
	return e.Persona
}

func (s *Store) AddNote(platform, selfID, chatType, chatID, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return errors.New("note is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entry(platform, selfID, chatType, chatID)
	if len(e.Notes) >= maxNotesPerChat {
		return errors.New("note limit reached")
	}
	e.Notes = append(e.Notes, text)
	return s.saveLocked()
}

// AddImageNote remembers an image reference under a name so keyword image
// generation can reuse it when the prompt mentions that name.
func (s *Store) AddImageNote(platform, selfID, chatType, chatID, name, path string) error {
	name = strings.TrimSpace(name)
	path = strings.TrimSpace(path)
	if name == "" || path == "" {
		return errors.New("image note is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entry(platform, selfID, chatType, chatID)
	// Replace an existing note under the same key.
	for i, n := range e.ImageNotes {
		if n.Key == name {
			e.ImageNotes[i].Path = path
			return s.saveLocked()
		}
	}
	e.ImageNotes = append(e.ImageNotes, imageNote{Key: name, Path: path})
	return s.saveLocked()
}

// LatestImage returns the most recently stored image note path for a chat.
func (s *Store) LatestImage(platform, selfID, chatType, chatID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e := s.lookup(platform, selfID, chatType, chatID)
	if e == nil || len(e.ImageNotes) == 0 {
		return ""
	}
	return e.ImageNotes[len(e.ImageNotes)-1].Path
}

// ImageForPrompt returns the stored image path whose key appears in prompt.
func (s *Store) ImageForPrompt(platform, selfID, chatType, chatID, prompt string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e := s.lookup(platform, selfID, chatType, chatID)
	if e == nil {
		return ""
	}
	for _, n := range e.ImageNotes {
		if n.Key != "" && strings.Contains(prompt, n.Key) {
			return n.Path
		}
	}
	return ""
}

const (
	maxFactsPerChat = 50
	// Archive is the only real deletion point in the whole memory
	// chain — 400 filled in months on a long-lived chat, so capacity
	// was raised to 2000 (sqlite cost is negligible).
	maxArchivePerChat     = 2000
	maxImpressionsPerChat = 60
	maxProfilesPerPersona = 120
	maxToneBansPerChat    = 20
	// minFactRunes / maxFactWeight bound the memory quality gate and the
	// recall-heat multiplier.
	minFactRunes  = 4
	maxFactWeight = 3.0
	// recentFactsPerPrompt keeps the freshest memories always visible;
	// the rest only resurface when the current message brings them up.
	recentFactsPerPrompt = 10
	recallFactsPerPrompt = 5
	// recallMinScore is the CJK-bigram overlap a fact needs before the
	// bot "remembers" it mid-turn. 2 keeps real cues ("阿茶"+"咖啡")
	// reachable while "今天天气" style chit-chat still misses.
	recallMinScore = 2
)

// factAnchor returns the freshest timestamp a fact earned, for sorting
// and pruning — a memory keeps its youth by being recalled.
func factAnchor(f fact) time.Time {
	if f.LastSeenAt.After(f.CreatedAt) {
		return f.LastSeenAt
	}
	return f.CreatedAt
}

// textBigrams extracts CJK character bigrams plus ASCII word tokens,
// the cheapest relevance signal for memory recall.
func textBigrams(text string) map[string]bool {
	text = strings.ToLower(text)
	grams := map[string]bool{}
	runes := []rune(text)
	isCJK := func(r rune) bool {
		return r >= 0x3000 && r <= 0x9fff
	}
	var word strings.Builder
	flushWord := func() {
		if w := word.String(); len(w) >= 2 {
			grams["w:"+w] = true
		}
		word.Reset()
	}
	prevCJK := -1
	for i, r := range runes {
		if isCJK(r) {
			flushWord()
			if prevCJK >= 0 {
				grams[string(runes[prevCJK:i+1])] = true
			}
			prevCJK = i
			continue
		}
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' {
			word.WriteRune(r)
			continue
		}
		flushWord()
		prevCJK = -1
	}
	flushWord()
	return grams
}

// scoreFact counts how much of a fact's vocabulary shows up in the
// current message — the recall trigger.
func scoreFact(f fact, query map[string]bool) int {
	overlap := 0
	for gram := range textBigrams(f.Text) {
		if query[gram] {
			overlap++
		}
	}
	return overlap
}

// bestFactMatch finds the fact most similar to target (for replace /
// forget); returns the index or -1 when nothing matches at all.
// Destructive edits demand more than the recall floor — at least half
// the fact's vocabulary must overlap, or the target must literally
// contain it. Otherwise vague targets would erase the wrong memory.
func bestFactMatch(facts []fact, target string) int {
	query := textBigrams(target)
	best, bestScore := -1, 0
	for i, f := range facts {
		if strings.Contains(f.Text, target) || strings.Contains(target, f.Text) {
			return i
		}
		if sc := scoreFact(f, query); sc > bestScore {
			best, bestScore = i, sc
		}
	}
	if best < 0 {
		return -1
	}
	need := len(textBigrams(facts[best].Text))/2 + 1
	if need < recallMinScore {
		need = recallMinScore
	}
	if bestScore < need {
		return -1
	}
	return best
}

// HybridTextScore applies the same three-arm scoring (CJK bigram +
// concept clusters + cached dense similarity) to a raw text — used by
// the session history index so old conversation chunks rank exactly
// like remembered facts. Weight does not apply (raw history has none).
func HybridTextScore(text, query string, vec *vecstore.Engine) int {
	score := 0
	grams := textBigrams(query)
	for g := range textBigrams(text) {
		if grams[g] {
			score++
		}
	}
	if concepts := vecstore.ConceptOverlap(text, query); concepts > 0 {
		score += concepts * 2
	}
	if vec != nil && vec.RemoteEnabled() {
		if sim := vec.CachedSimilarity(text, query); sim >= 0.45 {
			score += int(sim * 6)
		}
	}
	return score
}

// scoreFactHybrid combines CJK bigram overlap with local semantic concept
// clusters and optional dense vector similarity so cross-word cues like
// "天天熬夜写代码" <-> "睡眠不足想休息" still trigger memory recall.
// The fact's Weight (memory heat) scales the final score — frequently
// recalled memories resurface easier.
func scoreFactHybrid(f fact, query string, grams map[string]bool, vec *vecstore.Engine) int {
	score := scoreFact(f, grams)
	if concepts := vecstore.ConceptOverlap(f.Text, query); concepts > 0 {
		score += concepts * 2
	}
	// Dense similarity reads ONLY the disk-backed cache — never the
	// network — so the recall hot path stays zero-IO even when a remote
	// embedding endpoint is configured. Cold-cache facts simply score
	// on bigram + concept alone until a background warm-up fills them.
	if vec != nil && vec.RemoteEnabled() {
		if sim := vec.CachedSimilarity(f.Text, query); sim >= 0.45 {
			score += int(sim * 6)
		}
	}
	return int(float64(score) * factWeight(f))
}

// legacyFactPrefix marks memories that faded into a long-ago trace —
// the model reads the prefix and naturally treats it as a distant
// recollection rather than a recent fact.
const legacyFactPrefix = "很久以前:"

func archiveFactLocked(e *chatEntry, evicted fact) {
	if strings.TrimSpace(evicted.Text) == "" {
		return
	}
	for i, a := range e.Archive {
		if a.Text == evicted.Text {
			e.Archive[i] = evicted
			return
		}
	}
	if len(e.Archive) >= maxArchivePerChat {
		fadeOldestArchive(e)
	}
	e.Archive = append(e.Archive, evicted)
}

// fadeOldestArchive folds the stalest memories into one "long ago" line
// instead of deleting them — old memories degrade to a vague trace like
// a person's, never hard-truncated. The folded line inherits the oldest
// anchor so it stays eligible for further fading on the next overflow,
// and keeps a slightly lower weight so it surfaces weaker.
func fadeOldestArchive(e *chatEntry) {
	const foldCount = 10
	if len(e.Archive) == 0 {
		return
	}
	n := foldCount
	if len(e.Archive) < n {
		n = len(e.Archive)
	}
	idxs := make([]int, len(e.Archive))
	for i := range idxs {
		idxs[i] = i
	}
	sort.Slice(idxs, func(a, b int) bool {
		return factAnchor(e.Archive[idxs[a]]).Before(factAnchor(e.Archive[idxs[b]]))
	})
	victims := idxs[:n]
	oldestAnchor := factAnchor(e.Archive[victims[0]])
	parts := make([]string, 0, n)
	for _, vi := range victims {
		t := strings.TrimSpace(strings.TrimPrefix(e.Archive[vi].Text, legacyFactPrefix))
		runes := []rune(t)
		if len(runes) > 40 {
			runes = runes[:40]
		}
		if len(runes) > 0 {
			parts = append(parts, string(runes))
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(victims)))
	for _, vi := range victims {
		e.Archive = append(e.Archive[:vi], e.Archive[vi+1:]...)
	}
	if len(parts) > 0 {
		e.Archive = append(e.Archive, fact{
			Text:       legacyFactPrefix + strings.Join(parts, ";"),
			CreatedAt:  oldestAnchor,
			LastSeenAt: oldestAnchor,
			Weight:     0.8,
		})
	}
}

// factOpts carries the optional tags a memory can be stamped with at
// write time — speaker attribution, the scene it formed in, and the
// autobiographical flag. All empty means an anonymous plain fact.
type factOpts struct {
	UserID   string
	UserName string
	Context  string
	Self     bool
}

// AddFact stores a self-learned fact line for the chat. Duplicates are
// ignored; over capacity the least-recently-recalled memory moves into
// the long-term Archive instead of being lost. Zero-information filler
// ("嗯", "好的") is rejected so the memory pool stays meaningful.
func (s *Store) AddFact(platform, selfID, chatType, chatID, text string) error {
	return s.addFactWithOpts(platform, selfID, chatType, chatID, text, factOpts{})
}

// AddFactForUser is AddFact plus who the memory is about — in group
// chats recall surfaces "阿伟说的" instead of blending every member
// into one anonymous pool. Private chats need no tag; pass empty.
func (s *Store) AddFactForUser(platform, selfID, chatType, chatID, userID, userName, text string) error {
	return s.addFactWithOpts(platform, selfID, chatType, chatID, text, factOpts{
		UserID: userID, UserName: userName,
	})
}

// AddFactWithContext is AddFactForUser plus the scene the memory formed
// in — "当时在聊火锅" — so recall can re-attach the moment, not just
// the bare fact. Pass empty context to behave like AddFactForUser.
func (s *Store) AddFactWithContext(platform, selfID, chatType, chatID, userID, userName, context, text string) error {
	return s.addFactWithOpts(platform, selfID, chatType, chatID, text, factOpts{
		UserID: userID, UserName: userName, Context: context,
	})
}

// AddSelfFact stores an autobiographical memory — something that
// happened to HER ("我那天挺难过"), not something she observed about
// someone else. Recall renders it in first person.
func (s *Store) AddSelfFact(platform, selfID, chatType, chatID, text string) error {
	return s.addFactWithOpts(platform, selfID, chatType, chatID, text, factOpts{
		Self: true,
	})
}

// addFactWithOpts is the shared write path: clean, quality-gate, then
// append under lock with the given tags and warm the dense arm.
func (s *Store) addFactWithOpts(platform, selfID, chatType, chatID, text string, o factOpts) error {
	text = cleanFactText(text)
	if text == "" {
		return errors.New("fact is empty")
	}
	if !memoryWorthy(text) {
		return errors.New("nothing worth remembering")
	}
	o.Context = strings.TrimSpace(o.Context)
	if runes := []rune(o.Context); len(runes) > 30 {
		o.Context = string(runes[:30])
	}
	s.mu.Lock()
	err := s.addFactLocked(s.entry(platform, selfID, chatType, chatID), text, o, s.now())
	vec := s.vec
	s.mu.Unlock()
	// Warm the dense arm for the fresh memory — facts evicted into the
	// Archive were already warmed here when they entered, so long-term
	// recall keeps its remote cosine instead of falling back forever.
	if err == nil && vec != nil {
		vec.WarmAsync(text)
	}
	return err
}

// addFactLocked does the append/dedupe/prune under the held lock so
// ReplaceFact can fall back without re-locking.
func (s *Store) addFactLocked(e *chatEntry, text string, o factOpts, now time.Time) error {
	currentMood := decayedMood(e, now)
	for i, f := range e.Facts {
		if f.Text == text {
			e.Facts[i].LastSeenAt = now
			// Reconsolidation: re-living a memory in a new mood nudges
			// its emotional tag toward how she feels now (2:1 toward the
			// old color so one odd day doesn't repaint it).
			e.Facts[i].Mood = (f.Mood*2 + currentMood) / 3
			return s.saveLocked()
		}
	}
	if len(e.Facts) >= maxFactsPerChat {
		oldest := 0
		for i := range e.Facts {
			if factAnchor(e.Facts[i]).Before(factAnchor(e.Facts[oldest])) {
				oldest = i
			}
		}
		archiveFactLocked(e, e.Facts[oldest])
		e.Facts = append(e.Facts[:oldest], e.Facts[oldest+1:]...)
	}
	// Emotional intensity boost: memories formed during strong feelings
	// (fight, big joy) start with higher recall heat — like the flash-bulb
	// effect in real memory where surprises stick harder.
	var initWeight float64
	if currentMood >= 30 || currentMood <= -30 {
		initWeight = 1.5
	}
	e.Facts = append(e.Facts, fact{
		Text: text, CreatedAt: now, LastSeenAt: now, Mood: currentMood,
		Weight: initWeight,
		UserID: o.UserID, UserName: o.UserName,
		Context: o.Context, Self: o.Self,
	})
	return s.saveLocked()
}

// AddToneBan records a phrase this chat told her to stop saying.
// Deduped, capped — new complaints bump old ones out.
func (s *Store) AddToneBan(platform, selfID, chatType, chatID, phrase string) {
	phrase = strings.TrimSpace(phrase)
	if phrase == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entry(platform, selfID, chatType, chatID)
	for _, b := range e.ToneBans {
		if b == phrase {
			return
		}
	}
	if len(e.ToneBans) >= maxToneBansPerChat {
		e.ToneBans = e.ToneBans[1:]
	}
	e.ToneBans = append(e.ToneBans, phrase)
	_ = s.saveLocked()
}

// ToneBans returns the phrases this chat asked her to drop.
func (s *Store) ToneBans(platform, selfID, chatType, chatID string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e := s.lookup(platform, selfID, chatType, chatID)
	if e == nil || len(e.ToneBans) == 0 {
		return nil
	}
	out := make([]string, len(e.ToneBans))
	copy(out, e.ToneBans)
	return out
}

// RemoveToneBan lifts a ban — "可以说X了" gives the phrase back.
// Matches exact or substring: ban "emo" lifts for "说emo了".
func (s *Store) RemoveToneBan(platform, selfID, chatType, chatID, phrase string) {
	phrase = strings.TrimSpace(phrase)
	if phrase == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entry(platform, selfID, chatType, chatID)
	kept := e.ToneBans[:0]
	for _, b := range e.ToneBans {
		if b == phrase || strings.Contains(b, phrase) || strings.Contains(phrase, b) {
			continue
		}
		kept = append(kept, b)
	}
	if len(kept) != len(e.ToneBans) {
		e.ToneBans = kept
		_ = s.saveLocked()
	}
}

// ClosePromise drops the closest open 承诺· fact when her reply shows
// she just did the thing ("提醒你了" / "帮你查好了"). Needs a real
// overlap — the fulfillment cue alone never closes anything.
func (s *Store) ClosePromise(platform, selfID, chatType, chatID, text string) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entry(platform, selfID, chatType, chatID)
	if e == nil {
		return false
	}
	grams := textBigrams(text)
	closest, best := -1, 0
	for i, f := range e.Facts {
		if !strings.HasPrefix(f.Text, promisePrefix) {
			continue
		}
		overlap := 0
		for g := range textBigrams(f.Text) {
			if grams[g] {
				overlap++
			}
		}
		if overlap > best {
			best, closest = overlap, i
		}
	}
	if closest < 0 || best < 2 {
		return false
	}
	e.Facts = append(e.Facts[:closest], e.Facts[closest+1:]...)
	_ = s.saveLocked()
	return true
}

// PromiseFacts returns the "承诺·…" lines she still owes this chat —
// things she said she'd do. Surfaced to proactive and prompts so a
// promise doesn't die the moment it was spoken.
func (s *Store) PromiseFacts(platform, selfID, chatType, chatID string) []fact {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e := s.lookup(platform, selfID, chatType, chatID)
	if e == nil {
		return nil
	}
	var out []fact
	collect := func(list []fact) {
		for _, f := range list {
			if strings.HasPrefix(f.Text, promisePrefix) {
				out = append(out, f)
			}
		}
	}
	collect(e.Facts)
	collect(e.Archive)
	return out
}

// OwedFacts returns the "他答应·…" lines — things THEY said they'd do
// for her. A person remembers being promised something; the pair with
// PromiseFacts keeps both sides of the ledger.
func (s *Store) OwedFacts(platform, selfID, chatType, chatID string) []fact {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e := s.lookup(platform, selfID, chatType, chatID)
	if e == nil {
		return nil
	}
	var out []fact
	collect := func(list []fact) {
		for _, f := range list {
			if strings.HasPrefix(f.Text, owedPromisePrefix) {
				out = append(out, f)
			}
		}
	}
	collect(e.Facts)
	collect(e.Archive)
	return out
}

// HasThoughtMaterial reports whether this chat holds anything worth
// spontaneously bringing up — open promises or memories still warm.
// Permanent fields (ToneBans, Impressions) deliberately do NOT count:
// they never expire, so counting them would keep the gate open forever
// and the 念头 check would silently never dampen anything.
func (s *Store) HasThoughtMaterial(platform, selfID, chatType, chatID string, now time.Time) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e := s.lookup(platform, selfID, chatType, chatID)
	if e == nil {
		return false
	}
	recent := now.Add(-14 * 24 * time.Hour)
	for _, f := range e.Facts {
		if strings.HasPrefix(f.Text, promisePrefix) ||
			strings.HasPrefix(f.Text, owedPromisePrefix) ||
			factAnchor(f).After(recent) {
			return true
		}
	}
	return false
}

// promisePrefix marks facts that record something she offered to do.
const promisePrefix = "承诺·"

// owedPromisePrefix marks facts that record something THEY offered to
// do for her — the other side of the promise ledger.
const owedPromisePrefix = "他答应·"

// ReplaceFact updates the closest existing fact with new text — the
// "she corrected herself" path; falls back to AddFact when no match.
func (s *Store) ReplaceFact(platform, selfID, chatType, chatID, target, text string) error {
	text = strings.TrimSpace(text)
	target = strings.TrimSpace(target)
	if text == "" || target == "" {
		return errors.New("replace needs old and new text")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entry(platform, selfID, chatType, chatID)
	now := s.now()
	if idx := bestFactMatch(e.Facts, target); idx >= 0 {
		e.Facts[idx].Text = text
		e.Facts[idx].LastSeenAt = now
		return s.saveLocked()
	}
	return s.addFactLocked(e, text, factOpts{}, now)
}

// ForgetFact drops the closest matching fact — deliberate forgetting.
func (s *Store) ForgetFact(platform, selfID, chatType, chatID, target string) error {
	target = strings.TrimSpace(target)
	if target == "" {
		return errors.New("nothing to forget")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entry(platform, selfID, chatType, chatID)
	if e == nil {
		return errors.New("没有相关的记忆")
	}
	if idx := bestFactMatch(e.Facts, target); idx >= 0 {
		e.Facts = append(e.Facts[:idx], e.Facts[idx+1:]...)
		return s.saveLocked()
	}
	// Deliberate forgetting reaches the Archive too — a memory the user
	// asked her to drop should not resurface from the long-term pool.
	if idx := bestFactMatch(e.Archive, target); idx >= 0 {
		e.Archive = append(e.Archive[:idx], e.Archive[idx+1:]...)
		return s.saveLocked()
	}
	return errors.New("没找到要忘的内容")
}

// WarmFacts pre-warms remote embeddings for the chat's freshest facts —
// called at compression time so the dense recall arm has hot vectors
// instead of scoring cold-cache misses forever.
func (s *Store) WarmFacts(platform, selfID, chatType, chatID string, limit int) {
	s.mu.RLock()
	e := s.lookup(platform, selfID, chatType, chatID)
	if e == nil {
		s.mu.RUnlock()
		return
	}
	facts := append([]fact(nil), e.Facts...)
	vec := s.vec
	s.mu.RUnlock()
	if vec == nil {
		return
	}
	for i := len(facts) - 1; i >= 0 && len(facts)-i <= limit; i-- {
		vec.WarmAsync(facts[i].Text)
	}
}

// RecallFacts returns up to limit facts matching the message, freshest
// first — memories the conversation just pulled back into her head.
// Searches both active Facts and long-term Archive via hybrid lexical +
// semantic concept/vector scoring.
func (s *Store) RecallFacts(platform, selfID, chatType, chatID, query string, limit int) []fact {
	grams := textBigrams(query)

	// Phase 1: snapshot candidates under a read lock. lookup (not
	// entry) so recall never creates or claims a chat entry.
	s.mu.RLock()
	e := s.lookup(platform, selfID, chatType, chatID)
	if e == nil {
		s.mu.RUnlock()
		return nil
	}
	facts := append([]fact(nil), e.Facts...)
	archive := append([]fact(nil), e.Archive...)
	vec := s.vec
	s.mu.RUnlock()

	// The query itself usually misses the remote cache — without a
	// cached query vector CachedSimilarity can never blend dense
	// similarity, so warm it in the background for the next turn.
	// Same for archive memories added before the remote arm existed:
	// batch-warm so long-term recall stops degrading to local-only.
	if vec != nil {
		vec.WarmAsync(query)
		archiveTexts := make([]string, 0, len(archive))
		for _, f := range archive {
			archiveTexts = append(archiveTexts, f.Text)
		}
		vec.WarmBatch(archiveTexts)
	}

	// Phase 2: score and rank with no lock held — pure CPU, zero IO.
	type scored struct {
		f        fact
		score    int
		archived bool
	}
	seenText := make(map[string]bool, len(facts))
	hits := make([]scored, 0, len(facts)+len(archive))
	for _, f := range facts {
		seenText[f.Text] = true
		if sc := scoreFactHybrid(f, query, grams, vec); sc >= recallMinScore {
			hits = append(hits, scored{f: f, score: sc})
		}
	}
	for _, f := range archive {
		if seenText[f.Text] {
			continue
		}
		if sc := scoreFactHybrid(f, query, grams, vec); sc >= recallMinScore {
			hits = append(hits, scored{f: f, score: sc, archived: true})
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return factAnchor(hits[i].f).After(factAnchor(hits[j].f))
	})
	selected := hits
	if len(selected) > limit {
		selected = selected[:limit]
	}
	if len(selected) == 0 {
		return nil
	}
	out := make([]fact, 0, len(selected))
	for _, h := range selected {
		out = append(out, h.f)
	}

	// Phase 3: short write lock to bump LastSeenAt/Weight on the lines
	// that actually surfaced. Facts may have shifted since the
	// snapshot, so re-locate by Text instead of trusting old indexes;
	// a line that vanished simply skips its heat bump.
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.lookup(platform, selfID, chatType, chatID); e != nil {
		now := s.now()
		bumpIn := func(list []fact, text string) bool {
			for i := range list {
				if list[i].Text == text {
					list[i].LastSeenAt = now
					list[i].Weight = factWeight(list[i]) + 0.25
					return true
				}
			}
			return false
		}
		for _, h := range selected {
			// Recalled memories stay fresh — LastSeenAt is her "used
			// it recently" signal for later pruning, and Weight creeps
			// up so lines she leans on recall harder next time. The
			// expected list is tried first, the other as fallback in
			// case the line rotated between snapshot and write-back.
			if h.archived {
				if !bumpIn(e.Archive, h.f.Text) {
					bumpIn(e.Facts, h.f.Text)
				}
			} else {
				if !bumpIn(e.Facts, h.f.Text) {
					bumpIn(e.Archive, h.f.Text)
				}
			}
		}
		_ = s.saveLocked()
	}
	return out
}

// FactsSnapshot returns all chat facts, oldest first (compression,
// admin views, tests).
func (s *Store) FactsSnapshot(platform, selfID, chatType, chatID string) []fact {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e := s.lookup(platform, selfID, chatType, chatID)
	if e == nil {
		return nil
	}
	return append([]fact(nil), e.Facts...)
}

// ColdFact returns the memory she hasn't touched the longest — a fact
// or archived trace whose anchor is at least minAge old. Proactive uses
// it for the "suddenly remembered" beat instead of recycling the fresh
// material she already saw yesterday.
func (s *Store) ColdFact(platform, selfID, chatType, chatID string, now time.Time, minAge time.Duration) (fact, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e := s.lookup(platform, selfID, chatType, chatID)
	if e == nil {
		return fact{}, false
	}
	var coldest fact
	var best time.Time
	for _, bucket := range [][]fact{e.Facts, e.Archive} {
		for _, f := range bucket {
			anchor := f.LastSeenAt
			if anchor.IsZero() {
				anchor = f.CreatedAt
			}
			if anchor.IsZero() || now.Sub(anchor) < minAge {
				continue
			}
			if best.IsZero() || anchor.Before(best) {
				best, coldest = anchor, f
			}
		}
	}
	if best.IsZero() {
		return fact{}, false
	}
	return coldest, true
}

// RecentFacts returns the newest keepAlways facts — the steady core of
// what she still holds about this chat.
func (s *Store) RecentFacts(platform, selfID, chatType, chatID string, keepAlways int) []fact {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e := s.lookup(platform, selfID, chatType, chatID)
	if e == nil || len(e.Facts) == 0 {
		return nil
	}
	start := len(e.Facts) - keepAlways
	if start < 0 {
		start = 0
	}
	return append([]fact(nil), e.Facts[start:]...)
}

// ForgetImpression clears the take on one member of this chat.
func (s *Store) ForgetImpression(platform, selfID, chatType, chatID, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entry(platform, selfID, chatType, chatID)
	if e == nil || e.Impressions == nil {
		return errors.New("没有相关印象")
	}
	if _, ok := e.Impressions[strings.TrimSpace(userID)]; !ok {
		return errors.New("没有相关印象")
	}
	delete(e.Impressions, strings.TrimSpace(userID))
	return s.saveLocked()
}

// SetImpression overwrites the persona's remembered take on one member.
func (s *Store) SetImpression(platform, selfID, chatType, chatID, userID, text string) error {
	text = strings.TrimSpace(text)
	userID = strings.TrimSpace(userID)
	if userID == "" || text == "" {
		return errors.New("impression is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entry(platform, selfID, chatType, chatID)
	if e.Impressions == nil {
		e.Impressions = map[string]fact{}
	}
	if _, exists := e.Impressions[userID]; !exists &&
		len(e.Impressions) >= maxImpressionsPerChat {
		// drop the stalest impression to make room
		oldestUser, oldestTime := "", time.Now().AddDate(10, 0, 0)
		for u, f := range e.Impressions {
			if factAnchor(f).Before(oldestTime) {
				oldestUser, oldestTime = u, factAnchor(f)
			}
		}
		delete(e.Impressions, oldestUser)
	}
	now := s.now()
	e.Impressions[userID] = fact{Text: text, CreatedAt: now, LastSeenAt: now}
	return s.saveLocked()
}

// Impression returns the remembered take on a member, empty if none.
func (s *Store) Impression(platform, selfID, chatType, chatID, userID string) fact {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e := s.lookup(platform, selfID, chatType, chatID)
	if e == nil {
		return fact{}
	}
	return e.Impressions[strings.TrimSpace(userID)]
}

// SetProfile writes a persona-scoped impression that follows the same
// person across every binding this persona serves.
func (s *Store) SetProfile(platform, persona, userID, text string) error {
	persona = strings.TrimSpace(persona)
	userID = strings.TrimSpace(userID)
	text = sanitizeProfileText(text)
	if persona == "" || userID == "" || text == "" {
		return errors.New("profile needs persona, user and text")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := profileKey(platform, persona, userID)
	if _, ok := s.profiles[key]; !ok {
		count := 0
		for k := range s.profiles {
			if strings.HasPrefix(k, persona+"|") {
				count++
			}
		}
		if count >= maxProfilesPerPersona {
			oldestKey, oldestTime := "", time.Now().AddDate(10, 0, 0)
			for k, f := range s.profiles {
				if strings.HasPrefix(k, persona+"|") && factAnchor(f).Before(oldestTime) {
					oldestKey, oldestTime = k, factAnchor(f)
				}
			}
			delete(s.profiles, oldestKey)
		}
	}
	now := s.now()
	s.profiles[key] = fact{Text: text, CreatedAt: now, LastSeenAt: now}
	return s.saveLocked()
}

// Profile returns the persona-scoped take on a user, empty when she
// never formed one under this persona.
func (s *Store) Profile(platform, persona, userID string) fact {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.profiles[profileKey(platform, persona, userID)]
}

// UserCard returns the cached profile for one person on one platform.
func (s *Store) UserCard(platform, userID string) (UserCard, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.cards[userKey(platform, userID)]
	return c, ok
}

// SetUserCard upserts the cached profile. Callers may store a card with
// only FetchedAt set to mark "fetch tried and failed" — Nick stays empty
// so retries follow the short retry TTL, not the full card TTL.
func (s *Store) SetUserCard(platform, userID string, card UserCard) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return errors.New("user id is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	uk := userKey(platform, userID)
	old := s.cards[uk]
	card.Badges = old.Badges // badges are group-scoped, kept across refreshes
	s.cards[uk] = card
	return s.saveLocked()
}

// SetUserBadge records role/title a user holds in one group.
func (s *Store) SetUserBadge(platform, userID, groupID string, badge GroupBadge) error {
	userID = strings.TrimSpace(userID)
	groupID = strings.TrimSpace(groupID)
	if userID == "" || groupID == "" {
		return errors.New("badge needs user and group")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	uk := userKey(platform, userID)
	c := s.cards[uk]
	if c.Badges == nil {
		c.Badges = map[string]GroupBadge{}
	}
	c.Badges[groupID] = badge
	s.cards[uk] = c
	return s.saveLocked()
}

// NotesText returns the remembered notes for the chat joined with newlines.
func (s *Store) NotesText(platform, selfID, chatType, chatID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e := s.lookup(platform, selfID, chatType, chatID)
	if e == nil || len(e.Notes) == 0 {
		return ""
	}
	var b strings.Builder
	for i, note := range e.Notes {
		if b.Len() >= maxNoteRunes {
			break
		}
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString("· ")
		b.WriteString(note)
	}
	return b.String()
}

// moodDecayPerHour pulls the mood score back toward zero this many points
// per hour so a bad morning does not sour the whole week.
const moodDecayPerHour = 6

func decayedMood(e *chatEntry, now time.Time) int {
	if e == nil || e.Mood == 0 {
		return 0
	}
	if e.MoodAt.IsZero() {
		return e.Mood
	}
	steps := int(now.Sub(e.MoodAt).Hours() * moodDecayPerHour)
	if steps <= 0 {
		return e.Mood
	}
	if e.Mood > 0 {
		if v := e.Mood - steps; v > 0 {
			return v
		}
		return 0
	}
	if v := e.Mood + steps; v < 0 {
		return v
	}
	return 0
}

// BumpMood adjusts the chat mood score by delta (clamped -100..100) and
// persists. Returns the resulting decayed-adjusted score.
func (s *Store) BumpMood(platform, selfID, chatType, chatID string, delta int, now time.Time) (int, error) {
	if delta == 0 {
		return s.MoodScore(platform, selfID, chatType, chatID, now), nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entry(platform, selfID, chatType, chatID)
	mood := decayedMood(e, now) + delta
	if mood > 100 {
		mood = 100
	} else if mood < -100 {
		mood = -100
	}
	e.Mood = mood
	e.MoodAt = now
	return mood, s.saveLocked()
}

// MoodScore returns the decayed mood score for the chat (0 when unset).
func (s *Store) MoodScore(platform, selfID, chatType, chatID string, now time.Time) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return decayedMood(s.lookup(platform, selfID, chatType, chatID), now)
}

// SetMuteUntil makes the bot ignore the chat until the given time —
// the "go quiet" tool's backing switch.
func (s *Store) SetMuteUntil(platform, selfID, chatType, chatID string, until time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entry(platform, selfID, chatType, chatID).MuteUntil = until
	return s.saveLocked()
}

// Muted reports whether the chat is currently self-muted.
func (s *Store) Muted(platform, selfID, chatType, chatID string, now time.Time) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e := s.lookup(platform, selfID, chatType, chatID)
	return e != nil && !e.MuteUntil.IsZero() && now.Before(e.MuteUntil)
}

// ChatKeys lists every chat that has a prefs entry — the reflect engine
// sweeps these for consolidation work. Each tuple is
// {platform, selfID, chatType, chatID}; selfID "" marks a legacy
// shared entry no account has claimed yet.
func (s *Store) ChatKeys() [][4]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([][4]string, 0, len(s.data))
	for k := range s.data {
		platform, selfID, chatType, chatID := splitKey(k)
		out = append(out, [4]string{platform, selfID, chatType, chatID})
	}
	return out
}

// FactTexts returns the chat's fact lines as plain strings.
func (s *Store) FactTexts(platform, selfID, chatType, chatID string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e := s.lookup(platform, selfID, chatType, chatID)
	if e == nil || len(e.Facts) == 0 {
		return nil
	}
	out := make([]string, len(e.Facts))
	for i, f := range e.Facts {
		out[i] = f.Text
	}
	return out
}

// ImpressionSnapshot returns every member impression with its anchors,
// so the reflect engine can sort by staleness.
func (s *Store) ImpressionSnapshot(platform, selfID, chatType, chatID string) map[string]fact {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e := s.lookup(platform, selfID, chatType, chatID)
	if e == nil || len(e.Impressions) == 0 {
		return nil
	}
	out := make(map[string]fact, len(e.Impressions))
	for u, f := range e.Impressions {
		out[u] = f
	}
	return out
}

// ImpressionTexts returns every member impression as userID -> text.
func (s *Store) ImpressionTexts(platform, selfID, chatType, chatID string) map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e := s.lookup(platform, selfID, chatType, chatID)
	if e == nil || len(e.Impressions) == 0 {
		return nil
	}
	out := make(map[string]string, len(e.Impressions))
	for u, f := range e.Impressions {
		out[u] = f.Text
	}
	return out
}

// HasFactPrefix reports whether any fact starts with the prefix — the
// journal uses it to keep one diary entry per day.
func (s *Store) HasFactPrefix(platform, selfID, chatType, chatID, prefix string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e := s.lookup(platform, selfID, chatType, chatID)
	if e == nil {
		return false
	}
	for _, f := range e.Facts {
		if strings.HasPrefix(f.Text, prefix) {
			return true
		}
	}
	return false
}

// FactMeta returns aggregate metadata across a chat's active facts so
// the reflect engine can propagate emotional color and recall heat
// through a merge instead of resetting them to zero.
func (s *Store) FactMeta(platform, selfID, chatType, chatID string) (avgMood int, avgWeight float64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e := s.lookup(platform, selfID, chatType, chatID)
	if e == nil || len(e.Facts) == 0 {
		return 0, 1
	}
	var totalMood int
	var totalWeight float64
	for _, f := range e.Facts {
		totalMood += f.Mood
		totalWeight += factWeight(f)
	}
	n := len(e.Facts)
	return totalMood / n, totalWeight / float64(n)
}

// RewriteFacts swaps the whole fact list for the consolidated lines the
// reflect pass produced. avgMood and avgWeight carry forward the
// pre-merge emotional color and recall heat so consolidation no longer
// resets them to zero — a memory that tasted bitter before the merge
// still tastes bitter after.
func (s *Store) RewriteFacts(platform, selfID, chatType, chatID string, texts []string, avgMood int, avgWeight float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entry(platform, selfID, chatType, chatID)
	now := s.now()
	if avgWeight <= 0 {
		avgWeight = 1
	}
	seen := map[string]bool{}
	facts := make([]fact, 0, len(texts))
	for _, t := range texts {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		facts = append(facts, fact{
			Text:       t,
			CreatedAt:  now,
			LastSeenAt: now,
			Mood:       avgMood,
			Weight:     avgWeight,
		})
	}
	if len(facts) > maxFactsPerChat {
		facts = facts[len(facts)-maxFactsPerChat:]
	}
	e.Facts = facts
	return s.saveLocked()
}

// RewriteImpressions replaces the member impressions wholesale — stale
// members condensed into a roster fact have their entries dropped.
func (s *Store) RewriteImpressions(platform, selfID, chatType, chatID string, texts map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entry(platform, selfID, chatType, chatID)
	if e == nil {
		return errors.New("没有相关印象")
	}
	now := s.now()
	out := map[string]fact{}
	for u, t := range texts {
		u = strings.TrimSpace(u)
		t = strings.TrimSpace(t)
		if u == "" || t == "" {
			continue
		}
		old := e.Impressions[u]
		f := fact{Text: t, CreatedAt: now, LastSeenAt: now}
		if !old.CreatedAt.IsZero() {
			f.CreatedAt = old.CreatedAt
		}
		out[u] = f
	}
	e.Impressions = out
	return s.saveLocked()
}

// saveLocked marks the store dirty and nudges the background flusher —
// the real disk write is debounced in flushLoop so hot-path callers no
// longer pay a full-file marshal per update.
func (s *Store) saveLocked() error {
	if s.path == "" {
		return nil
	}
	s.dirty = true
	select {
	case s.saveCh <- struct{}{}:
	default:
	}
	return nil
}

// flushLocked performs the atomic full-file rewrite. Caller must hold
// s.mu (write side).
func (s *Store) flushLocked() error {
	if s.path == "" {
		return nil
	}
	raw, err := json.MarshalIndent(persistedStore{
		Chats:    s.data,
		Profiles: s.profiles,
		Cards:    s.cards,
	}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	s.dirty = false
	return nil
}

// maxRemindersPerChat bounds the prospective shelf — a chatty model
// should not be able to queue dozens of future wake-ups.
const maxRemindersPerChat = 10

// AddReminder queues a prospective memory: text she remembers on her
// own once triggerAt arrives ("周三他考试" fires Wednesday morning).
// Kept off the ordinary recall path — a pending reminder must never
// leak into replies before its time.
func (s *Store) AddReminder(platform, selfID, chatType, chatID, text string, triggerAt time.Time) error {
	text = cleanFactText(text)
	if text == "" {
		return errors.New("fact is empty")
	}
	if triggerAt.IsZero() {
		return errors.New("no trigger time")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entry(platform, selfID, chatType, chatID)
	now := s.now()
	for i, r := range e.Reminders {
		if r.Text == text {
			e.Reminders[i].TriggerAt = triggerAt
			return s.saveLocked()
		}
	}
	if len(e.Reminders) >= maxRemindersPerChat {
		// Shed the furthest-out reminder — the nearest deadline
		// matters most, like a person keeping only what is next.
		furthest := 0
		for i := range e.Reminders {
			if e.Reminders[i].TriggerAt.After(e.Reminders[furthest].TriggerAt) {
				furthest = i
			}
		}
		e.Reminders = append(e.Reminders[:furthest], e.Reminders[furthest+1:]...)
	}
	e.Reminders = append(e.Reminders, fact{
		Text:       text,
		CreatedAt:  now,
		LastSeenAt: now,
		TriggerAt:  triggerAt,
	})
	return s.saveLocked()
}

// DueReminder is a fired prospective memory with its routing keys —
// the reminder loop pops these and turns them into a spontaneous
// message to the chat.
type DueReminder struct {
	Platform string
	SelfID   string
	ChatType string
	ChatID   string
	Text     string
	SavedAt  time.Time
}

// DueReminders pops every reminder whose trigger time has passed —
// fired reminders are removed so they do not re-deliver every tick.
func (s *Store) DueReminders(now time.Time) []DueReminder {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []DueReminder
	dirty := false
	for k, e := range s.data {
		if len(e.Reminders) == 0 {
			continue
		}
		platform, selfID, chatType, chatID := splitKey(k)
		keep := e.Reminders[:0]
		for _, r := range e.Reminders {
			if r.TriggerAt.IsZero() || r.TriggerAt.After(now) {
				keep = append(keep, r)
				continue
			}
			out = append(out, DueReminder{
				Platform: platform, SelfID: selfID,
				ChatType: chatType, ChatID: chatID,
				Text: r.Text, SavedAt: r.CreatedAt,
			})
		}
		if len(keep) != len(e.Reminders) {
			e.Reminders = keep
			dirty = true
		}
	}
	if dirty {
		_ = s.saveLocked()
	}
	return out
}
