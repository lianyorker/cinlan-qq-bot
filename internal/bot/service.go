package bot

import (
	"context"
	"fmt"
	"hash/fnv"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/lianyorker/cinlan-qq-bot/internal/agent"
	"github.com/lianyorker/cinlan-qq-bot/internal/binding"
	"github.com/lianyorker/cinlan-qq-bot/internal/command"
	"github.com/lianyorker/cinlan-qq-bot/internal/config"
	"github.com/lianyorker/cinlan-qq-bot/internal/message"
	"github.com/lianyorker/cinlan-qq-bot/internal/onebot"
	"github.com/lianyorker/cinlan-qq-bot/internal/persona"
	"github.com/lianyorker/cinlan-qq-bot/internal/pipeline"
	"github.com/lianyorker/cinlan-qq-bot/internal/platform"
	"github.com/lianyorker/cinlan-qq-bot/internal/plugin"
	"github.com/lianyorker/cinlan-qq-bot/internal/provider"
	"github.com/lianyorker/cinlan-qq-bot/internal/session"
	"github.com/lianyorker/cinlan-qq-bot/internal/tool"
)

const (
	workerQueueSize      = 64
	maintenanceQueueSize = 64
)

type Sender interface {
	Send(context.Context, platform.Outbound) error
}

type Stats struct {
	Received             uint64 `json:"received"`
	Ignored              uint64 `json:"ignored"`
	Processed            uint64 `json:"processed"`
	Replied              uint64 `json:"replied"`
	AgentErrors          uint64 `json:"agent_errors"`
	SendErrors           uint64 `json:"send_errors"`
	RateLimited          uint64 `json:"rate_limited"`
	AttentionDecisions   uint64 `json:"attention_decisions"`
	AttentionReplies     uint64 `json:"attention_replies"`
	AttentionIgnored     uint64 `json:"attention_ignored"`
	AttentionErrors      uint64 `json:"attention_errors"`
	AttentionRateLimited uint64 `json:"attention_rate_limited"`
	HandoffCount         uint64 `json:"handoff_count"`
}

type counters struct {
	received             atomic.Uint64
	ignored              atomic.Uint64
	processed            atomic.Uint64
	replied              atomic.Uint64
	agentErrors          atomic.Uint64
	sendErrors           atomic.Uint64
	rateLimited          atomic.Uint64
	attentionDecisions   atomic.Uint64
	attentionReplies     atomic.Uint64
	attentionIgnored     atomic.Uint64
	attentionErrors      atomic.Uint64
	attentionRateLimited atomic.Uint64
	handoffCount         atomic.Uint64
}

type Service struct {
	cfg          config.Config
	agent        agent.Client
	sender       Sender
	sessions     *session.Store
	logger       *slog.Logger
	stats        counters
	now          func() time.Time
	providers    *provider.Registry
	plugins      *plugin.Registry
	messageGuard plugin.BeforeHook
	commands     *command.Registry
	tools        *tool.Registry
	personas     *persona.Registry
	bindings     *binding.Registry

	stateMu          sync.Mutex
	lastAccepted     map[string]time.Time
	requestTimes     map[string][]time.Time
	attentionTimes   map[string][]time.Time
	seenMessages     map[string]time.Time
	dedupeCounter    uint64
	rateCounter      uint64
	attentionCounter uint64

	flow *pipeline.Pipeline

	maintenance        chan flowState
	maintenanceRunning atomic.Bool
	maintenanceMu      sync.Mutex
	maintenancePending map[string]struct{}
}

func New(
	cfg config.Config,
	agentClient agent.Client,
	sender Sender,
	sessions *session.Store,
	logger *slog.Logger,
) *Service {
	providers := provider.NewRegistry()
	if agentClient != nil {
		_ = providers.Register(provider.Wrap("default", "agent", agentClient))
	}
	plugins := plugin.NewRegistry()
	return NewWithRuntime(cfg, agentClient, sender, sessions, logger, providers, plugins)
}

