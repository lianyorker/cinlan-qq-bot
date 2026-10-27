package bot

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/agent"
	"github.com/lianyorker/cinlan-qq-bot/internal/binding"
	"github.com/lianyorker/cinlan-qq-bot/internal/domain"
	"github.com/lianyorker/cinlan-qq-bot/internal/message"
	"github.com/lianyorker/cinlan-qq-bot/internal/persona"
	"github.com/lianyorker/cinlan-qq-bot/internal/platform"
	"github.com/lianyorker/cinlan-qq-bot/internal/session"
)

func TestAIDecisionRoutesAmbientSupportQuestionWithoutIdentifiers(t *testing.T) {
	agentClient := &fakeAgent{reply: func(request domain.AgentRequest) (domain.AgentResponse, error) {
		if strings.HasPrefix(request.RequestID, "attention:") {
			return attentionResponse("reply", "support", 0.96, "业务问题"), nil
		}
		return domain.AgentResponse{Reply: "直接结论"}, nil
	}}
	sender := &fakeSender{}
	service := smartAttentionService(t, agentClient, sender)

	service.handleEvent(context.Background(), testEvent("ambient", "这个错误怎么解决", false))

	if len(agentClient.requests) != 2 {
		t.Fatalf("agent requests = %d, want attention + answer", len(agentClient.requests))
	}
	request := agentClient.requests[0]
	if !strings.HasPrefix(request.SessionID, "maintenance:attention:") ||
		!request.RestrictTools || len(request.History) != 0 {
		t.Fatalf("attention request = %#v", request)
	}
	if request.MessageID != "" || request.UserID != "" || request.ChatID != "" ||
		request.GroupID != "" || request.SelfID != "" || request.SenderName != "" ||
		strings.Contains(request.SessionID, "30003") ||
		strings.Contains(request.SystemPrompt, "只处理当前测试业务") {
		t.Fatalf("attention request leaked identifiers or answer prompt: %#v", request)
	}
	if len(sender.messages) != 1 || sender.messages[0].text != "直接结论" {
		t.Fatalf("sent messages = %#v", sender.messages)
	}
	stats := service.Stats()
	if stats.AttentionDecisions != 1 || stats.AttentionReplies != 1 {
		t.Fatalf("attention stats = %#v", stats)
	}
}

func TestAIDecisionIgnoresExactAcknowledgementLocally(t *testing.T) {
	agentClient := &fakeAgent{response: domain.AgentResponse{Reply: "不应调用"}}
	sender := &fakeSender{}
	service := smartAttentionService(t, agentClient, sender)

	service.handleEvent(context.Background(), testEvent("ack", "好的", true))

	if len(agentClient.requests) != 0 || len(sender.messages) != 0 ||
		service.Stats().AttentionIgnored != 1 {
		t.Fatalf("acknowledgement reached agent: requests=%d messages=%d stats=%#v", len(agentClient.requests), len(sender.messages), service.Stats())
	}
}

func TestAIDecisionClassifierCanIgnoreCandidate(t *testing.T) {
	agentClient := &fakeAgent{response: attentionResponse(
		"ignore", "out_of_scope", 0.91, "不属于当前业务",
	)}
	sender := &fakeSender{}
	service := smartAttentionService(t, agentClient, sender)

	// Ambient message: the classifier gets to say ignore.
	service.handleEvent(context.Background(), testEvent("ignore", "给我写一首诗", false))

	if len(agentClient.requests) != 1 || len(sender.messages) != 0 {
		t.Fatalf("attention ignore requests=%d messages=%d", len(agentClient.requests), len(sender.messages))
	}
}

func TestAIDecisionIgnoresQuestionAddressedToAnotherUser(t *testing.T) {
	agentClient := &fakeAgent{response: domain.AgentResponse{Reply: "不应调用"}}
	sender := &fakeSender{}
	service := smartAttentionService(t, agentClient, sender)
	event := platform.Event{
		ID:          "qq-onebot:other-user",
		Platform:    platform.PlatformQQOneBot,
		PostType:    "message",
		MessageType: platform.ChatGroup,
		MessageID:   "other-user",
		SelfID:      "10001",
		UserID:      "20002",
		ChatID:      "30003",
		SenderName:  "tester",
		Chain: message.Chain{
			message.At("99999"),
			message.Text("这个配置是不是要打开"),
		},
	}

	service.handlePlatformEvent(context.Background(), event)

	if len(agentClient.requests) != 0 || len(sender.messages) != 0 {
		t.Fatal("message addressed to another user reached agent")
	}
}

