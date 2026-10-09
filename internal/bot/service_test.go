package bot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/binding"
	"github.com/lianyorker/cinlan-qq-bot/internal/command"
	"github.com/lianyorker/cinlan-qq-bot/internal/config"
	"github.com/lianyorker/cinlan-qq-bot/internal/domain"
	"github.com/lianyorker/cinlan-qq-bot/internal/message"
	"github.com/lianyorker/cinlan-qq-bot/internal/onebot"
	"github.com/lianyorker/cinlan-qq-bot/internal/persona"
	"github.com/lianyorker/cinlan-qq-bot/internal/platform"
	"github.com/lianyorker/cinlan-qq-bot/internal/plugin"
	"github.com/lianyorker/cinlan-qq-bot/internal/provider"
	"github.com/lianyorker/cinlan-qq-bot/internal/session"
)

type fakeAgent struct {
	mu       sync.Mutex
	requests []domain.AgentRequest
	response domain.AgentResponse
	err      error
	reply    func(domain.AgentRequest) (domain.AgentResponse, error)
}

func (f *fakeAgent) Reply(_ context.Context, request domain.AgentRequest) (domain.AgentResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, request)
	if f.reply != nil {
		return f.reply(request)
	}
	return f.response, f.err
}

type sentMessage struct {
	chatType string
	chatID   string
	selfID   string
	replyTo  string
	text     string
	atUserID string
	atName   string
	quote    bool
}

type fakeSender struct {
	mu       sync.Mutex
	messages []sentMessage
	err      error
}

func (f *fakeSender) Send(_ context.Context, outbound platform.Outbound) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	var text string
	var atUserID string
	var atName string
	for _, component := range outbound.Chain {
		switch component.Type {
		case message.TypeText:
			text += component.Data["text"].(string)
		case message.TypeAt:
			atUserID, _ = component.Data["qq"].(string)
			atName, _ = component.Data["name"].(string)
		}
	}
	f.messages = append(f.messages, sentMessage{
		chatType: outbound.ChatType,
		chatID:   outbound.ChatID,
		selfID:   outbound.SelfID,
		replyTo:  outbound.ReplyTo,
		text:     text,
		atUserID: atUserID,
		atName:   atName,
		quote:    outbound.Quote,
	})
	return f.err
}

func TestHandleMentionedGroupMessageAndHistory(t *testing.T) {
	cfg := testBotConfig(t)
	agentClient := &fakeAgent{response: domain.AgentResponse{Reply: "客服答案"}}
	sender := &fakeSender{}
	service := New(cfg, agentClient, sender, session.New(10, time.Hour), testLogger())

	service.handleEvent(context.Background(), testEvent("1", "第一个问题", true))
	service.handleEvent(context.Background(), testEvent("2", "第二个问题", true))

	if len(agentClient.requests) != 2 {
		t.Fatalf("agent calls = %d, want 2", len(agentClient.requests))
	}
	if got := agentClient.requests[1].History; len(got) != 2 ||
		got[0].Content != "[tester (20002)]: 第一个问题" ||
		got[1].Content != "客服答案" {
		t.Fatalf("second request history = %#v", got)
	}
	if agentClient.requests[0].Text != "[tester (20002)]: 第一个问题" {
		t.Fatalf("first request text = %q", agentClient.requests[0].Text)
	}
	if len(sender.messages) != 2 || !sender.messages[0].quote {
		t.Fatalf("sent messages = %#v", sender.messages)
	}
	if sender.messages[0].atUserID != "" || sender.messages[0].atName != "" {
		t.Fatalf("quoted reply also mentioned sender = %#v", sender.messages[0])
	}
}

