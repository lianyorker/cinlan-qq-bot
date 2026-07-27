package mcp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

const (
	ProtocolVersion         = "2025-11-25"
	clientName              = "cinlan-qq-bot"
	clientVersion           = "0.1.0"
	maxResponseBytes        = 4 << 20
	maxToolsPerServer       = 256
	maxToolSchemaBytes      = 64 << 10
	maxToolDescriptionRunes = 4096
)

var supportedProtocolVersions = map[string]struct{}{
	"2025-11-25": {},
	"2025-06-18": {},
	"2025-03-26": {},
	"2024-11-05": {},
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	if e == nil {
		return ""
	}
	message := strings.TrimSpace(e.Message)
	if len([]rune(message)) > 512 {
		message = string([]rune(message)[:512]) + "..."
	}
	if message == "" {
		message = "unknown MCP error"
	}
	return fmt.Sprintf("MCP JSON-RPC error %d: %s", e.Code, message)
}

type rpcEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type sessionExpiredError struct {
	sessionID string
}

func (e *sessionExpiredError) Error() string {
	return "MCP session expired"
}

type requestState struct {
	sessionID string
	protocol  string
	ready     bool
}

type initializeResult struct {
	ProtocolVersion string `json:"protocolVersion"`
	ServerInfo      struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"serverInfo"`
	Instructions string `json:"instructions"`
}

type listToolsResult struct {
	Tools      []Tool `json:"tools"`
	NextCursor string `json:"nextCursor"`
}

type toolCallResult struct {
	Content           []map[string]any `json:"content"`
	StructuredContent map[string]any   `json:"structuredContent,omitempty"`
	IsError           bool             `json:"isError,omitempty"`
}

// Tool is the protocol-level description returned by tools/list.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema"`
}

// ToolResult is the untrusted result returned by tools/call.
type ToolResult struct {
	Content           []map[string]any `json:"content"`
	StructuredContent map[string]any   `json:"structured_content,omitempty"`
	IsError           bool             `json:"is_error,omitempty"`
}

// Client implements the MCP Streamable HTTP transport. It deliberately does
// not expose server-side sampling, roots, elicitation, or arbitrary stdio
// process execution.
type Client struct {
	cfg        ServerConfig
	httpClient *http.Client
	logger     Logger

	stateMu       sync.RWMutex
	state         requestState
	serverName    string
	serverVersion string
	instructions  string

	initMu sync.Mutex
	nextID atomic.Uint64
}

// Logger is the small logging surface required by the MCP package.
type Logger interface {
	Debug(msg string, args ...any)
	Warn(msg string, args ...any)
}

// NopLogger avoids coupling the protocol package to log/slog in tests and
// allows callers to provide their existing logger.
type NopLogger struct{}

func (NopLogger) Debug(string, ...any) {}
func (NopLogger) Warn(string, ...any)  {}

func NewClient(cfg ServerConfig, logger Logger) *Client {
	if logger == nil {
		logger = NopLogger{}
	}
	return &Client{
		cfg: cfg,
		httpClient: &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		logger: logger,
	}
}

func (c *Client) Config() ServerConfig {
	config := c.cfg
	config.Headers = cloneStringMap(c.cfg.Headers)
	config.HeaderEnv = cloneStringMap(c.cfg.HeaderEnv)
	config.AllowTools = cloneSet(c.cfg.AllowTools)
	return config
}

func (c *Client) Ready() bool {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.state.ready
}

func (c *Client) ProtocolVersion() string {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.state.protocol
}

func (c *Client) ServerInfo() (string, string, string) {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.serverName, c.serverVersion, c.instructions
}

func (c *Client) Initialize(parent context.Context) error {
	c.initMu.Lock()
	defer c.initMu.Unlock()
	return c.initializeLocked(parent)
}

func (c *Client) initializeIfCurrent(parent context.Context, expiredSession string) error {
	c.initMu.Lock()
	defer c.initMu.Unlock()
	c.stateMu.RLock()
	current := c.state.sessionID
	ready := c.state.ready
	c.stateMu.RUnlock()
	if ready && current != "" && current != expiredSession {
		return nil
	}
	return c.initializeLocked(parent)
}

