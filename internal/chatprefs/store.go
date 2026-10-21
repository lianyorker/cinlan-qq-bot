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
	"time"
)

const maxNotesPerChat = 50
const maxNoteRunes = 2000

type imageNote struct {
	Key  string `json:"key"`
	Path string `json:"path"`
}

type chatEntry struct {
	NotifyOff  bool        `json:"notify_off,omitempty"`
	VoiceOff   bool        `json:"voice_off,omitempty"`
	Persona    string      `json:"persona,omitempty"`
	Notes      []string    `json:"notes,omitempty"`
	ImageNotes []imageNote `json:"image_notes,omitempty"`
	Mood       int         `json:"mood,omitempty"`
	MoodAt     time.Time   `json:"mood_at,omitempty"`
	MuteUntil  time.Time   `json:"mute_until,omitempty"`
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

// SetVoiceEnabled toggles [[VOICE]] replies for the chat. Enabled is the
// default; disabled strips the model's voice markers into silence.
func (s *Store) SetVoiceEnabled(chatType, chatID string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entry(chatType, chatID).VoiceOff = !enabled
	return s.saveLocked()
}

// VoiceEnabled reports whether voice clips are allowed for the chat.
func (s *Store) VoiceEnabled(chatType, chatID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e := s.data[key(chatType, chatID)]
	return e == nil || !e.VoiceOff
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

// LatestImage returns the most recently stored image note path for a chat.
func (s *Store) LatestImage(chatType, chatID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e := s.data[key(chatType, chatID)]
	if e == nil || len(e.ImageNotes) == 0 {
		return ""
	}
	return e.ImageNotes[len(e.ImageNotes)-1].Path
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
func (s *Store) BumpMood(chatType, chatID string, delta int, now time.Time) (int, error) {
	if delta == 0 {
		return s.MoodScore(chatType, chatID, now), nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entry(chatType, chatID)
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
func (s *Store) MoodScore(chatType, chatID string, now time.Time) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return decayedMood(s.data[key(chatType, chatID)], now)
}

// SetMuteUntil makes the bot ignore the chat until the given time —
// the "go quiet" tool's backing switch.
func (s *Store) SetMuteUntil(chatType, chatID string, until time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entry(chatType, chatID).MuteUntil = until
	return s.saveLocked()
}

// Muted reports whether the chat is currently self-muted.
func (s *Store) Muted(chatType, chatID string, now time.Time) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e := s.data[key(chatType, chatID)]
	return e != nil && !e.MuteUntil.IsZero() && now.Before(e.MuteUntil)
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
