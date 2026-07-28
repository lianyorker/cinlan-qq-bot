package media

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/message"
	"github.com/lianyorker/cinlan-qq-bot/internal/tool"
)

func TestImageGeneratorOpenAISavesAndReturnsImageChain(t *testing.T) {
	imageData := testPNG(t)
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.Method != http.MethodPost {
			t.Fatalf("method = %s", request.Method)
		}
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["model"] != "image-test" ||
			payload["prompt"] != "日落海面" ||
			payload["size"] != "1024x1024" {
			t.Fatalf("payload = %#v", payload)
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"data": []map[string]any{{
				"b64_json": base64.StdEncoding.EncodeToString(imageData),
			}},
		})
	}))
	defer server.Close()

	root := t.TempDir()
	generator, err := NewImageGenerator(ImageGeneratorConfig{
		Mode:        "openai",
		APIURL:      server.URL,
		Model:       "image-test",
		OutputDir:   "generated",
		AllowedRoot: root,
		MaxBytes:    1 << 20,
		Timeout:     time.Second,
		Retention:   time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := generator.Tool().Handler(context.Background(), tool.Call{
		Arguments: json.RawMessage(`{"prompt":"日落海面"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Response == nil ||
		len(result.Response.Chain) != 2 ||
		result.Response.Chain[1].Type != message.TypeImage {
		t.Fatalf("response = %#v", result.Response)
	}
	path, _ := result.Response.Chain[1].Data["file"].(string)
	if !filepath.IsAbs(path) || filepath.Dir(path) != generator.OutputDir() {
		t.Fatalf("generated path = %q", path)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, imageData) {
		t.Fatal("stored image differs from provider response")
	}
}

func TestImageGeneratorRejectsOutputOutsideAllowedRoot(t *testing.T) {
	root := t.TempDir()
	_, err := NewImageGenerator(ImageGeneratorConfig{
		Mode:        "openai",
		APIURL:      "https://example.test/v1/images/generations",
		Model:       "image-test",
		OutputDir:   filepath.Join(t.TempDir(), "outside"),
		AllowedRoot: root,
		MaxBytes:    1 << 20,
		Timeout:     time.Second,
	})
	if err == nil {
		t.Fatal("outside generated image directory was accepted")
	}
}

func TestImageGeneratorPollinationsUsesEnhancementAndRandomSeed(t *testing.T) {
	imageData := testPNG(t)
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		query := request.URL.Query()
		if query.Get("model") != "flux" ||
			query.Get("enhance") != "true" ||
			query.Get("width") != "1024" ||
			query.Get("height") != "1024" ||
			query.Get("seed") == "" {
			t.Fatalf("pollinations query = %#v", query)
		}
		writer.Header().Set("Content-Type", "image/png")
		_, _ = writer.Write(imageData)
	}))
	defer server.Close()

	root := t.TempDir()
	generator, err := NewImageGenerator(ImageGeneratorConfig{
		Mode:        "pollinations",
		APIURL:      server.URL + "/prompt",
		Model:       "flux",
		OutputDir:   "generated",
		AllowedRoot: root,
		MaxBytes:    1 << 20,
		Timeout:     time.Second,
		Retention:   time.Hour,
		Enhance:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := generator.Tool().Handler(context.Background(), tool.Call{
		Arguments: json.RawMessage(`{"prompt":"日落海面"}`),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestValidateGeneratedImageRejectsText(t *testing.T) {
	if _, _, _, err := validateGeneratedImage([]byte("not an image")); err == nil {
		t.Fatal("text response was accepted as an image")
	}
}

func TestLiveImageGenerator(t *testing.T) {
	if os.Getenv("CINLAN_LIVE_IMAGE_TEST") != "1" {
		t.Skip("set CINLAN_LIVE_IMAGE_TEST=1 to call the configured image provider")
	}
	root := t.TempDir()
	generator, err := NewImageGenerator(ImageGeneratorConfig{
		Mode:        "pollinations",
		APIURL:      "https://image.pollinations.ai/prompt",
		Model:       "flux",
		OutputDir:   "generated",
		AllowedRoot: root,
		MaxBytes:    20 << 20,
		Timeout:     2 * time.Minute,
		Retention:   time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := generator.Tool().Handler(context.Background(), tool.Call{
		Arguments: json.RawMessage(`{"prompt":"日落海面，金色天空，写实摄影"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Response == nil || len(result.Response.Chain) != 2 {
		t.Fatalf("response = %#v", result.Response)
	}
	path, _ := result.Response.Chain[1].Data["file"].(string)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() <= 0 {
		t.Fatal("live image provider returned an empty file")
	}
}

func testPNG(t *testing.T) []byte {
	t.Helper()
	source := image.NewRGBA(image.Rect(0, 0, 2, 2))
	source.Set(0, 0, color.RGBA{R: 255, A: 255})
	var output bytes.Buffer
	if err := png.Encode(&output, source); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