func (c *Client) initializeLocked(parent context.Context) error {
	ctx, cancel := context.WithTimeout(parent, c.cfg.Timeout)
	defer cancel()
	c.setState(requestState{})

	id := c.nextID.Add(1)
	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities":    map[string]any{},
			"clientInfo": map[string]any{
				"name":    clientName,
				"version": clientVersion,
			},
		},
	}
	raw, headers, err := c.postRPC(ctx, payload, id, requestState{})
	if err != nil {
		return err
	}
	var result initializeResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return fmt.Errorf("decode MCP initialize result: %w", err)
	}
	if _, ok := supportedProtocolVersions[result.ProtocolVersion]; !ok {
		return fmt.Errorf("MCP server selected unsupported protocol version %q", result.ProtocolVersion)
	}
	sessionID := strings.TrimSpace(headers.Get("MCP-Session-Id"))
	if sessionID != "" && !validSessionID(sessionID) {
		return errors.New("MCP server returned an invalid session ID")
	}
	state := requestState{
		sessionID: sessionID,
		protocol:  result.ProtocolVersion,
		ready:     true,
	}
	c.setServerState(state, result.ServerInfo.Name, result.ServerInfo.Version, result.Instructions)

	notification := map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	}
	if err := c.postNotification(ctx, notification, state); err != nil {
		c.setState(requestState{})
		return fmt.Errorf("send MCP initialized notification: %w", err)
	}
	return nil
}

func (c *Client) ListTools(parent context.Context) ([]Tool, error) {
	ctx, cancel := context.WithTimeout(parent, c.cfg.Timeout)
	defer cancel()
	state, err := c.currentState()
	if err != nil {
		return nil, err
	}

	var (
		cursor string
		tools  []Tool
		seen   = make(map[string]struct{})
	)
	for page := 0; page < 100; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		id := c.nextID.Add(1)
		raw, _, err := c.postRPC(ctx, map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"method":  "tools/list",
			"params":  params,
		}, id, state)
		if err != nil {
			return nil, err
		}
		var result listToolsResult
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, fmt.Errorf("decode MCP tools/list result: %w", err)
		}
		for _, item := range result.Tools {
			normalized, err := normalizeTool(item)
			if err != nil {
				return nil, err
			}
			if _, exists := seen[normalized.Name]; exists {
				return nil, fmt.Errorf("MCP server returned duplicate tool %q", normalized.Name)
			}
			seen[normalized.Name] = struct{}{}
			tools = append(tools, normalized)
			if len(tools) > maxToolsPerServer {
				return nil, fmt.Errorf("MCP server exposes more than %d tools", maxToolsPerServer)
			}
		}
		next := strings.TrimSpace(result.NextCursor)
		if next == "" {
			break
		}
		if next == cursor {
			return nil, errors.New("MCP tools/list returned a repeated pagination cursor")
		}
		cursor = next
		if page == 99 {
			return nil, errors.New("MCP tools/list exceeded pagination limit")
		}
	}
	return tools, nil
}

func (c *Client) CallTool(parent context.Context, name string, arguments []byte) (ToolResult, error) {
	ctx, cancel := context.WithTimeout(parent, c.cfg.ToolTimeout)
	defer cancel()
	if strings.TrimSpace(name) == "" {
		return ToolResult{}, errors.New("MCP tool name is empty")
	}
	if len(arguments) == 0 {
		arguments = []byte(`{}`)
	}
	var decoded map[string]any
	if err := json.Unmarshal(arguments, &decoded); err != nil {
		return ToolResult{}, fmt.Errorf("MCP tool arguments are invalid JSON: %w", err)
	}
	if decoded == nil {
		decoded = map[string]any{}
	}
	if len(decoded) == 0 && strings.TrimSpace(string(arguments)) != "{}" && strings.TrimSpace(string(arguments)) != "null" {
		return ToolResult{}, errors.New("MCP tool arguments must be a JSON object")
	}

	state, err := c.currentState()
	if err != nil {
		return ToolResult{}, err
	}
	result, err := c.callToolOnce(ctx, state, name, decoded)
	if err == nil {
		return result, nil
	}
	var expired *sessionExpiredError
	if !errors.As(err, &expired) {
		return ToolResult{}, err
	}
	if reinitErr := c.initializeIfCurrent(ctx, expired.sessionID); reinitErr != nil {
		return ToolResult{}, fmt.Errorf("reinitialize expired MCP session: %w", reinitErr)
	}
	state, err = c.currentState()
	if err != nil {
		return ToolResult{}, err
	}
	return c.callToolOnce(ctx, state, name, decoded)
}

