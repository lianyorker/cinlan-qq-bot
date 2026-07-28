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
	Name            string   `json:"name"`
	Platform        string   `json:"platform"`
	SelfID          string   `json:"self_id"`
	ChatType        string   `json:"chat_type"`
	ChatID          string   `json:"chat_id,omitempty"`
	ChatIDs         []string `json:"chat_ids,omitempty"`
	UserIDs         []string `json:"user_ids,omitempty"`
	Persona         string   `json:"persona,omitempty"`
	Provider        string   `json:"provider,omitempty"`
	Tools           []string `json:"tools,omitempty"`
	Skills          []string `json:"skills,omitempty"`
	KnowledgeBases  []string `json:"knowledge_bases,omitempty"`
	MCPServers      []string `json:"mcp_servers,omitempty"`
	RequireMention  *bool    `json:"require_mention,omitempty"`
	SmartAttention  *bool    `json:"smart_attention,omitempty"`
	LearningEnabled *bool    `json:"learning_enabled,omitempty"`
	AllowLinks      *bool    `json:"allow_links,omitempty"`
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
	return r.MatchActor(platformName, selfID, chatType, chatID, "")
}

func (r *Registry) MatchActor(
	platformName, selfID, chatType, chatID, userID string,
) (Rule, bool) {
	if r == nil {
		return Rule{}, false
	}
	platformName = strings.TrimSpace(platformName)
	selfID = strings.TrimSpace(selfID)
	chatType = strings.TrimSpace(chatType)
	chatID = strings.TrimSpace(chatID)
	userID = strings.TrimSpace(userID)

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
			!matchesChat(rule, chatID) ||
			!matchesAny(rule.UserIDs, userID) {
			continue
		}
		score := specificity(rule)
		if score > bestScore {
			best = rule
			bestScore = score
		}
	}
	if bestScore < 0 {
		return Rule{}, false
	}
	return cloneRule(best), true
}

func (r *Registry) List() []Rule {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := append([]Rule(nil), r.rules...)
	for index := range result {
		result[index] = cloneRule(result[index])
	}
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
	rule.ChatID = strings.TrimSpace(rule.ChatID)
	if rule.ChatID == "" && len(rule.ChatIDs) == 0 {
		rule.ChatID = "*"
	}
	rule.Persona = strings.TrimSpace(rule.Persona)
	rule.Provider = strings.TrimSpace(rule.Provider)
	var err error
	if rule.ChatIDs, err = normalizeSelectorIDs(
		rule.Name,
		"chat_ids",
		rule.ChatIDs,
	); err != nil {
		return Rule{}, err
	}
	if rule.UserIDs, err = normalizeSelectorIDs(
		rule.Name,
		"user_ids",
		rule.UserIDs,
	); err != nil {
		return Rule{}, err
	}
	if rule.Tools, err = normalizeResources(rule.Name, "tools", rule.Tools); err != nil {
		return Rule{}, err
	}
	if rule.Skills, err = normalizeResources(rule.Name, "skills", rule.Skills); err != nil {
		return Rule{}, err
	}
	if rule.KnowledgeBases, err = normalizeResources(
		rule.Name,
		"knowledge_bases",
		rule.KnowledgeBases,
	); err != nil {
		return Rule{}, err
	}
	if rule.MCPServers, err = normalizeResources(
		rule.Name,
		"mcp_servers",
		rule.MCPServers,
	); err != nil {
		return Rule{}, err
	}

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
	if rule.Persona == "" &&
		rule.Provider == "" &&
		len(rule.Tools) == 0 &&
		len(rule.Skills) == 0 &&
		len(rule.KnowledgeBases) == 0 &&
		len(rule.MCPServers) == 0 &&
		rule.RequireMention == nil &&
		rule.SmartAttention == nil &&
		rule.LearningEnabled == nil &&
		rule.AllowLinks == nil {
		return Rule{}, fmt.Errorf("chat binding %q has no settings", rule.Name)
	}
	if err := validateID(rule.Name, "self_id", rule.SelfID); err != nil {
		return Rule{}, err
	}
	if rule.ChatID != "" {
		if err := validateID(rule.Name, "chat_id", rule.ChatID); err != nil {
			return Rule{}, err
		}
	}
	return cloneRule(rule), nil
}

func normalizeSelectorIDs(
	bindingName, field string,
	values []string,
) ([]string, error) {
	if len(values) > 256 {
		return nil, fmt.Errorf(
			"chat binding %q has more than 256 %s",
			bindingName,
			field,
		)
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if err := validateID(bindingName, field, value); err != nil {
			return nil, err
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	if len(result) > 1 {
		if _, wildcard := seen["*"]; wildcard {
			return nil, fmt.Errorf(
				"chat binding %q %s cannot mix * with explicit IDs",
				bindingName,
				field,
			)
		}
	}
	sort.Strings(result)
	return result, nil
}

func normalizeResources(bindingName, field string, values []string) ([]string, error) {
	if len(values) > 128 {
		return nil, fmt.Errorf("chat binding %q has more than 128 %s", bindingName, field)
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 128 {
			return nil, fmt.Errorf("chat binding %q has invalid %s entry", bindingName, field)
		}
		for _, current := range value {
			if current < 0x20 || current == 0x7f {
				return nil, fmt.Errorf("chat binding %q has invalid %s entry", bindingName, field)
			}
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func cloneRule(rule Rule) Rule {
	rule.ChatIDs = append([]string(nil), rule.ChatIDs...)
	rule.UserIDs = append([]string(nil), rule.UserIDs...)
	rule.Tools = append([]string(nil), rule.Tools...)
	rule.Skills = append([]string(nil), rule.Skills...)
	rule.KnowledgeBases = append([]string(nil), rule.KnowledgeBases...)
	rule.MCPServers = append([]string(nil), rule.MCPServers...)
	if rule.RequireMention != nil {
		value := *rule.RequireMention
		rule.RequireMention = &value
	}
	if rule.SmartAttention != nil {
		value := *rule.SmartAttention
		rule.SmartAttention = &value
	}
	if rule.LearningEnabled != nil {
		value := *rule.LearningEnabled
		rule.LearningEnabled = &value
	}
	if rule.AllowLinks != nil {
		value := *rule.AllowLinks
		rule.AllowLinks = &value
	}
	return rule
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

func matchesChat(rule Rule, chatID string) bool {
	if rule.ChatID != "" && matches(rule.ChatID, chatID) {
		return true
	}
	for _, selector := range rule.ChatIDs {
		if matches(selector, chatID) {
			return true
		}
	}
	return false
}

func matchesAny(selectors []string, value string) bool {
	if len(selectors) == 0 {
		return true
	}
	for _, selector := range selectors {
		if matches(selector, value) {
			return true
		}
	}
	return false
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
	if rule.ChatID != "*" || len(rule.ChatIDs) > 0 {
		score += 8
	}
	if len(rule.UserIDs) > 0 {
		score += 16
	}
	return score
}

func selectorKey(rule Rule) string {
	return rule.Platform + "\x00" +
		rule.SelfID + "\x00" +
		rule.ChatType + "\x00" +
		rule.ChatID + "\x00" +
		strings.Join(rule.ChatIDs, "\x01") + "\x00" +
		strings.Join(rule.UserIDs, "\x01")
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
