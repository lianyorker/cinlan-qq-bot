package onebot

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/lianyorker/cinlan-qq-bot/internal/message"
)

const (
	eventBufferSize = 512
	readLimit       = 4 << 20

	TransportForwardWebSocket = "forward_ws"
	TransportReverseWebSocket = "reverse_ws"
	TransportHTTPSSE          = "http_sse"
	TransportReverseHTTP      = "reverse_http"
)

type ClientConfig struct {
	URL                   string
	AccessToken           string
	ActionTimeout         time.Duration
	Transport             string
	ListenAddr            string
	Path                  string
	QuickOperationHandler func(context.Context, Event) (map[string]any, error)
}

type Client struct {
	cfg      ClientConfig
	logger   *slog.Logger
	endpoint string
	events   chan Event
	http     *http.Client

	connMu sync.RWMutex
	conn   *websocket.Conn

	writeMu sync.Mutex

	pendingMu sync.Mutex
	pending   map[string]chan actionResult
	echo      atomic.Uint64

	connected atomic.Bool
}

func NewClient(cfg ClientConfig, logger *slog.Logger) *Client {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.ActionTimeout <= 0 {
		cfg.ActionTimeout = 10 * time.Second
	}
	cfg.Transport = normalizeTransport(cfg.Transport)
	if cfg.Transport == TransportReverseWebSocket || cfg.Transport == TransportReverseHTTP {
		if strings.TrimSpace(cfg.ListenAddr) == "" {
			cfg.ListenAddr = "127.0.0.1:3002"
		}
		fallbackPath := "/onebot/v11/ws"
		if cfg.Transport == TransportReverseHTTP {
			fallbackPath = "/onebot/v11/events"
		}
		cfg.Path = normalizeListenerPath(cfg.Path, fallbackPath)
	}
	endpoint := endpointSummary(cfg.URL)
	if cfg.Transport == TransportReverseWebSocket {
		endpoint = "ws://" + cfg.ListenAddr + cfg.Path
	} else if cfg.Transport == TransportReverseHTTP {
		endpoint = "http://" + cfg.ListenAddr + cfg.Path
	}
	return &Client{
		cfg:      cfg,
		logger:   logger,
		endpoint: endpoint,
		events:   make(chan Event, eventBufferSize),
		http: &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return errors.New("onebot HTTP redirect is not allowed")
			},
		},
		pending: make(map[string]chan actionResult),
	}
}

func (c *Client) Events() <-chan Event {
	return c.events
}

func (c *Client) Connected() bool {
	return c.connected.Load()
}

func (c *Client) Run(ctx context.Context) error {
	defer close(c.events)
	transport := normalizeTransport(c.cfg.Transport)
	var err error
	switch transport {
	case TransportForwardWebSocket:
		err = c.runForward(ctx)
	case TransportReverseWebSocket:
		err = c.runReverse(ctx)
	case TransportHTTPSSE:
		err = c.runHTTPSSE(ctx)
	case TransportReverseHTTP:
		err = c.runReverseHTTP(ctx)
	default:
		err = fmt.Errorf("unsupported OneBot transport %q", c.cfg.Transport)
	}
	c.failPending(errors.New("onebot client stopped"))
	return err
}

func (c *Client) runForward(ctx context.Context) error {
	backoff := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		err := c.dialConnection(ctx)
		if ctx.Err() != nil {
			break
		}
		if time.Since(started) >= 30*time.Second {
			backoff = time.Second
		}

		c.logger.Warn(
			"onebot websocket disconnected",
			"endpoint", c.endpoint,
			"retry_in_ms", backoff.Milliseconds(),
			"error", redactConnectionError(err, c.cfg.URL, c.endpoint),
		)
		if err := sleep(ctx, backoff); err != nil {
			break
		}
		if backoff < 30*time.Second {
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
	}
	return nil
}

func (c *Client) dialConnection(ctx context.Context) error {
	headers := make(http.Header)
	if c.cfg.AccessToken != "" {
		headers.Set("Authorization", "Bearer "+c.cfg.AccessToken)
	}

	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, response, err := websocket.DefaultDialer.DialContext(dialCtx, c.cfg.URL, headers)
	if err != nil {
		if response != nil {
			return fmt.Errorf("websocket handshake returned HTTP %d", response.StatusCode)
		}
		return err
	}
	return c.serveConnection(ctx, conn)
}

