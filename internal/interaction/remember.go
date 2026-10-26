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

// RememberTool lets the persona learn, correct and forget mid-conversation.
// Two memory shapes: a chat fact (about_user empty) or an impression of a
// specific member (about_user = that member's QQ id). Impressions also flow
// into a persona-scoped profile so the same persona recognizes the person
// across every binding she serves. Learned lines inject back as lower-trust
// context than operator notes.
func RememberTool(prefs *chatprefs.Store, review Reviewer) tool.Definition {
	return tool.Definition{
		Name: "remember",
		Description: "记忆管理。action=set(默认)记新内容；replace 改口——old 填大致旧内容" +
			"content 填更正后的；forget 忘掉——content 或 old 描述要忘的东西。about_user=某人QQ号" +
			"时针对那个人（比如'总爱抬杠''生日10.28'），不带就是本聊天的公共事实。只记确定的" +
			"信息，别记猜测和临时状态。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"enum":        []string{"set", "replace", "forget"},
					"description": "set 记住 / replace 改正旧记忆 / forget 忘掉，默认 set",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "要记住或更正后的内容；forget 时也可以只填这里描述要忘的",
				},
				"old": map[string]any{
					"type":        "string",
					"description": "replace/forget 用：大致描述要被替换或忘掉的旧记忆",
				},
				"about_user": map[string]any{
					"type":        "string",
					"description": "可选：这条记忆是关于谁的QQ号，留空则为聊天公共事实",
				},
			},
		},
		Permission: tool.PermissionEveryone,
		Timeout:    30 * time.Second,
		Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
			if prefs == nil {
				return tool.Result{Error: "记忆不可用"}, nil
			}
			var args struct {
				Action    string `json:"action"`
				Content   string `json:"content"`
				Old       string `json:"old"`
				AboutUser string `json:"about_user"`
			}
			_ = json.Unmarshal(call.Arguments, &args)
			action := strings.ToLower(strings.TrimSpace(args.Action))
			if action == "" {
				action = "set"
			}
			content := strings.TrimSpace(args.Content)
			old := strings.TrimSpace(args.Old)
			about := strings.TrimSpace(args.AboutUser)
			target := old
			if target == "" {
				target = content
			}
			if runes := []rune(content); len(runes) > 80 && action != "forget" {
				content = string(runes[:80])
			}

			switch action {
			case "forget":
				if target == "" {
					return tool.Result{Error: "要忘什么没说"}, nil
				}
				if about != "" {
					if err := prefs.ForgetImpression(
						call.Actor.Platform, call.Actor.SelfID, call.Actor.ChatType, call.Actor.ChatID, about,
					); err != nil {
						return tool.Result{Error: err.Error()}, nil
					}
					return tool.Result{Content: fmt.Sprintf(
						"对 %s 的印象忘了", about)}, nil
				}
				if err := prefs.ForgetFact(
					call.Actor.Platform, call.Actor.SelfID, call.Actor.ChatType, call.Actor.ChatID, target,
				); err != nil {
					return tool.Result{Error: err.Error()}, nil
				}
				return tool.Result{Content: "忘了"}, nil

			case "replace":
				if old == "" || content == "" {
					return tool.Result{Error: "改口要同时给旧内容和新内容"}, nil
				}
				if about != "" {
					if review != nil {
						if err := screenContent(ctx, review, content); err != nil {
							return tool.Result{Error: err.Error()}, nil
						}
					}
					if err := writeImpression(prefs, call, about, content); err != nil {
						return tool.Result{Error: "没改成：" + err.Error()}, nil
					}
					return tool.Result{Content: fmt.Sprintf(
						"对 %s 的印象改好了", about)}, nil
				}
				if review != nil {
					if err := screenContent(ctx, review, content); err != nil {
						return tool.Result{Error: err.Error()}, nil
					}
				}
				if err := prefs.ReplaceFact(
					call.Actor.Platform, call.Actor.SelfID, call.Actor.ChatType, call.Actor.ChatID, old, content,
				); err != nil {
					return tool.Result{Error: "没改成：" + err.Error()}, nil
				}
				return tool.Result{Content: "改了"}, nil

			default:
				if content == "" {
					return tool.Result{Error: "没啥可记的"}, nil
				}
				if review != nil {
					if err := screenContent(ctx, review, content); err != nil {
						return tool.Result{Error: err.Error()}, nil
					}
				}
				if about != "" {
					if err := writeImpression(prefs, call, about, content); err != nil {
						return tool.Result{Error: "没记住：" + err.Error()}, nil
					}
					return tool.Result{Content: fmt.Sprintf(
						"记下了对 %s 的印象", about)}, nil
				}
				if err := prefs.AddFact(
					call.Actor.Platform, call.Actor.SelfID, call.Actor.ChatType, call.Actor.ChatID, content,
				); err != nil {
					return tool.Result{Error: "没记住：" + err.Error()}, nil
				}
				return tool.Result{Content: "记住了"}, nil
			}
		},
	}
}

func screenContent(ctx context.Context, review Reviewer, content string) error {
	ok, reason, err := review(ctx, content)
	if err != nil {
		return fmt.Errorf("审核走神了，先不记")
	}
	if !ok {
		return fmt.Errorf("这个不值得记：%s", reason)
	}
	return nil
}

// writeImpression keeps the per-chat take and the persona-scoped profile
// in lockstep — she remembers the person, not just the chat room.
func writeImpression(
	prefs *chatprefs.Store,
	call tool.Call,
	about, content string,
) error {
	if err := prefs.SetImpression(
		call.Actor.Platform, call.Actor.SelfID, call.Actor.ChatType, call.Actor.ChatID, about, content,
	); err != nil {
		return err
	}
	persona := prefs.PersonaName(call.Actor.Platform, call.Actor.SelfID, call.Actor.ChatType, call.Actor.ChatID)
	if persona == "" {
		return nil
	}
	return prefs.SetProfile(call.Actor.Platform, persona, about, content)
}
