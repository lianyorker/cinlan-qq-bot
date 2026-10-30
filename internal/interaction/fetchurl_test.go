package interaction

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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

func TestRewriteBskyProfileToAuthorFeed(t *testing.T) {
	u, _ := url.Parse("https://bsky.app/profile/thibaut0.bsky.social")
	got := rewriteURL(u)
	if got == nil ||
		got.Hostname() != "public.api.bsky.app" ||
		!strings.Contains(got.Path, "getAuthorFeed") ||
		!strings.Contains(got.RawQuery, "actor=thibaut0.bsky.social") {
		t.Fatalf("bsky profile → %v", got)
	}
}

func TestRewriteBskyPostToThread(t *testing.T) {
	u, _ := url.Parse("https://bsky.app/profile/a.bsky.social/post/3abc")
	got := rewriteURL(u)
	if got == nil ||
		!strings.Contains(got.Path, "getPostThread") ||
		!strings.Contains(got.RawQuery, "at%3A%2F%2Fa.bsky.social") {
		t.Fatalf("bsky post → %v", got)
	}
}

func TestFetchRendersBskyFeed(t *testing.T) {
	body := `{"feed":[{"post":{"author":{"handle":"x.bsky.social","displayName":"X"},` +
		`"record":{"text":"hello world","createdAt":"2026-10-30T01:02:03Z"},` +
		`"likeCount":7,"repostCount":2}}]}`
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	text, err := fetchWithClient(context.Background(), srv.Client(), u)
	if err != nil || !strings.Contains(text, "hello world") ||
		!strings.Contains(text, "@x.bsky.social") {
		t.Fatalf("feed render: %q err=%v", text, err)
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