func TestGroupContextIsSharedAndKeepsSenderIdentity(t *testing.T) {
	cfg := testBotConfig(t)
	agentClient := &fakeAgent{response: domain.AgentResponse{Reply: "answer"}}
	service := New(cfg, agentClient, &fakeSender{}, session.New(10, time.Hour), testLogger())

	service.handleEvent(context.Background(), testEventFrom("1", "first", true, "20002", "alice"))
	service.handleEvent(context.Background(), testEventFrom("2", "second", true, "20003", "bob"))

	if len(agentClient.requests) != 2 {
		t.Fatalf("agent calls = %d, want 2", len(agentClient.requests))
	}
	second := agentClient.requests[1]
	if second.SessionID != "qq-onebot:self:10001:group:30003" {
		t.Fatalf("session ID = %q", second.SessionID)
	}
	if second.Text != "[bob (20003)]: second" {
		t.Fatalf("second text = %q", second.Text)
	}
	if len(second.History) != 2 || second.History[0].Content != "[alice (20002)]: first" {
		t.Fatalf("shared history = %#v", second.History)
	}
}

func TestRequiresMention(t *testing.T) {
	cfg := testBotConfig(t)
	agentClient := &fakeAgent{response: domain.AgentResponse{Reply: "answer"}}
	sender := &fakeSender{}
	service := New(cfg, agentClient, sender, session.New(10, time.Hour), testLogger())

	service.handleEvent(context.Background(), testEvent("1", "普通群消息", false))

	if len(agentClient.requests) != 0 || len(sender.messages) != 0 {
		t.Fatalf("unmentioned message reached bot")
	}
	if service.Stats().Ignored != 1 {
		t.Fatalf("Ignored = %d, want 1", service.Stats().Ignored)
	}
}

func TestIgnoresAutomatedSenders(t *testing.T) {
	cfg := testBotConfig(t)
	agentClient := &fakeAgent{response: domain.AgentResponse{Reply: "answer"}}
	sender := &fakeSender{}
	service := New(cfg, agentClient, sender, session.New(10, time.Hour), testLogger())

	service.handleEvent(
		context.Background(),
		testEventFrom("auto-id", "hello", true, "2854196310", "assistant"),
	)
	service.handleEvent(
		context.Background(),
		testEventFrom("auto-name", "hello", true, "12345", "Q\u7fa4\u7ba1\u5bb6"),
	)
	service.handleEvent(
		context.Background(),
		testEventFrom("system", "hello", true, "0", "system"),
	)

	if len(agentClient.requests) != 0 || len(sender.messages) != 0 {
		t.Fatalf("automated sender reached bot")
	}
	if service.Stats().Ignored != 3 {
		t.Fatalf("Ignored = %d, want 3", service.Stats().Ignored)
	}
}

func TestFailedSendDoesNotEnterHistory(t *testing.T) {
	cfg := testBotConfig(t)
	agentClient := &fakeAgent{response: domain.AgentResponse{Reply: "answer"}}
	sender := &fakeSender{err: errors.New("send failed")}
	sessions := session.New(10, time.Hour)
	service := New(cfg, agentClient, sender, sessions, testLogger())

	service.handleEvent(context.Background(), testEvent("send-failed", "hello", true))

	snapshot := sessions.SnapshotState("qq-onebot:self:10001:group:30003")
	if len(snapshot.History) != 0 {
		t.Fatalf("failed reply entered history: %#v", snapshot.History)
	}
	if service.Stats().SendErrors != 1 {
		t.Fatalf("SendErrors = %d, want 1", service.Stats().SendErrors)
	}
}

func TestBindingRemovesOutboundLinks(t *testing.T) {
	cfg := testBotConfig(t)
	agentClient := &fakeAgent{
		response: domain.AgentResponse{
			Reply: "See https://example.com/path and docs.example.org/a.",
		},
	}
	sender := &fakeSender{}
	sessions := session.New(10, time.Hour)
	service := New(cfg, agentClient, sender, sessions, testLogger())
	allowLinks := false
	bindings, err := binding.NewRegistry([]binding.Rule{{
		Name:       "no-links",
		Platform:   "*",
		SelfID:     "10001",
		ChatType:   "group",
		ChatID:     "30003",
		AllowLinks: &allowLinks,
	}})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	service.SetChatBindings(bindings)

	service.handleEvent(context.Background(), testEvent("no-links", "hello", true))

	if len(sender.messages) != 1 ||
		strings.Contains(sender.messages[0].text, "example.") {
		t.Fatalf("outbound = %#v", sender.messages)
	}
	snapshot := sessions.SnapshotState("qq-onebot:self:10001:group:30003")
	if len(snapshot.History) != 2 ||
		strings.Contains(snapshot.History[1].Content, "example.") {
		t.Fatalf("history = %#v", snapshot.History)
	}
}