func NewWithRuntime(
	cfg config.Config,
	agentClient agent.Client,
	sender Sender,
	sessions *session.Store,
	logger *slog.Logger,
	providers *provider.Registry,
	plugins *plugin.Registry,
) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.AttentionTimeout <= 0 {
		cfg.AttentionTimeout = 5 * time.Second
	}
	if cfg.AttentionRateWindow <= 0 {
		cfg.AttentionRateWindow = time.Minute
	}
	if providers == nil {
		providers = provider.NewRegistry()
		if agentClient != nil {
			_ = providers.Register(provider.Wrap("default", "agent", agentClient))
		}
	}
	if plugins == nil {
		plugins = plugin.NewRegistry()
	}
	if sessions == nil {
		maxHistory := cfg.MaxHistory
		if maxHistory < 2 {
			maxHistory = 20
		}
		ttl := cfg.SessionTTL
		if ttl <= 0 {
			ttl = 24 * time.Hour
		}
		sessions = session.New(maxHistory, ttl)
	}
	service := &Service{
		cfg:                cfg,
		agent:              agentClient,
		sender:             sender,
		sessions:           sessions,
		logger:             logger,
		now:                time.Now,
		lastAccepted:       make(map[string]time.Time),
		requestTimes:       make(map[string][]time.Time),
		attentionTimes:     make(map[string][]time.Time),
		seenMessages:       make(map[string]time.Time),
		providers:          providers,
		plugins:            plugins,
		commands:           command.NewRegistry(),
		tools:              tool.NewRegistry(),
		personas:           persona.NewRegistry(),
		maintenance:        make(chan flowState, maintenanceQueueSize),
		maintenancePending: make(map[string]struct{}),
	}
	if strings.TrimSpace(cfg.AgentSystemPrompt) != "" {
		_ = service.personas.Register(persona.Profile{
			Name:         "default",
			Description:  "Default customer-service persona",
			SystemPrompt: cfg.AgentSystemPrompt,
			Default:      true,
		})
	}
	service.flow = service.newPipeline()
	return service
}

func (s *Service) ProviderRegistry() *provider.Registry {
	return s.providers
}

func (s *Service) PluginRegistry() *plugin.Registry {
	return s.plugins
}

func (s *Service) SetMessageGuard(guard plugin.BeforeHook) {
	s.messageGuard = guard
}

func (s *Service) CommandRegistry() *command.Registry {
	return s.commands
}

func (s *Service) ToolRegistry() *tool.Registry {
	return s.tools
}

func (s *Service) PersonaRegistry() *persona.Registry {
	return s.personas
}

func (s *Service) SetChatBindings(registry *binding.Registry) {
	s.bindings = registry
}

func (s *Service) ChatBindings() *binding.Registry {
	return s.bindings
}

func (s *Service) Run(ctx context.Context, events <-chan onebot.Event) {
	stopMaintenance := s.startMaintenance(ctx)
	defer stopMaintenance()
	workerCount := s.cfg.MaxConcurrency
	if workerCount <= 0 {
		workerCount = 1
	}
	workers := make([]chan onebot.Event, workerCount)
	var workersWG sync.WaitGroup
	for i := range workers {
		workers[i] = make(chan onebot.Event, workerQueueSize)
		workersWG.Add(1)
		go func(queue <-chan onebot.Event) {
			defer workersWG.Done()
			for event := range queue {
				s.handleEvent(ctx, event)
			}
		}(workers[i])
	}

	defer func() {
		for _, worker := range workers {
			close(worker)
		}
		workersWG.Wait()
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}
			index := workerIndex(event, len(workers))
			select {
			case <-ctx.Done():
				return
			case workers[index] <- event:
			}
		}
	}
}

// RunPlatform is the transport-neutral event entry point. Run remains
// available for callers that consume the legacy OneBot event channel.
func (s *Service) RunPlatform(ctx context.Context, events <-chan platform.Event) {
	stopMaintenance := s.startMaintenance(ctx)
	defer stopMaintenance()
	workerCount := s.cfg.MaxConcurrency
	if workerCount <= 0 {
		workerCount = 1
	}
	workers := make([]chan platform.Event, workerCount)
	var workersWG sync.WaitGroup
	for i := range workers {
		workers[i] = make(chan platform.Event, workerQueueSize)
		workersWG.Add(1)
		go func(queue <-chan platform.Event) {
			defer workersWG.Done()
			s.runPlatformWorker(ctx, queue)
		}(workers[i])
	}

	defer func() {
		for _, worker := range workers {
			close(worker)
		}
		workersWG.Wait()
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}
			index := platformWorkerIndex(event, len(workers))
			select {
			case <-ctx.Done():
				return
			case workers[index] <- event:
			}
		}
	}
}

