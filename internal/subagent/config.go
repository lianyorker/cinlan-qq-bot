package subagent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/lianyorker/cinlan-qq-bot/internal/tool"
)

const maxConfigBytes = 512 << 10

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,47}$`)

type Config struct {
	Name         string
	Description  string
	SystemPrompt string
	Active       bool
	Permission   tool.Permission
	Tools        []string
	Provider     string
}

type fileConfig struct {
	Agents               []rawConfig `json:"agents"`
	SubagentOrchestrator *struct {
		Agents []rawConfig `json:"agents"`
	} `json:"subagent_orchestrator"`
}

type rawConfig struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	SystemPrompt string          `json:"system_prompt"`
	Active       *bool           `json:"active"`
	Enabled      *bool           `json:"enabled"`
	Permission   tool.Permission `json:"permission"`
	Tools        []string        `json:"tools"`
	Provider     string          `json:"provider"`
}

func LoadFile(path string) ([]Config, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("subagent config path is empty")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read subagent config: %w", err)
	}
	if len(data) > maxConfigBytes {
		return nil, fmt.Errorf("subagent config exceeds %d bytes", maxConfigBytes)
	}
	var decoded fileConfig
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, fmt.Errorf("decode subagent config: %w", err)
	}
	rawAgents := decoded.Agents
	if decoded.SubagentOrchestrator != nil {
		if len(rawAgents) > 0 && len(decoded.SubagentOrchestrator.Agents) > 0 {
			return nil, errors.New("subagent config must use either agents or subagent_orchestrator.agents")
		}
		rawAgents = decoded.SubagentOrchestrator.Agents
	}
	if len(rawAgents) > 64 {
		return nil, errors.New("subagent config contains more than 64 agents")
	}
	result := make([]Config, 0, len(rawAgents))
	for _, raw := range rawAgents {
		config, err := resolveConfig(raw)
		if err != nil {
			return nil, err
		}
		result = append(result, config)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func resolveConfig(raw rawConfig) (Config, error) {
	name := strings.TrimSpace(raw.Name)
	if !namePattern.MatchString(name) {
		return Config{}, fmt.Errorf("subagent name %q is invalid", name)
	}
	description := truncate(strings.TrimSpace(raw.Description), 512)
	prompt := truncate(strings.TrimSpace(raw.SystemPrompt), 16000)
	if prompt == "" {
		prompt = description
	}
	if prompt == "" {
		return Config{}, fmt.Errorf("subagent %q requires system_prompt or description", name)
	}
	active := true
	if raw.Active != nil {
		active = *raw.Active
	}
	if raw.Enabled != nil {
		if raw.Active != nil && *raw.Active != *raw.Enabled {
			return Config{}, fmt.Errorf("subagent %q sets conflicting active and enabled values", name)
		}
		active = *raw.Enabled
	}
	permission := raw.Permission
	if permission == "" {
		permission = tool.PermissionEveryone
	}
	if permission != tool.PermissionEveryone && permission != tool.PermissionAdmin {
		return Config{}, fmt.Errorf("subagent %q has unsupported permission %q", name, permission)
	}
	seenTools := make(map[string]struct{}, len(raw.Tools))
	tools := make([]string, 0, len(raw.Tools))
	for _, item := range raw.Tools {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, exists := seenTools[item]; exists {
			continue
		}
		seenTools[item] = struct{}{}
		tools = append(tools, item)
	}
	sort.Strings(tools)
	return Config{
		Name:         name,
		Description:  description,
		SystemPrompt: prompt,
		Active:       active,
		Permission:   permission,
		Tools:        tools,
		Provider:     strings.TrimSpace(raw.Provider),
	}, nil
}

func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "..."
}
