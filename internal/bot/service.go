package bot

import (
	"context"
	"fmt"
	"hash/fnv"
	"log/slog"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/lianyorker/cinlan-qq-bot/internal/affinity"
	"github.com/lianyorker/cinlan-qq-bot/internal/agent"
	"github.com/lianyorker/cinlan-qq-bot/internal/binding"
	"github.com/lianyorker/cinlan-qq-bot/internal/chatprefs"
	"github.com/lianyorker/cinlan-qq-bot/internal/command"
	"github.com/lianyorker/cinlan-qq-bot/internal/config"
	"github.com/lianyorker/cinlan-qq-bot/internal/message"
	"github.com/lianyorker/cinlan-qq-bot/internal/moderation"
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

// FileSender uploads a local file to a private chat with the given user.
type FileSender interface {
	UploadPrivateFile(ctx context.Context, selfID, userID, filePath, name string) error
	IsFriend(ctx context.Context, selfID, userID string) (bool, error)
}

// MessageFetcher resolves a replied-to (quoted) message's full chain, which
// is not part of the inbound chain.
type MessageFetcher interface {
	FetchMessage(ctx context.Context, selfID, messageID string) (message.Chain, error)
}

// GroupModerator performs deterministic moderation actions (recall, mute,
// kick) through the platform adapter. Requires bot-side admin rights.
type GroupModerator interface {
	RecallMessage(ctx context.Context, selfID, messageID string) error
	BanMember(
		ctx context.Context,
		selfID, groupID, userID string,
		duration time.Duration,
	) error
	KickMember(ctx context.Context, selfID, groupID, userID string) error
}

// GroupMemberChecker reports whether a user belongs to a group. Private-chat
// bindings use it to route personas by community membership (member_groups).
type GroupMemberChecker interface {
	IsGroupMember(ctx context.Context, selfID, groupID, userID string) (bool, error)
}

// UserProfiler is the raw platform action surface used to look up who a
// person is — get_stranger_info / get_friend_list / get_group_member_info.
// The platform adapters already expose Call; this narrows it to what the
// profile cache needs.
type UserProfiler interface {
	Call(ctx context.Context, action string, params map[string]any) (any, error)
}

const memberCacheTTL = 10 * time.Minute

type memberCacheEntry struct {
	member bool
	at     time.Time
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
	cfg            config.Config
	agent          agent.Client
	sender         Sender
	fileSender     FileSender
	chainFetcher   MessageFetcher
	groupModerator GroupModerator
	memberChecker  GroupMemberChecker
	userProfiler   UserProfiler
	memberMu       sync.Mutex
	memberCache    map[string]memberCacheEntry
	warnStore      *moderation.Store
	voiceProvider  voiceSynth
	voiceBudget    voiceBudget
	voiceQuota     *voiceQuota
	sessions       *session.Store
	logger         *slog.Logger
	stats          counters
	now            func() time.Time
	providers      *provider.Registry
	plugins        *plugin.Registry
	messageGuard   plugin.BeforeHook
	commands       *command.Registry
	tools          *tool.Registry
	personas       *persona.Registry
	bindings       *binding.Registry
	chatPrefs      *chatprefs.Store
	affinity       *affinity.Store
	memeDir        string
	keywordTools   []keywordTool
	// basePrompt is the shared human-like behavior block loaded from
	// cfg.BasePromptFile and appended to every persona prompt.
	basePrompt string
	// peerSelfIDs are our own other bot accounts. Their group messages are
	// observed as [同伴账号] but never answered — two personas replying to
	// each other read as one bot contradicting itself.
	peerSelfIDs map[string]struct{}

	stateMu          sync.Mutex
	lastAccepted     map[string]time.Time
	requestTimes     map[string][]time.Time
	attentionTimes   map[string][]time.Time
	seenMessages     map[string]time.Time
	lastActivity     map[string]time.Time
	activityMu       sync.RWMutex
	dedupeCounter    uint64
	rateCounter      uint64
	attentionCounter uint64
	spamMu           sync.Mutex
	spamRepeats      map[string]spamRepeatEntry
	fatigueMu        sync.Mutex
	chatFatigue      map[string]fatigueEntry
	chatMu           sync.Mutex
	chatLocks        map[string]*sync.Mutex
	recentMu         sync.Mutex
	recentImages     map[string]recentImage

	flow *pipeline.Pipeline

	// recentImage holds the freshest image ref seen per chat so a
	// text-only follow-up ("怎么回事"/"报错是啥") can reattach it.

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
		sessions.SetSemanticTTL(cfg.SessionSemanticTTL)
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
		lastActivity:       make(map[string]time.Time),
		providers:          providers,
		plugins:            plugins,
		commands:           command.NewRegistry(),
		tools:              tool.NewRegistry(),
		personas:           persona.NewRegistry(),
		maintenance:        make(chan flowState, maintenanceQueueSize),
		maintenancePending: make(map[string]struct{}),
		memberCache:        make(map[string]memberCacheEntry),
	}
	if strings.TrimSpace(cfg.AgentSystemPrompt) != "" {
		_ = service.personas.Register(persona.Profile{
			Name:         "default",
			Description:  "Default customer-service persona",
			SystemPrompt: cfg.AgentSystemPrompt,
			Default:      true,
		})
	}
	if path := strings.TrimSpace(cfg.BasePromptFile); path != "" {
		if data, err := os.ReadFile(path); err == nil {
			service.basePrompt = strings.TrimSpace(string(data))
		} else if !os.IsNotExist(err) {
			logger.Warn("failed to read base prompt file", "path", path, "error", err.Error())
		}
	}
	service.flow = service.newPipeline()
	return service
}

