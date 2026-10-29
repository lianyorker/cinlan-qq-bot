package interaction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/tool"
)

const (
	fetchURLMaxBody   = 256 << 10 // enough text for an article/timeline
	fetchURLMaxRunes  = 3000
	fetchURLTimeout   = 15 * time.Second
	fetchURLMaxRedirs = 4
)

// FetchURLTool lets the persona open a web page and read its text. It rides
// the same scoped proxy as web_search (WEB_SEARCH_PROXY) — the only egress
// that should leave the mainland network — so sites like x.com become
// readable without rerouting any other traffic. Internal addresses are
// refused outright: the box hosts NapCat and the admin API on loopback, and
// a fetch tool that can hit 127.0.0.1 is an SSRF hole, not a feature.
func FetchURLTool(proxy string) tool.Definition {
	return tool.Definition{
		Name: "fetch_url",
		Description: "打开一个网页链接，读取正文内容（像人点开链接看一眼）。看资讯、读文章、" +
			"看搜索结果里某个页面具体写了什么时用。x.com/twitter 链接直接丢进来就行，" +
			"能读到推文正文；个人主页链接能看到最近的推文列表。" +
			"只知道某人推名就把 https://x.com/<用户名> 拼出来抓。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{
					"type":        "string",
					"description": "要打开的完整网址，http 或 https",
				},
			},
			"required": []string{"url"},
		},
		Permission: tool.PermissionEveryone,
		Timeout:    25 * time.Second,
		Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
			var args struct {
				URL string `json:"url"`
			}
			_ = json.Unmarshal(call.Arguments, &args)
			raw := strings.TrimSpace(args.URL)
			if raw == "" {
				return tool.Result{Error: "url 是空的"}, nil
			}
			text, err := runFetchURL(ctx, proxy, raw)
			if err != nil {
				return tool.Result{Error: "打不开：" + err.Error()}, nil
			}
			return tool.Result{Content: text}, nil
		},
	}
}

// publicFetchURL rejects everything that is not a plain public http(s) URL:
// no loopback, no RFC1918, no link-local, no multicast, no oddball schemes.
func publicFetchURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u == nil {
		return nil, errors.New("网址格式不对")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("只支持 http/https")
	}
	host := u.Hostname()
	if host == "" {
		return nil, errors.New("网址里没有域名")
	}
	ips, err := net.DefaultResolver.LookupIPAddr(context.Background(), host)
	if err != nil || len(ips) == 0 {
		return nil, fmt.Errorf("域名解析失败：%s", host)
	}
	for _, a := range ips {
		ip := a.IP
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
			ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
			return nil, errors.New("内网地址不给抓")
		}
	}
	return u, nil
}

// rewriteXURL maps x.com/twitter.com links onto endpoints that return
// real text — the site itself serves a bare JS shell to scrapers. Status
// pages go through api.fxtwitter.com (JSON carrying the tweet text);
// profiles go through the syndication timeline widget.
func rewriteXURL(u *url.URL) *url.URL {
	host := strings.ToLower(u.Hostname())
	if host != "x.com" && !strings.HasSuffix(host, ".x.com") &&
		host != "twitter.com" && !strings.HasSuffix(host, ".twitter.com") {
		return u
	}
	parts := strings.FieldsFunc(strings.Trim(u.EscapedPath(), "/"),
		func(r rune) bool { return r == '/' })
	if len(parts) >= 3 && strings.EqualFold(parts[1], "status") {
		r, err := url.Parse("https://api.fxtwitter.com/" +
			parts[0] + "/status/" + parts[2])
		if err == nil {
			return r
		}
		return u
	}
	if len(parts) >= 1 && parts[0] != "" &&
		!strings.EqualFold(parts[0], "i") && !strings.EqualFold(parts[0], "hashtag") {
		r, err := url.Parse("https://syndication.twitter.com/srv/timeline-profile/screen-name/" +
			parts[0] + "?dnt=true&limit=10")
		if err == nil {
			return r
		}
	}
	return u
}

