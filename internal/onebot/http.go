package onebot

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxHTTPResponseBytes = 4 << 20

type retryAfterError struct {
	statusCode int
	delay      time.Duration
}

func (e *retryAfterError) Error() string {
	return fmt.Sprintf("onebot HTTP returned status %d", e.statusCode)
}

func (c *Client) runHTTPSSE(ctx context.Context) error {
	backoff := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		err := c.consumeSSE(ctx)
		if ctx.Err() != nil {
			break
		}
		if time.Since(started) >= 30*time.Second {
			backoff = time.Second
		}

		retryDelay := backoff
		var retryErr *retryAfterError
		if errors.As(err, &retryErr) && retryErr.delay > 0 {
			retryDelay = retryErr.delay
		}
		c.logger.Warn(
			"onebot HTTP SSE disconnected",
			"endpoint", c.endpoint,
			"retry_in_ms", retryDelay.Milliseconds(),
			"error", redactConnectionError(err, c.cfg.URL, c.endpoint),
		)
		if err := sleep(ctx, retryDelay); err != nil {
			break
		}
		if retryDelay == backoff && backoff < 30*time.Second {
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
	}
	return nil
}

func (c *Client) consumeSSE(ctx context.Context) error {
	eventsURL, err := appendURLPath(c.cfg.URL, "_events")
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, eventsURL, nil)
	if err != nil {
		return errors.New("build OneBot SSE request")
	}
	request.Header.Set("Accept", "text/event-stream")
	c.setHTTPAuthorization(request)

	response, err := c.http.Do(request)
	if err != nil {
		return sanitizeHTTPNetworkError(err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusTooManyRequests {
		return &retryAfterError{
			statusCode: response.StatusCode,
			delay:      parseRetryAfter(response.Header.Get("Retry-After"), time.Now()),
		}
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("onebot SSE returned HTTP %d", response.StatusCode)
	}
	contentType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if contentType != "text/event-stream" {
		return fmt.Errorf("onebot SSE returned content type %q", contentType)
	}

	c.connected.Store(true)
	c.logger.Info("onebot HTTP SSE connected", "endpoint", c.endpoint)
	defer c.connected.Store(false)

	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 64<<10), readLimit)
	var data strings.Builder
	dispatch := func() error {
		if data.Len() == 0 {
			return nil
		}
		payload := strings.TrimSuffix(data.String(), "\n")
		data.Reset()
		event, decodeErr := decodeEvent([]byte(payload))
		if decodeErr != nil {
			c.logger.Warn("ignored invalid onebot SSE event", "reason", "contract_mismatch")
			return nil
		}
		return c.emitEvent(ctx, event)
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "data:") {
			value := strings.TrimPrefix(line, "data:")
			value = strings.TrimPrefix(value, " ")
			if data.Len()+len(value)+1 > readLimit {
				return fmt.Errorf("onebot SSE event exceeds %d bytes", readLimit)
			}
			data.WriteString(value)
			data.WriteByte('\n')
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read OneBot SSE stream: %w", err)
	}
	if err := dispatch(); err != nil {
		return err
	}
	return io.EOF
}

func (c *Client) sendHTTPAction(parent context.Context, action string, params map[string]any) (json.RawMessage, error) {
	actionURL, err := appendURLPath(c.cfg.URL, action)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(params)
	if err != nil {
		return nil, errors.New("encode OneBot HTTP action parameters")
	}
	ctx, cancel := context.WithTimeout(parent, c.cfg.ActionTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, actionURL, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("build OneBot HTTP action request")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	c.setHTTPAuthorization(request)

	response, err := c.http.Do(request)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, errors.New("onebot action timed out")
		}
		return nil, fmt.Errorf("call OneBot HTTP action: %w", sanitizeHTTPNetworkError(err))
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxHTTPResponseBytes+1))
	if err != nil {
		return nil, errors.New("read OneBot HTTP action response")
	}
	if len(payload) > maxHTTPResponseBytes {
		return nil, fmt.Errorf("OneBot HTTP action response exceeds %d bytes", maxHTTPResponseBytes)
	}
	if response.StatusCode == http.StatusTooManyRequests {
		return nil, &retryAfterError{
			statusCode: response.StatusCode,
			delay:      parseRetryAfter(response.Header.Get("Retry-After"), time.Now()),
		}
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("onebot HTTP action returned HTTP %d", response.StatusCode)
	}
	var result apiResponse
	if err := json.Unmarshal(payload, &result); err != nil {
		return nil, errors.New("decode OneBot HTTP action response")
	}
	if result.Status != "ok" || result.RetCode != 0 {
		return nil, &ActionError{
			Action:  action,
			Status:  result.Status,
			RetCode: result.RetCode,
			Message: result.Message,
			Wording: result.Wording,
		}
	}
	return result.Data, nil
}