// SetPeerSelfIDs registers our own other bot accounts so their group
// messages are observed but never answered.
func (s *Service) SetPeerSelfIDs(selfIDs []string) {
	peers := make(map[string]struct{}, len(selfIDs))
	for _, selfID := range selfIDs {
		selfID = strings.TrimSpace(selfID)
		if selfID != "" {
			peers[selfID] = struct{}{}
		}
	}
	s.peerSelfIDs = peers
}

func (s *Service) isPeerSelf(userID string) bool {
	if len(s.peerSelfIDs) == 0 {
		return false
	}
	_, ok := s.peerSelfIDs[strings.TrimSpace(userID)]
	return ok
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

// TouchActivity records that a chat saw traffic (inbound message or our
// own outbound) at now. The proactive engine keys off this to stay quiet
// in lively chats.
func (s *Service) TouchActivity(chatType, chatID string) {
	if chatType == "" || chatID == "" {
		return
	}
	s.activityMu.Lock()
	s.lastActivity[chatType+":"+chatID] = s.now()
	s.activityMu.Unlock()
}

// LastActivity returns when the chat last saw traffic, zero if unknown.
// Falls back to the session entry's UpdatedAt so the "距上次" line still
// works after restarts and across the semantic-memory window.
func (s *Service) LastActivity(chatType, chatID string) time.Time {
	s.activityMu.RLock()
	at := s.lastActivity[chatType+":"+chatID]
	s.activityMu.RUnlock()
	if !at.IsZero() || s.sessions == nil {
		return at
	}
	return s.sessions.LastActivity(chatType + ":" + chatID)
}

func (s *Service) SetChatPrefs(store *chatprefs.Store) {
	s.chatPrefs = store
}

// ClearHandoff releases a session's human-takeover flag. The /人工 /恢复
// commands were folded into the notify pair — /开启通知 now serves as the
// single "wake her up" and clears handoff alongside notify-off and mute.
func (s *Service) ClearHandoff(sessionID string) {
	s.sessions.SetHandoff(sessionID, false)
}

// SetGroupMemberChecker wires the platform capability used by member_groups
// binding selectors (private-chat routing by community membership).
func (s *Service) SetGroupMemberChecker(checker GroupMemberChecker) {
	s.memberChecker = checker
}

// SetUserProfiler wires the action caller used to refresh cached QQ
// profiles (nickname/sex/sign/friend flag/group badges).
func (s *Service) SetUserProfiler(profiler UserProfiler) {
	s.userProfiler = profiler
}

// groupMemberOf answers whether userID is in groupID, caching each pair for
// memberCacheTTL. Checker failures are treated as "not a member" so a flaky
// API fails closed — the private message is then ignored as unbound.
func (s *Service) groupMemberOf(ctx context.Context, selfID, groupID, userID string) bool {
	if s.memberChecker == nil || groupID == "" || userID == "" {
		return false
	}
	key := groupID + ":" + userID
	s.memberMu.Lock()
	if entry, ok := s.memberCache[key]; ok && time.Since(entry.at) < memberCacheTTL {
		s.memberMu.Unlock()
		return entry.member
	}
	s.memberMu.Unlock()
	member, err := s.memberChecker.IsGroupMember(ctx, selfID, groupID, userID)
	if err != nil {
		s.logger.Warn(
			"group membership check failed",
			"group_id", groupID,
			"user_id", userID,
			"error", err,
		)
		return false
	}
	s.memberMu.Lock()
	s.memberCache[key] = memberCacheEntry{member: member, at: time.Now()}
	s.memberMu.Unlock()
	return member
}

func (s *Service) ChatPrefs() *chatprefs.Store {
	return s.chatPrefs
}

// SetAffinity wires the per-user intimacy score store so the pipeline can
// show the persona how close it is to each sender.
func (s *Service) SetAffinity(store *affinity.Store) {
	s.affinity = store
}

func (s *Service) Affinity() *affinity.Store {
	return s.affinity
}

// SetMemeDir enables the per-chat meme pool under dir.
func (s *Service) SetMemeDir(dir string) {
	s.memeDir = strings.TrimSpace(dir)
}

// RegisterKeywordMeme sends a random stored meme from the chat's pool when
// the pattern matches.
func (s *Service) RegisterKeywordMeme(pattern *regexp.Regexp) {
	if pattern == nil {
		return
	}
	s.keywordTools = append(s.keywordTools, keywordTool{pattern: pattern, meme: true})
}

// ReferenceImageFor resolves the reference photo for an image-edit call.
// A remembered per-chat image (/记住 带图) wins; when the chat has none the
// bound persona's fixed default_image (canonical portrait) kicks in, so she
// stops drifting face shapes in chats that never stored a photo.
func (s *Service) ReferenceImageFor(ctx context.Context, actor tool.Actor) string {
	if s.chatPrefs != nil {
		if path := s.chatPrefs.LatestImage(actor.ChatType, actor.ChatID); path != "" {
			return path
		}
	}
	personaName := ""
	if s.chatPrefs != nil {
		personaName = s.chatPrefs.PersonaName(actor.ChatType, actor.ChatID)
	}
	if personaName == "" && s.bindings != nil {
		var memberOf func(groupID, userID string) bool
		if actor.ChatType == platform.ChatPrivate {
			memberOf = func(groupID, userID string) bool {
				return s.groupMemberOf(ctx, actor.SelfID, groupID, userID)
			}
		}
		if rule, ok := s.bindings.MatchActor(
			actor.Platform, actor.SelfID,
			actor.ChatType, actor.ChatID, actor.UserID, memberOf,
		); ok {
			personaName = rule.Persona
		}
	}
	if personaName == "" || s.personas == nil {
		return ""
	}
	if profile, ok := s.personas.Get(personaName); ok {
		return strings.TrimSpace(profile.DefaultImage)
	}
	return ""
}

type keywordTool struct {
	pattern   *regexp.Regexp
	tool      string
	binding   string   // optional: only fire for this binding name
	reply     string   // optional: fixed text reply instead of a tool call
	promptArg string   // optional: pass text after the match as this JSON argument
	filePath  string   // optional: upload this file in private chat on match
	meme      bool     // optional: send a random meme from the chat pool
	groups    []string // optional: file only for these groups/their members
}

// SetFileSender wires the private-file upload path used by keyword file
// replies (e.g. delivering an .sql file to a user in private chat).
func (s *Service) SetFileSender(sender FileSender) {
	s.fileSender = sender
}

// SetMessageFetcher wires the quoted-message image lookup used by /记住 and
// 生图 when the user replies to an image instead of attaching one.
func (s *Service) SetMessageFetcher(fetcher MessageFetcher) {
	s.chainFetcher = fetcher
}

// SetGroupModerator wires recall/mute/kick actions used by the moderation
// stage for pattern-matched spam.
func (s *Service) SetGroupModerator(moderator GroupModerator) {
	s.groupModerator = moderator
}

// SetModerationStore wires the persistent spam-warning counter.
func (s *Service) SetModerationStore(store *moderation.Store) {
	s.warnStore = store
}

// SetVoiceProvider wires the TTS backend for [[VOICE]] reply clips; nil
// disables voice replies.
func (s *Service) SetVoiceProvider(provider voiceSynth) {
	s.voiceProvider = provider
	if provider != nil {
		limit := s.cfg.VoiceDailyLimit
		if limit <= 0 {
			limit = 50
		}
		s.voiceQuota = &voiceQuota{
			path:  filepath.Join(s.cfg.VoiceDir, "quota.json"),
			limit: limit,
		}
	}
}

// EventImageRefs returns image references from the message itself and, when
// it contains none, from a replied-to message.
func (s *Service) EventImageRefs(
	ctx context.Context,
	selfID string,
	chain message.Chain,
) []string {
	if refs := chain.ImageReferences(); len(refs) > 0 {
		return refs
	}
	if quoted := s.QuotedChain(ctx, selfID, chain); quoted != nil {
		return quoted.ImageReferences()
	}
	return nil
}

// QuotedChain returns the parsed chain of the message this one replies to,
// or nil when there is no reply segment or the fetch fails.
func (s *Service) QuotedChain(
	ctx context.Context,
	selfID string,
	chain message.Chain,
) message.Chain {
	if s.chainFetcher == nil {
		return nil
	}
	replyID := chain.ReplyID()
	if replyID == "" {
		return nil
	}
	quoted, err := s.chainFetcher.FetchMessage(ctx, selfID, replyID)
	if err != nil {
		s.logger.Warn(
			"quoted message lookup failed",
			"message_id", replyID,
			"error", err.Error(),
		)
		return nil
	}
	return quoted
}

// RegisterKeywordTool maps a message pattern directly to a registered tool,
// bypassing the model. The tool still must be listed in the chat binding's
// tools allowlist for the intercept to fire.
func (s *Service) RegisterKeywordTool(pattern *regexp.Regexp, toolName string) {
	if pattern == nil || strings.TrimSpace(toolName) == "" {
		return
	}
	s.keywordTools = append(s.keywordTools, keywordTool{
		pattern: pattern,
		tool:    toolName,
	})
}

// RegisterKeywordPromptTool maps a message pattern to a tool call whose
// named argument receives the text remaining after the matched keyword.
// The tool still must be listed in the chat binding's tools allowlist.
func (s *Service) RegisterKeywordPromptTool(
	pattern *regexp.Regexp,
	toolName string,
	argName string,
) {
	if pattern == nil || strings.TrimSpace(toolName) == "" ||
		strings.TrimSpace(argName) == "" {
		return
	}
	s.keywordTools = append(s.keywordTools, keywordTool{
		pattern:   pattern,
		tool:      toolName,
		promptArg: argName,
	})
}

// RegisterKeywordReply sends a fixed reply when a message matches the pattern
// inside the given binding, bypassing the model entirely.
func (s *Service) RegisterKeywordReply(pattern *regexp.Regexp, bindingName, text string) {
	if pattern == nil || strings.TrimSpace(bindingName) == "" || strings.TrimSpace(text) == "" {
		return
	}
	s.keywordTools = append(s.keywordTools, keywordTool{
		pattern: pattern,
		binding: bindingName,
		reply:   text,
	})
}

// RegisterKeywordFile delivers a local file in private chat on match and
// falls back to the fixed reply text everywhere else (or when the upload
// path is unavailable).
func (s *Service) RegisterKeywordFile(pattern *regexp.Regexp, filePath, text string, groups []string) {
	if pattern == nil || strings.TrimSpace(filePath) == "" ||
		strings.TrimSpace(text) == "" {
		return
	}
	s.keywordTools = append(s.keywordTools, keywordTool{
		pattern:  pattern,
		filePath: filePath,
		reply:    text,
		groups:   groups,
	})
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

// chatLockFor returns the per-chat mutex that serializes pipeline turns.
func (s *Service) chatLockFor(chatType, chatID string) *sync.Mutex {
	s.chatMu.Lock()
	defer s.chatMu.Unlock()
	if s.chatLocks == nil {
		s.chatLocks = map[string]*sync.Mutex{}
	}
	key := chatType + ":" + chatID
	if m, ok := s.chatLocks[key]; ok {
		return m
	}
	m := &sync.Mutex{}
	s.chatLocks[key] = m
	return m
}

// recentImage records a freshly seen image ref per chat.
type recentImage struct {
	ref    string
	userID string
	at     time.Time
}

func (s *Service) noteRecentImage(chatType, chatID, userID, ref string) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return
	}
	s.recentMu.Lock()
	defer s.recentMu.Unlock()
	if s.recentImages == nil {
		s.recentImages = map[string]recentImage{}
	}
	s.recentImages[chatType+":"+chatID] = recentImage{
		ref: ref, userID: userID, at: time.Now(),
	}
}

