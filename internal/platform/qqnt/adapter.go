package qqnt

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/message"
	"github.com/lianyorker/cinlan-qq-bot/internal/platform"
)

const (
	defaultMaximumFrameBytes = 1024 * 1024
	eventQueueSize           = 512
)

var avIntegerPattern = regexp.MustCompile(`^-?[0-9]{1,128}$`)
var friendUINPattern = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)

type Config struct {
	ListenAddr       string
	Token            string
	ActionTimeout    time.Duration
	HandshakeTimeout time.Duration
	MaxFrameBytes    int
	AutoLaunch       bool
	AllowRunning     bool
	AutoAcceptFriend bool
	Headless         bool

	QQExecutable     string
	LoaderPath       string
	HookPath         string
	LoadPath         string
	RuntimePath      string
	PatchPackagePath string
	ImageSendRoots   []string
	ImageMaxBytes    int64
}

type RuntimeInfo struct {
	Connected              bool            `json:"connected"`
	Ready                  bool            `json:"ready"`
	State                  string          `json:"state"`
	SelfID                 string          `json:"self_id"`
	SelfUID                string          `json:"self_uid"`
	Nickname               string          `json:"nickname"`
	Runtime                string          `json:"runtime"`
	PID                    int             `json:"pid"`
	QQVersion              string          `json:"qq_version"`
	Capabilities           []string        `json:"capabilities"`
	WrapperLoaded          bool            `json:"wrapper_loaded"`
	SessionAttached        bool            `json:"session_attached"`
	AVSDKAvailable         bool            `json:"avsdk_available"`
	AVSDKListenerAttached  bool            `json:"avsdk_listener_attached"`
	AVSDKMethods           []string        `json:"avsdk_methods,omitempty"`
	LastAVEvent            *AVEventSummary `json:"last_av_event,omitempty"`
	LastError              string          `json:"last_error"`
	FriendListenerAttached bool            `json:"friend_listener_attached"`
	LastFriendRequest      *FriendRequest  `json:"last_friend_request,omitempty"`
}

type actionResult struct {
	envelope envelope
	err      error
}

type Adapter struct {
	cfg    Config
	logger *slog.Logger
	token  string
	events chan platform.Event

	connected atomic.Bool
	ready     atomic.Bool
	sequence  atomic.Uint64

	connMu sync.RWMutex
	conn   net.Conn

	writeMu sync.Mutex

	pendingMu sync.Mutex
	pending   map[string]chan actionResult

	statusMu sync.RWMutex
	status   RuntimeInfo
	loginQR  LoginQR
}

func NewAdapter(cfg Config, logger *slog.Logger) (*Adapter, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if strings.TrimSpace(cfg.ListenAddr) == "" {
		cfg.ListenAddr = "127.0.0.1:18081"
	}
	if err := validateLoopbackAddress(cfg.ListenAddr); err != nil {
		return nil, err
	}
	if cfg.ActionTimeout <= 0 {
		cfg.ActionTimeout = 10 * time.Second
	}
	if cfg.HandshakeTimeout <= 0 {
		cfg.HandshakeTimeout = 10 * time.Second
	}
	if cfg.MaxFrameBytes <= 0 {
		cfg.MaxFrameBytes = defaultMaximumFrameBytes
	}
	if cfg.MaxFrameBytes < 4096 {
		return nil, fmt.Errorf("QQNT IPC maximum frame size must be at least 4096 bytes")
	}

	token := strings.TrimSpace(cfg.Token)
	if token == "" {
		generated, err := randomToken()
		if err != nil {
			return nil, err
		}
		token = generated
	}
	return &Adapter{
		cfg:     cfg,
		logger:  logger,
		token:   token,
		events:  make(chan platform.Event, eventQueueSize),
		pending: make(map[string]chan actionResult),
		status:  RuntimeInfo{State: "disconnected"},
	}, nil
}

func (a *Adapter) Name() string {
	return platform.PlatformQQNative
}

func (a *Adapter) Connected() bool {
	return a.connected.Load() && a.ready.Load()
}

func (a *Adapter) Events() <-chan platform.Event {
	return a.events
}

