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
// the same scoped ProxyPool as web_search — the only egress that should
// leave the mainland network — so sites like x.com become readable without
// rerouting any other traffic. Internal addresses are refused outright:
// the box hosts NapCat and the admin API on loopback, and a fetch tool
// that can hit 127.0.0.1 is an SSRF hole, not a feature. pool may be nil.
func FetchURLTool(pool *ProxyPool) tool.Definition {
	return tool.Definition{
		Name: "fetch_url",
		Description: "打开一个网页链接，读取正文内容（像人点开链接看一眼）。看资讯、读文章、" +
			"看搜索结果里某个页面具体写了什么时用。x.com/twitter 链接直接丢进来就行：" +
			"单条推文链接能读到正文；个人主页链接（x.com/用户名）能拿到账号档案" +
			"（简介/粉丝数/注册时间等，没有推文列表）。bsky.app 个人主页链接能拿到最近帖子正文" +
			"（x 档案里常挂着对方的 bsky 主页，想看最近动态就去抓它）。只知道某人推名" +
			"就把 https://x.com/<用户名> 拼出来抓。",
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
			text, err := runFetchURL(ctx, pool, raw)
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

// rewriteURL maps social links onto public JSON endpoints that return
// real text: x.com goes through api.fxtwitter.com, Bluesky goes through
// the anonymous public.api.bsky.app xrpc endpoints.
func rewriteURL(u *url.URL) *url.URL {
	if r := rewriteBskyURL(u); r != nil {
		return r
	}
	return rewriteXURL(u)
}

// rewriteBskyURL maps bsky.app links onto public.api.bsky.app. The
// public XRPC API needs no auth for public feeds — profiles become
// getAuthorFeed (real post text, the timeline X lost), posts become
// getPostThread (at:// accepts a handle as authority, no DID lookup).
func rewriteBskyURL(u *url.URL) *url.URL {
	host := strings.ToLower(u.Hostname())
	if host != "bsky.app" && !strings.HasSuffix(host, ".bsky.app") {
		return nil
	}
	parts := strings.FieldsFunc(strings.Trim(u.EscapedPath(), "/"),
		func(r rune) bool { return r == '/' })
	if len(parts) >= 4 &&
		strings.EqualFold(parts[0], "profile") &&
		strings.EqualFold(parts[2], "post") {
		at := "at://" + parts[1] + "/app.bsky.feed.post/" + parts[3]
		r, err := url.Parse("https://public.api.bsky.app/xrpc/" +
			"app.bsky.feed.getPostThread?depth=0&uri=" +
			url.QueryEscape(at))
		if err == nil {
			return r
		}
		return nil
	}
	if len(parts) >= 2 && strings.EqualFold(parts[0], "profile") &&
		parts[1] != "" {
		r, err := url.Parse("https://public.api.bsky.app/xrpc/" +
			"app.bsky.feed.getAuthorFeed?limit=8&actor=" +
			url.QueryEscape(parts[1]))
		if err == nil {
			return r
		}
	}
	return nil
}

// rewriteXURL maps x.com/twitter.com links onto api.fxtwitter.com — the
// site itself serves a bare JS shell to scrapers, while fxtwitter
// returns clean JSON for both tweets and profiles. The old syndication
// profile widget only serves a JS loader page now, so it is gone.
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
		r, err := url.Parse("https://api.fxtwitter.com/" + parts[0])
		if err == nil {
			return r
		}
	}
	return u
}

func runFetchURL(ctx context.Context, pool *ProxyPool, raw string) (string, error) {
	u, err := publicFetchURL(raw)
	if err != nil {
		return "", err
	}
	u = rewriteURL(u)
	if _, err := publicFetchURL(u.String()); err != nil {
		return "", err
	}
	attempts := 1
	if pool != nil {
		attempts += pool.size()
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		var client *http.Client
		var entry *proxyEntry
		if pool != nil {
			client, entry = pool.client(fetchURLTimeout)
		} else {
			client = &http.Client{
				Timeout:   fetchURLTimeout,
				Transport: &http.Transport{Proxy: http.ProxyFromEnvironment},
			}
		}
		text, rotate, err := fetchOnce(ctx, client, u)
		if err == nil {
			return text, nil
		}
		lastErr = err
		if rotate && entry != nil {
			pool.markDead(entry)
			continue
		}
		return "", err
	}
	return "", lastErr
}

// errRetryableStatus wraps HTTP statuses that blame the exit IP, not
// the request — rate limits and CDN bans come and go per egress, so
// they deserve the same proxy rotation as transport failures.
var errRetryableStatus = errors.New("egress IP got a retryable HTTP status")

func retryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests,
		http.StatusForbidden,
		http.StatusServiceUnavailable:
		return true
	}
	return false
}

