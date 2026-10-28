//go:build !localbusiness

package main

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/bot"
	"github.com/lianyorker/cinlan-qq-bot/internal/chatprefs"
	"github.com/lianyorker/cinlan-qq-bot/internal/command"
	"github.com/lianyorker/cinlan-qq-bot/internal/config"
	"github.com/lianyorker/cinlan-qq-bot/internal/cron"
	"github.com/lianyorker/cinlan-qq-bot/internal/interaction"
	"github.com/lianyorker/cinlan-qq-bot/internal/media"
	"github.com/lianyorker/cinlan-qq-bot/internal/platform"
	"github.com/lianyorker/cinlan-qq-bot/internal/tool"
)

// registerLocalExtensions wires the chat preference store and the operator
// slash commands (/开启通知 /关闭通知 /记住).
func registerLocalExtensions(
	_ context.Context,
	cfg config.Config,
	service *bot.Service,
	imageGenerator *media.ImageGenerator,
	scheduler *cron.Scheduler,
	caller interaction.ActionCaller,
	platforms *platform.Registry,
	logger *slog.Logger,
) error {
	store, err := chatprefs.Load(cfg.ChatPrefsFile, platform.PlatformQQOneBot)
	if err != nil {
		return err
	}
	service.SetChatPrefs(store)
	if imageGenerator != nil {
		imageGenerator.SetReferenceResolver(service.ReferenceImageFor)
	}
	if scheduler != nil {
		if registerErr := service.ToolRegistry().Register(
			cron.Tool(scheduler),
		); registerErr != nil {
			logger.Error("failed to register schedule tool", "error", registerErr)
		}
	}
	if caller != nil {
		if registerErr := service.ToolRegistry().Register(
			interaction.PokeTool(caller),
		); registerErr != nil {
			logger.Error("failed to register poke tool", "error", registerErr)
		}
		if registerErr := service.ToolRegistry().Register(
			interaction.EmojiLikeTool(caller),
		); registerErr != nil {
			logger.Error("failed to register emoji react tool", "error", registerErr)
		}
		if registerErr := service.ToolRegistry().Register(
			interaction.SignatureTool(caller, time.Now),
		); registerErr != nil {
			logger.Error("failed to register signature tool", "error", registerErr)
		}
		if registerErr := service.ToolRegistry().Register(
			interaction.QzoneTool(caller, time.Now),
		); registerErr != nil {
			logger.Error("failed to register qzone tool", "error", registerErr)
		}
	}
	if registerErr := service.ToolRegistry().Register(
		interaction.QuietTool(store, time.Now),
	); registerErr != nil {
		logger.Error("failed to register quiet tool", "error", registerErr)
	}
	if registerErr := service.ToolRegistry().Register(
		interaction.RememberTool(store, service.ReviewNote),
	); registerErr != nil {
		logger.Error("failed to register remember tool", "error", registerErr)
	}
	// Live web lookup for things her training data misses — fresh model
	// names, group jargon, prices. Keyless DDG fallback keeps it free.
	if registerErr := service.ToolRegistry().Register(
		interaction.WebSearchTool(cfg.WebSearchAPIURL, cfg.WebSearchAPIKey, cfg.WebSearchProxy),
	); registerErr != nil {
		logger.Error("failed to register web search tool", "error", registerErr)
	}
	// channel_action bridges the agent to the platform Call surface —
	// reads open to everyone, writes/moderation admin-gated.
	if platforms != nil {
		if registerErr := service.ToolRegistry().Register(
			tool.ChannelActionTool(platforms, cfg.BotAllowedRoot),
		); registerErr != nil {
			logger.Error("failed to register channel action tool", "error", registerErr)
		}
	}

	service.SetMemeDir(
		filepath.Join(filepath.Dir(cfg.ChatPrefsFile), "chat-images", "memes"),
	)
	service.SetStickerDir(
		filepath.Join(filepath.Dir(cfg.ChatPrefsFile), "stickers"),
	)
	service.RegisterKeywordMeme(
		regexp.MustCompile(`表情包|来张|偷个|发个表情|偷表情`),
	)

	fileNotice := "要 zkmall 的 .sql 数据库文件直接加我好友，通过后自动发你文件。"
	if uin := strings.TrimSpace(cfg.BotPublicUIN); uin != "" {
		fileNotice = fmt.Sprintf(
			"要 zkmall 的 .sql 数据库文件直接加我好友（QQ %s），通过后自动发你文件。",
			uin,
		)
	}
	service.RegisterKeywordFile(
		regexp.MustCompile(`(?i)sql\s*文件|\.sql|数据库脚本`),
		cfg.FriendFilePath,
		fileNotice,
		cfg.FriendFileGroups,
	)

	isOperator := func(c *command.Context) bool {
		// Operator = whitelist only: the binding's allowed_users (Privileged)
		// or the global BOT_ADMIN_USER_IDS. Group owner/admin titles are
		// display context, not authority.
		return c.Event.Privileged ||
			slices.Contains(cfg.AdminUserIDs, c.Event.UserID)
	}

	register := func(def command.Definition) {
		if err := service.CommandRegistry().Register(def); err != nil {
			logger.Error("failed to register command", "name", def.Name, "error", err)
		}
	}

	register(command.Definition{
		Name:        "开启通知",
		Aliases:     []string{"通知开", "on"},
		Description: "开启本群主动回复（ai_decide）",
		Permission:  command.PermissionEveryone,
		Handler: func(_ context.Context, c *command.Context) (command.Result, error) {
			if !isOperator(c) {
				return command.Result{Handled: true, Reply: "权限不足。"}, nil
			}
			if err := store.SetNotifyEnabled(c.Event.Platform, c.Event.SelfID, c.Event.MessageType, c.Event.ChatID, true); err != nil {
				return command.Result{Handled: true, Reply: "保存失败：" + err.Error()}, nil
			}
			// "叫回来" lifts everything: notify on, self-mute cleared, and
			// any handoff released — the single wake command now that
			// /人工 /恢复 are gone.
			_ = store.SetMuteUntil(c.Event.Platform, c.Event.SelfID, c.Event.MessageType, c.Event.ChatID, time.Time{})
			service.ClearHandoff(c.SessionID)
			return command.Result{Handled: true, Reply: "通知已开启，装死/接管状态也一并解除。"}, nil
		},
	})

	register(command.Definition{
		Name:        "关闭通知",
		Aliases:     []string{"通知关", "off"},
		Description: "关闭本群主动回复，只在被 @ 时回答",
		Permission:  command.PermissionEveryone,
		Handler: func(_ context.Context, c *command.Context) (command.Result, error) {
			if !isOperator(c) {
				return command.Result{Handled: true, Reply: "权限不足。"}, nil
			}
			if err := store.SetNotifyEnabled(c.Event.Platform, c.Event.SelfID, c.Event.MessageType, c.Event.ChatID, false); err != nil {
				return command.Result{Handled: true, Reply: "保存失败：" + err.Error()}, nil
			}
			return command.Result{Handled: true, Reply: "本群通知已关闭，只在 @ 我时回复。"}, nil
		},
	})

	logger.Info("chat preference commands registered", "file", cfg.ChatPrefsFile)
	return nil
}
