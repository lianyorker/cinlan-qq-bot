package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadCustomAgentConfig(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("QQ_GROUP_ALLOWLIST", "123456, 789012")
	t.Setenv("QQ_PRIVATE_ALLOWLIST", "234567")
	t.Setenv("AGENT_API_MODE", "custom")
	t.Setenv("AGENT_API_URL", "http://127.0.0.1:9000/reply")
	t.Setenv("QQ_REQUIRE_MENTION", "false")
	t.Setenv("AGENT_MAX_RETRIES", "2")
	t.Setenv("SESSION_TTL", "2h")
	t.Setenv("SESSION_STORE_PATH", "data/sessions.json")
	t.Setenv("CRON_STORE_PATH", "data/cron.json")
	t.Setenv("MCP_SERVERS_FILE", "data/mcp.json")
	t.Setenv("SKILLS_DIR", "data/skills")
	t.Setenv("SUBAGENTS_FILE", "data/subagents.json")
	t.Setenv("PLUGINS_FILE", "data/plugins.json")
	t.Setenv("PROVIDERS_FILE", "data/providers.json")
	t.Setenv("FILE_CATALOG_PATH", "data/files.json")
	t.Setenv("FILE_DELIVERY_MAX_BYTES", "4096")
	t.Setenv("FILE_DELIVERY_TIMEOUT", "45s")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.GroupAllowlist.Allows("123456") || cfg.GroupAllowlist.Allows("999999") {
		t.Fatalf("unexpected group allowlist")
	}
	if !cfg.PrivateAllowlist.Allows("234567") || cfg.PrivateAllowlist.Allows("999999") {
		t.Fatalf("unexpected private allowlist")
	}
	if cfg.RequireMention {
		t.Fatalf("RequireMention = true, want false")
	}
	if cfg.AgentMaxRetries != 2 {
		t.Fatalf("AgentMaxRetries = %d, want 2", cfg.AgentMaxRetries)
	}
	if cfg.SessionTTL != 2*time.Hour {
		t.Fatalf("SessionTTL = %s, want 2h", cfg.SessionTTL)
	}
	if cfg.SessionStorePath == "" {
		t.Fatal("SessionStorePath is empty")
	}
	if cfg.CronStorePath != "data/cron.json" {
		t.Fatalf("CronStorePath = %q, want data/cron.json", cfg.CronStorePath)
	}
	if cfg.MCPServersFile != "data/mcp.json" {
		t.Fatalf("MCPServersFile = %q, want data/mcp.json", cfg.MCPServersFile)
	}
	if cfg.SkillsDir != "data/skills" {
		t.Fatalf("SkillsDir = %q, want data/skills", cfg.SkillsDir)
	}
	if cfg.SubagentsFile != "data/subagents.json" {
		t.Fatalf("SubagentsFile = %q, want data/subagents.json", cfg.SubagentsFile)
	}
	if cfg.PluginsFile != "data/plugins.json" {
		t.Fatalf("PluginsFile = %q, want data/plugins.json", cfg.PluginsFile)
	}
	if cfg.ProvidersFile != "data/providers.json" {
		t.Fatalf("ProvidersFile = %q, want data/providers.json", cfg.ProvidersFile)
	}
	if cfg.FileCatalogPath != "data/files.json" ||
		cfg.FileMaxBytes != 4096 ||
		cfg.FileSendTimeout != 45*time.Second {
		t.Fatalf(
			"file delivery config = %q/%d/%s",
			cfg.FileCatalogPath,
			cfg.FileMaxBytes,
			cfg.FileSendTimeout,
		)
	}
	if cfg.QQPlatform != QQPlatformNative {
		t.Fatalf("QQPlatform = %q, want native", cfg.QQPlatform)
	}
	if cfg.HTTPListenAddr != "127.0.0.1:18080" {
		t.Fatalf("HTTPListenAddr = %q, want loopback default", cfg.HTTPListenAddr)
	}
	if cfg.AdminUsername != "admin" || cfg.AdminPassword != "admin123" {
		t.Fatalf("admin defaults = %q/%q", cfg.AdminUsername, cfg.AdminPassword)
	}
	if !cfg.GroupAtSender {
		t.Fatal("GroupAtSender = false, want true")
	}
}