func (s *Service) Stats() Stats {
	return Stats{
		Received:             s.stats.received.Load(),
		Ignored:              s.stats.ignored.Load(),
		Processed:            s.stats.processed.Load(),
		Replied:              s.stats.replied.Load(),
		AgentErrors:          s.stats.agentErrors.Load(),
		SendErrors:           s.stats.sendErrors.Load(),
		RateLimited:          s.stats.rateLimited.Load(),
		AttentionDecisions:   s.stats.attentionDecisions.Load(),
		AttentionReplies:     s.stats.attentionReplies.Load(),
		AttentionIgnored:     s.stats.attentionIgnored.Load(),
		AttentionErrors:      s.stats.attentionErrors.Load(),
		AttentionRateLimited: s.stats.attentionRateLimited.Load(),
		HandoffCount:         s.stats.handoffCount.Load(),
	}
}

func (s *Service) handleEvent(ctx context.Context, event onebot.Event) {
	chain, err := message.ParseOneBot(event.Message, event.RawMessage, event.SelfID.String())
	if err != nil {
		s.stats.received.Add(1)
		s.stats.ignored.Add(1)
		s.logger.Warn("ignored onebot event", "reason", err.Error())
		return
	}
	eventTime := int64(0)
	sender := senderName(event.Sender)
	if event.MessageID.String() != "" {
		eventTime = event.Time
	}
	chatID := event.GroupID.String()
	if event.MessageType == platform.ChatPrivate {
		chatID = event.UserID.String()
	}
	s.handlePlatformEvent(ctx, platform.Event{
		ID:          fmt.Sprintf("%s:%s", event.SelfID.String(), event.MessageID.String()),
		Platform:    platform.PlatformQQOneBot,
		PostType:    event.PostType,
		MessageType: event.MessageType,
		SubType:     event.SubType,
		MessageID:   event.MessageID.String(),
		SelfID:      event.SelfID.String(),
		UserID:      event.UserID.String(),
		ChatID:      chatID,
		SenderName:  sender,
		SenderRole:  event.Sender.Role,
		Chain:       chain,
		RawMessage:  event.RawMessage,
		Metadata: map[string]any{
			"time":   eventTime,
			"sender": sender,
		},
	})
}

func (s *Service) handlePlatformEvent(ctx context.Context, event platform.Event) {
	s.stats.received.Add(1)
	if s.plugins != nil {
		if err := s.plugins.Event(ctx, event); err != nil {
			s.stats.ignored.Add(1)
			s.logger.Error(
				"platform event plugin failed",
				"platform", event.Platform,
				"post_type", event.PostType,
				"error", err,
			)
			return
		}
	}
	flowContext := pipeline.NewContext(event)
	if err := s.flow.Run(ctx, flowContext); err != nil {
		s.logger.Error(
			"message pipeline failed",
			"platform", event.Platform,
			"chat_id", event.ChatID,
			"user_id", event.UserID,
			"message_id", event.MessageID,
			"error", err,
		)
		if state, ok := flowContext.Values[flowStateKey].(*flowState); ok && state.chatID != "" {
			s.sendReply(
				ctx,
				state.selfID,
				state.chatType,
				state.chatID,
				state.userID,
				state.event.SenderName,
				state.messageID,
				state.errorReply,
			)
		}
		return
	}
	if state, ok := flowContext.Values[flowStateKey].(*flowState); ok && state.ignored {
		s.stats.ignored.Add(1)
		s.logger.Info(
			"message ignored",
			"reason", flowContext.Reason,
			"platform", state.platform,
			"chat_type", state.chatType,
			"chat_id", state.chatID,
			"user_id", state.userID,
			"self_id", state.selfID,
			"message_id", state.messageID,
			"mentioned", state.eventMentioned,
		)
	}
}

func (s *Service) handleCommand(
	ctx context.Context,
	sessionID, selfID, chatType, chatID, userID, senderName, messageID, text string,
) bool {
	command := strings.ToLower(strings.TrimSpace(text))
	var reply string

	switch command {
	case "/clear", "/清空":
		s.sessions.Clear(sessionID)
		reply = s.cfg.ClearReply
	case "/human", "/人工":
		s.sessions.SetHandoff(sessionID, true)
		s.stats.handoffCount.Add(1)
		reply = s.cfg.HandoffReply
	case "/resume", "/恢复":
		s.sessions.SetHandoff(sessionID, false)
		reply = s.cfg.ResumeReply
	case "/help", "/帮助":
		reply = "支持命令：/清空、/人工、/恢复、/帮助"
	default:
		return false
	}

	s.stats.processed.Add(1)
	s.sendReply(ctx, selfID, chatType, chatID, userID, senderName, messageID, reply)
	return true
}