func TestAIDecisionDirectMentionWinsOverExtraAtComponent(t *testing.T) {
	agentClient := &fakeAgent{reply: func(request domain.AgentRequest) (domain.AgentResponse, error) {
		if strings.HasPrefix(request.RequestID, "attention:") {
			return attentionResponse("reply", "support", 0.93, "support question"), nil
		}
		return domain.AgentResponse{Reply: "direct answer"}, nil
	}}
	sender := &fakeSender{}
	service := smartAttentionService(t, agentClient, sender)
	event := platformEvent("direct-with-extra-at", "support?", true)
	event.Chain = append(
		event.Chain[:1],
		append(message.Chain{message.At("99999")}, event.Chain[1:]...)...,
	)

	service.handlePlatformEvent(context.Background(), event)

	// Direct mention bypasses the classifier — one answer call only.
	if len(agentClient.requests) != 1 || len(sender.messages) != 1 ||
		sender.messages[0].text != "direct answer" {
		t.Fatalf("requests=%#v messages=%#v", agentClient.requests, sender.messages)
	}
}

func TestAIDecisionDoesNotHardCodeCreationTasks(t *testing.T) {
	agentClient := &fakeAgent{reply: func(request domain.AgentRequest) (domain.AgentResponse, error) {
		if strings.HasPrefix(request.RequestID, "attention:") {
			return attentionResponse("reply", "support", 0.9, "在当前角色范围内"), nil
		}
		return domain.AgentResponse{Reply: "可以实现"}, nil
	}}
	sender := &fakeSender{}
	service := smartAttentionService(t, agentClient, sender)

	// Ambient message: the classifier decides creation tasks are in scope.
	service.handlePlatformEvent(
		context.Background(),
		platformEvent("creation", "帮我写一个业务脚本", false),
	)

	if len(agentClient.requests) != 2 || len(sender.messages) != 1 {
		t.Fatalf("creation task was hard-coded away: requests=%#v messages=%#v", agentClient.requests, sender.messages)
	}
}

func TestAIDecisionLowConfidenceUsesSafeFallback(t *testing.T) {
	agentClient := &fakeAgent{response: attentionResponse(
		"reply", "uncertain", 0.4, "信息不足",
	)}
	sender := &fakeSender{}
	service := smartAttentionService(t, agentClient, sender)

	// Ambient message: low-confidence classification fails closed.
	service.handlePlatformEvent(context.Background(), platformEvent("uncertain", "看看这个", false))

	if len(agentClient.requests) != 1 || len(sender.messages) != 0 {
		t.Fatalf("low-confidence decision did not fail closed: requests=%#v messages=%#v", agentClient.requests, sender.messages)
	}
}

func TestAIDecisionErrorCanFallBackToDirectMention(t *testing.T) {
	policy := binding.ReplyPolicy{
		Mode:                binding.ReplyModeAIDecide,
		OnError:             binding.ReplyOnErrorMentionOnly,
		ConfidenceThreshold: 0.7,
	}
	agentClient := &fakeAgent{reply: func(request domain.AgentRequest) (domain.AgentResponse, error) {
		if strings.HasPrefix(request.RequestID, "attention:") {
			return domain.AgentResponse{}, errors.New("router unavailable")
		}
		return domain.AgentResponse{Reply: "fallback answer"}, nil
	}}
	sender := &fakeSender{}
	service := replyPolicyService(t, agentClient, sender, platform.ChatGroup, "30003", policy)

	// Ambient (non-mention) message: the classifier error falls back to the
	// mention_only policy, which ignores it.
	service.handlePlatformEvent(context.Background(), platformEvent("fallback", "请处理", false))

	if len(agentClient.requests) != 1 || len(sender.messages) != 0 ||
		service.Stats().AttentionErrors != 1 {
		t.Fatalf("fallback requests=%#v messages=%#v stats=%#v", agentClient.requests, sender.messages, service.Stats())
	}

	// A direct mention bypasses the classifier entirely and still answers.
	service.handlePlatformEvent(context.Background(), platformEvent("fallback-2", "请处理", true))
	if len(agentClient.requests) != 2 || len(sender.messages) != 1 {
		t.Fatalf("mention bypass requests=%#v messages=%#v", agentClient.requests, sender.messages)
	}
}

