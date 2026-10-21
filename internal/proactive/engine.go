// Package proactive is the ambient opener engine: when a bound group has
// been quiet long enough, the persona may decide to say something on its
// own. This is what makes the bot feel like a member, not a vending
// machine — gated by idle time, per-chat cooldown, a daily cap and the
// operator sleep window so it never turns into spam.
package proactive

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/agent"
	"github.com/lianyorker/cinlan-qq-bot/internal/binding"
	"github.com/lianyorker/cinlan-qq-bot/internal/chatprefs"
	"github.com/lianyorker/cinlan-qq-bot/internal/domain"
	"github.com/lianyorker/cinlan-qq-bot/internal/message"
	"github.com/lianyorker/cinlan-qq-bot/internal/mood"
	"github.com/lianyorker/cinlan-qq-bot/internal/persona"
	"github.com/lianyorker/cinlan-qq-bot/internal/platform"
	"github.com/lianyorker/cinlan-qq-bot/internal/provider"
	"github.com/lianyorker/cinlan-qq-bot/internal/session"
)

type Config struct {
	Enabled    bool
	Tick       time.Duration // how often to evaluate chats (default 5m)
	MinIdle    time.Duration // chat must be this quiet first (default 45m)
	Cooldown   time.Duration // between two proactive sends (default 4h)
	Chance     float64       // probability per eligible tick, 0-1 (default 0.12)
	DailyLimit int           // per-chat daily cap (default 5)
	SleepStart int           // hour-of-day sleep window start, -1 disables
	SleepEnd   int           // hour-of-day sleep window end
}

type Deps struct {
	// Providers resolves each binding's provider by name; preferred.
	Providers *provider.Registry
	// Agent is the single-provider fallback when Providers is nil.
	Agent     agent.Client
	Send      func(context.Context, platform.Outbound) error
	Bindings  *binding.Registry
	Personas  *persona.Registry
	Sessions  *session.Store
	ChatPrefs *chatprefs.Store
	// Activity returns the last inbound activity time for a chat key.
	Activity func(chatType, chatID string) time.Time
	// Touch records an outbound activity so the engine doesn't refire.
	Touch func(chatType, chatID string)
	// Platform names the session-key platform prefix (default qq-onebot).
	Platform string
	// DefaultSelfID fills rules that carry no explicit self_id.
	DefaultSelfID string
	Now           func() time.Time
	Logger        *slog.Logger
	Rand          *rand.Rand
}

type Engine struct {
	cfg  Config
	deps Deps
	mu   sync.Mutex
	sent map[string]time.Time // chatKey -> last proactive send
	day  string              // yyyy-mm-dd of counters
	hits map[string]int      // chatKey -> proactive sends today
}

func New(cfg Config, deps Deps) *Engine {
	if cfg.Tick <= 0 {
		cfg.Tick = 5 * time.Minute
	}
	if cfg.MinIdle <= 0 {
		cfg.MinIdle = 45 * time.Minute
	}
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = 4 * time.Hour
	}
	if cfg.Chance <= 0 {
		cfg.Chance = 0.12
	}
	if cfg.DailyLimit <= 0 {
		cfg.DailyLimit = 5
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.Platform == "" {
		deps.Platform = "qq-onebot"
	}
	if deps.Rand == nil {
		deps.Rand = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	return &Engine{
		cfg:  cfg,
		deps: deps,
		sent: map[string]time.Time{},
		day:  deps.Now().Format("2006-01-02"),
		hits: map[string]int{},
	}
}

// Run evaluates all bound group chats every Tick until ctx is cancelled.
func (e *Engine) Run(ctx context.Context) {
	if !e.cfg.Enabled {
		return
	}
	timer := time.NewTicker(e.cfg.Tick)
	defer timer.Stop()
	e.deps.Logger.Info("proactive engine started",
		"tick", e.cfg.Tick,
		"min_idle", e.cfg.MinIdle,
		"cooldown", e.cfg.Cooldown,
		"chance", e.cfg.Chance,
		"daily_limit", e.cfg.DailyLimit,
	)
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			e.sweep(ctx)
		}
	}
}

func (e *Engine) sweep(ctx context.Context) {
	if e.asleep() {
		return
	}
	if e.deps.Bindings == nil {
		return
	}
	for _, rule := range e.deps.Bindings.List() {
		if rule.ChatType != platform.ChatGroup || len(rule.ChatIDs) == 0 {
			continue
		}
		mode := ""
		if rule.ReplyPolicy != nil {
			mode = rule.ReplyPolicy.Mode
		}
		if mode != binding.ReplyModeAIDecide && mode != binding.ReplyModeAlways {
			continue
		}
		for _, chatID := range rule.ChatIDs {
			if !e.eligible(chatID) {
				continue
			}
			// Mood gates the urge to talk: sulking chats stay silent,
			// cheerful chats get chatty.
			if e.deps.ChatPrefs != nil {
				moodScore := e.deps.ChatPrefs.MoodScore(
					platform.ChatGroup, chatID, e.deps.Now())
				if moodScore <= -30 {
					continue
				}
			}
			chance := e.cfg.Chance
			if e.deps.ChatPrefs != nil &&
				e.deps.ChatPrefs.MoodScore(platform.ChatGroup, chatID, e.deps.Now()) >= 30 {
				chance = min(chance*1.5, 0.5)
			}
			if e.deps.Rand.Float64() < chance {
				e.fire(ctx, rule, chatID)
			}
		}
	}
}

