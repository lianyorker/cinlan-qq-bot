package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/domain"
	"github.com/lianyorker/cinlan-qq-bot/internal/message"
	"github.com/lianyorker/cinlan-qq-bot/internal/tool"
)

func TestCustomAgentContract(t *testing.T) {
	var received customRequest
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q", got)
		}
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			t.Errorf("decode request: %v", err)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"reply":"已处理","handoff":true}`)
	}))
	defer server.Close()

	client := NewHTTPClient(testAgentConfig("custom", server.URL), discardLogger())
	response, err := client.Reply(context.Background(), testAgentRequest())
	if err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if response.Reply != "已处理" || !response.Handoff {
		t.Fatalf("response = %#v", response)
	}
	if received.Version != "1" ||
		received.Channel != "qq" ||
		received.Message.Platform != "qq-onebot" ||
		received.Message.ChatType != "group" ||
		received.Message.ChatID != "30003" ||
		received.Message.GroupID != "30003" ||
		len(received.History) != 1 ||
		len(received.Message.Components) != 1 {
		t.Fatalf("received contract = %#v", received)
	}
}

func TestCustomAgentPrivateChatContract(t *testing.T) {
	request := testAgentRequest()
	request.SessionID = "qq:self:10001:private:20002"
	request.ChatType = "private"
	request.ChatID = "20002"
	request.GroupID = ""

	client := NewHTTPClient(testAgentConfig("custom", "http://127.0.0.1"), discardLogger())
	payload := client.buildCustomRequest(request, nil)
	if payload.Message.Platform != "qq-onebot" ||
		payload.Message.ChatType != "private" ||
		payload.Message.ChatID != "20002" ||
		payload.Message.GroupID != "" {
		t.Fatalf("private custom request = %#v", payload)
	}
}

func TestOpenAICompatibleContract(t *testing.T) {
	var received openAIRequest
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_, _ = io.WriteString(writer, `{"choices":[{"message":{"role":"assistant","content":"答案"}}]}`)
	}))
	defer server.Close()

	cfg := testAgentConfig("openai", server.URL)
	cfg.Model = "test-model"
	client := NewHTTPClient(cfg, discardLogger())
	response, err := client.Reply(context.Background(), testAgentRequest())
	if err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if response.Reply != "答案" {
		t.Fatalf("Reply = %q", response.Reply)
	}
	if received.Model != "test-model" || len(received.Messages) != 3 {
		t.Fatalf("request = %#v", received)
	}
	if received.Messages[0].Role != "system" || received.Messages[2].Content != "新问题" {
		t.Fatalf("messages = %#v", received.Messages)
	}
}

func TestPromptContextIsSentToBothAgentModes(t *testing.T) {
	var received customRequest
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_, _ = io.WriteString(writer, `{"reply":"ok"}`)
	}))
	defer server.Close()

	request := testAgentRequest()
	request.PromptContext = "[资料 1] 退款规则"
	client := NewHTTPClient(testAgentConfig("custom", server.URL), discardLogger())
	if _, err := client.Reply(context.Background(), request); err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if received.Context != request.PromptContext {
		t.Fatalf("context = %q, want %q", received.Context, request.PromptContext)
	}
}

func TestRequestToolScopeDoesNotExposeOtherChatTools(t *testing.T) {
	client := NewHTTPClient(testAgentConfig("custom", "http://127.0.0.1"), discardLogger())
	registry := tool.NewRegistry()
	for _, name := range []string{"group_a_tool", "group_b_tool"} {
		if err := registry.Register(tool.Definition{
			Name: name,
			Handler: func(context.Context, tool.Call) (tool.Result, error) {
				return tool.Result{}, nil
			},
		}); err != nil {
			t.Fatalf("Register(%q) error = %v", name, err)
		}
	}
	client.SetTools(registry, 2)
	request := testAgentRequest()
	request.RestrictTools = true
	request.AllowedTools = []string{"group_a_tool"}
	payload := client.buildCustomRequest(request, nil)
	if len(payload.Tools) != 1 {
		t.Fatalf("scoped tools = %#v", payload.Tools)
	}
	function, _ := payload.Tools[0]["function"].(map[string]any)
	if function["name"] != "group_a_tool" {
		t.Fatalf("scoped tool = %#v", function)
	}
}

func TestOpenAIToolLoopExecutesOnlyRegisteredTool(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		call := calls.Add(1)
		if call == 1 {
			_, _ = io.WriteString(writer, `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"refund\"}"}}]}}]}`)
			return
		}
		var payload openAIRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode second request: %v", err)
		}
		if len(payload.Messages) < 3 || payload.Messages[len(payload.Messages)-1].Role != "tool" {
			t.Errorf("tool result missing from messages: %#v", payload.Messages)
		}
		_, _ = io.WriteString(writer, `{"choices":[{"message":{"role":"assistant","content":"最终答案"}}]}`)
	}))
	defer server.Close()

	registry := tool.NewRegistry()
	if err := registry.Register(tool.Definition{
		Name: "lookup",
		Handler: func(_ context.Context, call tool.Call) (tool.Result, error) {
			if call.Actor.Platform != "qq-onebot" ||
				call.Actor.ChatType != "group" ||
				call.Actor.ChatID != "30003" {
				t.Errorf("OpenAI tool actor = %#v", call.Actor)
			}
			return tool.Result{Content: map[string]string{"answer": string(call.Arguments)}}, nil
		},
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	client := NewHTTPClient(testAgentConfig("openai", server.URL), discardLogger())
	client.SetTools(registry, 2)
	response, err := client.Reply(context.Background(), testAgentRequest())
	if err != nil || response.Reply != "最终答案" || calls.Load() != 2 {
		t.Fatalf("Reply() = %#v, %v, calls=%d", response, err, calls.Load())
	}
}

func TestCustomToolLoopExecutesRegisteredTool(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		call := calls.Add(1)
		var payload customRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode custom request: %v", err)
		}
		if call == 1 {
			if len(payload.Tools) != 1 || len(payload.ToolResults) != 0 {
				t.Errorf("initial custom tool payload = %#v", payload)
			}
			_, _ = io.WriteString(writer, `{"tool_calls":[{"id":"custom-1","name":"lookup","arguments":"{\"q\":\"refund\"}"}]}`)
			return
		}
		if len(payload.ToolResults) != 1 || payload.ToolResults[0].ID != "custom-1" ||
			payload.ToolResults[0].Content == nil {
			t.Errorf("custom tool result missing: %#v", payload.ToolResults)
		}
		_, _ = io.WriteString(writer, `{"reply":"自定义工具答案"}`)
	}))
	defer server.Close()

	registry := tool.NewRegistry()
	if err := registry.Register(tool.Definition{
		Name: "lookup",
		Handler: func(_ context.Context, call tool.Call) (tool.Result, error) {
			if call.Actor.Platform != "qq-onebot" ||
				call.Actor.ChatType != "group" ||
				call.Actor.ChatID != "30003" {
				t.Errorf("custom tool actor = %#v", call.Actor)
			}
			return tool.Result{Content: map[string]string{"answer": string(call.Arguments)}}, nil
		},
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	client := NewHTTPClient(testAgentConfig("custom", server.URL), discardLogger())
	client.SetTools(registry, 2)
	response, err := client.Reply(context.Background(), testAgentRequest())
	if err != nil || response.Reply != "自定义工具答案" || calls.Load() != 2 {
		t.Fatalf("Reply() = %#v, %v, calls=%d", response, err, calls.Load())
	}
}

func TestToolLoopFinalRoundDoesNotExposeTools(t *testing.T) {
	tests := []struct {
		mode string
	}{
		{mode: "openai"},
		{mode: "custom"},
	}
	for _, current := range tests {
		t.Run(current.mode, func(t *testing.T) {
			var calls atomic.Int32
			var executions atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(
				writer http.ResponseWriter,
				request *http.Request,
			) {
				call := calls.Add(1)
				if current.mode == "openai" {
					var payload openAIRequest
					if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
						t.Fatal(err)
					}
					if call == 1 {
						if len(payload.Tools) != 1 {
							t.Fatalf("initial tools = %#v", payload.Tools)
						}
						_, _ = io.WriteString(writer, `{"choices":[{"message":{
							"role":"assistant",
							"tool_calls":[{"id":"call-1","type":"function",
							"function":{"name":"lookup","arguments":"{}"}}]
						}}]}`)
						return
					}
					if len(payload.Tools) != 0 {
						t.Fatalf("final tools = %#v", payload.Tools)
					}
					_, _ = io.WriteString(writer, `{"choices":[{"message":{
						"role":"assistant","content":"根据现有结果回答"
					}}]}`)
					return
				}
				var payload customRequest
				if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				if call == 1 {
					if len(payload.Tools) != 1 {
						t.Fatalf("initial tools = %#v", payload.Tools)
					}
					_, _ = io.WriteString(writer,
						`{"tool_calls":[{"id":"call-1","name":"lookup","arguments":"{}"}]}`,
					)
					return
				}
				if len(payload.Tools) != 0 {
					t.Fatalf("final tools = %#v", payload.Tools)
				}
				_, _ = io.WriteString(writer, `{"reply":"根据现有结果回答"}`)
			}))
			defer server.Close()

			registry := tool.NewRegistry()
			if err := registry.Register(tool.Definition{
				Name: "lookup",
				Handler: func(context.Context, tool.Call) (tool.Result, error) {
					executions.Add(1)
					return tool.Result{Content: "evidence"}, nil
				},
			}); err != nil {
				t.Fatal(err)
			}
			client := NewHTTPClient(
				testAgentConfig(current.mode, server.URL),
				discardLogger(),
			)
			client.SetTools(registry, 1)
			response, err := client.Reply(context.Background(), testAgentRequest())
			if err != nil ||
				response.Reply != "根据现有结果回答" ||
				calls.Load() != 2 ||
				executions.Load() != 1 {
				t.Fatalf(
					"Reply()=%#v err=%v calls=%d executions=%d",
					response,
					err,
					calls.Load(),
					executions.Load(),
				)
			}
		})
	}
}

func TestOpenAITerminalToolResponseSkipsSecondModelRequest(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(writer, `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call-1","type":"function","function":{"name":"deliver","arguments":"{}"}}]}}]}`)
	}))
	defer server.Close()

	registry := tool.NewRegistry()
	if err := registry.Register(tool.Definition{
		Name: "deliver",
		Handler: func(context.Context, tool.Call) (tool.Result, error) {
			return tool.Result{Response: &domain.AgentResponse{
				Reply: "请转私聊获取文件。",
			}}, nil
		},
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	client := NewHTTPClient(testAgentConfig("openai", server.URL), discardLogger())
	client.SetTools(registry, 2)
	response, err := client.Reply(context.Background(), testAgentRequest())
	if err != nil ||
		response.Reply != "请转私聊获取文件。" ||
		len(response.Chain) != 1 ||
		calls.Load() != 1 {
		t.Fatalf("Reply() = %#v, %v, calls=%d", response, err, calls.Load())
	}
}

func TestCustomTerminalToolResponseSkipsSecondModelRequest(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(writer, `{"tool_calls":[{"id":"call-1","name":"deliver","arguments":"{}"}]}`)
	}))
	defer server.Close()

	registry := tool.NewRegistry()
	if err := registry.Register(tool.Definition{
		Name: "deliver",
		Handler: func(context.Context, tool.Call) (tool.Result, error) {
			return tool.Result{Response: &domain.AgentResponse{
				Reply: "文件已发送。",
			}}, nil
		},
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	client := NewHTTPClient(testAgentConfig("custom", server.URL), discardLogger())
	client.SetTools(registry, 2)
	response, err := client.Reply(context.Background(), testAgentRequest())
	if err != nil ||
		response.Reply != "文件已发送。" ||
		len(response.Chain) != 1 ||
		calls.Load() != 1 {
		t.Fatalf("Reply() = %#v, %v, calls=%d", response, err, calls.Load())
	}
}

type denyingToolGuard struct{}

func (denyingToolGuard) Check(context.Context, tool.Call) error {
	return tool.ErrPermissionDenied
}

func TestToolPermissionDenialReturnsFixedReplyWithoutSecondModelRequest(t *testing.T) {
	tests := []struct {
		mode     string
		response string
	}{
		{
			mode:     "openai",
			response: `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{}"}}]}}]}`,
		},
		{
			mode:     "custom",
			response: `{"tool_calls":[{"id":"call-1","name":"lookup","arguments":"{}"}]}`,
		},
	}
	for _, current := range tests {
		t.Run(current.mode, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				_, _ = io.WriteString(writer, current.response)
			}))
			defer server.Close()

			registry := tool.NewRegistry()
			registry.SetGuard(denyingToolGuard{})
			if err := registry.Register(tool.Definition{
				Name: "lookup",
				Handler: func(context.Context, tool.Call) (tool.Result, error) {
					t.Fatal("denied tool handler was called")
					return tool.Result{}, nil
				},
			}); err != nil {
				t.Fatalf("Register() error = %v", err)
			}
			client := NewHTTPClient(testAgentConfig(current.mode, server.URL), discardLogger())
			client.SetTools(registry, 2)
			response, err := client.Reply(context.Background(), testAgentRequest())
			if err != nil ||
				response.Reply != tool.PermissionDeniedReply ||
				len(response.Chain) != 1 ||
				calls.Load() != 1 {
				t.Fatalf("Reply() = %#v, %v, calls=%d", response, err, calls.Load())
			}
		})
	}
}

