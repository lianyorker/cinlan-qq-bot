package tool

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMediaLimiterSharesCooldownAcrossMediaTools(t *testing.T) {
	limiter := NewMediaLimiter(MediaLimiterConfig{
		Cooldown:      time.Minute,
		Limit:         10,
		Window:        time.Hour,
		MaxConcurrent: 2,
	})
	now := time.Unix(100, 0)
	limiter.now = func() time.Time { return now }
	var calls atomic.Int32
	handler := func(context.Context, Call) (Result, error) {
		calls.Add(1)
		return Result{}, nil
	}
	first := limiter.Wrap(Definition{Name: "generate_image", Handler: handler})
	second := limiter.Wrap(Definition{Name: "capture_webpage", Handler: handler})
	actor := Actor{Platform: "qq", SelfID: "bot", UserID: "user", ChatID: "group-a"}
	if _, err := first.Handler(context.Background(), Call{Actor: actor}); err != nil {
		t.Fatal(err)
	}
	result, err := second.Handler(context.Background(), Call{Actor: actor})
	if err != nil {
		t.Fatal(err)
	}
	if result.Response == nil || result.Response.Reply != "操作太频繁了，稍等一会再试。" {
		t.Fatalf("second result = %#v", result)
	}
	if calls.Load() != 1 {
		t.Fatalf("handler calls = %d, want 1", calls.Load())
	}
}

func TestMediaLimiterWindowAndConcurrency(t *testing.T) {
	limiter := NewMediaLimiter(MediaLimiterConfig{
		Cooldown:      0,
		Limit:         2,
		Window:        time.Minute,
		MaxConcurrent: 1,
	})
	now := time.Unix(100, 0)
	limiter.now = func() time.Time { return now }
	entered := make(chan struct{})
	release := make(chan struct{})
	var enteredOnce sync.Once
	handler := func(ctx context.Context, _ Call) (Result, error) {
		enteredOnce.Do(func() { close(entered) })
		select {
		case <-release:
			return Result{}, nil
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	}
	definition := limiter.Wrap(Definition{Name: "media", Handler: handler})
	actor := Actor{
		Platform: "qq",
		SelfID:   "bot",
		UserID:   "user",
	}
	firstDone := make(chan struct{})
	go func() {
		_, _ = definition.Handler(context.Background(), Call{Actor: actor})
		close(firstDone)
	}()
	<-entered
	third, err := definition.Handler(context.Background(), Call{Actor: actor})
	if err != nil {
		t.Fatal(err)
	}
	if third.Response == nil || third.Response.Reply != "当前图片任务有点多，稍后再试。" {
		t.Fatalf("concurrency result = %#v", third)
	}
	close(release)
	<-firstDone
	now = now.Add(2 * time.Minute)
	result, err := definition.Handler(context.Background(), Call{
		Actor:     actor,
		Arguments: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Response != nil {
		t.Fatalf("window did not recover: %#v", result)
	}
}
