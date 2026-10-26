package domain

import "github.com/lianyorker/cinlan-qq-bot/internal/message"

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type AgentRequest struct {
	RequestID  string
	SessionID  string
	Text       string
	MessageID  string
	UserID     string
	Platform   string
	ChatType   string
	ChatID     string
	GroupID    string
	SelfID     string
	SenderName string
	SenderRole string
	// Privileged marks a whitelisted super-user (allowed_users); it is the
	// only gate for admin-tier tools, regardless of group owner/admin role.
	Privileged    bool
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
	// ToolActivity lists the tool calls the agent ran this turn ("用了 X"),
	// so the pipeline can note them into session history — she should
	// remember what she DID, not only what she said.
	ToolActivity []string
}