func (c *Client) serveConnection(ctx context.Context, conn *websocket.Conn) error {
	conn.SetReadLimit(readLimit)
	if err := conn.SetReadDeadline(time.Now().Add(90 * time.Second)); err != nil {
		conn.Close()
		return err
	}
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	})

	c.setConnection(conn)
	c.logger.Info("onebot websocket connected", "endpoint", c.endpoint)

	defer func() {
		c.clearConnection(conn)
		c.failPending(errors.New("onebot websocket disconnected"))
		_ = conn.Close()
	}()

	readErr := make(chan error, 1)
	go func() {
		readErr <- c.readLoop(ctx, conn)
	}()

	pingTicker := time.NewTicker(30 * time.Second)
	defer pingTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = conn.WriteControl(
				websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, "shutdown"),
				time.Now().Add(time.Second),
			)
			return ctx.Err()
		case err := <-readErr:
			return err
		case <-pingTicker.C:
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
				return err
			}
		}
	}
}

func (c *Client) runReverse(ctx context.Context) error {
	accepted := make(chan *websocket.Conn, 1)
	upgrader := websocket.Upgrader{
		CheckOrigin: reverseOriginAllowed,
	}
	mux := http.NewServeMux()
	mux.HandleFunc(c.cfg.Path, func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			writer.Header().Set("Allow", http.MethodGet)
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !c.authorizeReverse(request) {
			writer.Header().Set("WWW-Authenticate", `Bearer realm="onebot-reverse-ws"`)
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		if c.Connected() {
			http.Error(writer, "onebot connection already active", http.StatusConflict)
			return
		}
		conn, err := upgrader.Upgrade(writer, request, nil)
		if err != nil {
			return
		}
		select {
		case accepted <- conn:
		default:
			_ = conn.WriteControl(
				websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseTryAgainLater, "another connection is pending"),
				time.Now().Add(time.Second),
			)
			_ = conn.Close()
		}
	})

	listener, err := net.Listen("tcp", c.cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen for OneBot reverse websocket: %w", err)
	}
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.Serve(listener)
	}()
	c.logger.Info("onebot reverse websocket listening", "endpoint", c.endpoint)

	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = server.Shutdown(shutdownCtx)
		cancel()
		select {
		case pending := <-accepted:
			_ = pending.Close()
		default:
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case serveErr := <-serverErr:
			if errors.Is(serveErr, http.ErrServerClosed) {
				return nil
			}
			return serveErr
		case conn := <-accepted:
			if err := c.serveConnection(ctx, conn); err != nil && ctx.Err() == nil {
				c.logger.Warn(
					"onebot reverse websocket disconnected",
					"endpoint", c.endpoint,
					"error", redactConnectionError(err, "", c.endpoint),
				)
			}
		}
	}
}

func (c *Client) authorizeReverse(request *http.Request) bool {
	expected := c.cfg.AccessToken
	if expected == "" {
		return true
	}
	provided := strings.TrimSpace(request.Header.Get("Authorization"))
	if len(provided) >= len("Bearer ") && strings.EqualFold(provided[:len("Bearer ")], "Bearer ") {
		provided = strings.TrimSpace(provided[len("Bearer "):])
	} else {
		provided = strings.TrimSpace(request.URL.Query().Get("access_token"))
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}

func reverseOriginAllowed(request *http.Request) bool {
	origin := strings.TrimSpace(request.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	return err == nil && strings.EqualFold(parsed.Host, request.Host)
}

func (c *Client) readLoop(ctx context.Context, conn *websocket.Conn) error {
	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			return err
		}

		var envelope struct {
			PostType string          `json:"post_type"`
			Echo     json.RawMessage `json:"echo"`
		}
		if err := json.Unmarshal(payload, &envelope); err != nil {
			c.logger.Warn("ignored invalid onebot payload", "reason", "invalid_json")
			continue
		}

		if echo := echoString(envelope.Echo); echo != "" {
			var response apiResponse
			if err := json.Unmarshal(payload, &response); err != nil {
				continue
			}
			c.resolvePending(echo, actionResult{response: response})
			continue
		}
		if envelope.PostType == "" {
			continue
		}

		event, err := decodeEvent(payload)
		if err != nil {
			c.logger.Warn("ignored invalid onebot event", "reason", "contract_mismatch")
			continue
		}
		if err := c.emitEvent(ctx, event); err != nil {
			return err
		}
	}
}

func (c *Client) SendGroupText(ctx context.Context, groupID, replyTo, text string, quote bool) error {
	chain := message.Chain{message.Text(text)}
	if quote && replyTo != "" {
		chain = append(message.Chain{message.Reply(replyTo)}, chain...)
	}
	return c.SendGroupMessage(ctx, groupID, chain)
}

func (c *Client) SendGroupMessage(ctx context.Context, groupID string, chain message.Chain) error {
	params := map[string]any{
		"group_id": idValue(groupID),
		"message":  toSegments(chain),
	}
	_, err := c.sendAction(ctx, "send_group_msg", params)
	return err
}

