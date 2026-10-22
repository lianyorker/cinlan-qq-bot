package persona

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Persona pack directory format v1 (docs/人格包格式.md):
//
//	persona-packs/
//	  <pack>/                      ← one directory per persona
//	    manifest.json              ← required: metadata + routing + voice
//	    system_prompt.md           ← prompt (or manifest.system_prompt inline)
//	    samples.md                 ← begin dialogs, human format (## user/## assistant)
//	    begin_dialogs.json         ← begin dialogs, lossless JSON alternative
//	    voice.json                 ← optional voice override {"voice","voice_rate"}
//	    knowledge/                 ← optional markdown/txt docs for knowledge wiring
//
// Manifest "default": true marks the default persona; without one the first
// pack (sorted) wins. enabled:false packs are skipped like personas.json.

const (
	// PackSchema marks manifest.json files written by this package; foreign
	// directories are never touched by pack persistence.
	PackSchema = "cinlan-persona-pack/v1"

	manifestFile    = "manifest.json"
	promptFile      = "system_prompt.md"
	samplesFile     = "samples.md"
	dialogsFile     = "begin_dialogs.json"
	voiceFile       = "voice.json"
	knowledgeDir    = "knowledge"
	maxManifestSize = 64 << 10
	maxSamplesSize  = 256 << 10
	maxPacks        = 128
)

// Pack is a persona profile plus its on-disk footprint.
type Pack struct {
	Profile      Profile
	Dir          string
	KnowledgeDir string // "" when the pack has no knowledge/ directory
}

// PackManifest is the manifest.json contract. Only metadata lives here;
// long-form text goes to the sibling files.
type PackManifest struct {
	Schema             string            `json:"schema"`
	Name               string            `json:"name"`
	Version            string            `json:"version,omitempty"`
	Description        string            `json:"description,omitempty"`
	Default            bool              `json:"default,omitempty"`
	Enabled            *bool             `json:"enabled,omitempty"`
	Voice              *PackVoice        `json:"voice,omitempty"`
	SystemPrompt       string            `json:"system_prompt,omitempty"`
	SystemPromptFile   string            `json:"system_prompt_file,omitempty"`
	RoutingScope       *RoutingScope     `json:"routing_scope,omitempty"`
	AffinityPrompts    map[string]string `json:"affinity_prompts,omitempty"`
	CustomErrorMessage string            `json:"custom_error_message,omitempty"`
}

// PackVoice carries voice settings; identical keys appear in voice.json.
type PackVoice struct {
	Voice     string  `json:"voice,omitempty"`
	VoiceRate float64 `json:"voice_rate,omitempty"`
}

// LoadPack reads one pack directory into a Profile. dir must contain
// manifest.json; everything else is optional.
func LoadPack(dir string) (Pack, error) {
	manifest, err := readManifest(dir)
	if err != nil {
		return Pack{}, err
	}
	if manifest.Schema != PackSchema {
		return Pack{}, fmt.Errorf("pack %q has unsupported schema %q", dir, manifest.Schema)
	}
	if manifest.Enabled != nil && !*manifest.Enabled {
		return Pack{Dir: dir}, errPackDisabled
	}

	profile := Profile{
		Name:               manifest.Name,
		Description:        manifest.Description,
		RoutingScope:       manifest.RoutingScope,
		AffinityPrompts:    manifest.AffinityPrompts,
		CustomErrorMessage: manifest.CustomErrorMessage,
		Default:            manifest.Default,
	}

	// Voice: voice.json overrides manifest.voice.
	voice := PackVoice{}
	if manifest.Voice != nil {
		voice = *manifest.Voice
	}
	if data, err := readLimited(filepath.Join(dir, voiceFile), maxManifestSize); err == nil {
		var override PackVoice
		if json.Unmarshal(data, &override) == nil {
			if override.Voice != "" {
				voice.Voice = override.Voice
			}
			if override.VoiceRate != 0 {
				voice.VoiceRate = override.VoiceRate
			}
		}
	} else if !os.IsNotExist(err) {
		return Pack{}, fmt.Errorf("read %s: %w", voiceFile, err)
	}
	profile.Voice = voice.Voice
	profile.VoiceRate = voice.VoiceRate

	// System prompt: manifest.system_prompt inline, else prompt file.
	if profile.SystemPrompt = strings.TrimSpace(manifest.SystemPrompt); profile.SystemPrompt == "" {
		name := manifest.SystemPromptFile
		if name == "" {
			name = promptFile
		}
		data, err := readLimited(filepath.Join(dir, name), maxSamplesSize)
		if err != nil {
			return Pack{}, fmt.Errorf("read %s: %w", name, err)
		}
		profile.SystemPrompt = string(data)
	}

	// Begin dialogs: begin_dialogs.json preferred, samples.md fallback.
	switch {
	case fileExists(filepath.Join(dir, dialogsFile)):
		data, err := readLimited(filepath.Join(dir, dialogsFile), maxSamplesSize)
		if err != nil {
			return Pack{}, fmt.Errorf("read %s: %w", dialogsFile, err)
		}
		if err := json.Unmarshal(data, &profile.BeginDialogs); err != nil {
			return Pack{}, fmt.Errorf("decode %s: %w", dialogsFile, err)
		}
	case fileExists(filepath.Join(dir, samplesFile)):
		data, err := readLimited(filepath.Join(dir, samplesFile), maxSamplesSize)
		if err != nil {
			return Pack{}, fmt.Errorf("read %s: %w", samplesFile, err)
		}
		dialogs, err := parseSamples(string(data))
		if err != nil {
			return Pack{}, fmt.Errorf("%s: %w", samplesFile, err)
		}
		profile.BeginDialogs = dialogs
	}

	profile, err = normalizeProfile(profile)
	if err != nil {
		return Pack{}, fmt.Errorf("pack %q: %w", dir, err)
	}

	pack := Pack{Profile: profile, Dir: dir}
	kdir := filepath.Join(dir, knowledgeDir)
	if info, err := os.Stat(kdir); err == nil && info.IsDir() {
		pack.KnowledgeDir = kdir
	}
	return pack, nil
}

