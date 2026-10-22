package persona

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func makePack(t *testing.T, root, dirName, manifest string, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(root, dirName)
	writeTestFile(t, dir, manifestFile, manifest)
	for name, content := range files {
		writeTestFile(t, dir, name, content)
	}
	return dir
}

const alphaManifest = `{
  "schema": "cinlan-persona-pack/v1",
  "name": "alpha",
  "version": "1.0.0",
  "description": "test alpha",
  "default": true,
  "voice": {"voice": "zh-CN-XiaoyiNeural", "voice_rate": 1.1},
  "routing_scope": {"include_topics": ["售后"], "languages": ["zh-CN"]},
  "affinity_prompts": {"high": "热情"},
  "custom_error_message": "出错了"
}`

func TestLoadPackFull(t *testing.T) {
	root := t.TempDir()
	dir := makePack(t, root, "alpha-pack", alphaManifest, map[string]string{
		promptFile:  "  你是 alpha 助手。\n",
		samplesFile: "## user\n你好\n## assistant\n你好呀\n## 用户\n退货\n## 助手\n可以\n",
	})
	writeTestFile(t, filepath.Join(dir, knowledgeDir), "refund.md", "退货规则")

	pack, err := LoadPack(dir)
	if err != nil {
		t.Fatalf("LoadPack: %v", err)
	}
	p := pack.Profile
	if p.Name != "alpha" || p.Description != "test alpha" || !p.Default {
		t.Fatalf("profile = %+v", p)
	}
	if p.Voice != "zh-CN-XiaoyiNeural" || p.VoiceRate != 1.1 {
		t.Fatalf("voice = %q %v", p.Voice, p.VoiceRate)
	}
	if p.SystemPrompt != "你是 alpha 助手。" {
		t.Fatalf("prompt = %q", p.SystemPrompt)
	}
	if len(p.BeginDialogs) != 2 || p.BeginDialogs[0].Assistant != "你好呀" ||
		p.BeginDialogs[1].User != "退货" {
		t.Fatalf("dialogs = %+v", p.BeginDialogs)
	}
	if p.RoutingScope == nil || len(p.RoutingScope.IncludeTopics) != 1 {
		t.Fatalf("scope = %+v", p.RoutingScope)
	}
	if p.AffinityPrompts["high"] != "热情" || p.CustomErrorMessage != "出错了" {
		t.Fatalf("affinity/error = %+v %q", p.AffinityPrompts, p.CustomErrorMessage)
	}
	if pack.KnowledgeDir == "" {
		t.Fatal("knowledge dir not detected")
	}
}

func TestLoadPackVoiceJSONOverridesManifest(t *testing.T) {
	root := t.TempDir()
	dir := makePack(t, root, "v", `{"schema":"cinlan-persona-pack/v1","name":"v",
		"voice":{"voice":"manifest-voice"},"system_prompt":"p"}`, map[string]string{
		voiceFile: `{"voice":"file-voice","voice_rate":0.9}`,
	})
	pack, err := LoadPack(dir)
	if err != nil {
		t.Fatalf("LoadPack: %v", err)
	}
	if pack.Profile.Voice != "file-voice" || pack.Profile.VoiceRate != 0.9 {
		t.Fatalf("voice = %q %v", pack.Profile.Voice, pack.Profile.VoiceRate)
	}
}

func TestLoadPackDialogsJSONBeatsSamplesMD(t *testing.T) {
	root := t.TempDir()
	dir := makePack(t, root, "d", `{"schema":"cinlan-persona-pack/v1","name":"d","system_prompt":"p"}`,
		map[string]string{
			dialogsFile: `[{"user":"u","assistant":"a"}]`,
			samplesFile: "## user\nshould not be read\n## assistant\nx\n",
		})
	pack, err := LoadPack(dir)
	if err != nil {
		t.Fatalf("LoadPack: %v", err)
	}
	if len(pack.Profile.BeginDialogs) != 1 || pack.Profile.BeginDialogs[0].User != "u" {
		t.Fatalf("dialogs = %+v", pack.Profile.BeginDialogs)
	}
}

func TestLoadPacksDirOrderingDefaultAndSkips(t *testing.T) {
	root := t.TempDir()
	makePack(t, root, "z-last", `{"schema":"cinlan-persona-pack/v1","name":"beta","system_prompt":"p"}`, nil)
	makePack(t, root, "a-first", alphaManifest, map[string]string{promptFile: "alpha prompt"})
	makePack(t, root, "disabled", `{"schema":"cinlan-persona-pack/v1","name":"off","enabled":false,"system_prompt":"p"}`, nil)
	writeTestFile(t, filepath.Join(root, "foreign"), "README.txt", "not a pack")

	packs, def, err := LoadPacksDir(root)
	if err != nil {
		t.Fatalf("LoadPacksDir: %v", err)
	}
	if len(packs) != 2 || packs[0].Profile.Name != "alpha" || packs[1].Profile.Name != "beta" {
		t.Fatalf("packs = %+v", packs)
	}
	if def != "alpha" {
		t.Fatalf("default = %q", def)
	}
}

