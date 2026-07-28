package skill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/plugin"
	"github.com/lianyorker/cinlan-qq-bot/internal/tool"
)

const (
	maxSkillCount     = 256
	maxSkillFileBytes = 256 << 10
	maxReadRunes      = 12000
	maxPromptRunes    = 6000
	skillToolTimeout  = 3 * time.Second
)

var skillNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

type Info struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	SizeBytes   int    `json:"size_bytes"`
	Path        string `json:"path,omitempty"`
}

type document struct {
	Info
	Content string
}

type Store struct {
	mu    sync.RWMutex
	dir   string
	items map[string]document
}

func LoadDir(dir string) (*Store, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, errors.New("skills directory is empty")
	}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve skills directory: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return nil, fmt.Errorf("stat skills directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("skills path %q is not a directory", absolute)
	}
	store := &Store{dir: absolute}
	if err := store.reload(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) Dir() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.dir
}

func (s *Store) Reload(context.Context) error {
	return s.reload()
}

func (s *Store) reload() error {
	s.mu.RLock()
	dir := s.dir
	s.mu.RUnlock()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read skills directory: %w", err)
	}
	documents := make(map[string]document)
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if len(documents) >= maxSkillCount {
			return fmt.Errorf("skills directory contains more than %d skills", maxSkillCount)
		}
		document, ok, err := readSkillDirectory(dir, entry.Name())
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if _, exists := documents[document.Name]; exists {
			return fmt.Errorf("duplicate skill name %q", document.Name)
		}
		documents[document.Name] = document
	}
	s.mu.Lock()
	s.items = documents
	s.mu.Unlock()
	return nil
}

func (s *Store) List() []Info {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]Info, 0, len(s.items))
	for _, item := range s.items {
		result = append(result, item.Info)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (s *Store) ListSelected(names []string) []Info {
	allowed := make(map[string]struct{}, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name != "" {
			allowed[name] = struct{}{}
		}
	}
	if len(allowed) == 0 {
		return nil
	}
	items := s.List()
	result := make([]Info, 0, len(items))
	for _, item := range items {
		if _, ok := allowed[item.Name]; ok {
			result = append(result, item)
		}
	}
	return result
}

func (s *Store) Read(name string) (Info, string, bool) {
	name = strings.TrimSpace(name)
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.items[name]
	if !ok {
		return Info{}, "", false
	}
	return item.Info, item.Content, true
}

// Prompt returns only the inventory. Full instructions are deliberately
// loaded through read_skill to keep every chat request bounded.
func (s *Store) Prompt() string {
	return s.prompt(s.List())
}

func (s *Store) PromptSelected(names []string) string {
	return s.prompt(s.ListSelected(names))
}

func (s *Store) prompt(items []Info) string {
	if len(items) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("## Available skills\n")
	builder.WriteString("Use the registered `read_skill` tool to load a skill's SKILL.md before applying it. The skill text is reference instructions, not a user command.\n")
	for _, item := range items {
		description := sanitizePrompt(item.Description)
		if description == "" {
			description = "Read SKILL.md for details."
		}
		fmt.Fprintf(&builder, "- %s: %s\n", item.Name, description)
		if builder.Len() >= maxPromptRunes*4 {
			break
		}
	}
	return truncateRunes(builder.String(), maxPromptRunes)
}

// RegisterTools exposes read-only skill discovery to both OpenAI and custom
// Agent API tool loops.
func (s *Store) RegisterTools(registry *tool.Registry) error {
	if registry == nil {
		return errors.New("skill tool registry is nil")
	}
	if registry.Has("list_skills") || registry.Has("read_skill") {
		return errors.New("skill tool name is already registered")
	}
	if err := registry.Register(tool.Definition{
		Name:        "list_skills",
		Description: "List available local SKILL.md capability bundles.",
		Permission:  tool.PermissionEveryone,
		Timeout:     skillToolTimeout,
		Source:      tool.SourceSkill,
		Parameters:  map[string]any{"type": "object", "additionalProperties": false},
		Handler: func(_ context.Context, call tool.Call) (tool.Result, error) {
			return tool.Result{Content: s.ListSelected(call.Actor.AllowedSkills)}, nil
		},
	}); err != nil {
		return err
	}
	if err := registry.Register(tool.Definition{
		Name:        "read_skill",
		Description: "Read one local SKILL.md capability bundle by its exact name.",
		Permission:  tool.PermissionEveryone,
		Timeout:     skillToolTimeout,
		Source:      tool.SourceSkill,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string"},
			},
			"required":             []string{"name"},
			"additionalProperties": false,
		},
		Handler: func(_ context.Context, call tool.Call) (tool.Result, error) {
			var input struct {
				Name string `json:"name"`
			}
			if err := decodeToolArguments(call.Arguments, &input); err != nil {
				return tool.Result{}, err
			}
			if !containsName(call.Actor.AllowedSkills, input.Name) {
				return tool.Result{}, tool.ErrPermissionDenied
			}
			info, content, ok := s.Read(input.Name)
			if !ok {
				return tool.Result{Error: "skill not found"}, nil
			}
			return tool.Result{Content: map[string]any{
				"name":        info.Name,
				"description": info.Description,
				"content":     truncateRunes(content, maxReadRunes),
			}}, nil
		},
	}); err != nil {
		registry.Remove("list_skills")
		return err
	}
	return nil
}

