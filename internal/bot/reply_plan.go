package bot

import (
	"math/rand"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/lianyorker/cinlan-qq-bot/internal/domain"
	"github.com/lianyorker/cinlan-qq-bot/internal/message"
	"github.com/lianyorker/cinlan-qq-bot/internal/vecstore"
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

// quoteMarkerPattern matches [[QUOTE]] — the model tags a bubble that
// specifically answers an earlier message, and the chain gets a real
// reply/quote segment pointing at the inbound message_id.
var quoteMarkerPattern = regexp.MustCompile(
	`\[\[(?:QUOTE|quote)\]\]\s*`,
)

// stripQuoteMarker removes the marker and reports whether this part
// asked for a quote. Runs before the unknown-marker stripper, which
// would otherwise eat [[QUOTE]] as an unrecognized tag.
func stripQuoteMarker(part string) (string, bool) {
	if !quoteMarkerPattern.MatchString(part) {
		return part, false
	}
	return strings.TrimSpace(quoteMarkerPattern.ReplaceAllString(part, "")), true
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
		var lines []string
		for _, piece := range splitOnBlankLines(segment) {
			for _, ln := range strings.Split(piece, "\n") {
				if t := strings.TrimSpace(ln); t != "" {
					lines = append(lines, t)
				}
			}
		}
		if part := joinBubbleLines(lines); part != "" {
			parts = append(parts, part)
		}
	}
	// Up to maxParts [[NEXT]] bursts ride through as separate bubbles —
	// people fire several short messages, not two giant ones. Any
	// excess merges into the last bubble so nothing is lost. Short
	// casual fragments merged this way fold continuous (comma at
	// unpunctuated boundaries) — a "\n"-joined stack of 5-char lines
	// reads staged for the same reason a mid-bubble break does.
	if len(parts) <= maxParts {
		return parts
	}
	head := append([]string{}, parts[:maxParts-1]...)
	rest := parts[maxParts-1:]
	merged := strings.Join(rest, "\n")
	if len(rest) <= 4 {
		total := 0
		foldable := true
		for _, p := range rest {
			if strings.ContainsAny(p, "\n[") {
				foldable = false
			}
			total += len([]rune(p))
		}
		if foldable && total <= 60 {
			merged = foldJoin(rest)
		}
	}
	return append(head, merged)
}

// joinBubbleLines rejoins the lines inside one [[NEXT]] segment. On QQ,
// pressing Enter sends the message — a real person can hardly produce a
// mid-bubble line break, so when the model emits a bare newline inside
// short casual chatter it reads staged. Fold those into one continuous
// line; structured or longer content (steps, lists, code) keeps its
// newlines because joining them would destroy the content itself.
func joinBubbleLines(lines []string) string {
	switch len(lines) {
	case 0:
		return ""
	case 1:
		return lines[0]
	}
	total := 0
	for _, ln := range lines {
		total += len([]rune(ln))
		if strings.HasPrefix(ln, "```") {
			return strings.Join(lines, "\n")
		}
	}
	if len(lines) <= 4 && total <= 60 {
		return foldJoin(lines)
	}
	return strings.Join(lines, "\n")
}

// foldJoin glues short folded lines into one continuous message. An
// unpunctuated boundary gets a comma so the breath still reads ("看完了
// 快去睡觉" + "眼睛都快睁不开了" → "…睡觉，眼睛都…"), while lines that
// already carry their own pause (。？！~ space) or start with a [[marker]]
// join bare so we never inject a stray comma before a send marker.
func foldJoin(lines []string) string {
	var b strings.Builder
	for i, ln := range lines {
		if i > 0 && needsFoldComma(b.String(), ln) {
			b.WriteString("，")
		}
		b.WriteString(ln)
	}
	return b.String()
}

