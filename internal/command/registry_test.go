package command

import (
	"context"
	"testing"

	"github.com/lianyorker/cinlan-qq-bot/internal/platform"
)

func TestParseAndExecuteAlias(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(Definition{
		Name:    "faq",
		Aliases: []string{"question"},
		Handler: func(_ context.Context, input *Context) (Result, error) {
			return Result{Handled: true, Reply: input.Args[0]}, nil
		},
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	result, handled, err := registry.Execute(
		context.Background(),
		platform.Event{},
		"session",
		"/question answer",
		nil,
	)
	if err != nil || !handled || result.Reply != "answer" {
		t.Fatalf("Execute() = %#v, handled=%v, error=%v", result, handled, err)
	}
}
