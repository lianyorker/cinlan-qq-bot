package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/domain"
)

type entry struct {
	History   []domain.ChatMessage `json:"history"`
	Handoff   bool                 `json:"handoff"`
	Persona   string               `json:"persona,omitempty"`
	Provider  string               `json:"provider,omitempty"`
	Summary   string               `json:"summary,omitempty"`
	Memory    string               `json:"memory,omitempty"`
	UpdatedAt time.Time            `json:"updated_at"`
}

type persistedState struct {
	Version  int              `json:"version"`
	Sessions map[string]entry `json:"sessions"`
}

const maxPersistedBytes = 16 << 20

type Info struct {
	ID           string    `json:"id"`
	HistoryCount int       `json:"history_count"`
	Handoff      bool      `json:"handoff"`
	Persona      string    `json:"persona,omitempty"`
	Provider     string    `json:"provider,omitempty"`
	HasSummary   bool      `json:"has_summary"`
	HasMemory    bool      `json:"has_memory"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type Settings struct {
	Persona  string `json:"persona,omitempty"`
	Provider string `json:"provider,omitempty"`
}

type Snapshot struct {
	History  []domain.ChatMessage
	Handoff  bool
	Settings Settings
	Summary  string
	Memory   string
}

type Store struct {
	mu         sync.Mutex
	entries    map[string]entry
	markers    map[string]time.Time
	maxHistory int
	ttl        time.Duration
	now        func() time.Time
	path       string
	persistErr error
	sqlite     *sqliteBackend
}

func New(maxHistory int, ttl time.Duration) *Store {
	return newWithClock(maxHistory, ttl, time.Now)
}

func newWithClock(maxHistory int, ttl time.Duration, now func() time.Time) *Store {
	return &Store{
		entries:    make(map[string]entry),
		markers:    make(map[string]time.Time),
		maxHistory: maxHistory,
		ttl:        ttl,
		now:        now,
	}
}

// openLegacy restores the retired plaintext JSON format for migration tests.
// Production callers must use OpenEncryptedSQLite.
func openLegacy(maxHistory int, ttl time.Duration, path string) (*Store, error) {
	store := newWithClock(maxHistory, ttl, time.Now)
	store.path = stringsTrim(path)
	if store.path == "" {
		return store, nil
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) Path() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.path
}

func (s *Store) PersistenceError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.persistErr
}

// ClaimMarker atomically records a durable one-time event. It returns false
// when the same marker was already claimed.
func (s *Store) ClaimMarker(key string) (bool, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return false, errors.New("marker key is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.markers[key]; exists {
		return false, nil
	}
	createdAt := s.now()
	if s.sqlite != nil {
		claimed, err := s.claimSQLiteMarkerLocked(key, createdAt)
		s.persistErr = err
		if err != nil || !claimed {
			return claimed, err
		}
	}
	s.markers[key] = createdAt
	return true, nil
}

// ReleaseMarker rolls back a claim when its associated operation did not
// complete.
func (s *Store) ReleaseMarker(key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("marker key is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.markers[key]; !exists {
		return nil
	}
	if s.sqlite != nil {
		err := s.releaseSQLiteMarkerLocked(key)
		s.persistErr = err
		if err != nil {
			return err
		}
	}
	delete(s.markers, key)
	return nil
}

func (s *Store) Snapshot(key string) ([]domain.ChatMessage, bool) {
	current := s.SnapshotState(key)
	return current.History, current.Handoff
}

func (s *Store) SnapshotState(key string) Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	current, ok := s.getLocked(key)
	if !ok {
		return Snapshot{}
	}
	return Snapshot{
		History: cloneMessages(current.History),
		Handoff: current.Handoff,
		Settings: Settings{
			Persona:  current.Persona,
			Provider: current.Provider,
		},
		Summary: current.Summary,
		Memory:  current.Memory,
	}
}

func (s *Store) Settings(key string) Settings {
	return s.SnapshotState(key).Settings
}

// UpdateSettings changes only non-nil fields. Passing a pointer to an empty
// string clears that override and returns the complete resulting settings.
func (s *Store) UpdateSettings(key string, persona, provider *string) Settings {
	s.mu.Lock()
	defer s.mu.Unlock()

	current, _ := s.getLocked(key)
	if persona != nil {
		current.Persona = strings.TrimSpace(*persona)
	}
	if provider != nil {
		current.Provider = strings.TrimSpace(*provider)
	}
	current.UpdatedAt = s.now()
	s.entries[key] = current
	s.persistLocked()
	return Settings{Persona: current.Persona, Provider: current.Provider}
}

func (s *Store) AddExchange(key, userText, assistantText string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	current, _ := s.getLocked(key)
	current.History = append(current.History,
		domain.ChatMessage{Role: "user", Content: userText},
		domain.ChatMessage{Role: "assistant", Content: assistantText},
	)
	if overflow := len(current.History) - s.maxHistory; overflow > 0 {
		if overflow%2 != 0 {
			overflow++
		}
		current.History = append([]domain.ChatMessage(nil), current.History[overflow:]...)
	}
	current.UpdatedAt = s.now()
	s.entries[key] = current
	s.persistLocked()
}

func (s *Store) ApplyCompression(key, summary, memory string, retain int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	current, ok := s.getLocked(key)
	if !ok {
		return
	}
	current.Summary = strings.TrimSpace(summary)
	current.Memory = strings.TrimSpace(memory)
	if retain < 0 {
		retain = 0
	}
	if retain%2 != 0 {
		retain--
	}
	if overflow := len(current.History) - retain; overflow > 0 {
		current.History = append(
			[]domain.ChatMessage(nil),
			current.History[overflow:]...,
		)
	}
	current.UpdatedAt = s.now()
	s.entries[key] = current
	s.persistLocked()
}

// ApplyCompressionSnapshot removes only the prefix covered by the supplied
// snapshot. Messages appended while compression was running are preserved.
func (s *Store) ApplyCompressionSnapshot(
	key, summary, memory string,
	retain int,
	snapshot []domain.ChatMessage,
) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	current, ok := s.getLocked(key)
	if !ok || len(snapshot) == 0 || len(current.History) < len(snapshot) {
		return false
	}
	for index := range snapshot {
		if current.History[index] != snapshot[index] {
			return false
		}
	}
	if retain < 0 {
		retain = 0
	}
	if retain%2 != 0 {
		retain--
	}
	if retain > len(snapshot) {
		retain = len(snapshot)
	}
	remove := len(snapshot) - retain
	current.History = append(
		[]domain.ChatMessage(nil),
		current.History[remove:]...,
	)
	current.Summary = strings.TrimSpace(summary)
	current.Memory = strings.TrimSpace(memory)
	current.UpdatedAt = s.now()
	s.entries[key] = current
	s.persistLocked()
	return true
}

func (s *Store) Clear(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	current, _ := s.getLocked(key)
	current.History = nil
	current.Summary = ""
	current.Memory = ""
	current.UpdatedAt = s.now()
	s.entries[key] = current
	s.persistLocked()
}

func (s *Store) SetHandoff(key string, enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	current, _ := s.getLocked(key)
	current.Handoff = enabled
	current.UpdatedAt = s.now()
	s.entries[key] = current
	s.persistLocked()
}

func (s *Store) CleanupExpired() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	removed := 0
	for key, current := range s.entries {
		if now.Sub(current.UpdatedAt) >= s.ttl {
			delete(s.entries, key)
			removed++
		}
	}
	if removed > 0 {
		s.persistLocked()
	}
	return removed
}

func (s *Store) List() []Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	result := make([]Info, 0, len(s.entries))
	for key, current := range s.entries {
		if now.Sub(current.UpdatedAt) >= s.ttl {
			continue
		}
		result = append(result, Info{
			ID:           key,
			HistoryCount: len(current.History),
			Handoff:      current.Handoff,
			Persona:      current.Persona,
			Provider:     current.Provider,
			HasSummary:   strings.TrimSpace(current.Summary) != "",
			HasMemory:    strings.TrimSpace(current.Memory) != "",
			UpdatedAt:    current.UpdatedAt,
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (s *Store) ListHandoffs() []Info {
	all := s.List()
	result := make([]Info, 0)
	for _, current := range all {
		if current.Handoff {
			result = append(result, current)
		}
	}
	return result
}

func (s *Store) Delete(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.entries[key]; !ok {
		return false
	}
	delete(s.entries, key)
	s.persistLocked()
	return true
}

func (s *Store) ClearAll() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := len(s.entries)
	if count == 0 {
		return 0
	}
	s.entries = make(map[string]entry)
	s.persistLocked()
	return count
}

func (s *Store) RunJanitor(ctx context.Context) {
	interval := s.ttl / 4
	if interval > 10*time.Minute {
		interval = 10 * time.Minute
	}
	if interval < time.Minute {
		interval = time.Minute
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.CleanupExpired()
		}
	}
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sqlite == nil || s.sqlite.db == nil {
		return nil
	}
	err := s.sqlite.db.Close()
	s.sqlite.db = nil
	return err
}

func (s *Store) getLocked(key string) (entry, bool) {
	current, ok := s.entries[key]
	if !ok {
		return entry{}, false
	}
	if s.now().Sub(current.UpdatedAt) >= s.ttl {
		delete(s.entries, key)
		return entry{}, false
	}
	return current, true
}

func (s *Store) load() error {
	info, statErr := os.Stat(s.path)
	if statErr == nil && info.Size() > maxPersistedBytes {
		return fmt.Errorf("session store exceeds %d bytes", maxPersistedBytes)
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("stat session store: %w", statErr)
	}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read session store: %w", err)
	}
	if len(data) == 0 {
		return nil
	}
	var state persistedState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("decode session store: %w", err)
	}
	if state.Version != 0 && state.Version != 1 {
		return fmt.Errorf("unsupported session store version %d", state.Version)
	}
	for key, current := range state.Sessions {
		if key == "" || current.UpdatedAt.IsZero() {
			continue
		}
		current.History = cloneMessages(current.History)
		s.entries[key] = current
	}
	return nil
}

func (s *Store) persistLocked() {
	if s.sqlite != nil {
		s.persistErr = s.persistSQLiteLocked()
		return
	}
	if s.path == "" {
		return
	}
	state := persistedState{
		Version:  1,
		Sessions: make(map[string]entry, len(s.entries)),
	}
	for key, current := range s.entries {
		current.History = cloneMessages(current.History)
		state.Sessions[key] = current
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err == nil {
		dir := filepath.Dir(s.path)
		if dir != "." {
			err = os.MkdirAll(dir, 0o700)
		}
	}
	if err == nil {
		temp, createErr := os.CreateTemp(filepath.Dir(s.path), ".sessions-*.tmp")
		if createErr != nil {
			err = createErr
		} else {
			tempName := temp.Name()
			_, writeErr := temp.Write(data)
			closeErr := temp.Close()
			if writeErr != nil {
				err = writeErr
			} else if closeErr != nil {
				err = closeErr
			} else if renameErr := os.Rename(tempName, s.path); renameErr != nil {
				// Windows does not replace an existing file with Rename.
				_ = os.Remove(s.path)
				err = os.Rename(tempName, s.path)
			}
			if err != nil {
				_ = os.Remove(tempName)
			} else {
				_ = os.Chmod(s.path, 0o600)
			}
		}
	}
	s.persistErr = err
}

func stringsTrim(value string) string {
	return strings.TrimSpace(value)
}

func cloneMessages(messages []domain.ChatMessage) []domain.ChatMessage {
	return append([]domain.ChatMessage(nil), messages...)
}