func TestLoadRequiresExplicitGroupAllowlist(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("AGENT_API_URL", "http://127.0.0.1:9000/reply")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "QQ_GROUP_ALLOWLIST") {
		t.Fatalf("Load() error = %v, want QQ_GROUP_ALLOWLIST error", err)
	}
}

func TestLoadOpenAIRequiresModel(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("QQ_GROUP_ALLOWLIST", "*")
	t.Setenv("AGENT_API_MODE", "openai")
	t.Setenv("AGENT_API_URL", "http://127.0.0.1:9000/v1/chat/completions")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "AGENT_MODEL") {
		t.Fatalf("Load() error = %v, want AGENT_MODEL error", err)
	}
}

func TestParseAllowlistWildcard(t *testing.T) {
	allowlist, err := ParseAllowlist("*,123")
	if err != nil {
		t.Fatalf("ParseAllowlist() error = %v", err)
	}
	if !allowlist.Wildcard() || !allowlist.Allows("anything") {
		t.Fatalf("wildcard allowlist did not allow arbitrary ID")
	}
}

func TestParseOptionalAllowlistAllowsEmpty(t *testing.T) {
	allowlist, err := ParseOptionalAllowlist("")
	if err != nil {
		t.Fatalf("ParseOptionalAllowlist() error = %v", err)
	}
	if allowlist.Wildcard() || allowlist.Allows("123") || allowlist.Size() != 0 {
		t.Fatalf("empty optional allowlist = %#v", allowlist)
	}
}

func TestLoadRejectsInvalidPrivateAllowlist(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("QQ_GROUP_ALLOWLIST", "*")
	t.Setenv("QQ_PRIVATE_ALLOWLIST", "not-an-id")
	t.Setenv("AGENT_API_URL", "http://127.0.0.1:9000/reply")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "QQ_PRIVATE_ALLOWLIST") {
		t.Fatalf("Load() error = %v, want QQ_PRIVATE_ALLOWLIST error", err)
	}
}

func TestLoadAllowsDisablingSessionPersistence(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("QQ_GROUP_ALLOWLIST", "*")
	t.Setenv("AGENT_API_URL", "http://127.0.0.1:9000/reply")
	t.Setenv("SESSION_STORE_PATH", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.SessionStorePath != "" {
		t.Fatalf("SessionStorePath = %q, want empty", cfg.SessionStorePath)
	}
}

func TestLoadReverseWebSocketDoesNotRequireForwardURL(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("QQ_GROUP_ALLOWLIST", "*")
	t.Setenv("AGENT_API_URL", "http://127.0.0.1:9000/reply")
	t.Setenv("QQ_PLATFORM", "onebot")
	t.Setenv("ONEBOT_TRANSPORT", "reverse_ws")
	t.Setenv("ONEBOT_WS_URL", "not-used")
	t.Setenv("ONEBOT_REVERSE_LISTEN_ADDR", "127.0.0.1:3100")
	t.Setenv("ONEBOT_REVERSE_PATH", "/events")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.OneBotTransport != "reverse_ws" || cfg.OneBotListenAddr != "127.0.0.1:3100" {
		t.Fatalf("reverse config = %#v", cfg)
	}
}

func TestLoadHTTPSSEUsesHTTPURL(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("QQ_GROUP_ALLOWLIST", "*")
	t.Setenv("AGENT_API_URL", "http://127.0.0.1:9000/reply")
	t.Setenv("QQ_PLATFORM", "onebot")
	t.Setenv("ONEBOT_TRANSPORT", "http_sse")
	t.Setenv("ONEBOT_WS_URL", "not-used")
	t.Setenv("ONEBOT_HTTP_URL", "http://127.0.0.1:3000")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.OneBotTransport != "http_sse" || cfg.OneBotHTTPURL != "http://127.0.0.1:3000" {
		t.Fatalf("HTTP SSE config = %#v", cfg)
	}
}

func TestLoadReverseHTTPUsesHTTPURLAndListener(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("QQ_GROUP_ALLOWLIST", "*")
	t.Setenv("AGENT_API_URL", "http://127.0.0.1:9000/reply")
	t.Setenv("QQ_PLATFORM", "onebot")
	t.Setenv("ONEBOT_TRANSPORT", "reverse_http")
	t.Setenv("ONEBOT_HTTP_URL", "http://127.0.0.1:3000")
	t.Setenv("ONEBOT_REVERSE_LISTEN_ADDR", "127.0.0.1:3100")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.OneBotTransport != "reverse_http" ||
		cfg.OneBotReversePath != "/onebot/v11/events" ||
		cfg.OneBotHTTPURL != "http://127.0.0.1:3000" {
		t.Fatalf("reverse HTTP config = %#v", cfg)
	}
}

func TestLoadProviderFileDoesNotRequireSingleAgentURL(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("QQ_GROUP_ALLOWLIST", "*")
	t.Setenv("PROVIDERS_FILE", "data/providers.json")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ProvidersFile != "data/providers.json" {
		t.Fatalf("ProvidersFile = %q", cfg.ProvidersFile)
	}
}

func TestLoadRejectsNonLoopbackNativeIPC(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("QQ_GROUP_ALLOWLIST", "*")
	t.Setenv("AGENT_API_URL", "http://127.0.0.1:9000/reply")
	t.Setenv("QQNT_IPC_LISTEN_ADDR", "0.0.0.0:18081")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("Load() error = %v, want loopback error", err)
	}
}

