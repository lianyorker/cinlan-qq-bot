package bot

import (
	"context"
	"strconv"
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

func TestSmartAttentionRoutesAmbientSupportQuestion(t *testing.T) {
	agentClient := &fakeAgent{reply: func(
		request domain.AgentRequest,
	) (domain.AgentResponse, error) {
		if strings.HasPrefix(request.RequestID, "attention:") {
			return domain.AgentResponse{
				Reply: `{"action":"reply","reason":"业务问题"}`,
			}, nil
		}
		return domain.AgentResponse{Reply: "直接结论"}, nil
	}}
	sender := &fakeSender{}
	service := smartAttentionService(t, agentClient, sender)

	service.handleEvent(context.Background(), testEvent("ambient", "这个错误怎么解决", false))

	if len(agentClient.requests) != 2 {
		t.Fatalf("agent requests = %d, want attention + answer", len(agentClient.requests))
	}
	if !strings.HasSuffix(
		agentClient.requests[0].SessionID,
		":maintenance:attention",
	) || !agentClient.requests[0].RestrictTools {
		t.Fatalf("attention request = %#v", agentClient.requests[0])
	}
	if len(sender.messages) != 1 || sender.messages[0].text != "直接结论" {
		t.Fatalf("sent messages = %#v", sender.messages)
	}
}

func TestSmartAttentionIgnoresChatterAndAcknowledgement(t *testing.T) {
	agentClient := &fakeAgent{response: domain.AgentResponse{Reply: "不应调用"}}
	sender := &fakeSender{}
	service := smartAttentionService(t, agentClient, sender)

	service.handleEvent(context.Background(), testEvent("chatter", "手续费好贵", false))
	service.handleEvent(context.Background(), testEvent("ack", "好的", true))

	if len(agentClient.requests) != 0 || len(sender.messages) != 0 {
		t.Fatalf(
			"ignored messages reached agent: requests=%d messages=%d",
			len(agentClient.requests),
			len(sender.messages),
		)
	}
}

func TestSmartAttentionClassifierCanIgnoreAmbientCandidate(t *testing.T) {
	agentClient := &fakeAgent{response: domain.AgentResponse{
		Reply: `{"action":"ignore","reason":"不属于当前业务"}`,
	}}
	sender := &fakeSender{}
	service := smartAttentionService(t, agentClient, sender)

	service.handleEvent(context.Background(), testEvent("ignore", "这个错误怎么解决", false))

	if len(agentClient.requests) != 1 || len(sender.messages) != 0 {
		t.Fatalf(
			"attention ignore requests=%d messages=%d",
			len(agentClient.requests),
			len(sender.messages),
		)
	}
}

func TestSmartAttentionIgnoresQuestionAddressedToAnotherUser(t *testing.T) {
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
		t.Fatalf("message addressed to another user reached agent")
	}
}

