// Package chatprefs keeps per-chat operator preferences: a notification
// switch for proactive replies and a list of admin "remember" notes that
// get injected into the model's system prompt.
package chatprefs

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const maxNotesPerChat = 50
const maxNoteRunes = 2000

type chatEntry struct {
	NotifyOff bool     `json:"notify_off,omitempty"`
	Notes     []string `json:"notes,omitempty"`
}

// Store is a small JSON-file-backed per-chat preference store keyed by
// "<chat_type>:<chat_id>".
type Store struct {
	path string
	mu   sync.RWMutex
	data map[string]*chatEntry
}

func Load(path string) (*Store, error) {
	store := &Store{path: path, data: map[string]*chatEntry{}}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return store, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(raw, &store.data); err != nil {
		return nil, err
	}
	if store.data == nil {
		store.data = map[string]*chatEntry{}
	}
	return store, nil
}

func key(chatType, chatID string) string {
	return strings.TrimSpace(chatType) + ":" + strings.TrimSpace(chatID)
}

func (s *Store) entry(chatType, chatID string) *chatEntry {
	e := s.data[key(chatType, chatID)]
	if e == nil {
		e = &chatEntry{}
		s.data[key(chatType, chatID)] = e
	}
	return e
}

// SetNotifyEnabled toggles proactive replies for the chat. Enabled is the
// default; when disabled the bot only answers explicit mentions.
func (s *Store) SetNotifyEnabled(chatType, chatID string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entry(chatType, chatID).NotifyOff = !enabled
	return s.saveLocked()
}

func (s *Store) NotifyEnabled(chatType, chatID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e := s.data[key(chatType, chatID)]
	return e == nil || !e.NotifyOff
}

func (s *Store) AddNote(chatType, chatID, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return errors.New("note is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entry(chatType, chatID)
	if len(e.Notes) >= maxNotesPerChat {
		return errors.New("note limit reached")
	}
	e.Notes = append(e.Notes, text)
	return s.saveLocked()
}

// NotesText returns the remembered notes for the chat joined with newlines.
func (s *Store) NotesText(chatType, chatID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e := s.data[key(chatType, chatID)]
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

func (s *Store) saveLocked() error {
	if s.path == "" {
		return nil
	}
	raw, err := json.MarshalIndent(s.data, "", "  ")
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