func TestOpenAIStreamingEmitsDeltas(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer,
			"data: {\"choices\":[{\"delta\":{\"content\":\"第\"}}]}\n\n"+
				"data: {\"choices\":[{\"delta\":{\"content\":\"一段\"}}]}\n\n"+
				"data: [DONE]\n\n",
		)
	}))
	defer server.Close()
	client := NewHTTPClient(testAgentConfig("openai", server.URL), discardLogger())
	var chunks []string
	err := client.Stream(context.Background(), testAgentRequest(), func(chunk string) error {
		chunks = append(chunks, chunk)
		return nil
	})
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	if strings.Join(chunks, "") != "第一段" {
		t.Fatalf("chunks = %#v", chunks)
	}
}

func TestRetryOnRateLimit(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if calls.Add(1) == 1 {
			writer.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = io.WriteString(writer, `{"reply":"ok","handoff":false}`)
	}))
	defer server.Close()

	cfg := testAgentConfig("custom", server.URL)
	cfg.MaxRetries = 1
	client := NewHTTPClient(cfg, discardLogger())
	client.sleep = func(context.Context, time.Duration) error { return nil }

	response, err := client.Reply(context.Background(), testAgentRequest())
	if err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if response.Reply != "ok" || calls.Load() != 2 {
		t.Fatalf("response=%#v calls=%d", response, calls.Load())
	}
}

