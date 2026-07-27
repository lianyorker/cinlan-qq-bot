package pipeline

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/lianyorker/cinlan-qq-bot/internal/platform"
)

func TestPipelineRunsInOrderAndStops(t *testing.T) {
	var order []string
	p := New(
		StageFunc{StageName: "wake", Handler: func(context.Context, *Context) error {
			order = append(order, "wake")
			return nil
		}},
		StageFunc{StageName: "command", Handler: func(_ context.Context, event *Context) error {
			order = append(order, "command")
			event.Stop("handled")
			return nil
		}},
		StageFunc{StageName: "agent", Handler: func(context.Context, *Context) error {
			order = append(order, "agent")
			return nil
		}},
	)
	if err := p.Run(context.Background(), NewContext(platform.Event{})); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !reflect.DeepEqual(order, []string{"wake", "command"}) {
		t.Fatalf("stage order = %#v", order)
	}
}

func TestPipelineReturnsStageError(t *testing.T) {
	want := errors.New("boom")
	p := New(StageFunc{StageName: "fail", Handler: func(context.Context, *Context) error {
		return want
	}})
	err := p.Run(context.Background(), NewContext(platform.Event{}))
	if err == nil || !errors.Is(err, want) {
		t.Fatalf("Run() error = %v, want wrapped boom", err)
	}
}
