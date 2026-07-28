package status

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/binding"
	"github.com/lianyorker/cinlan-qq-bot/internal/bot"
	"github.com/lianyorker/cinlan-qq-bot/internal/cron"
	"github.com/lianyorker/cinlan-qq-bot/internal/domain"
	"github.com/lianyorker/cinlan-qq-bot/internal/mcp"
	coreonebot "github.com/lianyorker/cinlan-qq-bot/internal/onebot"
	"github.com/lianyorker/cinlan-qq-bot/internal/persona"
	"github.com/lianyorker/cinlan-qq-bot/internal/platform"
	"github.com/lianyorker/cinlan-qq-bot/internal/plugin"
	"github.com/lianyorker/cinlan-qq-bot/internal/provider"
	"github.com/lianyorker/cinlan-qq-bot/internal/session"
	"github.com/lianyorker/cinlan-qq-bot/internal/skill"
	"github.com/lianyorker/cinlan-qq-bot/internal/subagent"
	"github.com/lianyorker/cinlan-qq-bot/internal/tool"
)

type fakeConnection bool

func (f fakeConnection) Connected() bool {
	return bool(f)
}

type fakeNamedConnection struct {
	connected bool
	name      string
}

func (f fakeNamedConnection) Connected() bool { return f.connected }
func (f fakeNamedConnection) Name() string    { return f.name }

type fakeStats struct{}

func (fakeStats) Stats() bot.Stats {
	return bot.Stats{Received: 3, Replied: 2}
}

type fakeProvider struct{}

func (fakeProvider) Name() string { return "default" }
func (fakeProvider) Kind() string { return "test" }
func (fakeProvider) Reply(context.Context, domain.AgentRequest) (domain.AgentResponse, error) {
	return domain.AgentResponse{Reply: "ok"}, nil
}

type fakePipeline struct{}

func (fakePipeline) PipelineStages() []string { return []string{"wake", "agent"} }

type fakeMCP struct {
	refreshed bool
	err       error
}

type fakeSkills struct {
	reloaded bool
	err      error
}

type fakeSubagents struct {
	reloaded bool
}

type fakePluginRuntime struct {
	reloaded bool
}

func (f *fakePluginRuntime) List() []plugin.RuntimeInfo {
	return []plugin.RuntimeInfo{{Name: "crm", Enabled: true}}
}

func (f *fakePluginRuntime) Reload(context.Context) error {
	f.reloaded = true
	return nil
}

type fakeActionAdapter struct {
	action string
	params map[string]any
	err    error
}

func (f *fakeActionAdapter) Name() string                  { return "fake" }
func (f *fakeActionAdapter) Connected() bool               { return true }
func (f *fakeActionAdapter) Events() <-chan platform.Event { return nil }
func (f *fakeActionAdapter) Run(context.Context) error     { return nil }
func (f *fakeActionAdapter) Send(context.Context, platform.Outbound) error {
	return nil
}
func (f *fakeActionAdapter) Call(_ context.Context, action string, params map[string]any) (any, error) {
	f.action = action
	f.params = params
	if f.err != nil {
		return nil, f.err
	}
	return map[string]any{"ok": true}, nil
}

func (f *fakeSubagents) List() []subagent.Info {
	return []subagent.Info{{Name: "after_sales", Tool: "transfer_to_after_sales"}}
}

func (f *fakeSubagents) Reload(context.Context) error {
	f.reloaded = true
	return nil
}

func (f *fakeSkills) List() []skill.Info {
	return []skill.Info{{Name: "refund", Description: "refund"}}
}

func (f *fakeSkills) Reload(context.Context) error {
	f.reloaded = true
	return f.err
}

func (f *fakeMCP) List() []mcp.ServerInfo {
	return []mcp.ServerInfo{{Name: "crm", State: "ready", ToolCount: 1}}
}

func (f *fakeMCP) Refresh(context.Context) error {
	f.refreshed = true
	return f.err
}

