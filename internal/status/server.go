package status

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/binding"
	"github.com/lianyorker/cinlan-qq-bot/internal/bot"
	"github.com/lianyorker/cinlan-qq-bot/internal/command"
	"github.com/lianyorker/cinlan-qq-bot/internal/cron"
	"github.com/lianyorker/cinlan-qq-bot/internal/knowledge"
	"github.com/lianyorker/cinlan-qq-bot/internal/mcp"
	coreonebot "github.com/lianyorker/cinlan-qq-bot/internal/onebot"
	"github.com/lianyorker/cinlan-qq-bot/internal/persona"
	"github.com/lianyorker/cinlan-qq-bot/internal/platform"
	onebotplatform "github.com/lianyorker/cinlan-qq-bot/internal/platform/onebot"
	"github.com/lianyorker/cinlan-qq-bot/internal/plugin"
	"github.com/lianyorker/cinlan-qq-bot/internal/provider"
	"github.com/lianyorker/cinlan-qq-bot/internal/session"
	"github.com/lianyorker/cinlan-qq-bot/internal/skill"
	"github.com/lianyorker/cinlan-qq-bot/internal/subagent"
	"github.com/lianyorker/cinlan-qq-bot/internal/tool"
)

type ConnectionStatus interface {
	Connected() bool
}

type StatsProvider interface {
	Stats() bot.Stats
}

type PipelineProvider interface {
	PipelineStages() []string
}

type MCPRuntime interface {
	List() []mcp.ServerInfo
	Refresh(context.Context) error
}

type SkillRuntime interface {
	List() []skill.Info
	Reload(context.Context) error
}

type SubagentRuntime interface {
	List() []subagent.Info
	Reload(context.Context) error
}

type PluginRuntime interface {
	List() []plugin.RuntimeInfo
	Reload(context.Context) error
}

type KnowledgeRuntime interface {
	Count() int
	Collections() []string
	Search(string, int) []knowledge.Hit
}

type AccountRuntime interface {
	Accounts() []onebotplatform.AccountInfo
}

type AccountActionRuntime interface {
	CallFor(context.Context, string, string, map[string]any) (any, error)
}

type AdminOptions struct {
	Token         string
	Providers     *provider.Registry
	Plugins       *plugin.Registry
	PluginRuntime PluginRuntime
	Commands      *command.Registry
	Platforms     *platform.Registry
	Tools         *tool.Registry
	MCP           MCPRuntime
	Skills        SkillRuntime
	Knowledge     KnowledgeRuntime
	Subagents     SubagentRuntime
	Accounts      AccountRuntime
	Actions       platform.Adapter
	Cron          *cron.Scheduler
	Personas      *persona.Registry
	Bindings      *binding.Registry
	Sessions      *session.Store
	Pipeline      PipelineProvider
}

type Server struct {
	httpServer *http.Server
	connection ConnectionStatus
	stats      StatsProvider
	startedAt  time.Time
	admin      AdminOptions
}

func New(listenAddr string, connection ConnectionStatus, stats StatsProvider) *Server {
	return NewWithOptions(listenAddr, connection, stats, AdminOptions{})
}

func NewWithOptions(
	listenAddr string,
	connection ConnectionStatus,
	stats StatsProvider,
	admin AdminOptions,
) *Server {
	server := &Server{
		connection: connection,
		stats:      stats,
		startedAt:  time.Now(),
		admin:      admin,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", server.health)
	mux.HandleFunc("/readyz", server.ready)
	mux.HandleFunc("/status", server.status)
	mux.HandleFunc("/api/v1/", server.adminAPI)
	server.httpServer = &http.Server{
		Addr:              listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	return server
}

func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.httpServer.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
			return err
		}
		err := <-errCh
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (s *Server) health(writer http.ResponseWriter, request *http.Request) {
	if !allowReadMethod(writer, request) {
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) ready(writer http.ResponseWriter, request *http.Request) {
	if !allowReadMethod(writer, request) {
		return
	}
	if !s.connection.Connected() {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]any{
			"status":             "not_ready",
			"platform":           s.platformName(),
			"platform_connected": false,
			"onebot_connected":   false,
		})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"status":             "ready",
		"platform":           s.platformName(),
		"platform_connected": true,
		"onebot_connected":   s.oneBotConnected(),
	})
}

