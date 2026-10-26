// Package affinity tracks a per-user intimacy score the bot accumulates
// through conversation. The score is keyed by "selfID:userID" so a user's
// relationship with the persona follows them across groups and private
// chats, and it persists to a small JSON file.
package affinity

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Level names, ascending. Personas may attach a per-level behavior prompt
// keyed by these names via Profile.AffinityPrompts.
const (
	LevelStranger     = "陌生"
	LevelAcquaintance = "认识"
	LevelFamiliar     = "熟悉"
	LevelClose        = "亲近"
	LevelIntimate     = "亲密"
)

// levelThresholds maps each level to the minimum score it requires.
var levelThresholds = []struct {
	Name string
	Min  int
}{
	{LevelIntimate, 150},
	{LevelClose, 60},
	{LevelFamiliar, 20},
	{LevelAcquaintance, 5},
	{LevelStranger, 0},
}

// Level returns the level name for a score.
func Level(score int) string {
	for _, threshold := range levelThresholds {
		if score >= threshold.Min {
			return threshold.Name
		}
	}
	return LevelStranger
}

// Percent renders the raw score on a 0-100 scale for user-facing display.
func Percent(score int) int {
	if score < 0 {
		return 0
	}
	if score > 100 {
		return 100
	}
	return score
}

// GenericHint is the fallback behavior hint used when a persona does not
// define its own affinity prompt for the level.
func GenericHint(level string) string {
	switch level {
	case LevelStranger:
		return "你和对方还很陌生，保持礼貌，不要自来熟。"
	case LevelAcquaintance:
		return "你和对方聊过几次，可以自然提起之前的话题。"
	case LevelFamiliar:
		return "你和对方算是熟人了，语气可以更随意亲切。"
	case LevelClose:
		return "你和对方很亲近，可以撒娇、开玩笑、主动关心。"
	case LevelIntimate:
		return "对方是你最亲近的人之一，亲昵的称呼和玩笑可以放开一点（仍守分寸）。"
	}
	return ""
}

// dailyDeltaCap bounds how far one user's score can move per day —
// [[AFFINITY:±N]] markers are clamped to ±5 each, but nothing stopped
// a chatterbox from farming +5 all day long.
const dailyDeltaCap = 15

type Store struct {
	path       string
	mu         sync.RWMutex
	scores     map[string]int
	dailyDelta map[string]int // key → net change already spent today
	dailyDate  string         // "2006-01-02" — rolls the cap at midnight
}

// Load opens the JSON score file; a missing file yields an empty store.
// legacyPlatform is stamped onto pre-platform two-part keys so an
// upgrade keeps old scores instead of orphaning them.
func Load(path, legacyPlatform string) (*Store, error) {
	store := &Store{
		path:       strings.TrimSpace(path),
		scores:     map[string]int{},
		dailyDelta: map[string]int{},
		dailyDate:  time.Now().Format("2006-01-02"),
	}
	if store.path == "" {
		return store, nil
	}
	raw, err := os.ReadFile(store.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return store, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(raw, &store.scores); err != nil {
		return nil, err
	}
	if store.scores == nil {
		store.scores = map[string]int{}
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

// migrateLegacy prefixes keys saved before the platform scope existed
// ("self:user") with the legacy platform. Scoped keys already present
// always win — a collision merges the old score into the scoped entry
// rather than discarding it.
func (s *Store) migrateLegacy(legacyPlatform string) (migrated bool) {
	lp := strings.TrimSpace(legacyPlatform)
	if lp == "" {
		return false
	}
	for k, score := range s.scores {
		if strings.Count(k, ":") != 1 {
			continue
		}
		dst := lp + ":" + k
		s.scores[dst] += score
		delete(s.scores, k)
		migrated = true
	}
	return migrated
}

// Key builds the score key for a user talking to a bot account on a
// given platform — a douyin uid and a QQ number never share a score.
func Key(platform, selfID, userID string) string {
	return strings.TrimSpace(platform) + ":" + strings.TrimSpace(selfID) +
		":" + strings.TrimSpace(userID)
}

// Score returns the user's current affinity score.
func (s *Store) Score(platform, selfID, userID string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.scores[Key(platform, selfID, userID)]
}

// Add increments a user's score by delta and persists the file. The
// delta is trimmed so the user's net daily change stays inside
// ±dailyDeltaCap; the cap is in-memory only — a restart is a fresh day.
func (s *Store) Add(platform, selfID, userID string, delta int) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	today := time.Now().Format("2006-01-02")
	if s.dailyDate != today {
		s.dailyDelta = map[string]int{}
		s.dailyDate = today
	}
	if s.dailyDelta == nil {
		s.dailyDelta = map[string]int{}
	}

	key := Key(platform, selfID, userID)
	used := s.dailyDelta[key]
	if delta > 0 {
		if remain := dailyDeltaCap - used; remain <= 0 {
			return s.scores[key]
		} else if delta > remain {
			delta = remain
		}
	} else if delta < 0 {
		if remain := dailyDeltaCap + used; remain <= 0 {
			return s.scores[key]
		} else if -delta > remain {
			delta = -remain
		}
	}

	s.scores[key] += delta
	if s.scores[key] < 0 {
		s.scores[key] = 0
	}
	s.dailyDelta[key] += delta
	_ = s.saveLocked()
	return s.scores[key]
}

func (s *Store) saveLocked() error {
	if s.path == "" {
		return nil
	}
	raw, err := json.MarshalIndent(s.scores, "", "  ")
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
