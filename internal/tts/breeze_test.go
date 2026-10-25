package tts

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBreezeSynthesizePostsVoiceIDAndWritesAudio(t *testing.T) {
	fakeAudio := make([]byte, 512)
	for i := range fakeAudio {
		fakeAudio[i] = byte(i)
	}
	var gotPath, gotKey string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter, r *http.Request,
	) {
		gotPath = r.URL.EscapedPath()
		gotKey = r.Header.Get("xi-api-key")
		if r.URL.Query().Get("output_format") != "mp3" {
			t.Fatalf("output_format missing: %s", r.URL.RawQuery)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(fakeAudio)
	}))
	defer server.Close()

	provider, err := New(Config{
		Mode:    "breeze",
		APIURL:  server.URL,
		APIKey:  "brz_test_key",
		Voice:   "voc_default",
		Model:   "breeze-tts-2-multilingual",
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "clip.mp3")
	if err := provider.Synthesize(
		context.Background(), "你好呀", out, "voc_per_persona", 1.2,
	); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/text-to-speech/voc_per_persona" {
		t.Fatalf("path = %s", gotPath)
	}
	if gotKey != "brz_test_key" {
		t.Fatalf("xi-api-key = %s", gotKey)
	}
	if gotBody["text"] != "你好呀" ||
		gotBody["model_id"] != "breeze-tts-2-multilingual" {
		t.Fatalf("body = %#v", gotBody)
	}
	if speed, ok := gotBody["speed"].(float64); !ok || speed != 1.2 {
		t.Fatalf("speed = %#v", gotBody["speed"])
	}
	written, err := os.ReadFile(out)
	if err != nil || len(written) != len(fakeAudio) {
		t.Fatalf("audio file = %d bytes, err %v", len(written), err)
	}
}

func TestBreezeMissingVoiceIDAndAPIErrors(t *testing.T) {
	provider, err := New(Config{Mode: "breeze", APIKey: "brz_x"})
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "x.mp3")
	if err := provider.Synthesize(
		context.Background(), "hi", out, "", 0,
	); err == nil {
		t.Fatal("expected missing voice_id error")
	}

	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter, _ *http.Request,
	) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"AUTH_REQUIRED"}`))
	}))
	defer server.Close()
	provider, err = New(Config{
		Mode: "breeze", APIURL: server.URL,
		APIKey: "brz_x", Voice: "voc_1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Synthesize(
		context.Background(), "hi", out, "", 0,
	); err == nil {
		t.Fatal("expected HTTP error")
	}
}

func TestBreezeRequiresAPIKey(t *testing.T) {
	if _, err := New(Config{Mode: "breeze"}); err == nil {
		t.Fatal("expected missing API key error")
	}
}
