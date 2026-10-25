package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const breezeDefaultAPIBase = "https://api.breeze.blue"

// breezeProvider calls Breeze Blue text-to-speech
// (docs.breezeblue.ai): POST {apiBase}/v1/text-to-speech/{voice_id}
// ?output_format=mp3 with an xi-api-key header; the response body is
// raw audio. Supports cloned/designed voices via voice_id (voc_…),
// voice instructions, and the multilingual model_id.
type breezeProvider struct {
	apiBase string
	apiKey  string
	model   string
	voice   string // default voice_id, e.g. voc_xeh3w54cqvnp
	timeout time.Duration
	client  *http.Client
}

type breezeRequest struct {
	Text    string   `json:"text"`
	ModelID string   `json:"model_id,omitempty"`
	Speed   *float64 `json:"speed,omitempty"`
}

func (p *breezeProvider) Synthesize(
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
	voiceID := strings.TrimSpace(voice)
	if voiceID == "" {
		voiceID = p.voice
	}
	if voiceID == "" {
		return fmt.Errorf("breeze tts needs a voice_id (voc_…); set VOICE_NAME")
	}
	body := breezeRequest{
		Text:    text,
		ModelID: p.model,
	}
	if rate > 0 && rate != 1.0 {
		speed := rate
		if speed < 0.25 {
			speed = 0.25
		} else if speed > 4.0 {
			speed = 4.0
		}
		body.Speed = &speed
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	endpoint := strings.TrimRight(p.apiBase, "/") +
		"/v1/text-to-speech/" + url.PathEscape(voiceID) +
		"?output_format=mp3"
	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, endpoint, bytes.NewReader(payload),
	)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("xi-api-key", p.apiKey)
	response, err := p.client.Do(request)
	if err != nil {
		return fmt.Errorf("breeze tts request: %w", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < http.StatusOK ||
		response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf(
			"breeze tts HTTP %d: %s",
			response.StatusCode, trunc(raw, 200),
		)
	}
	if len(raw) < 128 {
		return fmt.Errorf("breeze tts returned %d bytes — not audio", len(raw))
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(outPath, raw, 0o600)
}
