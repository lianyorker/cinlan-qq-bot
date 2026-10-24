package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/affinity"
	"github.com/lianyorker/cinlan-qq-bot/internal/agent"
	"github.com/lianyorker/cinlan-qq-bot/internal/binding"
	"github.com/lianyorker/cinlan-qq-bot/internal/bot"
	"github.com/lianyorker/cinlan-qq-bot/internal/config"
	"github.com/lianyorker/cinlan-qq-bot/internal/cron"
	"github.com/lianyorker/cinlan-qq-bot/internal/filedelivery"
	"github.com/lianyorker/cinlan-qq-bot/internal/interaction"
	"github.com/lianyorker/cinlan-qq-bot/internal/knowledge"
	"github.com/lianyorker/cinlan-qq-bot/internal/mcp"
	"github.com/lianyorker/cinlan-qq-bot/internal/media"
	"github.com/lianyorker/cinlan-qq-bot/internal/message"
	"github.com/lianyorker/cinlan-qq-bot/internal/moderation"
	"github.com/lianyorker/cinlan-qq-bot/internal/onebot"
	"github.com/lianyorker/cinlan-qq-bot/internal/platform"
	onebotplatform "github.com/lianyorker/cinlan-qq-bot/internal/platform/onebot"
	qqntplatform "github.com/lianyorker/cinlan-qq-bot/internal/platform/qqnt"
	"github.com/lianyorker/cinlan-qq-bot/internal/plugin"
	"github.com/lianyorker/cinlan-qq-bot/internal/proactive"
	"github.com/lianyorker/cinlan-qq-bot/internal/provider"
	"github.com/lianyorker/cinlan-qq-bot/internal/reflect"
	"github.com/lianyorker/cinlan-qq-bot/internal/security"
	"github.com/lianyorker/cinlan-qq-bot/internal/session"
	"github.com/lianyorker/cinlan-qq-bot/internal/skill"
	statusserver "github.com/lianyorker/cinlan-qq-bot/internal/status"
	"github.com/lianyorker/cinlan-qq-bot/internal/subagent"
	"github.com/lianyorker/cinlan-qq-bot/internal/tool"
	"github.com/lianyorker/cinlan-qq-bot/internal/tts"
	"github.com/lianyorker/cinlan-qq-bot/internal/vecstore"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "configuration error: %v\n", err)
		os.Exit(2)
	}

	logger := newLogger(cfg.LogLevel)
	slog.SetDefault(logger)
	var imageGenerator *media.ImageGenerator
	if cfg.ImageMode != "" {
		imageGenerator, err = media.NewImageGenerator(
			media.ImageGeneratorConfig{
				Mode:        cfg.ImageMode,
				APIURL:      cfg.ImageAPIURL,
				APIKey:      cfg.ImageAPIKey,
				Model:       cfg.ImageModel,
				OutputDir:   cfg.ImageOutputDir,
				AllowedRoot: cfg.BotAllowedRoot,
				MaxBytes:    cfg.ImageMaxBytes,
				Timeout:     cfg.ImageTimeout,
				Retention:   cfg.ImageRetention,
				Enhance:     cfg.ImageEnhance,
			},
		)
		if err != nil {
			logger.Error("failed to configure image generation", "error", err)
			os.Exit(2)
		}
		logger.Info(
			"image generation configured",
			"mode", cfg.ImageMode,
			"model", cfg.ImageModel,
			"output_dir", imageGenerator.OutputDir(),
			"max_bytes", cfg.ImageMaxBytes,
		)
	}
	var webScreenshot *media.WebScreenshot
	if cfg.WebScreenshotEnabled {
		webScreenshot, err = media.NewWebScreenshot(
			media.WebScreenshotConfig{
				BrowserPath:  cfg.WebScreenshotBrowserPath,
				AllowedHosts: cfg.WebScreenshotAllowedHosts,
				OutputDir:    cfg.WebScreenshotOutputDir,
				AllowedRoot:  cfg.BotAllowedRoot,
				MaxBytes:     cfg.WebScreenshotMaxBytes,
				Timeout:      cfg.WebScreenshotTimeout,
				Retention:    cfg.WebScreenshotRetention,
				Width:        cfg.WebScreenshotWidth,
				Height:       cfg.WebScreenshotHeight,
				Wait:         cfg.WebScreenshotWait,
			},
		)
		if err != nil {
			logger.Error("failed to configure web screenshots", "error", err)
			os.Exit(2)
		}
		logger.Info(
			"web screenshot configured",
			"browser", cfg.WebScreenshotBrowserPath,
			"allowed_hosts", len(cfg.WebScreenshotAllowedHosts),
			"output_dir", webScreenshot.OutputDir(),
			"max_bytes", cfg.WebScreenshotMaxBytes,
		)
	}
	if len(os.Args) == 2 && os.Args[1] == "--migrate-sessions-only" {
		sessionStore, openErr := openSessionStore(cfg)
		if openErr != nil {
			logger.Error("session migration failed", "error", openErr)
			os.Exit(2)
		}
		if closeErr := sessionStore.Close(); closeErr != nil {
			logger.Error("close migrated session store", "error", closeErr)
			os.Exit(2)
		}
		logger.Info("encrypted SQLite session migration complete", "file", cfg.SessionStorePath)
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	providerRegistry := provider.NewRegistry()
	agentClients := make(map[string]*agent.HTTPClient)
	var agentClient *agent.HTTPClient
	if cfg.ProvidersFile != "" {
		providerConfigs, defaultProvider, loadErr := provider.LoadFile(cfg.ProvidersFile)
		if loadErr != nil {
			logger.Error("failed to load provider config", "error", loadErr)
			os.Exit(2)
		}
		for _, providerConfig := range providerConfigs {
			if !providerConfig.Enabled {
				continue
			}
			client := agent.NewHTTPClient(
				providerConfig.Agent,
				logger.With("provider", providerConfig.Name),
			)
			if registerErr := providerRegistry.Register(provider.Wrap(
				providerConfig.Name,
				providerConfig.Kind,
				client,
			)); registerErr != nil {
				logger.Error("failed to register provider", "provider", providerConfig.Name, "error", registerErr)
				os.Exit(2)
			}
			agentClients[providerConfig.Name] = client
		}
		if defaultErr := providerRegistry.SetDefault(defaultProvider); defaultErr != nil {
			logger.Error("failed to set default provider", "provider", defaultProvider, "error", defaultErr)
			os.Exit(2)
		}
		agentClient = agentClients[defaultProvider]
	} else {
		agentClient = agent.NewHTTPClient(agent.Config{
			Mode:         cfg.AgentMode,
			URL:          cfg.AgentAPIURL,
			APIKey:       cfg.AgentAPIKey,
			Model:        cfg.AgentModel,
			SystemPrompt: cfg.AgentSystemPrompt,
			AuthHeader:   cfg.AgentAuthHeader,
			AuthScheme:   cfg.AgentAuthScheme,
			Timeout:      cfg.AgentTimeout,
			MaxRetries:   cfg.AgentMaxRetries,
			RetryBase:    cfg.AgentRetryBase,
			RetryMax:     cfg.AgentRetryMax,
		}, logger)
		if registerErr := providerRegistry.Register(provider.Wrap("default", "agent", agentClient)); registerErr != nil {
			logger.Error("failed to register default provider", "error", registerErr)
			os.Exit(2)
		}
		agentClients["default"] = agentClient
	}
	for name, client := range agentClients {
		if imageErr := client.SetLocalImagePolicy(
			cfg.QQNTImageAllowedRoots,
			cfg.QQNTImageMaxBytes,
		); imageErr != nil {
			logger.Error(
				"failed to configure local image input",
				"provider",
				name,
				"error",
				imageErr,
			)
			os.Exit(2)
		}
	}
	logger.Info(
		"local image input policy configured",
		"allowed_roots",
		len(cfg.QQNTImageAllowedRoots),
		"max_bytes",
		cfg.QQNTImageMaxBytes,
	)
	var qqAdapter interface {
		platform.Adapter
		bot.Sender
	}
	switch cfg.QQPlatform {
	case config.QQPlatformNative:
		var imageSendRoots []string
		if imageGenerator != nil {
			imageSendRoots = []string{imageGenerator.OutputDir()}
		}
		if webScreenshot != nil {
			imageSendRoots = appendUniquePath(imageSendRoots, webScreenshot.OutputDir())
		}
		nativeAdapter, openErr := qqntplatform.NewAdapter(qqntplatform.Config{
			ListenAddr:       cfg.QQNTIPCListenAddr,
			Token:            cfg.QQNTIPCToken,
			ActionTimeout:    cfg.QQNTActionTTL,
			HandshakeTimeout: cfg.QQNTHandshakeTTL,
			MaxFrameBytes:    cfg.QQNTMaxFrameBytes,
			AutoLaunch:       cfg.QQNTAutoLaunch,
			AllowRunning:     cfg.QQNTAllowRunning,
			AutoAcceptFriend: cfg.QQNTAutoAcceptFriend,
			Headless:         cfg.QQNTHeadless,
			QQExecutable:     cfg.QQNTPath,
			LoaderPath:       cfg.QQNTLoaderPath,
			HookPath:         cfg.QQNTHookPath,
			LoadPath:         cfg.QQNTLoadPath,
			RuntimePath:      cfg.QQNTRuntimePath,
			PatchPackagePath: cfg.QQNTPatchPackagePath,
			ImageSendRoots:   imageSendRoots,
			ImageMaxBytes:    maxInt64(cfg.ImageMaxBytes, cfg.WebScreenshotMaxBytes),
		}, logger)
		if openErr != nil {
			logger.Error("failed to configure QQNT runtime", "error", openErr)
			os.Exit(2)
		}
		qqAdapter = nativeAdapter
	case config.QQPlatformOneBot:
		if cfg.OneBotAccountsFile != "" {
			multiAdapter, openErr := onebotplatform.OpenMulti(
				cfg.OneBotAccountsFile,
				cfg.OneBotActionTTL,
				logger,
			)
			if openErr != nil {
				logger.Error("failed to load OneBot accounts", "error", openErr)
				os.Exit(2)
			}
			multiAdapter.SetAutoAcceptFriend(cfg.OneBotAutoAccept)
			multiAdapter.SetFriendWelcome(cfg.FriendWelcomeText, cfg.FriendFilePath, cfg.FriendFileGroups)
			multiAdapter.SetGroupWelcome(cfg.GroupWelcomeText, cfg.GroupWelcomeImage, cfg.GroupWelcomeGroups)
			qqAdapter = multiAdapter
		} else {
			oneBotURL := cfg.OneBotWSURL
			if cfg.OneBotTransport == onebot.TransportHTTPSSE ||
				cfg.OneBotTransport == onebot.TransportReverseHTTP {
				oneBotURL = cfg.OneBotHTTPURL
			}
			oneBotClient := onebot.NewClient(onebot.ClientConfig{
				URL:           oneBotURL,
				AccessToken:   cfg.OneBotAccessToken,
				ActionTimeout: cfg.OneBotActionTTL,
				Transport:     cfg.OneBotTransport,
				ListenAddr:    cfg.OneBotListenAddr,
				Path:          cfg.OneBotReversePath,
			}, logger)
			oneBotAdapter := onebotplatform.NewAdapter(oneBotClient, logger)
			oneBotAdapter.AutoAcceptFriend = cfg.OneBotAutoAccept
			oneBotAdapter.FriendWelcomeText = cfg.FriendWelcomeText
			oneBotAdapter.FriendFilePath = cfg.FriendFilePath
			oneBotAdapter.FriendFileGroups = cfg.FriendFileGroups
			oneBotAdapter.GroupWelcomeImage = cfg.GroupWelcomeImage
			oneBotAdapter.GroupWelcomeText = cfg.GroupWelcomeText
			oneBotAdapter.GroupWelcomeGroups = make(map[string]bool, len(cfg.GroupWelcomeGroups))
			for _, groupID := range cfg.GroupWelcomeGroups {
				oneBotAdapter.GroupWelcomeGroups[groupID] = true
			}
			qqAdapter = oneBotAdapter
		}
	default:
		logger.Error("unsupported QQ platform", "platform", cfg.QQPlatform)
		os.Exit(2)
	}
	scheduler, err := cron.Open(qqAdapter, logger, cfg.CronStorePath)
	if err != nil {
		logger.Error("failed to open cron store", "error", err)
		os.Exit(2)
	}
	platforms := platform.NewRegistry()
	if err := platforms.Register(qqAdapter); err != nil {
		logger.Error("failed to register platform adapter", "error", err)
		os.Exit(2)
	}
	sessionStore, err := openSessionStore(cfg)
	if err != nil {
		logger.Error("failed to open encrypted SQLite session store", "error", err)
		os.Exit(2)
	}
	defer func() {
		if closeErr := sessionStore.Close(); closeErr != nil {
			logger.Error("failed to close encrypted SQLite session store", "error", closeErr)
		}
	}()
	botService := bot.NewWithRuntime(
		cfg,
		agentClient,
		qqAdapter,
		sessionStore,
		logger,
		providerRegistry,
		plugin.NewRegistry(),
	)
	if fileSender, ok := qqAdapter.(bot.FileSender); ok {
		botService.SetFileSender(fileSender)
	}
	if fetcher, ok := qqAdapter.(bot.MessageFetcher); ok {
		botService.SetMessageFetcher(fetcher)
	}
	if moderator, ok := qqAdapter.(bot.GroupModerator); ok {
		botService.SetGroupModerator(moderator)
	}
	if checker, ok := qqAdapter.(bot.GroupMemberChecker); ok {
		botService.SetGroupMemberChecker(checker)
	}
	if profiler, ok := qqAdapter.(bot.UserProfiler); ok {
		botService.SetUserProfiler(profiler)
	}
	peerSelfIDs := append([]string(nil), cfg.PeerBotIDs...)
	if multi, ok := qqAdapter.(*onebotplatform.MultiAdapter); ok {
		accounts := multi.Accounts()
		if len(accounts) > 1 {
			for _, account := range accounts {
				peerSelfIDs = append(peerSelfIDs, account.SelfID)
			}
		}
	}
	if len(peerSelfIDs) > 0 {
		botService.SetPeerSelfIDs(peerSelfIDs)
		logger.Info("peer bot accounts registered", "accounts", len(peerSelfIDs))
	}
	// Same accounts never get a welcome — a peer bot joining shouldn't be
	// greeted like a new member.
	if len(peerSelfIDs) > 0 {
		if multi, ok := qqAdapter.(*onebotplatform.MultiAdapter); ok {
			multi.SetWelcomeSkip(peerSelfIDs)
		} else if single, ok := qqAdapter.(*onebotplatform.Adapter); ok {
			single.WelcomeSkipUserIDs = make(map[string]bool, len(peerSelfIDs))
			for _, id := range peerSelfIDs {
				single.WelcomeSkipUserIDs[id] = true
			}
		}
	}
	if warnStore, warnErr := moderation.Open(
		filepath.Join(filepath.Dir(cfg.ChatPrefsFile), "moderation.json"),
	); warnErr != nil {
		logger.Warn("moderation store unavailable", "error", warnErr)
	} else {
		botService.SetModerationStore(warnStore)
	}
	if affinityStore, affinityErr := affinity.Load(cfg.AffinityFile); affinityErr != nil {
		logger.Warn("affinity store unavailable", "error", affinityErr)
	} else {
		botService.SetAffinity(affinityStore)
		logger.Info("affinity tracking enabled", "file", cfg.AffinityFile)
	}
	if voiceProvider, voiceErr := tts.New(tts.Config{
		Mode:           cfg.VoiceMode,
		Voice:          cfg.VoiceName,
		Style:          cfg.VoiceStyle,
		APIURL:         cfg.VoiceAPIURL,
		APIKey:         cfg.VoiceAPIKey,
		Model:          cfg.VoiceModel,
		Command:        cfg.VoiceCommand,
		Timeout:        cfg.VoiceTimeout,
		VolcAppID:      cfg.VoiceVolcAppID,
		VolcToken:      cfg.VoiceVolcToken,
		VolcResourceID: cfg.VoiceVolcResourceID,
		VolcEmotion:    cfg.VoiceVolcEmotion,
	}); voiceErr != nil {
		logger.Warn("voice tts unavailable", "error", voiceErr)
	} else if voiceProvider != nil {
		botService.SetVoiceProvider(voiceProvider)
		logger.Info(
			"voice tts enabled",
			"mode", cfg.VoiceMode,
			"voice", cfg.VoiceName,
			"output_dir", cfg.VoiceDir,
		)
	}
	mediaLimiter := tool.NewMediaLimiter(tool.MediaLimiterConfig{
		Cooldown:      cfg.MediaToolCooldown,
		Limit:         cfg.MediaToolLimit,
		Window:        cfg.MediaToolWindow,
		MaxConcurrent: cfg.MediaToolConcurrency,
	})
	securityPolicy, securityErr := security.OpenPolicy(
		cfg.BotAllowedRoot,
		cfg.SecurityStorePath,
		logger,
	)
	if securityErr != nil {
		logger.Error("failed to configure security policy", "error", securityErr)
		os.Exit(2)
	}
	botService.SetMessageGuard(securityPolicy)
	botService.ToolRegistry().SetGuard(securityPolicy)
	logger.Info(
		"security policy enabled",
		"allowed_root", securityPolicy.AllowedRoot(),
		"incident_store", securityPolicy.IncidentStorePath(),
	)
	if cfg.PersonasFile != "" {
		if loadErr := botService.PersonaRegistry().UseFile(cfg.PersonasFile); loadErr != nil {
			logger.Error("failed to load persona config", "error", loadErr)
			os.Exit(2)
		}
		logger.Info(
			"persona config loaded",
			"file", cfg.PersonasFile,
			"personas", len(botService.PersonaRegistry().List()),
		)
	}
	// Persona packs (dir-of-dirs format) override personas.json when the
	// configured directory exists — mutations then persist back per-pack.
	if cfg.PersonaPacksDir != "" {
		if packErr := botService.PersonaRegistry().UsePacksDir(cfg.PersonaPacksDir); packErr != nil {
			logger.Error("failed to load persona packs", "error", packErr)
			os.Exit(2)
		}
		if botService.PersonaRegistry().PacksDir() != "" {
			logger.Info(
				"persona packs loaded",
				"dir", cfg.PersonaPacksDir,
				"personas", len(botService.PersonaRegistry().List()),
			)
		}
	}
	var bindings *binding.Registry
	if cfg.ChatBindingsFile != "" {
		var loadErr error
		bindings, loadErr = binding.OpenFile(cfg.ChatBindingsFile)
		if loadErr != nil {
			logger.Error("failed to load chat binding config", "error", loadErr)
			os.Exit(2)
		}
		for _, current := range bindings.List() {
			if current.Persona != "" {
				if _, ok := botService.PersonaRegistry().Get(current.Persona); !ok {
					logger.Error(
						"chat binding references an unknown persona",
						"binding", current.Name,
						"persona", current.Persona,
					)
					os.Exit(2)
				}
			}
			if current.Provider != "" && !providerRegistry.Has(current.Provider) {
				logger.Error(
					"chat binding references an unknown provider",
					"binding", current.Name,
					"provider", current.Provider,
				)
				os.Exit(2)
			}
			if current.ReplyPolicy != nil && current.ReplyPolicy.Provider != "" &&
				!providerRegistry.Has(current.ReplyPolicy.Provider) {
				logger.Error(
					"chat binding reply policy references an unknown provider",
					"binding", current.Name,
					"provider", current.ReplyPolicy.Provider,
				)
				os.Exit(2)
			}
		}
		botService.SetChatBindings(bindings)
		logger.Info("chat binding config loaded", "file", cfg.ChatBindingsFile, "bindings", len(bindings.List()))
	}
	if cfg.FileCatalogPath != "" {
		fileCatalog, loadErr := filedelivery.LoadFileWithinRoot(
			cfg.FileCatalogPath,
			cfg.FileMaxBytes,
			cfg.BotAllowedRoot,
		)
		if loadErr != nil {
			logger.Error("failed to load file catalog", "file", cfg.FileCatalogPath, "error", loadErr)
			os.Exit(2)
		}
		if entries := fileCatalog.List(); len(entries) > 0 {
			if registerErr := botService.ToolRegistry().Register(
				fileCatalog.Tool(qqAdapter, cfg.FileSendTimeout),
			); registerErr != nil {
				logger.Error("failed to register file delivery tool", "error", registerErr)
				os.Exit(2)
			}
			logger.Info("file catalog loaded", "file", cfg.FileCatalogPath, "files", len(entries))
		} else {
			logger.Warn("file catalog contains no enabled files", "file", cfg.FileCatalogPath)
		}
	}
	// Micro-action tools need a raw-action channel: multi-account adapters
	// expose CallFor(selfID, ...); single-account ones only Call, so wrap it.
	var actionCaller interaction.ActionCaller
	if multi, ok := qqAdapter.(interface {
		CallFor(context.Context, string, string, map[string]any) (any, error)
	}); ok {
		actionCaller = multi
	} else if single, ok := qqAdapter.(interface {
		Call(context.Context, string, map[string]any) (any, error)
	}); ok {
		actionCaller = interaction.Wrap(single.Call)
	}
	if extensionErr := registerLocalExtensions(
		ctx, cfg, botService, imageGenerator, scheduler, actionCaller, platforms, logger,
	); extensionErr != nil {
		logger.Error("failed to register local extensions", "error", extensionErr)
		os.Exit(2)
	}

	// Ambient opener: the free-range piece — persona may speak first in a
	// quiet group. Gates: QQ_PROACTIVE, notify switch, mute, sleep window.
	platformName := platform.PlatformQQOneBot
	if cfg.QQPlatform == "qqnt" {
		platformName = platform.PlatformQQNative
	}
	// QQ_PROACTIVE_GROUPS (optional) hard-limits which bound groups get
	// proactive openers; unset means every bound group may.
	var proactiveAllow func(string) bool
	if cfg.ProactiveGroups.Size() > 0 || cfg.ProactiveGroups.Wildcard() {
		proactiveAllow = cfg.ProactiveGroups.Allows
	}
	// Private proactive is opt-in only: QQ_PROACTIVE_PRIVATE must name
	// the user QQ(s) she may message first — unset keeps DMs silent.
	var proactivePrivateAllow func(string) bool
	if cfg.ProactivePrivate.Size() > 0 || cfg.ProactivePrivate.Wildcard() {
		proactivePrivateAllow = cfg.ProactivePrivate.Allows
	}
	go proactive.New(
		proactive.Config{
			Enabled:      cfg.Proactive,
			Tick:         cfg.ProactiveTick,
			MinIdle:      cfg.ProactiveIdle,
			Cooldown:     cfg.ProactiveWait,
			Chance:       cfg.ProactiveProb,
			DailyLimit:   cfg.ProactiveDaily,
			SleepStart:   cfg.SleepStart,
			SleepEnd:     cfg.SleepEnd,
			Allow:        proactiveAllow,
			PrivateAllow: proactivePrivateAllow,
			PrivateIdle:  cfg.ProactivePrivateIdle,
			StaleMax:     cfg.ProactiveStaleMax,
		},
		proactive.Deps{
			Providers: providerRegistry,
			Send: func(sendCtx context.Context, out platform.Outbound) error {
				return qqAdapter.Send(sendCtx, out)
			},
			Bindings:    bindings,
			Personas:    botService.PersonaRegistry(),
			Sessions:    sessionStore,
			ChatPrefs:   botService.ChatPrefs(),
			Activity:    botService.LastActivity,
			Touch:       botService.TouchActivity,
			Stickers:    botService.StickerPool,
			ResolveMeme: botService.MemeFile,
			Platform:    platformName,
			Logger:      logger,
		},
	).Run(ctx)
	// Memory-keeping: periodically merges bloated fact/impression
	// lists and writes one diary line per active chat per day — the
	// housekeeping that keeps her memory continuous instead of a pile.
	go reflect.New(
		reflect.Config{
			Enabled:            cfg.Reflect,
			Tick:               cfg.ReflectTick,
			MergeFactsAt:       cfg.ReflectMergeFacts,
			MergeImpressionsAt: cfg.ReflectMergeImps,
			JournalMaxAge:      cfg.ReflectJournalAge,
		},
		reflect.Deps{
			Providers: providerRegistry,
			Bindings:  bindings,
			Personas:  botService.PersonaRegistry(),
			Sessions:  sessionStore,
			ChatPrefs: botService.ChatPrefs(),
			Activity:  botService.LastActivity,
			Platform:  platformName,
			Logger:    logger,
		},
	).Run(ctx)
	mediaLimiter.Notify = func(name string, actor tool.Actor, used, limit int) {
		if name != "generate_image" || actor.ChatID == "" {
			return
		}
		notice := fmt.Sprintf("正在生图，请稍等~")
		if limit > 0 && used > 0 {
			notice = fmt.Sprintf("正在生图，请稍等~\n今日额度：%d/%d", used, limit)
		}
		if err := qqAdapter.Send(context.Background(), platform.Outbound{
			ChatType: actor.ChatType,
			ChatID:   actor.ChatID,
			SelfID:   actor.SelfID,
			ReplyTo:  actor.MessageID,
			Quote:    true,
			Chain:    message.Chain{message.Text(notice)},
		}); err != nil {
			logger.Debug("media notify send failed", "error", err.Error())
		}
	}
	if imageGenerator != nil {
		if registerErr := botService.ToolRegistry().Register(
			mediaLimiter.Wrap(imageGenerator.Tool()),
		); registerErr != nil {
			logger.Error("failed to register image generation tool", "error", registerErr)
			os.Exit(2)
		}
		botService.RegisterKeywordPromptTool(
			regexp.MustCompile(`生图|画图|画一张|画个|帮我画|来一张|生成图片`),
			"generate_image",
			"prompt",
		)
		logger.Info("image generation tool enabled")
	}
	if iqChart, iqErr := media.NewIQChart(media.IQChartConfig{
		APIBase:   cfg.IQChartAPIBase,
		Interval:  cfg.IQChartInterval,
		OutputDir: filepath.Join("data", "model-iq"),
		FontPath:  cfg.IQChartFontPath,
		Logger:    logger,
	}); iqErr != nil {
		logger.Warn("model iq chart disabled", "error", iqErr)
	} else {
		if registerErr := botService.ToolRegistry().Register(
			iqChart.Tool(),
		); registerErr != nil {
			logger.Error("failed to register model iq chart tool", "error", registerErr)
		} else {
			botService.RegisterKeywordTool(
				regexp.MustCompile(`(?i)模型\s*iq|iq\s*天梯|雷达分`),
				"model_iq_chart",
			)
			go iqChart.Run(ctx)
			logger.Info("model iq chart enabled", "interval", cfg.IQChartInterval)
		}
	}
	if webScreenshot != nil {
		if registerErr := botService.ToolRegistry().Register(
			mediaLimiter.Wrap(webScreenshot.Tool()),
		); registerErr != nil {
			logger.Error("failed to register web screenshot tool", "error", registerErr)
			os.Exit(2)
		}
		logger.Info("web screenshot tool enabled")
	}
	var pluginManager *plugin.WebhookManager
	if cfg.PluginsFile != "" {
		pluginManager, err = plugin.OpenWebhookManager(
			cfg.PluginsFile,
			botService.PluginRegistry(),
			logger,
		)
		if err != nil {
			logger.Error("failed to load plugin webhook config", "error", err)
			os.Exit(2)
		}
		logger.Info("plugin webhook config loaded", "file", cfg.PluginsFile, "plugins", len(pluginManager.List()))
	}
	var skillStore *skill.Store
	if cfg.SkillsDir != "" {
		skillStore, err = skill.LoadDir(cfg.SkillsDir)
		if err != nil {
			logger.Error("failed to load skills directory", "directory", cfg.SkillsDir, "error", err)
			os.Exit(2)
		}
		if registerErr := skillStore.RegisterTools(botService.ToolRegistry()); registerErr != nil {
			logger.Error("failed to register skill tools", "error", registerErr)
			os.Exit(2)
		}
		if registerErr := botService.PluginRegistry().Register(skill.PromptPlugin{Store: skillStore}); registerErr != nil {
			logger.Error("failed to register skills plugin", "error", registerErr)
			os.Exit(2)
		}
		logger.Info("skills directory loaded", "directory", skillStore.Dir(), "skills", len(skillStore.List()))
	}
	var mcpManager *mcp.Manager
	if cfg.MCPServersFile != "" {
		mcpManager, err = mcp.Open(cfg.MCPServersFile, botService.ToolRegistry(), logger)
		if err != nil {
			logger.Error("failed to load MCP server config", "error", err)
			os.Exit(2)
		}
		if refreshErr := mcpManager.Refresh(ctx); refreshErr != nil {
			logger.Warn("one or more MCP servers are unavailable", "error", refreshErr)
		}
	}
	var subagentManager *subagent.Manager
	if cfg.SubagentsFile != "" {
		subagentManager, err = subagent.OpenWithProviders(
			cfg.SubagentsFile,
			agentClients,
			providerRegistry.DefaultName(),
			botService.ToolRegistry(),
			logger,
			cfg.AgentMaxToolRounds,
		)
		if err != nil {
			logger.Error("failed to load subagent config", "error", err)
			os.Exit(2)
		}
		logger.Info("subagent config loaded", "file", cfg.SubagentsFile, "agents", len(subagentManager.List()))
	}
	for _, client := range agentClients {
		client.SetTools(botService.ToolRegistry(), cfg.AgentMaxToolRounds)
	}
	vecEngine := vecstore.New(vecstore.Config{
		APIURL:    cfg.EmbeddingAPIURL,
		APIKey:    cfg.EmbeddingAPIKey,
		Model:     cfg.EmbeddingModel,
		CachePath: cfg.EmbeddingCacheFile,
	})
	defer func() { _ = vecEngine.Flush() }()
	if botService.ChatPrefs() != nil {
		botService.ChatPrefs().SetVecEngine(vecEngine)
	}
	var knowledgeStore *knowledge.Store
	if cfg.KnowledgeDir != "" {
		knowledgeStore = knowledge.NewStore()
		knowledgeStore.SetVecEngine(vecEngine)
		count, loadErr := knowledgeStore.LoadDir(cfg.KnowledgeDir)
		if loadErr != nil {
			logger.Error("failed to load knowledge directory", "directory", cfg.KnowledgeDir, "error", loadErr)
			os.Exit(2)
		}
		if count > 0 {
			if registerErr := botService.PluginRegistry().Register(knowledge.Plugin{
				Store: knowledgeStore,
				TopK:  cfg.KnowledgeTopK,
			}); registerErr != nil {
				logger.Error("failed to register knowledge plugin", "error", registerErr)
				os.Exit(2)
			}
		}
		logger.Info("knowledge directory loaded", "directory", cfg.KnowledgeDir, "documents", count)
	}
	if err := validateBindingResources(
		bindings,
		botService.ToolRegistry(),
		skillStore,
		knowledgeStore,
		mcpManager,
	); err != nil {
		logger.Error("chat binding resource isolation is invalid", "error", err)
		os.Exit(2)
	}
	var accountRuntime statusserver.AccountRuntime
	if current, ok := qqAdapter.(statusserver.AccountRuntime); ok {
		accountRuntime = current
	}
	statusServer := statusserver.NewWithOptions(cfg.HTTPListenAddr, qqAdapter, botService, statusserver.AdminOptions{
		Token:         cfg.AdminAPIToken,
		Providers:     botService.ProviderRegistry(),
		Plugins:       botService.PluginRegistry(),
		PluginRuntime: pluginManager,
		Commands:      botService.CommandRegistry(),
		Platforms:     platforms,
		Tools:         botService.ToolRegistry(),
		MCP:           mcpManager,
		Skills:        skillStore,
		Knowledge:     knowledgeStore,
		Subagents:     subagentManager,
		Accounts:      accountRuntime,
		Actions:       qqAdapter,
		Cron:          scheduler,
		Personas:      botService.PersonaRegistry(),
		Bindings:      bindings,
		Sessions:      sessionStore,
		Pipeline:      botService,
	})

	logger.Info(
		"cinlan qq bot starting",
		"agent_mode", cfg.AgentMode,
		"qq_platform", cfg.QQPlatform,
		"onebot_transport", cfg.OneBotTransport,
		"onebot_accounts_file", cfg.OneBotAccountsFile,
		"group_allowlist_size", cfg.GroupAllowlist.Size(),
		"group_allowlist_wildcard", cfg.GroupAllowlist.Wildcard(),
		"private_allowlist_size", cfg.PrivateAllowlist.Size(),
		"private_allowlist_wildcard", cfg.PrivateAllowlist.Wildcard(),
		"require_mention", cfg.RequireMention,
		"group_batch_window", cfg.GroupBatchWindow,
		"reply_part_delay", cfg.ReplyPartDelay,
		"workers", cfg.MaxConcurrency,
		"http_listen_addr", cfg.HTTPListenAddr,
		"session_store_path", sessionStore.Path(),
		"cron_store_path", scheduler.Path(),
		"mcp_servers_file", cfg.MCPServersFile,
		"subagents_file", cfg.SubagentsFile,
		"plugins_file", cfg.PluginsFile,
		"providers_file", cfg.ProvidersFile,
		"admin_api_configured", cfg.AdminAPIToken != "",
	)
	if cfg.QQPlatform == config.QQPlatformOneBot &&
		cfg.OneBotAccountsFile == "" &&
		cfg.OneBotAccessToken == "" {
		logger.Warn("ONEBOT_ACCESS_TOKEN is empty; use a token unless OneBot is strictly isolated")
	}
	if cfg.GroupAllowlist.Wildcard() {
		logger.Warn("QQ_GROUP_ALLOWLIST allows every group")
	}
	if cfg.PrivateAllowlist.Wildcard() {
		logger.Warn("QQ_PRIVATE_ALLOWLIST allows every private chat")
	}

	errCh := make(chan error, 2)
	var waitGroup sync.WaitGroup
	run := func(name string, action func() error) {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			if runErr := action(); runErr != nil && ctx.Err() == nil {
				select {
				case errCh <- fmt.Errorf("%s: %w", name, runErr):
				case <-ctx.Done():
				}
			}
		}()
	}

	run("qq platform", func() error {
		return qqAdapter.Run(ctx)
	})
	run("status server", func() error {
		return statusServer.Run(ctx)
	})
	run("cron scheduler", func() error {
		return scheduler.Run(ctx)
	})

	waitGroup.Add(2)
	go func() {
		defer waitGroup.Done()
		botService.RunPlatform(ctx, qqAdapter.Events())
	}()
	go func() {
		defer waitGroup.Done()
		sessionStore.RunJanitor(ctx)
	}()

	select {
	case <-ctx.Done():
	case runtimeErr := <-errCh:
		logger.Error("runtime component failed", "error", runtimeErr)
		cancel()
	}

	cancel()
	waitGroup.Wait()
	if mcpManager != nil {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		if closeErr := mcpManager.Close(closeCtx); closeErr != nil {
			logger.Warn("failed to close MCP sessions", "error", closeErr)
		}
		closeCancel()
	}
	logger.Info("cinlan qq bot stopped")
}

