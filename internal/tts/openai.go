package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// openaiProvider calls an OpenAI-compatible POST {base}/audio/speech. Works
// against the same proxy the image generator already uses — a reliable
// fallback when the free Edge endpoint rate-limits datacenter IPs.
type openaiProvider struct {
	apiURL  string
	apiKey  string
	model   string
	voice   string
	timeout time.Duration
	client  *http.Client
}

type speechRequest struct {
	Model string `json:"model"`
	Voice string `json:"voice"`
	Input string `json:"input"`
}

func (p *openaiProvider) Synthesize(
	ctx context.Context,
	text, outPath string,
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
	payload, err := json.Marshal(speechRequest{
		Model: p.model,
		Voice: p.voice,
		Input: text,
	})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, p.apiURL, bytes.NewReader(payload),
	)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	response, err := p.client.Do(request)
	if err != nil {
		return fmt.Errorf("openai tts request: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < http.StatusOK ||
		response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf(
			"openai tts HTTP %d: %s",
			response.StatusCode, trunc(body, 200),
		)
	}
	if len(body) < 128 {
		return fmt.Errorf("openai tts returned %d bytes — not audio", len(body))
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(outPath, body, 0o600)
}