func TestHandleAllowedPrivateMessageWithoutMention(t *testing.T) {
	cfg := testBotConfig(t)
	agentClient := &fakeAgent{response: domain.AgentResponse{Reply: "私聊答案"}}
	sender := &fakeSender{}
	service := New(cfg, agentClient, sender, session.New(10, time.Hour), testLogger())

	service.handleEvent(context.Background(), testPrivateEvent("private-1", "私聊问题", "20002"))

	if len(agentClient.requests) != 1 {
		t.Fatalf("agent calls = %d, want 1", len(agentClient.requests))
	}
	request := agentClient.requests[0]
	if request.Platform != platform.PlatformQQOneBot ||
		request.ChatType != platform.ChatPrivate ||
		request.ChatID != "20002" ||
		request.GroupID != "" ||
		request.SessionID != "qq-onebot:self:10001:private:20002" {
		t.Fatalf("private agent request = %#v", request)
	}
	if len(sender.messages) != 1 {
		t.Fatalf("sent messages = %d, want 1", len(sender.messages))
	}
	sent := sender.messages[0]
	if sent.chatType != platform.ChatPrivate ||
		sent.chatID != "20002" ||
		sent.selfID != "10001" ||
		sent.replyTo != "private-1" ||
		sent.text != "私聊答案" {
		t.Fatalf("private outbound = %#v", sent)
	}
}

func TestIgnoresPrivateMessageOutsideAllowlist(t *testing.T) {
	cfg := testBotConfig(t)
	agentClient := &fakeAgent{response: domain.AgentResponse{Reply: "answer"}}
	sender := &fakeSender{}
	service := New(cfg, agentClient, sender, session.New(10, time.Hour), testLogger())

	service.handleEvent(context.Background(), testPrivateEvent("private-2", "不允许的私聊", "99999"))

	if len(agentClient.requests) != 0 || len(sender.messages) != 0 {
		t.Fatalf("private message outside allowlist reached bot")
	}
	if service.Stats().Ignored != 1 {
		t.Fatalf("Ignored = %d, want 1", service.Stats().Ignored)
	}
}

func TestHandoffAndResumeCommands(t *testing.T) {
	cfg := testBotConfig(t)
	agentClient := &fakeAgent{response: domain.AgentResponse{Reply: "answer"}}
	sender := &fakeSender{}
	service := New(cfg, agentClient, sender, session.New(10, time.Hour), testLogger())

	service.handleEvent(context.Background(), testEvent("1", "/人工", true))
	service.handleEvent(context.Background(), testEvent("2", "暂停期间的问题", true))
	service.handleEvent(context.Background(), testEvent("3", "/恢复", true))
	service.handleEvent(context.Background(), testEvent("4", "恢复后的问题", true))

	if len(agentClient.requests) != 1 ||
		agentClient.requests[0].Text != "[tester (20002)]: 恢复后的问题" {
		t.Fatalf("agent requests = %#v", agentClient.requests)
	}
	if len(sender.messages) != 3 {
		t.Fatalf("sent messages = %d, want 3", len(sender.messages))
	}
	if service.Stats().HandoffCount != 1 {
		t.Fatalf("HandoffCount = %d, want 1", service.Stats().HandoffCount)
	}
}

