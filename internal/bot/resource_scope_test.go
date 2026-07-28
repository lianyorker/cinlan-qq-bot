package bot

import (
	"context"
	"testing"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/binding"
	"github.com/lianyorker/cinlan-qq-bot/internal/domain"
	"github.com/lianyorker/cinlan-qq-bot/internal/session"
)

func TestBindingResourceScopesDoNotCrossChats(t *testing.T) {
	cfg := testBotConfig(t)
	agentClient := &fakeAgent{response: domain.AgentResponse{Reply: "answer"}}
	service := New(
		cfg,
		agentClient,
		&fakeSender{},
		session.New(20, time.Hour),
		testLogger(),
	)
	learning := true
	bindings, err := binding.NewRegistry([]binding.Rule{
		{
			Name:            "group",
			Platform:        "*",
			SelfID:          "10001",
			ChatType:        "group",
			ChatID:          "30003",
			Tools:           []string{"group_tool"},
			Skills:          []string{"group_skill"},
			KnowledgeBases:  []string{"group_knowledge"},
			MCPServers:      []string{"group_mcp"},
			LearningEnabled: &learning,
		},
		{
			Name:           "private",
			Platform:       "*",
			SelfID:         "10001",
			ChatType:       "private",
			ChatID:         "20002",
			Tools:          []string{"private_tool"},
			Skills:         []string{"private_skill"},
			KnowledgeBases: []string{"private_knowledge"},
			MCPServers:     []string{"private_mcp"},
		},
	})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	service.SetChatBindings(bindings)

	service.handleEvent(context.Background(), testEvent("group-scope", "group", true))
	service.handleEvent(
		context.Background(),
		testPrivateEvent("private-scope", "private", "20002"),
	)

	if len(agentClient.requests) != 2 {
		t.Fatalf("agent requests = %d", len(agentClient.requests))
	}
	group := agentClient.requests[0]
	private := agentClient.requests[1]
	if !group.RestrictTools ||
		len(group.AllowedTools) != 1 ||
		group.AllowedTools[0] != "group_tool" ||
		len(group.AllowedSkills) != 1 ||
		group.AllowedSkills[0] != "group_skill" ||
		len(group.MCPServers) != 1 ||
		group.MCPServers[0] != "group_mcp" {
		t.Fatalf("group scope = %#v", group)
	}
	if !private.RestrictTools ||
		len(private.AllowedTools) != 1 ||
		private.AllowedTools[0] != "private_tool" ||
		len(private.AllowedSkills) != 1 ||
		private.AllowedSkills[0] != "private_skill" ||
		len(private.MCPServers) != 1 ||
		private.MCPServers[0] != "private_mcp" {
		t.Fatalf("private scope = %#v", private)
	}
}

func TestBindingsFailClosedForUnmatchedActor(t *testing.T) {
	cfg := testBotConfig(t)
	agentClient := &fakeAgent{response: domain.AgentResponse{Reply: "answer"}}
	sender := &fakeSender{}
	service := New(
		cfg,
		agentClient,
		sender,
		session.New(20, time.Hour),
		testLogger(),
	)
	requireMention := true
	bindings, err := binding.NewRegistry([]binding.Rule{{
		Name:           "restricted-user",
		Platform:       "*",
		SelfID:         "10001",
		ChatType:       "group",
		ChatID:         "30003",
		UserIDs:        []string{"20003"},
		RequireMention: &requireMention,
	}})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	service.SetChatBindings(bindings)

	service.handleEvent(
		context.Background(),
		testEventFrom("unbound-user", "question", true, "20002", "tester"),
	)

	if len(agentClient.requests) != 0 {
		t.Fatalf("unmatched actor reached agent: %#v", agentClient.requests)
	}
	if len(sender.messages) != 0 {
		t.Fatalf("unmatched actor received reply: %#v", sender.messages)
	}
}