func (s *Server) status(writer http.ResponseWriter, request *http.Request) {
	if !allowReadMethod(writer, request) {
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"status":             "ok",
		"platform":           s.platformName(),
		"platform_connected": s.connection.Connected(),
		"onebot_connected":   s.oneBotConnected(),
		"uptime_seconds":     int64(time.Since(s.startedAt).Seconds()),
		"stats":              s.stats.Stats(),
	})
}

func (s *Server) platformName() string {
	named, ok := s.connection.(interface{ Name() string })
	if !ok {
		return ""
	}
	return named.Name()
}

func (s *Server) oneBotConnected() bool {
	return s.connection.Connected() && s.platformName() == platform.PlatformQQOneBot
}

func (s *Server) adminAPI(writer http.ResponseWriter, request *http.Request) {
	path := strings.TrimPrefix(request.URL.Path, "/api/v1/")
	if !s.authorizeAdmin(writer, request) {
		return
	}
	switch {
	case path == "providers":
		if !allowReadMethod(writer, request) {
			return
		}
		if s.admin.Providers == nil {
			writeJSON(writer, http.StatusOK, []any{})
			return
		}
		writeJSON(writer, http.StatusOK, s.admin.Providers.List())
	case path == "providers/default":
		s.setDefaultProvider(writer, request)
	case path == "plugins":
		if !allowReadMethod(writer, request) {
			return
		}
		if s.admin.Plugins == nil {
			writeJSON(writer, http.StatusOK, []any{})
			return
		}
		writeJSON(writer, http.StatusOK, s.admin.Plugins.List())
	case path == "plugins/webhooks":
		if !allowReadMethod(writer, request) {
			return
		}
		if s.admin.PluginRuntime == nil {
			writeJSON(writer, http.StatusOK, []any{})
			return
		}
		writeJSON(writer, http.StatusOK, s.admin.PluginRuntime.List())
	case path == "plugins/refresh":
		s.refreshPlugins(writer, request)
	case path == "personas":
		s.personas(writer, request)
	case path == "personas/default":
		s.setDefaultPersona(writer, request)
	case strings.HasPrefix(path, "personas/"):
		s.persona(writer, request, strings.TrimPrefix(path, "personas/"))
	case path == "bindings":
		s.bindings(writer, request)
	case strings.HasPrefix(path, "bindings/"):
		s.binding(writer, request, strings.TrimPrefix(path, "bindings/"))
	case path == "commands":
		if !allowReadMethod(writer, request) {
			return
		}
		if s.admin.Commands == nil {
			writeJSON(writer, http.StatusOK, []any{})
			return
		}
		writeJSON(writer, http.StatusOK, s.admin.Commands.List())
	case path == "platforms":
		if !allowReadMethod(writer, request) {
			return
		}
		if s.admin.Platforms == nil {
			writeJSON(writer, http.StatusOK, []any{})
			return
		}
		writeJSON(writer, http.StatusOK, s.admin.Platforms.List())
	case path == "accounts":
		if !allowReadMethod(writer, request) {
			return
		}
		if s.admin.Accounts == nil {
			writeJSON(writer, http.StatusOK, []any{})
			return
		}
		writeJSON(writer, http.StatusOK, s.admin.Accounts.Accounts())
	case path == "actions":
		s.callAction(writer, request)
	case path == "tools":
		if !allowReadMethod(writer, request) {
			return
		}
		if s.admin.Tools == nil {
			writeJSON(writer, http.StatusOK, []any{})
			return
		}
		writeJSON(writer, http.StatusOK, s.admin.Tools.List())
	case path == "mcp":
		if !allowReadMethod(writer, request) {
			return
		}
		if s.admin.MCP == nil {
			writeJSON(writer, http.StatusOK, []any{})
			return
		}
		writeJSON(writer, http.StatusOK, s.admin.MCP.List())
	case path == "mcp/refresh":
		s.refreshMCP(writer, request)
	case path == "skills":
		if !allowReadMethod(writer, request) {
			return
		}
		if s.admin.Skills == nil {
			writeJSON(writer, http.StatusOK, []any{})
			return
		}
		writeJSON(writer, http.StatusOK, s.admin.Skills.List())
	case path == "skills/refresh":
		s.refreshSkills(writer, request)
	case path == "knowledge":
		s.knowledge(writer, request)
	case path == "subagents":
		if !allowReadMethod(writer, request) {
			return
		}
		if s.admin.Subagents == nil {
			writeJSON(writer, http.StatusOK, []any{})
			return
		}
		writeJSON(writer, http.StatusOK, s.admin.Subagents.List())
	case path == "subagents/refresh":
		s.refreshSubagents(writer, request)
	case path == "jobs":
		s.jobs(writer, request)
	case strings.HasPrefix(path, "jobs/"):
		s.deleteJob(writer, request, strings.TrimPrefix(path, "jobs/"))
	case path == "pipeline":
		if !allowReadMethod(writer, request) {
			return
		}
		stages := []string(nil)
		if s.admin.Pipeline != nil {
			stages = s.admin.Pipeline.PipelineStages()
		}
		writeJSON(writer, http.StatusOK, map[string]any{"stages": stages})
	case path == "config-files":
		s.configFiles(writer, request)
	case path == "sessions":
		if !allowReadMethod(writer, request) {
			return
		}
		if s.admin.Sessions == nil {
			writeJSON(writer, http.StatusOK, []any{})
			return
		}
		writeJSON(writer, http.StatusOK, s.admin.Sessions.List())
	case path == "handoffs":
		if !allowReadMethod(writer, request) {
			return
		}
		if s.admin.Sessions == nil {
			writeJSON(writer, http.StatusOK, []any{})
			return
		}
		writeJSON(writer, http.StatusOK, s.admin.Sessions.ListHandoffs())
	case strings.HasPrefix(path, "sessions/") && strings.HasSuffix(path, "/settings"):
		encodedID := strings.TrimSuffix(strings.TrimPrefix(path, "sessions/"), "/settings")
		s.sessionSettings(writer, request, encodedID)
	case strings.HasPrefix(path, "sessions/"):
		s.deleteSession(writer, request, strings.TrimPrefix(path, "sessions/"))
	default:
		writeJSON(writer, http.StatusNotFound, map[string]any{"error": "not_found"})
	}
}

