// Package interaction gives the agent small human-like micro-actions:
// poking a group member and going quiet for a while. Both are reversible,
// cheap and safe — no destructive group-admin powers.
package interaction

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
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

// EmojiLikeTool lets the persona slap an emoji reaction on the inbound
// message — the lowest-effort response real users give constantly: a
// 👍 on a joke, a ❤️ on a photo, no words needed.
func EmojiLikeTool(caller ActionCaller) tool.Definition {
	return tool.Definition{
		Name: "react_emoji",
		Description: "给对方刚发的消息贴一个表情回应（消息角上的表情），不用打字也算回了。" +
			"想表示看到/被逗笑/赞同但懒得回话时用。参数 emoji 填表情符号，如 👍😂❤️🤔",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"emoji": map[string]any{
					"type":        "string",
					"description": "要贴的表情符号，填一个 emoji",
				},
			},
			"required": []string{"emoji"},
		},
		Permission: tool.PermissionEveryone,
		Timeout:    10 * time.Second,
		Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
			if caller == nil {
				return tool.Result{Error: "表情回应不可用"}, nil
			}
			var args struct {
				Emoji string `json:"emoji"`
			}
			_ = json.Unmarshal(call.Arguments, &args)
			runes := []rune(strings.TrimSpace(args.Emoji))
			if len(runes) == 0 {
				return tool.Result{Error: "没给表情"}, nil
			}
			// NapCat set_msg_emoji_like takes the emoji's unicode
			// codepoint as a decimal string.
			emojiID := fmt.Sprintf("%d", runes[0])
			messageID := strings.TrimSpace(call.Actor.MessageID)
			if messageID == "" {
				return tool.Result{Error: "没有可回应的消息"}, nil
			}
			params := map[string]any{
				"message_id": messageID,
				"emoji_id":   emojiID,
				"set":        true,
			}
			_, err := caller.CallFor(ctx, call.Actor.SelfID, "set_msg_emoji_like", params)
			if err != nil {
				return tool.Result{Error: "没贴上：" + err.Error()}, nil
			}
			return tool.Result{Content: "贴了 " + string(runes[0])}, nil
		},
	}
}

// signatureCooldown is the minimum gap between profile writes — QQ
// risk-controls accounts that change their signature too often, so a
// moody persona can't trip the anti-abuse tripwire.
const signatureCooldown = 30 * time.Minute

// SignatureTool lets the persona update her own QQ signature — the tiny
// mood line real people flip when they're happy or annoyed. Changes are
// rate-limited per bot account; the tool only touches the bot's own
// profile, never anyone else's.
func SignatureTool(caller ActionCaller, now func() time.Time) tool.Definition {
	var mu sync.Mutex
	lastChange := map[string]time.Time{}
	if now == nil {
		now = time.Now
	}
	return tool.Definition{
		Name: "update_signature",
		Description: "改自己的 QQ 个性签名（别人看你资料时那一句心情/状态）。心情明显变化、想了句很贴当下的话时用。" +
			"参数 signature 填新签名，别太长。改的不是对方也不是群，是你自己的资料。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"signature": map[string]any{
					"type":        "string",
					"description": "新的个性签名内容",
				},
			},
			"required": []string{"signature"},
		},
		Permission: tool.PermissionEveryone,
		Timeout:    10 * time.Second,
		Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
			if caller == nil {
				return tool.Result{Error: "签名不可用"}, nil
			}
			var args struct {
				Signature string `json:"signature"`
			}
			_ = json.Unmarshal(call.Arguments, &args)
			sig := strings.TrimSpace(args.Signature)
			if sig == "" {
				return tool.Result{Error: "签名是空的"}, nil
			}
			if n := len([]rune(sig)); n > 60 {
				return tool.Result{Error: fmt.Sprintf("签名太长了（%d 字），压到 60 字内", n)}, nil
			}
			selfID := call.Actor.SelfID
			mu.Lock()
			if last, ok := lastChange[selfID]; ok && now().Sub(last) < signatureCooldown {
				wait := signatureCooldown - now().Sub(last)
				mu.Unlock()
				return tool.Result{Error: fmt.Sprintf(
					"签名刚改没多久，冷却中——%d 分钟后再改", int(wait.Minutes())+1)}, nil
			}
			mu.Unlock()
			_, err := caller.CallFor(ctx, selfID, "set_qq_profile", map[string]any{
				"personal_note": sig,
			})
			if err != nil {
				return tool.Result{Error: "没改成：" + err.Error()}, nil
			}
			mu.Lock()
			lastChange[selfID] = now()
			mu.Unlock()
			return tool.Result{Content: "签名改成「" + sig + "」了"}, nil
		},
	}
}

// qzoneCooldown keeps feed posts rare — spamming 说说 reads bot, and QQ
// throttles accounts that post too often anyway.
const qzoneCooldown = 2 * time.Hour

// QzoneTool lets the persona post to her own QQ 空间 — the mood line or
// little moment a real person broadcasts when chat isn't enough. Own
// feed only; it can never post to anyone else's space.
func QzoneTool(caller ActionCaller, now func() time.Time) tool.Definition {
	var mu sync.Mutex
	lastPost := map[string]time.Time{}
	if now == nil {
		now = time.Now
	}
	return tool.Definition{
		Name: "post_qzone",
		Description: "发一条自己的 QQ 空间说说（你的个人动态，好友刷空间能看到）。有想公开碎碎念的事——心情、小事、日常——用；" +
			"私密的话发聊天里就行，别往空间发。参数 content 写说说正文。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"content": map[string]any{
					"type":        "string",
					"description": "说说正文，口语一点，别太长",
				},
			},
			"required": []string{"content"},
		},
		Permission: tool.PermissionEveryone,
		Timeout:    15 * time.Second,
		Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
			if caller == nil {
				return tool.Result{Error: "空间不可用"}, nil
			}
			var args struct {
				Content string `json:"content"`
			}
			_ = json.Unmarshal(call.Arguments, &args)
			content := strings.TrimSpace(args.Content)
			if content == "" {
				return tool.Result{Error: "说说是空的"}, nil
			}
			if n := len([]rune(content)); n > 200 {
				return tool.Result{Error: fmt.Sprintf("说说太长了（%d 字），压到 200 字内", n)}, nil
			}
			selfID := call.Actor.SelfID
			mu.Lock()
			if last, ok := lastPost[selfID]; ok && now().Sub(last) < qzoneCooldown {
				wait := qzoneCooldown - now().Sub(last)
				mu.Unlock()
				return tool.Result{Error: fmt.Sprintf(
					"刚发过说说，%d 分钟后再发", int(wait.Minutes())+1)}, nil
			}
			mu.Unlock()
			_, err := caller.CallFor(ctx, selfID, "send_qzone", map[string]any{
				"content": content,
			})
			if err != nil {
				return tool.Result{Error: "没发出去：" + err.Error()}, nil
			}
			mu.Lock()
			lastPost[selfID] = now()
			mu.Unlock()
			return tool.Result{Content: "说说发出去了"}, nil
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
			if err := prefs.SetMuteUntil(call.Actor.Platform, call.Actor.SelfID, call.Actor.ChatType, call.Actor.ChatID, until); err != nil {
				return tool.Result{Error: "没装成：" + err.Error()}, nil
			}
			return tool.Result{Content: fmt.Sprintf(
				"好，%d 分钟内不回话了（%s 恢复）", minutes, until.Format("15:04"))}, nil
		},
	}
}
