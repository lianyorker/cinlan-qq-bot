package tool

import (
	"context"
	"errors"
	"testing"
	"time"
)

type testGuard struct {
	calls int
	err   error
}

func (g *testGuard) Check(context.Context, Call) error {
	g.calls++
	return g.err
}

func TestRegistryValidatesArgumentsAndPermissions(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(Definition{
		Name:       "lookup",
		Permission: PermissionAdmin,
		Timeout:    time.Second,
		Handler: func(_ context.Context, call Call) (Result, error) {
			return Result{Content: string(call.Arguments)}, nil
		},
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if _, err := registry.Execute(context.Background(), Call{
		Name:      "lookup",
		Arguments: []byte(`{}`),
		Actor:     Actor{Role: "member"},
	}); err == nil {
		t.Fatal("member unexpectedly executed admin tool")
	}
	result, err := registry.Execute(context.Background(), Call{
		Name:      "lookup",
		Arguments: []byte(`{"q":"x"}`),
		Actor:     Actor{Role: "admin"},
	})
	if err != nil || result.Error != "" {
		t.Fatalf("Execute() = %#v, %v", result, err)
	}
}

func TestRegistryGuardAppliesToSelectedRegistries(t *testing.T) {
	registry := NewRegistry()
	guard := &testGuard{err: ErrPermissionDenied}
	registry.SetGuard(guard)
	if err := registry.Register(Definition{
		Name: "lookup",
		Handler: func(context.Context, Call) (Result, error) {
			t.Fatal("guarded handler was called")
			return Result{}, nil
		},
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	selected := registry.Select([]string{"lookup"})
	_, err := selected.Execute(context.Background(), Call{Name: "lookup"})
	if !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("Execute() error = %v, want permission denial", err)
	}
	if guard.calls != 1 {
		t.Fatalf("guard calls = %d, want 1", guard.calls)
	}
}

func TestRegistryGuardRunsBeforeRegistrationAndRoleChecks(t *testing.T) {
	registry := NewRegistry()
	guard := &testGuard{err: ErrPermissionDenied}
	registry.SetGuard(guard)
	if err := registry.Register(Definition{
		Name:       "admin_shell",
		Permission: PermissionAdmin,
		Handler: func(context.Context, Call) (Result, error) {
			t.Fatal("guarded handler was called")
			return Result{}, nil
		},
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	for _, call := range []Call{
		{Name: "unregistered_shell", Arguments: []byte(`{`)},
		{Name: "admin_shell", Actor: Actor{Role: "member"}},
	} {
		if _, err := registry.Execute(context.Background(), call); !errors.Is(err, ErrPermissionDenied) {
			t.Fatalf("Execute(%q) error = %v", call.Name, err)
		}
	}
	if guard.calls != 2 {
		t.Fatalf("guard calls = %d, want 2", guard.calls)
	}
}

func TestSelectScopedAllowsOnlyDeclaredResources(t *testing.T) {
	registry := NewRegistry()
	for _, definition := range []Definition{
		{Name: "builtin", Handler: testHandler},
		{Name: "mcp_search_query", Source: SourceMCP, SourceName: "search", Handler: testHandler},
		{Name: "mcp_files_read", Source: SourceMCP, SourceName: "files", Handler: testHandler},
		{Name: "read_skill", Source: SourceSkill, Handler: testHandler},
	} {
		if err := registry.Register(definition); err != nil {
			t.Fatalf("Register() error = %v", err)
		}
	}
	selected := registry.SelectScoped(Selection{
		Names:       []string{"builtin"},
		MCPServers:  []string{"search"},
		AllowSkills: true,
	})
	for _, name := range []string{"builtin", "mcp_search_query", "read_skill"} {
		if !selected.Has(name) {
			t.Fatalf("selected registry is missing %q", name)
		}
	}
	if selected.Has("mcp_files_read") {
		t.Fatal("tool from another MCP server leaked into selection")
	}
}

func testHandler(context.Context, Call) (Result, error) {
	return Result{}, nil
}