func (c *Client) callToolOnce(ctx context.Context, state requestState, name string, arguments map[string]any) (ToolResult, error) {
	id := c.nextID.Add(1)
	raw, _, err := c.postRPC(ctx, map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      name,
			"arguments": arguments,
		},
	}, id, state)
	if err != nil {
		return ToolResult{}, err
	}
	var result toolCallResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return ToolResult{}, fmt.Errorf("decode MCP tools/call result: %w", err)
	}
	return ToolResult{
		Content:           result.Content,
		StructuredContent: result.StructuredContent,
		IsError:           result.IsError,
	}, nil
}

func (c *Client) Close(parent context.Context) error {
	state, err := c.currentState()
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(parent, c.cfg.Timeout)
	defer cancel()
	request, err := c.newRequest(ctx, http.MethodDelete, nil, state)
	if err != nil {
		return err
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return sanitizeHTTPError(ctx, "close MCP session", err)
	}
	defer response.Body.Close()
	_, _ = readLimited(response.Body, maxResponseBytes)
	c.setState(requestState{})
	if response.StatusCode >= 200 && response.StatusCode < 300 ||
		response.StatusCode == http.StatusMethodNotAllowed ||
		response.StatusCode == http.StatusNotFound {
		return nil
	}
	return fmt.Errorf("MCP session close returned HTTP %d", response.StatusCode)
}

func (c *Client) currentState() (requestState, error) {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	if !c.state.ready {
		return requestState{}, errors.New("MCP client is not initialized")
	}
	return c.state, nil
}

func (c *Client) setState(state requestState) {
	c.stateMu.Lock()
	c.state = state
	if !state.ready {
		c.serverName = ""
		c.serverVersion = ""
		c.instructions = ""
	}
	c.stateMu.Unlock()
}

func (c *Client) setServerState(state requestState, name, version, instructions string) {
	c.stateMu.Lock()
	c.state = state
	c.serverName = strings.TrimSpace(name)
	c.serverVersion = strings.TrimSpace(version)
	c.instructions = truncateRunes(strings.TrimSpace(instructions), maxToolDescriptionRunes)
	c.stateMu.Unlock()
}

func (c *Client) callID(id uint64) string {
	return fmt.Sprintf("%d", id)
}

func (c *Client) postRPC(
	ctx context.Context,
	payload any,
	id uint64,
	state requestState,
) ([]byte, http.Header, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, fmt.Errorf("encode MCP request: %w", err)
	}
	request, err := c.newRequest(ctx, http.MethodPost, bytes.NewReader(body), state)
	if err != nil {
		return nil, nil, err
	}
	request.Header.Set("Accept", "application/json, text/event-stream")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, nil, sanitizeHTTPError(ctx, "MCP HTTP request failed", err)
	}
	defer response.Body.Close()
	rawBody, readErr := readLimited(response.Body, maxResponseBytes)
	if readErr != nil {
		return nil, nil, fmt.Errorf("read MCP response: %w", readErr)
	}
	if response.StatusCode == http.StatusNotFound && state.sessionID != "" {
		return nil, response.Header, &sessionExpiredError{sessionID: state.sessionID}
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, response.Header, fmt.Errorf("MCP server returned HTTP %d", response.StatusCode)
	}
	if len(bytes.TrimSpace(rawBody)) == 0 {
		return nil, response.Header, errors.New("MCP server returned an empty response")
	}
	contentType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if contentType == "text/event-stream" {
		rawBody, err = parseSSEResponse(rawBody, c.callID(id))
		if err != nil {
			return nil, response.Header, err
		}
	}
	if err := validateRPCResponse(rawBody, c.callID(id)); err != nil {
		return nil, response.Header, err
	}
	var envelope rpcEnvelope
	if err := json.Unmarshal(rawBody, &envelope); err != nil {
		return nil, response.Header, fmt.Errorf("decode MCP response: %w", err)
	}
	if envelope.Error != nil {
		return nil, response.Header, envelope.Error
	}
	if len(envelope.Result) == 0 {
		return nil, response.Header, errors.New("MCP response has no result")
	}
	return envelope.Result, response.Header, nil
}

func (c *Client) postNotification(ctx context.Context, payload any, state requestState) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode MCP notification: %w", err)
	}
	request, err := c.newRequest(ctx, http.MethodPost, bytes.NewReader(body), state)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json, text/event-stream")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return sanitizeHTTPError(ctx, "send MCP notification", err)
	}
	defer response.Body.Close()
	_, _ = readLimited(response.Body, maxResponseBytes)
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("MCP notification returned HTTP %d", response.StatusCode)
	}
	return nil
}

