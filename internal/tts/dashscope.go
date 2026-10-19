package tts

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// DashScope TTS over the native WebSocket inference API
// (wss://dashscope.aliyuncs.com/api-ws/v1/inference/). The OpenAI-compat
// endpoint does not expose audio/speech, and the server python is too old
// for the official SDK — this speaks the run-task protocol directly.
const dashScopeInferenceURL = "wss://dashscope.aliyuncs.com/api-ws/v1/inference/"

type dashProvider struct {
	apiKey  string
	baseURL string
	model   string
	voice   string
	timeout time.Duration
}

type dashTaskMessage struct {
	Header  dashHeader `json:"header"`
	Payload any        `json:"payload"`
}

type dashHeader struct {
	Action    string `json:"action"`
	TaskID    string `json:"task_id"`
	Streaming string `json:"streaming"`
	Event     string `json:"event"`
	ErrorCode string `json:"error_code,omitempty"`
	Message   string `json:"error_message,omitempty"`
}

func (p *dashProvider) Synthesize(
	ctx context.Context,
	text, outPath, voice string,
	rate float64,
) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("tts text is empty")
	}
	if p.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.timeout)
		defer cancel()
	}
	baseURL := strings.TrimSpace(p.baseURL)
	if baseURL == "" {
		baseURL = dashScopeInferenceURL
	}
	header := http.Header{}
	header.Set("Authorization", "Bearer "+p.apiKey)
	conn, resp, err := websocket.DefaultDialer.DialContext(
		ctx, baseURL, header,
	)
	if err != nil {
		status := ""
		if resp != nil {
			status = resp.Status
		}
		return fmt.Errorf("dashscope dial failed %s: %w", status, err)
	}
	defer conn.Close()

	voiceName := voice
	if voiceName == "" {
		voiceName = p.voice
	}
	speechRate := 1.0
	if rate > 0.2 && rate <= 4.0 {
		speechRate = rate
	}
	taskID := randHex(16)
	runTask := dashTaskMessage{
		Header: dashHeader{
			Action:    "run-task",
			TaskID:    taskID,
			Streaming: "out",
		},
		Payload: map[string]any{
			"task_group": "aigc",
			"task":       "tts",
			"function":   "SpeechSynthesizer",
			"model":      p.model,
			"parameters": map[string]any{
				"volume":      50,
				"text_type":   "PlainText",
				"voice":       voiceName,
				"sample_rate": 22050,
				"format":      "mp3",
				"speech_rate": speechRate,
			},
			"input": map[string]any{"text": text},
		},
	}
	if err := conn.WriteJSON(runTask); err != nil {
		return fmt.Errorf("dashscope run-task: %w", err)
	}

	var audio []byte
	for {
		msgType, payload, readErr := conn.ReadMessage()
		if readErr != nil {
			return fmt.Errorf("dashscope read: %w", readErr)
		}
		if msgType == websocket.BinaryMessage {
			audio = append(audio, payload...)
			continue
		}
		var event dashHeader
		var envelope struct {
			Header dashHeader `json:"header"`
		}
		if jsonErr := json.Unmarshal(payload, &envelope); jsonErr == nil {
			event = envelope.Header
		}
		switch event.Event {
		case "task-finished":
			if len(audio) == 0 {
				return fmt.Errorf("dashscope returned no audio")
			}
			if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
				return err
			}
			return os.WriteFile(outPath, audio, 0o600)
		case "task-failed":
			return fmt.Errorf(
				"dashscope task failed %s: %s",
				event.ErrorCode, event.Message,
			)
		}
	}
}
