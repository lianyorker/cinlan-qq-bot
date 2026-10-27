package interaction

import (
	"context"
	"encoding/json"
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
//   - WEB_SEARCH_PROXY optionally routes all search traffic through a
//     http/https/socks5 proxy; unset → HTTP(S)_PROXY env is honoured.
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
// chat when allow_links strips them anyway.
func WebSearchTool(apiURL, apiKey, proxy string) tool.Definition {
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
			results, err := runWebSearch(ctx, apiURL, apiKey, proxy, query)
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

// webSearchClient honours WEB_SEARCH_PROXY when set, else the standard
// HTTP_PROXY/HTTPS_PROXY environment variables.
func webSearchClient(proxy string) (*http.Client, error) {
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment}
	if p := strings.TrimSpace(proxy); p != "" {
		proxyURL, err := url.Parse(p)
		if err != nil {
			return nil, fmt.Errorf("WEB_SEARCH_PROXY: %w", err)
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	}
	return &http.Client{Timeout: webSearchHTTPTimeout, Transport: transport}, nil
}

func runWebSearch(
	ctx context.Context,
	apiURL, apiKey, proxy, query string,
) ([]webSearchResult, error) {
	client, err := webSearchClient(proxy)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(apiURL) != "" {
		return searchJSONEndpoint(ctx, client, apiURL, apiKey, query)
	}
	var firstErr error
	for _, backend := range []func(context.Context, *http.Client, string) ([]webSearchResult, error){
		searchBing,
		searchDuckDuckGoLite,
		searchDuckDuckGoInstant,
	} {
		results, err := backend(ctx, client, query)
		if err == nil && len(results) > 0 {
			return results, nil
		}
		if firstErr == nil && err != nil {
			firstErr = err
		}
		if ctx.Err() != nil {
			break
		}
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return nil, nil
}

// searchBing scrapes bing.com — reachable from mainland-CN servers,
// where the DuckDuckGo domains are blocked outright.
func searchBing(
	ctx context.Context,
	client *http.Client,
	query string,
) ([]webSearchResult, error) {
	endpoint := "https://www.bing.com/search?q=" + url.QueryEscape(query) + "&mkt=zh-CN"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", webSearchUserAgent)
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
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
	return parseBingResults(string(body)), nil
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
