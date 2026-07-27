package qqnt

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/message"
	"github.com/lianyorker/cinlan-qq-bot/internal/platform"
)

func TestAdapterRuntimeRoundTrip(t *testing.T) {
	address := availableAddress(t)
	adapter, err := NewAdapter(Config{
		ListenAddr:       address,
		Token:            "runtime-test-token",
		ActionTimeout:    time.Second,
		HandshakeTimeout: time.Second,
		MaxFrameBytes:    64 * 1024,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewAdapter() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() {
		runDone <- adapter.Run(ctx)
	}()
	connection := dialEventually(t, address)
	defer connection.Close()
	scanner := bufio.NewScanner(connection)

	writeTestEnvelope(t, connection, envelope{
		Type:  "hello",
		Token: "runtime-test-token",
		Payload: mustJSON(t, helloPayload{
			Runtime:      "cinlan-qqnt",
			PID:          1234,
			QQVersion:    "9.9.31-49738",
			Capabilities: []string{"message_event", "send_message"},
		}),
	})
	ack := scanTestEnvelope(t, scanner)
	if ack.Type != "hello_ack" {
		t.Fatalf("hello response type = %q, want hello_ack", ack.Type)
	}

	writeTestEnvelope(t, connection, envelope{
		Type: "runtime_status",
		Payload: mustJSON(t, runtimeStatus{
			State:         "waiting_session",
			WrapperLoaded: true,
			LastError:     "session_attach: session unavailable",
		}),
	})
	eventually(t, time.Second, func() bool {
		return adapter.RuntimeInfo().LastError != ""
	})
	if adapter.Connected() {
		t.Fatal("adapter reported ready while runtime had an attach error")
	}
	if sendErr := adapter.Send(context.Background(), platform.Outbound{
		ChatType: platform.ChatGroup,
		ChatID:   "30003",
		Chain:    message.Chain{message.Text("test")},
	}); sendErr == nil || !strings.Contains(sendErr.Error(), "session_attach") {
		t.Fatalf("Send() before ready error = %v", sendErr)
	}

	writeTestEnvelope(t, connection, envelope{
		Type: "runtime_status",
		Payload: mustJSON(t, runtimeStatus{
			State:           "ready",
			SelfID:          "10001",
			SelfUID:         "u_self",
			WrapperLoaded:   true,
			SessionAttached: true,
		}),
	})
	eventually(t, time.Second, adapter.Connected)

	writeTestEnvelope(t, connection, envelope{
		Type: "event",
		Payload: mustJSON(t, nativeEvent{
			ID:          "10001:9001",
			Platform:    platform.PlatformQQNative,
			PostType:    "message",
			MessageType: platform.ChatGroup,
			SubType:     "normal",
			MessageID:   "9001",
			SelfID:      "10001",
			UserID:      "20002",
			ChatID:      "30003",
			SenderName:  "tester",
			Chain: message.Chain{
				message.At("10001"),
				message.Text(" ping"),
			},
			RawMessage: "@bot ping",
			Metadata:   map[string]any{"time": 123},
		}),
	})
	select {
	case event := <-adapter.Events():
		text, mentioned := event.Chain.PlainText("10001")
		if event.Platform != platform.PlatformQQNative ||
			event.ChatID != "30003" ||
			text != "ping" ||
			!mentioned {
			t.Fatalf("event = %#v, text = %q, mentioned = %v", event, text, mentioned)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for native event")
	}

	callDone := make(chan error, 1)
	go func() {
		callErr := adapter.Send(context.Background(), platform.Outbound{
			ChatType: platform.ChatPrivate,
			ChatID:   "20002",
			SelfID:   "10001",
			Chain: message.Chain{
				message.File(`D:\files\database.sql`, "database.sql"),
			},
		})
		callDone <- callErr
	}()
	action := scanTestEnvelope(t, scanner)
	if action.Type != "action" || action.ID == "" {
		t.Fatalf("action envelope = %#v", action)
	}
	var request actionPayload
	if err := decodePayload(action.Payload, &request); err != nil {
		t.Fatalf("decode action: %v", err)
	}
	if request.Name != "send_message" {
		t.Fatalf("action name = %q", request.Name)
	}
	if fmt.Sprint(request.Params["chat_type"]) != platform.ChatPrivate ||
		fmt.Sprint(request.Params["chat_id"]) != "20002" {
		t.Fatalf("send_message params = %#v", request.Params)
	}
	encodedChain, err := json.Marshal(request.Params["chain"])
	if err != nil {
		t.Fatalf("encode action chain: %v", err)
	}
	var outboundChain message.Chain
	if err := json.Unmarshal(encodedChain, &outboundChain); err != nil {
		t.Fatalf("decode action chain: %v", err)
	}
	if len(outboundChain) != 1 ||
		outboundChain[0].Type != message.TypeFile ||
		outboundChain[0].Data["name"] != "database.sql" {
		t.Fatalf("action chain = %#v", outboundChain)
	}
	writeTestEnvelope(t, connection, envelope{
		Type:    "action_result",
		ID:      action.ID,
		OK:      true,
		Payload: json.RawMessage(`{"message_id":"9002"}`),
	})
	if err := <-callDone; err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	cancel()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Adapter.Run did not stop")
	}
}

func TestAdapterRejectsInvalidToken(t *testing.T) {
	address := availableAddress(t)
	adapter, err := NewAdapter(Config{
		ListenAddr:       address,
		Token:            "correct-token",
		HandshakeTimeout: time.Second,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewAdapter() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- adapter.Run(ctx) }()

	connection := dialEventually(t, address)
	writeTestEnvelope(t, connection, envelope{
		Type:    "hello",
		Token:   "wrong-token",
		Payload: mustJSON(t, helloPayload{Runtime: "cinlan-qqnt"}),
	})
	_ = connection.SetReadDeadline(time.Now().Add(time.Second))
	buffer := make([]byte, 1)
	if _, err := connection.Read(buffer); err == nil {
		t.Fatal("unauthenticated connection remained open")
	}
	if adapter.Connected() {
		t.Fatal("adapter connected after invalid token")
	}
	connection.Close()
	cancel()
	if err := <-runDone; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func availableAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	return address
}

func dialEventually(t *testing.T, address string) net.Conn {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		connection, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err == nil {
			return connection
		}
		if time.Now().After(deadline) {
			t.Fatalf("dial %s: %v", address, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func eventually(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition was not met")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func writeTestEnvelope(t *testing.T, writer io.Writer, current envelope) {
	t.Helper()
	encoded, err := encodeEnvelope(current, 64*1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(encoded); err != nil {
		t.Fatal(err)
	}
}

func scanTestEnvelope(t *testing.T, scanner *bufio.Scanner) envelope {
	t.Helper()
	if !scanner.Scan() {
		t.Fatalf("scan envelope: %v", scanner.Err())
	}
	current, err := decodeEnvelope(scanner.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	return current
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestValidateLoopbackAddress(t *testing.T) {
	for _, address := range []string{"127.0.0.1:1", "[::1]:1", "localhost:1"} {
		if err := validateLoopbackAddress(address); err != nil {
			t.Errorf("validateLoopbackAddress(%q): %v", address, err)
		}
	}
	if err := validateLoopbackAddress("0.0.0.0:18081"); err == nil ||
		!strings.Contains(err.Error(), "loopback") {
		t.Fatalf("validateLoopbackAddress(non-loopback) error = %v", err)
	}
}
