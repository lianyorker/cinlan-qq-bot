package tool

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
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
	// Notify is invoked right after a request is admitted, before the
	// handler runs. used is the accepted-call count inside the window.
	Notify func(toolName string, actor Actor, used, limit int)

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
		release, refund, response := l.acquire(call.Actor)
		if response != nil {
			return Result{Response: response}, nil
		}
		defer release()
		if l.Notify != nil {
			l.Notify(definition.Name, call.Actor, l.usedCount(call.Actor), l.limit)
		}
		result, err := handler(ctx, call)
		if err != nil || result.Error != "" {
			reason := result.Error
			if err != nil {
				reason = err.Error()
			}
			slog.Default().Warn("media tool failed",
				"tool", definition.Name,
				"user", call.Actor.UserID,
				"error", reason,
			)
			refund()
			return Result{Response: terminalMediaResponse(
				l.failureText(definition.Name, reason),
			)}, nil
		}
		return result, nil
	}
	return definition
}

// failureText picks a user-facing failure notice per media tool, refined by
// the underlying error so a policy block or timeout doesn't read like a
// generic crash.
func (l *MediaLimiter) failureText(name, reason string) string {
	lower := strings.ToLower(reason)
	switch {
	// Upstream moderation errors arrive in both languages:
	// "content policy", "审核拒绝", "违规" — treat them all as a
	// blocked description so she can offer a safer rephrase.
	case strings.Contains(lower, "policy") ||
		strings.Contains(lower, "prohibited") ||
		strings.Contains(lower, "sensitive") ||
		strings.Contains(lower, "moderation") ||
		strings.Contains(lower, "reject") ||
		strings.Contains(lower, "blocked") ||
		strings.Contains(lower, "unsafe") ||
		strings.Contains(lower, "审核") ||
		strings.Contains(lower, "拒绝") ||
		strings.Contains(lower, "违规"):
		return "这个描述被拦了，换个说法我再试试"
	case strings.Contains(lower, "timeout") ||
		strings.Contains(lower, "deadline") ||
		strings.Contains(lower, "connection") ||
		strings.Contains(lower, "download"):
		return "生图服务超时了，等会再试"
	case strings.Contains(lower, "quota") ||
		strings.Contains(lower, "insufficient"):
		return "生图额度用完了，明天再来"
	}
	switch name {
	case "generate_image":
		return "生图失败了，稍后再试试～"
	default:
		return "生成失败了，稍后再试试～"
	}
}

func (l *MediaLimiter) usedCount(actor Actor) int {
	key := actorKey(actor)
	l.mu.Lock()
	defer l.mu.Unlock()
	state := l.actors[key]
	if state == nil {
		return 0
	}
	l.pruneActorLocked(l.now(), state)
	return len(state.accepted)
}

func actorKey(actor Actor) string {
	if actor.UserID == "" {
		return actor.Platform + ":session:" + actor.SessionID
	}
	return actor.Platform + ":self:" + actor.SelfID + ":user:" + actor.UserID
}

func (l *MediaLimiter) acquire(actor Actor) (func(), func(), *domain.AgentResponse) {
	now := l.now()
	key := actorKey(actor)

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
		return nil, nil, terminalMediaResponse("操作太频繁了，稍等一会再试。")
	}
	if l.limit > 0 && len(state.accepted) >= l.limit {
		return nil, nil, terminalMediaResponse(fmt.Sprintf(
			"今天的次数用完啦（%d/%d），明天再来。", l.limit, l.limit))
	}
	if l.active >= l.maxConcurrent {
		return nil, nil, terminalMediaResponse("当前图片任务有点多，稍后再试。")
	}
	state.accepted = append(state.accepted, now)
	l.active++
	release := func() {
		l.mu.Lock()
		if l.active > 0 {
			l.active--
		}
		l.mu.Unlock()
	}
	// refund removes the accepted stamp so failed calls don't burn quota.
	refund := func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		current := l.actors[key]
		if current == nil || len(current.accepted) == 0 {
			return
		}
		current.accepted = current.accepted[:len(current.accepted)-1]
	}
	return release, refund, nil
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
