package bot

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/domain"
	"github.com/lianyorker/cinlan-qq-bot/internal/onebot"
	"github.com/lianyorker/cinlan-qq-bot/internal/platform"
	"github.com/lianyorker/cinlan-qq-bot/internal/session"
)

// fakeCaller records platform Call invocations and can answer
// send_*_msg with a message_id like NapCat does.
type fakeCaller struct {
	mu      sync.Mutex
	actions []string
	params  []map[string]any
	results map[string]any
}

func (f *fakeCaller) Call(
	_ context.Context, action string, params map[string]any,
) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.actions = append(f.actions, action)
	f.params = append(f.params, params)
	if f.results != nil {
		return f.results[action], nil
	}
	return nil, nil
}

func (f *fakeCaller) count(action string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, a := range f.actions {
		if a == action {
			n++
		}
	}
	return n
}

func TestActionMarkerParsing(t *testing.T) {
	if tag, ok := parseSelfieMarker("[[SELFIE:委屈]]"); !ok || tag != "委屈" {
		t.Fatalf("selfie marker = %q %v", tag, ok)
	}
	if tag, ok := parseSelfieMarker("[[SELFIE]]"); !ok || tag != "" {
		t.Fatalf("bare selfie = %q %v", tag, ok)
	}
	if _, ok := parseSelfieMarker("文字 [[SELFIE:x]]"); ok {
		t.Fatalf("inline selfie must be whole-part")
	}
	if key, ok := parseReactMarker("[[REACT:爱心]]"); !ok || key != "爱心" {
		t.Fatalf("react marker = %q %v", key, ok)
	}
	if key, ok := parseReactMarker("[[表情:哭]]"); !ok || key != "哭" {
		t.Fatalf("react alias = %q %v", key, ok)
	}
	if key, ok := parseReactMarker("[[REACT]]"); !ok || key != "" {
		t.Fatalf("bare react = %q %v", key, ok)
	}
	if target, ok := parsePokeMarker("[[POKE]]"); !ok || target != "" {
		t.Fatalf("bare poke = %q %v", target, ok)
	}
	if target, ok := parsePokeMarker("[[POKE:12345]]"); !ok || target != "12345" {
		t.Fatalf("poke target = %q %v", target, ok)
	}
	if !isDiceMarker("[[DICE]]") || !isDiceMarker("[[骰子]]") {
		t.Fatalf("dice marker not recognized")
	}
	if !isRPSMarker("[[RPS]]") || !isRPSMarker("[[猜拳]]") {
		t.Fatalf("rps marker not recognized")
	}
	if inner, ok := parseOopsMarker("[[OOPS]]其实我想你[[/OOPS]]"); !ok ||
		inner != "其实我想你" {
		t.Fatalf("oops marker = %q %v", inner, ok)
	}
	if _, ok := parseOopsMarker("[[OOPS]]没了"); ok {
		t.Fatalf("unclosed oops must not parse")
	}
}

func TestSplitMarkerLinesKeepsActions(t *testing.T) {
	parts := splitMarkerLines("行 我马上[[REACT:爱心]]\n[[POKE]]\n[[DICE]]\n[[OOPS]]发错了[[/OOPS]]")
	want := []string{"行 我马上", "[[REACT:爱心]]", "[[POKE]]", "[[DICE]]", "[[OOPS]]发错了[[/OOPS]]"}
	if len(parts) != len(want) {
		t.Fatalf("parts = %#v", parts)
	}
	for i := range want {
		if parts[i] != want[i] {
			t.Fatalf("part %d = %q, want %q", i, parts[i], want[i])
		}
	}
	// Unknown [[..]] still drops instead of leaking.
	parts = splitMarkerLines("前面[[FOO:x]]后面")
	if len(parts) != 2 || parts[0] != "前面" || parts[1] != "后面" {
		t.Fatalf("unknown marker split = %#v", parts)
	}
}

