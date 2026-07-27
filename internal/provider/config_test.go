package provider

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadFileResolvesMultipleProviders(t *testing.T) {
	t.Setenv("PRIMARY_API_KEY", "secret")
	path := filepath.Join(t.TempDir(), "providers.json")
	data := []byte(`{
		"default_provider": "primary",
		"providers": [
			{
				"name": "primary",
				"mode": "openai",
				"url": "https://api.example.com/v1/chat/completions",
				"api_key_env": "PRIMARY_API_KEY",
				"model": "model-a",
				"timeout": "45s",
				"max_retries": 2
			},
			{
				"name": "fallback",
				"mode": "custom",
				"url": "http://127.0.0.1:9000/reply"
			},
			{
				"name": "disabled",
				"enabled": false
			}
		]
	}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	configs, defaultName, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if len(configs) != 3 || defaultName != "primary" {
		t.Fatalf("configs = %#v, default=%q", configs, defaultName)
	}
	for _, config := range configs {
		if config.Name != "primary" {
			continue
		}
		if config.Agent.APIKey != "secret" || config.Agent.Model != "model-a" ||
			config.Agent.Timeout != 45*time.Second || config.Agent.MaxRetries != 2 {
			t.Fatalf("primary = %#v", config)
		}
	}
}

func TestLoadFileRejectsMissingDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "providers.json")
	data := []byte(`{
		"default_provider": "missing",
		"providers": [{
			"name": "primary",
			"mode": "custom",
			"url": "http://127.0.0.1:9000/reply"
		}]
	}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, _, err := LoadFile(path); err == nil {
		t.Fatal("LoadFile() error = nil")
	}
}
