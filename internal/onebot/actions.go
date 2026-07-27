package onebot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/message"
)

// SendMessage is the transport-neutral form of send_group_msg/send_private_msg.
// It returns the raw OneBot data object because NapCat adds fields to the
// standard message result across versions.
func (c *Client) SendMessage(ctx context.Context, messageType, targetID string, chain message.Chain) (json.RawMessage, error) {
	messageType = strings.ToLower(strings.TrimSpace(messageType))
	targetID = strings.TrimSpace(targetID)
	if messageType != "group" && messageType != "private" {
		return nil, fmt.Errorf("unsupported OneBot message type %q", messageType)
	}
	if targetID == "" {
		return nil, errors.New("OneBot target ID is empty")
	}
	params := map[string]any{"message": toSegments(chain)}
	if messageType == "group" {
		params["group_id"] = idValue(targetID)
	} else {
		params["user_id"] = idValue(targetID)
	}
	return c.sendAction(ctx, "send_"+messageType+"_msg", params)
}

func (c *Client) GetStatus(ctx context.Context) (map[string]any, error) {
	return c.callObject(ctx, "get_status", nil)
}

func (c *Client) GetVersionInfo(ctx context.Context) (map[string]any, error) {
	return c.callObject(ctx, "get_version_info", nil)
}

func (c *Client) GetStrangerInfo(ctx context.Context, userID string, noCache bool) (map[string]any, error) {
	return c.callObject(ctx, "get_stranger_info", map[string]any{
		"user_id":  idValue(userID),
		"no_cache": noCache,
	})
}

func (c *Client) GetGroupMemberList(ctx context.Context, groupID string, noCache bool) (json.RawMessage, error) {
	return c.sendAction(ctx, "get_group_member_list", map[string]any{
		"group_id": idValue(groupID),
		"no_cache": noCache,
	})
}

func (c *Client) SetGroupAdmin(ctx context.Context, groupID, userID string, enable bool) error {
	_, err := c.sendAction(ctx, "set_group_admin", map[string]any{
		"group_id": idValue(groupID),
		"user_id":  idValue(userID),
		"enable":   enable,
	})
	return err
}

func (c *Client) SetGroupCard(ctx context.Context, groupID, userID, card string) error {
	_, err := c.sendAction(ctx, "set_group_card", map[string]any{
		"group_id": idValue(groupID),
		"user_id":  idValue(userID),
		"card":     card,
	})
	return err
}

func (c *Client) SetGroupName(ctx context.Context, groupID, name string) error {
	_, err := c.sendAction(ctx, "set_group_name", map[string]any{
		"group_id":   idValue(groupID),
		"group_name": name,
	})
	return err
}

func (c *Client) SetGroupSpecialTitle(ctx context.Context, groupID, userID, title string, duration time.Duration) error {
	_, err := c.sendAction(ctx, "set_group_special_title", map[string]any{
		"group_id":      idValue(groupID),
		"user_id":       idValue(userID),
		"special_title": title,
		"duration":      durationSeconds(duration),
	})
	return err
}

func (c *Client) SetGroupLeave(ctx context.Context, groupID string, isDismiss bool) error {
	_, err := c.sendAction(ctx, "set_group_leave", map[string]any{
		"group_id":   idValue(groupID),
		"is_dismiss": isDismiss,
	})
	return err
}

func (c *Client) SetGroupAnonymousBan(ctx context.Context, groupID, flag string, duration time.Duration) error {
	_, err := c.sendAction(ctx, "set_group_anonymous_ban", map[string]any{
		"group_id":       idValue(groupID),
		"anonymous_flag": flag,
		"duration":       durationSeconds(duration),
	})
	return err
}

func (c *Client) SetGroupAnonymous(ctx context.Context, groupID string, enable bool) error {
	_, err := c.sendAction(ctx, "set_group_anonymous", map[string]any{
		"group_id": idValue(groupID),
		"enable":   enable,
	})
	return err
}

func (c *Client) SetGroupEssence(ctx context.Context, messageID string) error {
	return c.SetEssenceMessage(ctx, messageID)
}

func (c *Client) SetEssenceMessage(ctx context.Context, messageID string) error {
	_, err := c.sendAction(ctx, "set_essence_msg", map[string]any{"message_id": idValue(messageID)})
	return err
}

func (c *Client) DeleteGroupEssence(ctx context.Context, messageID string) error {
	return c.DeleteEssenceMessage(ctx, messageID)
}

func (c *Client) DeleteEssenceMessage(ctx context.Context, messageID string) error {
	_, err := c.sendAction(ctx, "delete_essence_msg", map[string]any{"message_id": idValue(messageID)})
	return err
}