// fetchOnce performs the single-request fetch and reports whether the
// failure is egress-specific — transport error or a rate-limit/ban
// status — meaning the caller should rotate to another proxy.
func fetchOnce(ctx context.Context, client *http.Client, u *url.URL) (string, bool, error) {
	text, err := fetchWithClient(ctx, client, u)
	rotate := isTransportErr(err) || errors.Is(err, errRetryableStatus)
	return text, rotate, err
}

// fetchWithClient is the previous runFetchURL body minus proxy setup —
// it does the redirect-validated GET and text extraction.
func fetchWithClient(ctx context.Context, client *http.Client, u *url.URL) (string, error) {
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
		if retryableStatus(resp.StatusCode) {
			return "", fmt.Errorf("%w: HTTP %d", errRetryableStatus, resp.StatusCode)
		}
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
			User *struct {
				ScreenName  string `json:"screen_name"`
				Name        string `json:"name"`
				Description string `json:"description"`
				Followers   int    `json:"followers"`
				Following   int    `json:"following"`
				Tweets      int    `json:"tweets"`
				Likes       int    `json:"likes"`
				Joined      string `json:"joined"`
				Location    string `json:"location"`
				Website     struct {
					DisplayURL string `json:"display_url"`
				} `json:"website"`
			} `json:"user"`
		}
		var bsky struct {
			Feed []struct {
				Post struct {
					Author struct {
						Handle      string `json:"handle"`
						DisplayName string `json:"displayName"`
					} `json:"author"`
					Record struct {
						Text      string `json:"text"`
						CreatedAt string `json:"createdAt"`
					} `json:"record"`
					LikeCount   int `json:"likeCount"`
					RepostCount int `json:"repostCount"`
				} `json:"post"`
			} `json:"feed"`
			Thread *struct {
				Post struct {
					Author struct {
						Handle      string `json:"handle"`
						DisplayName string `json:"displayName"`
					} `json:"author"`
					Record struct {
						Text      string `json:"text"`
						CreatedAt string `json:"createdAt"`
					} `json:"record"`
					LikeCount   int `json:"likeCount"`
					RepostCount int `json:"repostCount"`
				} `json:"post"`
			} `json:"thread"`
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
		case json.Unmarshal(body, &fx) == nil && fx.User != nil && fx.User.ScreenName != "":
			u := fx.User
			var b strings.Builder
			fmt.Fprintf(&b, "@%s（%s）\n", u.ScreenName, u.Name)
			if u.Description != "" {
				fmt.Fprintf(&b, "简介：%s\n", u.Description)
			}
			fmt.Fprintf(&b, "粉丝 %d · 关注 %d · 推文 %d · 喜欢 %d\n",
				u.Followers, u.Following, u.Tweets, u.Likes)
			if u.Joined != "" {
				fmt.Fprintf(&b, "注册：%s\n", u.Joined)
			}
			if u.Location != "" {
				fmt.Fprintf(&b, "位置：%s\n", u.Location)
			}
			if u.Website.DisplayURL != "" {
				fmt.Fprintf(&b, "网站：%s\n", u.Website.DisplayURL)
			}
			b.WriteString("（X 的推文列表接口已失效；具体推文请发单条链接。" +
				"若简介/网站挂了 bsky.app 主页，抓那个链接能看到最近帖子）")
			text = b.String()
		case json.Unmarshal(body, &bsky) == nil && len(bsky.Feed) > 0:
			var b strings.Builder
			b.WriteString("Bluesky 最近帖子：\n")
			for _, item := range bsky.Feed {
				b.WriteString(formatBskyPost(
					item.Post.Author.Handle,
					item.Post.Author.DisplayName,
					item.Post.Record.Text,
					item.Post.Record.CreatedAt,
					item.Post.LikeCount,
					item.Post.RepostCount))
			}
			text = b.String()
		case json.Unmarshal(body, &bsky) == nil && bsky.Thread != nil &&
			bsky.Thread.Post.Record.Text != "":
			p := bsky.Thread.Post
			text = formatBskyPost(p.Author.Handle, p.Author.DisplayName,
				p.Record.Text, p.Record.CreatedAt,
				p.LikeCount, p.RepostCount)
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

// formatBskyPost renders one Bluesky feed item as a chat-friendly
// line: date @handle（昵称）: text  ♥x ↻x
func formatBskyPost(handle, displayName, text, createdAt string, likes, reposts int) string {
	date := createdAt
	if len(date) >= 16 {
		date = date[:16] // keep "2006-01-02T15:04"
	}
	who := "@" + handle
	if displayName != "" {
		who += "（" + displayName + "）"
	}
	text = strings.Join(strings.Fields(text), " ")
	return fmt.Sprintf("· %s %s：%s  ♥%d ↻%d\n",
		date, who, text, likes, reposts)
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
