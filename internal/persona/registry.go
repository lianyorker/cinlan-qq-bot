package persona

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

type Dialogue struct {
	User      string `json:"user"`
	Assistant string `json:"assistant"`
}

type RoutingScope struct {
	Description      string   `json:"description,omitempty"`
	IncludeTopics    []string `json:"include_topics,omitempty"`
	ExcludeTopics    []string `json:"exclude_topics,omitempty"`
	PositiveExamples []string `json:"positive_examples,omitempty"`
	NegativeExamples []string `json:"negative_examples,omitempty"`
	Languages        []string `json:"languages,omitempty"`
}

type Profile struct {
	Name               string        `json:"name"`
	Description        string        `json:"description,omitempty"`
	SystemPrompt       string        `json:"system_prompt,omitempty"`
	BeginDialogs       []Dialogue    `json:"begin_dialogs,omitempty"`
	CustomErrorMessage string        `json:"custom_error_message,omitempty"`
	RoutingScope       *RoutingScope `json:"routing_scope,omitempty"`
	Default            bool          `json:"default,omitempty"`
}

type Registry struct {
	mu          sync.RWMutex
	profiles    map[string]Profile
	defaultName string
	path        string
	persistErr  error
}

func NewRegistry() *Registry {
	return &Registry{profiles: make(map[string]Profile)}
}

func (r *Registry) Register(profile Profile) error {
	profile, err := normalizeProfile(profile)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.profiles[profile.Name]; exists {
		return fmt.Errorf("persona %q is already registered", profile.Name)
	}
	r.profiles[profile.Name] = profile
	if r.defaultName == "" || profile.Default {
		r.defaultName = profile.Name
	}
	r.persistLocked()
	return r.persistErr
}

func (r *Registry) Upsert(profile Profile) error {
	profile, err := normalizeProfile(profile)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	previous, existed := r.profiles[profile.Name]
	previousDefault := r.defaultName
	r.profiles[profile.Name] = profile
	if r.defaultName == "" || profile.Default {
		r.defaultName = profile.Name
	}
	r.persistLocked()
	if r.persistErr != nil {
		if existed {
			r.profiles[profile.Name] = previous
		} else {
			delete(r.profiles, profile.Name)
		}
		r.defaultName = previousDefault
	}
	return r.persistErr
}

func (r *Registry) Delete(name string) (bool, error) {
	name = strings.TrimSpace(name)
	r.mu.Lock()
	defer r.mu.Unlock()
	profile, exists := r.profiles[name]
	if !exists {
		return false, nil
	}
	if name == r.defaultName {
		return false, fmt.Errorf("default persona %q cannot be deleted", name)
	}
	delete(r.profiles, name)
	r.persistLocked()
	if r.persistErr != nil {
		r.profiles[name] = profile
		return false, r.persistErr
	}
	return true, nil
}

func (r *Registry) SetDefault(name string) error {
	name = strings.TrimSpace(name)
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.profiles[name]; !ok {
		return fmt.Errorf("persona %q is not registered", name)
	}
	previous := r.defaultName
	r.defaultName = name
	r.persistLocked()
	if r.persistErr != nil {
		r.defaultName = previous
	}
	return r.persistErr
}

func (r *Registry) Default() (Profile, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	profile, ok := r.profiles[r.defaultName]
	if ok {
		profile = cloneProfile(profile)
		profile.Default = true
	}
	return profile, ok
}

func (r *Registry) Get(name string) (Profile, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	profile, ok := r.profiles[strings.TrimSpace(name)]
	if ok {
		profile = cloneProfile(profile)
	}
	return profile, ok
}

func (r *Registry) List() []Profile {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Profile, 0, len(r.profiles))
	for name, profile := range r.profiles {
		profile = cloneProfile(profile)
		profile.Default = name == r.defaultName
		result = append(result, profile)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (r *Registry) UseFile(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("persona config path is empty")
	}
	if _, err := os.Stat(path); err == nil {
		profiles, defaultName, loadErr := LoadFile(path)
		if loadErr != nil {
			return loadErr
		}
		replacement := make(map[string]Profile, len(profiles))
		for _, profile := range profiles {
			replacement[profile.Name] = profile
		}
		r.mu.Lock()
		r.profiles = replacement
		r.defaultName = defaultName
		r.path = path
		r.persistErr = nil
		r.mu.Unlock()
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat persona config: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.profiles) == 0 {
		return errors.New("cannot initialize persona config without a default persona")
	}
	r.path = path
	r.persistLocked()
	return r.persistErr
}

func (r *Registry) Path() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.path
}

func (r *Registry) PersistenceError() error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.persistErr
}

func (r *Registry) persistLocked() {
	if r.path == "" {
		r.persistErr = nil
		return
	}
	profiles := make([]Profile, 0, len(r.profiles))
	for _, profile := range r.profiles {
		profile.Default = false
		profiles = append(profiles, profile)
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Name < profiles[j].Name })
	data, err := json.MarshalIndent(struct {
		DefaultPersona string    `json:"default_persona"`
		Personas       []Profile `json:"personas"`
	}{
		DefaultPersona: r.defaultName,
		Personas:       profiles,
	}, "", "  ")
	if err == nil {
		err = writeAtomic(r.path, ".personas-*.tmp", data)
	}
	r.persistErr = err
}

