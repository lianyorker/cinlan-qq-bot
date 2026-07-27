package onebot

import (
	"encoding/json"

	"github.com/lianyorker/cinlan-qq-bot/internal/message"
)

type ParsedMessage struct {
	Text      string
	Mentioned bool
}

func ParseMessage(raw json.RawMessage, rawMessage, selfID string) ParsedMessage {
	chain, err := message.ParseOneBot(raw, rawMessage, selfID)
	if err != nil {
		return ParsedMessage{}
	}
	text, mentioned := chain.PlainText(selfID)
	return ParsedMessage{
		Text:      text,
		Mentioned: mentioned,
	}
}

func ParseChain(raw json.RawMessage, rawMessage, selfID string) (message.Chain, error) {
	return message.ParseOneBot(raw, rawMessage, selfID)
}
