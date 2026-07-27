package tool

import (
	"context"
	"testing"
	"time"
)

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