func (c *Client) newRequest(ctx context.Context, method string, body io.Reader, state requestState) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, method, c.cfg.URL, body)
	if err != nil {
		return nil, fmt.Errorf("build MCP request: %w", err)
	}
	for name, value := range c.cfg.Headers {
		request.Header.Set(name, value)
	}
	request.Header.Set("User-Agent", clientName+"/"+clientVersion)
	if state.protocol != "" {
		request.Header.Set("MCP-Protocol-Version", state.protocol)
	}
	if state.sessionID != "" {
		request.Header.Set("MCP-Session-Id", state.sessionID)
	}
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
	}
	return request, nil
}

func sanitizeHTTPError(ctx context.Context, operation string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		err = urlErr.Err
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func normalizeTool(item Tool) (Tool, error) {
	item.Name = strings.TrimSpace(item.Name)
	if item.Name == "" {
		return Tool{}, errors.New("MCP tools/list returned a tool without a name")
	}
	if len([]rune(item.Name)) > 128 {
		return Tool{}, fmt.Errorf("MCP tool %q name exceeds 128 runes", item.Name)
	}
	if item.InputSchema == nil {
		item.InputSchema = map[string]any{"type": "object", "additionalProperties": false}
	}
	if schemaType, ok := item.InputSchema["type"]; !ok {
		item.InputSchema["type"] = "object"
	} else if schemaType != "object" {
		return Tool{}, fmt.Errorf("MCP tool %q inputSchema must have type object", item.Name)
	}
	if _, ok := item.InputSchema["properties"]; !ok {
		item.InputSchema["properties"] = map[string]any{}
	}
	if required, ok := item.InputSchema["required"].(bool); ok {
		properties, _ := item.InputSchema["properties"].(map[string]any)
		if required {
			names := make([]string, 0, len(properties))
			for name := range properties {
				names = append(names, name)
			}
			sort.Strings(names)
			item.InputSchema["required"] = names
		} else {
			delete(item.InputSchema, "required")
		}
	}
	if encoded, err := json.Marshal(item.InputSchema); err != nil || len(encoded) > maxToolSchemaBytes {
		return Tool{}, fmt.Errorf("MCP tool %q inputSchema is too large", item.Name)
	}
	item.Description = truncateRunes(strings.TrimSpace(item.Description), maxToolDescriptionRunes)
	return item, nil
}

func parseSSEResponse(data []byte, expectedID string) ([]byte, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4<<10), maxResponseBytes)
	var dataLines []string
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if raw, ok := sseMessage(dataLines, expectedID); ok {
				return raw, nil
			}
			dataLines = nil
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, found := strings.Cut(line, ":")
		if !found {
			value = ""
		} else {
			value = strings.TrimPrefix(value, " ")
		}
		if field == "data" {
			dataLines = append(dataLines, value)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read MCP SSE response: %w", err)
	}
	if raw, ok := sseMessage(dataLines, expectedID); ok {
		return raw, nil
	}
	return nil, errors.New("MCP SSE response did not contain the requested response")
}

func sseMessage(lines []string, expectedID string) ([]byte, bool) {
	if len(lines) == 0 {
		return nil, false
	}
	raw := []byte(strings.Join(lines, "\n"))
	var envelope rpcEnvelope
	if json.Unmarshal(raw, &envelope) != nil {
		return nil, false
	}
	if strings.TrimSpace(string(envelope.ID)) != expectedID {
		return nil, false
	}
	return raw, true
}

func validateRPCResponse(raw []byte, expectedID string) error {
	var envelope rpcEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("decode MCP JSON-RPC response: %w", err)
	}
	if envelope.JSONRPC != "2.0" {
		return errors.New("MCP response is not JSON-RPC 2.0")
	}
	if strings.TrimSpace(string(envelope.ID)) != expectedID {
		return errors.New("MCP response ID does not match request")
	}
	return nil
}

func validSessionID(value string) bool {
	for _, current := range value {
		if current < 0x21 || current > 0x7e {
			return false
		}
	}
	return value != ""
}

func readLimited(reader io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("response exceeds %d bytes", limit)
	}
	return data, nil
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "..."
}

func cloneSet(source map[string]struct{}) map[string]struct{} {
	if len(source) == 0 {
		return nil
	}
	result := make(map[string]struct{}, len(source))
	for key := range source {
		result[key] = struct{}{}
	}
	return result
}

func hashSuffix(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:8]
}