func (a *Adapter) RuntimeInfo() RuntimeInfo {
	a.statusMu.RLock()
	defer a.statusMu.RUnlock()
	info := a.status
	info.Capabilities = append([]string(nil), info.Capabilities...)
	info.AVSDKMethods = append([]string(nil), info.AVSDKMethods...)
	info.LastAVEvent = cloneAVEvent(info.LastAVEvent)
	if info.LastFriendRequest != nil {
		request := *info.LastFriendRequest
		info.LastFriendRequest = &request
	}
	return info
}

func (a *Adapter) Run(ctx context.Context) error {
	listener, err := net.Listen("tcp", a.cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen for QQNT runtime on %s: %w", a.cfg.ListenAddr, err)
	}
	defer listener.Close()
	defer close(a.events)

	go func() {
		<-ctx.Done()
		_ = listener.Close()
		a.closeConnection()
	}()

	a.logger.Info("QQNT IPC listener ready", "address", listener.Addr().String())
	if a.cfg.AutoLaunch {
		if err := a.launch(ctx, listener.Addr().String(), a.token); err != nil {
			a.statusMu.Lock()
			a.status.LastError = err.Error()
			a.statusMu.Unlock()
			return err
		}
	}

	for {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			if ctx.Err() != nil || errors.Is(acceptErr, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("accept QQNT runtime connection: %w", acceptErr)
		}
		if err := a.serveConnection(ctx, connection); err != nil && ctx.Err() == nil {
			a.logger.Warn("QQNT runtime disconnected", "error", err)
		}
	}
}

func (a *Adapter) Send(ctx context.Context, outbound platform.Outbound) error {
	info := a.RuntimeInfo()
	if !info.Ready {
		if info.LastError != "" {
			return fmt.Errorf(
				"QQNT runtime is not ready (state %s): %s",
				info.State,
				info.LastError,
			)
		}
		return fmt.Errorf("QQNT runtime is not ready (state %s)", info.State)
	}
	if outbound.SelfID != "" && info.SelfID != "" && outbound.SelfID != info.SelfID {
		return fmt.Errorf(
			"QQNT runtime account %s cannot send for account %s",
			info.SelfID,
			outbound.SelfID,
		)
	}
	params := map[string]any{
		"chat_type": outbound.ChatType,
		"chat_id":   outbound.ChatID,
		"reply_to":  outbound.ReplyTo,
		"quote":     outbound.Quote,
		"chain":     outbound.Chain.Clone(),
	}
	_, err := a.Call(ctx, "send_message", params)
	return err
}

func (a *Adapter) SendGroupText(
	ctx context.Context,
	groupID, replyTo, text string,
	quote bool,
) error {
	return a.Send(ctx, platform.Outbound{
		ChatType: platform.ChatGroup,
		ChatID:   groupID,
		ReplyTo:  replyTo,
		Quote:    quote,
		Chain:    message.Chain{message.Text(text)},
	})
}

func (a *Adapter) Call(
	ctx context.Context,
	action string,
	params map[string]any,
) (any, error) {
	if !a.connected.Load() {
		return nil, fmt.Errorf("QQNT runtime is not connected")
	}
	action = strings.TrimSpace(action)
	if action == "" {
		return nil, fmt.Errorf("QQNT action is empty")
	}
	id := fmt.Sprintf("native-%d", a.sequence.Add(1))
	payload, err := json.Marshal(actionPayload{Name: action, Params: params})
	if err != nil {
		return nil, fmt.Errorf("encode QQNT action %q: %w", action, err)
	}

	resultCh := make(chan actionResult, 1)
	a.pendingMu.Lock()
	a.pending[id] = resultCh
	a.pendingMu.Unlock()
	defer func() {
		a.pendingMu.Lock()
		delete(a.pending, id)
		a.pendingMu.Unlock()
	}()

	if err := a.write(envelope{Type: "action", ID: id, Payload: payload}); err != nil {
		return nil, err
	}

	waitCtx, cancel := context.WithTimeout(ctx, a.cfg.ActionTimeout)
	defer cancel()
	select {
	case <-waitCtx.Done():
		return nil, fmt.Errorf("QQNT action %q: %w", action, waitCtx.Err())
	case result := <-resultCh:
		if result.err != nil {
			return nil, result.err
		}
		if !result.envelope.OK {
			detail := strings.TrimSpace(result.envelope.Error)
			if detail == "" {
				detail = "runtime returned an unspecified error"
			}
			return nil, fmt.Errorf("QQNT action %q failed: %s", action, detail)
		}
		return decodeAny(result.envelope.Payload)
	}
}