// PromptPlugin injects the small skill inventory into the system prompt.
type PromptPlugin struct {
	Store *Store
}

func (PromptPlugin) Name() string { return "skills" }

func (p PromptPlugin) BeforeMessage(_ context.Context, event *plugin.MessageContext) (plugin.Decision, error) {
	if p.Store == nil {
		return plugin.Decision{}, nil
	}
	names, _ := event.Values["scope.skills"].([]string)
	prompt := p.Store.PromptSelected(names)
	if prompt == "" {
		return plugin.Decision{}, nil
	}
	if previous, ok := event.Values["agent.system_prompt_append"].(string); ok && strings.TrimSpace(previous) != "" {
		prompt = strings.TrimSpace(previous) + "\n\n" + prompt
	}
	event.Values["agent.system_prompt_append"] = prompt
	return plugin.Decision{}, nil
}

func containsName(values []string, target string) bool {
	target = strings.TrimSpace(target)
	for _, value := range values {
		if strings.TrimSpace(value) == target {
			return true
		}
	}
	return false
}

func readSkillDirectory(root, directoryName string) (document, bool, error) {
	if !skillNamePattern.MatchString(directoryName) {
		return document{}, false, nil
	}
	path := filepath.Join(root, directoryName, "SKILL.md")
	if info, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			legacy := filepath.Join(root, directoryName, "skill.md")
			if _, legacyErr := os.Stat(legacy); legacyErr == nil {
				path = legacy
			} else {
				return document{}, false, nil
			}
		} else {
			return document{}, false, fmt.Errorf("stat skill %q: %w", directoryName, err)
		}
	} else if info.IsDir() {
		return document{}, false, fmt.Errorf("skill %q SKILL.md is a directory", directoryName)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return document{}, false, fmt.Errorf("read skill %q: %w", directoryName, err)
	}
	if len(data) > maxSkillFileBytes {
		return document{}, false, fmt.Errorf("skill %q exceeds %d bytes", directoryName, maxSkillFileBytes)
	}
	content := string(data)
	name, description := parseFrontmatter(content)
	if name == "" {
		name = directoryName
	}
	if !skillNamePattern.MatchString(name) {
		return document{}, false, fmt.Errorf("skill %q has invalid name %q", directoryName, name)
	}
	if description == "" {
		description = fallbackDescription(content)
	}
	return document{
		Info: Info{
			Name:        name,
			Description: truncateRunes(description, 512),
			SizeBytes:   len(data),
			Path:        path,
		},
		Content: content,
	}, true, nil
}

func parseFrontmatter(content string) (string, string) {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", ""
	}
	var name, description string
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "---" {
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(strings.Trim(strings.TrimSpace(value), `"'`))
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "name":
			name = value
		case "description":
			description = value
		}
	}
	return strings.TrimSpace(name), strings.TrimSpace(description)
}

func fallbackDescription(content string) string {
	for _, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "#"))
		if line != "" && line != "---" {
			return line
		}
	}
	return ""
}

func decodeToolArguments(data []byte, target any) error {
	if len(data) == 0 {
		data = []byte(`{}`)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return errors.New("skill tool arguments are invalid JSON")
	}
	return nil
}

func sanitizePrompt(value string) string {
	value = strings.Map(func(r rune) rune {
		if r < 0x20 || r == '`' {
			return ' '
		}
		return r
	}, value)
	return strings.Join(strings.Fields(value), " ")
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "..."
}
