// Package moderation persists per-member spam warning counts so the
// recall → warn → act ladder survives restarts.
package moderation

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// warnEntry tracks how many times a member has been warned in one chat.
type warnEntry struct {
	Count int       `json:"count"`
	Last  time.Time `json:"last"`
}

// Store is a JSON-backed warning counter.
type Store struct {
	path  string
	mu    sync.Mutex
	warns map[string]warnEntry
}

// Open loads (or creates) the warning store at path. legacyPlatform is
// stamped onto pre-platform three-part keys so an upgrade keeps the
// warning counts instead of orphaning them.
func Open(path, legacyPlatform string) (*Store, error) {
	store := &Store{path: path, warns: make(map[string]warnEntry)}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		var file struct {
			Warnings map[string]warnEntry `json:"warnings"`
		}
		if err := json.Unmarshal(data, &file); err != nil {
			return nil, fmt.Errorf("moderation store: %w", err)
		}
		for key, entry := range file.Warnings {
			if entry.Count > 0 {
				store.warns[key] = entry
			}
		}
	case errors.Is(err, fs.ErrNotExist):
	default:
		return nil, fmt.Errorf("moderation store: %w", err)
	}
	if store.migrateLegacy(legacyPlatform) {
		// Rewrite right away so the migration doesn't depend on the
		// next coincidental Warn() firing.
		if saveErr := store.saveLocked(); saveErr != nil {
			return store, saveErr
		}
	}
	return store, nil
}

// migrateLegacy prefixes keys saved before the platform scope existed
// ("chatType:chatID:userID") with the legacy platform. Scoped keys
// already present always win — a collision keeps the newer count.
func (s *Store) migrateLegacy(legacyPlatform string) (migrated bool) {
	lp := strings.TrimSpace(legacyPlatform)
	if lp == "" {
		return false
	}
	for k, entry := range s.warns {
		if strings.Count(k, ":") != 2 {
			continue
		}
		dst := lp + ":" + k
		if _, clash := s.warns[dst]; !clash {
			s.warns[dst] = entry
		}
		delete(s.warns, k)
		migrated = true
	}
	return migrated
}

// Warn increments and returns the warning count for key, persisting to disk.
func (s *Store) Warn(key string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.warns[key]
	entry.Count++
	entry.Last = time.Now()
	s.warns[key] = entry
	if err := s.saveLocked(); err == nil {
		return entry.Count
	}
	return entry.Count
}

// Count returns the current warning count without mutating it.
func (s *Store) Count(key string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.warns[key].Count
}

// Reset clears the warning count for key after a ban/kick was issued.
func (s *Store) Reset(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.warns, key)
	_ = s.saveLocked()
}

func (s *Store) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	file := struct {
		Warnings map[string]warnEntry `json:"warnings"`
	}{Warnings: s.warns}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o600)
}
