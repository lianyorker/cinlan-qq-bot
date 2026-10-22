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
	"github.com/lianyorker/cinlan-qq-bot/internal/cron"
	"github.com/lianyorker/cinlan-qq-bot/internal/interaction"
	"github.com/lianyorker/cinlan-qq-bot/internal/media"
	"github.com/lianyorker/cinlan-qq-bot/internal/platform"
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
	logger *slog.Logger,
) error {
	store, err := chatprefs.Load(cfg.ChatPrefsFile)
	if err != nil {
		return err
	}
	service.SetChatPrefs(store)
	if imageGenerator != nil {
		imageGenerator.SetReferenceResolver(store.LatestImage)
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
			// "叫回来" lifts everything: notify on, self-mute cleared, and
			// any handoff released — the single wake command now that
			// /人工 /恢复 are gone.
			_ = store.SetMuteUntil(c.Event.MessageType, c.Event.ChatID, time.Time{})
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
			// An attached or quoted image makes the text just a label —
			// review is only needed for standalone text notes.
			refs := service.EventImageRefs(
				ctx, c.Event.SelfID, c.Event.Chain,
			)
			if len(refs) == 0 {
				keep, reason, err := service.ReviewNote(ctx, text)
				switch {
				case err != nil:
					return command.Result{Handled: true, Reply: "审核走神了，这条先不记：" + err.Error()}, nil
				case !keep:
					return command.Result{Handled: true, Reply: "这个没啥好记的：" + reason}, nil
				}
			}
			if err := store.AddNote(
				c.Event.MessageType,
				c.Event.ChatID,
				text,
			); err != nil {
				return command.Result{Handled: true, Reply: "没记住：" + err.Error()}, nil
			}
			if len(refs) > 0 {
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

	// Runtime persona switching is gone — each chat keeps the single
	// persona from its binding. This stub remains so attempts get a
	// deterministic reply instead of the model playing along.
	register(command.Definition{
		Name:        "切换",
		Aliases:     []string{"形态", "切换形态", "persona"},
		Description: "人格已固定，不可切换",
		Permission:  command.PermissionEveryone,
		Handler: func(_ context.Context, c *command.Context) (command.Result, error) {
			return command.Result{Handled: true, Reply: "人格是管理员在后台配置的，群里不能换哦。"}, nil
		},
	})

	register(command.Definition{
		Name:        "指令",
		Aliases:     []string{"命令", "help", "菜单"},
		Description: "列出全部可用指令（仅白名单私聊）",
		Permission:  command.PermissionEveryone,
		Handler: func(_ context.Context, c *command.Context) (command.Result, error) {
			if c.Event.MessageType != platform.ChatPrivate || !isOperator(c) {
				return command.Result{Handled: true, Reply: "这个命令只有白名单用户私聊能用。"}, nil
			}
			var builder strings.Builder
			builder.WriteString("可用指令：")
			for _, info := range service.CommandRegistry().List() {
				builder.WriteString("\n· /" + info.Name)
				if len(info.Aliases) > 0 {
					builder.WriteString(
						"（别名 " + strings.Join(info.Aliases, "、") + "）",
					)
				}
				if desc := strings.TrimSpace(info.Description); desc != "" {
					builder.WriteString("：" + desc)
				}
			}
			return command.Result{Handled: true, Reply: builder.String()}, nil
		},
	})

	register(command.Definition{
		Name:        "语音",
		Aliases:     []string{"唱歌", "voice"},
		Description: "开关本群语音回复（/语音 开启|关闭）",
		Permission:  command.PermissionEveryone,
		Handler: func(_ context.Context, c *command.Context) (command.Result, error) {
			if !isOperator(c) {
				return command.Result{Handled: true, Reply: "权限不足。"}, nil
			}
			arg := strings.TrimSpace(strings.Join(c.Args, " "))
			switch arg {
			case "开启", "开", "on":
				if err := store.SetVoiceEnabled(
					c.Event.MessageType, c.Event.ChatID, true,
				); err != nil {
					return command.Result{Handled: true, Reply: "设置失败：" + err.Error()}, nil
				}
				return command.Result{Handled: true, Reply: "语音已开启，可以点语音/哼歌了。"}, nil
			case "关闭", "关", "off":
				if err := store.SetVoiceEnabled(
					c.Event.MessageType, c.Event.ChatID, false,
				); err != nil {
					return command.Result{Handled: true, Reply: "设置失败：" + err.Error()}, nil
				}
				return command.Result{Handled: true, Reply: "语音已关闭。"}, nil
			default:
				state := "开启"
				if !store.VoiceEnabled(
					c.Event.MessageType, c.Event.ChatID,
				) {
					state = "关闭"
				}
				return command.Result{Handled: true, Reply: "当前语音：" + state + "（/语音 开启|关闭）"}, nil
			}
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
