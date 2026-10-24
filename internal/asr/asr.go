// Package asr posts short voice files to an OpenAI-compatible
// /audio/transcriptions endpoint (SiliconFlow FunAudioLLM/SenseVoiceSmall
// is free; Groq whisper-large-v3 also speaks this shape) so inbound voice
// messages land as readable text instead of a bare [语音] marker.
package asr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Config describes the transcription endpoint. Disabled when URL is empty.
type Config struct {
	APIURL  string // e.g. https://api.siliconflow.cn/v1
	APIKey  string // Bearer token (env ASR_API_KEY)
	Model   string // e.g. FunAudioLLM/SenseVoiceSmall
	Timeout time.Duration
}

// Client transcribes audio files via the configured endpoint.
type Client struct {
	cfg        Config
	httpClient *http.Client
}

// New returns nil when no endpoint is configured — voice transcription is
// opt-in, never a hard dependency.
func New(cfg Config) *Client {
	if strings.TrimSpace(cfg.APIURL) == "" {
		return nil
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	if strings.TrimSpace(cfg.Model) == "" {
		cfg.Model = "FunAudioLLM/SenseVoiceSmall"
	}
	return &Client{cfg: cfg, httpClient: &http.Client{Timeout: cfg.Timeout}}
}

// Transcribe sends the audio file and returns the transcript text.
// Errors are caller-loggable; the bot keeps working without speech text.
func (c *Client) Transcribe(ctx context.Context, path string) (string, error) {
	if c == nil {
		return "", fmt.Errorf("asr not configured")
	}
	data, err := readFileCapped(path, 8<<20) // 8MB sanity cap
	if err != nil {
		return "", err
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filepath.Base(path))
	if err != nil {
		return "", err
	}
	if _, err := part.Write(data); err != nil {
		return "", err
	}
	if err := writer.WriteField("model", c.cfg.Model); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	endpoint := strings.TrimRight(c.cfg.APIURL, "/")
	if !strings.HasSuffix(endpoint, "/audio/transcriptions") {
		if strings.HasSuffix(endpoint, "/v1") {
			endpoint += "/audio/transcriptions"
		} else {
			endpoint += "/v1/audio/transcriptions"
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if key := strings.TrimSpace(c.cfg.APIKey); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("asr %d: %s", resp.StatusCode,
			truncateRunes(string(raw), 120))
	}
	var parsed struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("asr bad response: %w", err)
	}
	return strings.TrimSpace(parsed.Text), nil
}

func readFileCapped(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, limit))
}

func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}