func (a *Adapter) serveConnection(ctx context.Context, connection net.Conn) error {
	defer connection.Close()
	scanner := bufio.NewScanner(connection)
	scanner.Buffer(make([]byte, 4096), a.cfg.MaxFrameBytes)
	if err := connection.SetReadDeadline(time.Now().Add(a.cfg.HandshakeTimeout)); err != nil {
		return err
	}
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return fmt.Errorf("read QQNT hello: %w", err)
		}
		return io.EOF
	}
	hello, err := decodeEnvelope(scanner.Bytes())
	if err != nil {
		return err
	}
	if hello.Type != "hello" {
		return fmt.Errorf("first QQNT IPC frame must be hello, got %q", hello.Type)
	}
	if subtle.ConstantTimeCompare([]byte(hello.Token), []byte(a.token)) != 1 {
		return fmt.Errorf("QQNT IPC authentication failed")
	}
	var details helloPayload
	if err := decodePayload(hello.Payload, &details); err != nil {
		return err
	}
	if details.Runtime != "cinlan-qqnt" {
		return fmt.Errorf("unexpected QQNT runtime %q", details.Runtime)
	}
	if err := connection.SetReadDeadline(time.Time{}); err != nil {
		return err
	}

	a.setConnection(connection, details)
	defer a.clearConnection(connection, fmt.Errorf("QQNT runtime connection closed"))
	if err := a.write(envelope{
		Type:    "hello_ack",
		Payload: json.RawMessage(`{"accepted":true}`),
	}); err != nil {
		return err
	}
	a.logger.Info(
		"QQNT runtime authenticated",
		"pid", details.PID,
		"qq_version", details.QQVersion,
	)
	// Action waits must not block the reader that delivers their replies.
	friendCtx, cancelFriends := context.WithCancel(ctx)
	friendQueue := make(chan FriendRequest, 32)
	friendDone := make(chan struct{})
	go func() {
		defer close(friendDone)
		for {
			select {
			case <-friendCtx.Done():
				return
			case request := <-friendQueue:
				if friendCtx.Err() != nil {
					return
				}
				_, err := a.Call(friendCtx, "approve_friend", map[string]any{
					"flag": request.Flag, "approve": true, "remark": "",
				})
				if err != nil {
					a.statusMu.Lock()
					a.status.LastError = err.Error()
					a.statusMu.Unlock()
					a.logger.Error("QQNT friend request auto-accept failed", "uin", request.UIN, "error", err)
				} else {
					a.logger.Info("QQNT friend request auto-accepted", "uin", request.UIN)
				}
			}
		}
	}()
	defer func() { cancelFriends(); <-friendDone }()

	for scanner.Scan() {
		current, decodeErr := decodeEnvelope(scanner.Bytes())
		if decodeErr != nil {
			return decodeErr
		}
		switch current.Type {
		case "runtime_status":
			if err := a.handleStatus(current.Payload); err != nil {
				return err
			}
		case "event":
			event, err := decodeNativeEvent(current.Payload)
			if err != nil {
				a.logger.Warn("ignored QQNT event", "reason", err.Error())
				continue
			}
			select {
			case <-ctx.Done():
				return nil
			case a.events <- event:
			}
		case "av_event":
			if err := a.handleAVEvent(current.Payload); err != nil {
				a.logger.Warn("ignored QQNT AVSDK event", "reason", err.Error())
			}
		case "action_result":
			a.handleActionResult(current)
		case "friend_request":
			var request FriendRequest
			if err := decodePayload(current.Payload, &request); err != nil ||
				!friendUINPattern.MatchString(request.UIN) || request.UID == "" ||
				request.Flag == "" || len(request.Flag) > 256 || len(request.UID) > 128 ||
				len(request.Comment) > 4096 || len(request.Nickname) > 512 {
				a.logger.Warn("ignored invalid QQNT friend request")
				continue
			}
			a.statusMu.Lock()
			a.status.LastFriendRequest = &request
			a.statusMu.Unlock()
			a.logger.Info("QQNT friend request observed", "uin", request.UIN)
			if a.cfg.AutoAcceptFriend {
				select {
				case friendQueue <- request:
				default:
					a.logger.Error("QQNT friend auto-accept queue full", "uin", request.UIN)
				}
			}
		case "login_qr":
			var qr LoginQR
			if err := decodePayload(current.Payload, &qr); err != nil {
				return err
			}
			if err := validateLoginQR(qr); err != nil {
				return err
			}
			a.statusMu.Lock()
			a.loginQR = qr
			a.statusMu.Unlock()
		case "hello":
			return fmt.Errorf("QQNT runtime sent duplicate hello")
		default:
			a.logger.Debug("ignored QQNT IPC frame", "type", current.Type)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read QQNT IPC frame: %w", err)
	}
	return io.EOF
}

