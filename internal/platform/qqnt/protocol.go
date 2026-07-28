package qqnt

import (
	"bytes"
	"encoding/json"
	"fmt"
)

const protocolVersion = 1

type envelope struct {
	Version int             `json:"v"`
	Type    string          `json:"type"`
	ID      string          `json:"id,omitempty"`
	Token   string          `json:"token,omitempty"`
	OK      bool            `json:"ok,omitempty"`
	Error   string          `json:"error,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

type helloPayload struct {
	Runtime      string   `json:"runtime"`
	PID          int      `json:"pid"`
	QQVersion    string   `json:"qq_version"`
	Capabilities []string `json:"capabilities"`
}

type runtimeStatus struct {
	State                 string   `json:"state"`
	SelfID                string   `json:"self_id"`
	SelfUID               string   `json:"self_uid"`
	Nickname              string   `json:"nickname"`
	WrapperLoaded         bool     `json:"wrapper_loaded"`
	SessionAttached       bool     `json:"session_attached"`
	AVSDKAvailable        bool     `json:"avsdk_available"`
	AVSDKListenerAttached bool     `json:"avsdk_listener_attached"`
	AVSDKMethods          []string `json:"avsdk_methods"`
	LastError             string   `json:"last_error"`
}

type AVArgumentSummary struct {
	Type          string   `json:"type"`
	NumberValue   *float64 `json:"number_value,omitempty"`
	NumberSpecial string   `json:"number_special,omitempty"`
	IntegerValue  string   `json:"integer_value,omitempty"`
	BooleanValue  *bool    `json:"boolean_value,omitempty"`
	ByteLength    int      `json:"byte_length,omitempty"`
	SHA256        string   `json:"sha256,omitempty"`
	Oversized     bool     `json:"oversized,omitempty"`
	ObjectType    string   `json:"object_type,omitempty"`
	Keys          []string `json:"keys,omitempty"`
	KeysTruncated bool     `json:"keys_truncated,omitempty"`
}

type AVEventSummary struct {
	Sequence            uint64              `json:"sequence"`
	Callback            string              `json:"callback"`
	ReceivedAt          string              `json:"received_at"`
	ActionCodeCandidate *int64              `json:"action_code_candidate"`
	ArgumentCount       int                 `json:"argument_count"`
	Arguments           []AVArgumentSummary `json:"arguments"`
	ArgumentsTruncated  bool                `json:"arguments_truncated"`
}

type actionPayload struct {
	Name   string         `json:"name"`
	Params map[string]any `json:"params,omitempty"`
}

func encodeEnvelope(current envelope, maximum int) ([]byte, error) {
	current.Version = protocolVersion
	encoded, err := json.Marshal(current)
	if err != nil {
		return nil, fmt.Errorf("encode QQNT IPC envelope: %w", err)
	}
	if len(encoded)+1 > maximum {
		return nil, fmt.Errorf("QQNT IPC frame exceeds %d bytes", maximum)
	}
	return append(encoded, '\n'), nil
}

func decodeEnvelope(line []byte) (envelope, error) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return envelope{}, fmt.Errorf("QQNT IPC frame is empty")
	}
	var current envelope
	if err := json.Unmarshal(line, &current); err != nil {
		return envelope{}, fmt.Errorf("decode QQNT IPC envelope: %w", err)
	}
	if current.Version != protocolVersion {
		return envelope{}, fmt.Errorf(
			"unsupported QQNT IPC protocol version %d",
			current.Version,
		)
	}
	if current.Type == "" {
		return envelope{}, fmt.Errorf("QQNT IPC envelope type is empty")
	}
	return current, nil
}

func decodePayload(raw json.RawMessage, target any) error {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("QQNT IPC payload is empty")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode QQNT IPC payload: %w", err)
	}
	return nil
}

func decodeAny(raw json.RawMessage) (any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var result any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&result); err != nil {
		return nil, fmt.Errorf("decode QQNT action result: %w", err)
	}
	return result, nil
}
