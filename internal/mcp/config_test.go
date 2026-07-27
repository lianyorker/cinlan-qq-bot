package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lianyorker/cinlan-qq-bot/internal/tool"
)

func TestLoadFileResolvesHeadersAndDefaultsRemotePermission(t *testing.T) {
	t.Setenv("CRM_MCP_AUTH", "Bearer test-token")
	path := filepath.Join(t.TempDir(), "mcp.json")
	data := `{
		"mcpServers": {
			"crm": {
				"url": "https://mcp.example.test/mcp",
				"header_env": {"Authorization": "CRM_MCP_AUTH"},
				"allow_tools": ["search_customer"]
			}
		}
	}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	configs, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if len(configs) != 1 {
		t.Fatalf("configs = %#v", configs)
	}
	config := configs[0]
	if !config.Active || config.Permission != tool.PermissionAdmin {
		t.Fatalf("config defaults = %#v", config)
	}
	if config.Headers["Authorization"] != "Bearer test-token" {
		t.Fatalf("resolved headers = %#v", config.Headers)
	}
	if _, ok := config.AllowTools["search_customer"]; !ok {
		t.Fatalf("allow_tools = %#v", config.AllowTools)
	}
}

func TestLoadFileRejectsUnsafeTransportAndHeaders(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "stdio",
			body: `{"mcpServers":{"x":{"command":"node","args":["server.js"]}}}`,
			want: "stdio",
		},
		{
			name: "reserved header",
			body: `{"mcpServers":{"x":{"url":"https://example.test/mcp","headers":{"MCP-Session-Id":"bad"}}}}`,
			want: "reserved",
		},
		{
			name: "missing env",
			body: `{"mcpServers":{"x":{"url":"https://example.test/mcp","header_env":{"Authorization":"NOT_SET_CINLAN"}}}}`,
			want: "environment variable",
		},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "mcp.json")
			if err := os.WriteFile(path, []byte(current.body), 0o600); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			_, err := LoadFile(path)
			if err == nil || !strings.Contains(err.Error(), current.want) {
				t.Fatalf("LoadFile() error = %v, want %q", err, current.want)
			}
		})
	}
}
