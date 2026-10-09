package persona

import "testing"

func TestRegistrySelectsDefault(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(Profile{Name: "support", SystemPrompt: "support"}); err != nil {
		t.Fatalf("Register(support): %v", err)
	}
	if err := registry.Register(Profile{Name: "sales", SystemPrompt: "sales"}); err != nil {
		t.Fatalf("Register(sales): %v", err)
	}
	if err := registry.SetDefault("sales"); err != nil {
		t.Fatalf("SetDefault(): %v", err)
	}
	profile, ok := registry.Default()
	if !ok || profile.Name != "sales" {
		t.Fatalf("Default() = %#v, %v", profile, ok)
	}
}

func TestRegistryNormalizesAndClonesRoutingScope(t *testing.T) {
	registry := NewRegistry()
	profile := Profile{
		Name: "support", SystemPrompt: "answer prompt",
		RoutingScope: &RoutingScope{
			Description:   " support only ",
			IncludeTopics: []string{"refund", "refund"},
			ExcludeTopics: []string{"chatter"},
		},
	}
	if err := registry.Register(profile); err != nil {
		t.Fatal(err)
	}
	got, ok := registry.Get("support")
	if !ok || got.RoutingScope == nil ||
		got.RoutingScope.Description != "support only" ||
		len(got.RoutingScope.IncludeTopics) != 1 {
		t.Fatalf("routing scope = %#v", got.RoutingScope)
	}
	got.RoutingScope.IncludeTopics[0] = "mutated"
	again, _ := registry.Get("support")
	if again.RoutingScope.IncludeTopics[0] != "refund" {
		t.Fatal("routing scope leaked a mutable registry slice")
	}
}
