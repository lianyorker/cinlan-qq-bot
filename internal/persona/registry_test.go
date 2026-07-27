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
