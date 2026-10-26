package bot

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/lianyorker/cinlan-qq-bot/internal/domain"
	"github.com/lianyorker/cinlan-qq-bot/internal/message"
)

const naturalReplyBreak = "[[NEXT]]"

// memeMarkerPattern matches a whole reply part like "[[MEME]]" or
// "[[MEME:嘲笑]]" that asks the bot to send a sticker from the chat's meme
// pool instead of text.
var memeMarkerPattern = regexp.MustCompile(
	`^\s*\[\[\s*MEME\s*(?:[:：]\s*([^\[\]]{1,20}))?\s*\]\]\s*$`,
)

// parseMemeMarker reports whether the part is a [[MEME]] marker and returns
// the optional emotion/query keyword.
func parseMemeMarker(part string) (string, bool) {
	m := memeMarkerPattern.FindStringSubmatch(part)
	if m == nil {
		return "", false
	}
	return strings.TrimSpace(m[1]), true
}

// storedImageMarkerPattern matches a whole reply part like "[[IMG:七七自拍]]"
// that asks the bot to resend an image remembered via /记住 image notes.
var storedImageMarkerPattern = regexp.MustCompile(
	`^\s*\[\[\s*(?:IMG|IMAGE|PHOTO)\s*[:：]\s*([^\[\]]{1,40})\s*\]\]\s*$`,
)

// parseStoredImageMarker reports whether the part is an [[IMG:key]] marker
// and returns the remembered-image key.
func parseStoredImageMarker(part string) (string, bool) {
	m := storedImageMarkerPattern.FindStringSubmatch(part)
	if m == nil {
		return "", false
	}
	return strings.TrimSpace(m[1]), true
}

var faceMarkerPattern = regexp.MustCompile(
	`\[(?:face|表情)[:：]([^\[\]]{1,8})\]`,
)

// faceNameToID maps common QQ face names to their numeric ids (classic
// 0-21 table). Unknown names are dropped from the text.
var faceNameToID = map[string]int{
	"惊讶": 0, "撇嘴": 1, "色": 2, "发呆": 3, "得意": 4,
	"流泪": 5, "害羞": 6, "闭嘴": 7, "睡": 8, "大哭": 9,
	"尴尬": 10, "发怒": 11, "调皮": 12, "龇牙": 13, "呲牙": 13,
	"微笑": 14, "难过": 15, "酷": 16, "抓狂": 18, "吐": 20,
	"偷笑": 21, "可爱": 21, "白眼": 22, "傲慢": 23, "饥饿": 24,
	"困": 25, "惊恐": 26, "流汗": 27, "憨笑": 28, "奋斗": 30,
	"咒骂": 31, "疑问": 32, "嘘": 33, "晕": 34, "折磨": 35,
	"衰": 36, "骷髅": 37, "敲打": 38, "再见": 39, "发抖": 41,
	"爱情": 42, "跳跳": 43, "猪头": 44, "拥抱": 45, "蛋糕": 46,
	"闪电": 47, "炸弹": 48, "刀": 49, "足球": 50, "瓢虫": 51,
	"便便": 52, "咖啡": 53, "饭": 54, "药": 55, "玫瑰": 56,
	"凋谢": 57, "示爱": 58, "爱心": 59, "心碎": 60, "礼物": 62,
	"太阳": 63, "月亮": 64, "握手": 66, "胜利": 67, "抱拳": 68,
	"勾引": 69, "拳头": 70, "差劲": 71, "爱你": 72, "飞吻": 76,
	"怄火": 79, "转圈": 80, "磕头": 81, "回头": 82, "跳绳": 83,
	"挥手": 84, "激动": 85, "街舞": 86, "献吻": 87, "冷汗": 96,
	"擦汗": 97, "抠鼻": 98, "鼓掌": 99, "糗大了": 100, "坏笑": 101,
	"哈欠": 104, "鄙视": 105, "委屈": 106, "快哭了": 107, "阴险": 108,
	"亲亲": 109, "吓": 110, "可怜": 111, "菜刀": 112, "西瓜": 113,
	"啤酒": 114, "篮球": 115, "乒乓": 116,
	// aliases so natural-language tags hit real QQ faces too
	"生气": 11, "脸红": 6, "开心": 28, "笑": 28,
	"哭": 9, "哭哭": 9, "亲": 109, "么么哒": 109,
}