func TestLoadPacksDirErrors(t *testing.T) {
	root := t.TempDir()
	// duplicate persona name across packs
	makePack(t, root, "p1", `{"schema":"cinlan-persona-pack/v1","name":"dup","system_prompt":"p"}`, nil)
	makePack(t, root, "p2", `{"schema":"cinlan-persona-pack/v1","name":"dup","system_prompt":"p"}`, nil)
	if _, _, err := LoadPacksDir(root); err == nil || !strings.Contains(err.Error(), "multiple packs") {
		t.Fatalf("duplicate err = %v", err)
	}
	// bad schema
	root2 := t.TempDir()
	makePack(t, root2, "bad", `{"schema":"other/v9","name":"x","system_prompt":"p"}`, nil)
	if _, _, err := LoadPacksDir(root2); err == nil || !strings.Contains(err.Error(), "schema") {
		t.Fatalf("schema err = %v", err)
	}
	// missing dir → empty result, no error
	if packs, def, err := LoadPacksDir(filepath.Join(root2, "nope")); err != nil || packs != nil || def != "" {
		t.Fatalf("missing dir = %v %v %v", packs, def, err)
	}
}

func TestRegistryUsePacksDirPersistRoundTrip(t *testing.T) {
	root := t.TempDir()
	packsDir := filepath.Join(root, "persona-packs")
	makePack(t, packsDir, "alpha", alphaManifest, map[string]string{
		promptFile:  "alpha prompt",
		samplesFile: "## user\nhi\n## assistant\nyo\n",
	})
	makePack(t, packsDir, "beta", `{"schema":"cinlan-persona-pack/v1","name":"beta","system_prompt":"b"}`, nil)

	reg := NewRegistry()
	if err := reg.UsePacksDir(packsDir); err != nil {
		t.Fatalf("UsePacksDir: %v", err)
	}
	if reg.PacksDir() != packsDir || reg.Path() != "" {
		t.Fatalf("packsDir/path = %q %q", reg.PacksDir(), reg.Path())
	}
	if list := reg.List(); len(list) != 2 {
		t.Fatalf("list = %+v", list)
	}
	if p, ok := reg.Default(); !ok || p.Name != "alpha" {
		t.Fatalf("default = %+v", p)
	}

	// Register persists a NEW pack dir
	err := reg.Register(Profile{
		Name: "gamma", SystemPrompt: "g prompt",
		BeginDialogs: []Dialogue{{User: "u", Assistant: "a"}},
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if !fileExists(filepath.Join(packsDir, "gamma", manifestFile)) ||
		!fileExists(filepath.Join(packsDir, "gamma", promptFile)) ||
		!fileExists(filepath.Join(packsDir, "gamma", dialogsFile)) {
		t.Fatal("gamma pack files not persisted")
	}

	// Delete removes the pack dir; default is protected
	if _, err := reg.Delete("alpha"); err == nil {
		t.Fatal("default delete should fail")
	}
	if ok, err := reg.Delete("beta"); !ok || err != nil {
		t.Fatalf("delete beta: %v %v", ok, err)
	}
	if _, err := os.Stat(filepath.Join(packsDir, "beta")); !os.IsNotExist(err) {
		t.Fatal("beta pack dir not removed")
	}

	// SetDefault flips the manifest default flag
	if err := reg.SetDefault("gamma"); err != nil {
		t.Fatalf("SetDefault: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(packsDir, "gamma", manifestFile))
	var manifest PackManifest
	if err := json.Unmarshal(data, &manifest); err != nil || !manifest.Default {
		t.Fatalf("gamma manifest = %s", data)
	}

	// Reload from disk proves round-trip
	reg2 := NewRegistry()
	if err := reg2.UsePacksDir(packsDir); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if p, ok := reg2.Default(); !ok || p.Name != "gamma" {
		t.Fatalf("reloaded default = %+v", p)
	}
	if p, ok := reg2.Get("alpha"); !ok || len(p.BeginDialogs) != 1 {
		t.Fatalf("reloaded alpha = %+v", p)
	}
}

func TestRegistryUsePacksDirMissingIsNoop(t *testing.T) {
	reg := NewRegistry()
	if err := reg.UsePacksDir(filepath.Join(t.TempDir(), "absent")); err != nil {
		t.Fatalf("missing dir should be noop: %v", err)
	}
	if reg.PacksDir() != "" {
		t.Fatal("packsDir set on noop")
	}
}

func TestParseSamplesStrictness(t *testing.T) {
	if _, err := parseSamples("dangling text\n## user\nu\n## assistant\na\n"); err == nil {
		t.Fatal("content before first section should fail")
	}
	if _, err := parseSamples("## assistant\na\n"); err == nil {
		t.Fatal("assistant without user should fail")
	}
	if _, err := parseSamples("## user\nu\n"); err == nil {
		t.Fatal("unanswered user should fail")
	}
}
