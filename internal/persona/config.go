package persona

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	maxConfigBytes = 512 << 10
	maxProfiles    = 128
)

type rawFile struct {
	DefaultPersona string       `json:"default_persona"`
	Personas       []rawProfile `json:"personas"`
}

type rawProfile struct {
	Name               string     `json:"name"`
	Description        string     `json:"description"`
	SystemPrompt       string     `json:"system_prompt"`
	BeginDialogs       []Dialogue `json:"begin_dialogs"`
	CustomErrorMessage string     `json:"custom_error_message"`
	Enabled            *bool      `json:"enabled"`
}

func LoadFile(path string) ([]Profile, string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, "", errors.New("persona config path is empty")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, "", fmt.Errorf("open persona config: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxConfigBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("read persona config: %w", err)
	}
	if len(data) > maxConfigBytes {
		return nil, "", fmt.Errorf("persona config exceeds %d bytes", maxConfigBytes)
	}
	var decoded rawFile
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, "", fmt.Errorf("decode persona config: %w", err)
	}
	if len(decoded.Personas) == 0 {
		return nil, "", errors.New("persona config contains no personas")
	}
	if len(decoded.Personas) > maxProfiles {
		return nil, "", fmt.Errorf("persona config contains more than %d personas", maxProfiles)
	}
	profiles := make([]Profile, 0, len(decoded.Personas))
	seen := make(map[string]struct{}, len(decoded.Personas))
	for _, raw := range decoded.Personas {
		if raw.Enabled != nil && !*raw.Enabled {
			continue
		}
		profile := Profile{
			Name:               strings.TrimSpace(raw.Name),
			Description:        strings.TrimSpace(raw.Description),
			SystemPrompt:       strings.TrimSpace(raw.SystemPrompt),
			BeginDialogs:       raw.BeginDialogs,
			CustomErrorMessage: strings.TrimSpace(raw.CustomErrorMessage),
		}
		profile, err = normalizeProfile(profile)
		if err != nil {
			return nil, "", err
		}
		if _, exists := seen[profile.Name]; exists {
			return nil, "", fmt.Errorf("persona %q is duplicated", profile.Name)
		}
		seen[profile.Name] = struct{}{}
		profiles = append(profiles, profile)
	}
	if len(profiles) == 0 {
		return nil, "", errors.New("persona config contains no enabled personas")
	}
	defaultName := strings.TrimSpace(decoded.DefaultPersona)
	if defaultName == "" {
		defaultName = profiles[0].Name
	}
	if _, exists := seen[defaultName]; !exists {
		return nil, "", fmt.Errorf("default persona %q is not enabled", defaultName)
	}
	return profiles, defaultName, nil
}
