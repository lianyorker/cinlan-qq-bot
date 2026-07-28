package bot

import (
	"strings"

	"github.com/lianyorker/cinlan-qq-bot/internal/message"
)

const naturalReplyBreak = "[[NEXT]]"

func planReplyParts(reply string) []string {
	reply = strings.TrimSpace(reply)
	if reply == "" {
		return nil
	}
	rawParts := strings.Split(reply, naturalReplyBreak)
	parts := make([]string, 0, min(len(rawParts), 2))
	for _, raw := range rawParts {
		if part := strings.TrimSpace(raw); part != "" {
			parts = append(parts, part)
		}
	}
	if len(parts) <= 2 {
		return parts
	}
	return []string{parts[0], strings.Join(parts[1:], "\n")}
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
