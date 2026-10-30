package bot

import (
	"context"
	"errors"
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
	"github.com/lianyorker/cinlan-qq-bot/internal/domain"
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
	FetchMessage(ctx context.Context, selfID, messageID string) (*message.FetchedMessage, error)
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

// atNameTTL: display names change rarely; refresh after a day.
const atNameTTL = 24 * time.Hour

type atNameEntry struct {
	name      string
	fetchedAt time.Time
}

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
	// atNameCache remembers resolved @-mention display names (group card
	// or nickname) so PlainText reads "@阿茶" instead of "@123456".
	atNameMu      sync.Mutex
	atNameCache   map[string]atNameEntry
	warnStore     *moderation.Store
	voiceProvider voiceSynth
	// singProvider handles [[SING]] clips when a dedicated singing
	// engine is configured (e.g. Breeze Blue); nil keeps singing on
	// the plain voice provider.
	singProvider voiceSynth
	// songProvider generates real songs (melody + arrangement) from
	// [[SING]] lyrics via an async music API — the actual "singing",
	// as opposed to the sung-TTS approximation above.
	songProvider songGenerator
	// singBusy blocks concurrent song-generation per chat — each task
	// burns 30s–3min of API work, so a chat gets one at a time.
	singBusyMu sync.Mutex
	singBusy   map[string]bool
	voiceBudget  voiceBudget
	// pokeBudget cooldowns inbound 戳一戳 so a poked reply stays an
	// occasional human reaction instead of a hair-trigger.
	pokeBudget voiceBudget
	feelBudget voiceBudget
	// feelQueue carries inner-monologue jobs off the reply hot path —
	// feelEvent enqueues (mood bump stays synchronous) and feelWorker
	// drains them so the triggering turn never waits for an LLM.
	feelQueue  chan feelJob
	voiceQuota *voiceQuota
	sessions   *session.Store
	logger     *slog.Logger
	stats      counters
	now        func() time.Time
	providers  *provider.Registry
	// judgeProvider names the cheap decision model (JUDGE_PROVIDER). All
	// classifier/judge calls — attention, spam, voice safety, note review —
	// route through it instead of burning the conversation model.
	judgeProvider string
	plugins       *plugin.Registry
	messageGuard  plugin.BeforeHook
	commands      *command.Registry
	tools         *tool.Registry
	personas      *persona.Registry
	bindings      *binding.Registry
	chatPrefs     *chatprefs.Store
	affinity      *affinity.Store
	memeDir       string
	stickerDir    string
	// transcriber turns inbound voice clips into text (opt-in ASR).
	transcriber  voiceTranscriber
	keywordTools []keywordTool
	// basePrompt is the shared human-like behavior block loaded from
	// cfg.BasePromptFile and appended to every persona prompt.
	basePrompt string
	// bannedWords is the platform-level outbound hard block — base list
	// plus optional hot-reloading file (data/outbound-banned-words.txt).
	bannedWords *outboundBannedWords
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
	// echoMu guards the meme-chain tracker: echoChains counts distinct
	// users echoing the same line in a chat, echoJoinAt throttles her own
	// tags so one meme run doesn't turn her into a parrot, and echoRoll
	// is a swappable 0-99 roller for tests.
	echoMu      sync.Mutex
	echoChains  map[string]echoChainEntry
	echoJoinAt  map[string]time.Time
	echoRoll    func() int
	fatigueMu   sync.Mutex
	chatFatigue map[string]fatigueEntry
	// socialMu guards the neglect tracker: lastSpoke is when she last
	// said something in the chat, lastNeglect the last time being
	// ignored cost her mood — a sting per quiet stretch, not per snub.
	socialMu     sync.Mutex
	lastSpoke    map[string]time.Time
	lastNeglect  map[string]time.Time
	gameMu       sync.Mutex
	gameInvites  map[string]time.Time
	chatMu       sync.Mutex
	chatLocks    map[string]*sync.Mutex
	recentMu     sync.Mutex
	recentImages map[string]recentImage
	// recallText remembers recent inbound texts by sessionID+messageID
	// so observeGroupMemberNotice can quote what a recall erased.
	recallText *recallSnippets

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
		lastSpoke:          make(map[string]time.Time),
		lastNeglect:        make(map[string]time.Time),
		providers:          providers,
		plugins:            plugins,
		commands:           command.NewRegistry(),
		tools:              tool.NewRegistry(),
		personas:           persona.NewRegistry(),
		maintenance:        make(chan flowState, maintenanceQueueSize),
		maintenancePending: make(map[string]struct{}),
		memberCache:        make(map[string]memberCacheEntry),
		atNameCache:        make(map[string]atNameEntry),
		recallText:         newRecallSnippets(300),
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
	if service.basePrompt == "" {
		// No file on disk (or unreadable) — fall back to the copy baked
		// into the binary so behavior rules survive a bare checkout.
		service.basePrompt = strings.TrimSpace(embeddedBasePrompt)
		if service.basePrompt != "" {
			logger.Info("base prompt loaded from embedded default")
		}
	}
	service.bannedWords = newOutboundBannedWords(strings.TrimSpace(cfg.OutboundBannedWordsFile))
	service.judgeProvider = strings.TrimSpace(cfg.JudgeProvider)
	service.feelQueue = make(chan feelJob, 32)
	go service.voiceCacheLoop()
	go service.feelWorker()
	go service.reminderLoop()
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

// isOwnerUser reports whether userID is in the global creator list
// (BOT_OWNER_USER_IDS). The owner is whitelisted in every binding —
// not just for admin-tier tools but as the person who built her, which
// the prompt layer uses to drop the stranger distance.
func (s *Service) isOwnerUser(userID string) bool {
	for _, id := range s.cfg.OwnerUserIDs {
		if strings.TrimSpace(id) == userID {
			return true
		}
	}
	return false
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

// SetStickerDir enables the persona sticker packs under dir
// (dir/<persona>/<tag>.<ext> plus dir/shared/ for common ones).
func (s *Service) SetStickerDir(dir string) {
	s.stickerDir = strings.TrimSpace(dir)
}

// SetTranscriber plugs in the ASR client so inbound voice messages get
// transcribed into readable text instead of staying a [语音] marker.
func (s *Service) SetTranscriber(t voiceTranscriber) {
	s.transcriber = t
}

// MemeFile resolves a meme-pool query to a file path for the proactive
// engine; "" means nothing matched (or the pool is off).
func (s *Service) MemeFile(platform, chatType, chatID, query string) string {
	path, err := s.pickMeme(platform, chatType, chatID, query)
	if err != nil {
		return ""
	}
	return path
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
		if path := s.chatPrefs.LatestImage(actor.Platform, actor.SelfID, actor.ChatType, actor.ChatID); path != "" {
			return path
		}
	}
	personaName := ""
	if s.chatPrefs != nil {
		personaName = s.chatPrefs.PersonaName(actor.Platform, actor.SelfID, actor.ChatType, actor.ChatID)
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

// SetSingProvider wires the dedicated singing engine; nil keeps
// [[SING]] clips on the plain voice provider.
func (s *Service) SetSingProvider(provider voiceSynth) {
	s.singProvider = provider
}

// SetSongProvider wires the real lyrics-to-song generator; non-nil
// makes [[SING]] clips produce an actual song instead of styled TTS.
func (s *Service) SetSongProvider(provider songGenerator) {
	s.songProvider = provider
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
		return quoted.Chain.ImageReferences()
	}
	return nil
}

// QuotedChain returns the fetched message this one replies to, or nil
// when there is no reply segment or the fetch fails. Sender info is
// attached so the prompt can attribute the quote.
func (s *Service) QuotedChain(
	ctx context.Context,
	selfID string,
	chain message.Chain,
) *message.FetchedMessage {
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
	msgType := event.MessageType
	if msgType == "" && event.PostType == "notice" {
		if chatID != "" {
			msgType = platform.ChatGroup
		} else if event.UserID.String() != "" {
			// Private notices (friend_recall, friend poke) carry no
			// group_id — keep them in the private lane.
			msgType = platform.ChatPrivate
		}
	}
	if msgType == platform.ChatPrivate {
		chatID = event.UserID.String()
	}
	s.handlePlatformEvent(ctx, platform.Event{
		ID:          fmt.Sprintf("%s:%s", event.SelfID.String(), event.MessageID.String()),
		Platform:    platform.PlatformQQOneBot,
		PostType:    event.PostType,
		MessageType: msgType,
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
			"time":        eventTime,
			"sender":      sender,
			"notice_type": event.NoticeType,
			"target_id":   event.TargetID.String(),
			"operator_id": event.OperatorID.String(),
			"honor_type":  event.HonorType,
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
	sessionID, plat, selfID, chatType, chatID, userID, senderName, messageID, text string,
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
		score := s.affinity.Score(plat, selfID, userID)
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
	parts, truncated := splitReply(
		text, s.cfg.MaxReplyRunes, s.cfg.MaxReplyChunks,
	)
	if truncated {
		parts = append(parts, truncationCloser(chatType))
	}
	return s.sendTextParts(
		ctx,
		selfID,
		chatType,
		chatID,
		userID,
		senderName,
		messageID,
		parts,
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
		if index == 0 {
			s.noteSpoke(chatType, chatID)
		}
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
	// Sulky or drowsy replies sit for a while before landing — a person
	// who isn't feeling it reads the @ and gets back to it when she gets
	// back to it. Zero for happy/neutral states so eagerness stays snappy.
	if len(chains) > 0 {
		if d := s.sulkDelay(selfID, chatType, chatID); d > 0 &&
			!waitForReplyPart(ctx, d) {
			return false
		}
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
		if index == 0 {
			s.noteSpoke(chatType, chatID)
		}
	}
	return true
}

// noteSpoke stamps when she last actually said something in a chat —
// the neglect check compares inbound traffic against this, not the
// pipeline's decision to reply (an ignored reply still counts as spoke).
func (s *Service) noteSpoke(chatType, chatID string) {
	s.socialMu.Lock()
	s.lastSpoke[chatType+":"+chatID] = s.now()
	s.socialMu.Unlock()
}

// recentReplyWindow marks how fresh her last send must be for an
// inbound message to count as a same-moment follow-up rather than a new
// topic — a person who just replied and immediately gets another line
// taps out a short continuation, not a fresh burst.
const recentReplyWindow = 90 * time.Second

// followUpMaxChunks caps the bubble burst for a same-moment follow-up
// reply — short continuations, not a second full burst.
const followUpMaxChunks = 2

// justRepliedRecently reports whether her last send in this chat landed
// inside recentReplyWindow. Read-only over lastSpoke; used to annotate
// and dampen back-to-back private replies.
func (s *Service) justRepliedRecently(chatType, chatID string) bool {
	s.socialMu.Lock()
	defer s.socialMu.Unlock()
	spoke, ok := s.lastSpoke[chatType+":"+chatID]
	return ok && s.now().Sub(spoke) < recentReplyWindow
}

// neglected reports whether she spoke recently and this inbound group
// message — not directed at her — means the room talked past her. Rate-
// limited: the sting lands once per 45 minutes, not per ignored line.
func (s *Service) neglected(chatType, chatID string) bool {
	key := chatType + ":" + chatID
	now := s.now()
	s.socialMu.Lock()
	defer s.socialMu.Unlock()
	spoke, ok := s.lastSpoke[key]
	if !ok || now.Sub(spoke) > 20*time.Minute {
		return false
	}
	if dip, ok := s.lastNeglect[key]; ok && now.Sub(dip) < 45*time.Minute {
		return false
	}
	s.lastNeglect[key] = now
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

// replyPartDelay simulates the gap before the next bubble lands: a
// short reaction beat plus the time it takes to actually type that
// part (~90ms per rune — a 15-rune line costs ~1.7s, which is what
// thumbs on glass look like). A rare mid-burst hesitation — re-reading
// the thread, second thoughts — stretches one gap past the typing
// time, the way a real burst isn't evenly spaced. Jittered and slowed
// when she's sleepy so a burst of bubbles doesn't land at a robotic
// fixed cadence.
func (s *Service) replyPartDelay(part string) time.Duration {
	if s.cfg.ReplyPartDelay <= 0 {
		return 0
	}
	extraRunes := min(utf8.RuneCountInString(part), 80)
	delay := s.cfg.ReplyPartDelay + time.Duration(extraRunes)*90*time.Millisecond
	// Mid-burst hesitation — roughly one gap in eight breathes.
	if rand.Float64() < 0.12 {
		delay += 1200*time.Millisecond + time.Duration(rand.Int63n(int64(1800*time.Millisecond)))
	}
	// ±30% jitter — real typing rhythm wobbles.
	jitter := time.Duration(rand.Int63n(int64(delay/3) + 1))
	delay += jitter - delay/6
	if s.cfg.Asleep(time.Now()) {
		delay += delay / 2
	}
	return delay
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

// sulkDelayGroupCap bounds any pre-reply stall in a group so a bad mood
// reads as tone, not a dead-air vanish-then-burst. A real person who's
// grumpy answers shorter and snippier — the words carry it, not minutes
// of silence.
const sulkDelayGroupCap = 5 * time.Second

// sulkDelay is the pre-reply stall of a person who isn't in the mood:
// grumpy or sleepy she takes a beat before answering. Private 1:1 never
// stalls — a person you're talking to directly doesn't leave you on read
// for a minute then dump everything; the mood lives in the reply's words.
// In a group the stall is kept to a few seconds (sulkDelayGroupCap) so it
// still feels like a beat, never absence. Happy/neutral returns zero.
func (s *Service) sulkDelay(selfID, chatType, chatID string) time.Duration {
	if s.chatPrefs == nil || chatType == platform.ChatPrivate {
		return 0
	}
	now := s.now()
	var lo, hi time.Duration
	switch {
	case s.cfg.Asleep(now):
		lo, hi = 2*time.Second, 5*time.Second
	case s.cfg.Waking(now):
		lo, hi = 1*time.Second, 4*time.Second
	case s.cfg.MoodEnabled:
		switch mood := s.chatPrefs.MoodScore(
			platform.PlatformQQOneBot, selfID, chatType, chatID, now,
		); {
		case mood <= -40:
			lo, hi = 3*time.Second, 5*time.Second
		case mood < 0:
			lo, hi = 1*time.Second, 4*time.Second
		default:
			return 0
		}
	default:
		return 0
	}
	d := lo + time.Duration(rand.Int63n(int64(hi-lo)+1))
	if d > sulkDelayGroupCap {
		d = sulkDelayGroupCap
	}
	return d
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
	return s.allowAttentionLimited(key, s.cfg.AttentionRateLimit)
}

// judgeReply sends a classification/judge call to the cheap model named by
// JUDGE_PROVIDER. When the judge provider is unset or fails mid-call it
// falls back to the caller's preferred provider, then the registry
// default, then the legacy agent client — a judge that costs nothing but
// silently dies would quietly reopen the token firehose.
func (s *Service) judgeReply(
	ctx context.Context, preferred string, request domain.AgentRequest,
) (domain.AgentResponse, error) {
	if s.providers != nil {
		if s.judgeProvider != "" {
			if response, err := s.providers.ReplyWith(ctx, s.judgeProvider, request); err == nil {
				return response, nil
			} else {
				s.logger.Warn("judge provider failed, falling back",
					"provider", s.judgeProvider, "error", err.Error())
			}
		}
		if preferred != "" {
			if response, err := s.providers.ReplyWith(ctx, preferred, request); err == nil {
				return response, nil
			}
		}
		return s.providers.Reply(ctx, request)
	}
	if s.agent != nil {
		return s.agent.Reply(ctx, request)
	}
	return domain.AgentResponse{}, errors.New("no judge provider is available")
}

// allowAttentionLimited is the per-key sliding-window check — reused for
// the per-actor cap and the per-chat cap so a flooding room and a
// flooding user hit independent budgets.
func (s *Service) allowAttentionLimited(key string, limit int) bool {
	if limit <= 0 {
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
	if len(requests) >= limit {
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

func splitReply(text string, maxRunes, maxChunks int) ([]string, bool) {
	text = strings.TrimSpace(text)
	if text == "" || maxRunes <= 0 || maxChunks <= 0 {
		return nil, false
	}

	remaining := []rune(text)
	chunks := make([]string, 0, min(maxChunks, len(remaining)/maxRunes+1))
	for len(remaining) > 0 && len(chunks) < maxChunks {
		size := min(maxRunes, len(remaining))
		if size < len(remaining) {
			// Cut at the last sentence terminator inside the cap —
			// a person doesn't split a word across two bubbles. No
			// text is lost here (the remainder flows to the next
			// chunk), so even an early boundary beats a mid-word cut.
			if end := lastSentenceEnd(remaining[:size]); end > 0 {
				size = end
			}
		}
		chunks = append(chunks, strings.TrimSpace(string(remaining[:size])))
		remaining = remaining[size:]
	}

	if len(remaining) == 0 {
		return chunks, false
	}
	// Overflow: a human who ran out of typing room doesn't paste a
	// "[truncated]" banner — they stop at a sentence break and let the
	// follow-up bubble say the rest. Walk the last chunk back to its
	// nearest terminator; keep it only when the trim isn't drastic.
	last := []rune(chunks[len(chunks)-1])
	if end := lastSentenceEnd(last); end >= len(last)/2 {
		chunks[len(chunks)-1] = strings.TrimSpace(string(last[:end]))
	}
	return chunks, true
}

// lastSentenceEnd returns the rune offset just past the last sentence
// terminator in text, or -1 when there is none. Newlines count so a
// bubble never dies mid-sentence inside a multi-line reply.
func lastSentenceEnd(text []rune) int {
	best := -1
	for i, r := range text {
		switch r {
		case '。', '！', '？', '!', '?', '；', ';', '\n':
			best = i
		}
	}
	return best + 1
}

// truncationCloser is the follow-up bubble when a reply got capped —
// group and private keep separate voices.
func truncationCloser(chatType string) string {
	if chatType == platform.ChatPrivate {
		return "太长了，先说到这，想继续再问"
	}
	return "群里讲不完了，想细聊私我"
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

// reminderLoop is the prospective-memory heartbeat: every minute it pops
// reminders whose time came and delivers them as spontaneous messages —
// she remembers on her own; nobody has to nudge her. Asleep hours defer
// the fire to wake-up, the way a person would not send a 4am reminder.
func (s *Service) reminderLoop() {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for range tick.C {
		if s.chatPrefs == nil || s.sender == nil {
			continue
		}
		now := s.now()
		for _, r := range s.chatPrefs.DueReminders(now) {
			if s.cfg.Asleep(now) {
				wake := time.Date(now.Year(), now.Month(), now.Day(),
					s.cfg.SleepEnd, 0, 0, 0, now.Location())
				if !wake.After(now) {
					wake = wake.Add(24 * time.Hour)
				}
				_ = s.chatPrefs.AddReminder(r.Platform, r.SelfID, r.ChatType, r.ChatID, r.Text, wake)
				continue
			}
			s.deliverReminder(r)
		}
	}
}

// deliverReminder turns a fired reminder into one natural bubble. With a
// working agent the persona voices it herself; without one a plain
// reminder line still lands the point instead of dropping it.
func (s *Service) deliverReminder(r chatprefs.DueReminder) {
	text := "对了，" + r.Text
	if composed := s.composeReminderText(r); composed != "" {
		text = composed
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := s.sender.Send(ctx, platform.Outbound{
		ChatType: r.ChatType,
		ChatID:   r.ChatID,
		SelfID:   r.SelfID,
		Chain:    message.Chain{message.Text(text)},
	}); err != nil {
		s.logger.Info("reminder send failed",
			"chat_type", r.ChatType, "chat_id", r.ChatID,
			"error", err.Error())
		return
	}
	s.noteSpoke(r.ChatType, r.ChatID)
	s.logger.Info("reminder delivered",
		"chat_type", r.ChatType, "chat_id", r.ChatID,
		"text", truncateRunes(r.Text, 30))
}

// composeReminderText asks the persona to voice her own reminder — the
// fact she remembered is context, the output is what she would actually
// say out loud.
func (s *Service) composeReminderText(r chatprefs.DueReminder) string {
	if s.providers == nil && s.agent == nil {
		return ""
	}
	inner := "你之前给自己留了个提醒：「" + r.Text + "」。现在时间到了，" +
		"像人一样自然地提起这件事（一两句话就行，别念提醒原文，别解释你在提醒）。"
	if s.personas != nil && s.chatPrefs != nil {
		persona := s.chatPrefs.PersonaName(r.Platform, r.SelfID, r.ChatType, r.ChatID)
		if profile, ok := s.personas.Get(persona); ok && profile.SystemPrompt != "" {
			inner = profile.SystemPrompt + "\n\n" + inner
		}
	}
	request := domain.AgentRequest{
		RequestID:     "remind:" + r.ChatID + ":" + truncateRunes(r.Text, 20),
		Text:          inner,
		RestrictTools: true,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var response domain.AgentResponse
	var err error
	if s.providers != nil {
		response, err = s.providers.Reply(ctx, request)
	} else {
		response, err = s.agent.Reply(ctx, request)
	}
	if err != nil {
		return ""
	}
	return truncateRunes(strings.TrimSpace(response.Reply), 120)
}
