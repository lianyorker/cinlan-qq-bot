package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/platform"
)

// ChannelActionTool exposes the current channel adapter's Call surface to
// the agent as one generic tool: {action, params}. Read-only actions are
// open to everyone; mutating/moderating actions require the caller's
// role to be "admin" (chatprefs admin mapping).
//
// Action names are platform-native: OneBot V11 names on QQ
// (send_like, set_group_ban, get_login_info, set_qq_profile, send_qzone…),
// douyin api_* names on douyin (api_publish_comment, api_dm_send…).
// ChannelActionTool builds the tool. allowedRoot, when given, confines
// local file paths in upload actions to that directory — without it,
// local paths in upload actions are denied outright (http/base64 still
// pass through to the adapter).
func ChannelActionTool(platforms *platform.Registry, allowedRoot ...string) Definition {
	return Definition{
		Name: "channel_action",
		Description: "调用当前频道平台动作。QQ(OneBot): send_like 点赞、set_qq_profile 改签名、send_qzone 发空间、set_group_ban/kick/admin 群管理、get_login_info/get_stranger_info/get_group_info 查询、upload_group_file 传文件等；douyin: api_user_profile/api_search_*/api_publish_comment/api_dm_send 等。" +
			"action=动作名，params=动作参数对象。只读动作对所有人开放，写/管动作仅管理员可用。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"description": "平台原生动作名（如 send_like / set_group_ban / api_user_profile）",
				},
				"params": map[string]any{
					"type":        "object",
					"description": "动作参数（如 {\"user_id\":\"123\",\"times\":1}）",
				},
			},
			"required": []string{"action"},
		},
		Permission: PermissionEveryone, // tiered inside the handler
		Timeout:    30 * time.Second,
		Source:     "builtin",
		SourceName: "channel",
		Handler:    channelActionHandler(platforms, allowedRoot),
	}
}

// safeChannelActions is the everyone-tier allowlist: reads + harmless
// profile likes. Everything else (writes/moderation) requires admin role.
var safeChannelActions = map[string]bool{
	// OneBot reads
	"get_login_info": true, "get_stranger_info": true,
	"get_group_info": true, "get_group_member_list": true,
	"get_group_honor_info": true, "get_group_at_all_remain": true,
	"get_status": true, "get_version_info": true,
	"can_send_image": true, "can_send_record": true,
	"get_msg": true, "get_forward_msg": true,
	"get_image": true, "get_record": true, "get_file": true,
	// douyin reads
	"api_user_profile": true, "api_user_works": true,
	"api_aweme_detail": true, "api_comments": true,
	"api_comment_replies": true, "api_search_video": true,
	"api_search_user": true, "api_search_live": true,
	"api_followers": true, "api_following": true,
	"api_notices": true, "api_collects": true,
	"api_favorites": true, "api_feed": true, "api_my_uid": true,
	"api_live_room_enter": true, "api_live_linkmic": true,
	"api_live_pk": true, "api_live_pk_rank": true,
	"api_live_rank": true, "api_live_ticket_rank": true,
	"live_status": true,
	// harmless writes
	"send_like":      true, // profile page like — no content side-effects
	"runtime_status": true,
}

func channelActionHandler(platforms *platform.Registry, allowedRoot []string) Handler {
	root := ""
	if len(allowedRoot) > 0 {
		root = strings.TrimSpace(allowedRoot[0])
	}
	return func(ctx context.Context, call Call) (Result, error) {
		var args struct {
			Action string         `json:"action"`
			Params map[string]any `json:"params"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return Result{Error: "invalid channel_action arguments"}, err
		}
		action := strings.TrimSpace(args.Action)
		if action == "" {
			return Result{Error: "channel_action requires an action name"},
				fmt.Errorf("channel_action: empty action")
		}
		params := args.Params
		if params == nil {
			params = map[string]any{}
		}
		admin := call.Actor.Role == "admin" || call.Actor.Role == "owner"
		if !safeChannelActions[action] {
			if !admin {
				return Result{Error: fmt.Sprintf(
						"channel_action %q requires admin role", action)},
					fmt.Errorf("channel_action %q: permission denied", action)
			}
			if deny := crossChatTargetViolation(action, params, call.Actor); deny != "" {
				return Result{Error: deny}, fmt.Errorf("channel_action: %s", deny)
			}
			if deny := filePathViolation(params, root); deny != "" {
				return Result{Error: deny}, fmt.Errorf("channel_action: %s", deny)
			}
		}
		platformName := call.Actor.Platform
		if platformName == "" {
			return Result{Error: "channel_action has no platform context"},
				fmt.Errorf("channel_action: actor platform empty")
		}
		adapter, ok := platforms.Get(platformName)
		if !ok {
			return Result{Error: fmt.Sprintf(
					"platform %q not registered", platformName)},
				fmt.Errorf("channel_action: platform %q missing", platformName)
		}
		out, err := adapter.Call(ctx, action, params)
		if err != nil {
			return Result{Error: err.Error()}, nil // surface, don't fail the turn
		}
		return Result{Content: out}, nil
	}
}

// crossChatTargetViolation pins chat-targeted actions to the actor's own
// chat so a group admin (or a prompt-injected request) cannot make the bot
// post messages or upload files into a different chat. group_id on any
// action and user_id on private-chat actions are the destination fields;
// user_id on group actions like set_group_ban is a *target user*, not a
// destination, so it is left alone.
func crossChatTargetViolation(action string, params map[string]any, actor Actor) string {
	if gid, ok := stringParam(params, "group_id"); ok {
		if actor.ChatType != "group" || gid != actor.ChatID {
			return fmt.Sprintf(
				"channel_action %q can only target the current chat", action)
		}
	}
	sendPrivate := strings.HasPrefix(action, "send_private") ||
		strings.HasPrefix(action, "upload_private")
	if action == "send_msg" && params["group_id"] == nil {
		sendPrivate = true
	}
	if sendPrivate {
		if uid, ok := stringParam(params, "user_id"); ok && uid != actor.ChatID {
			return fmt.Sprintf(
				"channel_action %q can only target the current chat", action)
		}
	}
	return ""
}

// filePathViolation confines local file paths in upload/send actions to
// the allowed root. Remote URIs and base64 payloads pass through — they
// can't read the server disk.
func filePathViolation(params map[string]any, allowedRoot string) string {
	file, ok := stringParam(params, "file")
	if !ok || file == "" {
		return ""
	}
	lower := strings.ToLower(file)
	if strings.HasPrefix(lower, "http://") ||
		strings.HasPrefix(lower, "https://") ||
		strings.HasPrefix(lower, "base64://") {
		return ""
	}
	if allowedRoot == "" {
		return "channel_action local file paths are not allowed"
	}
	abs, err := filepath.Abs(strings.TrimPrefix(file, "file://"))
	if err != nil {
		return fmt.Sprintf("channel_action file path invalid: %v", err)
	}
	root, err := filepath.Abs(allowedRoot)
	if err != nil {
		return fmt.Sprintf("channel_action allowed root invalid: %v", err)
	}
	if abs != root && !strings.HasPrefix(abs, root+string(filepath.Separator)) {
		return "channel_action file path escapes the allowed root"
	}
	return ""
}

func stringParam(params map[string]any, key string) (string, bool) {
	raw, ok := params[key]
	if !ok {
		return "", false
	}
	switch v := raw.(type) {
	case string:
		return v, true
	case float64:
		return fmt.Sprintf("%.0f", v), true
	default:
		return fmt.Sprintf("%v", v), true
	}
}
