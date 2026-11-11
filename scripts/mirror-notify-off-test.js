// Mirror the notify-off regression tests into cinlan-bot core
// service_test.go — append after TestSlashCommandSurvivesMentionOnly.
const fs = require('fs');
const P = 'D:/Company/cinlan/apps/cinlan-bot/core/bot/service_test.go';
let t = fs.readFileSync(P, 'utf8');

const anchor = `	if len(sender.messages) != 2 || len(agentClient.requests) != 0 {
		t.Fatalf("slash junk leaked: messages=%#v requests=%#v", sender.messages, agentClient.requests)
	}
}`;
if (!t.includes(anchor)) throw new Error('anchor miss');

const add = anchor + `

func TestNotifyOffDropsFollowUpContinuations(t *testing.T) {
	// /关闭通知 promises "only @ reaches her" — a follow-up continuation
	// from the user she just answered must not slip through the
	// MentionOnly gate the way binding-level mention-only still allows.
	cfg := testBotConfig(t)
	agentClient := &fakeAgent{response: domain.AgentResponse{Reply: "agent"}}
	sender := &fakeSender{}
	service := New(cfg, agentClient, sender, session.New(10, time.Hour), testLogger())
	prefs, err := chatprefs.Load("", "qq-onebot")
	if err != nil {
		t.Fatalf("chatprefs.Load() error = %v", err)
	}
	service.SetChatPrefs(prefs)
	if err := prefs.SetNotifyEnabled("qq-onebot", "10001", platform.ChatGroup, "30003", false); err != nil {
		t.Fatalf("SetNotifyEnabled() error = %v", err)
	}

	// A mention still works — and seeds the follow-up window for this user.
	service.handleEvent(context.Background(), testEvent("notify-off-1", "hi", true))
	if len(sender.messages) != 1 {
		t.Fatalf("mention reply dropped under notify-off: %#v", sender.messages)
	}

	// The same user's non-@ follow-up lands inside the follow-up window
	// but notify-off hard-silences continuations too.
	service.handleEvent(context.Background(), testEvent("notify-off-2", "还有呢", false))
	if len(agentClient.requests) != 1 || len(sender.messages) != 1 {
		t.Fatalf("follow-up leaked under notify-off: requests=%#v messages=%#v",
			agentClient.requests, sender.messages)
	}
}

func TestMentionOnlyWithoutNotifyOffKeepsFollowUp(t *testing.T) {
	// Binding-level MentionOnly (RequireMention, no /关闭通知) keeps the
	// follow-up exemption — a person already in conversation keeps
	// listening without needing a fresh @ every line.
	cfg := testBotConfig(t)
	agentClient := &fakeAgent{response: domain.AgentResponse{Reply: "agent"}}
	sender := &fakeSender{}
	service := New(cfg, agentClient, sender, session.New(10, time.Hour), testLogger())

	service.handleEvent(context.Background(), testEvent("fu-1", "hi", true))
	service.handleEvent(context.Background(), testEvent("fu-2", "还有呢", false))
	if len(agentClient.requests) != 2 || len(sender.messages) != 2 {
		t.Fatalf("binding-level follow-up dropped: requests=%#v messages=%#v",
			agentClient.requests, sender.messages)
	}
}`;

t = t.replace(anchor, add);
fs.writeFileSync(P, t);
console.log('core tests added');