func (s *Server) personas(writer http.ResponseWriter, request *http.Request) {
	if s.admin.Personas == nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]any{"error": "persona_registry_unavailable"})
		return
	}
	switch request.Method {
	case http.MethodGet, http.MethodHead:
		writeJSON(writer, http.StatusOK, s.admin.Personas.List())
	case http.MethodPost:
		var profile persona.Profile
		if !decodeJSONBody(writer, request, 128<<10, &profile) {
			return
		}
		if _, exists := s.admin.Personas.Get(profile.Name); exists {
			writeJSON(writer, http.StatusConflict, map[string]any{"error": "persona_already_exists"})
			return
		}
		if err := s.admin.Personas.Upsert(profile); err != nil {
			s.writeRegistryError(writer, err, s.admin.Personas.PersistenceError())
			return
		}
		created, _ := s.admin.Personas.Get(profile.Name)
		if current, ok := s.admin.Personas.Default(); ok && current.Name == created.Name {
			created.Default = true
		}
		writeJSON(writer, http.StatusCreated, created)
	default:
		writer.Header().Set("Allow", "GET, HEAD, POST")
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]any{"error": "method_not_allowed"})
	}
}

func (s *Server) persona(writer http.ResponseWriter, request *http.Request, encodedName string) {
	if s.admin.Personas == nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]any{"error": "persona_registry_unavailable"})
		return
	}
	name, err := url.PathUnescape(encodedName)
	name = strings.TrimSpace(name)
	if err != nil || name == "" {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": "invalid_persona_name"})
		return
	}
	switch request.Method {
	case http.MethodPut:
		if _, exists := s.admin.Personas.Get(name); !exists {
			writeJSON(writer, http.StatusNotFound, map[string]any{"error": "persona_not_found"})
			return
		}
		var profile persona.Profile
		if !decodeJSONBody(writer, request, 128<<10, &profile) {
			return
		}
		if strings.TrimSpace(profile.Name) == "" {
			profile.Name = name
		}
		if strings.TrimSpace(profile.Name) != name {
			writeJSON(writer, http.StatusBadRequest, map[string]any{"error": "persona_name_mismatch"})
			return
		}
		if err := s.admin.Personas.Upsert(profile); err != nil {
			s.writeRegistryError(writer, err, s.admin.Personas.PersistenceError())
			return
		}
		updated, _ := s.admin.Personas.Get(name)
		if current, ok := s.admin.Personas.Default(); ok && current.Name == updated.Name {
			updated.Default = true
		}
		writeJSON(writer, http.StatusOK, updated)
	case http.MethodDelete:
		if reference := s.personaReference(name); reference != "" {
			writeJSON(writer, http.StatusConflict, map[string]any{
				"error":     "persona_in_use",
				"reference": reference,
			})
			return
		}
		deleted, deleteErr := s.admin.Personas.Delete(name)
		if deleteErr != nil {
			statusCode := http.StatusConflict
			if s.admin.Personas.PersistenceError() != nil {
				statusCode = http.StatusInternalServerError
			}
			writeJSON(writer, statusCode, map[string]any{"error": deleteErr.Error()})
			return
		}
		if !deleted {
			writeJSON(writer, http.StatusNotFound, map[string]any{"error": "persona_not_found"})
			return
		}
		writeJSON(writer, http.StatusNoContent, nil)
	default:
		writer.Header().Set("Allow", "PUT, DELETE")
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]any{"error": "method_not_allowed"})
	}
}

