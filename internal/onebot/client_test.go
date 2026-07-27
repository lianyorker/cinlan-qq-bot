package onebot

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestClientReceivesEventAndSendsAction(t *testing.T) {
	requests := make(chan map[string]any, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("Authorization"); got != "Bearer token" {
			t.Errorf("Authorization = %q", got)
		}
		conn, err := upgrader.Upgrade(writer, request, nil)
		if err != nil {
			t.Errorf("Upgrade() error = %v", err)
			return
		}
		defer conn.Close()

		_ = conn.WriteJSON(map[string]any{
			"time":         100,
			"self_id":      10001,
			"post_type":    "message",
			"message_type": "group",
			"message_id":   40004,
			"user_id":      20002,
			"group_id":     30003,
			"message": []map[string]any{
				{"type": "text", "data": map[string]any{"text": "hello"}},
			},
		})

		_, payload, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var action map[string]any
		if err := json.Unmarshal(payload, &action); err != nil {
			t.Errorf("decode action: %v", err)
			return
		}
		requests <- action
		_ = conn.WriteJSON(map[string]any{
			"status":  "ok",
			"retcode": 0,
			"data":    map[string]any{"message_id": 50005},
			"echo":    action["echo"],
		})
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	client := NewClient(ClientConfig{
		URL:           "ws" + strings.TrimPrefix(server.URL, "http"),
		AccessToken:   "token",
		ActionTimeout: time.Second,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	done := make(chan error, 1)
	go func() {
		done <- client.Run(ctx)
	}()

	waitForConnected(t, client)
	select {
	case event := <-client.Events():
		if event.GroupID.String() != "30003" || event.MessageID.String() != "40004" {
			t.Fatalf("event = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}

	if err := client.SendGroupText(context.Background(), "30003", "40004", "answer", true); err != nil {
		t.Fatalf("SendGroupText() error = %v", err)
	}
	action := <-requests
	if action["action"] != "send_group_msg" {
		t.Fatalf("action = %#v", action)
	}
	params := action["params"].(map[string]any)
	message := params["message"].([]any)
	if len(message) != 2 {
		t.Fatalf("message segments = %#v", message)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("client did not stop")
	}
}

func waitForConnected(t *testing.T, client *Client) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if client.Connected() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("client did not connect")
}

func TestReverseWebSocketReceivesConnectionAndCorrelatesAction(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	address := listener.Addr().String()
	_ = listener.Close()

	ctx, cancel := context.WithCancel(context.Background())
	client := NewClient(ClientConfig{
		Transport:     TransportReverseWebSocket,
		ListenAddr:    address,
		Path:          "/onebot",
		AccessToken:   "secret",
		ActionTimeout: time.Second,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	done := make(chan error, 1)
	go func() {
		done <- client.Run(ctx)
	}()

	endpoint := "ws://" + address + "/onebot"
	headers := http.Header{"Authorization": []string{"Bearer secret"}}
	var conn *websocket.Conn
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		conn, _, err = websocket.DefaultDialer.Dial(endpoint, headers)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		cancel()
		t.Fatalf("Dial() error = %v", err)
	}
	defer conn.Close()
	waitForConnected(t, client)

	if err := conn.WriteJSON(map[string]any{
		"time":         100,
		"self_id":      10001,
		"post_type":    "message",
		"message_type": "group",
		"message_id":   40004,
		"user_id":      20002,
		"group_id":     30003,
		"message": []map[string]any{
			{"type": "text", "data": map[string]any{"text": "hello"}},
		},
	}); err != nil {
		t.Fatalf("WriteJSON(event) error = %v", err)
	}
	select {
	case event := <-client.Events():
		if event.SelfID.String() != "10001" || event.GroupID.String() != "30003" {
			t.Fatalf("event = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for reverse event")
	}

	callDone := make(chan error, 1)
	go func() {
		_, callErr := client.GetStatus(context.Background())
		callDone <- callErr
	}()
	_, payload, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage(action) error = %v", err)
	}
	var action map[string]any
	if err := json.Unmarshal(payload, &action); err != nil {
		t.Fatalf("decode action: %v", err)
	}
	if action["action"] != "get_status" {
		t.Fatalf("action = %#v", action)
	}
	if err := conn.WriteJSON(map[string]any{
		"status":  "ok",
		"retcode": 0,
		"data":    map[string]any{"online": true},
		"echo":    action["echo"],
	}); err != nil {
		t.Fatalf("WriteJSON(response) error = %v", err)
	}
	if err := <-callDone; err != nil {
		t.Fatalf("GetStatus() error = %v", err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reverse client did not stop")
	}
}

func TestHTTPSSEReceivesEventAndCallsHTTPAction(t *testing.T) {
	actionParams := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("Authorization = %q", got)
		}
		switch request.URL.Path {
		case "/_events":
			writer.Header().Set("Content-Type", "text/event-stream")
			writer.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(writer, "data: {\"time\":100,\"self_id\":10001,\"post_type\":\"message\",\"message_type\":\"group\",\"message_id\":40004,\"user_id\":20002,\"group_id\":30003,\"message\":[]}\n\n")
			writer.(http.Flusher).Flush()
			<-request.Context().Done()
		case "/get_status":
			var params map[string]any
			if err := json.NewDecoder(request.Body).Decode(&params); err != nil {
				t.Errorf("decode action params: %v", err)
				return
			}
			actionParams <- params
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"status":  "ok",
				"retcode": 0,
				"data":    map[string]any{"online": true},
			})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	client := NewClient(ClientConfig{
		URL:           server.URL,
		AccessToken:   "secret",
		ActionTimeout: time.Second,
		Transport:     TransportHTTPSSE,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	done := make(chan error, 1)
	go func() {
		done <- client.Run(ctx)
	}()

	waitForConnected(t, client)
	select {
	case event := <-client.Events():
		if event.SelfID.String() != "10001" || event.GroupID.String() != "30003" {
			t.Fatalf("event = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for SSE event")
	}
	status, err := client.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus() error = %v", err)
	}
	if status["online"] != true {
		t.Fatalf("status = %#v", status)
	}
	select {
	case params := <-actionParams:
		if len(params) != 0 {
			t.Fatalf("action params = %#v", params)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for HTTP action")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("HTTP SSE client did not stop")
	}
}

func TestReverseHTTPVerifiesSignatureReturnsQuickOperationAndCallsAction(t *testing.T) {
	actionServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/get_status" {
			http.NotFound(writer, request)
			return
		}
		if got := request.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("Authorization = %q", got)
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"status":  "ok",
			"retcode": 0,
			"data":    map[string]any{"online": true},
		})
	}))
	defer actionServer.Close()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	client := NewClient(ClientConfig{
		URL:           actionServer.URL,
		AccessToken:   "secret",
		ActionTimeout: time.Second,
		Transport:     TransportReverseHTTP,
		ListenAddr:    address,
		Path:          "/events",
		QuickOperationHandler: func(_ context.Context, event Event) (map[string]any, error) {
			return map[string]any{"reply": "received " + event.MessageID.String()}, nil
		},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	done := make(chan error, 1)
	go func() {
		done <- client.Run(ctx)
	}()
	waitForConnected(t, client)

	payload := []byte(`{"time":100,"self_id":10001,"post_type":"message","message_type":"group","message_id":40004,"user_id":20002,"group_id":30003,"message":[]}`)
	invalidRequest, err := http.NewRequest(http.MethodPost, "http://"+address+"/events", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	invalidRequest.Header.Set("x-self-id", "10001")
	invalidRequest.Header.Set("x-signature", "sha1=invalid")
	invalidResponse, err := http.DefaultClient.Do(invalidRequest)
	if err != nil {
		t.Fatalf("invalid signature request error = %v", err)
	}
	_ = invalidResponse.Body.Close()
	if invalidResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("invalid signature status = %d", invalidResponse.StatusCode)
	}

	request, err := http.NewRequest(http.MethodPost, "http://"+address+"/events", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("x-self-id", "10001")
	request.Header.Set("x-signature", signReverseHTTPPayload(payload, "secret"))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("reverse HTTP request error = %v", err)
	}
	defer response.Body.Close()
	var operation map[string]any
	if err := json.NewDecoder(response.Body).Decode(&operation); err != nil {
		t.Fatalf("decode quick operation: %v", err)
	}
	if response.StatusCode != http.StatusOK || operation["reply"] != "received 40004" {
		t.Fatalf("quick operation = %d %#v", response.StatusCode, operation)
	}
	select {
	case event := <-client.Events():
		if event.SelfID.String() != "10001" || event.MessageID.String() != "40004" {
			t.Fatalf("event = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for reverse HTTP event")
	}
	if _, err := client.GetStatus(context.Background()); err != nil {
		t.Fatalf("GetStatus() error = %v", err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reverse HTTP client did not stop")
	}
}

func signReverseHTTPPayload(payload []byte, token string) string {
	mac := hmac.New(sha1.New, []byte(token))
	_, _ = mac.Write(payload)
	return "sha1=" + hex.EncodeToString(mac.Sum(nil))
}
