package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	AgentModeCustom  = "custom"
	AgentModeOpenAI  = "openai"
	QQPlatformNative = "native"
	QQPlatformOneBot = "onebot"
)

type Allowlist struct {
	wildcard bool
	ids      map[string]struct{}
}

func ParseAllowlist(value string) (Allowlist, error) {
	return parseAllowlist(value, true)
}

func ParseOptionalAllowlist(value string) (Allowlist, error) {
	return parseAllowlist(value, false)
}

func parseAllowlist(value string, required bool) (Allowlist, error) {
	result := Allowlist{ids: make(map[string]struct{})}
	for _, item := range strings.Split(value, ",") {
		id := strings.TrimSpace(item)
		if id == "" {
			continue
		}
		if id == "*" {
			result.wildcard = true
			continue
		}
		if _, err := strconv.ParseUint(id, 10, 64); err != nil {
			return Allowlist{}, fmt.Errorf("invalid QQ ID %q", id)
		}
		result.ids[id] = struct{}{}
	}
	if required && !result.wildcard && len(result.ids) == 0 {
		return Allowlist{}, errors.New("at least one QQ group ID is required; use * only when all groups are intended")
	}
	return result, nil
}

func (a Allowlist) Allows(id string) bool {
	if a.wildcard {
		return true
	}
	_, ok := a.ids[id]
	return ok
}

func (a Allowlist) Wildcard() bool {
	return a.wildcard
}

func (a Allowlist) Size() int {
	return len(a.ids)
}

type Config struct {
	HTTPListenAddr string
	LogLevel       string
	AdminAPIToken  string

	QQPlatform string

	QQNTPath              string
	QQNTAutoLaunch        bool
	QQNTAllowRunning      bool
	QQNTLoaderPath        string
	QQNTHookPath          string
	QQNTLoadPath          string
	QQNTRuntimePath       string
	QQNTPatchPackagePath  string
	QQNTIPCListenAddr     string
	QQNTIPCToken          string
	QQNTActionTTL         time.Duration
	QQNTHandshakeTTL      time.Duration
	QQNTMaxFrameBytes     int
	QQNTAutoAcceptFriend  bool
	QQNTHeadless          bool
	QQNTImageAllowedRoots []string
	QQNTImageMaxBytes     int64

	OneBotWSURL        string
	OneBotHTTPURL      string
	OneBotAccessToken  string
	OneBotActionTTL    time.Duration
	OneBotTransport    string
	OneBotListenAddr   string
	OneBotReversePath  string
	OneBotAccountsFile string
	OneBotAutoAccept   bool

	FriendWelcomeText string
	FriendFilePath    string
	// BotPublicUIN is the bot account's public QQ number, only used inside
	// user-facing reply texts (e.g. "add me QQ ..."); kept out of source
	// so real account IDs never live in the repo.
	BotPublicUIN string
	// FriendFileGroups limits the friend-welcome file to members of
	// these groups (comma-separated group ids); empty = send to anyone.
	FriendFileGroups   []string
	GroupWelcomeImage  string
	GroupWelcomeText   string
	GroupWelcomeGroups []string
	// WelcomeFile is an optional JSON config (friend_text / group_text)
	// that overrides the env welcome texts so operators can edit them
	// without touching the service environment.
	WelcomeFile string
	// BasePromptFile holds the shared human-like behavior rules injected
	// into every persona prompt, so personas only carry their own voice.
	BasePromptFile string
	// PeerBotIDs are bot accounts run by other deployments in the same
	// groups. Their messages are observed but never answered, so two
	// personas never argue with each other in public.
	PeerBotIDs      []string
	AdminUserIDs    []string
	ChatPrefsFile   string
	AffinityFile    string
	IQChartAPIBase  string
	IQChartInterval time.Duration
	IQChartFontPath string

	GroupAllowlist     Allowlist
	PrivateAllowlist   Allowlist
	RequireMention     bool
	QuoteReply         bool
	QuoteGroupProb     float64
	GroupAtSender      bool
	MaxReplyRunes      int
	MaxReplyChunks     int
	GroupBatchWindow   time.Duration
	PrivateBatchWindow time.Duration
	ReplyPartDelay     time.Duration

	AgentMode          string
	AgentAPIURL        string
	AgentAPIKey        string
	AgentModel         string
	AgentSystemPrompt  string
	AgentAuthHeader    string
	AgentAuthScheme    string
	AgentTimeout       time.Duration
	AgentMaxRetries    int
	AgentMaxToolRounds int
	AgentRetryBase     time.Duration
	AgentRetryMax      time.Duration

	ImageMode      string
	ImageAPIURL    string
	ImageAPIKey    string
	ImageModel     string
	ImageOutputDir string
	ImageMaxBytes  int64
	ImageTimeout   time.Duration
	ImageRetention time.Duration

	VoiceMode           string
	VoiceName           string
	VoiceStyle          string
	VoiceAPIURL         string
	VoiceAPIKey         string
	VoiceModel          string
	VoiceCommand        string
	VoiceVolcAppID      string
	VoiceVolcToken      string
	VoiceVolcResourceID string
	VoiceVolcEmotion    string
	VoiceDir            string
	VoiceMaxRunes       int
	VoiceCooldown       time.Duration
	VoiceTimeout        time.Duration
	VoiceDailyLimit     int
	ImageEnhance        bool

	// Repeat-spam handling: same normalized text N times inside the
	// window deducts affinity once per streak. SPAM_REPEAT_MODE accepts
	// a single mode or a weighted mix rolled per trigger:
	// "hint" (model gets a nudge and scolds in character), "voice"
	// (one canned voice scold, message dropped), "ignore" (silent
	// drop), "off" — e.g. "hint:50,voice:25,ignore:25".
	SpamRepeatMode      string
	SpamRepeatCount     int
	SpamRepeatWindow    time.Duration
	SpamRepeatPenalty   int
	SpamRepeatVoiceText string

	// Mention fatigue: after N consecutive replies to the same user in
	// one group chat, further messages from them get ignored with
	// MentionFatigueIgnorePct probability until the window lapses —
	// like a human drifting off when someone keeps poking. 0 = off.
	MentionFatigueCount     int
	MentionFatigueWindow    time.Duration
	MentionFatigueIgnorePct int

	WebScreenshotEnabled      bool
	WebScreenshotBrowserPath  string
	WebScreenshotAllowedHosts []string
	WebScreenshotOutputDir    string
	WebScreenshotMaxBytes     int64
	WebScreenshotTimeout      time.Duration
	WebScreenshotRetention    time.Duration
	WebScreenshotWidth        int
	WebScreenshotHeight       int
	WebScreenshotWait         time.Duration

	MaxHistory           int
	SessionTTL           time.Duration
	SessionStorePath     string
	SessionLegacyPath    string
	SessionKey           string
	SessionCompressAt    int
	SessionRetain        int
	SessionSemanticTTL   time.Duration
	SessionLearning      bool
	CronStorePath        string
	KnowledgeDir         string
	KnowledgeTopK        int
	MCPServersFile       string
	SkillsDir            string
	SubagentsFile        string
	PluginsFile          string
	ProvidersFile        string
	PersonasFile         string
	PersonaPacksDir      string
	ChatBindingsFile     string
	FileCatalogPath      string
	FileMaxBytes         int64
	FileSendTimeout      time.Duration
	BotAllowedRoot       string
	SecurityStorePath    string
	MaxConcurrency       int
	UserCooldown         time.Duration
	UserRateLimit        int
	UserRateWindow       time.Duration
	AttentionTimeout     time.Duration
	AttentionRateLimit   int
	AttentionRateWindow  time.Duration
	MediaToolCooldown    time.Duration
	MediaToolLimit       int
	MediaToolWindow      time.Duration
	MediaToolConcurrency int
	MessageDedupeTTL     time.Duration

	// Humanization layer — makes the bot behave like a person rather than
	// a vending machine. All persona-agnostic; personas stay operator-owned.
	TypingSim      bool          // human-like delay before sending
	TypingBase     time.Duration // base delay before first reply part
	TypingPerRune  time.Duration // extra delay per rune of the first part
	TypingMax      time.Duration // cap for the typing delay
	MoodEnabled    bool          // per-chat mood meter + prompt hint
	SleepStart     int           // hour sleep window begins (-1 disables)
	SleepEnd       int           // hour sleep window ends
	Proactive      bool          // ambient opener engine
	ProactiveTick  time.Duration // sweep interval
	ProactiveIdle  time.Duration // required chat silence before eligible
	ProactiveWait  time.Duration // cooldown between proactive sends
	ProactiveProb  float64       // per-tick fire probability
	ProactiveDaily int           // per-chat daily cap
	// ProactiveGroups optionally limits which bound groups may receive
	// proactive openers; empty means every bound group is eligible.
	ProactiveGroups Allowlist

	HandoffReply string
	ResumeReply  string
	ClearReply   string
	ErrorReply   string
	// ErrorSilent drops all agent-error fallback replies instead of
	// sending ErrorReply — a silent miss reads more human than a canned
	// "service unavailable" notice. QQ_ERROR_SILENT opt-in.
	ErrorSilent bool
}