// emotionToEmoji covers persona emotion tags that have no classic QQ
// face id — they degrade to a unicode emoji (QQ clients render these
// natively) instead of leaking the literal tag.
var emotionToEmoji = map[string]string{
	"傲娇": "\U0001f624", "气鼓鼓": "\U0001f624", "星星眼": "\U0001f929",
	"卖萌": "\U0001f970", "撒娇": "\U0001f97a", "委屈巴巴": "\U0001f97a",
	"装死": "\U0001f480", "躺平": "\U0001fae0", "吃瓜": "\U0001f349",
	"狗头": "\U0001f436", "泪目": "\U0001f97b", "感动": "\U0001f97b",
	"黑线": "\U0001f611", "汗颜": "\U0001f605", "捂脸": "\U0001f926",
	"偷看": "\U0001fae3", "偷瞄": "\U0001fae3", "啵": "\U0001f61a",
	"mua": "\U0001f618", "蹭": "\U0001f970", "蹭蹭": "\U0001f970",
	"叹气": "\U0001f62e\u200d\U0001f4a8", "震惊": "\U0001f631", "呆滞": "\U0001f636",
	"哭唧唧": "\U0001f62d", "嘤嘤": "\U0001f62d", "比耶": "\u270c\ufe0f",
	"打call": "\U0001f4e3", "撒花": "\U0001f389", "送花": "\U0001f490",
	"晚安": "\U0001f634", "早安": "\U0001f31e", "饿": "\U0001f924",
	"馋": "\U0001f924", "流口水": "\U0001f924", "贴贴": "\U0001f970",
	"举爪": "\U0001f64b", "沉思": "\U0001f914", "笑死": "\U0001f923",
	"偷乐": "\U0001f92d", "坏坏": "\U0001f60f", "得瑟": "\U0001f60f",
	"炸毛": "\U0001f63e", "凶": "\U0001f620", "凶巴巴": "\U0001f620",
	"害羞羞": "\U0001f648", "脸红红": "\u263a\ufe0f", "冒心心": "\U0001f60d",
	"眼冒心心": "\U0001f60d", "爱心眼": "\U0001f60d", "崇拜": "\U0001f929",
	"跪了": "\U0001f647", "加油鸭": "\U0001f4aa", "冲鸭": "\U0001f986",
	"摸头": "\U0001faf3", "捏脸": "\U0001f90f", "抱抱": "\U0001f917",
	"举高高": "\U0001f64c", "干杯": "\U0001f37b", "吃糖": "\U0001f36c",
}

var bareEmotionPattern = buildBareEmotionPattern()

func buildBareEmotionPattern() *regexp.Regexp {
	names := make([]string, 0, len(faceNameToID)+len(emotionToEmoji))
	for n := range faceNameToID {
		names = append(names, n)
	}
	for n := range emotionToEmoji {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		return len(names[i]) > len(names[j])
	})
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = regexp.QuoteMeta(n)
	}
	return regexp.MustCompile(`\[(?:` + strings.Join(quoted, "|") + `)\]`)
}

// expandBareEmotionTags converts the bare "[亲亲]"-style tags the model
// actually emits (no face:/表情: prefix) into the canonical face marker
// when a QQ face id exists, else a unicode emoji. Unknown brackets are
// left untouched so real text in [] survives.
func expandBareEmotionTags(text string) string {
	return bareEmotionPattern.ReplaceAllStringFunc(
		text,
		func(tag string) string {
			name := strings.TrimSuffix(strings.TrimPrefix(tag, "["), "]")
			if _, ok := faceNameToID[name]; ok {
				return "[face:" + name + "]"
			}
			if emoji, ok := emotionToEmoji[name]; ok {
				return emoji
			}
			return tag
		},
	)
}