func openSessionStore(cfg config.Config) (*session.Store, error) {
	var store *session.Store
	var err error
	if cfg.SessionStorePath == "" {
		store = session.New(cfg.MaxHistory, cfg.SessionTTL)
	} else {
		store, err = session.OpenEncryptedSQLite(
			cfg.MaxHistory,
			cfg.SessionTTL,
			cfg.SessionStorePath,
			cfg.SessionKey,
			cfg.SessionLegacyPath,
		)
	}
	if err != nil {
		return nil, err
	}
	store.SetSemanticTTL(cfg.SessionSemanticTTL)
	return store, nil
}

func validateBindingResources(
	bindings *binding.Registry,
	tools *tool.Registry,
	skills *skill.Store,
	knowledgeStore *knowledge.Store,
	mcpManager *mcp.Manager,
) error {
	if bindings == nil {
		return nil
	}
	skillNames := make(map[string]struct{})
	if skills != nil {
		for _, current := range skills.List() {
			skillNames[current.Name] = struct{}{}
		}
	}
	knowledgeNames := make(map[string]struct{})
	if knowledgeStore != nil {
		for _, current := range knowledgeStore.Collections() {
			knowledgeNames[current] = struct{}{}
		}
	}
	mcpNames := make(map[string]struct{})
	if mcpManager != nil {
		for _, current := range mcpManager.List() {
			mcpNames[current.Name] = struct{}{}
		}
	}
	for _, current := range bindings.List() {
		for _, name := range current.Tools {
			if tools == nil || !tools.Has(name) {
				return fmt.Errorf("binding %q references unknown tool %q", current.Name, name)
			}
		}
		for _, name := range current.Skills {
			if _, ok := skillNames[name]; !ok {
				return fmt.Errorf("binding %q references unknown skill %q", current.Name, name)
			}
		}
		for _, name := range current.KnowledgeBases {
			if _, ok := knowledgeNames[name]; !ok {
				return fmt.Errorf(
					"binding %q references unknown knowledge base %q",
					current.Name,
					name,
				)
			}
		}
		for _, name := range current.MCPServers {
			if _, ok := mcpNames[name]; !ok {
				return fmt.Errorf("binding %q references unknown MCP server %q", current.Name, name)
			}
		}
	}
	return nil
}

func newLogger(levelName string) *slog.Logger {
	level := slog.LevelInfo
	switch levelName {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}

func appendUniquePath(paths []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return paths
	}
	for _, current := range paths {
		if strings.EqualFold(strings.TrimSpace(current), value) {
			return paths
		}
	}
	return append(paths, value)
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
