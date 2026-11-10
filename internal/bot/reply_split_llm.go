package bot

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lianyorker/cinlan-qq-bot/internal/domain"
)

// LLM semantic reply segmentation — the "send-key position" judge.
//
// [[NEXT]] puts split decisions on the generating model, which keeps
// cutting single thoughts in half. This pass asks a judge model to
// re-segment the finished reply where a real person would hit send,
// then validates fidelity: the segments must reproduce the original
// text verbatim (ignoring whitespace and the [[NEXT]] markers
// themselves) and every [[..]] marker must land inside one segment.
// Any drift — reworded text, a split marker, broken JSON — falls back
// to the mechanical planReplyParts path, so the judge can only ever
// improve a split, never damage a reply.

const replySegTimeout = 8 * time.Second
const replySegMaxParts = 5
const replySegMinRunes = 20

// replySegSystemPrompt teaches the send-key judgement: split where the
// breath changes, never mid-thought, keep marker atoms whole, emit a
// bare JSON string array.
const replySegSystemPrompt = `你是聊天消息的断句器。读一段机器人要发的闲聊回复，按真人按下发送键的位置把它切成若干条消息，输出 JSON 字符串数组。

切点规则：
- 一个意思说完才切——同一个意思的不同部分别拆（"没找到这个号"+"就是个抖音号"是一件事，得在一条里）
- 节奏明显变化可以切（先逗他再哄他、先答再补一句）
- 独立的短反应可以单独成条（"啊？""笑死""哦？")
- 别为拆而拆：整段就讲一件事就返回一条
- 原文里可能有 [[NEXT]]（生成时的初稿切分建议），合理就采纳、不合理就合并
- 其他 [[XXX]] 形式的标记（[[MEME:xx]] [[REACT:xx]] [[OOPS]]x[[/OOPS]] 等）必须整体留在某一条里，不许拆开
- 1-4 条，闲聊一般 1-2 条就够
- 逐字保留原文：只能删除 [[NEXT]] 本身和空白，不许改字、增删内容；切点处的标点可以自然微调（去掉半截逗号、补个停顿），但字词不许动

只输出 JSON 数组，例如 ["第一句","第二句"]，别的什么都别写。`

var llmSegMarkerPattern = regexp.MustCompile(`\[\[[^\[\]]{0,40}\]\]`)

// segFidelityIgnored are the characters fidelity comparison throws
// away — whitespace, punctuation and quotes in both half- and
// full-width forms. Word order is what must survive, not punctuation.
const segFidelityIgnored = " \t\n\r，。！？；：、,.!?;:'\"“”‘’()（）【】《》<>…—~～·|/\\＊*「」『』"

// llmSegmentReply asks the judge model to re-segment the reply and
// returns the segments plus whether they passed fidelity checks. The
// caller must fall back to planReplyParts on false.
func (s *Service) llmSegmentReply(
	ctx context.Context, reply string, maxParts int,
) ([]string, bool) {
	if !s.cfg.LLMReplySegment || maxParts <= 1 {
		return nil, false
	}
	if utf8.RuneCountInString(reply) < replySegMinRunes {
		return nil, false
	}
	if s.providers == nil && s.agent == nil {
		return nil, false
	}
	callCtx, cancel := context.WithTimeout(ctx, replySegTimeout)
	defer cancel()
	response, err := s.judgeReply(callCtx, "", domain.AgentRequest{
		RequestID:     "reply-segment-" + s.now().Format("150405"),
		SessionID:     "maintenance:reply-segment",
		SystemPrompt:  replySegSystemPrompt,
		Text:          reply,
		RestrictTools: true,
	})
	if err != nil {
		s.logger.Debug("reply segment judge failed", "error", err.Error())
		return nil, false
	}
	segments := parseSegJSON(response.Reply)
	if len(segments) == 0 {
		return nil, false
	}
	// Overflow merges into the last segment rather than failing — an
	// over-eager judge still found valid send points.
	if len(segments) > replySegMaxParts {
		segments = append(
			segments[:replySegMaxParts-1],
			strings.Join(segments[replySegMaxParts-1:], " "),
		)
	}
	if !segFidelity(reply, segments) {
		s.logger.Debug("reply segment fidelity failed",
			"reply_len", len(reply), "segments", len(segments))
		return nil, false
	}
	return segments, true
}

// parseSegJSON extracts the first JSON string array from the judge's
// reply — models love wrapping answers in ```json fences or prose.
func parseSegJSON(text string) []string {
	start := strings.Index(text, "[")
	end := strings.LastIndex(text, "]")
	if start < 0 || end <= start {
		return nil
	}
	var raw []string
	if err := json.Unmarshal([]byte(text[start:end+1]), &raw); err != nil {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, seg := range raw {
		// A stray [[NEXT]] inside a segment is the judge echoing the
		// draft markers — strip the token, keep the text.
		seg = strings.ReplaceAll(seg, naturalReplyBreak, " ")
		if seg = strings.TrimSpace(seg); seg != "" {
			out = append(out, seg)
		}
	}
	return out
}

// segFidelity verifies the judge's segments reproduce the original
// reply: joined segments must carry the same word sequence as the
// original (whitespace, punctuation and [[NEXT]] markers ignored — a
// comma added at a join boundary is a feature, not drift), and every
// remaining [[..]] marker must sit entirely inside one segment.
func segFidelity(original string, segments []string) bool {
	norm := func(s string) string {
		s = strings.ReplaceAll(s, naturalReplyBreak, "")
		var b strings.Builder
		for _, r := range s {
			if strings.ContainsRune(segFidelityIgnored, r) {
				continue
			}
			b.WriteRune(r)
		}
		return b.String()
	}
	var joined strings.Builder
	for _, seg := range segments {
		joined.WriteString(seg)
	}
	if norm(joined.String()) != norm(original) {
		return false
	}
	// Marker atoms must survive whole inside a single segment — a
	// marker split across the join would render as broken literal text.
	markers := llmSegMarkerPattern.FindAllString(original, -1)
	for _, m := range markers {
		if m == naturalReplyBreak {
			continue
		}
		inside := false
		for _, seg := range segments {
			if strings.Contains(seg, m) {
				inside = true
				break
			}
		}
		if !inside {
			return false
		}
	}
	return true
}
