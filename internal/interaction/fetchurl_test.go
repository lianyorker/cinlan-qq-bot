package interaction

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestRewriteXURLStatusToFxTwitter(t *testing.T) {
	u, err := url.Parse("https://x.com/thsottiaux/status/1963012903277412618?s=20")
	if err != nil {
		t.Fatal(err)
	}
	got := rewriteXURL(u)
	want := "https://api.fxtwitter.com/thsottiaux/status/1963012903277412618"
	if got.String() != want {
		t.Fatalf("status url → %s, want %s", got, want)
	}
}

func TestRewriteXURLProfileToFxTwitter(t *testing.T) {
	u, err := url.Parse("https://twitter.com/thsottiaux")
	if err != nil {
		t.Fatal(err)
	}
	got := rewriteXURL(u)
	if got.String() != "https://api.fxtwitter.com/thsottiaux" {
		t.Fatalf("profile url → %s, want fxtwitter user endpoint", got)
	}
}

func TestRewriteXURLLeavesOtherHostsAlone(t *testing.T) {
	raw := "https://example.com/x/status/1"
	u, _ := url.Parse(raw)
	if got := rewriteXURL(u); got.String() != raw {
		t.Fatalf("non-x url rewritten to %s", got)
	}
}

func TestPublicFetchURLRejectsInternal(t *testing.T) {
	for _, raw := range []string{
		"http://127.0.0.1:3001/send_group_msg", // NapCat lives here
		"http://localhost:8080/admin",
		"http://192.168.1.1/",
		"http://10.0.0.5/",
		"http://169.254.169.254/latest/meta-data",
		"file:///etc/passwd",
	} {
		if _, err := publicFetchURL(raw); err == nil {
			t.Fatalf("%s should be refused", raw)
		}
	}
}

func TestPublicFetchURLAllowsHTTPS(t *testing.T) {
	u, err := publicFetchURL("https://example.com/")
	if err != nil || u == nil {
		t.Fatalf("public https refused: %v", err)
	}
}

func TestFetchOnceRotatesOnRateLimitStatus(t *testing.T) {
	for _, code := range []int{
		http.StatusTooManyRequests,
		http.StatusForbidden,
		http.StatusServiceUnavailable,
	} {
		srv := httptest.NewServer(http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(code)
			}))
		u, _ := url.Parse(srv.URL)
		_, rotate, err := fetchOnce(context.Background(), srv.Client(), u)
		srv.Close()
		if err == nil ||
			!errors.Is(err, errRetryableStatus) ||
			!rotate {
			t.Fatalf("status %d: rotate=%v err=%v", code, rotate, err)
		}
	}
}

func TestFetchOnceDoesNotRotateOnNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	_, rotate, err := fetchOnce(context.Background(), srv.Client(), u)
	if err == nil || errors.Is(err, errRetryableStatus) || rotate {
		t.Fatalf("404 should fail without rotation: rotate=%v err=%v", rotate, err)
	}
}
