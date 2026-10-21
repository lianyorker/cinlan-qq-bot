package bot

import (
	"regexp"
	"strconv"
	"strings"

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
}

// parseFaceMarkers splits text into a chain of text/face components.
// Returns the chain, the marker-stripped plain text, and whether any
// marker was consumed. Unknown face names are removed silently.
func parseFaceMarkers(text string) (message.Chain, string, bool) {
	matches := faceMarkerPattern.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return message.Chain{message.Text(text)}, text, false
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
				// Unknown name: still consume the marker so it never leaks
				// into the chat as literal text.
				flush(m[0])
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
	if !found {
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

func planReplyParts(reply string) []string {
	reply = strings.TrimSpace(reply)
	if reply == "" {
		return nil
	}
	parts := make([]string, 0, 4)
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
	if len(parts) <= 2 {
		return parts
	}
	return []string{parts[0], strings.Join(parts[1:], "\n")}
}

// splitMarkerLines splits a reply part into sub-parts so a marker line mixed
// with text ("嗯 抱着了\n[[MEME:害羞]]") still becomes its own bubble instead
// of leaking as literal text.
func splitMarkerLines(part string) []string {
	lines := strings.Split(part, "\n")
	var (
		result []string
		buffer []string
	)
	flush := func() {
		if text := strings.TrimSpace(strings.Join(buffer, "\n")); text != "" {
			result = append(result, text)
		}
		buffer = buffer[:0]
	}
	for _, line := range lines {
		if _, ok := parseMemeMarker(line); ok {
			flush()
			result = append(result, strings.TrimSpace(line))
			continue
		}
		if _, ok := parseStoredImageMarker(line); ok {
			flush()
			result = append(result, strings.TrimSpace(line))
			continue
		}
		buffer = append(buffer, line)
	}
	flush()
	return result
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

func expandReplyParts(parts []string, maxRunes, maxChunks int) []string {
	result := make([]string, 0, min(len(parts), maxChunks))
	for _, part := range parts {
		remaining := maxChunks - len(result)
		if remaining <= 0 {
			break
		}
		result = append(result, splitReply(part, maxRunes, remaining)...)
	}
	return result
}

func isTextReplyChain(chain message.Chain) bool {
	return len(chain) == 1 && chain[0].Type == message.TypeText
}