func (s *Service) sendReply(
	ctx context.Context,
	selfID, chatType, chatID, userID, senderName, messageID, text string,
) bool {
	return s.sendTextParts(
		ctx,
		selfID,
		chatType,
		chatID,
		userID,
		senderName,
		messageID,
		splitReply(text, s.cfg.MaxReplyRunes, s.cfg.MaxReplyChunks),
	)
}

func (s *Service) sendTextParts(
	ctx context.Context,
	selfID, chatType, chatID, userID, senderName, messageID string,
	parts []string,
) bool {
	if s.sender == nil {
		s.stats.sendErrors.Add(1)
		s.logger.Error("failed to send reply", "reason", "sender_not_configured")
		return false
	}
	for index, part := range parts {
		if index > 0 && !waitForReplyPart(ctx, s.replyPartDelay(parts[index-1])) {
			return false
		}
		quote := s.cfg.QuoteReply && index == 0
		chain := message.Chain{message.Text(part)}
		if s.cfg.GroupAtSender && chatType == platform.ChatGroup &&
			index == 0 && !quote {
			chain = append(message.Chain{message.AtNamed(userID, senderName)}, chain...)
		}
		err := s.sender.Send(ctx, platform.Outbound{
			ChatType: chatType,
			ChatID:   chatID,
			SelfID:   selfID,
			ReplyTo:  messageID,
			Quote:    quote,
			Chain:    chain,
		})
		if err != nil {
			s.stats.sendErrors.Add(1)
			s.logger.Error(
				"failed to send reply",
				"chat_type", chatType,
				"chat_id", chatID,
				"message_id", messageID,
				"part", index+1,
				"error", err,
			)
			return false
		}
		s.stats.replied.Add(1)
	}
	return true
}

func (s *Service) sendChain(
	ctx context.Context,
	selfID, chatType, chatID, userID, senderName, messageID string,
	chain message.Chain,
) bool {
	if len(chain) == 1 && chain[0].Type == message.TypeText {
		text, _ := chain.PlainText("")
		return s.sendReply(
			ctx,
			selfID,
			chatType,
			chatID,
			userID,
			senderName,
			messageID,
			text,
		)
	}
	if s.sender == nil {
		s.stats.sendErrors.Add(1)
		s.logger.Error("failed to send reply", "reason", "sender_not_configured")
		return false
	}
	outbound := chain.Clone()
	if s.cfg.GroupAtSender && chatType == platform.ChatGroup && !s.cfg.QuoteReply {
		outbound = append(message.Chain{message.AtNamed(userID, senderName)}, outbound...)
	}
	err := s.sender.Send(ctx, platform.Outbound{
		ChatType: chatType,
		ChatID:   chatID,
		SelfID:   selfID,
		ReplyTo:  messageID,
		Quote:    s.cfg.QuoteReply,
		Chain:    outbound,
	})
	if err != nil {
		s.stats.sendErrors.Add(1)
		s.logger.Error(
			"failed to send reply",
			"chat_type", chatType,
			"chat_id", chatID,
			"message_id", messageID,
			"error", err,
		)
		return false
	}
	s.stats.replied.Add(1)
	return true
}

func (s *Service) replyPartDelay(previous string) time.Duration {
	if s.cfg.ReplyPartDelay <= 0 {
		return 0
	}
	extraRunes := min(utf8.RuneCountInString(previous), 80)
	return s.cfg.ReplyPartDelay + time.Duration(extraRunes)*8*time.Millisecond
}

