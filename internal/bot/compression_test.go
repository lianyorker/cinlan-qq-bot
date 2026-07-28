package bot

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/binding"
	"github.com/lianyorker/cinlan-qq-bot/internal/domain"
	"github.com/lianyorker/cinlan-qq-bot/internal/session"
)

type compressionAgent struct {
	mu       sync.Mutex
	requests []domain.AgentRequest
}

func (a *compressionAgent) Reply(
	_ context.Context,
	request domain.AgentRequest,
) (domain.AgentResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.requests = append(a.requests, request)
	if strings.HasPrefix(request.RequestID, "compress:") {
		return domain.AgentResponse{
			Reply: `{"summary":"已讨论两个问题","memory":"偏好简洁回答"}`,
		}, nil
	}
	return domain.AgentResponse{Reply: "answer"}, nil
}

func TestSessionCompressionAndLearningStayInGroupScope(t *testing.T) {
	cfg := testBotConfig(t)
	cfg.SessionCompressAt = 4
	cfg.SessionRetain = 2
	cfg.SessionLearning = true
	agentClient := &compressionAgent{}
	sessions := session.New(20, time.Hour)
	service := New(cfg, agentClient, &fakeSender{}, sessions, testLogger())
	learning := true
	bindings, err := binding.NewRegistry([]binding.Rule{{
		Name:            "group",
		Platform:        "*",
		SelfID:          "10001",
		ChatType:        "group",
		ChatID:          "30003",
		Persona:         "support",
		LearningEnabled: &learning,
	}})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	service.SetChatBindings(bindings)

	service.handleEvent(context.Background(), testEvent("compress-1", "first", true))
	service.handleEvent(context.Background(), testEvent("compress-2", "second", true))

	snapshot := sessions.SnapshotState("qq-onebot:self:10001:group:30003")
	if len(snapshot.History) != 2 ||
		snapshot.Summary != "已讨论两个问题" ||
		snapshot.Memory != "偏好简洁回答" {
		t.Fatalf("compressed snapshot = %#v", snapshot)
	}
	if len(agentClient.requests) != 3 {
		t.Fatalf("agent requests = %d, want 3", len(agentClient.requests))
	}
	compression := agentClient.requests[2]
	if !compression.RestrictTools ||
		len(compression.AllowedTools) != 0 ||
		compression.SessionID !=
			"qq-onebot:self:10001:group:30003:maintenance:compression" {
		t.Fatalf("compression request scope = %#v", compression)
	}
}