func TestPrivateAIDecisionSupportsReplyAndIgnore(t *testing.T) {
	for _, test := range []struct {
		name         string
		action       string
		wantRequests int
		wantMessages int
	}{
		{name: "reply", action: "reply", wantRequests: 2, wantMessages: 1},
		{name: "ignore", action: "ignore", wantRequests: 1, wantMessages: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			agentClient := &fakeAgent{reply: func(request domain.AgentRequest) (domain.AgentResponse, error) {
				if strings.HasPrefix(request.RequestID, "attention:") {
					category := "support"
					if test.action == "ignore" {
						category = "chatter"
					}
					return attentionResponse(test.action, category, 0.9, "private decision"), nil
				}
				return domain.AgentResponse{Reply: "private answer"}, nil
			}}
			sender := &fakeSender{}
			service := replyPolicyService(t, agentClient, sender, platform.ChatPrivate, "20002", binding.ReplyPolicy{Mode: binding.ReplyModeAIDecide})

			service.handleEvent(context.Background(), testPrivateEvent("private-policy", "在吗", "20002"))

			if len(agentClient.requests) != test.wantRequests || len(sender.messages) != test.wantMessages {
				t.Fatalf("requests=%#v messages=%#v", agentClient.requests, sender.messages)
			}
		})
	}
}

func TestAttentionHistoryDefaultsToNoneAndAnonymizesOptIn(t *testing.T) {
	history := []domain.ChatMessage{
		{Role: "user", Content: "[Alice (12345)]: first"},
		{Role: "assistant", Content: "answer"},
		{Role: "user", Content: "[Bob (67890)]: second"},
	}
	if got := attentionHistory(history, binding.AttentionHistoryPolicy{Mode: binding.AttentionHistoryNone}); got != nil {
		t.Fatalf("default history = %#v, want nil", got)
	}
	got := attentionHistory(history, binding.AttentionHistoryPolicy{Mode: binding.AttentionHistoryLastN, Limit: 2})
	if len(got) != 2 || got[1].Content != "[previous participant]: second" ||
		strings.Contains(got[1].Content, "67890") {
		t.Fatalf("anonymized history = %#v", got)
	}
}

func TestAttentionDecisionRequiresStrictSchema(t *testing.T) {
	valid := attentionResponse("reply", "support", 0.9, "needed").Reply
	if decision, err := decodeAttentionDecision(valid); err != nil || decision.Action != "reply" {
		t.Fatalf("valid decision = %#v, %v", decision, err)
	}
	for _, fields := range []map[string]any{
		{"action": "reply", "reason": "missing fields"},
		{"action": "reply", "category": "unknown", "confidence": 0.9, "reason": "x"},
		{"action": "reply", "category": "support", "confidence": 1.1, "reason": "x"},
	} {
		invalid, _ := json.Marshal(fields)
		if _, err := decodeAttentionDecision(string(invalid)); err == nil {
			t.Fatalf("invalid decision accepted: %s", invalid)
		}
	}
}

func TestAttentionRateLimitStopsAdditionalRouterCalls(t *testing.T) {
	agentClient := &fakeAgent{response: attentionResponse(
		"ignore", "chatter", 0.9, "not needed",
	)}
	sender := &fakeSender{}
	service := smartAttentionService(t, agentClient, sender)
	service.cfg.AttentionRateLimit = 1
	service.cfg.AttentionRateWindow = time.Minute
	service.now = func() time.Time { return time.Unix(100, 0) }

	service.handlePlatformEvent(context.Background(), platformEvent("rate-1", "first?", false))
	service.handlePlatformEvent(context.Background(), platformEvent("rate-2", "second?", false))

	if len(agentClient.requests) != 1 || service.Stats().AttentionRateLimited != 1 {
		t.Fatalf("requests=%#v stats=%#v", agentClient.requests, service.Stats())
	}
}

func TestAIDecisionIgnoresTextualMentionAddressedToAnotherBot(t *testing.T) {
	agentClient := &fakeAgent{response: domain.AgentResponse{Reply: "不应调用"}}
	sender := &fakeSender{}
	service := smartAttentionService(t, agentClient, sender)
	service.handlePlatformEvent(
		context.Background(),
		platformEvent("textual-at", "@另一个机器人 请处理", false),
	)
	if len(agentClient.requests) != 0 || len(sender.messages) != 0 {
		t.Fatalf("textual mention reached agent: requests=%d messages=%d", len(agentClient.requests), len(sender.messages))
	}
}

