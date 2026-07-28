package agent

import (
	"encoding/base64"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lianyorker/cinlan-qq-bot/internal/domain"
	"github.com/lianyorker/cinlan-qq-bot/internal/message"
)

func TestBuildOpenAIMessagesIncludesRemoteImage(t *testing.T) {
	client := NewHTTPClient(Config{}, slog.Default())

	messages, err := client.buildOpenAIMessages(domain.AgentRequest{
		Text:  "[图片]",
		Chain: message.Chain{message.Image("https://example.test/error.png")},
	})

	if err != nil {
		t.Fatalf("buildOpenAIMessages: %v", err)
	}
	parts, ok := messages[len(messages)-1].Content.([]openAIContentPart)
	if !ok || len(parts) != 2 || parts[1].ImageURL == nil ||
		parts[1].ImageURL.URL != "https://example.test/error.png" {
		t.Fatalf("image content = %#v", messages[len(messages)-1].Content)
	}
}

func TestBuildOpenAIMessagesRejectsLocalImage(t *testing.T) {
	client := NewHTTPClient(Config{}, slog.Default())

	_, err := client.buildOpenAIMessages(domain.AgentRequest{
		Text:  "[图片]",
		Chain: message.Chain{message.Image(`D:\qq-cache\error.png`)},
	})

	if !errors.Is(err, ErrInputImageUnavailable) {
		t.Fatalf("error = %v, want ErrInputImageUnavailable", err)
	}
}

func TestBuildOpenAIMessagesIncludesAllowedLocalImage(t *testing.T) {
	root := t.TempDir()
	data := []byte("\x89PNG\r\n\x1a\n")
	path := filepath.Join(root, "error.png")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	client := NewHTTPClient(Config{}, slog.Default())
	if err := client.SetLocalImagePolicy([]string{root}, 1024); err != nil {
		t.Fatalf("SetLocalImagePolicy() error = %v", err)
	}

	messages, err := client.buildOpenAIMessages(domain.AgentRequest{
		Text:  "看看这张图",
		Chain: message.Chain{message.Image(path)},
	})

	if err != nil {
		t.Fatalf("buildOpenAIMessages() error = %v", err)
	}
	parts, ok := messages[len(messages)-1].Content.([]openAIContentPart)
	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
	if !ok || len(parts) != 2 || parts[1].ImageURL == nil ||
		parts[1].ImageURL.URL != want {
		t.Fatalf("image content = %#v", messages[len(messages)-1].Content)
	}
}

func TestBuildOpenAIMessagesUsesAllowedLocalImageWhenOriginIsSenderFileURI(t *testing.T) {
	root := t.TempDir()
	data := []byte("\x89PNG\r\n\x1a\n")
	path := filepath.Join(root, "error.png")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	client := NewHTTPClient(Config{}, slog.Default())
	if err := client.SetLocalImagePolicy([]string{root}, 1024); err != nil {
		t.Fatalf("SetLocalImagePolicy() error = %v", err)
	}

	messages, err := client.buildOpenAIMessages(domain.AgentRequest{
		Text: "查看图片",
		Chain: message.Chain{message.Attachment(message.TypeImage, map[string]any{
			"origin_image_url": "file://C:\\Users\\sender\\unavailable.png",
			"file_path":        path,
		})},
	})

	if err != nil {
		t.Fatalf("buildOpenAIMessages() error = %v", err)
	}
	parts, ok := messages[len(messages)-1].Content.([]openAIContentPart)
	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
	if !ok || len(parts) != 2 || parts[1].ImageURL == nil ||
		parts[1].ImageURL.URL != want {
		t.Fatalf("image content = %#v", messages[len(messages)-1].Content)
	}
}

func TestBuildOpenAIMessagesMapsSenderQQNTCachePathToAllowedRoot(t *testing.T) {
	root := t.TempDir()
	data := []byte("\x89PNG\r\n\x1a\n")
	path := filepath.Join(root, "2026-07", "Ori", "error.png")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	client := NewHTTPClient(Config{}, slog.Default())
	if err := client.SetLocalImagePolicy([]string{root}, 1024); err != nil {
		t.Fatalf("SetLocalImagePolicy() error = %v", err)
	}

	senderPath := `file://C:\Users\Example\Documents\Tencent Files\1000000000\nt_qq\nt_data\Pic\2026-07\Ori\error.png`
	messages, err := client.buildOpenAIMessages(domain.AgentRequest{
		Text:  "识别图片",
		Chain: message.Chain{message.Image(senderPath)},
	})

	if err != nil {
		t.Fatalf("buildOpenAIMessages() error = %v", err)
	}
	parts, ok := messages[len(messages)-1].Content.([]openAIContentPart)
	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
	if !ok || len(parts) != 2 || parts[1].ImageURL == nil ||
		parts[1].ImageURL.URL != want {
		t.Fatalf("image content = %#v", messages[len(messages)-1].Content)
	}
}

func TestBuildOpenAIMessagesRejectsLocalImageOutsideAllowedRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	path := filepath.Join(outside, "error.png")
	if err := os.WriteFile(path, []byte("\x89PNG\r\n\x1a\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	client := NewHTTPClient(Config{}, slog.Default())
	if err := client.SetLocalImagePolicy([]string{root}, 1024); err != nil {
		t.Fatalf("SetLocalImagePolicy() error = %v", err)
	}

	_, err := client.buildOpenAIMessages(domain.AgentRequest{
		Text:  "看看这张图",
		Chain: message.Chain{message.Image(path)},
	})

	if !errors.Is(err, ErrInputImageUnavailable) {
		t.Fatalf("error = %v, want ErrInputImageUnavailable", err)
	}
}

func TestBuildOpenAIMessagesRejectsNonImageAndOversizedLocalFile(t *testing.T) {
	root := t.TempDir()
	client := NewHTTPClient(Config{}, slog.Default())
	if err := client.SetLocalImagePolicy([]string{root}, 8); err != nil {
		t.Fatalf("SetLocalImagePolicy() error = %v", err)
	}
	for name, data := range map[string][]byte{
		"not-image.txt": []byte("not img"),
		"too-large.png": []byte("\x89PNG\r\n\x1a\nx"),
	} {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", name, err)
		}
		_, err := client.buildOpenAIMessages(domain.AgentRequest{
			Chain: message.Chain{message.Image(path)},
		})
		if !errors.Is(err, ErrInputImageUnavailable) {
			t.Fatalf("%s error = %v, want ErrInputImageUnavailable", name, err)
		}
	}
}

func TestLocalImageFileURI(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "space name.png")
	if err := os.WriteFile(path, []byte("\x89PNG\r\n\x1a\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	client := NewHTTPClient(Config{}, slog.Default())
	if err := client.SetLocalImagePolicy([]string{root}, 1024); err != nil {
		t.Fatalf("SetLocalImagePolicy() error = %v", err)
	}
	reference := "file:///" + strings.ReplaceAll(filepath.ToSlash(path), " ", "%20")
	if filepath.VolumeName(path) == "" {
		reference = "file://" + strings.ReplaceAll(filepath.ToSlash(path), " ", "%20")
	}

	if _, err := client.resolveImageReference(reference); err != nil {
		t.Fatalf("resolveImageReference() error = %v", err)
	}
}
