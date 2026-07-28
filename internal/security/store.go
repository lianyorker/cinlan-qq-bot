package security

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	maxIncidentStoreBytes = 2 << 20
	maxIncidentRecords    = 512
)

type Incident struct {
	Category    string    `json:"category"`
	Fingerprint string    `json:"fingerprint"`
	Count       uint64    `json:"count"`
	FirstSeen   time.Time `json:"first_seen"`
	LastSeen    time.Time `json:"last_seen"`
}

type incidentFile struct {
	Version   int        `json:"version"`
	Incidents []Incident `json:"incidents"`
}

type Store struct {
	mu       sync.RWMutex
	boundary *Boundary
	path     string
	items    map[string]Incident
	now      func() time.Time
}

func OpenStore(boundary *Boundary, path string) (*Store, error) {
	if boundary == nil {
		return nil, errors.New("incident store requires an allowed root")
	}
	resolved, err := boundary.Resolve(path)
	if err != nil {
		return nil, fmt.Errorf("resolve incident store path: %w", err)
	}
	store := &Store{
		boundary: boundary,
		path:     resolved,
		items:    make(map[string]Incident),
		now:      time.Now,
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

func (s *Store) Lookup(fingerprint string) (Incident, bool) {
	if s == nil {
		return Incident{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	incident, ok := s.items[fingerprint]
	return incident, ok
}

func (s *Store) List() []Incident {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]Incident, 0, len(s.items))
	for _, incident := range s.items {
		result = append(result, incident)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].LastSeen.Equal(result[j].LastSeen) {
			return result[i].Fingerprint < result[j].Fingerprint
		}
		return result[i].LastSeen.Before(result[j].LastSeen)
	})
	return result
}

func (s *Store) Record(category, fingerprint string) error {
	if s == nil {
		return errors.New("incident store is nil")
	}
	category = strings.TrimSpace(category)
	fingerprint = strings.TrimSpace(fingerprint)
	if category == "" || len(category) > 64 {
		return errors.New("incident category is invalid")
	}
	if len(fingerprint) != 64 {
		return errors.New("incident fingerprint is invalid")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	incident, exists := s.items[fingerprint]
	if !exists {
		if len(s.items) >= maxIncidentRecords {
			s.removeOldestLocked()
		}
		incident = Incident{
			Category:    category,
			Fingerprint: fingerprint,
			FirstSeen:   now,
		}
	}
	incident.Count++
	incident.LastSeen = now
	s.items[fingerprint] = incident
	return s.persistLocked()
}

func (s *Store) load() error {
	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open incident store: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxIncidentStoreBytes+1))
	if err != nil {
		return fmt.Errorf("read incident store: %w", err)
	}
	if len(data) > maxIncidentStoreBytes {
		return fmt.Errorf("incident store exceeds %d bytes", maxIncidentStoreBytes)
	}
	var decoded incidentFile
	if err := json.Unmarshal(data, &decoded); err != nil {
		return fmt.Errorf("decode incident store: %w", err)
	}
	if decoded.Version != 1 {
		return fmt.Errorf("unsupported incident store version %d", decoded.Version)
	}
	if len(decoded.Incidents) > maxIncidentRecords {
		return fmt.Errorf("incident store contains more than %d records", maxIncidentRecords)
	}
	for _, incident := range decoded.Incidents {
		if incident.Category == "" ||
			len(incident.Category) > 64 ||
			len(incident.Fingerprint) != 64 ||
			incident.Count == 0 ||
			incident.FirstSeen.IsZero() ||
			incident.LastSeen.IsZero() {
			return errors.New("incident store contains an invalid record")
		}
		if _, exists := s.items[incident.Fingerprint]; exists {
			return fmt.Errorf("incident store contains duplicate fingerprint %q", incident.Fingerprint)
		}
		s.items[incident.Fingerprint] = incident
	}
	return nil
}

func (s *Store) removeOldestLocked() {
	var (
		oldestKey string
		oldest    time.Time
	)
	for fingerprint, incident := range s.items {
		if oldestKey == "" || incident.LastSeen.Before(oldest) {
			oldestKey = fingerprint
			oldest = incident.LastSeen
		}
	}
	delete(s.items, oldestKey)
}

func (s *Store) persistLocked() error {
	resolved, err := s.boundary.Resolve(s.path)
	if err != nil {
		return fmt.Errorf("validate incident store path: %w", err)
	}
	if resolved != s.path {
		return errors.New("incident store target changed")
	}
	data, err := json.MarshalIndent(incidentFile{
		Version:   1,
		Incidents: s.listLocked(),
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode incident store: %w", err)
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create incident store directory: %w", err)
	}
	temp, err := os.CreateTemp(dir, ".security-incidents-*.tmp")
	if err != nil {
		return fmt.Errorf("create incident store temporary file: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return fmt.Errorf("secure incident store temporary file: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write incident store: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close incident store temporary file: %w", err)
	}
	if err := os.Rename(tempPath, s.path); err != nil {
		return fmt.Errorf("replace incident store: %w", err)
	}
	return nil
}

func (s *Store) listLocked() []Incident {
	result := make([]Incident, 0, len(s.items))
	for _, incident := range s.items {
		result = append(result, incident)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].LastSeen.Equal(result[j].LastSeen) {
			return result[i].Fingerprint < result[j].Fingerprint
		}
		return result[i].LastSeen.Before(result[j].LastSeen)
	})
	return result
}