func attentionResponse(action, category string, confidence float64, reason string) domain.AgentResponse {
	payload, _ := json.Marshal(map[string]any{
		"action": action, "category": category, "confidence": confidence, "reason": reason,
	})
	return domain.AgentResponse{Reply: string(payload)}
}

func replyPolicyService(
	t *testing.T,
	agentClient *fakeAgent,
	sender *fakeSender,
	chatType, chatID string,
	policy binding.ReplyPolicy,
) *Service {
	t.Helper()
	cfg := testBotConfig(t)
	service := New(cfg, agentClient, sender, session.New(10, time.Hour), testLogger())
	if err := service.PersonaRegistry().Register(persona.Profile{
		Name:         "support",
		Description:  "当前测试业务",
		SystemPrompt: "只处理当前测试业务。",
		RoutingScope: &persona.RoutingScope{Description: "当前测试业务"},
	}); err != nil {
		t.Fatalf("Register persona: %v", err)
	}
	bindings, err := binding.NewRegistry([]binding.Rule{{
		Name:        "policy",
		Platform:    "*",
		SelfID:      "10001",
		ChatType:    chatType,
		ChatID:      chatID,
		Persona:     "support",
		ReplyPolicy: &policy,
	}})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	service.SetChatBindings(bindings)
	return service
}

func TestMentionedImageReachesMultimodalAgentRequest(t *testing.T) {
	agentClient := &fakeAgent{response: domain.AgentResponse{Reply: "图里是报错信息"}}
	sender := &fakeSender{}
	service := smartAttentionService(t, agentClient, sender)
	event := platform.Event{
		ID:          "qq-onebot:image",
		Platform:    platform.PlatformQQOneBot,
		PostType:    "message",
		MessageType: platform.ChatGroup,
		MessageID:   "image",
		SelfID:      "10001",
		UserID:      "20002",
		ChatID:      "30003",
		SenderName:  "tester",
		Chain: message.Chain{
			message.At("10001"),
			message.Attachment(
				message.TypeImage,
				map[string]any{"filePath": `D:\cache\error.png`},
			),
		},
	}

	service.handlePlatformEvent(context.Background(), event)

	if len(agentClient.requests) != 1 ||
		len(agentClient.requests[0].Chain.ImageReferences()) != 1 {
		t.Fatalf("agent requests = %#v", agentClient.requests)
	}
}

func TestOwnerUserGetsPrivilegedAndCreatorNoteGlobally(t *testing.T) {
	cfg := testBotConfig(t)
	cfg.OwnerUserIDs = []string{"77777"}
	agentClient := &fakeAgent{response: domain.AgentResponse{Reply: "在呢"}}
	sender := &fakeSender{}
	service := New(cfg, agentClient, sender, session.New(10, time.Hour), testLogger())

	// The binding carries no allowed_users — owner status alone must
	// grant privileged treatment and tell the model who he is.
	event := testEventFrom("owner-1", "在吗", true, "77777", "owner")
	service.handleEvent(context.Background(), event)

	if len(agentClient.requests) != 1 {
		t.Fatalf("owner message dropped: %#v", agentClient.requests)
	}
	prompt := agentClient.requests[0].SystemPrompt
	if !strings.Contains(prompt, "把你做出来的人") ||
		!strings.Contains(prompt, "熟人") {
		t.Fatalf("owner notes missing from prompt: %s", prompt)
	}
}

func TestUnreadableImageReturnsSpecificReply(t *testing.T) {
	agentClient := &fakeAgent{err: agent.ErrInputImageUnavailable}
	sender := &fakeSender{}
	service := smartAttentionService(t, agentClient, sender)
	event := platformEvent("missing-image", "[图片]", true)
	event.Chain = append(event.Chain, message.Image(`D:\missing\image.png`))

	service.handlePlatformEvent(context.Background(), event)

	if len(sender.messages) != 1 ||
		!strings.Contains(sender.messages[0].text, "原图重发") {
		t.Fatalf("sender messages = %#v", sender.messages)
	}
}