func TestReadyReflectsPlatformConnection(t *testing.T) {
	tests := []struct {
		name                  string
		connection            fakeNamedConnection
		wantStatus            int
		wantPlatform          string
		wantPlatformConnected bool
		wantOneBotConnected   bool
	}{
		{
			name:                  "native ready",
			connection:            fakeNamedConnection{connected: true, name: platform.PlatformQQNative},
			wantStatus:            http.StatusOK,
			wantPlatform:          platform.PlatformQQNative,
			wantPlatformConnected: true,
			wantOneBotConnected:   false,
		},
		{
			name:                  "onebot ready",
			connection:            fakeNamedConnection{connected: true, name: platform.PlatformQQOneBot},
			wantStatus:            http.StatusOK,
			wantPlatform:          platform.PlatformQQOneBot,
			wantPlatformConnected: true,
			wantOneBotConnected:   true,
		},
		{
			name:                  "onebot disconnected",
			connection:            fakeNamedConnection{connected: false, name: platform.PlatformQQOneBot},
			wantStatus:            http.StatusServiceUnavailable,
			wantPlatform:          platform.PlatformQQOneBot,
			wantPlatformConnected: false,
			wantOneBotConnected:   false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := New(":0", test.connection, fakeStats{})
			request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
			response := httptest.NewRecorder()
			server.httpServer.Handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}

			var body struct {
				Platform          string `json:"platform"`
				PlatformConnected bool   `json:"platform_connected"`
				OneBotConnected   bool   `json:"onebot_connected"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Platform != test.wantPlatform ||
				body.PlatformConnected != test.wantPlatformConnected ||
				body.OneBotConnected != test.wantOneBotConnected {
				t.Fatalf("response = %#v", body)
			}
		})
	}
}

func TestStatusRejectsMutationMethods(t *testing.T) {
	server := New(":0", fakeConnection(true), fakeStats{})
	request := httptest.NewRequest(http.MethodPost, "/status", nil)
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)

	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", response.Code)
	}
}

func TestAdminWebIsEmbeddedAndProtectedByBrowserHeaders(t *testing.T) {
	server := New(":0", fakeConnection(true), fakeStats{})
	request := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), "Cinlan Bot 管理后台") {
		t.Fatalf("admin web response = %d %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Security-Policy") == "" ||
		response.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("admin web security headers = %#v", response.Header())
	}

	request = httptest.NewRequest(http.MethodGet, "/admin/js/app.js", nil)
	response = httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "loadShared") {
		t.Fatalf("admin app response = %d %s", response.Code, response.Body.String())
	}
}

func TestAdminAPIRequiresTokenAndListsRuntime(t *testing.T) {
	providers := provider.NewRegistry()
	if err := providers.Register(fakeProvider{}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	sessions := session.New(10, time.Hour)
	sessions.AddExchange("qq:group:1:user:2", "q", "a")
	server := NewWithOptions(":0", fakeConnection(true), fakeStats{}, AdminOptions{
		Token:     "secret",
		Providers: providers,
		Plugins:   plugin.NewRegistry(),
		Sessions:  sessions,
		Pipeline:  fakePipeline{},
		MCP:       &fakeMCP{},
	})

	unauthorized := httptest.NewRequest(http.MethodGet, "/api/v1/providers", nil)
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, unauthorized)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", response.Code)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/pipeline", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response = httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "wake") {
		t.Fatalf("pipeline response = %d %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil)
	request.Header.Set("X-Admin-Token", "secret")
	response = httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	var sessionsResponse []map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &sessionsResponse); err != nil {
		t.Fatalf("decode sessions response: %v", err)
	}
	if len(sessionsResponse) != 1 || sessionsResponse[0]["history_count"] != float64(2) {
		t.Fatalf("sessions response = %#v", sessionsResponse)
	}
}

func TestAdminLoginIssuesAndRevokesSessionToken(t *testing.T) {
	personas := persona.NewRegistry()
	if err := personas.Register(persona.Profile{
		Name:         "default",
		SystemPrompt: "default prompt",
		Default:      true,
	}); err != nil {
		t.Fatalf("Register(persona) error = %v", err)
	}
	server := NewWithOptions(":0", fakeConnection(true), fakeStats{}, AdminOptions{
		Username: "admin",
		Password: "admin123",
		Personas: personas,
	})

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/auth/login",
		strings.NewReader(`{"username":"admin","password":"wrong"}`),
	)
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("invalid login = %d %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(
		http.MethodPost,
		"/api/v1/auth/login",
		strings.NewReader(`{"username":"admin","password":"admin123"}`),
	)
	response = httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	var login struct {
		Token string `json:"token"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &login) != nil || login.Token == "" {
		t.Fatalf("valid login = %d %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/personas", nil)
	request.Header.Set("Authorization", "Bearer "+login.Token)
	response = httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("session authorization = %d %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	request.Header.Set("Authorization", "Bearer "+login.Token)
	response = httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("logout = %d %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/personas", nil)
	request.Header.Set("Authorization", "Bearer "+login.Token)
	response = httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session = %d %s", response.Code, response.Body.String())
	}
}

func TestAdminAPIMCPStatusAndRefresh(t *testing.T) {
	runtime := &fakeMCP{}
	server := NewWithOptions(":0", fakeConnection(true), fakeStats{}, AdminOptions{
		Token: "secret",
		MCP:   runtime,
	})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/mcp", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"crm"`) {
		t.Fatalf("MCP status = %d %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/mcp/refresh", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response = httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !runtime.refreshed {
		t.Fatalf("MCP refresh = %d %s", response.Code, response.Body.String())
	}
}

func TestAdminAPISkillsStatusAndRefresh(t *testing.T) {
	runtime := &fakeSkills{}
	server := NewWithOptions(":0", fakeConnection(true), fakeStats{}, AdminOptions{
		Token:  "secret",
		Skills: runtime,
	})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/skills", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"refund"`) {
		t.Fatalf("skills status = %d %s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodPost, "/api/v1/skills/refresh", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response = httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !runtime.reloaded {
		t.Fatalf("skills refresh = %d %s", response.Code, response.Body.String())
	}
}

func TestAdminAPISubagentStatusAndRefresh(t *testing.T) {
	runtime := &fakeSubagents{}
	server := NewWithOptions(":0", fakeConnection(true), fakeStats{}, AdminOptions{
		Token:     "secret",
		Subagents: runtime,
	})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/subagents", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "after_sales") {
		t.Fatalf("subagent status = %d %s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodPost, "/api/v1/subagents/refresh", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response = httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !runtime.reloaded {
		t.Fatalf("subagent refresh = %d %s", response.Code, response.Body.String())
	}
}

func TestAdminAPIPluginStatusAndRefresh(t *testing.T) {
	runtime := &fakePluginRuntime{}
	server := NewWithOptions(":0", fakeConnection(true), fakeStats{}, AdminOptions{
		Token:         "secret",
		PluginRuntime: runtime,
	})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/plugins/webhooks", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"crm"`) {
		t.Fatalf("plugin status = %d %s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodPost, "/api/v1/plugins/refresh", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response = httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !runtime.reloaded {
		t.Fatalf("plugin refresh = %d %s", response.Code, response.Body.String())
	}
}

func TestAdminAPICreatesAndDeletesCronJob(t *testing.T) {
	scheduler := cron.New(nil, nil)
	server := NewWithOptions(":0", fakeConnection(true), fakeStats{}, AdminOptions{
		Token: "secret",
		Cron:  scheduler,
	})
	body := bytes.NewBufferString(`{
		"id":"notice",
		"name":"notice",
		"chat_type":"group",
		"chat_id":"30003",
		"text":"hello",
		"interval_seconds":10
	}`)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", body)
	request.Header.Set("Authorization", "bEaReR secret")
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || len(scheduler.List()) != 1 {
		t.Fatalf("create job = %d, %#v", response.Code, scheduler.List())
	}
	request = httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/notice", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response = httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || len(scheduler.List()) != 0 {
		t.Fatalf("delete job = %d, %#v", response.Code, scheduler.List())
	}
}

func TestAdminAPICallsOneBotAction(t *testing.T) {
	adapter := &fakeActionAdapter{}
	server := NewWithOptions(":0", fakeConnection(true), fakeStats{}, AdminOptions{
		Token:   "secret",
		Actions: adapter,
	})
	body := bytes.NewBufferString(`{"action":"get_group_info","params":{"group_id":30003}}`)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/actions", body)
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("action response = %d %s", response.Code, response.Body.String())
	}
	if adapter.action != "get_group_info" || adapter.params["group_id"].(json.Number).String() != "30003" {
		t.Fatalf("captured action = %q %#v", adapter.action, adapter.params)
	}
}

func TestAdminAPIPreservesOneBotActionError(t *testing.T) {
	adapter := &fakeActionAdapter{err: &coreonebot.ActionError{
		Action:  "set_group_ban",
		Status:  "failed",
		RetCode: 1403,
		Message: "permission denied",
		Wording: "not an administrator",
	}}
	server := NewWithOptions(":0", fakeConnection(true), fakeStats{}, AdminOptions{
		Token:   "secret",
		Actions: adapter,
	})
	body := bytes.NewBufferString(`{"action":"set_group_ban","params":{"group_id":30003}}`)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/actions", body)
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadGateway {
		t.Fatalf("action response = %d %s", response.Code, response.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode action error: %v", err)
	}
	if result["error"] != "onebot_action_failed" || result["retcode"] != float64(1403) {
		t.Fatalf("action error = %#v", result)
	}
}

func TestAdminAPIUpdatesSessionPersonaAndProvider(t *testing.T) {
	providers := provider.NewRegistry()
	if err := providers.Register(fakeProvider{}); err != nil {
		t.Fatalf("Register(provider) error = %v", err)
	}
	personas := persona.NewRegistry()
	if err := personas.Register(persona.Profile{
		Name:         "sales",
		SystemPrompt: "sales prompt",
	}); err != nil {
		t.Fatalf("Register(persona) error = %v", err)
	}
	sessions := session.New(10, time.Hour)
	server := NewWithOptions(":0", fakeConnection(true), fakeStats{}, AdminOptions{
		Token:     "secret",
		Providers: providers,
		Personas:  personas,
		Sessions:  sessions,
	})
	body := bytes.NewBufferString(`{"persona":"sales","provider":"default"}`)
	request := httptest.NewRequest(
		http.MethodPut,
		"/api/v1/sessions/qq-onebot:self:10001:group:30003/settings",
		body,
	)
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("settings response = %d %s", response.Code, response.Body.String())
	}
	settings := sessions.Settings("qq-onebot:self:10001:group:30003")
	if settings.Persona != "sales" || settings.Provider != "default" {
		t.Fatalf("settings = %#v", settings)
	}
}

func TestAdminAPIManagesPersistentPersonasAndBindings(t *testing.T) {
	providers := provider.NewRegistry()
	if err := providers.Register(fakeProvider{}); err != nil {
		t.Fatalf("Register(provider) error = %v", err)
	}
	personas := persona.NewRegistry()
	if err := personas.Register(persona.Profile{
		Name:         "default",
		SystemPrompt: "default prompt",
		Default:      true,
	}); err != nil {
		t.Fatalf("Register(default persona) error = %v", err)
	}
	personaPath := filepath.Join(t.TempDir(), "personas.json")
	if err := personas.UseFile(personaPath); err != nil {
		t.Fatalf("UseFile(persona) error = %v", err)
	}
	bindingPath := filepath.Join(t.TempDir(), "bindings.json")
	bindings, err := binding.OpenFile(bindingPath)
	if err != nil {
		t.Fatalf("OpenFile(binding) error = %v", err)
	}
	server := NewWithOptions(":0", fakeConnection(true), fakeStats{}, AdminOptions{
		Token:     "secret",
		Providers: providers,
		Personas:  personas,
		Bindings:  bindings,
		Sessions:  session.New(10, time.Hour),
	})

	call := func(method, target, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, target, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer secret")
		response := httptest.NewRecorder()
		server.httpServer.Handler.ServeHTTP(response, request)
		return response
	}

	response := call(http.MethodPost, "/api/v1/personas", `{
		"name":"community",
		"description":"Community assistant",
		"system_prompt":"Answer community questions",
		"begin_dialogs":[{"user":"hello","assistant":"welcome"}],
		"custom_error_message":"Please retry later"
	}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("create persona = %d %s", response.Code, response.Body.String())
	}
	response = call(http.MethodPost, "/api/v1/bindings", `{
		"name":"test-group",
		"platform":"qq-native",
		"self_id":"10000001",
		"chat_type":"group",
		"chat_id":"20000001",
		"persona":"community",
		"provider":"default",
		"require_mention":false
	}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("create binding = %d %s", response.Code, response.Body.String())
	}
	response = call(http.MethodDelete, "/api/v1/personas/community", "")
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "persona_in_use") {
		t.Fatalf("delete referenced persona = %d %s", response.Code, response.Body.String())
	}
	response = call(http.MethodPut, "/api/v1/personas/community", `{
		"name":"community",
		"system_prompt":"Updated prompt",
		"custom_error_message":"Updated error"
	}`)
	if response.Code != http.StatusOK {
		t.Fatalf("update persona = %d %s", response.Code, response.Body.String())
	}
	response = call(http.MethodDelete, "/api/v1/bindings/test-group", "")
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete binding = %d %s", response.Code, response.Body.String())
	}
	response = call(http.MethodDelete, "/api/v1/personas/community", "")
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete persona = %d %s", response.Code, response.Body.String())
	}

	reloadedPersonas, _, err := persona.LoadFile(personaPath)
	if err != nil || len(reloadedPersonas) != 1 || reloadedPersonas[0].Name != "default" {
		t.Fatalf("reloaded personas = %#v, error = %v", reloadedPersonas, err)
	}
	reloadedBindings, err := binding.LoadFile(bindingPath)
	if err != nil || len(reloadedBindings.List()) != 0 {
		t.Fatalf("reloaded bindings = %#v, error = %v", reloadedBindings, err)
	}
}

func TestAdminBindingRejectsUnknownScopedTool(t *testing.T) {
	bindings, err := binding.OpenFile(filepath.Join(t.TempDir(), "bindings.json"))
	if err != nil {
		t.Fatalf("OpenFile(binding) error = %v", err)
	}
	tools := tool.NewRegistry()
	if err := tools.Register(tool.Definition{
		Name: "known",
		Handler: func(context.Context, tool.Call) (tool.Result, error) {
			return tool.Result{}, nil
		},
	}); err != nil {
		t.Fatalf("Register(tool) error = %v", err)
	}
	server := NewWithOptions(":0", fakeConnection(true), fakeStats{}, AdminOptions{
		Token:    "secret",
		Bindings: bindings,
		Tools:    tools,
	})
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/bindings",
		strings.NewReader(`{
			"name":"group",
			"platform":"*",
			"self_id":"1",
			"chat_type":"group",
			"chat_id":"2",
			"tools":["unknown"]
		}`),
	)
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest ||
		!strings.Contains(response.Body.String(), "tool_not_found") {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}
