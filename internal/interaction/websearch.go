package interaction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/tool"
)

// Web-search backends, cheapest first:
//   - WEB_SEARCH_API_URL configured → SearXNG-style JSON endpoint
//     (GET <url>?q=<query>&format=json, results[] = {title, content, url}).
//     Works with a self-hosted searxng instance or any endpoint that
//     speaks the same shape. WEB_SEARCH_API_KEY adds a Bearer header.
//   - unset → Bing web search scrape (reachable from CN servers),
//     then DuckDuckGo lite HTML scrape, then DDG instant-answer JSON.
//   - WEB_SEARCH_PROXY / WEB_SEARCH_PROXIES feed a ProxyPool: dead
//     proxies are skipped and revived by probes; all dead → direct.
const (
	webSearchMaxResults  = 5
	webSearchMaxRunes    = 1600
	webSearchHTTPTimeout = 8 * time.Second
	webSearchUserAgent   = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"
)

type webSearchResult struct {
	Title   string
	Snippet string
	URL     string
}

// errBotChallenge marks a 200-OK page that is actually an anti-bot
// gate (DDG anomaly modal, bing verify). Datacenter IPs collect these
// randomly per exit IP, so the right response is rotating the proxy,
// not reporting empty results.
var errBotChallenge = errors.New("search backend served a bot challenge")

// looksLikeBotChallenge sniffs the body for anti-bot page signatures.
// Conservative on purpose: only unambiguous markers count, because a
// false positive rotates away a healthy proxy for nothing.
func looksLikeBotChallenge(page string) bool {
	switch {
	case strings.Contains(page, "anomaly-modal"), // duckduckgo
		strings.Contains(page, "unusual traffic"),
		strings.Contains(page, "百度安全验证"),
		strings.Contains(page, "请输入验证码"):
		return true
	}
	return false
}

var (
	ddgLinkRe     = regexp.MustCompile(`<a[^>]+class="result-link"[^>]+href="([^"]+)"[^>]*>(.*?)</a>`)
	ddgSnippetRe  = regexp.MustCompile(`<td[^>]+class="result-snippet"[^>]*>(.*?)</td>`)
	bingItemRe    = regexp.MustCompile(`(?s)<li class="[^"]*b_algo[^"]*"[^>]*>(.*?)</li>`)
	bingTitleRe   = regexp.MustCompile(`(?s)<h2[^>]*>.*?<a[^>]+href="([^"]+)"[^>]*>(.*?)</a>`)
	bingSnippetRe = regexp.MustCompile(`(?s)<p[^>]*>(.*?)</p>`)
	htmlTagRe     = regexp.MustCompile(`<[^>]+>`)
)

// WebSearchTool gives the persona live web lookup for questions its
// training data misses — release names, group jargon, fresh prices.
// Group jargon note for the model: never quote raw URLs back to the
// chat when allow_links strips them anyway. pool may be nil → direct.
func WebSearchTool(apiURL, apiKey string, pool *ProxyPool) tool.Definition {
	return tool.Definition{
		Name: "web_search",
		Description: "联网搜索一下（像人打开搜索引擎查一下）。问的是你没听过的新东西、黑话、" +
			"新模型名、实时价格/活动时用，别拿来查你本来就知道的常识。" +
			"参数 query 填要搜的关键词，越具体越好。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "要搜的关键词",
				},
			},
			"required": []string{"query"},
		},
		Permission: tool.PermissionEveryone,
		Timeout:    20 * time.Second,
		Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
			var args struct {
				Query string `json:"query"`
			}
			_ = json.Unmarshal(call.Arguments, &args)
			query := strings.TrimSpace(args.Query)
			if query == "" {
				return tool.Result{Error: "query 是空的"}, nil
			}
			results, err := runWebSearch(ctx, apiURL, apiKey, pool, query)
			if err != nil {
				return tool.Result{Error: "没搜到：" + err.Error()}, nil
			}
			if len(results) == 0 {
				return tool.Result{Content: "搜了一圈没找到相关结果"}, nil
			}
			return tool.Result{Content: formatWebSearchResults(results)}, nil
		},
	}
}