func (e *Engine) asleep() bool {
	if e.cfg.SleepStart < 0 {
		return false
	}
	hour := e.deps.Now().Hour()
	if e.cfg.SleepStart <= e.cfg.SleepEnd {
		return hour >= e.cfg.SleepStart && hour < e.cfg.SleepEnd
	}
	return hour >= e.cfg.SleepStart || hour < e.cfg.SleepEnd
}

func (e *Engine) eligible(chatID string) bool {
	now := e.deps.Now()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.day != now.Format("2006-01-02") {
		e.day = now.Format("2006-01-02")
		e.hits = map[string]int{}
	}
	if e.hits[chatID] >= e.cfg.DailyLimit {
		return false
	}
	if last, ok := e.sent[chatID]; ok && now.Sub(last) < e.cfg.Cooldown {
		return false
	}
	if e.deps.ChatPrefs != nil {
		if !e.deps.ChatPrefs.NotifyEnabled(platform.ChatGroup, chatID) {
			return false
		}
		if e.deps.ChatPrefs.Muted(platform.ChatGroup, chatID, now) {
			return false
		}
	}
	if e.deps.Activity != nil {
		last := e.deps.Activity(platform.ChatGroup, chatID)
		if last.IsZero() {
			return false // never saw the chat this boot — don't blind-fire
		}
		if now.Sub(last) < e.cfg.MinIdle {
			return false // still lively
		}
	}
	return true
}

func (e *Engine) fire(ctx context.Context, rule binding.Rule, chatID string) {
	e.mu.Lock()
	e.sent[chatID] = e.deps.Now()
	e.hits[chatID]++
	e.mu.Unlock()

	if e.deps.Providers == nil && e.deps.Agent == nil || e.deps.Send == nil {
		return
	}
	profile, ok := e.profile(rule.Persona)
	if !ok {
		return
	}
	selfID := strings.TrimSpace(rule.SelfID)
	if selfID == "" {
		selfID = e.deps.DefaultSelfID
	}
	sessionID := e.deps.Platform + ":self:" + selfID + ":group:" + chatID
	var history []domain.ChatMessage
	if e.deps.Sessions != nil {
		history, _ = e.deps.Sessions.Snapshot(sessionID)
	}
	idle := e.cfg.MinIdle
	if e.deps.Activity != nil {
		if last := e.deps.Activity(platform.ChatGroup, chatID); !last.IsZero() {
			idle = e.deps.Now().Sub(last).Round(time.Minute)
		}
	}
	system := profile.SystemPrompt +
		"\n\n（主动开口模式：群里已经安静了 " + idle.String() + "，你现在想插一句就说，" +
		"一两句话、像随手发的，别@人、别叫名字、别总结、别提这是系统让你说的；" +
		"实在没话说就只回 [[SKIP]] 一个字）"
	if e.deps.ChatPrefs != nil {
		if hint := mood.Hint(e.deps.ChatPrefs.MoodScore(
			platform.ChatGroup, chatID, e.deps.Now())); hint != "" {
			system += "\n" + hint
		}
	}
	req := domain.AgentRequest{
		RequestID:  fmt.Sprintf("proactive-%s-%d", chatID, e.deps.Now().Unix()),
		SessionID:  sessionID,
		Text:       "（没人说话，你想说点什么就说）",
		Platform:   e.deps.Platform,
		ChatType:   platform.ChatGroup,
		ChatID:     chatID,
		GroupID:    chatID,
		SelfID:     selfID,
		SenderName: "",
		History:    history,
		SystemPrompt: system,
	}
	var (
		resp domain.AgentResponse
		err  error
	)
	if e.deps.Providers != nil {
		if name := strings.TrimSpace(rule.Provider); name != "" {
			resp, err = e.deps.Providers.ReplyWith(ctx, name, req)
		} else {
			resp, err = e.deps.Providers.Reply(ctx, req)
		}
	} else {
		resp, err = e.deps.Agent.Reply(ctx, req)
	}
	if err != nil {
		e.deps.Logger.Warn("proactive generation failed",
			"chat_id", chatID, "error", err.Error())
		return
	}
	text := strings.TrimSpace(resp.Reply)
	if text == "" || strings.HasPrefix(text, "[[SKIP]]") {
		return
	}
	// Never let a proactive message @ someone or quote — ambient only.
	if err := e.deps.Send(ctx, platform.Outbound{
		ChatType: platform.ChatGroup,
		ChatID:   chatID,
		SelfID:   selfID,
		Chain:    message.Chain{message.Text(text)},
	}); err != nil {
		e.deps.Logger.Warn("proactive send failed",
			"chat_id", chatID, "error", err.Error())
		return
	}
	if e.deps.Touch != nil {
		e.deps.Touch(platform.ChatGroup, chatID)
	}
	// Drop it into session history so later replies see our own line.
	if e.deps.Sessions != nil {
		e.deps.Sessions.AddExchange(sessionID, "（主动开口）", text)
	}
	e.deps.Logger.Info("proactive message sent",
		"binding", rule.Name, "chat_id", chatID, "text", text)
}

func (e *Engine) profile(name string) (persona.Profile, bool) {
	if e.deps.Personas == nil {
		return persona.Profile{}, false
	}
	if name != "" {
		return e.deps.Personas.Get(name)
	}
	return e.deps.Personas.Default()
}
