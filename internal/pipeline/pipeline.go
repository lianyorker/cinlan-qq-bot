package pipeline

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/lianyorker/cinlan-qq-bot/internal/platform"
)

type Context struct {
	Event   platform.Event
	Values  map[string]any
	Stopped bool
	Reason  string
}

func NewContext(event platform.Event) *Context {
	return &Context{
		Event:  event,
		Values: make(map[string]any),
	}
}

func (c *Context) Stop(reason string) {
	c.Stopped = true
	c.Reason = reason
}

type Stage interface {
	Name() string
	Handle(context.Context, *Context) error
}

type StageFunc struct {
	StageName string
	Handler   func(context.Context, *Context) error
}

func (s StageFunc) Name() string {
	return s.StageName
}

func (s StageFunc) Handle(ctx context.Context, event *Context) error {
	if s.Handler == nil {
		return errors.New("pipeline stage has no handler")
	}
	return s.Handler(ctx, event)
}

type Pipeline struct {
	mu     sync.RWMutex
	stages []Stage
}

func New(stages ...Stage) *Pipeline {
	p := &Pipeline{}
	for _, stage := range stages {
		if stage != nil {
			p.stages = append(p.stages, stage)
		}
	}
	return p
}

func (p *Pipeline) Add(stage Stage) {
	if stage == nil {
		return
	}
	p.mu.Lock()
	p.stages = append(p.stages, stage)
	p.mu.Unlock()
}

func (p *Pipeline) Names() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	names := make([]string, len(p.stages))
	for index, stage := range p.stages {
		names[index] = stage.Name()
	}
	return names
}

func (p *Pipeline) Run(ctx context.Context, event *Context) error {
	if event == nil {
		return errors.New("pipeline context is nil")
	}
	p.mu.RLock()
	stages := append([]Stage(nil), p.stages...)
	p.mu.RUnlock()
	for index, stage := range stages {
		if event.Stopped {
			break
		}
		if err := stage.Handle(ctx, event); err != nil {
			return fmt.Errorf("pipeline stage %q (%d): %w", stage.Name(), index, err)
		}
	}
	return nil
}