func (c *Client) SendPrivateMessage(ctx context.Context, userID string, chain message.Chain) error {
	params := map[string]any{
		"user_id": idValue(userID),
		"message": toSegments(chain),
	}
	_, err := c.sendAction(ctx, "send_private_msg", params)
	return err
}

func (c *Client) SendPrivateText(ctx context.Context, userID, text string) error {
	return c.SendPrivateMessage(ctx, userID, message.Chain{message.Text(text)})
}

func (c *Client) SendGroupImage(ctx context.Context, groupID, file string) error {
	return c.SendGroupMessage(ctx, groupID, message.Chain{
		message.Attachment(message.TypeImage, map[string]any{"file": file}),
	})
}

func (c *Client) SendGroupRecord(ctx context.Context, groupID, file string) error {
	return c.SendGroupMessage(ctx, groupID, message.Chain{
		message.Attachment(message.TypeRecord, map[string]any{"file": file}),
	})
}

func (c *Client) SendGroupFile(ctx context.Context, groupID, file, name string) error {
	data := map[string]any{"file": file}
	if name != "" {
		data["name"] = name
	}
	return c.SendGroupMessage(ctx, groupID, message.Chain{
		message.Attachment(message.TypeFile, data),
	})
}

func (c *Client) DeleteMessage(ctx context.Context, messageID string) error {
	_, err := c.sendAction(ctx, "delete_msg", map[string]any{"message_id": idValue(messageID)})
	return err
}

func (c *Client) GetLoginInfo(ctx context.Context) (map[string]any, error) {
	data, err := c.sendAction(ctx, "get_login_info", nil)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("decode get_login_info response: %w", err)
	}
	return result, nil
}

func (c *Client) GetGroupInfo(ctx context.Context, groupID string, noCache bool) (map[string]any, error) {
	data, err := c.sendAction(ctx, "get_group_info", map[string]any{
		"group_id": idValue(groupID),
		"no_cache": noCache,
	})
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("decode get_group_info response: %w", err)
	}
	return result, nil
}

func (c *Client) GetGroupMemberInfo(ctx context.Context, groupID, userID string, noCache bool) (map[string]any, error) {
	data, err := c.sendAction(ctx, "get_group_member_info", map[string]any{
		"group_id": idValue(groupID),
		"user_id":  idValue(userID),
		"no_cache": noCache,
	})
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("decode get_group_member_info response: %w", err)
	}
	return result, nil
}

func (c *Client) SetGroupBan(ctx context.Context, groupID, userID string, duration time.Duration) error {
	seconds := int64(duration / time.Second)
	if seconds < 0 {
		seconds = 0
	}
	_, err := c.sendAction(ctx, "set_group_ban", map[string]any{
		"group_id": idValue(groupID),
		"user_id":  idValue(userID),
		"duration": seconds,
	})
	return err
}

func (c *Client) SetGroupWholeBan(ctx context.Context, groupID string, enabled bool) error {
	_, err := c.sendAction(ctx, "set_group_whole_ban", map[string]any{
		"group_id": idValue(groupID),
		"enable":   enabled,
	})
	return err
}

func (c *Client) SetGroupKick(ctx context.Context, groupID, userID string, rejectAddRequest bool) error {
	_, err := c.sendAction(ctx, "set_group_kick", map[string]any{
		"group_id":           idValue(groupID),
		"user_id":            idValue(userID),
		"reject_add_request": rejectAddRequest,
	})
	return err
}

func (c *Client) GetMessage(ctx context.Context, messageID string) (json.RawMessage, error) {
	return c.sendAction(ctx, "get_msg", map[string]any{"message_id": idValue(messageID)})
}

func (c *Client) GetGroupList(ctx context.Context) (json.RawMessage, error) {
	return c.sendAction(ctx, "get_group_list", nil)
}

func (c *Client) GetFriendList(ctx context.Context) (json.RawMessage, error) {
	return c.sendAction(ctx, "get_friend_list", nil)
}

// CallAction exposes the OneBot action surface to platform plugins while
// keeping authentication, echo correlation, timeouts and reconnect handling
// inside the client.
func (c *Client) CallAction(ctx context.Context, action string, params map[string]any) (json.RawMessage, error) {
	return c.sendAction(ctx, action, params)
}