func (c *Client) SetGroupSign(ctx context.Context, groupID string) error {
	_, err := c.sendAction(ctx, "set_group_sign", map[string]any{"group_id": idValue(groupID)})
	return err
}

func (c *Client) SendLike(ctx context.Context, userID string, times int) error {
	if times < 1 {
		times = 1
	}
	if times > 10 {
		times = 10
	}
	_, err := c.sendAction(ctx, "send_like", map[string]any{
		"user_id": idValue(userID),
		"times":   times,
	})
	return err
}

func (c *Client) SetFriendAddRequest(ctx context.Context, flag string, approve bool, remark string) error {
	params := map[string]any{
		"flag":    flag,
		"approve": approve,
	}
	if strings.TrimSpace(remark) != "" {
		params["remark"] = remark
	}
	_, err := c.sendAction(ctx, "set_friend_add_request", params)
	return err
}

func (c *Client) SetGroupAddRequest(ctx context.Context, flag, subType string, approve bool, reason string) error {
	params := map[string]any{
		"flag":     flag,
		"sub_type": subType,
		"approve":  approve,
	}
	if strings.TrimSpace(reason) != "" {
		params["reason"] = reason
	}
	_, err := c.sendAction(ctx, "set_group_add_request", params)
	return err
}

func (c *Client) GetGroupHonorInfo(ctx context.Context, groupID, honorType string) (json.RawMessage, error) {
	params := map[string]any{"group_id": idValue(groupID)}
	if strings.TrimSpace(honorType) != "" {
		params["type"] = honorType
	}
	return c.sendAction(ctx, "get_group_honor_info", params)
}

func (c *Client) GetGroupAtAllRemain(ctx context.Context, groupID string) (json.RawMessage, error) {
	return c.sendAction(ctx, "get_group_at_all_remain", map[string]any{"group_id": idValue(groupID)})
}

func (c *Client) GetForwardMessage(ctx context.Context, messageID string) (json.RawMessage, error) {
	return c.GetForwardMsg(ctx, messageID)
}

func (c *Client) GetForwardMsg(ctx context.Context, messageID string) (json.RawMessage, error) {
	return c.sendAction(ctx, "get_forward_msg", map[string]any{"message_id": idValue(messageID)})
}

func (c *Client) GetImage(ctx context.Context, file string) (json.RawMessage, error) {
	return c.sendAction(ctx, "get_image", map[string]any{"file": file})
}

func (c *Client) GetRecord(ctx context.Context, file string, outFormat string) (json.RawMessage, error) {
	params := map[string]any{"file": file}
	if strings.TrimSpace(outFormat) != "" {
		params["out_format"] = outFormat
	}
	return c.sendAction(ctx, "get_record", params)
}

func (c *Client) GetFile(ctx context.Context, file string) (json.RawMessage, error) {
	return c.sendAction(ctx, "get_file", map[string]any{"file": file})
}

func (c *Client) UploadGroupFile(ctx context.Context, groupID, file, name, folder string) (json.RawMessage, error) {
	params := map[string]any{
		"group_id": idValue(groupID),
		"file":     file,
		"name":     name,
	}
	if strings.TrimSpace(folder) != "" {
		params["folder"] = folder
	}
	return c.sendAction(ctx, "upload_group_file", params)
}

func (c *Client) CanSendImage(ctx context.Context) (bool, error) {
	return c.callBool(ctx, "can_send_image", nil)
}

func (c *Client) CanSendRecord(ctx context.Context) (bool, error) {
	return c.callBool(ctx, "can_send_record", nil)
}

func durationSeconds(value time.Duration) int64 {
	if value <= 0 {
		return 0
	}
	return int64(value / time.Second)
}

func (c *Client) callObject(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	data, err := c.sendAction(ctx, action, params)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if unmarshalErr := json.Unmarshal(data, &result); unmarshalErr != nil {
		return nil, fmt.Errorf("decode %s response: %w", action, unmarshalErr)
	}
	return result, nil
}

func (c *Client) callBool(ctx context.Context, action string, params map[string]any) (bool, error) {
	data, err := c.sendAction(ctx, action, params)
	if err != nil {
		return false, err
	}
	var result map[string]any
	if unmarshalErr := json.Unmarshal(data, &result); unmarshalErr != nil {
		return false, fmt.Errorf("decode %s response: %w", action, unmarshalErr)
	}
	for _, key := range []string{"yes", "can_send", "enabled"} {
		if value, ok := result[key]; ok {
			switch typed := value.(type) {
			case bool:
				return typed, nil
			case string:
				return strings.EqualFold(typed, "true") || typed == "1", nil
			case float64:
				return typed != 0, nil
			}
		}
	}
	return false, fmt.Errorf("decode %s response: boolean field is missing", action)
}
