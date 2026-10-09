package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadCustomAgentConfig(t *testing.T) {
	clearConfigEnvironment(t)
	imageRootA := t.TempDir()
	imageRootB := t.TempDir()
	t.Setenv("QQ_GROUP_ALLOWLIST", "123456, 789012")
	t.Setenv("QQ_PRIVATE_ALLOWLIST", "234567")
	t.Setenv("AGENT_API_MODE", "custom")
	t.Setenv("AGENT_API_URL", "http://127.0.0.1:9000/reply")
	t.Setenv("QQ_REQUIRE_MENTION", "false")
	t.Setenv("QQ_GROUP_BATCH_WINDOW", "750ms")
	t.Setenv("QQ_REPLY_PART_DELAY", "250ms")
	t.Setenv("BOT_USER_COOLDOWN", "3s")
	t.Setenv("BOT_USER_RATE_LIMIT", "7")
	t.Setenv("BOT_USER_RATE_WINDOW", "2m")
	t.Setenv("MEDIA_TOOL_USER_COOLDOWN", "20s")
	t.Setenv("MEDIA_TOOL_USER_LIMIT", "5")
	t.Setenv("MEDIA_TOOL_WINDOW", "30m")
	t.Setenv("MEDIA_TOOL_MAX_CONCURRENCY", "3")
	t.Setenv("AGENT_MAX_RETRIES", "2")
	t.Setenv("QQNT_IMAGE_ALLOWED_ROOTS", imageRootA+";"+imageRootB)
	t.Setenv("QQNT_IMAGE_MAX_BYTES", "8192")
	t.Setenv("SESSION_TTL", "2h")
	t.Setenv("SESSION_STORE_PATH", "data/sessions.db")
	t.Setenv("SESSION_ENCRYPTION_KEY", "QUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUE=")
	t.Setenv("CRON_STORE_PATH", "data/cron.json")
	t.Setenv("MCP_SERVERS_FILE", "data/mcp.json")
	t.Setenv("SKILLS_DIR", "data/skills")
	t.Setenv("SUBAGENTS_FILE", "data/subagents.json")
	t.Setenv("PLUGINS_FILE", "data/plugins.json")
	t.Setenv("PROVIDERS_FILE", "data/providers.json")
	t.Setenv("FILE_CATALOG_PATH", "data/files.json")
	t.Setenv("FILE_DELIVERY_MAX_BYTES", "4096")
	t.Setenv("FILE_DELIVERY_TIMEOUT", "45s")
	t.Setenv("BOT_ALLOWED_ROOT", `D:\workspace\bot`)
	t.Setenv("SECURITY_INCIDENT_STORE_PATH", "data/security.json")

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
	if cfg.GroupBatchWindow != 750*time.Millisecond ||
		cfg.ReplyPartDelay != 250*time.Millisecond {
		t.Fatalf(
			"humanized timing = %s/%s",
			cfg.GroupBatchWindow,
			cfg.ReplyPartDelay,
		)
	}
	if cfg.UserCooldown != 3*time.Second ||
		cfg.UserRateLimit != 7 ||
		cfg.UserRateWindow != 2*time.Minute ||
		cfg.MediaToolCooldown != 20*time.Second ||
		cfg.MediaToolLimit != 5 ||
		cfg.MediaToolWindow != 30*time.Minute ||
		cfg.MediaToolConcurrency != 3 {
		t.Fatalf(
			"rate limits = %s/%d/%s/%s/%d/%s/%d",
			cfg.UserCooldown,
			cfg.UserRateLimit,
			cfg.UserRateWindow,
			cfg.MediaToolCooldown,
			cfg.MediaToolLimit,
			cfg.MediaToolWindow,
			cfg.MediaToolConcurrency,
		)
	}
	if cfg.AgentMaxRetries != 2 {
		t.Fatalf("AgentMaxRetries = %d, want 2", cfg.AgentMaxRetries)
	}
	if len(cfg.QQNTImageAllowedRoots) != 2 ||
		cfg.QQNTImageAllowedRoots[0] != imageRootA ||
		cfg.QQNTImageAllowedRoots[1] != imageRootB ||
		cfg.QQNTImageMaxBytes != 8192 {
		t.Fatalf(
			"QQNT image config = %#v/%d",
			cfg.QQNTImageAllowedRoots,
			cfg.QQNTImageMaxBytes,
		)
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
	if cfg.BotAllowedRoot != `D:\workspace\bot` ||
		cfg.SecurityStorePath != "data/security.json" {
		t.Fatalf(
			"security config = %q/%q",
			cfg.BotAllowedRoot,
			cfg.SecurityStorePath,
		)
	}
	if cfg.QQPlatform != QQPlatformNative {
		t.Fatalf("QQPlatform = %q, want native", cfg.QQPlatform)
	}
	if cfg.HTTPListenAddr != "127.0.0.1:18080" {
		t.Fatalf("HTTPListenAddr = %q, want loopback default", cfg.HTTPListenAddr)
	}
	if !cfg.GroupAtSender {
		t.Fatal("GroupAtSender = false, want true")
	}
}

func TestNativeHeadlessFlags(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("QQ_GROUP_ALLOWLIST", "123")
	t.Setenv("AGENT_API_URL", "http://127.0.0.1:9000/reply")
	base, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if base.QQNTHeadless || base.QQNTAutoAcceptFriend {
		t.Fatal("unsafe enabled default")
	}
	t.Setenv("QQNT_HEADLESS", "true")
	t.Setenv("QQNT_AUTO_ACCEPT_FRIEND", "true")
	t.Setenv("ADMIN_API_TOKEN", "test-token")
	cfg, err := Load()
	if err != nil || !cfg.QQNTHeadless || !cfg.QQNTAutoAcceptFriend {
		t.Fatalf("flags = %#v, %v", cfg, err)
	}
	t.Setenv("QQNT_HEADLESS", "invalid")
	if _, err := Load(); err == nil {
		t.Fatal("accepted invalid bool")
	}
	cfg.AdminAPIToken = ""
	cfg.QQPlatform = QQPlatformNative
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "ADMIN_API_TOKEN") {
		t.Fatalf("headless without admin token = %v", err)
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
		"QQ_PLATFORM",
		"QQNT_PATH",
		"QQNT_AUTO_LAUNCH",
		"QQNT_ALLOW_RUNNING",
		"QQNT_AUTO_ACCEPT_FRIEND",
		"QQNT_HEADLESS",
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
		"QQNT_IMAGE_ALLOWED_ROOTS",
		"QQNT_IMAGE_MAX_BYTES",
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
		"QQ_GROUP_BATCH_WINDOW",
		"QQ_REPLY_PART_DELAY",
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
		"IMAGE_API_MODE",
		"IMAGE_API_URL",
		"IMAGE_API_KEY",
		"IMAGE_MODEL",
		"IMAGE_OUTPUT_DIR",
		"IMAGE_MAX_BYTES",
		"IMAGE_TIMEOUT",
		"IMAGE_RETENTION",
		"IMAGE_ENHANCE_PROMPT",
		"WEB_SCREENSHOT_ENABLED",
		"WEB_SCREENSHOT_BROWSER_PATH",
		"WEB_SCREENSHOT_ALLOWED_HOSTS",
		"WEB_SCREENSHOT_OUTPUT_DIR",
		"WEB_SCREENSHOT_MAX_BYTES",
		"WEB_SCREENSHOT_TIMEOUT",
		"WEB_SCREENSHOT_RETENTION",
		"WEB_SCREENSHOT_WIDTH",
		"WEB_SCREENSHOT_HEIGHT",
		"WEB_SCREENSHOT_WAIT",
		"SESSION_MAX_HISTORY",
		"SESSION_TTL",
		"SESSION_STORE_PATH",
		"SESSION_LEGACY_STORE_PATH",
		"SESSION_ENCRYPTION_KEY",
		"SESSION_COMPRESSION_THRESHOLD",
		"SESSION_COMPRESSION_RETAIN",
		"SESSION_LEARNING_ENABLED",
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
		"BOT_ALLOWED_ROOT",
		"SECURITY_INCIDENT_STORE_PATH",
		"BOT_MAX_CONCURRENCY",
		"BOT_USER_COOLDOWN",
		"BOT_USER_RATE_LIMIT",
		"BOT_USER_RATE_WINDOW",
		"BOT_ATTENTION_TIMEOUT",
		"BOT_ATTENTION_RATE_LIMIT",
		"BOT_ATTENTION_RATE_WINDOW",
		"MEDIA_TOOL_USER_COOLDOWN",
		"MEDIA_TOOL_USER_LIMIT",
		"MEDIA_TOOL_WINDOW",
		"MEDIA_TOOL_MAX_CONCURRENCY",
		"BOT_MESSAGE_DEDUPE_TTL",
	}
	for _, key := range keys {
		t.Setenv(key, "")
	}
}

