package interaction

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ProxyPool routes scoped-egress tools (web_search, fetch_url) through a
// list of candidate proxies with health checks and rotation. A proxy
// that fails at transport level (dial/proxyconnect, like today's dead
// 127.0.0.1:7890) is marked dead and skipped; a background probe revives
// it once it answers again. Every proxy dead → fall back to direct
// traffic (ProxyFromEnvironment) instead of failing the tool call.
//
// Pool lifetime = process lifetime: the probe goroutine exits via Stop;
// registerLocalExtensions never calls it deliberately so the pool rides
// the daemon lifetime.
const (
	proxyPoolProbeTimeout = 6 * time.Second
	proxyPoolInterval     = 2 * time.Minute
)

// proxyPoolProbeURL is a var so tests can swap the https canary (which
// needs real CONNECT tunneling) for an http one a fake proxy can serve.
var proxyPoolProbeURL = "https://www.bing.com/"

type proxyEntry struct {
	raw   string
	url   *url.URL
	alive atomic.Bool
}

// ProxyPool is safe for concurrent use — tool calls and the probe
// goroutine share it.
type ProxyPool struct {
	entries []*proxyEntry
	next    atomic.Uint64
	logger  *slog.Logger
	stop    chan struct{}
	done    chan struct{}
}

// NewProxyPool builds a pool from comma-separated proxy URLs (http/https
// /socks5). Invalid specs are logged and skipped; an empty list leaves a
// zero-size pool whose every call goes direct — same as before. The
// first probe runs in background so a dead proxy never taxes the first
// tool call beyond its own timeout.
func NewProxyPool(specs []string, logger *slog.Logger) *ProxyPool {
	p := &ProxyPool{
		logger: logger,
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	seen := map[string]bool{}
	for _, spec := range specs {
		raw := strings.TrimSpace(spec)
		if raw == "" || seen[raw] {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			p.log().Warn("web egress proxy spec skipped",
				"proxy", raw, "error", err)
			continue
		}
		seen[raw] = true
		entry := &proxyEntry{raw: raw, url: u}
		entry.alive.Store(true) // optimistic until proven dead
		p.entries = append(p.entries, entry)
	}
	if len(p.entries) == 0 {
		return p
	}
	go p.probeAll() // converge before the first real call
	go p.probeLoop()
	return p
}

func (p *ProxyPool) log() *slog.Logger {
	if p.logger == nil {
		return slog.Default()
	}
	return p.logger
}

// Stop terminates the background probe; safe to call more than once.
func (p *ProxyPool) Stop() {
	select {
	case <-p.stop:
		return
	default:
	}
	close(p.stop)
	<-p.done
}

func (p *ProxyPool) size() int { return len(p.entries) }

// client returns an http.Client riding the next live proxy in rotation,
// plus the entry it picked — callers mark it dead on transport failure.
// Every entry dead → client is nil-proxied (direct) and entry is nil.
func (p *ProxyPool) client(timeout time.Duration) (*http.Client, *proxyEntry) {
	if entry := p.pick(); entry != nil {
		return &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				Proxy: http.ProxyURL(entry.url),
			},
		}, entry
	}
	// No live proxy — keep the old default behaviour (env proxies still
	// honoured via ProxyFromEnvironment).
	return &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{Proxy: http.ProxyFromEnvironment},
	}, nil
}

// pick rotates through entries currently marked alive.
func (p *ProxyPool) pick() *proxyEntry {
	n := len(p.entries)
	if n == 0 {
		return nil
	}
	for i := 0; i < n; i++ {
		idx := int(p.next.Add(1)-1) % n
		if p.entries[idx].alive.Load() {
			return p.entries[idx]
		}
	}
	return nil
}

// markDead flips an entry to dead on transport-level failure — dial,
// proxyconnect, read timeouts all surface as *url.Error from client.Do.
// The probe goroutine revives it when it answers again, so a wrongly
// dead-marked proxy costs at most one probe interval.
func (p *ProxyPool) markDead(e *proxyEntry) {
	if e == nil || !e.alive.CompareAndSwap(true, false) {
		return
	}
	p.log().Warn("web egress proxy marked dead", "proxy", e.raw)
}

// isTransportErr classifies client.Do failures: *url.Error wraps every
// transport problem (dial, connect, TLS, timeout). Status/parse errors
// returned by backends are plain errors and never reach here.
func isTransportErr(err error) bool {
	var uerr *url.Error
	return errors.As(err, &uerr)
}

// probeLoop re-checks entries on a fixed interval — dead ones earn back
// their slot; live ones get demoted the moment they stop answering.
func (p *ProxyPool) probeLoop() {
	defer close(p.done)
	ticker := time.NewTicker(proxyPoolInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			p.probeAll()
		case <-p.stop:
			return
		}
	}
}

func (p *ProxyPool) probeAll() {
	var wg sync.WaitGroup
	for _, e := range p.entries {
		wg.Add(1)
		go func(e *proxyEntry) {
			defer wg.Done()
			p.probe(e)
		}(e)
	}
	wg.Wait()
}

// probe issues a cheap GET through the proxy. Any HTTP response — even
// a 4xx — proves the tunnel works; a transport failure marks it dead.
func (p *ProxyPool) probe(e *proxyEntry) {
	ctx, cancel := context.WithTimeout(context.Background(), proxyPoolProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, proxyPoolProbeURL, nil)
	if err != nil {
		return
	}
	req.Header.Set("User-Agent", webSearchUserAgent)
	client := &http.Client{
		Timeout:   proxyPoolProbeTimeout,
		Transport: &http.Transport{Proxy: http.ProxyURL(e.url)},
	}
	resp, err := client.Do(req)
	if err == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 256))
		resp.Body.Close()
		if e.alive.CompareAndSwap(false, true) {
			p.log().Info("web egress proxy revived", "proxy", e.raw)
		}
		return
	}
	if e.alive.CompareAndSwap(true, false) {
		p.log().Warn("web egress proxy probe failed", "proxy", e.raw, "error", err)
	}
}