func TestRunPlatformBatchesConsecutiveMessagesFromSameUser(t *testing.T) {
	cfg := testBotConfig(t)
	cfg.GroupBatchWindow = 20 * time.Millisecond
	agentClient := &fakeAgent{response: domain.AgentResponse{Reply: "合并回答"}}
	sender := &fakeSender{}
	service := New(cfg, agentClient, sender, session.New(10, time.Hour), testLogger())
	events := make(chan platform.Event, 2)
	done := make(chan struct{})
	go func() {
		service.RunPlatform(context.Background(), events)
		close(done)
	}()
	events <- platformEvent("batch-1", "第一段", true)
	events <- platformEvent("batch-2", "补充信息", false)
	close(events)
	<-done

	if len(agentClient.requests) != 1 ||
		!strings.Contains(agentClient.requests[0].Text, "第一段\n补充信息") {
		t.Fatalf("agent requests = %#v", agentClient.requests)
	}
	if len(sender.messages) != 1 || sender.messages[0].replyTo != "batch-2" {
		t.Fatalf("sender messages = %#v", sender.messages)
	}
	if service.Stats().Received != 2 {
		t.Fatalf("Received = %d, want 2", service.Stats().Received)
	}
}

func TestInFlightMessagesCoalesceIntoOneFollowUpTurn(t *testing.T) {
	cfg := testBotConfig(t)
	cfg.GroupBatchWindow = 20 * time.Millisecond
	cfg.PrivateBatchWindow = 20 * time.Millisecond
	cfg.ReplyPartDelay = 0
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var calls int
	agentClient := &fakeAgent{reply: func(domain.AgentRequest) (domain.AgentResponse, error) {
		calls++
		if calls == 1 {
			entered <- struct{}{}
			<-release
		}
		return domain.AgentResponse{Reply: "好"}, nil
	}}
	sender := &fakeSender{}
	service := New(cfg, agentClient, sender, session.New(10, time.Hour), testLogger())
	events := make(chan platform.Event, 4)
	runDone := make(chan struct{})
	go func() {
		service.RunPlatform(context.Background(), events)
		close(runDone)
	}()

	events <- platformEvent("c1", "第一段", true)
	<-entered // turn 1 is now blocked inside the agent

	// These two arrive while she's still replying — they must fold into a
	// single follow-up turn, not spawn two more serialized turns.
	events <- platformEvent("c2", "第二段", true)
	events <- platformEvent("c3", "第三段", true)
	time.Sleep(60 * time.Millisecond) // let the worker merge them into pending

	close(release) // turn 1 finishes → done signal flushes the coalesced turn
	close(events)
	<-runDone

	if len(agentClient.requests) != 2 {
		t.Fatalf("agent requests = %d, want 2 (turn1 + one coalesced follow-up)", len(agentClient.requests))
	}
	followUp := agentClient.requests[1].Text
	if !strings.Contains(followUp, "第二段") || !strings.Contains(followUp, "第三段") {
		t.Fatalf("follow-up turn did not coalesce both messages: %q", followUp)
	}
}

func TestDisallowedGroupDoesNotEnterBatchWindow(t *testing.T) {
	cfg := testBotConfig(t)
	cfg.GroupBatchWindow = time.Second
	service := New(cfg, &fakeAgent{}, &fakeSender{}, session.New(10, time.Hour), testLogger())
	event := platformEvent("outside-group", "这个错误怎么解决", false)
	event.ChatID = "99999"

	if service.shouldBatchPlatformEvent(event) {
		t.Fatal("disallowed group entered the batch window")
	}
}

func TestNaturalReplyPlanSendsTwoMessagesWithoutRepeatedDecoration(t *testing.T) {
	cfg := testBotConfig(t)
	cfg.ReplyPartDelay = 0
	agentClient := &fakeAgent{
		response: domain.AgentResponse{Reply: "先看完整报错。[[NEXT]]再确认渠道配置。"},
	}
	sender := &fakeSender{}
	service := New(cfg, agentClient, sender, session.New(10, time.Hour), testLogger())

	service.handleEvent(context.Background(), testEvent("split", "怎么处理", true))

	if len(sender.messages) != 2 ||
		sender.messages[0].text != "先看完整报错。" ||
		sender.messages[1].text != "再确认渠道配置。" {
		t.Fatalf("sender messages = %#v", sender.messages)
	}
	if !sender.messages[0].quote || sender.messages[1].quote ||
		sender.messages[0].atUserID != "" || sender.messages[1].atUserID != "" {
		t.Fatalf("reply decoration = %#v", sender.messages)
	}
}

func smartAttentionService(
	t *testing.T,
	agentClient *fakeAgent,
	sender *fakeSender,
) *Service {
	return attentionService(t, agentClient, sender, true)
}