func TestDuplicateAndCooldown(t *testing.T) {
	cfg := testBotConfig(t)
	cfg.UserCooldown = time.Minute
	agentClient := &fakeAgent{response: domain.AgentResponse{Reply: "answer"}}
	service := New(cfg, agentClient, &fakeSender{}, session.New(10, time.Hour), testLogger())
	now := time.Unix(100, 0)
	service.now = func() time.Time { return now }

	service.handleEvent(context.Background(), testEvent("1", "问题", true))
	service.handleEvent(context.Background(), testEvent("1", "重复投递", true))
	service.handleEvent(context.Background(), testEvent("2", "过快消息", true))

	if len(agentClient.requests) != 1 {
		t.Fatalf("agent calls = %d, want 1", len(agentClient.requests))
	}
	stats := service.Stats()
	if stats.Ignored != 1 || stats.RateLimited != 1 {
		t.Fatalf("stats = %#v", stats)
	}
}

func TestCooldownIsSharedAcrossGroupsForSameUser(t *testing.T) {
	cfg := testBotConfig(t)
	cfg.GroupAllowlist, _ = config.ParseAllowlist("*")
	cfg.UserCooldown = time.Minute
	agentClient := &fakeAgent{response: domain.AgentResponse{Reply: "answer"}}
	service := New(cfg, agentClient, &fakeSender{}, session.New(10, time.Hour), testLogger())
	service.now = func() time.Time { return time.Unix(100, 0) }

	first := testEventFrom("1", "first", true, "20002", "alice")
	first.GroupID = "30003"
	second := testEventFrom("2", "second", true, "20002", "alice")
	second.GroupID = "30004"
	service.handleEvent(context.Background(), first)
	service.handleEvent(context.Background(), second)

	if len(agentClient.requests) != 1 || service.Stats().RateLimited != 1 {
		t.Fatalf("requests=%d stats=%#v", len(agentClient.requests), service.Stats())
	}
}

func TestRateWindowLimitsAndRecovers(t *testing.T) {
	cfg := testBotConfig(t)
	cfg.UserCooldown = 0
	cfg.UserRateLimit = 2
	cfg.UserRateWindow = time.Minute
	agentClient := &fakeAgent{response: domain.AgentResponse{Reply: "answer"}}
	service := New(cfg, agentClient, &fakeSender{}, session.New(10, time.Hour), testLogger())
	now := time.Unix(100, 0)
	service.now = func() time.Time { return now }

	service.handleEvent(context.Background(), testEvent("1", "one", true))
	now = now.Add(time.Second)
	service.handleEvent(context.Background(), testEvent("2", "two", true))
	now = now.Add(time.Second)
	service.handleEvent(context.Background(), testEvent("3", "three", true))
	if len(agentClient.requests) != 2 || service.Stats().RateLimited != 1 {
		t.Fatalf("before expiry requests=%d stats=%#v", len(agentClient.requests), service.Stats())
	}
	now = now.Add(time.Minute)
	service.handleEvent(context.Background(), testEvent("4", "four", true))
	if len(agentClient.requests) != 3 {
		t.Fatalf("after expiry requests=%d, want 3", len(agentClient.requests))
	}
}

func TestSplitReplyTruncatesAtConfiguredLimit(t *testing.T) {
	chunks := splitReply("1234567890ABC", 5, 2)
	if len(chunks) != 2 {
		t.Fatalf("len(chunks) = %d, want 2", len(chunks))
	}
	for _, chunk := range chunks {
		if len([]rune(chunk)) > 5 {
			t.Fatalf("chunk %q exceeds rune limit", chunk)
		}
	}
	if chunks[1] != "6[截断]" {
		t.Fatalf("last chunk = %q", chunks[1])
	}
}

