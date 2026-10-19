//go:build !localbusiness

package main

import (
	"context"
	"log/slog"
	"regexp"
	"slices"
	"strings"

	"github.com/lianyorker/cinlan-qq-bot/internal/bot"
	"github.com/lianyorker/cinlan-qq-bot/internal/chatprefs"
	"github.com/lianyorker/cinlan-qq-bot/internal/command"
	"github.com/lianyorker/cinlan-qq-bot/internal/config"
)

// registerLocalExtensions wires the chat preference store and the operator
// slash commands (/开启通知 /关闭通知 /记住).
func registerLocalExtensions(
	_ context.Context,
	cfg config.Config,
	service *bot.Service,
	logger *slog.Logger,
) error {
	store, err := chatprefs.Load(cfg.ChatPrefsFile)
	if err != nil {
		return err
	}
	service.SetChatPrefs(store)

	service.RegisterKeywordFile(
		regexp.MustCompile(`(?i)sql\s*文件|\.sql|数据库脚本`),
		cfg.FriendFilePath,
		"要 zkmall 的 .sql 数据库文件直接加我好友（QQ 2740954283），通过后自动发你文件。",
	)

	isOperator := func(c *command.Context) bool {
		role := strings.ToLower(c.Event.SenderRole)
		if role == "admin" || role == "owner" {
			return true
		}
		return slices.Contains(cfg.AdminUserIDs, c.Event.UserID)
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
			if err := store.SetNotifyEnabled(c.Event.MessageType, c.Event.ChatID, true); err != nil {
				return command.Result{Handled: true, Reply: "保存失败：" + err.Error()}, nil
			}
			return command.Result{Handled: true, Reply: "本群通知已开启。"}, nil
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
			if err := store.SetNotifyEnabled(c.Event.MessageType, c.Event.ChatID, false); err != nil {
				return command.Result{Handled: true, Reply: "保存失败：" + err.Error()}, nil
			}
			return command.Result{Handled: true, Reply: "本群通知已关闭，只在 @ 我时回复。"}, nil
		},
	})

	register(command.Definition{
		Name:        "记住",
		Aliases:     []string{"记", "remember"},
		Description: "把一段事实记进本群备忘，之后回答会遵循",
		Permission:  command.PermissionEveryone,
		Handler: func(_ context.Context, c *command.Context) (command.Result, error) {
			if !isOperator(c) {
				return command.Result{Handled: true, Reply: "权限不足。"}, nil
			}
			if len(c.Args) == 0 {
				return command.Result{Handled: true, Reply: "用法：/记住 要记住的内容"}, nil
			}
			if err := store.AddNote(
				c.Event.MessageType,
				c.Event.ChatID,
				strings.Join(c.Args, " "),
			); err != nil {
				return command.Result{Handled: true, Reply: "没记住：" + err.Error()}, nil
			}
			return command.Result{Handled: true, Reply: "记下了。"}, nil
		},
	})

	logger.Info("chat preference commands registered", "file", cfg.ChatPrefsFile)
	return nil
}