func (s *Server) bindings(writer http.ResponseWriter, request *http.Request) {
	if s.admin.Bindings == nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]any{"error": "binding_registry_unavailable"})
		return
	}
	switch request.Method {
	case http.MethodGet, http.MethodHead:
		writeJSON(writer, http.StatusOK, s.admin.Bindings.List())
	case http.MethodPost:
		var rule binding.Rule
		if !decodeJSONBody(writer, request, 32<<10, &rule) {
			return
		}
		for _, current := range s.admin.Bindings.List() {
			if current.Name == strings.TrimSpace(rule.Name) {
				writeJSON(writer, http.StatusConflict, map[string]any{"error": "binding_already_exists"})
				return
			}
		}
		if !s.validateBindingReferences(writer, rule) {
			return
		}
		if err := s.admin.Bindings.Upsert(rule); err != nil {
			s.writeRegistryError(writer, err, s.admin.Bindings.PersistenceError())
			return
		}
		writeJSON(writer, http.StatusCreated, bindingByName(s.admin.Bindings, rule.Name))
	default:
		writer.Header().Set("Allow", "GET, HEAD, POST")
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]any{"error": "method_not_allowed"})
	}
}

func (s *Server) binding(writer http.ResponseWriter, request *http.Request, encodedName string) {
	if s.admin.Bindings == nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]any{"error": "binding_registry_unavailable"})
		return
	}
	name, err := url.PathUnescape(encodedName)
	name = strings.TrimSpace(name)
	if err != nil || name == "" {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": "invalid_binding_name"})
		return
	}
	switch request.Method {
	case http.MethodPut:
		if _, exists := findBinding(s.admin.Bindings, name); !exists {
			writeJSON(writer, http.StatusNotFound, map[string]any{"error": "binding_not_found"})
			return
		}
		var rule binding.Rule
		if !decodeJSONBody(writer, request, 32<<10, &rule) {
			return
		}
		if strings.TrimSpace(rule.Name) == "" {
			rule.Name = name
		}
		if strings.TrimSpace(rule.Name) != name {
			writeJSON(writer, http.StatusBadRequest, map[string]any{"error": "binding_name_mismatch"})
			return
		}
		if !s.validateBindingReferences(writer, rule) {
			return
		}
		if err := s.admin.Bindings.Upsert(rule); err != nil {
			s.writeRegistryError(writer, err, s.admin.Bindings.PersistenceError())
			return
		}
		writeJSON(writer, http.StatusOK, bindingByName(s.admin.Bindings, name))
	case http.MethodDelete:
		deleted, deleteErr := s.admin.Bindings.Delete(name)
		if deleteErr != nil {
			s.writeRegistryError(writer, deleteErr, s.admin.Bindings.PersistenceError())
			return
		}
		if !deleted {
			writeJSON(writer, http.StatusNotFound, map[string]any{"error": "binding_not_found"})
			return
		}
		writeJSON(writer, http.StatusNoContent, nil)
	default:
		writer.Header().Set("Allow", "PUT, DELETE")
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]any{"error": "method_not_allowed"})
	}
}