func TestPipelineStagesAndCustomCommand(t *testing.T) {
	cfg := testBotConfig(t)
	agentClient := &fakeAgent{response: domain.AgentResponse{Reply: "agent"}}
	sender := &fakeSender{}
	service := New(cfg, agentClient, sender, session.New(10, time.Hour), testLogger())
	if got := service.PipelineStages(); len(got) != 11 ||
		got[0] != "wake" || got[1] != "security" ||
		got[4] != "attention" || got[len(got)-1] != "respond" {
		t.Fatalf("pipeline stages = %#v", got)
	}
	if err := service.CommandRegistry().Register(command.Definition{
		Name: "status",
		Handler: func(context.Context, *command.Context) (command.Result, error) {
			return command.Result{Handled: true, Reply: "custom status"}, nil
		},
	}); err != nil {
		t.Fatalf("Register command: %v", err)
	}
	service.handleEvent(context.Background(), testEvent("custom-command", "/status", true))
	if len(agentClient.requests) != 0 || len(sender.messages) != 1 || sender.messages[0].text != "custom status" {
		t.Fatalf("command handling agent=%#v messages=%#v", agentClient.requests, sender.messages)
	}
}

type denyingMessageGuard struct{}

func (denyingMessageGuard) BeforeMessage(
	context.Context,
	*plugin.MessageContext,
) (plugin.Decision, error) {
	return plugin.Decision{Handled: true, Reply: "denied"}, nil
}

