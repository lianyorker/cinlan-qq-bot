package binding

import "testing"

func TestBindingResourcesAreNormalizedAndIsolated(t *testing.T) {
	learning := true
	allowLinks := false
	smartAttention := true
	registry, err := NewRegistry([]Rule{{
		Name:            "group",
		Platform:        "*",
		SelfID:          "1",
		ChatType:        "group",
		ChatID:          "2",
		Persona:         "support",
		Tools:           []string{"lookup", "lookup"},
		Skills:          []string{"refund"},
		KnowledgeBases:  []string{"group-2"},
		MCPServers:      []string{"search"},
		SmartAttention:  &smartAttention,
		LearningEnabled: &learning,
		AllowLinks:      &allowLinks,
	}})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	rule, ok := registry.Match("qq-native", "1", "group", "2")
	if !ok || len(rule.Tools) != 1 || rule.Tools[0] != "lookup" ||
		len(rule.KnowledgeBases) != 1 || !*rule.LearningEnabled ||
		rule.SmartAttention == nil || !*rule.SmartAttention ||
		rule.AllowLinks == nil || *rule.AllowLinks {
		t.Fatalf("matched rule = %#v", rule)
	}
	if _, ok := registry.Match("qq-native", "1", "group", "3"); ok {
		t.Fatal("binding leaked into another group")
	}
}

func TestBindingMatchesMultipleChatsAndOptionalUsers(t *testing.T) {
	registry, err := NewRegistry([]Rule{{
		Name:     "studio",
		Platform: "*",
		SelfID:   "1",
		ChatType: "group",
		ChatIDs:  []string{"20", "10", "20"},
		UserIDs:  []string{"100", "200"},
		Persona:  "studio",
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, chatID := range []string{"10", "20"} {
		if _, ok := registry.MatchActor("qq-native", "1", "group", chatID, "100"); !ok {
			t.Fatalf("chat %s did not match", chatID)
		}
	}
	if _, ok := registry.MatchActor("qq-native", "1", "group", "30", "100"); ok {
		t.Fatal("binding leaked into an unbound chat")
	}
	if _, ok := registry.MatchActor("qq-native", "1", "group", "10", "300"); ok {
		t.Fatal("binding ignored user_ids")
	}
	listed := registry.List()
	if len(listed) != 1 ||
		len(listed[0].ChatIDs) != 2 ||
		listed[0].ChatIDs[0] != "10" {
		t.Fatalf("normalized binding = %#v", listed)
	}
}

func TestBindingMatchesAllUsersInConfiguredChats(t *testing.T) {
	registry, err := NewRegistry([]Rule{{
		Name:     "studio-groups",
		Platform: "*",
		SelfID:   "1",
		ChatType: "group",
		ChatIDs:  []string{"10", "20"},
		Persona:  "studio",
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, userID := range []string{"100", "200"} {
		if _, ok := registry.MatchActor(
			"qq-native",
			"1",
			"group",
			"20",
			userID,
		); !ok {
			t.Fatalf("bound group rejected user %s", userID)
		}
	}
}

func TestBindingSupportsDirectUserSelector(t *testing.T) {
	registry, err := NewRegistry([]Rule{{
		Name:     "direct-user",
		Platform: "*",
		SelfID:   "1",
		ChatType: "private",
		UserIDs:  []string{"100"},
		Persona:  "assistant",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.MatchActor(
		"qq-native",
		"1",
		"private",
		"arbitrary-chat",
		"100",
	); !ok {
		t.Fatal("direct user binding did not match")
	}
	if _, ok := registry.MatchActor(
		"qq-native",
		"1",
		"private",
		"arbitrary-chat",
		"200",
	); ok {
		t.Fatal("direct user binding leaked to another user")
	}
}

func TestReplyPolicyNormalizesAndSupportsPrivateAIDecision(t *testing.T) {
	policy := ReplyPolicy{
		Mode:    ReplyModeAIDecide,
		History: AttentionHistoryPolicy{Mode: AttentionHistoryLastN},
	}
	registry, err := NewRegistry([]Rule{{
		Name: "private-ai", Platform: "*", SelfID: "1",
		ChatType: "private", ChatID: "2", ReplyPolicy: &policy,
	}})
	if err != nil {
		t.Fatal(err)
	}
	rule, ok := registry.Match("qq-native", "1", "private", "2")
	if !ok || rule.ReplyPolicy == nil ||
		rule.ReplyPolicy.Mode != ReplyModeAIDecide ||
		rule.ReplyPolicy.OnError != ReplyOnErrorIgnore ||
		rule.ReplyPolicy.ConfidenceThreshold != 0.65 ||
		rule.ReplyPolicy.History.Limit != 2 {
		t.Fatalf("normalized policy = %#v", rule.ReplyPolicy)
	}
}

func TestReplyPolicyRejectsAmbiguousLegacyFields(t *testing.T) {
	smart := true
	_, err := NewRegistry([]Rule{{
		Name: "ambiguous", Platform: "*", SelfID: "1", ChatType: "group",
		ChatID: "2", SmartAttention: &smart,
		ReplyPolicy: &ReplyPolicy{Mode: ReplyModeAIDecide},
	}})
	if err == nil {
		t.Fatal("ambiguous reply policy was accepted")
	}
}

func TestLegacyReplyPolicyKeepsPrivateAlwaysAndGroupMention(t *testing.T) {
	rule := Rule{}
	if got := rule.EffectiveReplyPolicy("private", true); got.Mode != ReplyModeAlways {
		t.Fatalf("private legacy policy = %#v", got)
	}
	if got := rule.EffectiveReplyPolicy("group", true); got.Mode != ReplyModeMentionOnly {
		t.Fatalf("group legacy policy = %#v", got)
	}
}