func clearConfigEnvironment(t *testing.T) {
	t.Helper()
	keys := []string{
		"HTTP_LISTEN_ADDR",
		"LOG_LEVEL",
		"ADMIN_API_TOKEN",
		"ADMIN_USERNAME",
		"ADMIN_PASSWORD",
		"QQ_PLATFORM",
		"QQNT_PATH",
		"QQNT_AUTO_LAUNCH",
		"QQNT_ALLOW_RUNNING",
		"QQNT_LOADER_PATH",
		"QQNT_HOOK_PATH",
		"QQNT_LOAD_PATH",
		"QQNT_RUNTIME_PATH",
		"QQNT_PATCH_PACKAGE_PATH",
		"QQNT_IPC_LISTEN_ADDR",
		"QQNT_IPC_TOKEN",
		"QQNT_ACTION_TIMEOUT",
		"QQNT_HANDSHAKE_TIMEOUT",
		"QQNT_MAX_FRAME_BYTES",
		"ONEBOT_WS_URL",
		"ONEBOT_HTTP_URL",
		"ONEBOT_ACCESS_TOKEN",
		"ONEBOT_ACTION_TIMEOUT",
		"ONEBOT_TRANSPORT",
		"ONEBOT_REVERSE_LISTEN_ADDR",
		"ONEBOT_REVERSE_PATH",
		"ONEBOT_ACCOUNTS_FILE",
		"QQ_GROUP_ALLOWLIST",
		"QQ_PRIVATE_ALLOWLIST",
		"QQ_REQUIRE_MENTION",
		"QQ_QUOTE_REPLY",
		"QQ_GROUP_AT_SENDER",
		"QQ_MAX_REPLY_RUNES",
		"QQ_MAX_REPLY_CHUNKS",
		"AGENT_API_MODE",
		"AGENT_API_URL",
		"AGENT_API_KEY",
		"AGENT_MODEL",
		"AGENT_SYSTEM_PROMPT",
		"AGENT_AUTH_HEADER",
		"AGENT_AUTH_SCHEME",
		"AGENT_TIMEOUT",
		"AGENT_MAX_RETRIES",
		"AGENT_MAX_TOOL_ROUNDS",
		"AGENT_RETRY_BASE",
		"AGENT_RETRY_MAX",
		"SESSION_MAX_HISTORY",
		"SESSION_TTL",
		"SESSION_STORE_PATH",
		"CRON_STORE_PATH",
		"KNOWLEDGE_DIR",
		"KNOWLEDGE_TOP_K",
		"MCP_SERVERS_FILE",
		"SKILLS_DIR",
		"SUBAGENTS_FILE",
		"PLUGINS_FILE",
		"PROVIDERS_FILE",
		"PERSONAS_FILE",
		"CHAT_BINDINGS_FILE",
		"FILE_CATALOG_PATH",
		"FILE_DELIVERY_MAX_BYTES",
		"FILE_DELIVERY_TIMEOUT",
		"BOT_MAX_CONCURRENCY",
		"BOT_USER_COOLDOWN",
		"BOT_MESSAGE_DEDUPE_TTL",
	}
	for _, key := range keys {
		t.Setenv(key, "")
	}
}