func TestMessageGuardBypassesCommandsRateLimitPluginsAndHistory(t *testing.T) {
	cfg := testBotConfig(t)
	cfg.UserCooldown = time.Hour
	agentClient := &fakeAgent{response: domain.AgentResponse{Reply: "agent"}}
	sender := &fakeSender{}
	sessions := session.New(10, time.Hour)
	service := New(cfg, agentClient, sender, sessions, testLogger())
	service.SetMessageGuard(denyingMessageGuard{})
	if err := service.CommandRegistry().Register(command.Definition{
		Name: "status",
		Handler: func(context.Context, *command.Context) (command.Result, error) {
			t.Fatal("command handler was called after security denial")
			return command.Result{}, nil
		},
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if err := service.PluginRegistry().Register(promptPlugin{}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	service.handleEvent(context.Background(), testEvent("guard-1", "/status", true))
	service.handleEvent(context.Background(), testEvent("guard-2", "/status", true))

	if len(agentClient.requests) != 0 ||
		len(sender.messages) != 2 ||
		sender.messages[0].text != "denied" ||
		sender.messages[1].text != "denied" {
		t.Fatalf("agent=%#v messages=%#v", agentClient.requests, sender.messages)
	}
	if service.Stats().RateLimited != 0 {
		t.Fatalf("stats = %#v", service.Stats())
	}
	snapshot := sessions.SnapshotState("qq-onebot:self:10001:group:30003")
	if len(snapshot.History) != 0 {
		t.Fatalf("security denial entered history: %#v", snapshot.History)
	}
}

type promptPlugin struct{}

func (promptPlugin) Name() string { return "prompt" }

func (promptPlugin) BeforeMessage(_ context.Context, event *plugin.MessageContext) (plugin.Decision, error) {
	event.Values["agent.prompt_context"] = "trusted test context"
	return plugin.Decision{}, nil
}

func TestPluginContextReachesAgentRequest(t *testing.T) {
	cfg := testBotConfig(t)
	agentClient := &fakeAgent{response: domain.AgentResponse{Reply: "answer"}}
	service := New(cfg, agentClient, &fakeSender{}, session.New(10, time.Hour), testLogger())
	if err := service.PluginRegistry().Register(promptPlugin{}); err != nil {
		t.Fatalf("Register plugin: %v", err)
	}
	service.handlePlatformEvent(context.Background(), platform.Event{
		Platform:    platform.PlatformQQOneBot,
		PostType:    "message",
		MessageType: platform.ChatGroup,
		MessageID:   "plugin-context",
		SelfID:      "10001",
		UserID:      "20002",
		ChatID:      "30003",
		Chain:       message.Chain{message.At("10001"), message.Text("question")},
	})
	if len(agentClient.requests) != 1 ||
		!strings.Contains(agentClient.requests[0].PromptContext, "trusted test context") ||
		!strings.Contains(agentClient.requests[0].PromptContext, "CURRENT KNOWLEDGE") {
		t.Fatalf("agent request = %#v", agentClient.requests)
	}
}

func TestSessionSelectsProviderAndPersona(t *testing.T) {
	cfg := testBotConfig(t)
	primary := &fakeAgent{response: domain.AgentResponse{Reply: "primary"}}
	backup := &fakeAgent{response: domain.AgentResponse{Reply: "backup"}}
	providers := provider.NewRegistry()
	if err := providers.Register(provider.Wrap("primary", "test", primary)); err != nil {
		t.Fatalf("Register(primary) error = %v", err)
	}
	if err := providers.Register(provider.Wrap("backup", "test", backup)); err != nil {
		t.Fatalf("Register(backup) error = %v", err)
	}
	sessions := session.New(10, time.Hour)
	service := NewWithRuntime(
		cfg,
		primary,
		&fakeSender{},
		sessions,
		testLogger(),
		providers,
		plugin.NewRegistry(),
	)
	if err := service.PersonaRegistry().Register(persona.Profile{
		Name:         "sales",
		SystemPrompt: "sales prompt",
	}); err != nil {
		t.Fatalf("Register(persona) error = %v", err)
	}
	personaName := "sales"
	providerName := "backup"
	sessions.UpdateSettings(
		"qq-onebot:self:10001:group:30003",
		&personaName,
		&providerName,
	)
	service.handleEvent(context.Background(), testEvent("session-routing", "question", true))
	if len(primary.requests) != 0 || len(backup.requests) != 1 {
		t.Fatalf("provider calls primary=%d backup=%d", len(primary.requests), len(backup.requests))
	}
	if !strings.HasPrefix(backup.requests[0].SystemPrompt, "sales prompt") ||
		!strings.Contains(backup.requests[0].SystemPrompt, "群聊交互边界") {
		t.Fatalf("system prompt = %q", backup.requests[0].SystemPrompt)
	}
}

func TestBindingOverridesSessionProviderAndPersona(t *testing.T) {
	cfg := testBotConfig(t)
	boundAgent := &fakeAgent{response: domain.AgentResponse{Reply: "bound"}}
	sessionAgent := &fakeAgent{response: domain.AgentResponse{Reply: "session"}}
	providers := provider.NewRegistry()
	if err := providers.Register(provider.Wrap("bound", "test", boundAgent)); err != nil {
		t.Fatalf("Register(bound) error = %v", err)
	}
	if err := providers.Register(provider.Wrap("session", "test", sessionAgent)); err != nil {
		t.Fatalf("Register(session) error = %v", err)
	}
	sessions := session.New(10, time.Hour)
	service := NewWithRuntime(
		cfg,
		boundAgent,
		&fakeSender{},
		sessions,
		testLogger(),
		providers,
		plugin.NewRegistry(),
	)
	for _, profile := range []persona.Profile{
		{Name: "bound", SystemPrompt: "bound prompt"},
		{Name: "session", SystemPrompt: "session prompt"},
	} {
		if err := service.PersonaRegistry().Register(profile); err != nil {
			t.Fatalf("Register(%s) error = %v", profile.Name, err)
		}
	}
	bindings, err := binding.NewRegistry([]binding.Rule{{
		Name:     "bound-group",
		Platform: "*",
		SelfID:   "10001",
		ChatType: platform.ChatGroup,
		ChatID:   "30003",
		Persona:  "bound",
		Provider: "bound",
	}})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	service.SetChatBindings(bindings)
	sessionPersona := "session"
	sessionProvider := "session"
	sessions.UpdateSettings(
		"qq-onebot:self:10001:group:30003",
		&sessionPersona,
		&sessionProvider,
	)

	service.handleEvent(context.Background(), testEvent("binding-routing", "question", true))

	if len(boundAgent.requests) != 1 || len(sessionAgent.requests) != 0 {
		t.Fatalf(
			"provider calls bound=%d session=%d",
			len(boundAgent.requests),
			len(sessionAgent.requests),
		)
	}
	if !strings.HasPrefix(boundAgent.requests[0].SystemPrompt, "bound prompt") ||
		!strings.Contains(boundAgent.requests[0].SystemPrompt, "群聊交互边界") {
		t.Fatalf("system prompt = %q", boundAgent.requests[0].SystemPrompt)
	}
}

func TestPersonaBeginDialogsAndCustomErrorMessage(t *testing.T) {
	cfg := testBotConfig(t)
	agentClient := &fakeAgent{err: errors.New("provider unavailable")}
	sender := &fakeSender{}
	service := New(cfg, agentClient, sender, session.New(10, time.Hour), testLogger())
	if err := service.PersonaRegistry().Register(persona.Profile{
		Name:               "community",
		SystemPrompt:       "community prompt",
		BeginDialogs:       []persona.Dialogue{{User: "hello", Assistant: "welcome"}},
		CustomErrorMessage: "community unavailable",
		Default:            true,
	}); err != nil {
		t.Fatalf("Register(persona) error = %v", err)
	}

	service.handleEvent(context.Background(), testEvent("persona-error", "question", true))

	if len(agentClient.requests) != 1 {
		t.Fatalf("agent calls = %d, want 1", len(agentClient.requests))
	}
	history := agentClient.requests[0].History
	if len(history) != 2 || history[0].Content != "hello" || history[1].Content != "welcome" {
		t.Fatalf("begin dialogs = %#v", history)
	}
	if len(sender.messages) != 1 || sender.messages[0].text != "community unavailable" {
		t.Fatalf("error reply = %#v", sender.messages)
	}
}

func testBotConfig(t *testing.T) config.Config {
	t.Helper()
	allowlist, err := config.ParseAllowlist("30003")
	if err != nil {
		t.Fatalf("ParseAllowlist() error = %v", err)
	}
	privateAllowlist, err := config.ParseOptionalAllowlist("20002")
	if err != nil {
		t.Fatalf("ParseOptionalAllowlist() error = %v", err)
	}
	return config.Config{
		GroupAllowlist:   allowlist,
		PrivateAllowlist: privateAllowlist,
		RequireMention:   true,
		QuoteReply:       true,
		GroupAtSender:    true,
		MaxReplyRunes:    1500,
		MaxReplyChunks:   4,
		MaxConcurrency:   2,
		UserCooldown:     0,
		MessageDedupeTTL: time.Minute,
		HandoffReply:     "handoff",
		ResumeReply:      "resume",
		ClearReply:       "clear",
		ErrorReply:       "error",
	}
}

func testEvent(messageID, text string, mentioned bool) onebot.Event {
	return testEventFrom(messageID, text, mentioned, "20002", "tester")
}

func testEventFrom(messageID, text string, mentioned bool, userID, nickname string) onebot.Event {
	segments := make([]map[string]any, 0, 2)
	if mentioned {
		segments = append(segments, map[string]any{
			"type": "at",
			"data": map[string]any{"qq": "10001"},
		})
	}
	segments = append(segments, map[string]any{
		"type": "text",
		"data": map[string]any{"text": text},
	})
	message, _ := json.Marshal(segments)
	return onebot.Event{
		Time:        100,
		SelfID:      "10001",
		PostType:    "message",
		MessageType: "group",
		MessageID:   onebot.StringID(messageID),
		UserID:      onebot.StringID(userID),
		GroupID:     "30003",
		Message:     message,
		Sender:      onebot.Sender{Nickname: nickname},
	}
}

func testPrivateEvent(messageID, text, userID string) onebot.Event {
	message, _ := json.Marshal([]map[string]any{{
		"type": "text",
		"data": map[string]any{"text": text},
	}})
	return onebot.Event{
		Time:        100,
		SelfID:      "10001",
		PostType:    "message",
		MessageType: platform.ChatPrivate,
		MessageID:   onebot.StringID(messageID),
		UserID:      onebot.StringID(userID),
		Message:     message,
		Sender:      onebot.Sender{Nickname: "tester"},
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