func TestDoesNotRetryBadRequest(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		writer.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	client := NewHTTPClient(testAgentConfig("custom", server.URL), discardLogger())
	client.sleep = func(context.Context, time.Duration) error {
		t.Fatal("sleep called for non-retryable response")
		return nil
	}

	if _, err := client.Reply(context.Background(), testAgentRequest()); err == nil {
		t.Fatalf("Reply() error = nil, want error")
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", calls.Load())
	}
}

func testAgentConfig(mode, endpoint string) Config {
	return Config{
		Mode:         mode,
		URL:          endpoint,
		APIKey:       "test-key",
		SystemPrompt: "system",
		AuthHeader:   "Authorization",
		AuthScheme:   "Bearer",
		Timeout:      time.Second,
		MaxRetries:   0,
		RetryBase:    time.Millisecond,
		RetryMax:     10 * time.Millisecond,
	}
}

func testAgentRequest() domain.AgentRequest {
	return domain.AgentRequest{
		RequestID:  "request-1",
		SessionID:  "qq:group:30003:user:20002",
		Text:       "新问题",
		MessageID:  "40004",
		UserID:     "20002",
		Platform:   "qq-onebot",
		ChatType:   "group",
		ChatID:     "30003",
		GroupID:    "30003",
		SelfID:     "10001",
		SenderName: "tester",
		Chain:      message.Chain{message.Text("新问题")},
		History: []domain.ChatMessage{
			{Role: "user", Content: "旧问题"},
		},
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