// parseFaceMarkers splits text into a chain of text/face components.
// Returns the chain, the marker-stripped plain text, and whether any
// marker was consumed. Unknown face names are removed silently.
func parseFaceMarkers(text string) (message.Chain, string, bool) {
	orig := text
	text = expandBareEmotionTags(text)
	expanded := text != orig
	matches := faceMarkerPattern.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return message.Chain{message.Text(text)}, text, expanded
	}
	var (
		chain   message.Chain
		cleaned strings.Builder
		found   bool
		cursor  int
	)
	flush := func(end int) {
		if end <= cursor {
			return
		}
		segment := text[cursor:end]
		cleaned.WriteString(segment)
		if strings.TrimSpace(segment) != "" {
			chain = append(chain, message.Text(segment))
		}
	}
	for _, m := range matches {
		value := strings.TrimSpace(text[m[2]:m[3]])
		id, err := strconv.Atoi(value)
		if err != nil {
			mapped, ok := faceNameToID[value]
			if !ok {
				flush(m[0])
				if emoji, has := emotionToEmoji[value]; has {
					// Named but unmapped emotion: emit the unicode
					// emoji rather than swallowing the marker.
					chain = append(chain, message.Text(emoji))
					cleaned.WriteString(emoji)
				}
				// Fully unknown name: still consume the marker so it
				// never leaks into the chat as literal text.
				cursor = m[1]
				found = true
				continue
			}
			id = mapped
		}
		if id < 0 || id > 349 {
			flush(m[0])
			cursor = m[1]
			found = true
			continue
		}
		flush(m[0])
		chain = append(chain, message.Face(strconv.Itoa(id)))
		cursor = m[1]
		found = true
	}
	flush(len(text))
	if !found && !expanded {
		return message.Chain{message.Text(text)}, text, false
	}
	return chain, cleaned.String(), true
}

// atMarkerPattern matches [[AT]] (at the current sender) and
// [[AT:12345678]] (at a specific member by QQ id). Like the other
// markers the model can learn these from the system prompt note.
var atMarkerPattern = regexp.MustCompile(
	`\[\[(?:AT|at)(?::\s*([0-9]{5,12}))?\]\]`,
)

const maxAtPerPart = 3

// expandAtMarkers post-processes a parsed part chain: at-markers inside
// text components become real at-segments so the model can ping members.
// defaultUserID is the sender being replied to, used for bare [[AT]].
// Markers past the cap degrade to stripped text (never leak literally).
func expandAtMarkers(chain message.Chain, defaultUserID string) (message.Chain, string) {
	var out message.Chain
	var cleaned strings.Builder
	ats := 0
	for _, comp := range chain {
		if comp.Type != message.TypeText {
			out = append(out, comp)
			continue
		}
		text, _ := comp.Data["text"].(string)
		matches := atMarkerPattern.FindAllStringSubmatchIndex(text, -1)
		if len(matches) == 0 {
			out = append(out, comp)
			cleaned.WriteString(text)
			continue
		}
		cursor := 0
		for _, m := range matches {
			if segment := text[cursor:m[0]]; segment != "" {
				out = append(out, message.Text(segment))
				cleaned.WriteString(segment)
			}
			target := defaultUserID
			if m[2] >= 0 {
				target = strings.TrimSpace(text[m[2]:m[3]])
			}
			if target != "" && ats < maxAtPerPart {
				out = append(out, message.At(target))
				ats++
			}
			cursor = m[1]
		}
		if tail := text[cursor:]; tail != "" {
			out = append(out, message.Text(tail))
			cleaned.WriteString(tail)
		}
	}
	if len(out) == 0 {
		out = chain
	}
	return out, cleaned.String()
}