var errPackDisabled = errors.New("pack is disabled")

// LoadPacksDir scans dir for pack directories (immediate children containing
// manifest.json) and returns them sorted by profile name plus the default
// name. A missing/empty dir yields (nil, "", nil).
func LoadPacksDir(dir string) ([]Pack, string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", nil
		}
		return nil, "", fmt.Errorf("read persona packs dir: %w", err)
	}
	packs := make([]Pack, 0, len(entries))
	defaultName := ""
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		sub := filepath.Join(dir, entry.Name())
		if !fileExists(filepath.Join(sub, manifestFile)) {
			continue // foreign directory, not a pack
		}
		if len(packs) >= maxPacks {
			return nil, "", fmt.Errorf("persona packs exceed %d", maxPacks)
		}
		pack, err := LoadPack(sub)
		if errors.Is(err, errPackDisabled) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		for _, existing := range packs {
			if existing.Profile.Name == pack.Profile.Name {
				return nil, "", fmt.Errorf("persona %q is defined by multiple packs", pack.Profile.Name)
			}
		}
		if pack.Profile.Default {
			defaultName = pack.Profile.Name
		}
		packs = append(packs, pack)
	}
	sort.Slice(packs, func(i, j int) bool { return packs[i].Profile.Name < packs[j].Profile.Name })
	if defaultName == "" && len(packs) > 0 {
		defaultName = packs[0].Profile.Name
	}
	return packs, defaultName, nil
}

// WritePack persists a Profile as a v1 pack directory under parent.
// begin_dialogs.json is the canonical on-disk dialog form; samples.md and
// voice.json are never overwritten (human/plugin-owned).
func WritePack(parent string, profile Profile, isDefault bool) error {
	dir := filepath.Join(parent, sanitizeDirName(profile.Name))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	manifest := PackManifest{
		Schema:             PackSchema,
		Name:               profile.Name,
		Description:        profile.Description,
		Default:            isDefault,
		RoutingScope:       profile.RoutingScope,
		AffinityPrompts:    profile.AffinityPrompts,
		CustomErrorMessage: profile.CustomErrorMessage,
	}
	if profile.Voice != "" || profile.VoiceRate != 0 {
		manifest.Voice = &PackVoice{Voice: profile.Voice, VoiceRate: profile.VoiceRate}
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(dir, manifestFile), ".manifest-*.tmp", data); err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(dir, promptFile), ".prompt-*.tmp",
		[]byte(strings.TrimSpace(profile.SystemPrompt)+"\n")); err != nil {
		return err
	}
	if len(profile.BeginDialogs) > 0 {
		data, err = json.MarshalIndent(profile.BeginDialogs, "", "  ")
		if err != nil {
			return err
		}
		if err := writeAtomic(filepath.Join(dir, dialogsFile), ".dialogs-*.tmp", data); err != nil {
			return err
		}
	}
	return nil
}

// parseSamples reads the human samples.md format:
//
//	## user
//	text…
//	## assistant
//	reply…
//
// Sections alternate user/assistant; "## 用户"/"## 助手" also accepted.
func parseSamples(content string) ([]Dialogue, error) {
	var dialogs []Dialogue
	var role, text strings.Builder
	flush := func() error {
		body := strings.TrimSpace(text.String())
		text.Reset()
		switch strings.ToLower(role.String()) {
		case "":
			if body != "" {
				return errors.New("content before the first ## user section")
			}
		case "user", "用户":
			if body == "" {
				return errors.New("empty user sample")
			}
			dialogs = append(dialogs, Dialogue{User: body})
		case "assistant", "助手":
			if body == "" {
				return errors.New("empty assistant sample")
			}
			if len(dialogs) == 0 || dialogs[len(dialogs)-1].Assistant != "" {
				return errors.New("assistant sample without a preceding user")
			}
			dialogs[len(dialogs)-1].Assistant = body
		default:
			return fmt.Errorf("unknown sample section %q", role.String())
		}
		role.Reset()
		return nil
	}

	scanner := bufio.NewScanner(strings.NewReader(content))
	scanner.Buffer(make([]byte, 64<<10), maxSamplesSize)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "## ") {
			if err := flush(); err != nil {
				return nil, err
			}
			role.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "## ")))
			continue
		}
		text.WriteString(line + "\n")
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if len(dialogs) == 0 {
		return nil, errors.New("samples.md contains no user/assistant sections")
	}
	if dialogs[len(dialogs)-1].Assistant == "" {
		return nil, errors.New("last user sample has no assistant reply")
	}
	return dialogs, nil
}

func readManifest(dir string) (PackManifest, error) {
	data, err := readLimited(filepath.Join(dir, manifestFile), maxManifestSize)
	if err != nil {
		return PackManifest{}, fmt.Errorf("read %s: %w", manifestFile, err)
	}
	var manifest PackManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return PackManifest{}, fmt.Errorf("decode %s: %w", manifestFile, err)
	}
	return manifest, nil
}

func readLimited(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := os.ReadFile(file.Name())
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes", filepath.Base(path), limit)
	}
	return data, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// sanitizeDirName maps a persona name to a filesystem-safe directory name,
// keeping non-ASCII characters (e.g. "菜包").
func sanitizeDirName(name string) string {
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) || r < 0x20 {
			return '_'
		}
		return r
	}, strings.TrimSpace(name))
}