func TestSmartAttentionDirectMentionWinsOverExtraAtComponent(t *testing.T) {
	agentClient := &fakeAgent{reply: func(
		request domain.AgentRequest,
	) (domain.AgentResponse, error) {
		if strings.HasPrefix(request.RequestID, "attention:") {
			return domain.AgentResponse{
				Reply: `{"action":"reply","reason":"support question"}`,
			}, nil
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

	if len(agentClient.requests) != 2 {
		t.Fatalf("agent requests = %d, want attention + answer", len(agentClient.requests))
	}
	if len(sender.messages) != 1 || sender.messages[0].text != "direct answer" {
		t.Fatalf("sent messages = %#v", sender.messages)
	}
}

func TestSmartAttentionDirectTechnicalQuestionBypassesClassifier(t *testing.T) {
	agentClient := &fakeAgent{response: domain.AgentResponse{Reply: "direct answer"}}
	sender := &fakeSender{}
	service := smartAttentionService(t, agentClient, sender)
	profile, ok := service.PersonaRegistry().Get("support")
	if !ok {
		t.Fatal("support persona was not registered")
	}
	profile.Description = "open source community maintainer"
	if err := service.PersonaRegistry().Upsert(profile); err != nil {
		t.Fatalf("Upsert persona: %v", err)
	}

	service.handlePlatformEvent(
		context.Background(),
		platformEvent(
			"direct-technical-question",
			"定时任务好像是redis 我有必要集成MQ吗",
			true,
		),
	)

	if len(agentClient.requests) != 1 ||
		strings.HasPrefix(agentClient.requests[0].RequestID, "attention:") {
		t.Fatalf("agent requests = %#v, want direct answer request", agentClient.requests)
	}
	if len(sender.messages) != 1 || sender.messages[0].text != "direct answer" {
		t.Fatalf("sent messages = %#v", sender.messages)
	}
}

func TestSmartAttentionIgnoresTextualMentionAddressedToAnotherBot(t *testing.T) {
	agentClient := &fakeAgent{response: domain.AgentResponse{Reply: "不应调用"}}
	sender := &fakeSender{}
	service := smartAttentionService(t, agentClient, sender)
	for index, text := range []string{
		"@菜包（GPT-5.5） 群主帅吗？",
		"输出一张美女图@菜包（GPT-5.5）",
	} {
		service.handlePlatformEvent(
			context.Background(),
			platformEvent("textual-at-"+strconv.Itoa(index), text, false),
		)
	}
	if len(agentClient.requests) != 0 || len(sender.messages) != 0 {
		t.Fatalf(
			"textual mentions reached agent: requests=%d messages=%d",
			len(agentClient.requests),
			len(sender.messages),
		)
	}
}

func TestSmartAttentionClassifiesDirectUnrelatedTask(t *testing.T) {
	agentClient := &fakeAgent{response: domain.AgentResponse{
		Reply: `{"action":"ignore","reason":"与客服业务无关"}`,
	}}
	sender := &fakeSender{}
	service := smartAttentionService(t, agentClient, sender)

	service.handlePlatformEvent(
		context.Background(),
		platformEvent("direct-unrelated", "给我写一首诗", true),
	)

	if len(agentClient.requests) != 1 ||
		!strings.HasPrefix(agentClient.requests[0].RequestID, "attention:") ||
		len(sender.messages) != 0 {
		t.Fatalf(
			"direct unrelated task requests=%#v messages=%#v",
			agentClient.requests,
			sender.messages,
		)
	}
}

func TestSmartAttentionDeterministicallyIgnoresGenericCreationTasks(t *testing.T) {
	for index, text := range []string{
		"输出一张美女图",
		"帮我写一个贪吃蛇程序",
		"做个网站",
	} {
		agentClient := &fakeAgent{response: domain.AgentResponse{Reply: "不应调用"}}
		sender := &fakeSender{}
		service := smartAttentionService(t, agentClient, sender)

		service.handlePlatformEvent(
			context.Background(),
			platformEvent("creation-"+strconv.Itoa(index), text, true),
		)

		if len(agentClient.requests) != 0 || len(sender.messages) != 0 {
			t.Fatalf(
				"generic creation %q reached agent: requests=%d messages=%d",
				text,
				len(agentClient.requests),
				len(sender.messages),
			)
		}
	}
}

func TestSourceLocationQuestionIsNotTreatedAsCodeCreation(t *testing.T) {
	if isOutOfScopeCreationTask("订单创建代码在哪") {
		t.Fatal("source location question was treated as code creation")
	}
}

func TestSmartAttentionDoesNotInventAppearanceFacts(t *testing.T) {
	agentClient := &fakeAgent{response: domain.AgentResponse{Reply: "不应调用"}}
	sender := &fakeSender{}
	service := smartAttentionService(t, agentClient, sender)

	service.handlePlatformEvent(
		context.Background(),
		platformEvent("appearance", "群主帅吗？", true),
	)

	if len(agentClient.requests) != 0 ||
		len(sender.messages) != 1 ||
		sender.messages[0].text != "我没看到相关照片或资料，暂时没法判断。" {
		t.Fatalf(
			"appearance requests=%#v messages=%#v",
			agentClient.requests,
			sender.messages,
		)
	}
}

func TestAppearanceRuleAppliesWhenSmartAttentionDisabled(t *testing.T) {
	agentClient := &fakeAgent{response: domain.AgentResponse{Reply: "不应调用"}}
	sender := &fakeSender{}
	service := attentionService(t, agentClient, sender, false)

	service.handlePlatformEvent(
		context.Background(),
		platformEvent("appearance-without-attention", "群主帅不帅", true),
	)

	if len(agentClient.requests) != 0 ||
		len(sender.messages) != 1 ||
		sender.messages[0].text != "我没看到相关照片或资料，暂时没法判断。" {
		t.Fatalf(
			"appearance requests=%#v messages=%#v",
			agentClient.requests,
			sender.messages,
		)
	}
}

func TestMentionedImageReachesMultimodalAgentRequest(t *testing.T) {
	agentClient := &fakeAgent{response: domain.AgentResponse{Reply: "图里是报错信息"}}
	sender := &fakeSender{}
	service := smartAttentionService(t, agentClient, sender)
	event := platform.Event{
		ID:          "qq-native:image",
		Platform:    platform.PlatformQQNative,
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

func TestUnreadableImageReturnsSpecificReply(t *testing.T) {
	agentClient := &fakeAgent{err: agent.ErrInputImageUnavailable}
	sender := &fakeSender{}
	service := smartAttentionService(t, agentClient, sender)
	event := platformEvent("missing-image", "[图片]", true)
	event.Chain = append(event.Chain, message.Image(`D:\missing\image.png`))

	service.handlePlatformEvent(context.Background(), event)

	if len(sender.messages) != 1 ||
		!strings.Contains(sender.messages[0].text, "重新发送原图") {
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

func platformEvent(messageID, text string, mentioned bool) platform.Event {
	chain := make(message.Chain, 0, 2)
	if mentioned {
		chain = append(chain, message.At("10001"))
	}
	chain = append(chain, message.Text(text))
	return platform.Event{
		ID:          "qq-native:" + messageID,
		Platform:    platform.PlatformQQNative,
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
