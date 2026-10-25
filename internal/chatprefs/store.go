// Package chatprefs keeps per-chat operator preferences: a notification
// switch for proactive replies and a list of admin "remember" notes that
// get injected into the model's system prompt.
package chatprefs

import (
	"context"
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
	// SingOn gates [[SING]] clips — default OFF until real singing lands.
	SingOn     bool        `json:"sing_on,omitempty"`
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
}

// SetVecEngine attaches an optional dual-mode vector engine for semantic recall.
func (s *Store) SetVecEngine(v *vecstore.Engine) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.vec = v
}

func Load(path, legacyPlatform string) (*Store, error) {
	store := &Store{
		path:     path,
		data:     map[string]*chatEntry{},
		profiles: map[string]fact{},
		cards:    map[string]UserCard{},
		now:      time.Now,
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
		if saveErr := store.saveLocked(); saveErr != nil {
			return store, saveErr
		}
	}
	return store, nil
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

// SetSingEnabled toggles [[SING]] clips for the chat. Unlike voice,
// singing defaults OFF — operators opt in with /唱歌 开启.
func (s *Store) SetSingEnabled(platform, selfID, chatType, chatID string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entry(platform, selfID, chatType, chatID).SingOn = enabled
	return s.saveLocked()
}

// SingEnabled reports whether [[SING]] clips are allowed for the chat.
func (s *Store) SingEnabled(platform, selfID, chatType, chatID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e := s.lookup(platform, selfID, chatType, chatID)
	return e != nil && e.SingOn
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
	maxFactsPerChat       = 40
	maxArchivePerChat     = 400
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
	if vec != nil && vec.RemoteEnabled() {
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		sim := vec.Similarity(ctx, f.Text, query)
		cancel()
		if sim >= 0.45 {
			score += int(sim * 6)
		}
	}
	return int(float64(score) * factWeight(f))
}

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
		oldest := 0
		for i := range e.Archive {
			if factAnchor(e.Archive[i]).Before(factAnchor(e.Archive[oldest])) {
				oldest = i
			}
		}
		e.Archive = append(e.Archive[:oldest], e.Archive[oldest+1:]...)
	}
	e.Archive = append(e.Archive, evicted)
}

// AddFact stores a self-learned fact line for the chat. Duplicates are
// ignored; over capacity the least-recently-recalled memory moves into
// the long-term Archive instead of being lost. Zero-information filler
// ("嗯", "好的") is rejected so the memory pool stays meaningful.
func (s *Store) AddFact(platform, selfID, chatType, chatID, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return errors.New("fact is empty")
	}
	if !memoryWorthy(text) {
		return errors.New("nothing worth remembering")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addFactLocked(s.entry(platform, selfID, chatType, chatID), text, s.now())
}

// addFactLocked does the append/dedupe/prune under the held lock so
// ReplaceFact can fall back without re-locking.
func (s *Store) addFactLocked(e *chatEntry, text string, now time.Time) error {
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
	e.Facts = append(e.Facts, fact{
		Text: text, CreatedAt: now, LastSeenAt: now, Mood: currentMood,
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
		if strings.HasPrefix(f.Text, promisePrefix) || factAnchor(f).After(recent) {
			return true
		}
	}
	return false
}

// promisePrefix marks facts that record something she offered to do.
const promisePrefix = "承诺·"

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
	return s.addFactLocked(e, text, now)
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
	return errors.New("没找到要忘的内容")
}

// RecallFacts returns up to limit facts matching the message, freshest
// first — memories the conversation just pulled back into her head.
// Searches both active Facts and long-term Archive via hybrid lexical +
// semantic concept/vector scoring.
func (s *Store) RecallFacts(platform, selfID, chatType, chatID, query string, limit int) []fact {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entry(platform, selfID, chatType, chatID)
	if e == nil {
		return nil
	}
	grams := textBigrams(query)
	now := s.now()
	type scored struct {
		f        fact
		score    int
		idx      int
		archived bool
	}
	seenText := make(map[string]bool, len(e.Facts))
	hits := make([]scored, 0, len(e.Facts)+len(e.Archive))
	for i, f := range e.Facts {
		seenText[f.Text] = true
		if sc := scoreFactHybrid(f, query, grams, s.vec); sc >= recallMinScore {
			hits = append(hits, scored{f: f, score: sc, idx: i})
		}
	}
	for i, f := range e.Archive {
		if seenText[f.Text] {
			continue
		}
		if sc := scoreFactHybrid(f, query, grams, s.vec); sc >= recallMinScore {
			hits = append(hits, scored{f: f, score: sc, idx: i, archived: true})
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return factAnchor(hits[i].f).After(factAnchor(hits[j].f))
	})
	out := make([]fact, 0, limit)
	for _, h := range hits {
		if len(out) >= limit {
			break
		}
		out = append(out, h.f)
		// Recalled memories stay fresh — LastSeenAt is her "used it
		// recently" signal for later pruning, and Weight creeps up so
		// lines she actually leans on recall harder next time.
		if h.archived {
			e.Archive[h.idx].LastSeenAt = now
			e.Archive[h.idx].Weight = factWeight(e.Archive[h.idx]) + 0.25
		} else {
			e.Facts[h.idx].LastSeenAt = now
			e.Facts[h.idx].Weight = factWeight(e.Facts[h.idx]) + 0.25
		}
	}
	if len(out) > 0 {
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

// RewriteFacts swaps the whole fact list for the consolidated lines the
// reflect pass produced — anchors refresh because merged lines were
// just re-thought, not rescued from a shelf.
func (s *Store) RewriteFacts(platform, selfID, chatType, chatID string, texts []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entry(platform, selfID, chatType, chatID)
	now := s.now()
	seen := map[string]bool{}
	facts := make([]fact, 0, len(texts))
	for _, t := range texts {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		facts = append(facts, fact{Text: t, CreatedAt: now, LastSeenAt: now})
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

func (s *Store) saveLocked() error {
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
	return os.Rename(tmp, s.path)
}
