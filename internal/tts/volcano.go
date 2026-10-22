package tts

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const volcanoDefaultEndpoint = "https://openspeech.bytedance.com/api/v3/plan/tts/unidirectional"

// volcanoProvider calls Volcano Engine seed-tts (豆包语音合成大模型) via
// the V3 unidirectional HTTP API. New-console auth is just
// "X-Api-Key: <api-key>" + "X-Api-Resource-Id: seed-tts-2.0"; the old
// console needs X-Api-App-Id + X-Api-Access-Key instead. The response is
// an NDJSON stream of {code,data} frames — base64 audio chunks that end
// with code 20000000.
type volcanoProvider struct {
	apiURL     string
	apiKey     string
	appID      string // old console only
	resourceID string
	voice      string
	emotion    string
	timeout    time.Duration
	client     *http.Client
}

type volcanoRequest struct {
	User      *volcanoUser  `json:"user,omitempty"`
	ReqParams volcanoParams `json:"req_params"`
}

type volcanoUser struct {
	UID string `json:"uid"`
}

type volcanoParams struct {
	Text        string             `json:"text"`
	Speaker     string             `json:"speaker"`
	AudioParams volcanoAudioParams `json:"audio_params"`
}

type volcanoAudioParams struct {
	Format     string `json:"format"`
	SampleRate int    `json:"sample_rate"`
	// SpeechRate is a percent offset (-50..100); 0 keeps the default.
	SpeechRate int    `json:"speech_rate,omitempty"`
	Emotion    string `json:"emotion,omitempty"`
}

type volcanoFrame struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data"`
}

func (p *volcanoProvider) Synthesize(
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
	speaker := strings.TrimSpace(voice)
	if speaker == "" {
		speaker = p.voice
	}
	// rate arrives as a multiplier (1.0 = default); V3 wants a percent.
	speechRate := 0
	if rate > 0 {
		speechRate = int((rate - 1.0) * 100)
		if speechRate < -50 {
			speechRate = -50
		} else if speechRate > 100 {
			speechRate = 100
		}
	}
	payload, err := json.Marshal(volcanoRequest{
		User: &volcanoUser{UID: "cinlan-bot"},
		ReqParams: volcanoParams{
			Text:    text,
			Speaker: speaker,
			AudioParams: volcanoAudioParams{
				Format:     "mp3",
				SampleRate: 24000,
				SpeechRate: speechRate,
				Emotion:    p.emotion,
			},
		},
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
	if p.appID != "" {
		// Old console: appid + access token pair; the token doubles as
		// the access key here.
		request.Header.Set("X-Api-App-Id", p.appID)
		request.Header.Set("X-Api-Access-Key", p.apiKey)
	} else {
		// New console: single API Key.
		request.Header.Set("X-Api-Key", p.apiKey)
	}
	request.Header.Set("X-Api-Resource-Id", p.resourceID)
	request.Header.Set("X-Api-Request-Id", newVolcanoReqID())
	response, err := p.client.Do(request)
	if err != nil {
		return fmt.Errorf("volcano tts request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK ||
		response.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4<<10))
		return fmt.Errorf(
			"volcano tts HTTP %d: %s",
			response.StatusCode, trunc(body, 200),
		)
	}
	// NDJSON stream: {code:0,data:"<base64>"} chunks, terminator
	// {code:20000000}. Any other non-zero code is an error frame.
	var audio bytes.Buffer
	scanner := bufio.NewScanner(io.LimitReader(response.Body, 16<<20))
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var frame volcanoFrame
		if err := json.Unmarshal(line, &frame); err != nil {
			return fmt.Errorf("volcano tts frame: %w", err)
		}
		switch {
		case frame.Code == 20000000:
			goto done
		case frame.Code != 0:
			return fmt.Errorf(
				"volcano tts code %d: %s",
				frame.Code, trunc([]byte(frame.Message), 200),
			)
		case frame.Data != "":
			chunk, err := base64.StdEncoding.DecodeString(frame.Data)
			if err != nil {
				return fmt.Errorf("volcano tts audio decode: %w", err)
			}
			audio.Write(chunk)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("volcano tts stream: %w", err)
	}
done:
	if audio.Len() < 128 {
		return fmt.Errorf("volcano tts returned %d bytes — not audio", audio.Len())
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(outPath, audio.Bytes(), 0o600)
}

func newVolcanoReqID() string {
	var seed [16]byte
	if _, err := rand.Read(seed[:]); err != nil {
		return fmt.Sprintf("req-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(seed[:])
}