func (a *Adapter) handleStatus(raw json.RawMessage) error {
	var current runtimeStatus
	if err := decodePayload(raw, &current); err != nil {
		return err
	}
	ready := current.State == "ready" &&
		current.SelfID != "" &&
		current.WrapperLoaded &&
		current.SessionAttached && (!a.cfg.AutoAcceptFriend || current.FriendListenerAttached)
	a.ready.Store(ready)
	a.statusMu.Lock()
	a.status.State = current.State
	a.status.SelfID = current.SelfID
	a.status.SelfUID = current.SelfUID
	a.status.Nickname = current.Nickname
	a.status.WrapperLoaded = current.WrapperLoaded
	a.status.SessionAttached = current.SessionAttached
	a.status.FriendListenerAttached = current.FriendListenerAttached
	a.status.AVSDKAvailable = current.AVSDKAvailable
	a.status.AVSDKListenerAttached = current.AVSDKListenerAttached
	a.status.AVSDKMethods = append(
		a.status.AVSDKMethods[:0],
		current.AVSDKMethods...,
	)
	a.status.LastError = current.LastError
	a.status.Ready = ready
	if ready {
		a.loginQR = LoginQR{State: "ready", Status: "ready"}
	}
	a.statusMu.Unlock()
	a.logger.Info(
		"QQNT runtime status changed",
		"state", current.State,
		"self_id", current.SelfID,
		"wrapper_loaded", current.WrapperLoaded,
		"session_attached", current.SessionAttached,
		"avsdk_available", current.AVSDKAvailable,
		"avsdk_listener_attached", current.AVSDKListenerAttached,
		"last_error", current.LastError,
	)
	return nil
}

func (a *Adapter) handleAVEvent(raw json.RawMessage) error {
	var current AVEventSummary
	if err := decodePayload(raw, &current); err != nil {
		return err
	}
	if err := validateAVEvent(current); err != nil {
		return err
	}
	a.statusMu.Lock()
	a.status.LastAVEvent = cloneAVEvent(&current)
	a.statusMu.Unlock()
	actionCodeCandidate := any(nil)
	if current.ActionCodeCandidate != nil {
		actionCodeCandidate = *current.ActionCodeCandidate
	}
	a.logger.Info(
		"QQNT AVSDK event observed",
		"callback", current.Callback,
		"action_code_candidate", actionCodeCandidate,
		"argument_count", current.ArgumentCount,
	)
	return nil
}

func validateAVEvent(current AVEventSummary) error {
	switch current.Callback {
	case "onActionToAVSDK",
		"onS2CActionToAVSDK",
		"OnGroupVideoActionToAVSDK",
		"OnInviteActionToAVSDK",
		"OnGroupVideoServerPushToAVSDK":
	default:
		return fmt.Errorf("unsupported AVSDK callback %q", current.Callback)
	}
	if current.Sequence == 0 {
		return fmt.Errorf("AVSDK event sequence is empty")
	}
	if current.ArgumentCount < 0 ||
		len(current.Arguments) > 8 ||
		current.ArgumentCount < len(current.Arguments) {
		return fmt.Errorf("invalid AVSDK argument count")
	}
	for _, argument := range current.Arguments {
		switch argument.Type {
		case "buffer", "null", "undefined", "number", "bigint",
			"boolean", "string", "object", "function", "symbol",
			"unavailable":
		default:
			return fmt.Errorf("unsupported AVSDK argument type %q", argument.Type)
		}
		if argument.ByteLength < 0 || len(argument.Keys) > 16 {
			return fmt.Errorf("invalid AVSDK argument summary")
		}
		if argument.NumberSpecial != "" {
			switch argument.NumberSpecial {
			case "NaN", "Infinity", "-Infinity":
			default:
				return fmt.Errorf("invalid AVSDK special number")
			}
		}
		if argument.IntegerValue != "" &&
			!avIntegerPattern.MatchString(argument.IntegerValue) {
			return fmt.Errorf("invalid AVSDK integer value")
		}
		if len(argument.ObjectType) > 64 {
			return fmt.Errorf("invalid AVSDK object type")
		}
		for _, key := range argument.Keys {
			if len(key) > 64 {
				return fmt.Errorf("invalid AVSDK object key")
			}
		}
		if argument.SHA256 != "" {
			if len(argument.SHA256) != 64 {
				return fmt.Errorf("invalid AVSDK argument SHA256")
			}
			if _, err := hex.DecodeString(argument.SHA256); err != nil {
				return fmt.Errorf("invalid AVSDK argument SHA256")
			}
		}
	}
	return nil
}

