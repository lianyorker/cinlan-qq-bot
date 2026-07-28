package domain

import "github.com/lianyorker/cinlan-qq-bot/internal/message"

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type AgentRequest struct {
	RequestID     string
	SessionID     string
	Text          string
	MessageID     string
	UserID        string
	Platform      string
	ChatType      string
	ChatID        string
	GroupID       string
	SelfID        string
	SenderName    string
	SenderRole    string
	History       []ChatMessage
	PromptContext string
	SystemPrompt  string
	AllowedTools  []string
	AllowedSkills []string
	MCPServers    []string
	RestrictTools bool
	Chain         message.Chain
}

type AgentResponse struct {
	Reply   string
	Chain   message.Chain
	Handoff bool
}