func attentionService(
	t *testing.T,
	agentClient *fakeAgent,
	sender *fakeSender,
	smartAttention bool,
) *Service {
	t.Helper()
	cfg := testBotConfig(t)
	service := New(cfg, agentClient, sender, session.New(10, time.Hour), testLogger())
	if err := service.PersonaRegistry().Register(persona.Profile{
		Name:         "support",
		SystemPrompt: "只处理当前测试业务。",
	}); err != nil {
		t.Fatalf("Register persona: %v", err)
	}
	bindings, err := binding.NewRegistry([]binding.Rule{{
		Name:           "smart-group",
		Platform:       "*",
		SelfID:         "10001",
		ChatType:       platform.ChatGroup,
		ChatID:         "30003",
		Persona:        "support",
		SmartAttention: &smartAttention,
	}})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	service.SetChatBindings(bindings)
	return service
}

func platformEventFrom(messageID, text string, mentioned bool, userID string) platform.Event {
	event := platformEvent(messageID, text, mentioned)
	event.UserID = userID
	return event
}

func TestEchoChainJoinsWhenMoodRollPasses(t *testing.T) {
	agentClient := &fakeAgent{reply: func(request domain.AgentRequest) (domain.AgentResponse, error) {
		if strings.HasPrefix(request.RequestID, "attention:") {
			return attentionResponse("ignore", "chatter", 0.9, "复读接龙"), nil
		}
		return domain.AgentResponse{Reply: "不知道，需要佬们科研一下"}, nil
	}}
	sender := &fakeSender{}
	service := smartAttentionService(t, agentClient, sender)
	service.echoRoll = func() int { return 0 } // mood says she tags along

	service.handlePlatformEvent(context.Background(),
		platformEventFrom("echo-1", "不知道，需要佬们科研一下", false, "u1"))
	service.handlePlatformEvent(context.Background(),
		platformEventFrom("echo-2", "不知道，需要佬们科研一下", false, "u2"))

	// First echo went through the judge and got ignored; the second forms
	// the chain — she joins straight into the answer without a judge call.
	if len(agentClient.requests) != 2 {
		t.Fatalf("agent requests = %d, want judge + joined answer",
			len(agentClient.requests))
	}
	answer := agentClient.requests[1].Text
	if !strings.Contains(answer, "复读同一句话接龙") {
		t.Fatalf("join annotation missing from prompt: %q", answer)
	}
	if len(sender.messages) != 1 ||
		sender.messages[0].text != "不知道，需要佬们科研一下" {
		t.Fatalf("sender messages = %#v", sender.messages)
	}
}

func TestEchoChainLurksWhenMoodRollFails(t *testing.T) {
	agentClient := &fakeAgent{reply: func(request domain.AgentRequest) (domain.AgentResponse, error) {
		if strings.HasPrefix(request.RequestID, "attention:") {
			return attentionResponse("ignore", "chatter", 0.9, "复读接龙"), nil
		}
		return domain.AgentResponse{Reply: "不知道，需要佬们科研一下"}, nil
	}}
	sender := &fakeSender{}
	service := smartAttentionService(t, agentClient, sender)
	service.echoRoll = func() int { return 99 } // not feeling it — she lurks

	service.handlePlatformEvent(context.Background(),
		platformEventFrom("echo-1", "不知道，需要佬们科研一下", false, "u1"))
	service.handlePlatformEvent(context.Background(),
		platformEventFrom("echo-2", "不知道，需要佬们科研一下", false, "u2"))

	// Both echoes hit the judge, both ignored — nobody answered.
	if len(sender.messages) != 0 {
		t.Fatalf("she should lurk, sender messages = %#v", sender.messages)
	}
	if len(agentClient.requests) != 2 ||
		!strings.HasPrefix(agentClient.requests[0].RequestID, "attention:") ||
		!strings.HasPrefix(agentClient.requests[1].RequestID, "attention:") {
		t.Fatalf("both should be judge calls: %#v", agentClient.requests)
	}
}

func platformEvent(messageID, text string, mentioned bool) platform.Event {
	chain := make(message.Chain, 0, 2)
	if mentioned {
		chain = append(chain, message.At("10001"))
	}
	chain = append(chain, message.Text(text))
	return platform.Event{
		ID:          "qq-onebot:" + messageID,
		Platform:    platform.PlatformQQOneBot,
		PostType:    "message",
		MessageType: platform.ChatGroup,
		MessageID:   messageID,
		SelfID:      "10001",
		UserID:      "20002",
		ChatID:      "30003",
		SenderName:  "tester",
		Chain:       chain,
	}
}
