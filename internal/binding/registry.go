package binding

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/lianyorker/cinlan-qq-bot/internal/platform"
)

type Rule struct {
	Name           string `json:"name"`
	Platform       string `json:"platform"`
	SelfID         string `json:"self_id"`
	ChatType       string `json:"chat_type"`
	ChatID         string `json:"chat_id"`
	Persona        string `json:"persona,omitempty"`
	Provider       string `json:"provider,omitempty"`
	RequireMention *bool  `json:"require_mention,omitempty"`
}

type Registry struct {
	mu         sync.RWMutex
	rules      []Rule
	path       string
	persistErr error
}

func NewRegistry(rules []Rule) (*Registry, error) {
	registry := &Registry{}
	seenNames := make(map[string]struct{}, len(rules))
	seenSelectors := make(map[string]struct{}, len(rules))
	for _, raw := range rules {
		rule, err := normalize(raw)
		if err != nil {
			return nil, err
		}
		if _, exists := seenNames[rule.Name]; exists {
			return nil, fmt.Errorf("chat binding %q is duplicated", rule.Name)
		}
		selector := selectorKey(rule)
		if _, exists := seenSelectors[selector]; exists {
			return nil, fmt.Errorf("chat binding selector %q is duplicated", selector)
		}
		seenNames[rule.Name] = struct{}{}
		seenSelectors[selector] = struct{}{}
		registry.rules = append(registry.rules, rule)
	}
	return registry, nil
}

func (r *Registry) Match(platformName, selfID, chatType, chatID string) (Rule, bool) {
	if r == nil {
		return Rule{}, false
	}
	platformName = strings.TrimSpace(platformName)
	selfID = strings.TrimSpace(selfID)
	chatType = strings.TrimSpace(chatType)
	chatID = strings.TrimSpace(chatID)

	r.mu.RLock()
	defer r.mu.RUnlock()
	var (
		best      Rule
		bestScore = -1
	)
	for _, rule := range r.rules {
		if !matches(rule.Platform, platformName) ||
			!matches(rule.SelfID, selfID) ||
			!matches(rule.ChatType, chatType) ||
			!matches(rule.ChatID, chatID) {
			continue
		}
		score := specificity(rule)
		if score > bestScore {
			best = rule
			bestScore = score
		}
	}
	return best, bestScore >= 0
}

func (r *Registry) List() []Rule {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := append([]Rule(nil), r.rules...)
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (r *Registry) Upsert(raw Rule) error {
	rule, err := normalize(raw)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	previous := append([]Rule(nil), r.rules...)
	for _, current := range r.rules {
		if current.Name != rule.Name && selectorKey(current) == selectorKey(rule) {
			return fmt.Errorf("chat binding selector %q is already used by %q", selectorKey(rule), current.Name)
		}
	}
	replaced := false
	for index := range r.rules {
		if r.rules[index].Name == rule.Name {
			r.rules[index] = rule
			replaced = true
			break
		}
	}
	if !replaced {
		r.rules = append(r.rules, rule)
	}
	r.persistLocked()
	if r.persistErr != nil {
		r.rules = previous
	}
	return r.persistErr
}

func (r *Registry) Delete(name string) (bool, error) {
	name = strings.TrimSpace(name)
	r.mu.Lock()
	defer r.mu.Unlock()
	for index, rule := range r.rules {
		if rule.Name != name {
			continue
		}
		previous := append([]Rule(nil), r.rules...)
		r.rules = append(r.rules[:index], r.rules[index+1:]...)
		r.persistLocked()
		if r.persistErr != nil {
			r.rules = previous
			return false, r.persistErr
		}
		return true, nil
	}
	return false, nil
}

func OpenFile(path string) (*Registry, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("chat binding config path is empty")
	}
	if _, err := os.Stat(path); err == nil {
		registry, loadErr := LoadFile(path)
		if loadErr != nil {
			return nil, loadErr
		}
		registry.path = path
		return registry, nil
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("stat chat binding config: %w", err)
	}
	registry, err := NewRegistry(nil)
	if err != nil {
		return nil, err
	}
	registry.path = path
	registry.mu.Lock()
	registry.persistLocked()
	registry.mu.Unlock()
	if registry.persistErr != nil {
		return nil, registry.persistErr
	}
	return registry, nil
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
	rules := append([]Rule(nil), r.rules...)
	sort.Slice(rules, func(i, j int) bool { return rules[i].Name < rules[j].Name })
	data, err := json.MarshalIndent(struct {
		Bindings []Rule `json:"bindings"`
	}{Bindings: rules}, "", "  ")
	if err == nil {
		err = writeAtomic(r.path, data)
	}
	r.persistErr = err
}

func normalize(rule Rule) (Rule, error) {
	rule.Name = strings.TrimSpace(rule.Name)
	rule.Platform = defaultWildcard(rule.Platform)
	rule.SelfID = defaultWildcard(rule.SelfID)
	rule.ChatType = defaultWildcard(rule.ChatType)
	rule.ChatID = defaultWildcard(rule.ChatID)
	rule.Persona = strings.TrimSpace(rule.Persona)
	rule.Provider = strings.TrimSpace(rule.Provider)

	if rule.Name == "" || len(rule.Name) > 64 {
		return Rule{}, fmt.Errorf("chat binding name %q is invalid", rule.Name)
	}
	if len(rule.Platform) > 64 {
		return Rule{}, fmt.Errorf("chat binding %q platform is invalid", rule.Name)
	}
	if rule.ChatType != "*" &&
		rule.ChatType != platform.ChatPrivate &&
		rule.ChatType != platform.ChatGroup {
		return Rule{}, fmt.Errorf("chat binding %q has invalid chat_type %q", rule.Name, rule.ChatType)
	}
	if rule.Persona == "" && rule.Provider == "" && rule.RequireMention == nil {
		return Rule{}, fmt.Errorf("chat binding %q has no settings", rule.Name)
	}
	if err := validateID(rule.Name, "self_id", rule.SelfID); err != nil {
		return Rule{}, err
	}
	if err := validateID(rule.Name, "chat_id", rule.ChatID); err != nil {
		return Rule{}, err
	}
	return rule, nil
}

func validateID(name, field, value string) error {
	if value == "*" {
		return nil
	}
	if value == "" {
		return errors.New("chat binding ID is empty")
	}
	if len(value) > 256 {
		return fmt.Errorf("chat binding %q has an overlong %s", name, field)
	}
	for _, current := range value {
		if current < 0x20 || current == 0x7f {
			return fmt.Errorf("chat binding %q has invalid %s", name, field)
		}
	}
	return nil
}

func defaultWildcard(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "*"
	}
	return value
}

func matches(selector, value string) bool {
	return selector == "*" || selector == value
}

// Chat identity is the strongest selector, followed by chat type, account,
// and platform. This keeps a group-specific rule ahead of broad defaults.
func specificity(rule Rule) int {
	score := 0
	if rule.Platform != "*" {
		score += 1
	}
	if rule.SelfID != "*" {
		score += 2
	}
	if rule.ChatType != "*" {
		score += 4
	}
	if rule.ChatID != "*" {
		score += 8
	}
	return score
}

func selectorKey(rule Rule) string {
	return rule.Platform + "\x00" + rule.SelfID + "\x00" + rule.ChatType + "\x00" + rule.ChatID
}

func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	temp, err := os.CreateTemp(dir, ".bindings-*.tmp")
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