// imageRefHint marks texts that plausibly refer back to a just-sent
// image; very short follow-ups ("怎么回事") count as referential too.
var imageRefHint = regexp.MustCompile(
	`图|截图|报错|照片|表情|画的|上面|那个|刚才|刚刚|看看`)

func (s *Service) recentImageFor(state *flowState, window time.Duration) string {
	s.recentMu.Lock()
	defer s.recentMu.Unlock()
	entry, ok := s.recentImages[state.chatType+":"+state.chatID]
	if !ok || time.Since(entry.at) > window {
		return ""
	}
	text := strings.TrimSpace(state.text)
	if utf8.RuneCountInString(text) <= 14 || imageRefHint.MatchString(text) {
		return entry.ref
	}
	return ""
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
	// Serialize turns within a chat: a follow-up event handled while an
	// earlier reply is still in flight would snapshot history before the
	// first exchange is recorded — producing contradictory answers on
	// rapid @ mentions. Per-chat lock keeps observations+replies ordered.
	lock := s.chatLockFor(event.MessageType, event.ChatID)
	lock.Lock()
	defer lock.Unlock()
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
	case "/亲密度", "/affinity":
		if s.affinity == nil {
			reply = "亲密度功能未开启。"
			break
		}
		score := s.affinity.Score(selfID, userID)
		level, percent := affinity.Level(score), affinity.Percent(score)
		if chatType == platform.ChatPrivate {
			level, percent = affinity.LevelIntimate, 100
		}
		reply = fmt.Sprintf("你和我的亲密度：%s（%d/100）", level, percent)
	case "/help", "/帮助":
		reply = "支持命令：/清空、/亲密度、/帮助"
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
		if index == 0 {
			if !waitForReplyPart(ctx, s.typingDelay(part)) {
				return false
			}
		} else if !waitForReplyPart(ctx, s.replyPartDelay(part)) {
			return false
		}
		quote := s.cfg.QuoteReply && index == 0 && s.shouldQuoteReply(chatType)
		chain := message.Chain{message.Text(normalizeHTMLBreaks(part))}
		if s.cfg.GroupAtSender && chatType == platform.ChatGroup &&
			index == 0 && !quote {
			chain = withSenderAt(chain, userID, senderName)
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

// withSenderAt prepends the group @-mention for the sender and pads a
// leading space onto the first text segment so the bubble renders
// "@nick 文字" instead of "@nick文字". Non-text segments stay untouched.
func withSenderAt(chain message.Chain, userID, senderName string) message.Chain {
	out := append(message.Chain{message.AtNamed(userID, senderName)}, chain...)
	if len(out) > 1 && out[1].Type == message.TypeText {
		if text, ok := out[1].Data["text"].(string); ok && text != "" && !strings.HasPrefix(text, " ") {
			out[1] = message.Text(" " + text)
		}
	}
	return out
}

// sendChains sends pre-built chains one message at a time with the same
// delay/at/quote behaviour as sendTextParts. Used when replies contain
// non-text segments such as QQ faces.
func (s *Service) sendChains(
	ctx context.Context,
	selfID, chatType, chatID, userID, senderName, messageID string,
	chains []message.Chain,
) bool {
	if s.sender == nil {
		s.stats.sendErrors.Add(1)
		s.logger.Error("failed to send reply", "reason", "sender_not_configured")
		return false
	}
	for index, chain := range chains {
		if len(chain) == 0 {
			continue
		}
		if index > 0 {
			current, _ := chain.PlainText("")
			if !waitForReplyPart(ctx, s.replyPartDelay(current)) {
				return false
			}
		}
		quote := s.cfg.QuoteReply && index == 0 && s.shouldQuoteReply(chatType)
		outbound := normalizeChainHTMLBreaks(chain.Clone())
		if s.cfg.GroupAtSender && chatType == platform.ChatGroup &&
			index == 0 && !quote {
			outbound = withSenderAt(outbound, userID, senderName)
		}
		err := s.sender.Send(ctx, platform.Outbound{
			ChatType: chatType,
			ChatID:   chatID,
			SelfID:   selfID,
			ReplyTo:  messageID,
			Quote:    quote,
			Chain:    outbound,
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
	outbound := normalizeChainHTMLBreaks(chain.Clone())
	if s.cfg.GroupAtSender && chatType == platform.ChatGroup && !s.cfg.QuoteReply {
		outbound = withSenderAt(outbound, userID, senderName)
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

// htmlBreakPattern matches literal <br> variants the model sometimes emits.
// QQ does not render HTML, so they would otherwise leak into the chat as
// visible text instead of a line break.
var htmlBreakPattern = regexp.MustCompile(`(?i)<br\s*/?>`)

// blankLinePattern collapses paragraph gaps — a real person sends one
// line break at a time inside a bubble; blank-line separation reads AI.
var blankLinePattern = regexp.MustCompile(`\n{2,}`)

func normalizeHTMLBreaks(text string) string {
	text = blankLinePattern.ReplaceAllString(text, "\n")
	if !strings.Contains(text, "<") {
		return text
	}
	return htmlBreakPattern.ReplaceAllString(text, "\n")
}

func normalizeChainHTMLBreaks(chain message.Chain) message.Chain {
	for index, component := range chain {
		if component.Type != message.TypeText {
			continue
		}
		if text, ok := component.Data["text"].(string); ok {
			chain[index].Data["text"] = normalizeHTMLBreaks(text)
		}
	}
	return chain
}

// shouldQuoteReply decides whether the reply carries a message quote.
// Private chats never quote (1-on-1 context is obvious); group replies quote
// with a probability so the bot reads like a person who only quotes when the
// context is ambiguous.
func (s *Service) shouldQuoteReply(chatType string) bool {
	if chatType != platform.ChatGroup {
		return false
	}
	prob := s.cfg.QuoteGroupProb
	if prob <= 0 {
		return false
	}
	if prob >= 1 {
		return true
	}
	return rand.Float64() < prob
}

// replyPartDelay simulates the time a person needs to type the part
// about to be sent: the base beat plus ~30ms per rune (a longer message
// visibly takes longer to type). Capped at 80 runes of extra time.
func (s *Service) replyPartDelay(part string) time.Duration {
	if s.cfg.ReplyPartDelay <= 0 {
		return 0
	}
	extraRunes := min(utf8.RuneCountInString(part), 80)
	return s.cfg.ReplyPartDelay + time.Duration(extraRunes)*30*time.Millisecond
}

// typingDelay simulates the pause before a person sends their first
// message: a base beat plus per-rune typing time, jittered and capped so
// long replies still feel human rather than sluggish.
func (s *Service) typingDelay(first string) time.Duration {
	if !s.cfg.TypingSim || s.cfg.TypingBase <= 0 {
		return 0
	}
	delay := s.cfg.TypingBase +
		time.Duration(utf8.RuneCountInString(first))*s.cfg.TypingPerRune
	// ±35% jitter so the cadence isn't metronomic.
	jitter := time.Duration(rand.Int63n(int64(delay/3) + 1))
	delay += jitter - delay/6
	// Sleepy typing is slower typing.
	if s.cfg.Asleep(time.Now()) {
		delay += delay / 2
	}
	if s.cfg.TypingMax > 0 && delay > s.cfg.TypingMax {
		delay = s.cfg.TypingMax
	}
	if delay < 0 {
		delay = 0
	}
	return delay
}

// readDelay simulates actually reading the inbound message before
// thinking: one-liners barely register, a 500字小作文 takes a beat.
func (s *Service) readDelay(text string) time.Duration {
	if !s.cfg.TypingSim || s.cfg.ReadDelayPerRune <= 0 {
		return 0
	}
	delay := time.Duration(utf8.RuneCountInString(text)) * s.cfg.ReadDelayPerRune
	if s.cfg.ReadDelayMax > 0 && delay > s.cfg.ReadDelayMax {
		delay = s.cfg.ReadDelayMax
	}
	return delay
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
