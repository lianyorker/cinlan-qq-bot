//go:build !localbusiness

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

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

	service.SetMemeDir(
		filepath.Join(filepath.Dir(cfg.ChatPrefsFile), "chat-images", "memes"),
	)
	service.RegisterKeywordMeme(
		regexp.MustCompile(`表情包|来张|偷个|发个表情|偷表情`),
	)

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
		Handler: func(ctx context.Context, c *command.Context) (command.Result, error) {
			if !isOperator(c) {
				return command.Result{Handled: true, Reply: "权限不足。"}, nil
			}
			if len(c.Args) == 0 {
				return command.Result{Handled: true, Reply: "用法：/记住 要记住的内容"}, nil
			}
			text := strings.Join(c.Args, " ")
			if err := store.AddNote(
				c.Event.MessageType,
				c.Event.ChatID,
				text,
			); err != nil {
				return command.Result{Handled: true, Reply: "没记住：" + err.Error()}, nil
			}
			// An attached or quoted image becomes a named reference for 生图.
			if refs := service.EventImageRefs(
				ctx, c.Event.SelfID, c.Event.Chain,
			); len(refs) > 0 {
				name := c.Args[0]
				saved, saveErr := saveImageRef(
					refs[0],
					filepath.Join(filepath.Dir(cfg.ChatPrefsFile), "chat-images"),
					name,
				)
				if saveErr != nil {
					return command.Result{Handled: true, Reply: "记下了（图没存成：" + saveErr.Error() + "）。"}, nil
				}
				if err := store.AddImageNote(
					c.Event.MessageType,
					c.Event.ChatID,
					name,
					saved,
				); err == nil {
					return command.Result{Handled: true, Reply: "记下了（含图，生图提到「" + name + "」会拿它当参考）。"}, nil
				}
			}
			return command.Result{Handled: true, Reply: "记下了。"}, nil
		},
	})

	logger.Info("chat preference commands registered", "file", cfg.ChatPrefsFile)
	return nil
}

// saveImageRef persists an image reference (https URL, file:// URI or local
// path) into dir under a name-keyed filename so remembered images outlive
// expiring QQ CDN links.
func saveImageRef(ref, dir, name string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", errors.New("empty image reference")
	}
	var data []byte
	lower := strings.ToLower(ref)
	switch {
	case strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://"):
		client := &http.Client{Timeout: 15 * time.Second}
		resp, err := client.Get(ref)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("download http %d", resp.StatusCode)
		}
		limited, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
		if err != nil {
			return "", err
		}
		data = limited
	default:
		local := strings.TrimPrefix(ref, "file://")
		if runtime.GOOS == "windows" {
			local = strings.ReplaceAll(local, "/", "\\")
		}
		read, err := os.ReadFile(local)
		if err != nil {
			return "", err
		}
		if len(read) > 20<<20 {
			return "", errors.New("image too large")
		}
		data = read
	}
	ext := ".png"
	switch http.DetectContentType(data[:min(512, len(data))]) {
	case "image/jpeg":
		ext = ".jpg"
	case "image/webp":
		ext = ".webp"
	case "image/gif":
		ext = ".gif"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	safe := regexp.MustCompile(`[\\/:*?"<>|\s]`).ReplaceAllString(name, "_")
	filename := fmt.Sprintf("%s-%d%s", safe, time.Now().Unix(), ext)
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
