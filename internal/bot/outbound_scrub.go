package bot

import (
	"regexp"
	"strings"
)

// Outbound text hygiene — one ordered pass over the whole class of model
// scaffolding, not a rule per leaked token.
//
// Every provider we route through (gemini / deepseek / glm / qwen) leaks a
// different flavour of scaffolding into the visible reply: chat-template
// control tokens, XML-ish wrappers the model invented for its own
// annotations (`<recently>…</recently>`), reasoning containers, tool-call
// envelopes, markdown decoration. Patching them one at a time is
// whack-a-mole — the next model invents the next tag. This file handles
// the classes instead, in the order the industry pipelines use:
//
//  1. control tokens        → delete
//  2. reasoning containers  → delete block AND content (it is not chat)
//  3. tool envelopes        → delete block AND content (JSON body included)
//  4. every other tag       → delete the tag, keep the inner text
//  5. meta-commentary lines → delete the line
//  6. markdown              → unwrap the decoration, keep the text
//
// Step 4 is the catch-all that makes step 1-3 additions rare: any tag the
// model invents next is already covered. It is deliberately narrow (the
// tag name must start with an ASCII letter) so prose like "x < 5" or an
// emoticon "<3" survives untouched.
//
// Reference implementations that shaped this ordering:
//   - bandit-agent-framework packages/core-chat/src/sanitize.ts
//   - Stargazer response_postprocessor (strip_orphaned_tags / replace_special_tokens)
//   - goclaw internal/agent/sanitize.go (stripThinkingTags → stripFinalTags)

// controlTokenPattern matches chat-template control tokens that some
// providers leak verbatim into the content channel.
var controlTokenPattern = regexp.MustCompile(
	`<\|[^|<>]{0,40}\|>` + // <|im_start|> <|endoftext|> <|eot_id|>
		`|<<?/?SYS>>` + // llama2 system envelope
		`|\[/?INST\]` + // llama2 instruction envelope
		`|</?s>` + // sentence boundary tokens
		`|</?(?:pad|eos|bos|unk|mask|sep|cls)>` +
		`|\[/?(?:PAD|EOS|BOS|UNK|MASK|SEP|CLS)\]`,
)

// reasoningTagNames are containers whose CONTENT is internal reasoning and
// must never reach the chat — unwrapping them would leak the thinking.
var reasoningTagNames = []string{
	"think", "thinking", "thought", "thoughts",
	"reasoning", "analysis", "scratchpad", "plan", "planning",
	"internal", "reflection", "cot", "chain_of_thought",
}

// toolEnvelopeTagNames are tool-call frames. The body is JSON or a
// parameter dump, so the whole block goes — stripping only the tags would
// publish the payload.
var toolEnvelopeTagNames = []string{
	"tool_call", "tool_calls", "tool_use", "tool_result", "tool_results",
	"function_call", "function_calls", "function_result", "function_results",
	"invoke", "parameter", "antml:invoke", "antml:parameter",
}

var (
	reasoningBlockPattern   = tagBlockPattern(reasoningTagNames)
	reasoningOpenTailPattern = tagOpenTailPattern(reasoningTagNames)
	toolBlockPattern        = tagBlockPattern(toolEnvelopeTagNames)
	toolOpenTailPattern     = tagOpenTailPattern(toolEnvelopeTagNames)
)

// tagBlockPattern matches a closed <name …>…</name> block for any of the
// given names. RE2 has no backreferences, so the closing alternation is
// spelled out from the same list.
func tagBlockPattern(names []string) *regexp.Regexp {
	alt := strings.Join(names, "|")
	return regexp.MustCompile(
		`(?is)<(?:` + alt + `)\b[^<>]*>.*?</(?:` + alt + `)\s*>`,
	)
}

// tagOpenTailPattern matches an unclosed <name …> that runs to the end of
// the text — a model that got cut off mid-block still must not leak it.
func tagOpenTailPattern(names []string) *regexp.Regexp {
	alt := strings.Join(names, "|")
	return regexp.MustCompile(`(?is)<(?:` + alt + `)\b[^<>]*>.*$`)
}

// strayTagPattern is the catch-all: any remaining tag-shaped token. The
// tag name must start with an ASCII letter, so "x < 5" and "<3" are left
// alone. Attributes are bounded to keep the match cheap.
var strayTagPattern = regexp.MustCompile(
	`</?[a-zA-Z][a-zA-Z0-9_.:-]*(?:[ \t][^<>]{0,200})?/?>`,
)

// strayTagWidePattern is the same catch-all for full-width angle brackets
// — CJK IMEs and some models emit ＜recently＞ instead of <recently>.
var strayTagWidePattern = regexp.MustCompile(
	`＜/?[a-zA-Z][a-zA-Z0-9_.:-]*(?:[ \t][^＜＞]{0,200})?/?＞`,
)

// scrubSentinel marks where a tag was removed so the whitespace the tag
// occupied can be reclaimed — otherwise "似的<recently>你说的" becomes
// "似的 你说的" and the stray space reads as a bot tell.
const scrubSentinel = "\x00"