func planReplyParts(reply string, maxParts int) []string {
	reply = strings.TrimSpace(reply)
	if reply == "" {
		return nil
	}
	if maxParts <= 0 {
		maxParts = 1
	}
	parts := make([]string, 0, maxParts)
	for _, segment := range strings.Split(reply, naturalReplyBreak) {
		// Stray blank lines inside a thought get folded to a single newline —
		// only an explicit [[NEXT]] may start a new bubble.
		segments := splitOnBlankLines(segment)
		for i := range segments {
			segments[i] = strings.TrimSpace(segments[i])
		}
		joined := strings.Join(segments, "\n")
		if part := strings.TrimSpace(joined); part != "" {
			parts = append(parts, part)
		}
	}
	// Up to maxParts [[NEXT]] bursts ride through as separate bubbles —
	// people fire several short messages, not two giant ones. Any
	// excess merges into the last bubble so nothing is lost.
	if len(parts) <= maxParts {
		return parts
	}
	head := append([]string{}, parts[:maxParts-1]...)
	return append(head, strings.Join(parts[maxParts-1:], "\n"))
}

// capBurstParts re-enforces the bubble budget after splitMarkerLines
// re-expanded parts at marker boundaries. Overflowing marker parts are
// dropped silently; overflowing text folds into the previous text bubble
// so nothing the model wrote disappears.
func capBurstParts(parts []string, max int) []string {
	if max <= 0 {
		return parts
	}
	for len(parts) > max {
		tail := parts[len(parts)-1]
		parts = parts[:len(parts)-1]
		if isMarkerPart(tail) {
			continue
		}
		merged := false
		for i := len(parts) - 1; i >= 0; i-- {
			if !isMarkerPart(parts[i]) {
				parts[i] = strings.TrimSpace(parts[i] + "\n" + tail)
				merged = true
				break
			}
		}
		if !merged {
			break
		}
	}
	return parts
}

// inlineMarkerPattern finds [[MEME...]] / [[IMG:...]] / [[IMAGE:...]] /
// [[PHOTO:...]] / [[SELFIE:...]] markers and the micro-action markers
// ([[REACT:x]] [[POKE]] [[DICE]] [[RPS]] [[OOPS]]..[[/OOPS]]) anywhere
// inside a line — including glued to the end of a sentence — so they
// never reach chat as literal text.
var inlineMarkerPattern = regexp.MustCompile(
	`\[\[\s*(?:MEME|IMG|IMAGE|PHOTO|SELFIE|REACT|EMOJI|LIKE|表情|POKE|DICE|RPS|骰子|猜拳)\s*(?:[:：][^\[\]]{1,40})?\s*\]\]` +
		`|\[\[\s*OOPS\s*\]\][^\[\]]*\[\[\s*/\s*OOPS\s*\]\]` +
		`|\[\[[^\[\]]{1,60}\]\]`, // unknown [[..]] — dropped at split
)

// splitMarkerLines splits a reply part into sub-parts so a marker mixed
// with text ("嗯 抱着了\n[[MEME:害羞]]" or "骂你[[MEME:无语]]") becomes its
// own bubble — resolvable markers turn into image sends, unresolvable
// ones get skipped, and neither leaks as literal text.
func splitMarkerLines(part string) []string {
	var result []string
	cursor := 0
	for _, span := range inlineMarkerPattern.FindAllStringIndex(part, -1) {
		if segment := strings.TrimSpace(part[cursor:span[0]]); segment != "" {
			result = append(result, segment)
		}
		marker := part[span[0]:span[1]]
		if isMarkerPart(marker) {
			result = append(result, marker)
		}
		cursor = span[1]
	}
	if tail := strings.TrimSpace(part[cursor:]); tail != "" {
		result = append(result, tail)
	}
	return result
}

