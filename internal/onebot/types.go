package onebot

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

type StringID string

func (id *StringID) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if bytes.Equal(data, []byte("null")) || len(data) == 0 {
		*id = ""
		return nil
	}
	if data[0] == '"' {
		var value string
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		*id = StringID(value)
		return nil
	}
	if !json.Valid(data) {
		return fmt.Errorf("invalid ID %q", string(data))
	}
	*id = StringID(string(data))
	return nil
}

func (id StringID) String() string {
	return string(id)
}

type Sender struct {
	UserID   StringID `json:"user_id"`
	Nickname string   `json:"nickname"`
	Card     string   `json:"card"`
	Role     string   `json:"role"`
}

type Event struct {
	Time          int64           `json:"time"`
	SelfID        StringID        `json:"self_id"`
	PostType      string          `json:"post_type"`
	MessageType   string          `json:"message_type"`
	SubType       string          `json:"sub_type"`
	NoticeType    string          `json:"notice_type"`
	RequestType   string          `json:"request_type"`
	MetaEventType string          `json:"meta_event_type"`
	MessageID     StringID        `json:"message_id"`
	UserID        StringID        `json:"user_id"`
	GroupID       StringID        `json:"group_id"`
	Message       json.RawMessage `json:"message"`
	RawMessage    string          `json:"raw_message"`
	Sender        Sender          `json:"sender"`
	Status        json.RawMessage `json:"status"`
	RawPayload    json.RawMessage `json:"-"`
}

type MessageSegment struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data"`
}

type apiResponse struct {
	Status  string          `json:"status"`
	RetCode int             `json:"retcode"`
	Data    json.RawMessage `json:"data"`
	Message string          `json:"message"`
	Wording string          `json:"wording"`
	Echo    json.RawMessage `json:"echo"`
}

type actionResult struct {
	response apiResponse
	err      error
}

// ActionError preserves the OneBot error contract so callers can make
// decisions on retcode without parsing an error string.
type ActionError struct {
	Action  string
	Status  string
	RetCode int
	Message string
	Wording string
}

func (e *ActionError) Error() string {
	if e == nil {
		return "onebot action failed"
	}
	detail := strings.TrimSpace(e.Message)
	if detail == "" {
		detail = strings.TrimSpace(e.Wording)
	}
	if detail == "" {
		detail = "unknown error"
	}
	if e.Action == "" {
		return fmt.Sprintf("onebot action failed: retcode=%d message=%s", e.RetCode, detail)
	}
	return fmt.Sprintf("onebot action %q failed: retcode=%d message=%s", e.Action, e.RetCode, detail)
}
