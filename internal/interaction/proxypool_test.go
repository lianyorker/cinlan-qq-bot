package interaction

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// deadProxySpec points at a closed local port — dial fails instantly.
func deadProxySpec(t *testing.T) string {
	s := httptest.NewServer(http.NotFoundHandler())
	addr := s.Listener.Addr().String()
	s.Close()
	return "http://" + addr
}

func TestProxyPoolPicksAliveAndFallsBackDirect(t *testing.T) {
	dead := deadProxySpec(t)
	pool := NewProxyPool([]string{dead}, nil)
	defer pool.Stop()

	entry := pool.pick()
	if entry == nil || entry.raw != dead {
		t.Fatalf("pick should return the only entry, got %+v", entry)
	}
	pool.markDead(entry)
	if got := pool.pick(); got != nil {
		t.Fatalf("all-dead pool should pick nil, got %+v", got)
	}
	client, e := pool.client(time.Second)
	if e != nil || client.Transport.(*http.Transport).Proxy == nil {
		t.Fatal("all-dead pool should yield direct client")
	}
	// Revive manually → pick returns it again.
	entry.alive.Store(true)
	if got := pool.pick(); got != entry {
		t.Fatal("revived entry should be picked")
	}
}

func TestProxyPoolRoundRobin(t *testing.T) {
	pool := NewProxyPool([]string{
		"http://10.0.0.1:1", "http://10.0.0.2:1",
	}, nil)
	defer pool.Stop()
	first, second := pool.pick(), pool.pick()
	if first == nil || second == nil || first == second {
		t.Fatal("two live entries should rotate")
	}
	pool.markDead(first)
	if pool.pick() != second {
		t.Fatal("dead entry must be skipped")
	}
}

func TestProxyPoolDedupesAndSkipsBad(t *testing.T) {
	pool := NewProxyPool([]string{
		"http://10.0.0.1:1", "http://10.0.0.1:1", "", "nope",
	}, nil)
	defer pool.Stop()
	if pool.size() != 1 {
		t.Fatalf("want 1 valid entry, got %d", pool.size())
	}
	if NewProxyPool(nil, nil).size() != 0 {
		t.Fatal("empty specs = empty pool")
	}
}

// A live proxy server actually forwards the probe and the request.
func TestProxyPoolProbeAndServe(t *testing.T) {
	old := proxyPoolProbeURL
	proxyPoolProbeURL = "http://canary.test/" // http → no CONNECT needed
	defer func() { proxyPoolProbeURL = old }()

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Plain http requests arrive at the proxy as full URLs — any
		// response proves the tunnel works, same as a real proxy.
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	// Simulate: entry "dead" → probe via the pool's own probe() marks it
	// alive if the proxy answers. httptest server acts as the proxy.
	u, _ := url.Parse(target.URL)
	e := &proxyEntry{raw: target.URL, url: u}
	pool := NewProxyPool([]string{deadProxySpec(t)}, nil)
	defer pool.Stop()
	pool.entries = append(pool.entries, e)
	e.alive.Store(false)

	pool.probe(e)
	if !e.alive.Load() {
		t.Fatal("proxy that answers must be marked alive")
	}
}

func TestRunWebSearchRotatesPastDeadProxy(t *testing.T) {
	pool := NewProxyPool([]string{deadProxySpec(t)}, nil)
	defer pool.Stop()
	// Every backend will fail transport-level through the dead proxy →
	// pool marks it dead → next attempt goes direct. Direct egress in
	// the test env may or may not reach bing; we only assert rotation.
	_, err := runWebSearch(context.Background(), "", "", pool, "test")
	for _, e := range pool.entries {
		if e.alive.Load() {
			t.Fatal("dead proxy must be marked dead after call")
		}
	}
	if err == nil {
		t.Skip("direct egress worked — rotation path still verified by dead marks")
	}
}