func (c *Client) sendAction(parent context.Context, action string, params map[string]any) (json.RawMessage, error) {
	action = strings.TrimSpace(action)
	if action == "" {
		return nil, errors.New("onebot action name is empty")
	}
	if !c.Connected() {
		return nil, errors.New("onebot transport is not connected")
	}
	if params == nil {
		params = map[string]any{}
	}
	switch normalizeTransport(c.cfg.Transport) {
	case TransportHTTPSSE, TransportReverseHTTP:
		return c.sendHTTPAction(parent, action, params)
	}

	ctx, cancel := context.WithTimeout(parent, c.cfg.ActionTimeout)
	defer cancel()

	echo := fmt.Sprintf("cinlan-%d", c.echo.Add(1))
	resultCh := make(chan actionResult, 1)
	c.pendingMu.Lock()
	c.pending[echo] = resultCh
	c.pendingMu.Unlock()

	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, echo)
		c.pendingMu.Unlock()
	}()

	request := map[string]any{
		"action": action,
		"params": params,
		"echo":   echo,
	}
	if err := c.writeJSON(request); err != nil {
		return nil, fmt.Errorf("failed to send onebot action: %w", err)
	}

	select {
	case <-ctx.Done():
		return nil, errors.New("onebot action timed out")
	case result := <-resultCh:
		if result.err != nil {
			return nil, result.err
		}
		if result.response.Status != "ok" || result.response.RetCode != 0 {
			return nil, &ActionError{
				Action:  action,
				Status:  result.response.Status,
				RetCode: result.response.RetCode,
				Message: result.response.Message,
				Wording: result.response.Wording,
			}
		}
		return result.response.Data, nil
	}
}

func toSegments(chain message.Chain) []MessageSegment {
	segments := make([]MessageSegment, 0, len(chain))
	for _, component := range chain {
		data := make(map[string]any, len(component.Data))
		for key, value := range component.Data {
			data[key] = normalizeSegmentValue(key, value)
		}
		segments = append(segments, MessageSegment{
			Type: string(component.Type),
			Data: data,
		})
	}
	return segments
}

func normalizeSegmentValue(key string, value any) any {
	switch key {
	case "id", "qq", "user_id", "group_id", "message_id":
		if text := fmt.Sprint(value); text != "" {
			return idValue(text)
		}
	}
	return value
}

func (c *Client) writeJSON(value any) error {
	c.connMu.RLock()
	conn := c.conn
	c.connMu.RUnlock()
	if conn == nil {
		return errors.New("onebot websocket is not connected")
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return conn.WriteJSON(value)
}

func (c *Client) setConnection(conn *websocket.Conn) {
	c.connMu.Lock()
	c.conn = conn
	c.connMu.Unlock()
	c.connected.Store(true)
}

func (c *Client) clearConnection(conn *websocket.Conn) {
	c.connMu.Lock()
	if c.conn == conn {
		c.conn = nil
		c.connected.Store(false)
	}
	c.connMu.Unlock()
}

func (c *Client) resolvePending(echo string, result actionResult) {
	c.pendingMu.Lock()
	channel := c.pending[echo]
	c.pendingMu.Unlock()
	if channel == nil {
		return
	}
	select {
	case channel <- result:
	default:
	}
}

func (c *Client) failPending(err error) {
	c.pendingMu.Lock()
	pending := c.pending
	c.pending = make(map[string]chan actionResult)
	c.pendingMu.Unlock()

	for _, channel := range pending {
		select {
		case channel <- actionResult{err: err}:
		default:
		}
	}
}

func echoString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		return value
	}
	return strings.TrimSpace(string(raw))
}

func idValue(id string) any {
	if value, err := strconv.ParseInt(id, 10, 64); err == nil {
		return value
	}
	return id
}

func endpointSummary(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "invalid-onebot-endpoint"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func normalizeTransport(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "ws", "websocket", "forward", "forward_ws", "forward-websocket":
		return TransportForwardWebSocket
	case "reverse", "reverse_ws", "reverse-websocket":
		return TransportReverseWebSocket
	case "http", "sse", "http_sse", "http-sse":
		return TransportHTTPSSE
	case "reverse_http", "reverse-http", "http_client", "http-client":
		return TransportReverseHTTP
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func normalizeReversePath(value string) string {
	return normalizeListenerPath(value, "/onebot/v11/ws")
}

func normalizeListenerPath(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	cleaned := path.Clean(value)
	if cleaned == "." {
		return fallback
	}
	return cleaned
}

func decodeEvent(payload []byte) (Event, error) {
	var event Event
	if err := json.Unmarshal(payload, &event); err != nil {
		return Event{}, err
	}
	if strings.TrimSpace(event.PostType) == "" {
		return Event{}, errors.New("onebot event post_type is empty")
	}
	event.RawPayload = append(event.RawPayload[:0], payload...)
	return event, nil
}

func (c *Client) emitEvent(ctx context.Context, event Event) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case c.events <- event:
		return nil
	}
}

func redactConnectionError(err error, rawURL, endpoint string) string {
	if err == nil {
		return "connection closed"
	}
	message := err.Error()
	if rawURL != "" {
		message = strings.ReplaceAll(message, rawURL, endpoint)
	}
	return message
}

func sleep(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