func runFetchURL(ctx context.Context, proxy, raw string) (string, error) {
	u, err := publicFetchURL(raw)
	if err != nil {
		return "", err
	}
	u = rewriteXURL(u)
	if _, err := publicFetchURL(u.String()); err != nil {
		return "", err
	}
	client, err := webSearchClient(proxy)
	if err != nil {
		return "", err
	}
	// Same scoped-proxy transport, slightly longer budget for full pages.
	client.Timeout = fetchURLTimeout
	// Every redirect target gets re-validated — a public URL bouncing to
	// 127.0.0.1 is still an SSRF attempt.
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= fetchURLMaxRedirs {
			return errors.New("redirect limit")
		}
		if _, err := publicFetchURL(req.URL.String()); err != nil {
			return err
		}
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", webSearchUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain,application/json,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.7")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	ctype := strings.ToLower(resp.Header.Get("Content-Type"))
	switch {
	case strings.Contains(ctype, "text/html"),
		strings.Contains(ctype, "text/plain"),
		strings.Contains(ctype, "application/json"),
		strings.Contains(ctype, "application/xml"),
		strings.Contains(ctype, "text/xml"),
		ctype == "":
	default:
		return "", fmt.Errorf("二进制内容读不了（%s)", ctype)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, fetchURLMaxBody))
	if err != nil {
		return "", err
	}
	text := string(body)
	switch {
	case strings.Contains(ctype, "application/json"):
		// Known JSON shapes get distilled to text; anything else stays
		// as the raw body.
		var fx struct {
			Tweet *struct {
				Text   string `json:"text"`
				Author struct {
					Name       string `json:"name"`
					ScreenName string `json:"screen_name"`
				} `json:"author"`
			} `json:"tweet"`
		}
		var oembed struct {
			HTML       string `json:"html"`
			AuthorName string `json:"author_name"`
		}
		switch {
		case json.Unmarshal(body, &fx) == nil && fx.Tweet != nil && fx.Tweet.Text != "":
			who := fx.Tweet.Author.ScreenName
			if who == "" {
				who = fx.Tweet.Author.Name
			}
			if who != "" {
				text = "@" + who + "：\n" + fx.Tweet.Text
			} else {
				text = fx.Tweet.Text
			}
		case json.Unmarshal(body, &oembed) == nil && oembed.HTML != "":
			text = htmlToReadableText(oembed.HTML)
			if oembed.AuthorName != "" {
				text = "@" + oembed.AuthorName + "：\n" + text
			}
		}
	case strings.Contains(ctype, "text/html"), strings.Contains(ctype, "xhtml"):
		text = htmlToReadableText(text)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", errors.New("页面没有可读文本（可能是纯 JS 应用或登录墙）")
	}
	runes := []rune(text)
	if len(runes) > fetchURLMaxRunes {
		text = string(runes[:fetchURLMaxRunes]) + "\n…（截断）"
	}
	return text, nil
}

var (
	scriptTagRe = regexp.MustCompile(`(?s)<(script|style|noscript|svg|iframe)[^>]*>.*?</(script|style|noscript|svg|iframe)>`)
	blockTagRe  = regexp.MustCompile(`(?i)</?(p|div|br|li|tr|h[1-6]|article|section|header|footer|blockquote|table)[^>]*>`)
	spaceRunRe  = regexp.MustCompile(`[ \t\f\v]+`)
)

// htmlToReadableText strips markup down to line-broken plain text.
// htmlTagRe comes from websearch.go — same package, one definition.
func htmlToReadableText(page string) string {
	page = scriptTagRe.ReplaceAllString(page, " ")
	page = blockTagRe.ReplaceAllString(page, "\n")
	page = htmlTagRe.ReplaceAllString(page, "")
	page = html.UnescapeString(page)
	lines := make([]string, 0, 64)
	for _, line := range strings.Split(page, "\n") {
		line = spaceRunRe.ReplaceAllString(strings.TrimSpace(line), " ")
		if line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}
