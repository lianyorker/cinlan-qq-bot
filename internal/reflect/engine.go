// Package reflect is the memory-keeping engine: while the bot idles it
// tidies what she remembers — merging scattered facts into denser lines,
// condensing stale member impressions into a roster sketch, and writing
// a short daily journal for chats that were active today. Humans do
// this unconsciously ("he seemed tired lately" is three weak memories
// fused into one); without it the fact list just accretes until the
// prompt budget starves the useful entries out.
package reflect

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/agent"
	"github.com/lianyorker/cinlan-qq-bot/internal/binding"
	"github.com/lianyorker/cinlan-qq-bot/internal/chatprefs"
	"github.com/lianyorker/cinlan-qq-bot/internal/domain"
	"github.com/lianyorker/cinlan-qq-bot/internal/persona"
	"github.com/lianyorker/cinlan-qq-bot/internal/provider"
	"github.com/lianyorker/cinlan-qq-bot/internal/session"
)

type Config struct {
	Enabled bool
	Tick    time.Duration // how often the engine sweeps (default 30m)
	// MergeFactsAt consolidates a chat's fact list once it passes this
	// size — below it the raw lines stay granular.
	MergeFactsAt int // default 20
	// MergeImpressionsAt folds the stalest half of a chat's member
	// impressions into roster facts once the map passes this size.
	MergeImpressionsAt int // default 40
	// JournalMaxAge is how recent a chat's activity must be before she
	// bothers writing today's diary — dead chats don't get eulogies.
	JournalMaxAge time.Duration // default 24h
}

type Deps struct {
	// Providers resolves each binding's provider by name; preferred.
	Providers *provider.Registry
	// Agent is the single-provider fallback when Providers is nil.
	Agent     agent.Client
	Bindings  *binding.Registry
	Personas  *persona.Registry
	Sessions  *session.Store
	ChatPrefs *chatprefs.Store
	// Activity returns the last inbound activity time for a chat key.
	Activity func(chatType, chatID string) time.Time
	// Platform names the session-key platform prefix.
	Platform string
	Now      func() time.Time
	Logger   *slog.Logger
}

type Engine struct {
	cfg  Config
	deps Deps
}