func (c *Client) runReverseHTTP(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc(c.cfg.Path, c.receiveReverseHTTPEvent)

	listener, err := net.Listen("tcp", c.cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen for OneBot reverse HTTP: %w", err)
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
	c.connected.Store(true)
	c.logger.Info("onebot reverse HTTP listening", "endpoint", c.endpoint)
	defer c.connected.Store(false)
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = server.Shutdown(shutdownCtx)
		cancel()
	}()

	select {
	case <-ctx.Done():
		return nil
	case err := <-serverErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (c *Client) receiveReverseHTTPEvent(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writeHTTPJSON(writer, http.StatusMethodNotAllowed, map[string]any{"error": "method_not_allowed"})
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, readLimit)
	payload, err := io.ReadAll(request.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeHTTPJSON(writer, http.StatusRequestEntityTooLarge, map[string]any{"error": "body_too_large"})
			return
		}
		writeHTTPJSON(writer, http.StatusBadRequest, map[string]any{"error": "invalid_body"})
		return
	}
	if !verifyReverseHTTPSignature(payload, request.Header.Get("x-signature"), c.cfg.AccessToken) {
		writeHTTPJSON(writer, http.StatusUnauthorized, map[string]any{"error": "invalid_signature"})
		return
	}
	event, err := decodeEvent(payload)
	if err != nil {
		writeHTTPJSON(writer, http.StatusBadRequest, map[string]any{"error": "invalid_event"})
		return
	}
	headerSelfID := strings.TrimSpace(request.Header.Get("x-self-id"))
	if event.SelfID == "" && headerSelfID != "" {
		event.SelfID = StringID(headerSelfID)
	} else if headerSelfID != "" && event.SelfID.String() != headerSelfID {
		writeHTTPJSON(writer, http.StatusBadRequest, map[string]any{"error": "self_id_mismatch"})
		return
	}
	if err := c.emitEvent(request.Context(), event); err != nil {
		writeHTTPJSON(writer, http.StatusServiceUnavailable, map[string]any{"error": "event_receiver_stopped"})
		return
	}

	operation := map[string]any{}
	if handler := c.cfg.QuickOperationHandler; handler != nil {
		operation, err = handler(request.Context(), event)
		if err != nil {
			c.logger.Warn("onebot quick operation failed", slog.String("error", err.Error()))
			writeHTTPJSON(writer, http.StatusInternalServerError, map[string]any{"error": "quick_operation_failed"})
			return
		}
		if operation == nil {
			operation = map[string]any{}
		}
	}
	writeHTTPJSON(writer, http.StatusOK, operation)
}

func (c *Client) setHTTPAuthorization(request *http.Request) {
	if c.cfg.AccessToken != "" {
		request.Header.Set("Authorization", "Bearer "+c.cfg.AccessToken)
	}
}

func appendURLPath(rawURL, element string) (string, error) {
	endpoint, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return "", errors.New("OneBot HTTP URL must be an absolute HTTP(S) URL")
	}
	if strings.TrimSpace(element) == "" {
		return "", errors.New("OneBot HTTP path element is empty")
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/" + url.PathEscape(element)
	endpoint.RawPath = ""
	endpoint.Fragment = ""
	return endpoint.String(), nil
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		return capRetryDelay(time.Duration(seconds) * time.Second)
	}
	if target, err := http.ParseTime(value); err == nil {
		return capRetryDelay(target.Sub(now))
	}
	return 0
}

func capRetryDelay(delay time.Duration) time.Duration {
	if delay <= 0 {
		return 0
	}
	if delay > 5*time.Minute {
		return 5 * time.Minute
	}
	return delay
}

func verifyReverseHTTPSignature(payload []byte, provided, token string) bool {
	if token == "" {
		return true
	}
	mac := hmac.New(sha1.New, []byte(token))
	_, _ = mac.Write(payload)
	expected := "sha1=" + hex.EncodeToString(mac.Sum(nil))
	return subtle.ConstantTimeCompare([]byte(strings.TrimSpace(provided)), []byte(expected)) == 1
}

func writeHTTPJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func sanitizeHTTPNetworkError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		return urlErr.Err
	}
	return err
}
