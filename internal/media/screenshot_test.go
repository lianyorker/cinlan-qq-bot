package media

import (
	"context"
	"encoding/json"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/message"
	"github.com/lianyorker/cinlan-qq-bot/internal/tool"
)

func TestWebScreenshotCapturesAndReturnsImageChain(t *testing.T) {
	root := t.TempDir()
	browser := filepath.Join(root, "chrome.exe")
	if err := os.WriteFile(browser, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	screenshot, err := NewWebScreenshot(WebScreenshotConfig{
		BrowserPath:  browser,
		AllowedHosts: []string{"example.com"},
		OutputDir:    "generated",
		AllowedRoot:  root,
		MaxBytes:     1 << 20,
		Timeout:      time.Second,
		Retention:    time.Hour,
		Width:        2,
		Height:       2,
		Wait:         time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	screenshot.preflight = func(
		_ context.Context,
		value string,
	) (string, error) {
		return value, nil
	}
	screenshot.runBrowser = func(
		_ context.Context,
		executable string,
		args []string,
		_ io.Writer,
	) error {
		if executable != browser {
			t.Fatalf("browser = %q", executable)
		}
		var output string
		for _, argument := range args {
			if strings.HasPrefix(argument, "--screenshot=") {
				output = strings.TrimPrefix(argument, "--screenshot=")
			}
		}
		if output == "" ||
			!containsArgumentPrefix(args, "--proxy-server=http://127.0.0.1:") ||
			!containsArgumentPrefix(args, "--user-data-dir=") {
			t.Fatalf("browser args = %#v", args)
		}
		return os.WriteFile(output, testPNG(t), 0o600)
	}

	result, err := screenshot.Tool().Handler(context.Background(), tool.Call{
		Arguments: json.RawMessage(`{"url":"https://example.com/"}`),
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
	if filepath.Dir(path) != screenshot.OutputDir() {
		t.Fatalf("screenshot path = %q", path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestNormalizeScreenshotURLRejectsUnsafeTargets(t *testing.T) {
	allowed, err := normalizeAllowedScreenshotHosts([]string{"example.com"})
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{
		"http://example.com/",
		"https://user:pass@example.com/",
		"https://example.com:8443/",
		"https://example.com/#section",
		"https://127.0.0.1/",
		"https://localhost/",
		"https://not-allowed.example/",
	} {
		if _, err := normalizeScreenshotURL(value, allowed); err == nil {
			t.Fatalf("unsafe URL %q was accepted", value)
		}
	}
	normalized, err := normalizeScreenshotURL(
		"https://EXAMPLE.com:443/index?a=1",
		allowed,
	)
	if err != nil || normalized != "https://example.com/index?a=1" {
		t.Fatalf("normalized URL = %q, %v", normalized, err)
	}
}

func TestPublicAddressValidationRejectsSpecialNetworks(t *testing.T) {
	for _, value := range []string{
		"127.0.0.1",
		"10.0.0.1",
		"100.64.0.1",
		"169.254.1.1",
		"198.18.0.1",
		"192.0.2.1",
		"::1",
		"fc00::1",
		"fe80::1",
		"2001:db8::1",
	} {
		if isPublicAddress(netip.MustParseAddr(value)) {
			t.Fatalf("non-public address %s was accepted", value)
		}
	}
	if !isPublicAddress(netip.MustParseAddr("1.1.1.1")) ||
		!isPublicAddress(netip.MustParseAddr("2606:4700:4700::1111")) {
		t.Fatal("public address was rejected")
	}
}

func TestWebScreenshotRejectsOutputOutsideAllowedRoot(t *testing.T) {
	root := t.TempDir()
	browser := filepath.Join(root, "chrome.exe")
	if err := os.WriteFile(browser, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewWebScreenshot(WebScreenshotConfig{
		BrowserPath:  browser,
		AllowedHosts: []string{"example.com"},
		OutputDir:    filepath.Join(t.TempDir(), "outside"),
		AllowedRoot:  root,
		MaxBytes:     1 << 20,
		Timeout:      time.Second,
		Width:        1024,
		Height:       768,
	})
	if err == nil {
		t.Fatal("outside screenshot directory was accepted")
	}
}

func TestLiveWebScreenshot(t *testing.T) {
	if os.Getenv("CINLAN_LIVE_SCREENSHOT_TEST") != "1" {
		t.Skip("set CINLAN_LIVE_SCREENSHOT_TEST=1 to launch the configured browser")
	}
	browser := strings.TrimSpace(os.Getenv("WEB_SCREENSHOT_BROWSER_PATH"))
	if browser == "" {
		t.Fatal("WEB_SCREENSHOT_BROWSER_PATH is empty")
	}
	root := t.TempDir()
	screenshot, err := NewWebScreenshot(WebScreenshotConfig{
		BrowserPath:  browser,
		AllowedHosts: []string{"example.com"},
		OutputDir:    "generated",
		AllowedRoot:  root,
		MaxBytes:     20 << 20,
		Timeout:      45 * time.Second,
		Retention:    time.Hour,
		Width:        1365,
		Height:       768,
		Wait:         3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := screenshot.Tool().Handler(context.Background(), tool.Call{
		Arguments: json.RawMessage(`{"url":"https://example.com/"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Response == nil || len(result.Response.Chain) != 2 {
		t.Fatalf("response = %#v", result.Response)
	}
}

func containsArgumentPrefix(values []string, prefix string) bool {
	for _, value := range values {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}