func (s *Server) validateBindingReferences(writer http.ResponseWriter, rule binding.Rule) bool {
	if name := strings.TrimSpace(rule.Persona); name != "" {
		if s.admin.Personas == nil {
			writeJSON(writer, http.StatusServiceUnavailable, map[string]any{"error": "persona_registry_unavailable"})
			return false
		}
		if _, ok := s.admin.Personas.Get(name); !ok {
			writeJSON(writer, http.StatusBadRequest, map[string]any{"error": "persona_not_found"})
			return false
		}
	}
	providerNames := []string{strings.TrimSpace(rule.Provider)}
	if rule.ReplyPolicy != nil {
		providerNames = append(providerNames, strings.TrimSpace(rule.ReplyPolicy.Provider))
	}
	for _, name := range providerNames {
		if name == "" {
			continue
		}
		if s.admin.Providers == nil {
			writeJSON(writer, http.StatusServiceUnavailable, map[string]any{"error": "provider_registry_unavailable"})
			return false
		}
		if !s.admin.Providers.Has(name) {
			writeJSON(writer, http.StatusBadRequest, map[string]any{"error": "provider_not_found", "name": name})
			return false
		}
	}
	for _, name := range rule.Tools {
		if s.admin.Tools == nil {
			writeJSON(writer, http.StatusServiceUnavailable, map[string]any{"error": "tool_registry_unavailable"})
			return false
		}
		if !s.admin.Tools.Has(name) {
			writeJSON(writer, http.StatusBadRequest, map[string]any{"error": "tool_not_found", "name": name})
			return false
		}
	}
	if len(rule.Skills) > 0 {
		if s.admin.Skills == nil {
			writeJSON(writer, http.StatusServiceUnavailable, map[string]any{"error": "skill_registry_unavailable"})
			return false
		}
		available := make(map[string]struct{})
		for _, current := range s.admin.Skills.List() {
			available[current.Name] = struct{}{}
		}
		for _, name := range rule.Skills {
			if _, ok := available[name]; !ok {
				writeJSON(writer, http.StatusBadRequest, map[string]any{"error": "skill_not_found", "name": name})
				return false
			}
		}
	}
	if len(rule.KnowledgeBases) > 0 {
		if s.admin.Knowledge == nil {
			writeJSON(writer, http.StatusServiceUnavailable, map[string]any{"error": "knowledge_store_unavailable"})
			return false
		}
		available := make(map[string]struct{})
		for _, name := range s.admin.Knowledge.Collections() {
			available[name] = struct{}{}
		}
		for _, name := range rule.KnowledgeBases {
			if _, ok := available[name]; !ok {
				writeJSON(writer, http.StatusBadRequest, map[string]any{"error": "knowledge_base_not_found", "name": name})
				return false
			}
		}
	}
	if len(rule.MCPServers) > 0 {
		if s.admin.MCP == nil {
			writeJSON(writer, http.StatusServiceUnavailable, map[string]any{"error": "mcp_runtime_unavailable"})
			return false
		}
		available := make(map[string]struct{})
		for _, current := range s.admin.MCP.List() {
			available[current.Name] = struct{}{}
		}
		for _, name := range rule.MCPServers {
			if _, ok := available[name]; !ok {
				writeJSON(writer, http.StatusBadRequest, map[string]any{"error": "mcp_server_not_found", "name": name})
				return false
			}
		}
	}
	return true
}

func (s *Server) personaReference(name string) string {
	if s.admin.Bindings != nil {
		for _, rule := range s.admin.Bindings.List() {
			if rule.Persona == name {
				return "binding:" + rule.Name
			}
		}
	}
	if s.admin.Sessions != nil {
		for _, current := range s.admin.Sessions.List() {
			if current.Persona == name {
				return "session:" + current.ID
			}
		}
	}
	return ""
}

func (s *Server) writeRegistryError(writer http.ResponseWriter, err, persistenceErr error) {
	statusCode := http.StatusBadRequest
	if persistenceErr != nil {
		statusCode = http.StatusInternalServerError
	}
	writeJSON(writer, statusCode, map[string]any{"error": err.Error()})
}

func findBinding(registry *binding.Registry, name string) (binding.Rule, bool) {
	for _, rule := range registry.List() {
		if rule.Name == strings.TrimSpace(name) {
			return rule, true
		}
	}
	return binding.Rule{}, false
}

func bindingByName(registry *binding.Registry, name string) binding.Rule {
	rule, _ := findBinding(registry, strings.TrimSpace(name))
	return rule
}

func (s *Server) knowledge(writer http.ResponseWriter, request *http.Request) {
	if !allowReadMethod(writer, request) {
		return
	}
	if s.admin.Knowledge == nil {
		writeJSON(writer, http.StatusOK, map[string]any{
			"count":   0,
			"results": []knowledge.Hit{},
		})
		return
	}
	query := strings.TrimSpace(request.URL.Query().Get("q"))
	results := []knowledge.Hit{}
	if query != "" {
		results = s.admin.Knowledge.Search(query, 20)
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"count":   s.admin.Knowledge.Count(),
		"results": results,
	})
}

