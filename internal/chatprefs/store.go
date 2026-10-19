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

type imageNote struct {
	Key  string `json:"key"`
	Path string `json:"path"`
}

type chatEntry struct {
	NotifyOff  bool        `json:"notify_off,omitempty"`
	Persona    string      `json:"persona,omitempty"`
	Notes      []string    `json:"notes,omitempty"`
	ImageNotes []imageNote `json:"image_notes,omitempty"`
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

// SetPersona pins the active persona for the chat; empty restores the
// binding default.
func (s *Store) SetPersona(chatType, chatID, persona string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entry(chatType, chatID).Persona = strings.TrimSpace(persona)
	return s.saveLocked()
}

// PersonaName returns the chat's pinned persona, or "" for the default.
func (s *Store) PersonaName(chatType, chatID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e := s.data[key(chatType, chatID)]
	if e == nil {
		return ""
	}
	return e.Persona
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

// AddImageNote remembers an image reference under a name so keyword image
// generation can reuse it when the prompt mentions that name.
func (s *Store) AddImageNote(chatType, chatID, name, path string) error {
	name = strings.TrimSpace(name)
	path = strings.TrimSpace(path)
	if name == "" || path == "" {
		return errors.New("image note is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entry(chatType, chatID)
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

// ImageForPrompt returns the stored image path whose key appears in prompt.
func (s *Store) ImageForPrompt(chatType, chatID, prompt string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e := s.data[key(chatType, chatID)]
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
