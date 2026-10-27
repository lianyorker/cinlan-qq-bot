package bot

import (
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Outbound banned words are a platform-level hard block — different from
// ToneBans (per-chat "please stop saying X" requests). The base list is
// deliberately narrow and high-risk only; operators extend it by dropping
// lines into data/outbound-banned-words.txt, which hot-reloads on mtime
// change without a restart.
var outboundBannedBaseWords = []string{
	// 政治敏感 — outbound must never produce these regardless of context.
	"习近平", "江泽民", "胡锦涛", "六四", "法轮功", "天安门事件",
	"港独", "台独", "疆独", "藏独",
	// 色情/低俗 — text mirror of the voice safety list.
	"色情", "裸体", "做爱", "口交", "约炮", "援交", "福利姬", "看片",
	"娇喘", "叫床", "发情",
	// 广告/引流 — QQ moderation flags these aggressively.
	"代充", "加微信", "加v信", "vx:", "扫码进群", "扫码领取", "加群领",
	"刷单", "网赚", "博彩", "赌球", "彩票网站", "低价代购",
	// 未成年红线 — 任何 level 一律不产出，provider 不设防时兜底
	// (gemini-3.8-flash-high 实测被诱导直接写高中恋爱故事).
	"未成年", "未满十八", "未满18", "幼女", "恋童", "儿童色情",
	"萝莉岛", "小学生恋爱",
}

// outboundBannedWords carries the optional operator list for hot reload.
type outboundBannedWords struct {
	mu       sync.Mutex
	file     string
	mtime    time.Time
	nextStat time.Time
	pattern  *regexp.Regexp
}

// newOutboundBannedWords builds the combined base+file pattern. Missing
// file is fine — the base list still applies.
func newOutboundBannedWords(file string) *outboundBannedWords {
	w := &outboundBannedWords{file: file}
	w.rebuild()
	return w
}

// pattern returns the live regexp, reloading the file at most every 30s
// when its mtime changed.
func (w *outboundBannedWords) patternLive() *regexp.Regexp {
	if w == nil {
		return outboundBannedBasePattern
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file != "" && time.Now().After(w.nextStat) {
		w.nextStat = time.Now().Add(30 * time.Second)
		if st, err := os.Stat(w.file); err == nil && st.ModTime() != w.mtime {
			w.mtime = st.ModTime()
			w.rebuild()
		}
	}
	return w.pattern
}

func (w *outboundBannedWords) rebuild() {
	words := append([]string(nil), outboundBannedBaseWords...)
	if w.file != "" {
		if data, err := os.ReadFile(w.file); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSpace(line)
				if line != "" && !strings.HasPrefix(line, "#") {
					words = append(words, line)
				}
			}
		}
	}
	w.pattern = regexp.MustCompile(`(?i)` + joinQuotedWords(words))
}

var outboundBannedBasePattern = regexp.MustCompile(
	`(?i)` + joinQuotedWords(outboundBannedBaseWords),
)

func joinQuotedWords(words []string) string {
	var b strings.Builder
	for i, w := range words {
		if i > 0 {
			b.WriteByte('|')
		}
		b.WriteString(regexp.QuoteMeta(w))
	}
	return b.String()
}

// removeOutboundBanned drops whole sentences that hit the banned pattern —
// same shape as removeLinks so an all-banned reply collapses to "" and is
// not sent rather than shipped with a visible "filtered" scar.
func removeOutboundBanned(text string, pattern *regexp.Regexp) (string, bool) {
	if pattern == nil || !pattern.MatchString(text) {
		return text, false
	}
	var kept strings.Builder
	for _, sentence := range splitSentences(text) {
		if pattern.MatchString(sentence) {
			continue
		}
		kept.WriteString(sentence)
	}
	return strings.TrimSpace(kept.String()), true
}