func needsFoldComma(prev, next string) bool {
	pr := []rune(strings.TrimRight(prev, " \t"))
	nr := []rune(strings.TrimLeft(next, " \t"))
	if len(pr) == 0 || len(nr) == 0 {
		return false
	}
	if strings.HasPrefix(next, "[[") {
		return false
	}
	last, first := pr[len(pr)-1], nr[0]
	if strings.ContainsRune("。！？!?…~～—，、；;：:)）】\"'』」", last) {
		return false
	}
	if strings.ContainsRune("。！？!?…~～，、)）】\"'』」", first) {
		return false
	}
	return true
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

// passMarkerPattern matches the [[PASS]] abstain token — the model's
// explicit "saw it, nothing to say" signal. Outputting it alone means
// the turn is dropped silently instead of forcing a filler reply.
var passMarkerPattern = regexp.MustCompile(`(?i)\[\[\s*PASS\s*\]\]`)

// stripPassMarkers removes [[PASS]] tokens from the reply. passed is
// true when the marker was present and nothing else survives — she saw
// the message and deliberately stayed quiet.
func stripPassMarkers(text string) (cleaned string, passed bool) {
	if !passMarkerPattern.MatchString(text) {
		return text, false
	}
	cleaned = strings.TrimSpace(passMarkerPattern.ReplaceAllString(text, ""))
	return cleaned, cleaned == ""
}

// signatureHit reports which foreign-persona signature token leaked
// into the bubble, or "" when the part is clean.
func signatureHit(part string, sigs []string) string {
	for _, s := range sigs {
		if s != "" && strings.Contains(part, s) {
			return s
		}
	}
	return ""
}

// splitMarkerLines splits a reply part into sub-parts so a marker mixed
// with text ("嗯 抱着了\n[[MEME:害羞]]" or "骂你[[MEME:无语]]") becomes its
// own bubble — resolvable markers turn into image sends, unresolvable
// ones get skipped, and neither leaks as literal text. Unknown [[..]]
// scribbles (the model's [[轻笑]]-style action ticks) are deleted in
// place instead of tearing the sentence — a person never splits a
// message mid-word.
func splitMarkerLines(part string) []string {
	var result []string
	var pending strings.Builder
	flush := func() {
		if seg := strings.TrimSpace(pending.String()); seg != "" {
			result = append(result, seg)
		}
		pending.Reset()
	}
	cursor := 0
	for _, span := range inlineMarkerPattern.FindAllStringIndex(part, -1) {
		marker := part[span[0]:span[1]]
		pending.WriteString(part[cursor:span[0]])
		if !isMarkerPart(marker) {
			// Unknown marker — swallow it silently, keep the text whole.
			cursor = span[1]
			continue
		}
		flush()
		result = append(result, marker)
		cursor = span[1]
	}
	pending.WriteString(part[cursor:])
	flush()
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
	// A bare [[QUOTE]] part survives the split so the quote lands on the
	// next text bubble instead of dying inside the marker stripper.
	if quoteMarkerPattern.MatchString(strings.TrimSpace(part)) &&
		strings.TrimSpace(quoteMarkerPattern.ReplaceAllString(part, "")) == "" {
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
// exact tell that makes her read like a bot — so the odds ride her mood
// (moodAdj, roughly ±0.33 at mood ±100): happy she babbles, sulky she
// clips it short. Length only sets the loose base band; replies under
// 80 runes never split and a third bubble is reserved for real essays.
func softSplitReply(reply string, roll, moodAdj float64) []string {
	runes := []rune(reply)
	if len(runes) < 40 {
		return []string{reply}
	}
	var chance float64
	switch {
	case len(runes) < 60:
		chance = 0.25
	case len(runes) < 100:
		chance = 0.50
	case len(runes) < 160:
		chance = 0.70
	default:
		chance = 0.85
	}
	chance += moodAdj
	if chance < 0.05 {
		chance = 0.05
	} else if chance > 0.95 {
		chance = 0.95
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

// enforceShortBubbles hard-caps a single bubble's length for casual chat.
// softSplitReply is probabilistic ("does this read human"); this is the
// floor that keeps a real person's one-thought-per-message rhythm: a
// bubble over maxRunes is chopped at the nearest punctuation/space so a
// reply never arrives as one wall of five stacked intentions. Parts that
// carry a [[...]] marker are left whole — markers must ride their own
// bubble intact. Business/technical replies pass a larger maxRunes.
func enforceShortBubbles(text string, maxRunes int) []string {
	if maxRunes <= 0 {
		return []string{text}
	}
	// Marker-bearing parts (e.g. [[MEME]], [[AT]]) stay whole.
	if strings.Contains(text, "[[") {
		return []string{text}
	}
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return []string{text}
	}
	var parts []string
	for len(runes) > maxRunes {
		cutAt := -1
		// Prefer a sentence boundary in the back half of the window.
		for i := maxRunes; i >= maxRunes/2; i-- {
			if isSentenceEnd(runes[i]) {
				cutAt = i + 1
				break
			}
		}
		if cutAt < 0 {
			for i := maxRunes; i >= maxRunes/2; i-- {
				if runes[i] == ' ' || runes[i] == '　' {
					cutAt = i + 1
					break
				}
			}
		}
		if cutAt < 0 || cutAt > len(runes) {
			// No sentence boundary in reach — send the rest whole. A
			// mid-word guillotine reads robotic; a slightly long bubble
			// just reads like a person typing one thought.
			break
		}
		if part := strings.TrimSpace(string(runes[:cutAt])); part != "" {
			parts = append(parts, part)
		}
		runes = []rune(strings.TrimSpace(string(runes[cutAt:])))
	}
	if tail := strings.TrimSpace(string(runes)); tail != "" {
		parts = append(parts, tail)
	}
	if len(parts) == 0 {
		return []string{text}
	}
	return parts
}

// isSentenceEnd reports whether a rune is a natural bubble-break point —
// the punctuation a person would end a short message on. Mid-sentence
// pauses (commas, 、, closing brackets, quotes) never count: nobody ends
// a QQ message with a dangling comma.
func isSentenceEnd(r rune) bool {
	switch r {
	case '。', '！', '!', '？', '?', '；', ';', '…', '~', '～':
		return true
	}
	return false
}

// stubTailMin is the smallest trailing bubble worth sending — a cut
// that strands fewer runes reads like a mistyped send, not a beat, so
// the reply stays one bubble instead.
const stubTailMin = 15

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
	if best >= 0 && len(runes)-best-1 < stubTailMin {
		return -1
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

// echoSemanticCutoff marks a near-paraphrase of her own recent send.
// Measured on LocalSimilarity: paraphrased repeats land ~0.5-0.8 while
// unrelated-but-topic-adjacent lines stay under ~0.47.
const echoSemanticCutoff = 0.55

// filterSelfEcho drops or trims reply parts that repeat something she
// literally just sent — the "said the same phrase twice in a row" tell.
// Verbatim runs come off via trimRepeatedSpan; parts that paraphrase a
// recent send outright (semantically, not lexically) are dropped too.
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
		if semanticEcho(trimmed, sent) {
			continue
		}
		filtered = append(filtered, trimmed)
	}
	if len(filtered) == 0 && len(parts) > 0 {
		return parts[:1]
	}
	return filtered
}

// semanticEcho reports whether the part is basically her last message
// reworded — the paraphrase-level repeat verbatim trimming can't catch.
// Short lines are exempt: "好的" "嗯嗯" style acknowledgements cluster
// semantically without being a repeat.
func semanticEcho(part string, sent []string) bool {
	if len([]rune(part)) < 8 {
		return false
	}
	for _, text := range sent {
		if len([]rune(text)) < 8 {
			continue
		}
		if vecstore.LocalSimilarity(part, text) >= echoSemanticCutoff {
			return true
		}
	}
	return false
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

// fightPattern marks messages that read as quarrelling — insults,
// dismissals, doubled exclamation/question runs. Several of these in a
// row is an argument, not a lively chat.
var fightPattern = regexp.MustCompile(
	`滚|闭嘴|烦死|杠|有病|吵|喷|孝子|你懂个|懂什么|神经病|无语了|笑死人了|！{2,}|？{2,}`,
)

// groupTension scores how heated the recent group chatter is. Counts
// fight markers across the last stretch of member messages; >=3 reads
// as an argument in progress — the moment a real person goes quiet or
// steps in to smooth things over, not the moment to be chipper.
func groupTension(history []domain.ChatMessage) int {
	score := 0
	seen := 0
	for i := len(history) - 1; i >= 0 && seen < 14; i-- {
		if history[i].Role != "user" {
			continue
		}
		seen++
		content := sentTimestampPattern.ReplaceAllString(history[i].Content, "")
		score += len(fightPattern.FindAllString(content, -1))
	}
	return score
}

// leavePattern spots mid-conversation exits — the user said "later"
// and then actually went quiet, which is what makes the topic hang.
var leavePattern = regexp.MustCompile(
	`先去|待会|回头|晚点|下次聊|先这样|忙去了|我有事|撤了|先下了|一会儿`,
)

// hangingTail looks at the tail of a quiet conversation and returns the
// topic fragment that was left open — she asked something that never
// got answered, or he left mid-topic. Derived at prompt time; nothing
// to store, nothing to expire.
func hangingTail(history []domain.ChatMessage) (string, bool) {
	var lastUser, lastAssistant string
	for i := len(history) - 1; i >= 0 && (lastUser == "" || lastAssistant == ""); i-- {
		content := strings.TrimSpace(
			sentTimestampPattern.ReplaceAllString(history[i].Content, ""),
		)
		if content == "" || len([]rune(content)) < 4 {
			continue
		}
		switch history[i].Role {
		case "user":
			if lastUser == "" {
				lastUser = content
			}
		case "assistant":
			if lastAssistant == "" {
				lastAssistant = content
			}
		}
	}
	hanging := false
	switch {
	case lastAssistant != "" &&
		(strings.HasSuffix(lastAssistant, "？") || strings.HasSuffix(lastAssistant, "?")):
		// she asked, he vanished
		hanging = true
	case lastUser != "" && leavePattern.MatchString(lastUser):
		// he left mid-topic
		hanging = true
	}
	if !hanging {
		return "", false
	}
	frag := lastUser
	if frag == "" {
		frag = lastAssistant
	}
	runes := []rune(frag)
	if len(runes) > 18 {
		runes = runes[:18]
		frag = string(runes) + "…"
	}
	return frag, true
}

// typoChance is the per-reply odds of shipping a bubble with a typo.
// Opt-in via QQ_TYPO_SIM — rare enough to read as a slip, not a quirk.
const typoChance = 0.015

// maybeTypoPart corrupts a random text bubble the way thumbs do —
// swapped neighbours, a doubled character, or a dropped one — then
// appends the self-correction bubble "*正确写法" when the chunk budget
// has room. Without room the typo just flies: people typo constantly
// without fixing, that's human too.
func maybeTypoPart(parts []string, roll float64, room bool) []string {
	if roll >= typoChance {
		return parts
	}
	cands := make([]int, 0, len(parts))
	for i, p := range parts {
		if isMarkerPart(p) {
			continue
		}
		han := 0
		for _, r := range p {
			if unicode.Is(unicode.Han, r) {
				han++
			}
		}
		if han >= 6 {
			cands = append(cands, i)
		}
	}
	if len(cands) == 0 {
		return parts
	}
	idx := cands[rand.Intn(len(cands))]
	runes := []rune(parts[idx])
	pos := rand.Intn(len(runes))
	mutated := make([]rune, 0, len(runes)+1)
	var frag []rune
	switch rand.Intn(3) {
	case 0: // swap neighbours: 今天→天今
		if pos >= len(runes)-1 {
			pos = len(runes) - 2
		}
		if pos < 0 {
			return parts
		}
		mutated = append(mutated, runes[:pos]...)
		mutated = append(mutated, runes[pos+1], runes[pos])
		mutated = append(mutated, runes[pos+2:]...)
		frag = runes[pos:min(pos+2, len(runes))]
	case 1: // doubled char: 的→的的
		mutated = append(mutated, runes[:pos+1]...)
		mutated = append(mutated, runes[pos])
		mutated = append(mutated, runes[pos+1:]...)
		lo := pos - 1
		if lo < 0 {
			lo = 0
		}
		frag = runes[lo:min(pos+2, len(runes))]
	default: // dropped char
		mutated = append(mutated, runes[:pos]...)
		mutated = append(mutated, runes[pos+1:]...)
		lo := pos - 1
		if lo < 0 {
			lo = 0
		}
		frag = runes[lo:min(pos+2, len(runes))]
	}
	out := make([]string, 0, len(parts)+1)
	out = append(out, parts[:idx]...)
	out = append(out, string(mutated))
	if room {
		out = append(out, "*"+string(frag))
	}
	out = append(out, parts[idx+1:]...)
	return out
}
