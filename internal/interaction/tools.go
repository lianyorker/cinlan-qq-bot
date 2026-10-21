// Package interaction gives the agent small human-like micro-actions:
// poking a group member and going quiet for a while. Both are reversible,
// cheap and safe — no destructive group-admin powers.
package interaction

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/chatprefs"
	"github.com/lianyorker/cinlan-qq-bot/internal/platform"
	"github.com/lianyorker/cinlan-qq-bot/internal/tool"
)

// ActionCaller runs a raw OneBot/NapCat action for a specific bot account.
// *onebot.MultiAdapter and the single Adapter both satisfy this shape.
type ActionCaller interface {
	CallFor(ctx context.Context, selfID, action string, params map[string]any) (any, error)
}

type wrappedCaller struct {
	call func(ctx context.Context, action string, params map[string]any) (any, error)
}

func (w wrappedCaller) CallFor(
	ctx context.Context, _ string, action string, params map[string]any,
) (any, error) {
	return w.call(ctx, action, params)
}

// Wrap adapts a plain Call(ctx, action, params) — single-account adapter —
// to ActionCaller by ignoring the selfID selector.
func Wrap(
	call func(ctx context.Context, action string, params map[string]any) (any, error),
) ActionCaller {
	if call == nil {
		return nil
	}
	return wrappedCaller{call: call}
}

const maxQuietMinutes = 120

// PokeTool lets the persona poke someone in the group (戳一戳) instead of
// answering with words — the micro-interaction real users spam constantly.
func PokeTool(caller ActionCaller) tool.Definition {
	return tool.Definition{
		Name: "poke_user",
		Description: "在群里戳一戳某人（QQ 双击头像那种戳）。不想打字又想有反应时用。" +
			"参数 user_id 留空则戳当前发消息的人。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"user_id": map[string]any{
					"type":        "string",
					"description": "要戳的 QQ 号，留空默认戳当前发消息的人",
				},
			},
		},
		Permission: tool.PermissionEveryone,
		Timeout:    10 * time.Second,
		Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
			if caller == nil {
				return tool.Result{Error: "戳一戳不可用"}, nil
			}
			if call.Actor.ChatType != platform.ChatGroup {
				return tool.Result{Error: "只能在群里戳人"}, nil
			}
			var args struct {
				UserID string `json:"user_id"`
			}
			_ = json.Unmarshal(call.Arguments, &args)
			target := strings.TrimSpace(args.UserID)
			if target == "" {
				target = call.Actor.UserID
			}
			if target == "" {
				return tool.Result{Error: "不知道戳谁"}, nil
			}
			_, err := caller.CallFor(ctx, call.Actor.SelfID, "send_poke", map[string]any{
				"group_id": call.Actor.ChatID,
				"user_id":  target,
			})
			if err != nil {
				return tool.Result{Error: "没戳动：" + err.Error()}, nil
			}
			return tool.Result{Content: "戳了 " + target + " 一下"}, nil
		},
	}
}

// QuietTool lets the persona go silent for a while — the bot ignores the
// chat until the timer expires, like someone going AFK. Fully reversible.
func QuietTool(prefs *chatprefs.Store, now func() time.Time) tool.Definition {
	return tool.Definition{
		Name: "go_quiet",
		Description: fmt.Sprintf(
			"装死/自闭一会儿：接下来 N 分钟不回这个会话的任何消息（最长 %d 分钟）。"+
				"被惹烦了、想静静、懒得说话时自己用。",
			maxQuietMinutes,
		),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"minutes": map[string]any{
					"type":        "integer",
					"description": fmt.Sprintf("装死多少分钟，1-%d，默认 10", maxQuietMinutes),
				},
			},
		},
		Permission: tool.PermissionEveryone,
		Timeout:    10 * time.Second,
		Handler: func(_ context.Context, call tool.Call) (tool.Result, error) {
			if prefs == nil {
				return tool.Result{Error: "装死不可用"}, nil
			}
			var args struct {
				Minutes int `json:"minutes"`
			}
			_ = json.Unmarshal(call.Arguments, &args)
			minutes := args.Minutes
			if minutes <= 0 {
				minutes = 10
			}
			if minutes > maxQuietMinutes {
				minutes = maxQuietMinutes
			}
			until := now().Add(time.Duration(minutes) * time.Minute)
			if err := prefs.SetMuteUntil(call.Actor.ChatType, call.Actor.ChatID, until); err != nil {
				return tool.Result{Error: "没装成：" + err.Error()}, nil
			}
			return tool.Result{Content: fmt.Sprintf(
				"好，%d 分钟内不回话了（%s 恢复）", minutes, until.Format("15:04"))}, nil
		},
	}
}