func (s *Server) configFiles(writer http.ResponseWriter, request *http.Request) {
	if !allowReadMethod(writer, request) {
		return
	}
	type fileInfo struct {
		Name  string `json:"name"`
		Path  string `json:"path"`
		State string `json:"state"`
		Error string `json:"error,omitempty"`
	}
	files := make([]fileInfo, 0, 4)
	appendFile := func(name, path string, persistenceErr error) {
		state := "disabled"
		if path != "" {
			state = "ready"
		}
		current := fileInfo{Name: name, Path: path, State: state}
		if persistenceErr != nil {
			current.State = "error"
			current.Error = persistenceErr.Error()
		}
		files = append(files, current)
	}
	if s.admin.Personas != nil {
		appendFile("personas", s.admin.Personas.Path(), s.admin.Personas.PersistenceError())
	}
	if s.admin.Bindings != nil {
		appendFile("chat_bindings", s.admin.Bindings.Path(), s.admin.Bindings.PersistenceError())
	}
	if s.admin.Sessions != nil {
		appendFile("sessions", s.admin.Sessions.Path(), s.admin.Sessions.PersistenceError())
	}
	if s.admin.Cron != nil {
		appendFile("cron", s.admin.Cron.Path(), s.admin.Cron.PersistenceError())
	}
	writeJSON(writer, http.StatusOK, files)
}