// runWebSearch walks the backend chain once per proxy pick. A backend
// chain that fails on every transport-level attempt means the picked
// proxy is dead — mark it, rotate, retry. Status/parse failures are the
// endpoint's fault, not the proxy's, and stop the rotation.
func runWebSearch(
	ctx context.Context,
	apiURL, apiKey string,
	pool *ProxyPool,
	query string,
) ([]webSearchResult, error) {
	attempts := 1
	if pool != nil {
		attempts += pool.size()
	}
	var firstErr error
	for attempt := 0; attempt < attempts; attempt++ {
		var client *http.Client
		var entry *proxyEntry
		if pool != nil {
			client, entry = pool.client(webSearchHTTPTimeout)
		} else {
			client = &http.Client{
				Timeout:   webSearchHTTPTimeout,
				Transport: &http.Transport{Proxy: http.ProxyFromEnvironment},
			}
		}
		results, rotate, err := searchBackendsOnce(ctx, client, apiURL, apiKey, query)
		if err == nil && len(results) > 0 {
			return results, nil
		}
		if rotate && entry != nil {
			// Dead proxy or captcha-flagged exit IP — either way this
			// egress is useless for search until the probe clears it.
			pool.markDead(entry)
			continue
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
		if err == nil {
			// Empty results, no error — not a proxy problem.
			return nil, nil
		}
		if !rotate || entry == nil {
			// Endpoint-level error, or already on the direct path.
			return nil, firstErr
		}
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return nil, errors.New("搜索全挂了（代理不通）")
}

// searchBackendsOnce runs one full backend pass with one client. The
// second return says the whole pass failed rotation-worthy — every
// failure was a transport error or a bot challenge — meaning this
// egress IP is useless for search right now and the caller should
// pick another proxy.
func searchBackendsOnce(
	ctx context.Context,
	client *http.Client,
	apiURL, apiKey, query string,
) ([]webSearchResult, bool, error) {
	if strings.TrimSpace(apiURL) != "" {
		results, err := searchJSONEndpoint(ctx, client, apiURL, apiKey, query)
		return results, isTransportErr(err) || errors.Is(err, errBotChallenge), err
	}
	var firstErr error
	allRotateWorthy := true
	sawErr := false
	for _, backend := range []func(context.Context, *http.Client, string) ([]webSearchResult, error){
		searchBingAuto,
		searchDuckDuckGoLite,
		searchDuckDuckGoInstant,
	} {
		results, err := backend(ctx, client, query)
		if err == nil && len(results) > 0 {
			return results, false, nil
		}
		if err != nil {
			sawErr = true
			if !isTransportErr(err) && !errors.Is(err, errBotChallenge) {
				allRotateWorthy = false
			}
			if firstErr == nil {
				firstErr = err
			}
		}
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
	}
	if !sawErr {
		return nil, false, nil
	}
	return nil, allRotateWorthy, firstErr
}

// searchBingAuto wraps the zh bing call with a relevance gate: zh-CN
// bing mangles latin-script queries (names, handles) into unrelated zh
// matches. When nothing in the results echoes a query token, retry on
// the en-US market and prefer those.
func searchBingAuto(
	ctx context.Context,
	client *http.Client,
	query string,
) ([]webSearchResult, error) {
	results, err := searchBingMarket(ctx, client, query, "zh-CN", false)
	if err != nil {
		return nil, err
	}
	if len(results) > 0 && resultsLookRelevant(results, query) {
		return results, nil
	}
	usResults, usErr := searchBingMarket(ctx, client, query, "en-US", true)
	if usErr == nil && len(usResults) > 0 {
		return usResults, nil
	}
	return results, err
}

// resultsLookRelevant does a cheap echo check: at least one meaningful
// query token should show up in some title or snippet. zh bing zero-
// overlap results are silently swapped queries, not "found results".
func resultsLookRelevant(results []webSearchResult, query string) bool {
	tokens := queryTokens(query)
	if len(tokens) == 0 {
		return true
	}
	var hay strings.Builder
	for _, r := range results {
		hay.WriteString(strings.ToLower(r.Title))
		hay.WriteString(strings.ToLower(r.Snippet))
	}
	text := hay.String()
	for _, tok := range tokens {
		if strings.Contains(text, tok) {
			return true
		}
	}
	return false
}

// queryTokens splits the query into matchable tokens: whitespace
// groups plus latin/digit runs of len>=3 (a token like "tibo最新资讯"
// contributes both itself and "tibo").
var latinTokenRe = regexp.MustCompile(`[a-zA-Z0-9]{3,}`)

func queryTokens(query string) []string {
	seen := map[string]bool{}
	var tokens []string
	for _, field := range strings.Fields(strings.ToLower(query)) {
		if len(field) >= 2 && !seen[field] {
			seen[field] = true
			tokens = append(tokens, field)
		}
		for _, latin := range latinTokenRe.FindAllString(field, -1) {
			if !seen[latin] {
				seen[latin] = true
				tokens = append(tokens, latin)
			}
		}
	}
	return tokens
}

// searchBingMarket scrapes bing.com for the given market. ensearch=1
// keeps CN-geo IPs on .com instead of getting bounced to cn.bing.com.
func searchBingMarket(
	ctx context.Context,
	client *http.Client,
	query, mkt string,
	ensearch bool,
) ([]webSearchResult, error) {
	endpoint := "https://www.bing.com/search?q=" + url.QueryEscape(query) + "&mkt=" + mkt
	if ensearch {
		endpoint += "&ensearch=1"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", webSearchUserAgent)
	acceptLang := "zh-CN,zh;q=0.9"
	if mkt == "en-US" {
		acceptLang = "en-US,en;q=0.9"
	}
	req.Header.Set("Accept-Language", acceptLang)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bing status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	page := string(body)
	if looksLikeBotChallenge(page) {
		return nil, errBotChallenge
	}
	return parseBingResults(page), nil
}

func searchBing(
	ctx context.Context,
	client *http.Client,
	query string,
) ([]webSearchResult, error) {
	return searchBingAuto(ctx, client, query)
}

func parseBingResults(page string) []webSearchResult {
	results := make([]webSearchResult, 0, webSearchMaxResults)
	for _, item := range bingItemRe.FindAllStringSubmatch(page, webSearchMaxResults*2) {
		if len(results) >= webSearchMaxResults {
			break
		}
		block := item[1]
		title := bingTitleRe.FindStringSubmatch(block)
		if title == nil {
			continue
		}
		href := html.UnescapeString(title[1])
		if !strings.HasPrefix(href, "http") {
			continue
		}
		snippet := ""
		if p := bingSnippetRe.FindStringSubmatch(block); p != nil {
			snippet = cleanHTMLText(p[1])
		}
		results = append(results, webSearchResult{
			Title:   cleanHTMLText(title[2]),
			Snippet: snippet,
			URL:     href,
		})
	}
	return results
}

// searchJSONEndpoint queries a SearXNG-compatible endpoint: it appends
// q=<query>&format=json to the configured URL and reads results[].
func searchJSONEndpoint(
	ctx context.Context,
	client *http.Client,
	apiURL, apiKey, query string,
) ([]webSearchResult, error) {
	sep := "?"
	if strings.Contains(apiURL, "?") {
		sep = "&"
	}
	endpoint := apiURL + sep + "q=" + url.QueryEscape(query) + "&format=json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", webSearchUserAgent)
	req.Header.Set("Accept", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("search endpoint status %d", resp.StatusCode)
	}
	var payload struct {
		Results []struct {
			Title   string `json:"title"`
			Content string `json:"content"`
			URL     string `json:"url"`
		} `json:"results"`
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	results := make([]webSearchResult, 0, len(payload.Results))
	for _, r := range payload.Results {
		results = append(results, webSearchResult{
			Title:   strings.TrimSpace(r.Title),
			Snippet: strings.TrimSpace(r.Content),
			URL:     strings.TrimSpace(r.URL),
		})
	}
	return results, nil
}

// searchDuckDuckGoLite scrapes lite.duckduckgo.com — the no-JS table
// layout whose result links still carry the target URL in uddg=.
func searchDuckDuckGoLite(
	ctx context.Context,
	client *http.Client,
	query string,
) ([]webSearchResult, error) {
	endpoint := "https://lite.duckduckgo.com/lite/?q=" + url.QueryEscape(query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", webSearchUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("duckduckgo status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	page := string(body)
	if looksLikeBotChallenge(page) {
		return nil, errBotChallenge
	}
	links := ddgLinkRe.FindAllStringSubmatch(page, webSearchMaxResults*2)
	snippets := ddgSnippetRe.FindAllStringSubmatch(page, webSearchMaxResults*2)
	results := make([]webSearchResult, 0, webSearchMaxResults)
	for i, link := range links {
		if len(results) >= webSearchMaxResults {
			break
		}
		href := html.UnescapeString(link[1])
		if u, uErr := url.Parse(href); uErr == nil {
			if real := u.Query().Get("uddg"); real != "" {
				href = real
			}
		}
		snippet := ""
		if i < len(snippets) {
			snippet = cleanHTMLText(snippets[i][1])
		}
		results = append(results, webSearchResult{
			Title:   cleanHTMLText(link[2]),
			Snippet: snippet,
			URL:     href,
		})
	}
	return results, nil
}

// searchDuckDuckGoInstant hits the zero-key instant-answer API — it only
// knows encyclopedic facts, so it is the last resort, not the first.
func searchDuckDuckGoInstant(
	ctx context.Context,
	client *http.Client,
	query string,
) ([]webSearchResult, error) {
	endpoint := "https://api.duckduckgo.com/?format=json&no_redirect=1&no_html=1&q=" +
		url.QueryEscape(query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", webSearchUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var payload struct {
		AbstractText  string `json:"AbstractText"`
		AbstractURL   string `json:"AbstractURL"`
		RelatedTopics []struct {
			Text     string `json:"Text"`
			FirstURL string `json:"FirstURL"`
		} `json:"RelatedTopics"`
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	results := []webSearchResult{}
	if strings.TrimSpace(payload.AbstractText) != "" {
		results = append(results, webSearchResult{
			Title:   query,
			Snippet: strings.TrimSpace(payload.AbstractText),
			URL:     payload.AbstractURL,
		})
	}
	for _, topic := range payload.RelatedTopics {
		if len(results) >= webSearchMaxResults {
			break
		}
		if strings.TrimSpace(topic.Text) == "" {
			continue
		}
		results = append(results, webSearchResult{
			Title:   "",
			Snippet: strings.TrimSpace(topic.Text),
			URL:     topic.FirstURL,
		})
	}
	return results, nil
}

func cleanHTMLText(raw string) string {
	return strings.TrimSpace(html.UnescapeString(htmlTagRe.ReplaceAllString(raw, "")))
}

func formatWebSearchResults(results []webSearchResult) string {
	var b strings.Builder
	for i, r := range results {
		if i >= webSearchMaxResults {
			break
		}
		line := fmt.Sprintf("%d. %s", i+1, r.Title)
		if r.Snippet != "" {
			line += " — " + r.Snippet
		}
		if r.URL != "" {
			line += " (" + r.URL + ")"
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(line)
		if len([]rune(b.String())) > webSearchMaxRunes {
			break
		}
	}
	out := b.String()
	if runes := []rune(out); len(runes) > webSearchMaxRunes {
		out = string(runes[:webSearchMaxRunes]) + "…"
	}
	return out
}
