package interaction

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/chatprefs"
	"github.com/lianyorker/cinlan-qq-bot/internal/tool"
)

// Reviewer screens self-learned content before it is persisted — reuse the
// operator-note review so remembered facts can't smuggle instructions.
type Reviewer func(ctx context.Context, text string) (bool, string, error)

// RememberTool lets the persona learn on its own mid-conversation. Two
// memory shapes: a chat fact (about_user empty) or an impression of a
// specific member (about_user = that member's QQ id). Learned lines are
// injected back as lower-trust context than operator notes.
func RememberTool(prefs *chatprefs.Store, review Reviewer) tool.Definition {
	return tool.Definition{
		Name: "remember",
		Description: "记住一件事。两种用法：传 about_user=某人的QQ号时记住你对那个人的印象" +
			"（比如'总爱抬杠''生日10.28''喜欢玩原神'）；不传就记这个聊天的公共事实" +
			"（比如'群里周三开黑''群主叫阿明'）。只记确定的信息，别记猜测和临时状态。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"content": map[string]any{
					"type":        "string",
					"description": "要记住的内容，一句话",
				},
				"about_user": map[string]any{
					"type":        "string",
					"description": "可选：这条记忆是关于谁的QQ号，留空则为聊天公共事实",
				},
			},
			"required": []string{"content"},
		},
		Permission: tool.PermissionEveryone,
		Timeout:    30 * time.Second,
		Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
			if prefs == nil {
				return tool.Result{Error: "记忆不可用"}, nil
			}
			var args struct {
				Content   string `json:"content"`
				AboutUser string `json:"about_user"`
			}
			_ = json.Unmarshal(call.Arguments, &args)
			content := strings.TrimSpace(args.Content)
			if content == "" {
				return tool.Result{Error: "没啥可记的"}, nil
			}
			if runes := []rune(content); len(runes) > 80 {
				content = string(runes[:80])
			}
			if review != nil {
				ok, reason, err := review(ctx, content)
				if err != nil {
					return tool.Result{Error: "审核走神了，先不记"}, nil
				}
				if !ok {
					return tool.Result{Error: "这个不值得记：" + reason}, nil
				}
			}
			about := strings.TrimSpace(args.AboutUser)
			if about != "" {
				if err := prefs.SetImpression(
					call.Actor.ChatType, call.Actor.ChatID, about, content,
				); err != nil {
					return tool.Result{Error: "没记住：" + err.Error()}, nil
				}
				return tool.Result{Content: fmt.Sprintf(
					"记下了对 %s 的印象", about)}, nil
			}
			if err := prefs.AddFact(
				call.Actor.ChatType, call.Actor.ChatID, content,
			); err != nil {
				return tool.Result{Error: "没记住：" + err.Error()}, nil
			}
			return tool.Result{Content: "记住了"}, nil
		},
	}
}