func (s *Server) sessionSettings(writer http.ResponseWriter, request *http.Request, encodedID string) {
	if s.admin.Sessions == nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]any{"error": "session_store_unavailable"})
		return
	}
	sessionID, err := url.PathUnescape(encodedID)
	if err != nil || strings.TrimSpace(sessionID) == "" {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": "invalid_session_id"})
		return
	}
	switch request.Method {
	case http.MethodGet, http.MethodHead:
		writeJSON(writer, http.StatusOK, map[string]any{
			"session_id": sessionID,
			"settings":   s.admin.Sessions.Settings(sessionID),
		})
	case http.MethodPut:
		request.Body = http.MaxBytesReader(writer, request.Body, 8<<10)
		var body struct {
			Persona  *string `json:"persona"`
			Provider *string `json:"provider"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			writeJSON(writer, http.StatusBadRequest, map[string]any{"error": "invalid_json"})
			return
		}
		if body.Persona == nil && body.Provider == nil {
			writeJSON(writer, http.StatusBadRequest, map[string]any{"error": "settings_required"})
			return
		}
		if body.Persona != nil {
			name := strings.TrimSpace(*body.Persona)
			if name != "" {
				if s.admin.Personas == nil {
					writeJSON(writer, http.StatusServiceUnavailable, map[string]any{"error": "persona_registry_unavailable"})
					return
				}
				if _, ok := s.admin.Personas.Get(name); !ok {
					writeJSON(writer, http.StatusBadRequest, map[string]any{"error": "persona_not_found"})
					return
				}
			}
			*body.Persona = name
		}
		if body.Provider != nil {
			name := strings.TrimSpace(*body.Provider)
			if name != "" {
				if s.admin.Providers == nil {
					writeJSON(writer, http.StatusServiceUnavailable, map[string]any{"error": "provider_registry_unavailable"})
					return
				}
				if !s.admin.Providers.Has(name) {
					writeJSON(writer, http.StatusBadRequest, map[string]any{"error": "provider_not_found"})
					return
				}
			}
			*body.Provider = name
		}
		settings := s.admin.Sessions.UpdateSettings(sessionID, body.Persona, body.Provider)
		writeJSON(writer, http.StatusOK, map[string]any{
			"session_id": sessionID,
			"settings":   settings,
		})
	default:
		writer.Header().Set("Allow", "GET, HEAD, PUT")
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]any{"error": "method_not_allowed"})
	}
}

func (s *Server) callAction(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]any{"error": "method_not_allowed"})
		return
	}
	if s.admin.Actions == nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]any{"error": "platform_actions_unavailable"})
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, 1<<20)
	var body struct {
		SelfID string         `json:"self_id"`
		Action string         `json:"action"`
		Params map[string]any `json:"params"`
	}
	decoder := json.NewDecoder(request.Body)
	decoder.UseNumber()
	if err := decoder.Decode(&body); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": "invalid_json"})
		return
	}
	body.Action = strings.TrimSpace(body.Action)
	body.SelfID = strings.TrimSpace(body.SelfID)
	if body.Action == "" {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": "action_required"})
		return
	}
	if body.Params == nil {
		body.Params = map[string]any{}
	}

	var (
		data any
		err  error
	)
	if body.SelfID != "" {
		if routed, ok := s.admin.Actions.(AccountActionRuntime); ok {
			data, err = routed.CallFor(request.Context(), body.SelfID, body.Action, body.Params)
		} else {
			data, err = s.admin.Actions.Call(request.Context(), body.Action, body.Params)
		}
	} else {
		data, err = s.admin.Actions.Call(request.Context(), body.Action, body.Params)
	}
	if err != nil {
		var actionErr *coreonebot.ActionError
		if errors.As(err, &actionErr) {
			writeJSON(writer, http.StatusBadGateway, map[string]any{
				"error":   "onebot_action_failed",
				"action":  actionErr.Action,
				"status":  actionErr.Status,
				"retcode": actionErr.RetCode,
				"message": actionErr.Message,
				"wording": actionErr.Wording,
			})
			return
		}
		writeJSON(writer, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": data})
}

func (s *Server) refreshMCP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]any{"error": "method_not_allowed"})
		return
	}
	if s.admin.MCP == nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]any{"error": "mcp_unavailable"})
		return
	}
	err := s.admin.MCP.Refresh(request.Context())
	result := map[string]any{"servers": s.admin.MCP.List()}
	if err != nil {
		result["error"] = err.Error()
		writeJSON(writer, http.StatusBadGateway, result)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (s *Server) refreshPlugins(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]any{"error": "method_not_allowed"})
		return
	}
	if s.admin.PluginRuntime == nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]any{"error": "plugin_runtime_unavailable"})
		return
	}
	if err := s.admin.PluginRuntime.Reload(request.Context()); err != nil {
		writeJSON(writer, http.StatusBadGateway, map[string]any{
			"error":   err.Error(),
			"plugins": s.admin.PluginRuntime.List(),
		})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"plugins": s.admin.PluginRuntime.List()})
}

func (s *Server) refreshSkills(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]any{"error": "method_not_allowed"})
		return
	}
	if s.admin.Skills == nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]any{"error": "skills_unavailable"})
		return
	}
	if err := s.admin.Skills.Reload(request.Context()); err != nil {
		writeJSON(writer, http.StatusBadGateway, map[string]any{
			"error":  err.Error(),
			"skills": s.admin.Skills.List(),
		})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"skills": s.admin.Skills.List()})
}

func (s *Server) refreshSubagents(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]any{"error": "method_not_allowed"})
		return
	}
	if s.admin.Subagents == nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]any{"error": "subagents_unavailable"})
		return
	}
	if err := s.admin.Subagents.Reload(request.Context()); err != nil {
		writeJSON(writer, http.StatusBadGateway, map[string]any{
			"error":     err.Error(),
			"subagents": s.admin.Subagents.List(),
		})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"subagents": s.admin.Subagents.List()})
}

func (s *Server) setDefaultPersona(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPut {
		writer.Header().Set("Allow", http.MethodPut)
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]any{"error": "method_not_allowed"})
		return
	}
	if s.admin.Personas == nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]any{"error": "persona_registry_unavailable"})
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, 4<<10)
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": "invalid_json"})
		return
	}
	if err := s.admin.Personas.SetDefault(strings.TrimSpace(body.Name)); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"default": strings.TrimSpace(body.Name)})
}

func (s *Server) setDefaultProvider(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPut {
		writer.Header().Set("Allow", http.MethodPut)
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]any{"error": "method_not_allowed"})
		return
	}
	if s.admin.Providers == nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]any{"error": "provider_registry_unavailable"})
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, 4<<10)
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": "invalid_json"})
		return
	}
	if err := s.admin.Providers.SetDefault(strings.TrimSpace(body.Name)); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"default": strings.TrimSpace(body.Name)})
}

func (s *Server) jobs(writer http.ResponseWriter, request *http.Request) {
	if s.admin.Cron == nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]any{"error": "cron_unavailable"})
		return
	}
	switch request.Method {
	case http.MethodGet, http.MethodHead:
		writeJSON(writer, http.StatusOK, s.admin.Cron.List())
	case http.MethodPost:
		requestBody := struct {
			ID              string `json:"id"`
			Name            string `json:"name"`
			ChatType        string `json:"chat_type"`
			ChatID          string `json:"chat_id"`
			SelfID          string `json:"self_id"`
			Text            string `json:"text"`
			IntervalSeconds int64  `json:"interval_seconds"`
		}{}
		request.Body = http.MaxBytesReader(writer, request.Body, 64<<10)
		if err := json.NewDecoder(request.Body).Decode(&requestBody); err != nil {
			writeJSON(writer, http.StatusBadRequest, map[string]any{"error": "invalid_json"})
			return
		}
		job := cron.Job{
			ID:       requestBody.ID,
			Name:     requestBody.Name,
			ChatType: requestBody.ChatType,
			ChatID:   requestBody.ChatID,
			SelfID:   requestBody.SelfID,
			Text:     requestBody.Text,
			Interval: time.Duration(requestBody.IntervalSeconds) * time.Second,
		}
		if err := s.admin.Cron.Add(job); err != nil {
			writeJSON(writer, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(writer, http.StatusCreated, job)
	default:
		writer.Header().Set("Allow", "GET, HEAD, POST")
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]any{"error": "method_not_allowed"})
	}
}

func (s *Server) deleteJob(writer http.ResponseWriter, request *http.Request, id string) {
	if request.Method != http.MethodDelete {
		writer.Header().Set("Allow", http.MethodDelete)
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]any{"error": "method_not_allowed"})
		return
	}
	if s.admin.Cron == nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]any{"error": "cron_unavailable"})
		return
	}
	id, err := url.PathUnescape(id)
	if err != nil || id == "" {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": "invalid_job_id"})
		return
	}
	removed, removeErr := s.admin.Cron.RemoveWithError(id)
	if removeErr != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"error": removeErr.Error()})
		return
	}
	if !removed {
		writeJSON(writer, http.StatusNotFound, map[string]any{"error": "job_not_found"})
		return
	}
	writeJSON(writer, http.StatusNoContent, nil)
}

func (s *Server) deleteSession(writer http.ResponseWriter, request *http.Request, encodedID string) {
	if request.Method != http.MethodDelete {
		writer.Header().Set("Allow", http.MethodDelete)
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]any{"error": "method_not_allowed"})
		return
	}
	if s.admin.Sessions == nil {
		writeJSON(writer, http.StatusNotFound, map[string]any{"error": "session_store_unavailable"})
		return
	}
	sessionID, err := url.PathUnescape(encodedID)
	if err != nil || sessionID == "" {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": "invalid_session_id"})
		return
	}
	if !s.admin.Sessions.Delete(sessionID) {
		writeJSON(writer, http.StatusNotFound, map[string]any{"error": "session_not_found"})
		return
	}
	writeJSON(writer, http.StatusNoContent, nil)
}

func (s *Server) authorizeAdmin(writer http.ResponseWriter, request *http.Request) bool {
	provided := bearerToken(request)
	staticToken := strings.TrimSpace(s.admin.Token)
	if staticToken != "" &&
		subtle.ConstantTimeCompare([]byte(provided), []byte(staticToken)) == 1 {
		return true
	}
	writer.Header().Set("WWW-Authenticate", `Bearer realm="cinlan-qq-bot-admin"`)
	writeJSON(writer, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
	return false
}

func bearerToken(request *http.Request) string {
	header := strings.TrimSpace(request.Header.Get("Authorization"))
	const prefix = "Bearer "
	if len(header) >= len(prefix) && strings.EqualFold(header[:len(prefix)], prefix) {
		return strings.TrimSpace(header[len(prefix):])
	}
	return strings.TrimSpace(request.Header.Get("X-Admin-Token"))
}

func allowReadMethod(writer http.ResponseWriter, request *http.Request) bool {
	if request.Method == http.MethodGet || request.Method == http.MethodHead {
		return true
	}
	writer.Header().Set("Allow", "GET, HEAD")
	writeJSON(writer, http.StatusMethodNotAllowed, map[string]any{"error": "method_not_allowed"})
	return false
}

func decodeJSONBody(writer http.ResponseWriter, request *http.Request, maxBytes int64, target any) bool {
	request.Body = http.MaxBytesReader(writer, request.Body, maxBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeJSON(writer, http.StatusRequestEntityTooLarge, map[string]any{"error": "request_too_large"})
			return false
		}
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": "invalid_json"})
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": "invalid_json"})
		return false
	}
	return true
}

func writeJSON(writer http.ResponseWriter, statusCode int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.WriteHeader(statusCode)
	if statusCode == http.StatusNoContent {
		return
	}
	_ = json.NewEncoder(writer).Encode(value)
}
