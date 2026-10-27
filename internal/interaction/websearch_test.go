package interaction

import (
	"strings"
	"testing"
)

const bingFixture = `<html><body><ol id="b_results">
<li class="b_algo"><h2><a href="https://openai.com/gpt-5" target="_blank">GPT-5 发布：OpenAI 新旗舰</a></h2><div class="b_caption"><p>OpenAI 今日发布 GPT-5，推理与多模态大幅升级，Plus 用户首批可用。</p></div></li>
<li class="b_ad"><h2><a href="https://ads.example.com/x">广告位不该被收</a></h2></li>
<li class="b_algo"><h2><a href="javascript:void(0)">非 http 链接跳过</a></h2></li>
<li class="b_algo"><h2><a href="https://www.anthropic.com/claude">Claude Sonnet 更新日志 &amp; 说明</a></h2><p>Sonnet 4.5 提升代码能力，Extended Thinking 更长。</p></li>
</ol></body></html>`

func TestParseBingResults(t *testing.T) {
	results := parseBingResults(bingFixture)
	if len(results) != 2 {
		t.Fatalf("want 2 results, got %d: %+v", len(results), results)
	}
	if results[0].Title != "GPT-5 发布：OpenAI 新旗舰" {
		t.Fatalf("title0 = %q", results[0].Title)
	}
	if results[0].URL != "https://openai.com/gpt-5" {
		t.Fatalf("url0 = %q", results[0].URL)
	}
	if !strings.Contains(results[0].Snippet, "GPT-5") {
		t.Fatalf("snippet0 = %q", results[0].Snippet)
	}
	if results[1].Title != "Claude Sonnet 更新日志 & 说明" {
		t.Fatalf("title1 entity decode = %q", results[1].Title)
	}
}

func TestParseBingResultsEmpty(t *testing.T) {
	if got := parseBingResults(`<html><body>no results</body></html>`); len(got) != 0 {
		t.Fatalf("want empty, got %+v", got)
	}
}

func TestFormatWebSearchResultsCapsRunes(t *testing.T) {
	var results []webSearchResult
	for i := 0; i < webSearchMaxResults; i++ {
		results = append(results, webSearchResult{
			Title:   strings.Repeat("标题", 100),
			Snippet: strings.Repeat("摘要", 200),
			URL:     "https://example.com",
		})
	}
	out := formatWebSearchResults(results)
	if got := len([]rune(out)); got > webSearchMaxRunes+1 {
		t.Fatalf("formatted %d runes exceeds cap %d", got, webSearchMaxRunes)
	}
}
