// Package song generates real songs (melody + vocals + arrangement)
// from lyrics — unlike a TTS provider that only speaks text. The
// StepFun stepaudio-3-music-preview model runs as an async
// submit/poll task and returns the rendered audio inline.
package song

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// StepFun is a lyrics-to-song generator backed by the StepFun music
// API: submit returns a task_id, query polls until SUCCESS and
// yields base64 audio.
type StepFun struct {
	APIURL       string
	APIKey       string
	Model        string
	HTTPClient   *http.Client
	PollInterval time.Duration
	MaxWait      time.Duration
}

type stepfunSubmitRequest struct {
	Task           string `json:"task"`
	ModelID        string `json:"model_id"`
	Caption        string `json:"caption"`
	Lyrics         string `json:"lyrics,omitempty"`
	ResponseFormat string `json:"response_format"`
	SampleRate     int    `json:"sample_rate,omitempty"`
}

type stepfunSubmitResponse struct {
	TaskID string `json:"task_id"`
	Error  *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

type stepfunQueryResponse struct {
	Status string `json:"status"`
	Audio  string `json:"audio"`
	Error  *struct {
		Stage   string `json:"stage"`
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// Generate renders lyrics+caption into a full song and writes the
// mp3 to outPath. Generation typically takes 30s–3min — the caller
// should run it off the reply hot path.
func (p *StepFun) Generate(
	ctx context.Context,
	lyrics, caption, outPath string,
) error {
	if strings.TrimSpace(p.APIKey) == "" {
		return fmt.Errorf("stepfun song: api key is empty")
	}
	lyrics = strings.TrimSpace(lyrics)
	if lyrics == "" {
		return fmt.Errorf("stepfun song: lyrics are empty")
	}
	body, err := json.Marshal(stepfunSubmitRequest{
		Task:           "text_to_music",
		ModelID:        p.modelID(),
		Caption:        caption,
		Lyrics:         lyrics,
		ResponseFormat: "mp3",
		SampleRate:     24000,
	})
	if err != nil {
		return fmt.Errorf("stepfun song: marshal submit: %w", err)
	}
	var submit stepfunSubmitResponse
	if err := p.post(ctx, "/v1/audio/music/submit", body, &submit); err != nil {
		return err
	}
	if submit.Error != nil {
		return fmt.Errorf(
			"stepfun song: submit %s: %s",
			submit.Error.Type, submit.Error.Message,
		)
	}
	if submit.TaskID == "" {
		return fmt.Errorf("stepfun song: submit returned no task_id")
	}
	audio, err := p.wait(ctx, submit.TaskID)
	if err != nil {
		return err
	}
	if mkErr := os.MkdirAll(filepath.Dir(outPath), 0o755); mkErr != nil {
		return fmt.Errorf("stepfun song: mkdir: %w", mkErr)
	}
	if wErr := os.WriteFile(outPath, audio, 0o600); wErr != nil {
		return fmt.Errorf("stepfun song: write %s: %w", outPath, wErr)
	}
	return nil
}

// wait polls the task until SUCCESS, FAILED, or the deadline. The
// SUCCESS response carries the mp3 as base64 in `audio`.
func (p *StepFun) wait(ctx context.Context, taskID string) ([]byte, error) {
	poll := p.PollInterval
	if poll <= 0 {
		poll = 4 * time.Second
	}
	maxWait := p.MaxWait
	if maxWait <= 0 {
		maxWait = 5 * time.Minute
	}
	deadline := time.Now().Add(maxWait)
	for {
		var q stepfunQueryResponse
		payload, err := json.Marshal(map[string]string{"task_id": taskID})
		if err != nil {
			return nil, fmt.Errorf("stepfun song: marshal query: %w", err)
		}
		if err := p.post(ctx, "/v1/audio/music/query", payload, &q); err != nil {
			return nil, err
		}
		switch q.Status {
		case "SUCCESS":
			audio, err := base64.StdEncoding.DecodeString(q.Audio)
			if err != nil || len(audio) == 0 {
				return nil, fmt.Errorf("stepfun song: bad audio payload")
			}
			return audio, nil
		case "FAILED":
			msg := "unknown"
			stage := ""
			if q.Error != nil {
				msg = q.Error.Message
				stage = q.Error.Stage
			}
			return nil, fmt.Errorf(
				"stepfun song: task failed at %s: %s", stage, msg,
			)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf(
				"stepfun song: task %s still %s after %s",
				taskID, q.Status, maxWait,
			)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(poll):
		}
	}
}

func (p *StepFun) modelID() string {
	if strings.TrimSpace(p.Model) == "" {
		return "stepaudio-3-music-preview"
	}
	return p.Model
}

func (p *StepFun) post(
	ctx context.Context,
	path string,
	body []byte,
	out any,
) error {
	base := strings.TrimRight(p.APIURL, "/")
	if base == "" {
		base = "https://api.stepfun.com"
	}
	client := p.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 45 * time.Second}
	}
	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, base+path, bytes.NewReader(body),
	)
	if err != nil {
		return fmt.Errorf("stepfun song: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+p.APIKey)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("stepfun song: %s: %w", path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return fmt.Errorf("stepfun song: read %s: %w", path, err)
	}
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error *struct {
				Message string `json:"message"`
				Type    string `json:"type"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error != nil {
			return fmt.Errorf(
				"stepfun song: %s http %d %s: %s",
				path, resp.StatusCode, e.Error.Type, e.Error.Message,
			)
		}
		return fmt.Errorf(
			"stepfun song: %s http %d: %s",
			path, resp.StatusCode, truncate(string(raw), 200),
		)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("stepfun song: decode %s: %w", path, err)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
