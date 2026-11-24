package bot

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/domain"
	"github.com/lianyorker/cinlan-qq-bot/internal/session"
)

// Inbound messages get a "#N" handle in request order; a [[REPLY:#N]]
// citation re-anchors the send at the cited message.
func TestReplyRefAnchorsToModelChosenMessage(t *testing.T) {
	cfg := testBotConfig(t)
	var calls int
	agentClient := &fakeAgent{reply: func(request domain.AgentRequest) (domain.AgentResponse, error) {
		calls++
		if calls == 1 {
			return domain.AgentResponse{Reply: "先答这条"}, nil
		}
		return domain.AgentResponse{Reply: "[[REPLY:#1]] 接你上面那句"}, nil
	}}
	sender := &fakeSender{}
	sessions := session.New(10, time.Hour)
	service := New(cfg, agentClient, sender, sessions, testLogger())

	service.handleEvent(context.Background(), testEventFrom("m1", "第一句", true, "20002", "alice"))
	service.handleEvent(context.Background(), testEventFrom("m2", "第二句", true, "20003", "bob"))

	if len(agentClient.requests) != 2 {
		t.Fatalf("agent calls = %d, want 2", len(agentClient.requests))
	}
	if !strings.HasPrefix(agentClient.requests[0].Text, "#1 ") ||
		!strings.HasPrefix(agentClient.requests[1].Text, "#2 ") {
		t.Fatalf("inbound refs not tagged: %q / %q",
			agentClient.requests[0].Text, agentClient.requests[1].Text)
	}
	if len(sender.messages) != 2 || sender.messages[1].replyTo != "m1" {
		t.Fatalf("reply anchor = %#v, want replyTo m1", sender.messages)
	}
	if strings.Contains(sender.messages[1].text, "REPLY") {
		t.Fatalf("[[REPLY:#N]] leaked into outbound: %q", sender.messages[1].text)
	}
	// The answered tag must name the cited message, not the newest one.
	snapshot := sessions.SnapshotState("qq-onebot:self:10001:group:30003")
	if len(snapshot.History) != 4 ||
		!strings.Contains(snapshot.History[3].Content, "(回#1)") {
		t.Fatalf("answered-ref tag missing: %#v", snapshot.History)
	}
}

// A hallucinated or expired handle degrades to the newest inbound and
// still never leaks the marker.
func TestReplyRefUnknownFallsBackToLatestInbound(t *testing.T) {
	cfg := testBotConfig(t)
	agentClient := &fakeAgent{
		response: domain.AgentResponse{Reply: "[[REPLY:#99]] 在呢"},
	}
	sender := &fakeSender{}
	service := New(cfg, agentClient, sender, session.New(10, time.Hour), testLogger())

	service.handleEvent(context.Background(), testEvent("m1", "在吗", true))

	if len(sender.messages) != 1 || sender.messages[0].replyTo != "m1" {
		t.Fatalf("unknown ref did not fall back: %#v", sender.messages)
	}
	if strings.Contains(sender.messages[0].text, "REPLY") {
		t.Fatalf("marker leaked on fallback: %q", sender.messages[0].text)
	}
}

// BubbleShaping=off: the model's own [[NEXT]] placement stands — a long
// unmarked reply stays one bubble instead of being chopped at 32 runes.
func TestBubbleShapingOffKeepsSingleLongBubble(t *testing.T) {
	cfg := testBotConfig(t)
	cfg.BubbleShaping = false
	cfg.ReplyPartDelay = 0
	agentClient := &fakeAgent{
		response: domain.AgentResponse{
			Reply: "第一句挺长的话随便唠唠嗑嗑瓜子儿。第二句也很长不想被机械拆开哦。",
		},
	}
	sender := &fakeSender{}
	service := New(cfg, agentClient, sender, session.New(10, time.Hour), testLogger())

	service.handleEvent(context.Background(), testEvent("m1", "在吗", true))

	if len(sender.messages) != 1 {
		t.Fatalf("shaping off re-chopped the bubble: %#v", sender.messages)
	}
	if !strings.Contains(sender.messages[0].text, "第二句也很长") {
		t.Fatalf("bubble truncated: %q", sender.messages[0].text)
	}
}

func TestBubbleShapingOffStillHonorsModelNext(t *testing.T) {
	cfg := testBotConfig(t)
	cfg.BubbleShaping = false
	cfg.ReplyPartDelay = 0
	agentClient := &fakeAgent{
		response: domain.AgentResponse{Reply: "先说这个[[NEXT]]还有这个"},
	}
	sender := &fakeSender{}
	service := New(cfg, agentClient, sender, session.New(10, time.Hour), testLogger())

	service.handleEvent(context.Background(), testEvent("m1", "在吗", true))

	if len(sender.messages) != 2 ||
		sender.messages[0].text != "先说这个" ||
		sender.messages[1].text != "还有这个" {
		t.Fatalf("[[NEXT]] splits lost: %#v", sender.messages)
	}
}