// Asleep reports whether hour-of-day now falls inside the configured sleep
// window. Windows may wrap midnight (e.g. 1..8 means 01:00-07:59 asleep).
func (c Config) Asleep(now time.Time) bool {
	if c.SleepStart < 0 {
		return false
	}
	hour := now.Hour()
	if c.SleepStart <= c.SleepEnd {
		return hour >= c.SleepStart && hour < c.SleepEnd
	}
	return hour >= c.SleepStart || hour < c.SleepEnd
}

func Load() (Config, error) {
	var errs []error

	groupAllowlist, err := ParseAllowlist(os.Getenv("QQ_GROUP_ALLOWLIST"))
	if err != nil {
		errs = append(errs, fmt.Errorf("QQ_GROUP_ALLOWLIST: %w", err))
	}
	privateAllowlist, err := ParseOptionalAllowlist(os.Getenv("QQ_PRIVATE_ALLOWLIST"))
	if err != nil {
		errs = append(errs, fmt.Errorf("QQ_PRIVATE_ALLOWLIST: %w", err))
	}
	oneBotTransport := normalizeOneBotTransport(envOr("ONEBOT_TRANSPORT", "forward_ws"))
	reversePathDefault := "/onebot/v11/ws"
	if oneBotTransport == "reverse_http" {
		reversePathDefault = "/onebot/v11/events"
	}

	cfg := Config{
		HTTPListenAddr: envOr("HTTP_LISTEN_ADDR", "127.0.0.1:18080"),
		LogLevel:       strings.ToLower(envOr("LOG_LEVEL", "info")),
		AdminAPIToken:  os.Getenv("ADMIN_API_TOKEN"),

		QQPlatform:           normalizeQQPlatform(envOr("QQ_PLATFORM", defaultQQPlatform())),
		QQNTPath:             envOrAllowEmpty("QQNT_PATH", ""),
		QQNTLoaderPath:       envOr("QQNT_LOADER_PATH", "bin/cinlan-qq-loader.exe"),
		QQNTHookPath:         envOr("QQNT_HOOK_PATH", "bin/cinlan-qq-hook.dll"),
		QQNTLoadPath:         envOr("QQNT_LOAD_PATH", "runtime/qqnt/load-cinlan.cjs"),
		QQNTRuntimePath:      envOr("QQNT_RUNTIME_PATH", "runtime/qqnt/runtime.cjs"),
		QQNTPatchPackagePath: envOr("QQNT_PATCH_PACKAGE_PATH", "data/runtime/qqnt-package.json"),
		QQNTIPCListenAddr:    envOr("QQNT_IPC_LISTEN_ADDR", "127.0.0.1:18081"),
		QQNTIPCToken:         strings.TrimSpace(os.Getenv("QQNT_IPC_TOKEN")),

		OneBotWSURL:        envOr("ONEBOT_WS_URL", "ws://127.0.0.1:3001"),
		OneBotHTTPURL:      envOr("ONEBOT_HTTP_URL", "http://127.0.0.1:3000"),
		OneBotAccessToken:  os.Getenv("ONEBOT_ACCESS_TOKEN"),
		OneBotTransport:    oneBotTransport,
		OneBotListenAddr:   envOr("ONEBOT_REVERSE_LISTEN_ADDR", "127.0.0.1:3002"),
		OneBotReversePath:  envOr("ONEBOT_REVERSE_PATH", reversePathDefault),
		OneBotAccountsFile: envOrAllowEmpty("ONEBOT_ACCOUNTS_FILE", ""),
		OneBotAutoAccept:   parseBool("ONEBOT_AUTO_ACCEPT_FRIEND", false, &errs),
		FriendWelcomeText:  envOrAllowEmpty("FRIEND_WELCOME_TEXT", ""),
		FriendFilePath:     envOrAllowEmpty("FRIEND_FILE_PATH", ""),
		BotPublicUIN:       envOrAllowEmpty("BOT_PUBLIC_UIN", ""),
		FriendFileGroups:   parseList(os.Getenv("FRIEND_FILE_GROUPS")),
		GroupWelcomeImage:  envOrAllowEmpty("GROUP_WELCOME_IMAGE", ""),
		GroupWelcomeText:   envOrAllowEmpty("GROUP_WELCOME_TEXT", ""),
		GroupWelcomeGroups: parseList(envOrAllowEmpty("GROUP_WELCOME_GROUPS", "")),
		WelcomeFile:        envOrAllowEmpty("WELCOME_FILE", "data/welcome.json"),
		BasePromptFile:     envOrAllowEmpty("BASE_PROMPT_FILE", "data/prompts/base-human.md"),
		PeerBotIDs:         parseList(envOrAllowEmpty("PEER_BOT_IDS", "")),
		ChatPrefsFile:      envOr("CHAT_PREFS_FILE", "data/chat-prefs.json"),
		AffinityFile:       envOr("AFFINITY_FILE", "data/affinity.json"),
		IQChartAPIBase: envOr(
			"IQ_CHART_API_BASE",
			"https://codexradar.com/api/radar-bench-score",
		),
		IQChartInterval:  parseDuration("IQ_CHART_INTERVAL", 90*time.Minute, false, &errs),
		IQChartFontPath:  envOr("IQ_CHART_FONT", "data/fonts/iqchart.ttf"),
		GroupAllowlist:   groupAllowlist,
		PrivateAllowlist: privateAllowlist,

		AgentMode:         strings.ToLower(envOr("AGENT_API_MODE", AgentModeCustom)),
		AgentAPIURL:       os.Getenv("AGENT_API_URL"),
		AgentAPIKey:       os.Getenv("AGENT_API_KEY"),
		AgentModel:        os.Getenv("AGENT_MODEL"),
		AgentSystemPrompt: envOr("AGENT_SYSTEM_PROMPT", "你是 Cinlan QQ 智能客服。只回答与用户问题相关的内容；不确定时明确说明并建议转人工，不编造事实。"),
		AgentAuthHeader:   envOr("AGENT_AUTH_HEADER", "Authorization"),
		AgentAuthScheme:   envOr("AGENT_AUTH_SCHEME", "Bearer"),
		PersonasFile:      envOrAllowEmpty("PERSONAS_FILE", "data/personas.json"),
		PersonaPacksDir:   envOrAllowEmpty("PERSONA_PACKS_DIR", ""),
		ChatBindingsFile:  envOrAllowEmpty("CHAT_BINDINGS_FILE", "data/chat-bindings.json"),

		HandoffReply: envOr("QQ_HANDOFF_REPLY", "已转入人工服务，机器人将暂停回复。群聊发送“@机器人 /恢复”，私聊发送“/恢复”可重新启用机器人。"),
		ResumeReply:  envOr("QQ_RESUME_REPLY", "已恢复智能客服。"),
		ClearReply:   envOr("QQ_CLEAR_REPLY", "当前会话已清空。"),
		ErrorReply:   envOr("QQ_ERROR_REPLY", "服务暂时不可用，请稍后再试；群聊可发送“@机器人 /人工”，私聊可发送“/人工”。"),
	}

	cfg.ErrorSilent = parseBool("QQ_ERROR_SILENT", false, &errs)
	cfg.RequireMention = parseBool("QQ_REQUIRE_MENTION", true, &errs)
	cfg.QuoteReply = parseBool("QQ_QUOTE_REPLY", true, &errs)
	cfg.QuoteGroupProb = parseFloat("QQ_QUOTE_GROUP_PROB", 0.4, 0, 1, &errs)
	cfg.GroupAtSender = parseBool("QQ_GROUP_AT_SENDER", true, &errs)
	cfg.MaxReplyRunes = parseInt("QQ_MAX_REPLY_RUNES", 1500, 1, &errs)
	cfg.MaxReplyChunks = parseInt("QQ_MAX_REPLY_CHUNKS", 4, 1, &errs)
	cfg.GroupBatchWindow = parseDuration(
		"QQ_GROUP_BATCH_WINDOW",
		900*time.Millisecond,
		false,
		&errs,
	)
	// Rapid DM follow-ups ("还要有你的" after "拍个夕阳") arrive as
	// separate events; a short merge window turns them into one run
	// instead of two replies — and two image-gen quota burns.
	cfg.PrivateBatchWindow = parseDuration(
		"QQ_PRIVATE_BATCH_WINDOW",
		1200*time.Millisecond,
		false,
		&errs,
	)
	cfg.ReplyPartDelay = parseDuration(
		"QQ_REPLY_PART_DELAY",
		350*time.Millisecond,
		false,
		&errs,
	)
	cfg.QQNTAutoLaunch = parseBool("QQNT_AUTO_LAUNCH", true, &errs)
	cfg.QQNTAllowRunning = parseBool("QQNT_ALLOW_RUNNING", false, &errs)
	cfg.QQNTAutoAcceptFriend = parseBool("QQNT_AUTO_ACCEPT_FRIEND", false, &errs)
	cfg.QQNTHeadless = parseBool("QQNT_HEADLESS", false, &errs)
	cfg.QQNTActionTTL = parseDuration("QQNT_ACTION_TIMEOUT", 10*time.Second, true, &errs)
	cfg.QQNTHandshakeTTL = parseDuration("QQNT_HANDSHAKE_TIMEOUT", 10*time.Second, true, &errs)
	cfg.QQNTMaxFrameBytes = parseInt(
		"QQNT_MAX_FRAME_BYTES",
		1024*1024,
		4096,
		&errs,
	)
	cfg.QQNTImageAllowedRoots = parsePathList(envOrAllowEmpty(
		"QQNT_IMAGE_ALLOWED_ROOTS",
		defaultQQNTImageAllowedRoots(),
	))
	cfg.QQNTImageMaxBytes = parseInt64(
		"QQNT_IMAGE_MAX_BYTES",
		20<<20,
		1,
		&errs,
	)
	cfg.OneBotActionTTL = parseDuration("ONEBOT_ACTION_TIMEOUT", 10*time.Second, true, &errs)

	cfg.AgentTimeout = parseDuration("AGENT_TIMEOUT", 30*time.Second, true, &errs)
	cfg.AgentMaxRetries = parseInt("AGENT_MAX_RETRIES", 3, 0, &errs)
	// Multi-step tool flows (MCP ordering, file work) need ~5-6 rounds;
	// 4 starves them and surfaces the rounds-exhausted fallback.
	cfg.AgentMaxToolRounds = parseInt("AGENT_MAX_TOOL_ROUNDS", 6, 1, &errs)
	cfg.AgentRetryBase = parseDuration("AGENT_RETRY_BASE", 500*time.Millisecond, true, &errs)
	cfg.AgentRetryMax = parseDuration("AGENT_RETRY_MAX", 5*time.Second, true, &errs)

	cfg.AdminUserIDs = parseStringList(envOrAllowEmpty("BOT_ADMIN_USER_IDS", ""))

	cfg.ImageMode = strings.ToLower(envOrAllowEmpty("IMAGE_API_MODE", ""))
	cfg.ImageAPIURL = envOrAllowEmpty("IMAGE_API_URL", "")
	if cfg.ImageMode == "pollinations" && cfg.ImageAPIURL == "" {
		cfg.ImageAPIURL = "https://image.pollinations.ai/prompt"
	}
	if cfg.ImageMode == "openai" && cfg.ImageAPIURL == "" {
		cfg.ImageAPIURL = deriveImageAPIURL(cfg.AgentAPIURL)
	}
	cfg.ImageAPIKey = strings.TrimSpace(os.Getenv("IMAGE_API_KEY"))
	if cfg.ImageAPIKey == "" {
		cfg.ImageAPIKey = cfg.AgentAPIKey
	}
	cfg.ImageModel = envOrAllowEmpty("IMAGE_MODEL", "")
	if cfg.ImageMode == "openai" && cfg.ImageModel == "" {
		cfg.ImageModel = "gpt-image-1"
	}
	if cfg.ImageMode == "pollinations" && cfg.ImageModel == "" {
		cfg.ImageModel = "flux"
	}
	cfg.ImageOutputDir = envOr("IMAGE_OUTPUT_DIR", "data/generated-images")
	cfg.ImageMaxBytes = parseInt64("IMAGE_MAX_BYTES", 20<<20, 1, &errs)
	cfg.ImageTimeout = parseDuration("IMAGE_TIMEOUT", 2*time.Minute, true, &errs)
	cfg.ImageRetention = parseDuration("IMAGE_RETENTION", 24*time.Hour, true, &errs)

	cfg.VoiceMode = envOr("VOICE_MODE", "edge")
	cfg.VoiceName = envOr("VOICE_NAME", "zh-CN-XiaoyiNeural")
	cfg.VoiceAPIURL = envOrAllowEmpty("VOICE_API_URL", "")
	cfg.VoiceAPIKey = envOrAllowEmpty("VOICE_API_KEY", "")
	if cfg.VoiceAPIKey == "" {
		cfg.VoiceAPIKey = cfg.ImageAPIKey
	}
	if cfg.VoiceAPIKey == "" {
		cfg.VoiceAPIKey = cfg.AgentAPIKey
	}
	cfg.VoiceModel = envOr("VOICE_MODEL", "tts-1-hd")
	cfg.VoiceStyle = envOrAllowEmpty("VOICE_STYLE", "")
	cfg.VoiceCommand = envOrAllowEmpty("VOICE_COMMAND", "")
	cfg.VoiceVolcAppID = envOrAllowEmpty("VOICE_VOLC_APP_ID", "")
	cfg.VoiceVolcToken = envOrAllowEmpty("VOICE_VOLC_TOKEN", "")
	cfg.VoiceVolcResourceID = envOrAllowEmpty("VOICE_VOLC_RESOURCE_ID", "")
	cfg.VoiceVolcEmotion = envOrAllowEmpty("VOICE_VOLC_EMOTION", "")
	cfg.VoiceDir = envOr("VOICE_DIR", "data/voice")
	cfg.VoiceMaxRunes = parseInt("VOICE_MAX_RUNES", 200, 20, &errs)
	cfg.VoiceCooldown = parseDuration("VOICE_COOLDOWN", 45*time.Second, true, &errs)
	cfg.VoiceTimeout = parseDuration("VOICE_TIMEOUT", 15*time.Second, true, &errs)
	cfg.VoiceDailyLimit = parseInt("VOICE_DAILY_LIMIT", 50, 0, &errs)
	cfg.ImageEnhance = parseBool("IMAGE_ENHANCE_PROMPT", true, &errs)

	cfg.SpamRepeatMode = envOr("SPAM_REPEAT_MODE", "hint")
	cfg.SpamRepeatCount = parseInt("SPAM_REPEAT_COUNT", 3, 0, &errs)
	cfg.SpamRepeatWindow = parseDuration("SPAM_REPEAT_WINDOW", 2*time.Minute, true, &errs)
	cfg.SpamRepeatPenalty = parseInt("SPAM_REPEAT_PENALTY", 3, 0, &errs)
	cfg.SpamRepeatVoiceText = envOrAllowEmpty("SPAM_REPEAT_VOICE_TEXT", "")

	cfg.MentionFatigueCount = parseInt("MENTION_FATIGUE_COUNT", 3, 0, &errs)
	cfg.MentionFatigueWindow = parseDuration("MENTION_FATIGUE_WINDOW", 10*time.Minute, true, &errs)
	cfg.MentionFatigueIgnorePct = parseInt("MENTION_FATIGUE_IGNORE_PCT", 65, 0, &errs)

	cfg.WebScreenshotEnabled = parseBool("WEB_SCREENSHOT_ENABLED", false, &errs)
	cfg.WebScreenshotBrowserPath = strings.TrimSpace(
		os.Getenv("WEB_SCREENSHOT_BROWSER_PATH"),
	)
	cfg.WebScreenshotAllowedHosts = parseList(
		os.Getenv("WEB_SCREENSHOT_ALLOWED_HOSTS"),
	)
	cfg.WebScreenshotOutputDir = envOr(
		"WEB_SCREENSHOT_OUTPUT_DIR",
		"data/generated-images",
	)
	cfg.WebScreenshotMaxBytes = parseInt64(
		"WEB_SCREENSHOT_MAX_BYTES",
		20<<20,
		1,
		&errs,
	)
	cfg.WebScreenshotTimeout = parseDuration(
		"WEB_SCREENSHOT_TIMEOUT",
		45*time.Second,
		true,
		&errs,
	)
	cfg.WebScreenshotRetention = parseDuration(
		"WEB_SCREENSHOT_RETENTION",
		24*time.Hour,
		true,
		&errs,
	)
	cfg.WebScreenshotWidth = parseInt(
		"WEB_SCREENSHOT_WIDTH",
		1365,
		1,
		&errs,
	)
	cfg.WebScreenshotHeight = parseInt(
		"WEB_SCREENSHOT_HEIGHT",
		768,
		1,
		&errs,
	)
	cfg.WebScreenshotWait = parseDuration(
		"WEB_SCREENSHOT_WAIT",
		3*time.Second,
		false,
		&errs,
	)

	cfg.MaxHistory = parseInt("SESSION_MAX_HISTORY", 20, 2, &errs)
	cfg.SessionTTL = parseDuration("SESSION_TTL", 24*time.Hour, true, &errs)
	cfg.SessionStorePath = envOrAllowEmpty("SESSION_STORE_PATH", "data/sessions.db")
	cfg.SessionLegacyPath = envOrAllowEmpty(
		"SESSION_LEGACY_STORE_PATH",
		"data/sessions.json",
	)
	cfg.SessionKey = strings.TrimSpace(os.Getenv("SESSION_ENCRYPTION_KEY"))
	cfg.SessionCompressAt = parseInt("SESSION_COMPRESSION_THRESHOLD", 16, 4, &errs)
	cfg.SessionRetain = parseInt("SESSION_COMPRESSION_RETAIN", 6, 2, &errs)
	// Semantic memory outlives raw turns: SESSION_TTL controls history,
	// this controls how long compressed summary/memory still count.
	cfg.SessionSemanticTTL = parseDuration(
		"SESSION_SEMANTIC_TTL", 365*24*time.Hour, true, &errs)
	if cfg.SessionSemanticTTL < cfg.SessionTTL {
		cfg.SessionSemanticTTL = cfg.SessionTTL
	}
	cfg.SessionLearning = parseBool("SESSION_LEARNING_ENABLED", true, &errs)
	cfg.CronStorePath = envOrAllowEmpty("CRON_STORE_PATH", "data/cron.json")
	cfg.KnowledgeDir = strings.TrimSpace(os.Getenv("KNOWLEDGE_DIR"))
	cfg.KnowledgeTopK = parseInt("KNOWLEDGE_TOP_K", 3, 1, &errs)
	cfg.MCPServersFile = envOrAllowEmpty("MCP_SERVERS_FILE", "")
	cfg.SkillsDir = strings.TrimSpace(os.Getenv("SKILLS_DIR"))
	cfg.SubagentsFile = envOrAllowEmpty("SUBAGENTS_FILE", "")
	cfg.PluginsFile = envOrAllowEmpty("PLUGINS_FILE", "")
	cfg.ProvidersFile = envOrAllowEmpty("PROVIDERS_FILE", "")
	cfg.FileCatalogPath = envOrAllowEmpty("FILE_CATALOG_PATH", "")
	cfg.FileMaxBytes = parseInt64("FILE_DELIVERY_MAX_BYTES", 100<<20, 1, &errs)
	cfg.FileSendTimeout = parseDuration("FILE_DELIVERY_TIMEOUT", 30*time.Second, true, &errs)
	cfg.BotAllowedRoot = envOr("BOT_ALLOWED_ROOT", ".")
	cfg.SecurityStorePath = envOr(
		"SECURITY_INCIDENT_STORE_PATH",
		"data/security-incidents.json",
	)
	cfg.MaxConcurrency = parseInt("BOT_MAX_CONCURRENCY", 8, 1, &errs)
	cfg.UserCooldown = parseDuration("BOT_USER_COOLDOWN", 3*time.Second, false, &errs)
	cfg.UserRateLimit = parseInt("BOT_USER_RATE_LIMIT", 10, 0, &errs)
	cfg.UserRateWindow = parseDuration(
		"BOT_USER_RATE_WINDOW",
		time.Minute,
		false,
		&errs,
	)
	cfg.AttentionTimeout = parseDuration("BOT_ATTENTION_TIMEOUT", 5*time.Second, true, &errs)
	cfg.AttentionRateLimit = parseInt("BOT_ATTENTION_RATE_LIMIT", 20, 0, &errs)
	cfg.AttentionRateWindow = parseDuration(
		"BOT_ATTENTION_RATE_WINDOW",
		time.Minute,
		false,
		&errs,
	)
	cfg.MediaToolCooldown = parseDuration(
		"MEDIA_TOOL_USER_COOLDOWN",
		30*time.Second,
		false,
		&errs,
	)
	cfg.MediaToolLimit = parseInt("MEDIA_TOOL_USER_LIMIT", 10, 0, &errs)
	cfg.MediaToolWindow = parseDuration(
		"MEDIA_TOOL_WINDOW",
		time.Hour,
		false,
		&errs,
	)
	cfg.MediaToolConcurrency = parseInt(
		"MEDIA_TOOL_MAX_CONCURRENCY",
		2,
		1,
		&errs,
	)
	cfg.MessageDedupeTTL = parseDuration("BOT_MESSAGE_DEDUPE_TTL", 10*time.Minute, true, &errs)

	cfg.TypingSim = parseBool("QQ_TYPING_SIM", true, &errs)
	cfg.TypingBase = parseDuration("QQ_TYPING_BASE", 1200*time.Millisecond, true, &errs)
	cfg.TypingPerRune = parseDuration("QQ_TYPING_PER_RUNE", 55*time.Millisecond, true, &errs)
	cfg.TypingMax = parseDuration("QQ_TYPING_MAX", 8*time.Second, true, &errs)
	cfg.MoodEnabled = parseBool("QQ_MOOD_ENABLED", true, &errs)
	cfg.SleepStart = parseInt("QQ_SLEEP_START", 1, -1, &errs)
	cfg.SleepEnd = parseInt("QQ_SLEEP_END", 8, 0, &errs)
	cfg.Proactive = parseBool("QQ_PROACTIVE", false, &errs)
	cfg.ProactiveTick = parseDuration("QQ_PROACTIVE_TICK", 5*time.Minute, true, &errs)
	cfg.ProactiveIdle = parseDuration("QQ_PROACTIVE_IDLE", 45*time.Minute, true, &errs)
	cfg.ProactiveWait = parseDuration("QQ_PROACTIVE_COOLDOWN", 4*time.Hour, true, &errs)
	cfg.ProactiveProb = parseFloat("QQ_PROACTIVE_CHANCE", 0.12, 0, 1, &errs)
	cfg.ProactiveDaily = parseInt("QQ_PROACTIVE_DAILY", 5, 1, &errs)
	proactiveGroups, err := ParseOptionalAllowlist(os.Getenv("QQ_PROACTIVE_GROUPS"))
	if err != nil {
		errs = append(errs, fmt.Errorf("QQ_PROACTIVE_GROUPS: %w", err))
	}
	cfg.ProactiveGroups = proactiveGroups

	applyWelcomeFile(&cfg, &errs)
	if err := cfg.Validate(); err != nil {
		errs = append(errs, err)
	}
	return cfg, errors.Join(errs...)
}