// isMarkerPart reports whether a standalone part is a known marker that
// stageRespond knows how to resolve (image sends, emoji reactions,
// pokes, dice, recall-the-oops). Unknown [[..]] text stays dropped.
func isMarkerPart(part string) bool {
	if _, ok := parseMemeMarker(part); ok {
		return true
	}
	if _, ok := parseStoredImageMarker(part); ok {
		return true
	}
	if _, ok := parseSelfieMarker(part); ok {
		return true
	}
	if _, ok := parseReactMarker(part); ok {
		return true
	}
	if _, ok := parsePokeMarker(part); ok {
		return true
	}
	if _, ok := parseOopsMarker(part); ok {
		return true
	}
	return isDiceMarker(part) || isRPSMarker(part)
}

// splitOnBlankLines splits text at paragraph boundaries (blank lines) so a
// multi-thought reply becomes multiple messages, like a real person's send
// cadence. Splits are skipped inside fenced code blocks where blank lines
// are meaningful content.
func splitOnBlankLines(text string) []string {
	var (
		parts   []string
		start   int
		inFence bool
		lineBeg = 0
	)
	lines := strings.SplitAfter(text, "\n")
	for _, line := range lines {
		trimmed := strings.TrimRight(line, " \t\r\n")
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
		}
		if !inFence && trimmed == "" && lineBeg > start {
			if part := text[start:lineBeg]; strings.TrimSpace(part) != "" {
				parts = append(parts, part)
			}
			start = lineBeg + len(line)
		} else if !inFence && trimmed == "" {
			start = lineBeg + len(line)
		}
		lineBeg += len(line)
	}
	if rest := text[start:]; strings.TrimSpace(rest) != "" {
		parts = append(parts, rest)
	}
	return parts
}

var echoedMarkerPattern = regexp.MustCompile(
	`^\s*(?:\[[yY]\]|【[yY]】)\s*`,
)

// providerErrorPattern matches raw upstream API errors (policy refusals,
// transport failures) the model may leak verbatim into the reply.
var providerErrorPattern = regexp.MustCompile(
	`(?i)(could not be submitted|prohibited use policy|` +
		`policies\.google|ai\.google\.dev|request failed with|rate limit|` +
		`context deadline|connection refused|invalid api key|insufficient quota)`,
)

// providerErrorFallback replaces a leaked upstream error with something a
// human would actually say instead of pasting the failure.
const providerErrorFallback = "生图被拦了 换个说法我再试试"

// containsProviderError reports whether reply text is or embeds a leaked
// provider error message.
func containsProviderError(text string) bool {
	return providerErrorPattern.MatchString(text)
}

// selfTagPattern matches the internal "[SELF]" image-prompt marker — the
// model tags self-image tool calls with it and sometimes echoes the whole
// prompt (tag included) back into the reply. It is never chat content.
var selfTagPattern = regexp.MustCompile(`(?i)\[\s*self\s*\]\s*`)

// These patterns catch the markdown syntax models keep emitting even
// after being told not to — QQ shows raw text, so **bold**, > quotes and
// ``` fences arrive as literal garbage. Soft prompt rules stay; this is
// the hard outbound scrub.
var (
	// Fenced code blocks: drop the fence lines, keep the content.
	markdownFencePattern = regexp.MustCompile(`(?m)^\s*` + "```" + `[a-zA-Z]*\s*\n?`)
	// Bold / italic / strike → unwrap to the inner text (loop until none
	// left so ***nested*** markers fully dissolve).
	markdownEmphPattern = regexp.MustCompile(
		`\*\*([^*]+)\*\*|__([^_]+)__|~~([^~]+)~~|\*([^*]+)\*|_([^_]+)_`,
	)
	markdownCodePattern = regexp.MustCompile("`([^`\n]+)`")
	// Line-leading structural markers: headers, quotes, bullets, ordered
	// lists. Chat lines that happen to start with "- " are rare compared
	// to model bullet dumps.
	markdownLinePatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?m)^[ \t]{0,3}#{1,6}\s+`),
		regexp.MustCompile(`(?m)^[ \t]{0,3}>\s?`),
		regexp.MustCompile(`(?m)^([ \t]*)(?:[-*•]|\d{1,2}[.)])\s+`),
	}
)