// LeanPrompt strips the directing injections but keeps facts and the
// marker protocol the reply parser depends on.
func TestLeanPromptDropsDirectivesKeepsProtocol(t *testing.T) {
	cfg := testBotConfig(t)
	cfg.LeanPrompt = true
	agentClient := &fakeAgent{response: domain.AgentResponse{Reply: "ok"}}
	service := New(cfg, agentClient, &fakeSender{}, session.New(10, time.Hour), testLogger())

	service.handleEvent(context.Background(), testPrivateEvent("p1", "在吗", "20002"))

	if len(agentClient.requests) != 1 {
		t.Fatalf("agent calls = %d, want 1", len(agentClient.requests))
	}
	prompt := agentClient.requests[0].SystemPrompt
	for _, banned := range []string{
		"真人行为准则", // embedded base rules
		"现在是私聊",
		"把你做出来的人",
		"优先响应他的诉求",
	} {
		if strings.Contains(prompt, banned) {
			t.Fatalf("lean prompt kept directive %q", banned)
		}
	}
	for _, kept := range []string{"[[PASS]]", "[[NEXT]]", "[[REPLY"} {
		if !strings.Contains(prompt, kept) {
			t.Fatalf("lean prompt dropped protocol %q", kept)
		}
	}
}

// Lean off is the legacy full prompt — control case for the test above.
func TestFullPromptKeepsDirectives(t *testing.T) {
	cfg := testBotConfig(t)
	agentClient := &fakeAgent{response: domain.AgentResponse{Reply: "ok"}}
	service := New(cfg, agentClient, &fakeSender{}, session.New(10, time.Hour), testLogger())

	service.handleEvent(context.Background(), testPrivateEvent("p1", "在吗", "20002"))

	prompt := agentClient.requests[0].SystemPrompt
	if !strings.Contains(prompt, "现在是私聊") {
		t.Fatalf("full prompt lost private directive")
	}
}

// Ambient cooldown: right after she spoke, unrelated chatter stays
// background without burning a judge call; mentions still bypass it.
func TestAmbientCooldownSuppressesJudgeCall(t *testing.T) {
	agentClient := &fakeAgent{reply: func(request domain.AgentRequest) (domain.AgentResponse, error) {
		if strings.HasPrefix(request.RequestID, "attention:") {
			return attentionResponse("ignore", "chatter", 0.9, "闲聊"), nil
		}
		return domain.AgentResponse{Reply: "答你"}, nil
	}}
	sender := &fakeSender{}
	service := smartAttentionService(t, agentClient, sender)
	service.cfg.AmbientCooldown = time.Minute
	now := time.Unix(1000, 0)
	service.now = func() time.Time { return now }

	service.handleEvent(context.Background(), testEvent("cool-1", "在吗", true))
	if len(agentClient.requests) != 1 || len(sender.messages) != 1 {
		t.Fatalf("mention not answered: requests=%#v messages=%#v",
			agentClient.requests, sender.messages)
	}

	// A different user's ambient message inside the window: ignored with
	// no judge call at all.
	service.handlePlatformEvent(context.Background(),
		platformEventFrom("cool-2", "你们在聊啥", false, "30004"))
	if len(agentClient.requests) != 1 {
		t.Fatalf("ambient inside cooldown reached the judge: %#v", agentClient.requests)
	}

	// A mention during cooldown still bypasses every gate.
	service.handleEvent(context.Background(), testEvent("cool-3", "又问一句", true))
	if len(agentClient.requests) != 2 || len(sender.messages) != 2 {
		t.Fatalf("mention during cooldown dropped: requests=%#v messages=%#v",
			agentClient.requests, sender.messages)
	}

	// Past the window, ambient chatter goes back to the judge.
	now = now.Add(2 * time.Minute)
	service.handlePlatformEvent(context.Background(),
		platformEventFrom("cool-4", "还有人在吗", false, "30004"))
	if len(agentClient.requests) != 3 ||
		!strings.HasPrefix(agentClient.requests[2].RequestID, "attention:") {
		t.Fatalf("ambient past cooldown skipped the judge: %#v", agentClient.requests)
	}
}

func TestStripReplyRefMarker(t *testing.T) {
	cleaned, ref, ok := stripReplyRefMarker("答你[[REPLY:#3]]")
	if !ok || ref != "3" || cleaned != "答你" {
		t.Fatalf("strip = %q %q %v", cleaned, ref, ok)
	}
	cleaned, ref, ok = stripReplyRefMarker("[[REPLY: 2 ]] ok")
	if !ok || ref != "2" || cleaned != "ok" {
		t.Fatalf("strip = %q %q %v", cleaned, ref, ok)
	}
	if _, _, ok = stripReplyRefMarker("没标记"); ok {
		t.Fatal("plain text matched ref marker")
	}
	// Every occurrence scrubbed, first one wins.
	cleaned, ref, ok = stripReplyRefMarker("a[[REPLY:#1]]b[[REPLY:#2]]")
	if !ok || ref != "1" || strings.Contains(cleaned, "REPLY") {
		t.Fatalf("multi marker = %q %q %v", cleaned, ref, ok)
	}
}

func TestRawReplyParts(t *testing.T) {
	if got := rawReplyParts("a[[NEXT]]b", 4); len(got) != 2 ||
		got[0] != "a" || got[1] != "b" {
		t.Fatalf("split = %#v", got)
	}
	// Overflow folds into the last bubble — nothing is dropped.
	if got := rawReplyParts("a[[NEXT]]b[[NEXT]]c", 2); len(got) != 2 ||
		got[1] != "b\nc" {
		t.Fatalf("overflow fold = %#v", got)
	}
	if got := rawReplyParts("   ", 4); len(got) != 0 {
		t.Fatalf("blank reply = %#v", got)
	}
	if got := rawReplyParts("one", 4); len(got) != 1 || got[0] != "one" {
		t.Fatalf("single = %#v", got)
	}
}