var (
	// A tag wedged between two CJK characters leaves no space behind —
	// Chinese chat does not space words.
	sentinelAfterCJK  = regexp.MustCompile(`([\p{Han}，。！？；：、）」』】])[ \t]*\x00`)
	sentinelBeforeCJK = regexp.MustCompile(`\x00[ \t]*([\p{Han}，。！？；：、「『【])`)
	// Everywhere else the tag collapses to a single space so English
	// words on either side stay separated.
	sentinelAny = regexp.MustCompile(`[ \t]*\x00[ \t]*`)
)

// metaLinePatterns catch whole lines that are model commentary rather than
// chat. Kept narrow on purpose: a false positive deletes real speech.
var metaLinePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^\s*(?:let me think|thinking:|here(?:'s| is) (?:the|my) (?:response|answer)|as an ai\b|i should respond|based on the (?:system prompt|knowledge base)|internal reasoning|step-by-step thinking)`),
	regexp.MustCompile(`^\s*(?:作为一个?(?:AI|人工智能|语言模型)|以下是(?:我的)?(?:回复|回答)|根据(?:系统)?(?:提示词|设定)|内部推理|思考过程[:：])`),
}

// sanitizeOutboundText runs the full outbound hygiene pass and reports
// which rule classes fired, so the caller can log an audit trail.
func sanitizeOutboundText(text string) (string, []string) {
	if text == "" {
		return text, nil
	}
	var hits []string
	note := func(rule string) { hits = append(hits, rule) }

	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	if controlTokenPattern.MatchString(text) {
		text = controlTokenPattern.ReplaceAllString(text, "")
		note("control_token")
	}
	// Reasoning and tool envelopes drop their body, so they must run
	// before the catch-all — otherwise the tags go and the payload stays.
	if reasoningBlockPattern.MatchString(text) {
		text = reasoningBlockPattern.ReplaceAllString(text, "")
		note("reasoning_block")
	}
	if reasoningOpenTailPattern.MatchString(text) {
		text = reasoningOpenTailPattern.ReplaceAllString(text, "")
		note("reasoning_tail")
	}
	if toolBlockPattern.MatchString(text) {
		text = toolBlockPattern.ReplaceAllString(text, "")
		note("tool_block")
	}
	if toolOpenTailPattern.MatchString(text) {
		text = toolOpenTailPattern.ReplaceAllString(text, "")
		note("tool_tail")
	}
	if strayTagPattern.MatchString(text) || strayTagWidePattern.MatchString(text) {
		text = strayTagPattern.ReplaceAllString(text, scrubSentinel)
		text = strayTagWidePattern.ReplaceAllString(text, scrubSentinel)
		text = sentinelAfterCJK.ReplaceAllString(text, "$1")
		text = sentinelBeforeCJK.ReplaceAllString(text, "$1")
		text = sentinelAny.ReplaceAllString(text, " ")
		text = strings.ReplaceAll(text, scrubSentinel, "")
		note("stray_tag")
	}
	if cleaned, dropped := dropMetaLines(text); dropped {
		text = cleaned
		note("meta_line")
	}
	// Markdown last: the passes above may have unwrapped a tag that was
	// hiding a fence or an emphasis run.
	text = scrubMarkdown(text)
	return collapseScrubLines(text), hits
}

// collapseScrubLines drops whitespace-only lines left behind by removed
// tags and trims each surviving line — chat has no blank-line paragraphs.
func collapseScrubLines(text string) string {
	if !strings.Contains(text, "\n") {
		return strings.TrimSpace(text)
	}
	lines := strings.Split(text, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if t := strings.TrimSpace(line); t != "" {
			kept = append(kept, t)
		}
	}
	return strings.Join(kept, "\n")
}

// scrubOutbound applies the hygiene pass and logs which rule classes
// fired, so a new model leak shows up in the log instead of the group.
func (s *Service) scrubOutbound(state *flowState, text string) string {
	cleaned, hits := sanitizeOutboundText(text)
	if len(hits) > 0 {
		s.logger.Info(
			"outbound scaffolding scrubbed",
			"rules", strings.Join(hits, ","),
			"chat_type", state.chatType,
			"chat_id", state.chatID,
		)
	}
	return cleaned
}

// dropMetaLines removes lines that are pure model commentary. A line that
// only becomes empty after the tag passes is dropped too — an empty
// bubble is not chat.
func dropMetaLines(text string) (string, bool) {
	if !strings.Contains(text, "\n") {
		for _, p := range metaLinePatterns {
			if p.MatchString(text) {
				return "", true
			}
		}
		return text, false
	}
	lines := strings.Split(text, "\n")
	kept := make([]string, 0, len(lines))
	dropped := false
	for _, line := range lines {
		meta := false
		for _, p := range metaLinePatterns {
			if p.MatchString(line) {
				meta = true
				break
			}
		}
		if meta {
			dropped = true
			continue
		}
		kept = append(kept, line)
	}
	if !dropped {
		return text, false
	}
	return strings.Join(kept, "\n"), true
}
