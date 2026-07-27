package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type testMCPServer struct {
	mu             sync.Mutex
	initCount      int
	listCount      int
	callCount      int
	expireNextCall bool
	lastSession    string
}

func (s *testMCPServer) handler(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var envelope struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params map[string]any  `json:"params"`
	}
	if err := json.NewDecoder(request.Body).Decode(&envelope); err != nil {
		http.Error(writer, "invalid JSON", http.StatusBadRequest)
		return
	}
	if !strings.Contains(request.Header.Get("Accept"), "application/json") ||
		!strings.Contains(request.Header.Get("Accept"), "text/event-stream") {
		http.Error(writer, "missing Accept", http.StatusBadRequest)
		return
	}
	switch envelope.Method {
	case "initialize":
		s.mu.Lock()
		s.initCount++
		s.lastSession = fmt.Sprintf("session-%d", s.initCount)
		session := s.lastSession
		s.mu.Unlock()
		if request.Header.Get("MCP-Protocol-Version") != "" {
			http.Error(writer, "protocol header on initialize", http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("MCP-Session-Id", session)
		writeTestJSONRPC(writer, envelope.ID, map[string]any{
			"protocolVersion": ProtocolVersion,
			"serverInfo": map[string]any{
				"name":    "test-server",
				"version": "1.0",
			},
		})
	case "notifications/initialized":
		if request.Header.Get("MCP-Session-Id") == "" || request.Header.Get("MCP-Protocol-Version") == "" {
			http.Error(writer, "missing session headers", http.StatusBadRequest)
			return
		}
		writer.WriteHeader(http.StatusAccepted)
	case "tools/list":
		if request.Header.Get("MCP-Session-Id") == "" || request.Header.Get("MCP-Protocol-Version") == "" {
			http.Error(writer, "missing session headers", http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.listCount++
		listCount := s.listCount
		s.mu.Unlock()
		result := map[string]any{
			"tools": []any{
				map[string]any{
					"name":        "search.customer",
					"description": "Search customers",
					"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}},
				},
			},
		}
		if listCount == 1 {
			result["nextCursor"] = "page-2"
		} else {
			result["tools"] = []any{
				map[string]any{
					"name":        "get_status",
					"description": "Get status",
					"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
				},
			}
		}
		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		writeTestJSONRPC(writer, envelope.ID, result)
	case "tools/call":
		s.mu.Lock()
		s.callCount++
		expire := s.expireNextCall
		s.expireNextCall = false
		s.mu.Unlock()
		if expire {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		result := map[string]any{
			"content": []any{
				map[string]any{"type": "text", "text": "customer found"},
			},
			"structuredContent": map[string]any{"count": 1},
		}
		writeTestSSE(writer, envelope.ID, result)
	default:
		writer.WriteHeader(http.StatusNotFound)
	}
}

func writeTestJSONRPC(writer http.ResponseWriter, id json.RawMessage, result any) {
	_ = json.NewEncoder(writer).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(id),
		"result":  result,
	})
}

func writeTestSSE(writer http.ResponseWriter, id json.RawMessage, result any) {
	payload, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(id),
		"result":  result,
	})
	_, _ = fmt.Fprintf(writer, "event: message\nid: 1\ndata: %s\n\n", payload)
}

func newTestClient(t *testing.T, serverURL string) *Client {
	t.Helper()
	return NewClient(ServerConfig{
		Name:        "test",
		URL:         serverURL,
		Active:      true,
		Headers:     map[string]string{"X-Test": "ok"},
		Timeout:     time.Second,
		ToolTimeout: time.Second,
	}, nil)
}

func TestClientInitializesListsPaginatedToolsAndCallsSSE(t *testing.T) {
	server := &testMCPServer{}
	httpServer := httptest.NewServer(http.HandlerFunc(server.handler))
	defer httpServer.Close()

	client := newTestClient(t, httpServer.URL)
	if err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	tools, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if len(tools) != 2 || tools[0].Name != "search.customer" || tools[1].Name != "get_status" {
		t.Fatalf("tools = %#v", tools)
	}
	result, err := client.CallTool(context.Background(), "search.customer", []byte(`{"query":"alice"}`))
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	if result.IsError || len(result.Content) != 1 || result.Content[0]["text"] != "customer found" {
		t.Fatalf("result = %#v", result)
	}
	if result.StructuredContent["count"] != float64(1) {
		t.Fatalf("structured result = %#v", result.StructuredContent)
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.initCount != 1 || server.listCount != 2 || server.callCount != 1 {
		t.Fatalf("server counters = init:%d list:%d call:%d", server.initCount, server.listCount, server.callCount)
	}
}

func TestClientReinitializesExpiredSessionAndRetries(t *testing.T) {
	server := &testMCPServer{expireNextCall: true}
	httpServer := httptest.NewServer(http.HandlerFunc(server.handler))
	defer httpServer.Close()
	client := newTestClient(t, httpServer.URL)
	if err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	result, err := client.CallTool(context.Background(), "get_status", []byte(`{}`))
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	if len(result.Content) != 1 {
		t.Fatalf("result = %#v", result)
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.initCount != 2 || server.callCount != 2 {
		t.Fatalf("server counters = init:%d call:%d", server.initCount, server.callCount)
	}
}

func TestClientRejectsUnsupportedServerProtocol(t *testing.T) {
	httpServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var envelope struct {
			ID json.RawMessage `json:"id"`
		}
		_ = json.NewDecoder(request.Body).Decode(&envelope)
		writer.Header().Set("Content-Type", "application/json")
		writeTestJSONRPC(writer, envelope.ID, map[string]any{
			"protocolVersion": "2099-01-01",
			"serverInfo":      map[string]any{"name": "bad", "version": "1"},
		})
	}))
	defer httpServer.Close()
	client := newTestClient(t, httpServer.URL)
	if err := client.Initialize(context.Background()); err == nil || !strings.Contains(err.Error(), "unsupported protocol") {
		t.Fatalf("Initialize() error = %v, want unsupported protocol", err)
	}
}

func TestClientNetworkErrorDoesNotExposeEndpointQuery(t *testing.T) {
	client := NewClient(ServerConfig{
		Name:        "test",
		URL:         "http://127.0.0.1:1/mcp?access_token=super-secret",
		Active:      true,
		Timeout:     100 * time.Millisecond,
		ToolTimeout: time.Second,
	}, nil)
	err := client.Initialize(context.Background())
	if err == nil {
		t.Fatal("Initialize() unexpectedly succeeded")
	}
	if strings.Contains(err.Error(), "super-secret") || strings.Contains(err.Error(), "access_token") {
		t.Fatalf("Initialize() leaked endpoint query: %v", err)
	}
}
