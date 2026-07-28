package tool

import (
	"context"
	"sync"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/domain"
	"github.com/lianyorker/cinlan-qq-bot/internal/message"
)

type MediaLimiterConfig struct {
	Cooldown      time.Duration
	Limit         int
	Window        time.Duration
	MaxConcurrent int
}

type MediaLimiter struct {
	cooldown      time.Duration
	limit         int
	window        time.Duration
	maxConcurrent int
	now           func() time.Time

	mu       sync.Mutex
	actors   map[string]*mediaActor
	active   int
	sequence uint64
}

type mediaActor struct {
	accepted []time.Time
}

func NewMediaLimiter(cfg MediaLimiterConfig) *MediaLimiter {
	maxConcurrent := cfg.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	return &MediaLimiter{
		cooldown:      cfg.Cooldown,
		limit:         cfg.Limit,
		window:        cfg.Window,
		maxConcurrent: maxConcurrent,
		now:           time.Now,
		actors:        make(map[string]*mediaActor),
	}
}

func (l *MediaLimiter) Wrap(definition Definition) Definition {
	if l == nil {
		return definition
	}
	handler := definition.Handler
	definition.Handler = func(
		ctx context.Context,
		call Call,
	) (Result, error) {
		release, response := l.acquire(call.Actor)
		if response != nil {
			return Result{Response: response}, nil
		}
		defer release()
		return handler(ctx, call)
	}
	return definition
}

func (l *MediaLimiter) acquire(actor Actor) (func(), *domain.AgentResponse) {
	now := l.now()
	key := actor.Platform + ":self:" + actor.SelfID + ":user:" + actor.UserID
	if actor.UserID == "" {
		key = actor.Platform + ":session:" + actor.SessionID
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	state := l.actors[key]
	if state == nil {
		state = &mediaActor{}
		l.actors[key] = state
	}
	l.pruneActorLocked(now, state)
	l.sequence++
	if l.sequence%64 == 0 || len(l.actors) >= 256 {
		l.pruneLocked(now, key)
	}
	if l.cooldown > 0 && len(state.accepted) > 0 &&
		now.Sub(state.accepted[len(state.accepted)-1]) < l.cooldown {
		return nil, terminalMediaResponse("操作太频繁了，稍等一会再试。")
	}
	if l.limit > 0 && len(state.accepted) >= l.limit {
		return nil, terminalMediaResponse("这一时间段的操作次数已用完，晚点再试。")
	}
	if l.active >= l.maxConcurrent {
		return nil, terminalMediaResponse("当前图片任务有点多，稍后再试。")
	}
	state.accepted = append(state.accepted, now)
	l.active++
	return func() {
		l.mu.Lock()
		if l.active > 0 {
			l.active--
		}
		l.mu.Unlock()
	}, nil
}

func (l *MediaLimiter) pruneActorLocked(now time.Time, state *mediaActor) {
	if l.window <= 0 {
		return
	}
	first := 0
	for first < len(state.accepted) &&
		now.Sub(state.accepted[first]) >= l.window {
		first++
	}
	if first > 0 {
		state.accepted = append([]time.Time(nil), state.accepted[first:]...)
	}
}

func (l *MediaLimiter) pruneLocked(now time.Time, currentKey string) {
	for key, state := range l.actors {
		if key == currentKey {
			continue
		}
		l.pruneActorLocked(now, state)
		if len(state.accepted) == 0 {
			delete(l.actors, key)
		}
	}
}

func terminalMediaResponse(text string) *domain.AgentResponse {
	return &domain.AgentResponse{
		Reply: text,
		Chain: message.Chain{message.Text(text)},
	}
}
