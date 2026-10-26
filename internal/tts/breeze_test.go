package tts

import (
	"bytes"
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

func TestBreezePerformanceSendsInstructionsAndGuidance(t *testing.T) {
	var gotBody map[string]any
	fakeAudio := bytes.Repeat([]byte{0x2}, 512)
	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(fakeAudio)
		},
	))
	defer server.Close()

	provider, err := New(Config{
		Mode:   "breeze",
		APIURL: server.URL,
		APIKey: "brz_test_key",
		Voice:  "voc_default",
	})
	if err != nil {
		t.Fatal(err)
	}
	perf, ok := provider.(interface {
		SynthesizeWithPerformance(
			context.Context, string, string, string, float64, string, float64,
		) error
	})
	if !ok {
		t.Fatal("breeze provider missing SynthesizeWithPerformance")
	}
	out := filepath.Join(t.TempDir(), "sing.mp3")
	if err := perf.SynthesizeWithPerformance(
		context.Background(), "[唱歌] 啦啦啦", out, "", 1.0, "唱出来", 4.0,
	); err != nil {
		t.Fatal(err)
	}
	if gotBody["instructions"] != "唱出来" {
		t.Fatalf("instructions = %#v", gotBody["instructions"])
	}
	vs, _ := gotBody["voice_settings"].(map[string]any)
	if vs["guidance_scale"] != 4.0 {
		t.Fatalf("voice_settings = %#v", gotBody["voice_settings"])
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
