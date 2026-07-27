package subagent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadFileSupportsAstrBotStyleWrapper(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subagents.json")
	data := `{
		"subagent_orchestrator": {
			"agents": [{
				"name": "after_sales",
				"description": "处理售后",
				"system_prompt": "你是售后专员",
				"permission": "admin",
				"provider": "backup",
				"tools": ["read_skill", "read_skill"]
			}]
		}
	}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	configs, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if len(configs) != 1 || configs[0].Name != "after_sales" ||
		len(configs[0].Tools) != 1 || configs[0].Permission != "admin" ||
		configs[0].Provider != "backup" {
		t.Fatalf("configs = %#v", configs)
	}
}

func TestLoadFileRejectsInvalidAgent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subagents.json")
	if err := os.WriteFile(path, []byte(`{"agents":[{"name":"bad name","description":"x"}]}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	_, err := LoadFile(path)
	if err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("LoadFile() error = %v, want invalid name", err)
	}
}
