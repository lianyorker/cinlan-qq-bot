package binding

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	maxConfigBytes = 512 << 10
	maxBindings    = 256
)

type rawFile struct {
	Bindings []rawRule `json:"bindings"`
}

type rawRule struct {
	Name            string       `json:"name"`
	Platform        string       `json:"platform"`
	SelfID          string       `json:"self_id"`
	ChatType        string       `json:"chat_type"`
	ChatID          string       `json:"chat_id"`
	ChatIDs         []string     `json:"chat_ids"`
	UserIDs         []string     `json:"user_ids"`
	MemberGroups    []string     `json:"member_groups"`
	Persona         string       `json:"persona"`
	Provider        string       `json:"provider"`
	Tools           []string     `json:"tools"`
	Skills          []string     `json:"skills"`
	KnowledgeBases  []string     `json:"knowledge_bases"`
	MCPServers      []string     `json:"mcp_servers"`
	RequireMention  *bool        `json:"require_mention"`
	SmartAttention  *bool        `json:"smart_attention"`
	ReplyPolicy     *ReplyPolicy `json:"reply_policy"`
	LearningEnabled *bool        `json:"learning_enabled"`
	AllowLinks      *bool        `json:"allow_links"`
	Enabled         *bool        `json:"enabled"`
	// PromptFile scopes a business/persona prompt fragment to this
	// binding only — e.g. cinlan storefront rules must not leak into
	// zkmall chats. Relative paths resolve against the config dir.
	PromptFile string `json:"prompt_file"`
	// PromptFiles is the multi-file variant; contents join with prompt_file
	// in order so bindings can mix reusable rule files (rules/*.md).
	PromptFiles []string `json:"prompt_files"`
}

func LoadFile(path string) (*Registry, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("chat binding config path is empty")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open chat binding config: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxConfigBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read chat binding config: %w", err)
	}
	if len(data) > maxConfigBytes {
		return nil, fmt.Errorf("chat binding config exceeds %d bytes", maxConfigBytes)
	}
	var decoded rawFile
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, fmt.Errorf("decode chat binding config: %w", err)
	}
	if len(decoded.Bindings) == 0 {
		registry, err := NewRegistry(nil)
		if err != nil {
			return nil, err
		}
		registry.path = path
		return registry, nil
	}
	if len(decoded.Bindings) > maxBindings {
		return nil, fmt.Errorf("chat binding config contains more than %d bindings", maxBindings)
	}
	rules := make([]Rule, 0, len(decoded.Bindings))
	for _, raw := range decoded.Bindings {
		if raw.Enabled != nil && !*raw.Enabled {
			continue
		}
		// prompt_file (single, legacy) merges into prompt_files in order.
		promptNames := make([]string, 0, len(raw.PromptFiles)+1)
		if name := strings.TrimSpace(raw.PromptFile); name != "" {
			promptNames = append(promptNames, name)
		}
		for _, name := range raw.PromptFiles {
			if name = strings.TrimSpace(name); name != "" {
				promptNames = append(promptNames, name)
			}
		}
		promptParts := make([]string, 0, len(promptNames))
		for _, name := range promptNames {
			// Relative paths sit next to the bindings file so deploys
			// keep prompts and scoping in one data directory.
			resolved := name
			if !filepath.IsAbs(resolved) {
				resolved = filepath.Join(filepath.Dir(path), name)
			}
			body, readErr := os.ReadFile(resolved)
			if readErr != nil {
				return nil, fmt.Errorf("binding %q prompt_file %s: %w", raw.Name, name, readErr)
			}
			if part := strings.TrimSpace(string(body)); part != "" {
				promptParts = append(promptParts, part)
			}
		}
		prompt := strings.Join(promptParts, "\n\n")
		rules = append(rules, Rule{
			Name:            raw.Name,
			Platform:        raw.Platform,
			SelfID:          raw.SelfID,
			ChatType:        raw.ChatType,
			ChatID:          raw.ChatID,
			ChatIDs:         raw.ChatIDs,
			UserIDs:         raw.UserIDs,
			MemberGroups:    raw.MemberGroups,
			Persona:         raw.Persona,
			Provider:        raw.Provider,
			Tools:           raw.Tools,
			Skills:          raw.Skills,
			KnowledgeBases:  raw.KnowledgeBases,
			MCPServers:      raw.MCPServers,
			RequireMention:  raw.RequireMention,
			SmartAttention:  raw.SmartAttention,
			ReplyPolicy:     raw.ReplyPolicy,
			LearningEnabled: raw.LearningEnabled,
			AllowLinks:      raw.AllowLinks,
			PromptFile:      raw.PromptFile,
			PromptFiles:     promptNames,
			Prompt:          prompt,
		})
	}
	if len(rules) == 0 {
		return nil, errors.New("chat binding config contains no enabled bindings")
	}
	return NewRegistry(rules)
}