// applyWelcomeFile lets data/welcome.json override the env welcome texts.
// A missing file is not an error — env values stay in effect.
func applyWelcomeFile(cfg *Config, errs *[]error) {
	path := strings.TrimSpace(cfg.WelcomeFile)
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			*errs = append(*errs, fmt.Errorf("read welcome file: %w", err))
		}
		return
	}
	var file struct {
		FriendText string `json:"friend_text"`
		GroupText  string `json:"group_text"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		*errs = append(*errs, fmt.Errorf("parse welcome file: %w", err))
		return
	}
	if text := strings.TrimSpace(file.FriendText); text != "" {
		cfg.FriendWelcomeText = text
	}
	if text := strings.TrimSpace(file.GroupText); text != "" {
		cfg.GroupWelcomeText = text
	}
}

func (c Config) Validate() error {
	var errs []error

	if strings.TrimSpace(c.HTTPListenAddr) == "" {
		errs = append(errs, errors.New("HTTP_LISTEN_ADDR must not be empty"))
	} else if err := validateListenAddress(c.HTTPListenAddr, "HTTP_LISTEN_ADDR"); err != nil {
		errs = append(errs, err)
	}
	if !contains([]string{"debug", "info", "warn", "error"}, c.LogLevel) {
		errs = append(errs, fmt.Errorf("LOG_LEVEL must be debug, info, warn, or error; got %q", c.LogLevel))
	}
	if !contains([]string{QQPlatformNative, QQPlatformOneBot}, c.QQPlatform) {
		errs = append(errs, fmt.Errorf(
			"QQ_PLATFORM must be native or onebot; got %q",
			c.QQPlatform,
		))
	}
	if c.QQPlatform == QQPlatformNative && runtime.GOOS != "windows" {
		errs = append(errs, fmt.Errorf(
			"QQ_PLATFORM=native is supported only on Windows; use QQ_PLATFORM=onebot on %s",
			runtime.GOOS,
		))
	}
	if c.SessionStorePath != "" && strings.TrimSpace(c.SessionKey) == "" {
		errs = append(errs, errors.New(
			"SESSION_ENCRYPTION_KEY is required when SESSION_STORE_PATH is configured",
		))
	}
	if c.SessionCompressAt > c.MaxHistory {
		errs = append(errs, fmt.Errorf(
			"SESSION_COMPRESSION_THRESHOLD must not exceed SESSION_MAX_HISTORY",
		))
	}
	if c.SessionRetain >= c.SessionCompressAt {
		errs = append(errs, fmt.Errorf(
			"SESSION_COMPRESSION_RETAIN must be less than SESSION_COMPRESSION_THRESHOLD",
		))
	}
	if c.SessionRetain%2 != 0 {
		errs = append(errs, errors.New(
			"SESSION_COMPRESSION_RETAIN must be even to preserve complete exchanges",
		))
	}
	if c.QQPlatform == QQPlatformNative {
		if c.QQNTHeadless && strings.TrimSpace(c.AdminAPIToken) == "" {
			errs = append(errs, errors.New("ADMIN_API_TOKEN is required for QQNT_HEADLESS login API"))
		}
		if err := validateLoopbackAddress(c.QQNTIPCListenAddr, "QQNT_IPC_LISTEN_ADDR"); err != nil {
			errs = append(errs, err)
		}
		for _, root := range c.QQNTImageAllowedRoots {
			if !filepath.IsAbs(root) {
				errs = append(errs, fmt.Errorf(
					"QQNT_IMAGE_ALLOWED_ROOTS must contain absolute paths; got %q",
					root,
				))
			}
		}
		if c.QQNTAutoLaunch {
			for key, value := range map[string]string{
				"QQNT_LOADER_PATH":        c.QQNTLoaderPath,
				"QQNT_HOOK_PATH":          c.QQNTHookPath,
				"QQNT_LOAD_PATH":          c.QQNTLoadPath,
				"QQNT_RUNTIME_PATH":       c.QQNTRuntimePath,
				"QQNT_PATCH_PACKAGE_PATH": c.QQNTPatchPackagePath,
			} {
				if strings.TrimSpace(value) == "" {
					errs = append(errs, fmt.Errorf("%s must not be empty", key))
				}
			}
		}
	}
	if c.QQPlatform == QQPlatformOneBot && strings.TrimSpace(c.OneBotAccountsFile) == "" {
		switch c.OneBotTransport {
		case "forward_ws":
			if err := validateURL(c.OneBotWSURL, "ONEBOT_WS_URL", "ws", "wss"); err != nil {
				errs = append(errs, err)
			}
		case "http_sse":
			if err := validateURL(c.OneBotHTTPURL, "ONEBOT_HTTP_URL", "http", "https"); err != nil {
				errs = append(errs, err)
			}
		case "reverse_ws", "reverse_http":
			if c.OneBotTransport == "reverse_http" {
				if err := validateURL(c.OneBotHTTPURL, "ONEBOT_HTTP_URL", "http", "https"); err != nil {
					errs = append(errs, err)
				}
			}
			if strings.TrimSpace(c.OneBotListenAddr) == "" {
				errs = append(errs, errors.New("ONEBOT_REVERSE_LISTEN_ADDR must not be empty"))
			} else if _, _, err := net.SplitHostPort(strings.TrimSpace(c.OneBotListenAddr)); err != nil {
				errs = append(errs, fmt.Errorf("ONEBOT_REVERSE_LISTEN_ADDR must be host:port: %w", err))
			}
			if !strings.HasPrefix(strings.TrimSpace(c.OneBotReversePath), "/") {
				errs = append(errs, errors.New("ONEBOT_REVERSE_PATH must start with /"))
			}
		default:
			errs = append(errs, fmt.Errorf("ONEBOT_TRANSPORT must be forward_ws, reverse_ws, http_sse, or reverse_http; got %q", c.OneBotTransport))
		}
	}
	if strings.TrimSpace(c.ProvidersFile) == "" {
		if c.AgentMode != AgentModeCustom && c.AgentMode != AgentModeOpenAI {
			errs = append(errs, fmt.Errorf("AGENT_API_MODE must be custom or openai; got %q", c.AgentMode))
		}
		if err := validateURL(c.AgentAPIURL, "AGENT_API_URL", "http", "https"); err != nil {
			errs = append(errs, err)
		}
		if c.AgentMode == AgentModeOpenAI && strings.TrimSpace(c.AgentModel) == "" {
			errs = append(errs, errors.New("AGENT_MODEL is required when AGENT_API_MODE=openai"))
		}
		if strings.TrimSpace(c.AgentAuthHeader) == "" {
			errs = append(errs, errors.New("AGENT_AUTH_HEADER must not be empty"))
		}
	}
	if c.AgentRetryMax < c.AgentRetryBase {
		errs = append(errs, errors.New("AGENT_RETRY_MAX must be greater than or equal to AGENT_RETRY_BASE"))
	}
	if strings.TrimSpace(c.BotAllowedRoot) == "" {
		errs = append(errs, errors.New("BOT_ALLOWED_ROOT must not be empty"))
	}
	if c.ImageMode != "" {
		if !contains(
			[]string{"openai", "pollinations", "openai_chat", "antigravity"},
			c.ImageMode,
		) {
			errs = append(errs, fmt.Errorf(
				"IMAGE_API_MODE must be openai, openai_chat or pollinations; got %q",
				c.ImageMode,
			))
		}
		if err := validateURL(c.ImageAPIURL, "IMAGE_API_URL", "https"); err != nil {
			errs = append(errs, err)
		}
		if strings.TrimSpace(c.ImageModel) == "" {
			errs = append(errs, errors.New("IMAGE_MODEL must not be empty when image generation is enabled"))
		}
		if strings.TrimSpace(c.ImageOutputDir) == "" {
			errs = append(errs, errors.New("IMAGE_OUTPUT_DIR must not be empty"))
		}
	}
	if c.WebScreenshotEnabled {
		if strings.TrimSpace(c.WebScreenshotBrowserPath) == "" {
			errs = append(errs, errors.New(
				"WEB_SCREENSHOT_BROWSER_PATH is required when web screenshots are enabled",
			))
		}
		if len(c.WebScreenshotAllowedHosts) == 0 {
			errs = append(errs, errors.New(
				"WEB_SCREENSHOT_ALLOWED_HOSTS is required when web screenshots are enabled",
			))
		}
		if strings.TrimSpace(c.WebScreenshotOutputDir) == "" {
			errs = append(errs, errors.New(
				"WEB_SCREENSHOT_OUTPUT_DIR must not be empty",
			))
		}
		if c.WebScreenshotWidth > 4096 || c.WebScreenshotHeight > 4096 {
			errs = append(errs, errors.New(
				"WEB_SCREENSHOT_WIDTH and WEB_SCREENSHOT_HEIGHT must be at most 4096",
			))
		}
	}
	if c.UserRateLimit > 0 && c.UserRateWindow <= 0 {
		errs = append(errs, errors.New(
			"BOT_USER_RATE_WINDOW must be positive when BOT_USER_RATE_LIMIT is enabled",
		))
	}
	if c.AttentionTimeout <= 0 {
		errs = append(errs, errors.New("BOT_ATTENTION_TIMEOUT must be positive"))
	}
	if c.AttentionRateLimit > 0 && c.AttentionRateWindow <= 0 {
		errs = append(errs, errors.New(
			"BOT_ATTENTION_RATE_WINDOW must be positive when BOT_ATTENTION_RATE_LIMIT is enabled",
		))
	}
	if c.MediaToolLimit > 0 && c.MediaToolWindow <= 0 {
		errs = append(errs, errors.New(
			"MEDIA_TOOL_WINDOW must be positive when MEDIA_TOOL_USER_LIMIT is enabled",
		))
	}
	if strings.TrimSpace(c.SecurityStorePath) == "" {
		errs = append(errs, errors.New("SECURITY_INCIDENT_STORE_PATH must not be empty"))
	}
	return errors.Join(errs...)
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envOrAllowEmpty(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return strings.TrimSpace(value)
	}
	return fallback
}

func defaultQQNTImageAllowedRoots() string {
	userProfile := strings.TrimSpace(os.Getenv("USERPROFILE"))
	loginUIN := strings.TrimSpace(os.Getenv("QQNT_LOGIN_UIN"))
	if userProfile != "" && loginUIN != "" {
		if _, err := strconv.ParseUint(loginUIN, 10, 64); err == nil {
			return filepath.Join(
				userProfile,
				"Documents",
				"Tencent Files",
				loginUIN,
				"nt_qq",
				"nt_data",
				"Pic",
			)
		}
	}
	appData := strings.TrimSpace(os.Getenv("APPDATA"))
	if appData == "" {
		return ""
	}
	return filepath.Join(appData, "QQ")
}

func parseStringList(value string) []string {
	var result []string
	for _, field := range strings.FieldsFunc(
		strings.TrimSpace(value),
		func(r rune) bool { return r == ',' || r == ';' || unicode.IsSpace(r) },
	) {
		if field = strings.TrimSpace(field); field != "" {
			result = append(result, field)
		}
	}
	return result
}

func parsePathList(value string) []string {
	var paths []string
	// SplitList follows the host platform (':' on Unix, ';' on Windows).
	// Accept semicolons as well so a copied cross-platform configuration does
	// not silently turn multiple roots into one invalid path.
	for _, group := range strings.Split(value, ";") {
		for _, current := range filepath.SplitList(group) {
			if path := strings.TrimSpace(current); path != "" {
				paths = append(paths, path)
			}
		}
	}
	return paths
}

func defaultQQPlatform() string {
	if runtime.GOOS == "windows" {
		return QQPlatformNative
	}
	return QQPlatformOneBot
}

func validateListenAddress(value, key string) error {
	host, port, err := net.SplitHostPort(strings.TrimSpace(value))
	if err != nil {
		return fmt.Errorf("%s must be host:port: %w", key, err)
	}
	if strings.TrimSpace(port) == "" {
		return fmt.Errorf("%s port must not be empty", key)
	}
	parsed, err := strconv.Atoi(port)
	if err != nil || parsed < 0 || parsed > 65535 {
		return fmt.Errorf("%s port must be between 0 and 65535", key)
	}
	_ = host
	return nil
}

func parseList(value string) []string {
	var values []string
	for _, current := range strings.Split(value, ",") {
		if current = strings.TrimSpace(current); current != "" {
			values = append(values, current)
		}
	}
	return values
}

func deriveImageAPIURL(agentURL string) string {
	agentURL = strings.TrimRight(strings.TrimSpace(agentURL), "/")
	const suffix = "/chat/completions"
	if strings.HasSuffix(agentURL, suffix) {
		return strings.TrimSuffix(agentURL, suffix) + "/images/generations"
	}
	return ""
}

func parseBool(key string, fallback bool, errs *[]error) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s: %w", key, err))
		return fallback
	}
	return parsed
}

func parseFloat(key string, fallback, minimum, maximum float64, errs *[]error) float64 {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || parsed < minimum || parsed > maximum {
		*errs = append(*errs, fmt.Errorf("invalid %s: %q", key, value))
		return fallback
	}
	return parsed
}

func parseInt(key string, fallback, minimum int, errs *[]error) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minimum {
		*errs = append(*errs, fmt.Errorf("%s must be an integer greater than or equal to %d", key, minimum))
		return fallback
	}
	return parsed
}

func parseInt64(key string, fallback, minimum int64, errs *[]error) int64 {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < minimum {
		*errs = append(*errs, fmt.Errorf("%s must be an integer greater than or equal to %d", key, minimum))
		return fallback
	}
	return parsed
}

func parseDuration(key string, fallback time.Duration, positive bool, errs *[]error) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || (positive && parsed <= 0) || (!positive && parsed < 0) {
		requirement := "non-negative"
		if positive {
			requirement = "positive"
		}
		*errs = append(*errs, fmt.Errorf("%s must be a %s Go duration", key, requirement))
		return fallback
	}
	return parsed
}

func validateURL(value, key string, schemes ...string) error {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" || !contains(schemes, strings.ToLower(parsed.Scheme)) {
		return fmt.Errorf("%s must be an absolute %s URL", key, strings.Join(schemes, "/"))
	}
	return nil
}

func validateLoopbackAddress(value, key string) error {
	host, _, err := net.SplitHostPort(strings.TrimSpace(value))
	if err != nil {
		return fmt.Errorf("%s must be host:port: %w", key, err)
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("%s must bind to loopback", key)
	}
	return nil
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func normalizeOneBotTransport(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "ws", "websocket", "forward", "forward_ws", "forward-websocket":
		return "forward_ws"
	case "reverse", "reverse_ws", "reverse-websocket":
		return "reverse_ws"
	case "http", "sse", "http_sse", "http-sse":
		return "http_sse"
	case "reverse_http", "reverse-http", "http_client", "http-client":
		return "reverse_http"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func normalizeQQPlatform(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "native", "qqnt", "qq-native":
		return QQPlatformNative
	case "onebot", "onebot11", "ob11", "qq-onebot":
		return QQPlatformOneBot
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}
