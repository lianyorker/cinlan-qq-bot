// Package tts synthesizes text into voice files for QQ record messages.
// The default provider talks to Edge's readaloud TTS over WebSocket
// (free, no key); a shell command provider is available as a fallback.
package tts

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Config configures a voice provider.
type Config struct {
	// Mode: "edge" (default), "openai", "command", or "off".
	Mode string
	// Voice is the voice name, e.g. zh-CN-XiaoyiNeural (edge) or nova (openai).
	Voice string
	// APIURL/APIKey/Model are used in "openai" mode — APIURL is the full
	// /audio/speech endpoint.
	APIURL string
	APIKey string
	Model  string
	// Rate/Pitch map to SSML prosody (edge only, e.g. "+10%", "+2Hz").
	Rate  string
	Pitch string
	// Command is used in "command" mode, e.g.
	// "edge-tts --voice zh-CN-XiaoyiNeural --write-media {out} --text {text}".
	Command string
	Timeout time.Duration
}

// Provider synthesizes text to an audio file. Voice overrides the
// provider's default voice when non-empty; rate is speech-rate multiplier
// (0 means provider default).
type Provider interface {
	Synthesize(
		ctx context.Context,
		text, outPath, voice string,
		rate float64,
	) error
}

// New builds a provider from config; nil means voice is disabled.
func New(cfg Config) (Provider, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Mode)) {
	case "", "edge":
		voice := strings.TrimSpace(cfg.Voice)
		if voice == "" {
			voice = "zh-CN-XiaoyiNeural"
		}
		return &edgeProvider{
			voice:   voice,
			rate:    cfg.Rate,
			pitch:   cfg.Pitch,
			timeout: cfg.Timeout,
		}, nil
	case "openai":
		if strings.TrimSpace(cfg.APIURL) == "" {
			return nil, fmt.Errorf("tts openai mode needs VOICE_API_URL")
		}
		voice := strings.TrimSpace(cfg.Voice)
		if voice == "" || strings.HasPrefix(voice, "zh-CN-") {
			voice = "nova"
		}
		model := strings.TrimSpace(cfg.Model)
		if model == "" {
			model = "tts-1-hd"
		}
		return &openaiProvider{
			apiURL:  cfg.APIURL,
			apiKey:  cfg.APIKey,
			model:   model,
			voice:   voice,
			timeout: cfg.Timeout,
			client:  &http.Client{Timeout: cfg.Timeout},
		}, nil
	case "dashscope":
		if strings.TrimSpace(cfg.APIKey) == "" {
			return nil, fmt.Errorf("tts dashscope mode needs DASHSCOPE_API_KEY")
		}
		voice := strings.TrimSpace(cfg.Voice)
		if voice == "" || strings.HasPrefix(voice, "zh-CN-") {
			voice = "zhichu"
		}
		model := strings.TrimSpace(cfg.Model)
		if model == "" {
			model = "sambert-zhichu-v1"
		}
		return &dashProvider{
			apiKey:  cfg.APIKey,
			baseURL: cfg.APIURL,
			model:   model,
			voice:   voice,
			timeout: cfg.Timeout,
		}, nil
	case "command":
		if strings.TrimSpace(cfg.Command) == "" {
			return nil, fmt.Errorf("tts command mode needs a command template")
		}
		return &commandProvider{tmpl: cfg.Command, timeout: cfg.Timeout}, nil
	case "off", "none", "disabled":
		return nil, nil
	default:
		return nil, fmt.Errorf("unknown tts mode %q", cfg.Mode)
	}
}

// commandProvider shells out to an installed TTS CLI (e.g. edge-tts).
type commandProvider struct {
	tmpl    string
	timeout time.Duration
}

func (p *commandProvider) Synthesize(
	ctx context.Context,
	text, outPath, _voice string,
	_rate float64,
) error {
	if p.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.timeout)
		defer cancel()
	}
	line := strings.ReplaceAll(p.tmpl, "{text}", shellQuote(text))
	line = strings.ReplaceAll(line, "{out}", shellQuote(outPath))
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", line)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("tts command failed: %v: %s", err, trunc(out, 200))
	}
	return nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func trunc(value []byte, limit int) string {
	text := strings.TrimSpace(string(value))
	if len([]rune(text)) > limit {
		return string([]rune(text)[:limit])
	}
	return text
}
