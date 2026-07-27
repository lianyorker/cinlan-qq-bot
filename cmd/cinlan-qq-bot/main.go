package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/agent"
	"github.com/lianyorker/cinlan-qq-bot/internal/binding"
	"github.com/lianyorker/cinlan-qq-bot/internal/bot"
	"github.com/lianyorker/cinlan-qq-bot/internal/config"
	"github.com/lianyorker/cinlan-qq-bot/internal/cron"
	"github.com/lianyorker/cinlan-qq-bot/internal/filedelivery"
	"github.com/lianyorker/cinlan-qq-bot/internal/knowledge"
	"github.com/lianyorker/cinlan-qq-bot/internal/mcp"
	"github.com/lianyorker/cinlan-qq-bot/internal/onebot"
	"github.com/lianyorker/cinlan-qq-bot/internal/platform"
	onebotplatform "github.com/lianyorker/cinlan-qq-bot/internal/platform/onebot"
	qqntplatform "github.com/lianyorker/cinlan-qq-bot/internal/platform/qqnt"
	"github.com/lianyorker/cinlan-qq-bot/internal/plugin"
	"github.com/lianyorker/cinlan-qq-bot/internal/provider"
	"github.com/lianyorker/cinlan-qq-bot/internal/session"
	"github.com/lianyorker/cinlan-qq-bot/internal/skill"
	statusserver "github.com/lianyorker/cinlan-qq-bot/internal/status"
	"github.com/lianyorker/cinlan-qq-bot/internal/subagent"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "configuration error: %v\n", err)
		os.Exit(2)
	}

	logger := newLogger(cfg.LogLevel)
	slog.SetDefault(logger)

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
	var qqAdapter interface {
		platform.Adapter
		bot.Sender
	}
	switch cfg.QQPlatform {
	case config.QQPlatformNative:
		nativeAdapter, openErr := qqntplatform.NewAdapter(qqntplatform.Config{
			ListenAddr:       cfg.QQNTIPCListenAddr,
			Token:            cfg.QQNTIPCToken,
			ActionTimeout:    cfg.QQNTActionTTL,
			HandshakeTimeout: cfg.QQNTHandshakeTTL,
			MaxFrameBytes:    cfg.QQNTMaxFrameBytes,
			AutoLaunch:       cfg.QQNTAutoLaunch,
			AllowRunning:     cfg.QQNTAllowRunning,
			QQExecutable:     cfg.QQNTPath,
			LoaderPath:       cfg.QQNTLoaderPath,
			HookPath:         cfg.QQNTHookPath,
			LoadPath:         cfg.QQNTLoadPath,
			RuntimePath:      cfg.QQNTRuntimePath,
			PatchPackagePath: cfg.QQNTPatchPackagePath,
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
			qqAdapter = onebotplatform.NewAdapter(oneBotClient, logger)
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
	sessionStore, err := session.Open(cfg.MaxHistory, cfg.SessionTTL, cfg.SessionStorePath)
	if err != nil {
		logger.Error("failed to open session store", "error", err)
		os.Exit(2)
	}
	botService := bot.NewWithRuntime(
		cfg,
		agentClient,
		qqAdapter,
		sessionStore,
		logger,
		providerRegistry,
		plugin.NewRegistry(),
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
		}
		botService.SetChatBindings(bindings)
		logger.Info("chat binding config loaded", "file", cfg.ChatBindingsFile, "bindings", len(bindings.List()))
	}
	if cfg.FileCatalogPath != "" {
		fileCatalog, loadErr := filedelivery.LoadFile(cfg.FileCatalogPath, cfg.FileMaxBytes)
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
	var knowledgeStore *knowledge.Store
	if cfg.KnowledgeDir != "" {
		knowledgeStore = knowledge.NewStore()
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
	var accountRuntime statusserver.AccountRuntime
	if current, ok := qqAdapter.(statusserver.AccountRuntime); ok {
		accountRuntime = current
	}
	statusServer := statusserver.NewWithOptions(cfg.HTTPListenAddr, qqAdapter, botService, statusserver.AdminOptions{
		Token:         cfg.AdminAPIToken,
		Username:      cfg.AdminUsername,
		Password:      cfg.AdminPassword,
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
	if cfg.AdminUsername == "admin" && cfg.AdminPassword == "admin123" {
		logger.Warn("admin console is using the default credentials")
	}
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
