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

	// The post-compression feeling note lands asynchronously now —
	// feelWorker needs a beat to voice and store it.
	var snapshot session.Snapshot
	deadline := time.Now().Add(2 * time.Second)
	for {
		snapshot = sessions.SnapshotState("qq-onebot:self:10001:group:30003")
		if len(snapshot.History) >= 3 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(snapshot.History) != 3 ||
		snapshot.Summary != "已讨论两个问题" ||
		snapshot.Memory != "偏好简洁回答" {
		t.Fatalf("compressed snapshot = %#v", snapshot)
	}
	last := snapshot.History[len(snapshot.History)-1]
	if !strings.Contains(last.Content, "[心情] ") {
		t.Fatalf("missing post-compression feeling note: %#v", last)
	}
	var compression domain.AgentRequest
	found := false
	for _, req := range agentClient.requests {
		if strings.HasSuffix(req.SessionID, ":maintenance:compression") {
			compression = req
			found = true
		}
	}
	if !found || !compression.RestrictTools ||
		len(compression.AllowedTools) != 0 {
		t.Fatalf("compression request scope = %#v", compression)
	}
}