// scrubMarkdown removes markdown syntax from an outbound text part so the
// message reads like typed chat, not a rendered document. Inner content
// is preserved — only the decoration dies.
func scrubMarkdown(text string) string {
	text = selfTagPattern.ReplaceAllString(text, "")
	text = markdownFencePattern.ReplaceAllString(text, "")
	for _, p := range markdownLinePatterns {
		text = p.ReplaceAllString(text, "$1")
	}
	for {
		cleaned := markdownEmphPattern.ReplaceAllString(text, "$1$2$3$4$5")
		if cleaned == text {
			break
		}
		text = cleaned
	}
	text = markdownCodePattern.ReplaceAllString(text, "$1")
	return strings.TrimSpace(text)
}

// stripSpeakerPrefix removes leaked channel markers ("[Y]", "【Y】") and a
// leading speaker-name prefix the model may echo so replies never announce
// the persona name as their first line.
func stripSpeakerPrefix(text, name string) string {
	for {
		cleaned := echoedMarkerPattern.ReplaceAllString(text, "")
		if cleaned == text {
			break
		}
		text = cleaned
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return text
	}
	candidates := []string{name}
	for _, sep := range []string{"·", "（", "("} {
		if i := strings.Index(name, sep); i > 0 {
			candidates = append(candidates, strings.TrimSpace(name[:i]))
			break
		}
	}
	trimmed := strings.TrimLeft(text, " \t")
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		for _, prefix := range []string{
			candidate + "：", candidate + ":", candidate + " ", candidate + "\n",
			"【" + candidate + "】", "[" + candidate + "]", "「" + candidate + "」",
		} {
			if strings.HasPrefix(trimmed, prefix) {
				return strings.TrimLeft(strings.TrimPrefix(trimmed, prefix), " \t")
			}
		}
		if trimmed == candidate {
			return ""
		}
	}
	return text
}

// softSplitReply breaks a long single-bubble reply into 1–3 parts at
// sentence boundaries when the model forgot [[NEXT]]. Most replies must
// stay whole — a predictable multi-bubble cadence on every reply is the
// exact tell that makes her read like a bot — so the split chance is
// length-weighted: short-to-mid replies rarely split, a third bubble is
// reserved for real essays, and replies under 80 runes never split.
func softSplitReply(reply string, roll float64) []string {
	runes := []rune(reply)
	if len(runes) < 80 {
		return []string{reply}
	}
	var chance float64
	switch {
	case len(runes) < 160:
		chance = 0.35
	case len(runes) < 240:
		chance = 0.60
	default:
		chance = 0.85
	}
	if roll >= chance {
		return []string{reply}
	}
	parts := []string{reply}
	for len(parts) < 3 {
		if len(parts) == 2 && len(runes) < 240 {
			break
		}
		tail := parts[len(parts)-1]
		best := bestSoftSplitAt(tail)
		if best < 0 {
			break
		}
		tailRunes := []rune(tail)
		head := strings.TrimSpace(string(tailRunes[:best+1]))
		rest := strings.TrimSpace(string(tailRunes[best+1:]))
		if head == "" || rest == "" {
			break
		}
		// A second split only happens when the leftover is big enough to
		// read as its own bubble — otherwise the chop looks mechanical.
		if len(parts) > 1 && len([]rune(rest)) < 40 {
			break
		}
		parts[len(parts)-1] = head
		parts = append(parts, rest)
	}
	return parts
}