func TestLoadImageGenerationConfig(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("QQ_GROUP_ALLOWLIST", "123456")
	t.Setenv("AGENT_API_MODE", "openai")
	t.Setenv("AGENT_API_URL", "https://api.example.test/v1/chat/completions")
	t.Setenv("AGENT_MODEL", "gpt-test")
	t.Setenv("IMAGE_API_MODE", "openai")
	t.Setenv("IMAGE_OUTPUT_DIR", "data/generated")
	t.Setenv("IMAGE_MAX_BYTES", "4096")
	t.Setenv("IMAGE_TIMEOUT", "45s")
	t.Setenv("IMAGE_RETENTION", "2h")
	t.Setenv("SESSION_STORE_PATH", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ImageAPIURL != "https://api.example.test/v1/images/generations" ||
		cfg.ImageModel != "gpt-image-1" ||
		cfg.ImageOutputDir != "data/generated" ||
		cfg.ImageMaxBytes != 4096 ||
		cfg.ImageTimeout != 45*time.Second ||
		cfg.ImageRetention != 2*time.Hour {
		t.Fatalf("image config = %#v", cfg)
	}
	if !cfg.ImageEnhance {
		t.Fatal("ImageEnhance = false, want true by default")
	}
}

func TestLoadWebScreenshotConfig(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("QQ_GROUP_ALLOWLIST", "*")
	t.Setenv("AGENT_API_URL", "http://127.0.0.1:9000/reply")
	t.Setenv("WEB_SCREENSHOT_ENABLED", "true")
	t.Setenv("WEB_SCREENSHOT_BROWSER_PATH", `C:\Program Files\Google\Chrome\Application\chrome.exe`)
	t.Setenv("WEB_SCREENSHOT_ALLOWED_HOSTS", "example.com,github.com")
	t.Setenv("WEB_SCREENSHOT_OUTPUT_DIR", "data/screenshots")
	t.Setenv("WEB_SCREENSHOT_MAX_BYTES", "4096")
	t.Setenv("WEB_SCREENSHOT_TIMEOUT", "40s")
	t.Setenv("WEB_SCREENSHOT_RETENTION", "3h")
	t.Setenv("WEB_SCREENSHOT_WIDTH", "1280")
	t.Setenv("WEB_SCREENSHOT_HEIGHT", "720")
	t.Setenv("WEB_SCREENSHOT_WAIT", "2s")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.WebScreenshotEnabled ||
		cfg.WebScreenshotBrowserPath == "" ||
		len(cfg.WebScreenshotAllowedHosts) != 2 ||
		cfg.WebScreenshotOutputDir != "data/screenshots" ||
		cfg.WebScreenshotMaxBytes != 4096 ||
		cfg.WebScreenshotTimeout != 40*time.Second ||
		cfg.WebScreenshotRetention != 3*time.Hour ||
		cfg.WebScreenshotWidth != 1280 ||
		cfg.WebScreenshotHeight != 720 ||
		cfg.WebScreenshotWait != 2*time.Second {
		t.Fatalf("web screenshot config = %#v", cfg)
	}
}

func TestLoadAttentionConfig(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("QQ_GROUP_ALLOWLIST", "123456")
	t.Setenv("AGENT_API_URL", "http://127.0.0.1:9000/reply")
	t.Setenv("SESSION_STORE_PATH", "")
	t.Setenv("BOT_ATTENTION_TIMEOUT", "4s")
	t.Setenv("BOT_ATTENTION_RATE_LIMIT", "12")
	t.Setenv("BOT_ATTENTION_RATE_WINDOW", "2m")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AttentionTimeout != 4*time.Second ||
		cfg.AttentionRateLimit != 12 || cfg.AttentionRateWindow != 2*time.Minute {
		t.Fatalf("attention config = %s/%d/%s", cfg.AttentionTimeout, cfg.AttentionRateLimit, cfg.AttentionRateWindow)
	}
}