func normalizeProfile(profile Profile) (Profile, error) {
	profile.Name = strings.TrimSpace(profile.Name)
	profile.Description = strings.TrimSpace(profile.Description)
	profile.SystemPrompt = strings.TrimSpace(profile.SystemPrompt)
	profile.CustomErrorMessage = strings.TrimSpace(profile.CustomErrorMessage)
	if len(profile.Description) > 4<<10 {
		return Profile{}, fmt.Errorf("persona %q description exceeds 4 KiB", profile.Name)
	}
	var err error
	profile.RoutingScope, err = normalizeRoutingScope(profile.Name, profile.RoutingScope)
	if err != nil {
		return Profile{}, err
	}
	if profile.Name == "" || len(profile.Name) > 64 {
		return Profile{}, errors.New("persona name is empty or exceeds 64 characters")
	}
	if profile.SystemPrompt == "" {
		return Profile{}, fmt.Errorf("persona %q system prompt is empty", profile.Name)
	}
	if len(profile.SystemPrompt) > 64<<10 {
		return Profile{}, fmt.Errorf("persona %q system prompt exceeds 64 KiB", profile.Name)
	}
	if len(profile.BeginDialogs) > 20 {
		return Profile{}, fmt.Errorf("persona %q has more than 20 begin dialogs", profile.Name)
	}
	dialogs := make([]Dialogue, 0, len(profile.BeginDialogs))
	for _, dialogue := range profile.BeginDialogs {
		dialogue.User = strings.TrimSpace(dialogue.User)
		dialogue.Assistant = strings.TrimSpace(dialogue.Assistant)
		if dialogue.User == "" || dialogue.Assistant == "" {
			return Profile{}, fmt.Errorf("persona %q has an incomplete begin dialog", profile.Name)
		}
		if len(dialogue.User) > 8<<10 || len(dialogue.Assistant) > 8<<10 {
			return Profile{}, fmt.Errorf("persona %q begin dialog exceeds 8 KiB", profile.Name)
		}
		dialogs = append(dialogs, dialogue)
	}
	profile.BeginDialogs = dialogs
	return profile, nil
}

func normalizeRoutingScope(name string, scope *RoutingScope) (*RoutingScope, error) {
	if scope == nil {
		return nil, nil
	}
	value := *scope
	value.Description = strings.TrimSpace(value.Description)
	if len(value.Description) > 8<<10 {
		return nil, fmt.Errorf("persona %q routing scope description exceeds 8 KiB", name)
	}
	var err error
	for _, target := range []struct {
		field  string
		values *[]string
		limit  int
	}{
		{"include_topics", &value.IncludeTopics, 64},
		{"exclude_topics", &value.ExcludeTopics, 64},
		{"positive_examples", &value.PositiveExamples, 32},
		{"negative_examples", &value.NegativeExamples, 32},
		{"languages", &value.Languages, 16},
	} {
		*target.values, err = normalizeRoutingValues(name, target.field, *target.values, target.limit)
		if err != nil {
			return nil, err
		}
	}
	if value.Description == "" && len(value.IncludeTopics) == 0 &&
		len(value.ExcludeTopics) == 0 && len(value.PositiveExamples) == 0 &&
		len(value.NegativeExamples) == 0 && len(value.Languages) == 0 {
		return nil, nil
	}
	return &value, nil
}

func normalizeRoutingValues(name, field string, values []string, limit int) ([]string, error) {
	if len(values) > limit {
		return nil, fmt.Errorf("persona %q routing scope has more than %d %s", name, limit, field)
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 1024 {
			return nil, fmt.Errorf("persona %q routing scope has invalid %s entry", name, field)
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func cloneProfile(profile Profile) Profile {
	profile.BeginDialogs = append([]Dialogue(nil), profile.BeginDialogs...)
	if profile.RoutingScope != nil {
		scope := *profile.RoutingScope
		scope.IncludeTopics = append([]string(nil), scope.IncludeTopics...)
		scope.ExcludeTopics = append([]string(nil), scope.ExcludeTopics...)
		scope.PositiveExamples = append([]string(nil), scope.PositiveExamples...)
		scope.NegativeExamples = append([]string(nil), scope.NegativeExamples...)
		scope.Languages = append([]string(nil), scope.Languages...)
		profile.RoutingScope = &scope
	}
	return profile
}

func writeAtomic(path, pattern string, data []byte) error {
	dir := filepath.Dir(path)
	if dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	temp, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if _, err = temp.Write(data); err == nil {
		err = temp.Close()
	} else {
		_ = temp.Close()
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tempName, path); err != nil {
		_ = os.Remove(path)
		if err = os.Rename(tempName, path); err != nil {
			return err
		}
	}
	return os.Chmod(path, 0o600)
}