// bestSoftSplitAt finds the sentence-ending boundary nearest the middle
// of text, or -1 when no usable boundary exists.
func bestSoftSplitAt(text string) int {
	runes := []rune(text)
	mid := len(runes) / 2
	best, bestScore := -1, -1
	for i := 12; i < len(runes)-8; i++ {
		d := i - mid
		if d < 0 {
			d = -d
		}
		score := -1
		switch runes[i] {
		case '。', '！', '？', '!', '?', '；', ';':
			score = 1000 - d
		case '~', '～':
			score = 300 - d
		}
		if score > bestScore {
			bestScore, best = score, i
		}
	}
	return best
}

// sentTimestampPattern strips the "[MM-DD HH:MM] " stamp session history
// puts on recorded assistant turns.
var sentTimestampPattern = regexp.MustCompile(`^\[\d{2}-\d{2} \d{2}:\d{2}\]\s*`)

// recentAssistantTexts returns her last few plain-text sends from session
// history — the baseline filterSelfEcho compares new parts against.
func recentAssistantTexts(history []domain.ChatMessage, limit int) []string {
	texts := make([]string, 0, limit)
	for i := len(history) - 1; i >= 0 && len(texts) < limit; i-- {
		if history[i].Role != "assistant" {
			continue
		}
		content := strings.TrimSpace(
			sentTimestampPattern.ReplaceAllString(history[i].Content, ""),
		)
		if content != "" {
			texts = append(texts, content)
		}
	}
	return texts
}

// echoTrimCutoff is the shortest verbatim overlap worth removing — below
// this the shared run could be a coincidence ("好慢" "昂" style filler).
const echoTrimCutoff = 5

// trimRepeatedSpan removes the longest run of text that already appeared
// verbatim in one of the recent sends, so "好慢 熬着等天黑呢 快下班来找我"
// doesn't re-ship "熬着等天黑呢" one turn after she coined it.
func trimRepeatedSpan(part string, sent []string) string {
	current := part
	for _, text := range sent {
		a := []rune(current)
		b := []rune(text)
		best, bestLen := -1, 0
		// Longest common substring (contiguous run), rune-accurate.
		prev := make([]int, len(b)+1)
		for i := 1; i <= len(a); i++ {
			row := make([]int, len(b)+1)
			for j := 1; j <= len(b); j++ {
				if a[i-1] == b[j-1] {
					row[j] = prev[j-1] + 1
					if row[j] > bestLen {
						bestLen, best = row[j], i
					}
				}
			}
			prev = row
		}
		if bestLen < echoTrimCutoff {
			continue
		}
		start := best - bestLen
		current = strings.TrimSpace(
			string(a[:start]) + " " + string(a[best:]),
		)
	}
	return current
}

// filterSelfEcho drops or trims reply parts that repeat something she
// literally just sent — the "said the same phrase twice in a row" tell.
// When every part would be eaten the first survives: no reply at all is
// a worse failure than a mild echo.
func filterSelfEcho(parts []string, sent []string) []string {
	if len(sent) == 0 {
		return parts
	}
	filtered := parts[:0]
	for _, part := range parts {
		trimmed := trimRepeatedSpan(part, sent)
		if trimmed == "" || len([]rune(trimmed)) < 3 {
			continue
		}
		filtered = append(filtered, trimmed)
	}
	if len(filtered) == 0 && len(parts) > 0 {
		return parts[:1]
	}
	return filtered
}

func expandReplyParts(parts []string, maxRunes, maxChunks int) ([]string, bool) {
	result := make([]string, 0, min(len(parts), maxChunks))
	truncated := false
	for _, part := range parts {
		remaining := maxChunks - len(result)
		if remaining <= 0 {
			truncated = true
			break
		}
		chunks, cut := splitReply(part, maxRunes, remaining)
		if cut {
			truncated = true
		}
		result = append(result, chunks...)
	}
	return result, truncated
}

func isTextReplyChain(chain message.Chain) bool {
	return len(chain) == 1 && chain[0].Type == message.TypeText
}