func TestStickerPoolAndPick(t *testing.T) {
	root := t.TempDir()
	personaDir := filepath.Join(root, "七七")
	sharedDir := filepath.Join(root, "shared")
	for _, dir := range []string{personaDir, sharedDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		filepath.Join(personaDir, "委屈.png"):   "a",
		filepath.Join(personaDir, "炸毛.gif"):   "b",
		filepath.Join(sharedDir, "笑死.webp"):   "c",
		filepath.Join(personaDir, "note.txt"): "skip", // not an image
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	service := New(testBotConfig(t), &fakeAgent{}, &fakeSender{},
		session.New(10, time.Hour), testLogger())
	service.SetStickerDir(root)

	pool := service.StickerPool("七七")
	if len(pool) != 3 || pool["note.txt"] != "" {
		t.Fatalf("pool = %#v", pool)
	}
	if got := service.pickSticker("七七", "委屈"); got != filepath.Join(personaDir, "委屈.png") {
		t.Fatalf("exact pick = %q", got)
	}
	if got := service.pickSticker("七七", "笑"); got != filepath.Join(sharedDir, "笑死.webp") {
		t.Fatalf("substring pick = %q", got)
	}
	if got := service.pickSticker("七七", "不存在"); got != "" {
		t.Fatalf("unmatched tag must not pick, got %q", got)
	}
	if got := service.pickSticker("七七", ""); got == "" {
		t.Fatalf("bare tag should pick a random sticker")
	}
	// Unknown persona still sees the shared pack.
	if got := service.pickSticker("路人", "笑死"); got != filepath.Join(sharedDir, "笑死.webp") {
		t.Fatalf("shared fallback = %q", got)
	}
}

func TestReactToMessageCallsEmojiLike(t *testing.T) {
	caller := &fakeCaller{}
	service := New(testBotConfig(t), &fakeAgent{}, &fakeSender{},
		session.New(10, time.Hour), testLogger())
	service.SetUserProfiler(caller)
	state := &flowState{
		chatType:  platform.ChatGroup,
		chatID:    "30003",
		userID:    "20002",
		selfID:    "10001",
		messageID: "456789",
	}
	service.reactToMessage(context.Background(), state, "爱心")
	if caller.count("set_msg_emoji_like") != 1 {
		t.Fatalf("emoji like calls = %v", caller.actions)
	}
	if got := caller.params[0]["emoji_id"]; got != "59" {
		t.Fatalf("emoji_id = %v", got)
	}
	if got := caller.params[0]["message_id"]; got != int64(456789) {
		t.Fatalf("message_id = %v", got)
	}
	// Bare marker defaults to heart; unknown keyword skips.
	service.reactToMessage(context.Background(), state, "")
	service.reactToMessage(context.Background(), state, "不存在表情")
	if caller.count("set_msg_emoji_like") != 2 {
		t.Fatalf("calls after default+unknown = %v", caller.actions)
	}
	// Numeric passthrough works.
	service.reactToMessage(context.Background(), state, "297")
	if caller.count("set_msg_emoji_like") != 3 {
		t.Fatalf("numeric passthrough missing, %v", caller.actions)
	}
	// No message_id → no call (poke notice context).
	state.messageID = ""
	service.reactToMessage(context.Background(), state, "爱心")
	if caller.count("set_msg_emoji_like") != 3 {
		t.Fatalf("react without message_id leaked")
	}
}

func TestSendPokeFallbacks(t *testing.T) {
	caller := &fakeCaller{}
	service := New(testBotConfig(t), &fakeAgent{}, &fakeSender{},
		session.New(10, time.Hour), testLogger())
	service.SetUserProfiler(caller)
	state := &flowState{
		chatType: platform.ChatGroup,
		chatID:   "30003",
		userID:   "20002",
		selfID:   "10001",
	}
	service.sendPoke(context.Background(), state, "")
	if caller.count("send_poke") != 1 {
		t.Fatalf("poke calls = %v", caller.actions)
	}
	params := caller.params[0]
	if params["user_id"] != int64(20002) || params["group_id"] != int64(30003) {
		t.Fatalf("poke params = %#v", params)
	}
	// Poking herself or nobody is a no-op.
	service.sendPoke(context.Background(), state, "10001")
	if caller.count("send_poke") != 1 {
		t.Fatalf("self-poke attempted")
	}
}

func TestDiceAndRPSSendAsSegments(t *testing.T) {
	cfg := testBotConfig(t)
	agentClient := &fakeAgent{response: domain.AgentResponse{
		Reply: "来！[[NEXT]][[DICE]][[NEXT]]谁大听谁的",
	}}
	sender := &fakeSender{}
	service := New(cfg, agentClient, sender, session.New(10, time.Hour), testLogger())
	service.handleEvent(context.Background(), testEvent("1", "掷骰子", true))
	if len(sender.messages) != 3 {
		t.Fatalf("messages = %#v", sender.messages)
	}
	if sender.messages[1].text != "" {
		t.Fatalf("dice bubble should carry no text, got %q", sender.messages[1].text)
	}
	if !strings.Contains(sender.messages[0].text, "来") ||
		!strings.Contains(sender.messages[2].text, "谁大听谁的") {
		t.Fatalf("text bubbles wrong: %#v", sender.messages)
	}
}

func TestOopsSendsThenRecalls(t *testing.T) {
	caller := &fakeCaller{results: map[string]any{
		"send_group_msg": map[string]any{"message_id": float64(7788)},
	}}
	service := New(testBotConfig(t), &fakeAgent{}, &fakeSender{},
		session.New(10, time.Hour), testLogger())
	service.SetUserProfiler(caller)
	state := &flowState{
		chatType: platform.ChatGroup,
		chatID:   "30003",
		selfID:   "10001",
	}
	service.sendThenRecall(context.Background(), state, "发错了")
	if caller.count("send_group_msg") != 1 || caller.count("delete_msg") != 1 {
		t.Fatalf("oops calls = %v", caller.actions)
	}
	if got := caller.params[1]["message_id"]; got != 7788 {
		t.Fatalf("recall message_id = %v", got)
	}
	// A failed send must not trigger a recall.
	caller2 := &fakeCaller{}
	service2 := New(testBotConfig(t), &fakeAgent{}, &fakeSender{},
		session.New(10, time.Hour), testLogger())
	service2.SetUserProfiler(caller2)
	service2.sendThenRecall(context.Background(), state, "hi")
	if caller2.count("delete_msg") != 0 {
		t.Fatalf("recall fired without message_id")
	}
}

func testPokeEvent(userID, targetID string) onebot.Event {
	return onebot.Event{
		Time:       100,
		SelfID:     "10001",
		PostType:   "notice",
		NoticeType: "notify",
		SubType:    "poke",
		UserID:     onebot.StringID(userID),
		GroupID:    "30003",
		TargetID:   onebot.StringID(targetID),
		Sender:     onebot.Sender{Nickname: "tester"},
	}
}

func TestSelfPokeFlowsThroughPipeline(t *testing.T) {
	cfg := testBotConfig(t)
	agentClient := &fakeAgent{response: domain.AgentResponse{Reply: "干嘛戳我"}}
	sender := &fakeSender{}
	service := New(cfg, agentClient, sender, session.New(10, time.Hour), testLogger())

	service.handleEvent(context.Background(), testPokeEvent("20002", "10001"))
	if len(agentClient.requests) != 1 {
		t.Fatalf("self-poke did not reach agent, requests=%d", len(agentClient.requests))
	}
	if got := agentClient.requests[0].Text; !strings.Contains(got, "戳了戳你") {
		t.Fatalf("poke prompt text = %q", got)
	}
	if len(sender.messages) != 1 || sender.messages[0].text != "干嘛戳我" {
		t.Fatalf("poke reply not sent: %#v", sender.messages)
	}
	// Second poke within cooldown is observed silently, not answered.
	service.handleEvent(context.Background(), testPokeEvent("20002", "10001"))
	if len(agentClient.requests) != 1 {
		t.Fatalf("cooldown failed, requests=%d", len(agentClient.requests))
	}
}

func TestPokeAtOtherMemberIsObservedOnly(t *testing.T) {
	cfg := testBotConfig(t)
	agentClient := &fakeAgent{response: domain.AgentResponse{Reply: "x"}}
	sender := &fakeSender{}
	store := session.New(10, time.Hour)
	service := New(cfg, agentClient, sender, store, testLogger())

	service.handleEvent(context.Background(), testPokeEvent("20002", "20003"))
	if len(agentClient.requests) != 0 || len(sender.messages) != 0 {
		t.Fatalf("other-member poke must not be answered")
	}
	history, _ := store.Snapshot("qq-onebot:self:10001:group:30003")
	found := false
	for _, h := range history {
		if strings.Contains(h.Content, "戳了戳") {
			found = true
		}
	}
	if !found {
		t.Fatalf("poke observation missing, history=%#v", history)
	}
}

func TestGroupRecallIsObservedSilently(t *testing.T) {
	cfg := testBotConfig(t)
	agentClient := &fakeAgent{response: domain.AgentResponse{Reply: "x"}}
	sender := &fakeSender{}
	store := session.New(10, time.Hour)
	service := New(cfg, agentClient, sender, store, testLogger())

	service.handleEvent(context.Background(), onebot.Event{
		Time:       100,
		SelfID:     "10001",
		PostType:   "notice",
		NoticeType: "group_recall",
		UserID:     "20002",
		OperatorID: "20002",
		GroupID:    "30003",
		MessageID:  "999",
	})
	if len(agentClient.requests) != 0 || len(sender.messages) != 0 {
		t.Fatalf("recall must never be answered")
	}
	history, _ := store.Snapshot("qq-onebot:self:10001:group:30003")
	found := false
	for _, h := range history {
		if strings.Contains(h.Content, "撤回了一条消息") {
			found = true
		}
	}
	if !found {
		t.Fatalf("recall observation missing, history=%#v", history)
	}
}
