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

type Config struct {
	ListenAddr       string
	Token            string
	ActionTimeout    time.Duration
	HandshakeTimeout time.Duration
	MaxFrameBytes    int
	AutoLaunch       bool
	AllowRunning     bool

	QQExecutable     string
	LoaderPath       string
	HookPath         string
	LoadPath         string
	RuntimePath      string
	PatchPackagePath string
}

type RuntimeInfo struct {
	Connected       bool     `json:"connected"`
	Ready           bool     `json:"ready"`
	State           string   `json:"state"`
	SelfID          string   `json:"self_id"`
	SelfUID         string   `json:"self_uid"`
	Nickname        string   `json:"nickname"`
	Runtime         string   `json:"runtime"`
	PID             int      `json:"pid"`
	QQVersion       string   `json:"qq_version"`
	Capabilities    []string `json:"capabilities"`
	WrapperLoaded   bool     `json:"wrapper_loaded"`
	SessionAttached bool     `json:"session_attached"`
	LastError       string   `json:"last_error"`
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

	waitCtx := ctx
	var cancel context.CancelFunc
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		waitCtx, cancel = context.WithTimeout(ctx, a.cfg.ActionTimeout)
		defer cancel()
	}
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
		case "action_result":
			a.handleActionResult(current)
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
		current.SessionAttached
	a.ready.Store(ready)
	a.statusMu.Lock()
	a.status.State = current.State
	a.status.SelfID = current.SelfID
	a.status.SelfUID = current.SelfUID
	a.status.Nickname = current.Nickname
	a.status.WrapperLoaded = current.WrapperLoaded
	a.status.SessionAttached = current.SessionAttached
	a.status.LastError = current.LastError
	a.status.Ready = ready
	a.statusMu.Unlock()
	a.logger.Info(
		"QQNT runtime status changed",
		"state", current.State,
		"self_id", current.SelfID,
		"wrapper_loaded", current.WrapperLoaded,
		"session_attached", current.SessionAttached,
		"last_error", current.LastError,
	)
	return nil
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