func waitForReplyPart(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		return true
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (s *Service) duplicate(key string) bool {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()

	now := s.now()
	if seenAt, ok := s.seenMessages[key]; ok && now.Sub(seenAt) < s.cfg.MessageDedupeTTL {
		return true
	}
	s.seenMessages[key] = now
	s.dedupeCounter++
	if s.dedupeCounter%100 == 0 {
		for messageKey, seenAt := range s.seenMessages {
			if now.Sub(seenAt) >= s.cfg.MessageDedupeTTL {
				delete(s.seenMessages, messageKey)
			}
		}
	}
	return false
}

func (s *Service) allowRequest(key string) bool {
	if s.cfg.UserCooldown == 0 && s.cfg.UserRateLimit <= 0 {
		return true
	}

	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	now := s.now()
	if s.cfg.UserCooldown > 0 {
		if last, ok := s.lastAccepted[key]; ok &&
			now.Sub(last) < s.cfg.UserCooldown {
			return false
		}
	}
	requests := s.requestTimes[key]
	if s.cfg.UserRateWindow > 0 {
		first := 0
		for first < len(requests) &&
			now.Sub(requests[first]) >= s.cfg.UserRateWindow {
			first++
		}
		if first > 0 {
			requests = append([]time.Time(nil), requests[first:]...)
		}
	}
	if s.cfg.UserRateLimit > 0 &&
		len(requests) >= s.cfg.UserRateLimit {
		return false
	}
	if s.cfg.UserCooldown > 0 {
		s.lastAccepted[key] = now
	}
	if s.cfg.UserRateLimit > 0 {
		s.requestTimes[key] = append(requests, now)
	}
	s.rateCounter++
	if s.rateCounter%128 == 0 {
		for currentKey, currentRequests := range s.requestTimes {
			if s.cfg.UserRateWindow <= 0 {
				continue
			}
			first := 0
			for first < len(currentRequests) &&
				now.Sub(currentRequests[first]) >= s.cfg.UserRateWindow {
				first++
			}
			if first >= len(currentRequests) {
				delete(s.requestTimes, currentKey)
			} else if first > 0 {
				s.requestTimes[currentKey] =
					append([]time.Time(nil), currentRequests[first:]...)
			}
		}
		for currentKey, last := range s.lastAccepted {
			if s.cfg.UserCooldown > 0 &&
				now.Sub(last) >= s.cfg.UserCooldown &&
				len(s.requestTimes[currentKey]) == 0 {
				delete(s.lastAccepted, currentKey)
			}
		}
	}
	return true
}

func (s *Service) allowAttentionDecision(key string) bool {
	if s.cfg.AttentionRateLimit <= 0 {
		return true
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	now := s.now()
	requests := s.attentionTimes[key]
	first := 0
	for first < len(requests) &&
		now.Sub(requests[first]) >= s.cfg.AttentionRateWindow {
		first++
	}
	if first > 0 {
		requests = append([]time.Time(nil), requests[first:]...)
	}
	if len(requests) >= s.cfg.AttentionRateLimit {
		s.attentionTimes[key] = requests
		return false
	}
	s.attentionTimes[key] = append(requests, now)
	s.attentionCounter++
	if s.attentionCounter%128 == 0 {
		for currentKey, currentRequests := range s.attentionTimes {
			first = 0
			for first < len(currentRequests) &&
				now.Sub(currentRequests[first]) >= s.cfg.AttentionRateWindow {
				first++
			}
			if first >= len(currentRequests) {
				delete(s.attentionTimes, currentKey)
			} else if first > 0 {
				s.attentionTimes[currentKey] = append(
					[]time.Time(nil),
					currentRequests[first:]...,
				)
			}
		}
	}
	return true
}

func splitReply(text string, maxRunes, maxChunks int) []string {
	text = strings.TrimSpace(text)
	if text == "" || maxRunes <= 0 || maxChunks <= 0 {
		return nil
	}

	remaining := []rune(text)
	chunks := make([]string, 0, min(maxChunks, len(remaining)/maxRunes+1))
	for len(remaining) > 0 && len(chunks) < maxChunks {
		size := min(maxRunes, len(remaining))
		chunks = append(chunks, string(remaining[:size]))
		remaining = remaining[size:]
	}

	if len(remaining) > 0 {
		suffix := []rune("\n[内容过长，已截断]")
		if len(suffix) > maxRunes {
			suffix = []rune("[截断]")
		}
		last := []rune(chunks[len(chunks)-1])
		keep := maxRunes - len(suffix)
		if keep < 0 {
			keep = 0
			suffix = suffix[:min(len(suffix), maxRunes)]
		}
		if len(last) > keep {
			last = last[:keep]
		}
		chunks[len(chunks)-1] = string(append(last, suffix...))
	}
	return chunks
}

func workerIndex(event onebot.Event, count int) int {
	if count <= 0 {
		return 0
	}
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(event.SelfID.String()))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(event.MessageType))
	_, _ = hash.Write([]byte{0})
	chatID := event.GroupID.String()
	if event.MessageType == platform.ChatPrivate {
		chatID = event.UserID.String()
	}
	_, _ = hash.Write([]byte(chatID))
	return int(hash.Sum32() % uint32(count))
}

func platformWorkerIndex(event platform.Event, count int) int {
	if count <= 0 {
		return 0
	}
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(event.Platform))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(event.SelfID))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(event.MessageType))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(event.ChatID))
	return int(hash.Sum32() % uint32(count))
}

func senderName(sender onebot.Sender) string {
	if card := strings.TrimSpace(sender.Card); card != "" {
		return card
	}
	return strings.TrimSpace(sender.Nickname)
}

func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}
