package persona

import (
	"path/filepath"
	"testing"
)

func TestExamplePersonasLoad(t *testing.T) {
	profiles, defaultName, err := LoadFile(filepath.Join("..", "..", "examples", "personas.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 1 || defaultName != "human-support" || profiles[0].RoutingScope == nil {
		t.Fatalf("example personas = %#v, default = %q", profiles, defaultName)
	}
}