func New(cfg Config, deps Deps) *Engine {
	if cfg.Tick <= 0 {
		cfg.Tick = 30 * time.Minute
	}
	if cfg.MergeFactsAt <= 0 {
		cfg.MergeFactsAt = 20
	}
	if cfg.MergeImpressionsAt <= 0 {
		cfg.MergeImpressionsAt = 40
	}
	if cfg.JournalMaxAge <= 0 {
		cfg.JournalMaxAge = 24 * time.Hour
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.Platform == "" {
		deps.Platform = "qq-onebot"
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	return &Engine{cfg: cfg, deps: deps}
}

// Run sweeps on every Tick until ctx is cancelled. Each sweep does two
// jobs: consolidate overgrown memory lists, then journal today's chats.
func (e *Engine) Run(ctx context.Context) {
	if !e.cfg.Enabled {
		return
	}
	timer := time.NewTicker(e.cfg.Tick)
	defer timer.Stop()
	e.deps.Logger.Info("reflect engine started",
		"tick", e.cfg.Tick,
		"merge_facts_at", e.cfg.MergeFactsAt,
		"merge_impressions_at", e.cfg.MergeImpressionsAt,
		"journal_max_age", e.cfg.JournalMaxAge,
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
	if e.deps.ChatPrefs == nil {
		return
	}
	for _, k := range e.deps.ChatPrefs.ChatKeys() {
		e.consolidateFacts(ctx, k[0], k[1], k[2], k[3])
		e.consolidateImpressions(ctx, k[0], k[1], k[2], k[3])
	}
	e.journal(ctx)
	e.selfState(ctx)
}

// reply routes an agent request through the binding's provider (or the
// single fallback), mirroring how proactive opens a line.
func (e *Engine) reply(
	ctx context.Context, rule binding.Rule, req domain.AgentRequest,
) (domain.AgentResponse, error) {
	if e.deps.Providers != nil {
		if name := strings.TrimSpace(rule.Provider); name != "" {
			return e.deps.Providers.ReplyWith(ctx, name, req)
		}
		return e.deps.Providers.Reply(ctx, req)
	}
	if e.deps.Agent != nil {
		return e.deps.Agent.Reply(ctx, req)
	}
	return domain.AgentResponse{}, fmt.Errorf("no agent")
}

// bindingFor finds the rule that serves this chat so consolidation runs
// on the same provider/model the chat actually talks to.
func (e *Engine) bindingFor(platform, selfID, chatType, chatID string) (binding.Rule, bool) {
	if e.deps.Bindings == nil {
		return binding.Rule{}, false
	}
	for _, rule := range e.deps.Bindings.List() {
		if rule.ChatType != chatType {
			continue
		}
		if rule.Platform != "" && rule.Platform != "*" &&
			rule.Platform != platform {
			continue
		}
		// A rule pinned to a specific account only consolidates that
		// account's memory cell — same room, different owner.
		if rs := strings.TrimSpace(rule.SelfID); rs != "" && rs != selfID {
			continue
		}
		for _, id := range rule.ChatIDs {
			if id == chatID {
				return rule, true
			}
		}
	}
	return binding.Rule{}, false
}

// consolidateFacts asks the model to fuse the chat's scattered fact
// lines into fewer, denser entries once the list outgrows the floor.
func (e *Engine) consolidateFacts(ctx context.Context, platform, selfID, chatType, chatID string) {
	facts := e.deps.ChatPrefs.FactTexts(platform, selfID, chatType, chatID)
	if len(facts) <= e.cfg.MergeFactsAt {
		return
	}
	rule, ok := e.bindingFor(platform, selfID, chatType, chatID)
	if !ok {
		return
	}
	// Capture emotional color and recall heat BEFORE the merge so
	// consolidated facts inherit them instead of resetting to zero.
	avgMood, avgWeight := e.deps.ChatPrefs.FactMeta(platform, selfID, chatType, chatID)

	var b strings.Builder
	for i, f := range facts {
		fmt.Fprintf(&b, "%d. %s\n", i+1, f)
	}
	req := domain.AgentRequest{
		RequestID: fmt.Sprintf("reflect-facts-%s-%s-%d",
			chatType, chatID, e.deps.Now().Unix()),
		SessionID: "reflect",
		Text:      b.String(),
		Platform:  platform,
		ChatType:  chatType,
		ChatID:    chatID,
		SystemPrompt: "你在整理自己的记忆本。下面是你在一个聊天里陆续记下的事，零散还有重复。" +
			"把它们合并成更少的条目：同一主题、同一个人的合并成一条；" +
			"保留具体信息（名字、日期、细节、数字），别丢掉任何没重复的信息；" +
			"目标条数是原来的一半左右；别编造记忆里没有的事。" +
			"每条一行，不要序号、不要前缀符号、不要任何解释，只输出条目本身。",
	}
	resp, err := e.reply(ctx, rule, req)
	if err != nil {
		e.deps.Logger.Warn("reflect fact merge failed",
			"chat", chatType+":"+chatID, "error", err.Error())
		return
	}
	merged := parseLines(resp.Reply)
	if len(merged) == 0 || len(merged) >= len(facts) {
		return // model didn't actually shrink the list — keep originals
	}
	if err := e.deps.ChatPrefs.RewriteFacts(platform, selfID, chatType, chatID, merged, avgMood, avgWeight); err != nil {
		e.deps.Logger.Warn("reflect fact rewrite failed",
			"chat", chatType+":"+chatID, "error", err.Error())
		return
	}
	e.deps.Logger.Info("reflect merged facts",
		"chat", chatType+":"+chatID,
		"before", len(facts), "after", len(merged),
		"mood_carry", avgMood, "weight_carry", avgWeight)
}

// consolidateImpressions folds the stalest half of a chat's member
// impressions into compact "群友速写" roster facts once the map exceeds
// the threshold — important members keep their own line, the rest get
// a shared one instead of silently evaporating at the cap.
func (e *Engine) consolidateImpressions(ctx context.Context, platform, selfID, chatType, chatID string) {
	snap := e.deps.ChatPrefs.ImpressionSnapshot(platform, selfID, chatType, chatID)
	if len(snap) <= e.cfg.MergeImpressionsAt {
		return
	}
	rule, ok := e.bindingFor(platform, selfID, chatType, chatID)
	if !ok {
		return
	}
	type aged struct {
		id     string
		text   string
		anchor time.Time
	}
	list := make([]aged, 0, len(snap))
	for u, f := range snap {
		anchor := f.CreatedAt
		if f.LastSeenAt.After(anchor) {
			anchor = f.LastSeenAt
		}
		list = append(list, aged{id: u, text: f.Text, anchor: anchor})
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].anchor.Before(list[j].anchor)
	})
	stale := list[:len(list)/2]
	var b strings.Builder
	for _, s := range stale {
		fmt.Fprintf(&b, "%s: %s\n", s.id, s.text)
	}
	req := domain.AgentRequest{
		RequestID: fmt.Sprintf("reflect-imps-%s-%s-%d",
			chatType, chatID, e.deps.Now().Unix()),
		SessionID: "reflect",
		Text:      b.String(),
		Platform:  platform,
		ChatType:  chatType,
		ChatID:    chatID,
		SystemPrompt: "你在整理自己的记忆本。下面是你对群里一些人的印象（QQ号:印象），" +
			"都是很久没被提起、印象不深的人。把它们浓缩成最多三条群友速写：" +
			"每条概括几个人，带上能认出人的细节（群昵称/特点），" +
			"例如「群友速写：爱抬杠的甲、总在半夜冒泡的乙」。" +
			"每条一行，不要序号不要解释，只输出速写行。",
	}
	resp, err := e.reply(ctx, rule, req)
	if err != nil {
		e.deps.Logger.Warn("reflect impression merge failed",
			"chat", chatType+":"+chatID, "error", err.Error())
		return
	}
	roster := parseLines(resp.Reply)
	if len(roster) == 0 {
		return
	}
	// Keep the fresh half as their own lines; drop the stale half
	// (now represented by the roster facts).
	kept := map[string]string{}
	for _, s := range list[len(list)/2:] {
		kept[s.id] = s.text
	}
	if err := e.deps.ChatPrefs.RewriteImpressions(platform, selfID, chatType, chatID, kept); err != nil {
		e.deps.Logger.Warn("reflect impression rewrite failed",
			"chat", chatType+":"+chatID, "error", err.Error())
		return
	}
	for _, line := range roster {
		_ = e.deps.ChatPrefs.AddFact(platform, selfID, chatType, chatID, line)
	}
	e.deps.Logger.Info("reflect condensed impressions",
		"chat", chatType+":"+chatID,
		"dropped", len(stale), "roster", len(roster))
}

// journal gives every recently-active bound chat one diary fact per
// day — the "今天他情绪低落" recap nobody asked her to write but that
// makes tomorrow's conversation feel continuous.
func (e *Engine) journal(ctx context.Context) {
	if e.deps.Bindings == nil || e.deps.Sessions == nil {
		return
	}
	today := e.deps.Now().Format("01.02")
	prefix := "日记·" + today
	for _, rule := range e.deps.Bindings.List() {
		chatType := rule.ChatType
		plat := rule.Platform
		if plat == "" || plat == "*" {
			plat = e.deps.Platform
		}
		for _, chatID := range rule.ChatIDs {
			selfID := strings.TrimSpace(rule.SelfID)
			if selfID == "" {
				continue
			}
			if e.deps.ChatPrefs.HasFactPrefix(plat, selfID, chatType, chatID, prefix) {
				continue // already wrote today
			}
			if e.deps.Activity != nil {
				last := e.deps.Activity(chatType, chatID)
				if last.IsZero() ||
					e.deps.Now().Sub(last) > e.cfg.JournalMaxAge {
					continue
				}
			}
			sessionID := plat + ":self:" + selfID +
				":" + chatType + ":" + chatID
			history, _ := e.deps.Sessions.Snapshot(sessionID)
			if len(history) < 4 {
				continue // not enough of a day to write about
			}
			e.writeJournal(ctx, rule, plat, selfID, chatType, chatID, history, prefix)
		}
	}
}

// selfState gives each recently-active persona-binding a one-line
// self-appraisal ("状态·最近我变得…") once per day. It reads the latest
// journal entries plus the current mood trajectory and asks the persona
// to notice how she's been changing — the lightest possible self-iteration
// loop that feeds back into prompts without touching the identity red line.
func (e *Engine) selfState(ctx context.Context) {
	if e.deps.Bindings == nil || e.deps.ChatPrefs == nil {
		return
	}
	today := e.deps.Now().Format("01.02")
	statePrefix := "状态·"
	for _, rule := range e.deps.Bindings.List() {
		chatType := rule.ChatType
		plat := rule.Platform
		if plat == "" || plat == "*" {
			plat = e.deps.Platform
		}
		for _, chatID := range rule.ChatIDs {
			selfID := strings.TrimSpace(rule.SelfID)
			if selfID == "" {
				continue
			}
			// One self-state per chat per day.
			if e.deps.ChatPrefs.HasFactPrefix(plat, selfID, chatType, chatID, statePrefix+today) {
				continue
			}
			// Need recent activity.
			if e.deps.Activity != nil {
				last := e.deps.Activity(chatType, chatID)
				if last.IsZero() || e.deps.Now().Sub(last) > e.cfg.JournalMaxAge {
					continue
				}
			}
			// Collect recent diary entries (日记·) as context.
			allTexts := e.deps.ChatPrefs.FactTexts(plat, selfID, chatType, chatID)
			var diaries []string
			for _, t := range allTexts {
				if strings.HasPrefix(t, "日记·") {
					diaries = append(diaries, t)
				}
			}
			if len(diaries) == 0 {
				continue // no diary material to reflect on
			}
			// Keep only the latest 5 diary entries.
			if len(diaries) > 5 {
				diaries = diaries[len(diaries)-5:]
			}
			moodScore := e.deps.ChatPrefs.MoodScore(plat, selfID, chatType, chatID, e.deps.Now())
			e.writeSelfState(ctx, rule, plat, selfID, chatType, chatID, diaries, moodScore, statePrefix+today)
		}
	}
}

func (e *Engine) writeSelfState(
	ctx context.Context,
	rule binding.Rule,
	platform, selfID, chatType, chatID string,
	diaries []string, moodScore int,
	prefix string,
) {
	profile, ok := e.persona(rule.Persona)
	if !ok {
		return
	}
	var b strings.Builder
	b.WriteString("最近几天的日记：\n")
	for _, d := range diaries {
		b.WriteString("· " + d + "\n")
	}
	fmt.Fprintf(&b, "\n当前心情指数：%d（-100到100，负=不开心）\n", moodScore)

	system := profile.SystemPrompt +
		"\n\n（现在你在私下回顾最近自己的变化。根据上面的日记和心情，" +
		"用一句话描述你最近的状态变化——比如'最近更黏某人了''对XX话题腻了'" +
		"'心情一直不太好因为…''跟某某关系变好了'。" +
		"只写一条，自然口语，不要用引号，不要解释，不要写'状态'两个字开头。" +
		"如果真的没什么变化就回 [[SKIP]]）"

	req := domain.AgentRequest{
		RequestID: fmt.Sprintf("reflect-state-%s-%s-%d",
			chatType, chatID, e.deps.Now().Unix()),
		SessionID:    "reflect-state",
		Text:         b.String(),
		Platform:     platform,
		ChatType:     chatType,
		ChatID:       chatID,
		SelfID:       selfID,
		SystemPrompt: system,
	}
	resp, err := e.reply(ctx, rule, req)
	if err != nil {
		e.deps.Logger.Warn("reflect self-state failed",
			"chat", chatType+":"+chatID, "error", err.Error())
		return
	}
	line := strings.TrimSpace(resp.Reply)
	if line == "" || strings.HasPrefix(line, "[[SKIP]]") {
		return
	}
	// Trim to reasonable length.
	if runes := []rune(line); len(runes) > 80 {
		line = string(runes[:80])
	}
	_ = e.deps.ChatPrefs.AddFact(platform, selfID, chatType, chatID, prefix+" "+line)
	e.deps.Logger.Info("reflect self-state written",
		"chat", chatType+":"+chatID, "state", line)
}

func (e *Engine) writeJournal(
	ctx context.Context,
	rule binding.Rule,
	platform, selfID, chatType, chatID string,
	history []domain.ChatMessage,
	prefix string,
) {
	profile, ok := e.persona(rule.Persona)
	if !ok {
		return
	}
	var b strings.Builder
	// Cap the day at the newest 30 lines — enough texture, small bill.
	start := 0
	if len(history) > 30 {
		start = len(history) - 30
	}
	for _, m := range history[start:] {
		role := "对方"
		if m.Role == "assistant" {
			role = "你"
		}
		b.WriteString(role + "：" + m.Content + "\n")
	}
	system := profile.SystemPrompt +
		"\n\n（现在是你私下写日记的时间。看你们今天聊的这些，" +
		"写1-2条值得记住的事——他对什么上头、情绪怎么样、你们之间的新梗。" +
		"每条一行直接写内容，别加日期别加引号；没什么值得记就只回 [[SKIP]]）"
	req := domain.AgentRequest{
		RequestID: fmt.Sprintf("reflect-journal-%s-%s-%d",
			chatType, chatID, e.deps.Now().Unix()),
		SessionID:    "reflect-journal",
		Text:         b.String(),
		Platform:     platform,
		ChatType:     chatType,
		ChatID:       chatID,
		SelfID:       strings.TrimSpace(rule.SelfID),
		SystemPrompt: system,
	}
	resp, err := e.reply(ctx, rule, req)
	if err != nil {
		e.deps.Logger.Warn("reflect journal failed",
			"chat", chatType+":"+chatID, "error", err.Error())
		return
	}
	lines := parseLines(resp.Reply)
	for _, line := range lines {
		if strings.HasPrefix(line, "[[SKIP]]") {
			continue
		}
		if len(line) > 120 {
			line = string([]rune(line)[:120])
		}
		_ = e.deps.ChatPrefs.AddFact(platform, selfID, chatType, chatID, prefix+" "+line)
	}
	if len(lines) > 0 {
		e.deps.Logger.Info("reflect journaled",
			"chat", chatType+":"+chatID, "lines", len(lines))
	}
}

func (e *Engine) persona(name string) (persona.Profile, bool) {
	if e.deps.Personas == nil {
		return persona.Profile{}, false
	}
	if name != "" {
		return e.deps.Personas.Get(name)
	}
	return e.deps.Personas.Default()
}

// parseLines turns the model's one-line-per-entry output back into
// clean entries, tolerating stray bullets and numbering.
func parseLines(text string) []string {
	var out []string
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		line = strings.TrimLeft(line, "-·*• ")
		if i := strings.Index(line, "."); i >= 0 && i <= 3 {
			if _, err := strconv.Atoi(strings.TrimSpace(line[:i])); err == nil {
				line = strings.TrimSpace(line[i+1:])
			}
		}
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}
