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

type Store struct {
	path   string
	mu     sync.RWMutex
	scores map[string]int
}

// Load opens the JSON score file; a missing file yields an empty store.
func Load(path string) (*Store, error) {
	store := &Store{path: strings.TrimSpace(path), scores: map[string]int{}}
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
	return store, nil
}

// Key builds the score key for a user talking to a bot account.
func Key(selfID, userID string) string {
	return strings.TrimSpace(selfID) + ":" + strings.TrimSpace(userID)
}

// Score returns the user's current affinity score.
func (s *Store) Score(selfID, userID string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.scores[Key(selfID, userID)]
}

// Add increments a user's score by delta and persists the file.
func (s *Store) Add(selfID, userID string, delta int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := Key(selfID, userID)
	s.scores[key] += delta
	if s.scores[key] < 0 {
		s.scores[key] = 0
	}
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