func cloneAVEvent(source *AVEventSummary) *AVEventSummary {
	if source == nil {
		return nil
	}
	result := *source
	if source.ActionCodeCandidate != nil {
		actionCodeCandidate := *source.ActionCodeCandidate
		result.ActionCodeCandidate = &actionCodeCandidate
	}
	result.Arguments = append([]AVArgumentSummary(nil), source.Arguments...)
	for index := range result.Arguments {
		result.Arguments[index].Keys = append(
			[]string(nil),
			source.Arguments[index].Keys...,
		)
	}
	return &result
}

func (a *Adapter) handleActionResult(current envelope) {
	a.pendingMu.Lock()
	resultCh := a.pending[current.ID]
	a.pendingMu.Unlock()
	if resultCh == nil {
		a.logger.Debug("ignored late QQNT action result", "id", current.ID)
		return
	}
	select {
	case resultCh <- actionResult{envelope: current}:
	default:
	}
}

func (a *Adapter) write(current envelope) error {
	encoded, err := encodeEnvelope(current, a.cfg.MaxFrameBytes)
	if err != nil {
		return err
	}
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	a.connMu.RLock()
	connection := a.conn
	a.connMu.RUnlock()
	if connection == nil {
		return fmt.Errorf("QQNT runtime is not connected")
	}
	if err := connection.SetWriteDeadline(time.Now().Add(a.cfg.ActionTimeout)); err != nil {
		return err
	}
	defer connection.SetWriteDeadline(time.Time{})
	if _, err := connection.Write(encoded); err != nil {
		return fmt.Errorf("write QQNT IPC frame: %w", err)
	}
	return nil
}

func (a *Adapter) setConnection(connection net.Conn, hello helloPayload) {
	a.closeConnection()
	a.connMu.Lock()
	a.conn = connection
	a.connMu.Unlock()
	a.connected.Store(true)
	a.ready.Store(false)
	a.statusMu.Lock()
	a.status = RuntimeInfo{
		Connected:    true,
		State:        "connected",
		Runtime:      hello.Runtime,
		PID:          hello.PID,
		QQVersion:    hello.QQVersion,
		Capabilities: append([]string(nil), hello.Capabilities...),
	}
	a.loginQR = LoginQR{}
	a.statusMu.Unlock()
}

func (a *Adapter) clearConnection(connection net.Conn, cause error) {
	a.connMu.Lock()
	if a.conn != connection {
		a.connMu.Unlock()
		return
	}
	a.conn = nil
	a.connMu.Unlock()
	a.connected.Store(false)
	a.ready.Store(false)
	a.statusMu.Lock()
	a.status.Connected = false
	a.status.Ready = false
	a.status.State = "disconnected"
	a.loginQR = LoginQR{}
	a.statusMu.Unlock()
	a.failPending(cause)
}

func (a *Adapter) closeConnection() {
	a.connMu.Lock()
	connection := a.conn
	a.conn = nil
	a.connMu.Unlock()
	if connection != nil {
		_ = connection.Close()
	}
	a.connected.Store(false)
	a.ready.Store(false)
}

func (a *Adapter) failPending(cause error) {
	a.pendingMu.Lock()
	pending := a.pending
	a.pending = make(map[string]chan actionResult)
	a.pendingMu.Unlock()
	for _, resultCh := range pending {
		select {
		case resultCh <- actionResult{err: cause}:
		default:
		}
	}
}

func validateLoopbackAddress(address string) error {
	host, _, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil {
		return fmt.Errorf("QQNT_IPC_LISTEN_ADDR must be host:port: %w", err)
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("QQNT_IPC_LISTEN_ADDR must bind to loopback")
	}
	return nil
}

func randomToken() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate QQNT IPC token: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}
