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
//   - unset → DuckDuckGo lite HTML scrape (no key), instant-answer
//     JSON as a final fallback when the scrape comes back empty.
const (
	webSearchMaxResults  = 5
	webSearchMaxRunes    = 1600
	webSearchHTTPTimeout = 15 * time.Second
	webSearchUserAgent   = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"
)

type webSearchResult struct {
	Title   string
	Snippet string
	URL     string
}

var (
	ddgLinkRe    = regexp.MustCompile(`<a[^>]+class="result-link"[^>]+href="([^"]+)"[^>]*>(.*?)</a>`)
	ddgSnippetRe = regexp.MustCompile(`<td[^>]+class="result-snippet"[^>]*>(.*?)</td>`)
	htmlTagRe    = regexp.MustCompile(`<[^>]+>`)
)

// WebSearchTool gives the persona live web lookup for questions its
// training data misses — release names, group jargon, fresh prices.
// Group jargon note for the model: never quote raw URLs back to the
// chat when allow_links strips them anyway.
func WebSearchTool(apiURL, apiKey string) tool.Definition {
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
			results, err := runWebSearch(ctx, apiURL, apiKey, query)
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

func runWebSearch(
	ctx context.Context,
	apiURL, apiKey, query string,
) ([]webSearchResult, error) {
	if strings.TrimSpace(apiURL) != "" {
		return searchJSONEndpoint(ctx, apiURL, apiKey, query)
	}
	results, err := searchDuckDuckGoLite(ctx, query)
	if err == nil && len(results) > 0 {
		return results, nil
	}
	fallback, fbErr := searchDuckDuckGoInstant(ctx, query)
	if fbErr != nil {
		if err != nil {
			return nil, err
		}
		return nil, fbErr
	}
	return fallback, nil
}

// searchJSONEndpoint queries a SearXNG-compatible endpoint: it appends
// q=<query>&format=json to the configured URL and reads results[].
func searchJSONEndpoint(
	ctx context.Context,
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
	client := &http.Client{Timeout: webSearchHTTPTimeout}
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
	query string,
) ([]webSearchResult, error) {
	endpoint := "https://lite.duckduckgo.com/lite/?q=" + url.QueryEscape(query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", webSearchUserAgent)
	client := &http.Client{Timeout: webSearchHTTPTimeout}
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
	query string,
) ([]webSearchResult, error) {
	endpoint := "https://api.duckduckgo.com/?format=json&no_redirect=1&no_html=1&q=" +
		url.QueryEscape(query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", webSearchUserAgent)
	client := &http.Client{Timeout: webSearchHTTPTimeout}
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
