package bot

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/lianyorker/cinlan-qq-bot/internal/message"
)

const naturalReplyBreak = "[[NEXT]]"

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
	"偷笑": 21,
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
		marker := text[m[0]:m[1]]
		value := strings.TrimSpace(text[m[2]:m[3]])
		id, err := strconv.Atoi(value)
		if err != nil {
			mapped, ok := faceNameToID[value]
			if !ok {
				continue // leave unknown names as plain text
			}
			id = mapped
		}
		if id < 0 || id > 349 {
			continue
		}
		flush(m[0])
		_ = marker
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

func planReplyParts(reply string) []string {
	reply = strings.TrimSpace(reply)
	if reply == "" {
		return nil
	}
	var raw []string
	for _, segment := range strings.Split(reply, naturalReplyBreak) {
		raw = append(raw, splitOnBlankLines(segment)...)
	}
	parts := make([]string, 0, len(raw))
	for _, segment := range raw {
		if part := strings.TrimSpace(segment); part != "" {
			parts = append(parts, part)
		}
	}
	if len(parts) <= 2 {
		return parts
	}
	return []string{parts[0], strings.Join(parts[1:], "\n")}
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
