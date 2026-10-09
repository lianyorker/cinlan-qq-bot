package onebot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	coreonebot "github.com/lianyorker/cinlan-qq-bot/internal/onebot"
	"github.com/lianyorker/cinlan-qq-bot/internal/platform"
)

const maxAccountsConfigBytes = 512 << 10

var environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type AccountConfig struct {
	Name        string
	SelfID      string
	URL         string
	Transport   string
	ListenAddr  string
	Path        string
	AccessToken string
	Enabled     bool
}

type rawAccountsFile struct {
	DefaultAccount string       `json:"default_account"`
	Accounts       []rawAccount `json:"accounts"`
}

type rawAccount struct {
	Name           string `json:"name"`
	SelfID         string `json:"self_id"`
	URL            string `json:"url"`
	Transport      string `json:"transport"`
	ListenAddr     string `json:"listen_addr"`
	Path           string `json:"path"`
	AccessTokenEnv string `json:"access_token_env"`
	Enabled        *bool  `json:"enabled"`
}

type AccountInfo struct {
	Name      string `json:"name"`
	SelfID    string `json:"self_id"`
	Transport string `json:"transport"`
	Connected bool   `json:"connected"`
	Default   bool   `json:"default"`
}

type accountRuntime struct {
	config  AccountConfig
	adapter *Adapter
}

// MultiAdapter merges multiple NapCat/OneBot connections into one platform
// adapter and routes outbound operations by QQ self_id or configured name.
type MultiAdapter struct {
	logger      *slog.Logger
	accounts    map[string]accountRuntime
	route       map[string]string
	defaultName string
	events      chan platform.Event
}

func LoadAccountsFile(path string) ([]AccountConfig, string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, "", errors.New("OneBot accounts config path is empty")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, "", fmt.Errorf("open OneBot accounts config: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxAccountsConfigBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("read OneBot accounts config: %w", err)
	}
	if len(data) > maxAccountsConfigBytes {
		return nil, "", fmt.Errorf("OneBot accounts config exceeds %d bytes", maxAccountsConfigBytes)
	}
	var decoded rawAccountsFile
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, "", fmt.Errorf("decode OneBot accounts config: %w", err)
	}
	if len(decoded.Accounts) == 0 {
		return nil, "", errors.New("OneBot accounts config contains no accounts")
	}
	if len(decoded.Accounts) > 32 {
		return nil, "", errors.New("OneBot accounts config contains more than 32 accounts")
	}

	configs := make([]AccountConfig, 0, len(decoded.Accounts))
	names := make(map[string]struct{}, len(decoded.Accounts))
	selfIDs := make(map[string]struct{}, len(decoded.Accounts))
	for _, raw := range decoded.Accounts {
		config, err := resolveAccount(raw)
		if err != nil {
			return nil, "", err
		}
		if _, exists := names[config.Name]; exists {
			return nil, "", fmt.Errorf("OneBot account name %q is duplicated", config.Name)
		}
		names[config.Name] = struct{}{}
		if config.Enabled {
			if _, exists := selfIDs[config.SelfID]; exists {
				return nil, "", fmt.Errorf("OneBot self_id %q is duplicated", config.SelfID)
			}
			selfIDs[config.SelfID] = struct{}{}
		}
		configs = append(configs, config)
	}
	sort.Slice(configs, func(i, j int) bool { return configs[i].Name < configs[j].Name })

	defaultAccount := strings.TrimSpace(decoded.DefaultAccount)
	if defaultAccount != "" {
		found := false
		for _, config := range configs {
			if config.Enabled && (config.Name == defaultAccount || config.SelfID == defaultAccount) {
				defaultAccount = config.Name
				found = true
				break
			}
		}
		if !found {
			return nil, "", fmt.Errorf("default OneBot account %q is not enabled", decoded.DefaultAccount)
		}
	}
	return configs, defaultAccount, nil
}

func OpenMulti(path string, actionTimeout time.Duration, logger *slog.Logger) (*MultiAdapter, error) {
	configs, defaultName, err := LoadAccountsFile(path)
	if err != nil {
		return nil, err
	}
	return NewMultiAdapter(configs, defaultName, actionTimeout, logger)
}

func NewMultiAdapter(configs []AccountConfig, defaultName string, actionTimeout time.Duration, logger *slog.Logger) (*MultiAdapter, error) {
	if logger == nil {
		logger = slog.Default()
	}
	multi := &MultiAdapter{
		logger:      logger,
		accounts:    make(map[string]accountRuntime),
		route:       make(map[string]string),
		defaultName: strings.TrimSpace(defaultName),
		events:      make(chan platform.Event, 512),
	}
	for _, config := range configs {
		if !config.Enabled {
			continue
		}
		if config.Name == "" || config.SelfID == "" {
			return nil, errors.New("enabled OneBot account requires name and self_id")
		}
		if _, exists := multi.accounts[config.Name]; exists {
			return nil, fmt.Errorf("OneBot account %q is duplicated", config.Name)
		}
		client := coreonebot.NewClient(coreonebot.ClientConfig{
			URL:           config.URL,
			AccessToken:   config.AccessToken,
			ActionTimeout: actionTimeout,
			Transport:     config.Transport,
			ListenAddr:    config.ListenAddr,
			Path:          config.Path,
		}, logger.With("onebot_account", config.Name, "self_id", config.SelfID))
		multi.accounts[config.Name] = accountRuntime{
			config:  config,
			adapter: NewAdapter(client, logger.With("onebot_account", config.Name)),
		}
		multi.route[config.Name] = config.Name
		multi.route[config.SelfID] = config.Name
	}
	if len(multi.accounts) == 0 {
		return nil, errors.New("OneBot accounts config has no enabled account")
	}
	if multi.defaultName != "" {
		name, ok := multi.route[multi.defaultName]
		if !ok {
			return nil, fmt.Errorf("default OneBot account %q is unavailable", multi.defaultName)
		}
		multi.defaultName = name
	} else if len(multi.accounts) == 1 {
		for name := range multi.accounts {
			multi.defaultName = name
		}
	}
	return multi, nil
}

func (m *MultiAdapter) Name() string {
	return platform.PlatformQQOneBot
}

// SetAutoAcceptFriend enables or disables auto-accepting friend requests on
// every underlying account adapter.
func (m *MultiAdapter) SetAutoAcceptFriend(enabled bool) {
	for _, account := range m.accounts {
		account.adapter.AutoAcceptFriend = enabled
	}
}

func (m *MultiAdapter) Connected() bool {
	for _, account := range m.accounts {
		if account.adapter.Connected() {
			return true
		}
	}
	return false
}

func (m *MultiAdapter) Events() <-chan platform.Event {
	return m.events
}

func (m *MultiAdapter) Accounts() []AccountInfo {
	names := make([]string, 0, len(m.accounts))
	for name := range m.accounts {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]AccountInfo, 0, len(names))
	for _, name := range names {
		account := m.accounts[name]
		result = append(result, AccountInfo{
			Name:      account.config.Name,
			SelfID:    account.config.SelfID,
			Transport: normalizeAccountTransport(account.config.Transport),
			Connected: account.adapter.Connected(),
			Default:   name == m.defaultName,
		})
	}
	return result
}

func (m *MultiAdapter) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer close(m.events)

	errCh := make(chan error, len(m.accounts))
	var waitGroup sync.WaitGroup
	for name, current := range m.accounts {
		name := name
		current := current
		waitGroup.Add(2)
		go func() {
			defer waitGroup.Done()
			if err := current.adapter.Run(ctx); err != nil && ctx.Err() == nil {
				select {
				case errCh <- fmt.Errorf("OneBot account %s: %w", name, err):
				case <-ctx.Done():
				}
			}
		}()
		go func() {
			defer waitGroup.Done()
			for event := range current.adapter.Events() {
				if event.SelfID == "" {
					event.SelfID = current.config.SelfID
				} else if event.SelfID != current.config.SelfID {
					m.logger.Warn(
						"ignored OneBot event with unexpected self_id",
						"account", current.config.Name,
						"configured_self_id", current.config.SelfID,
						"event_self_id", event.SelfID,
					)
					continue
				}
				select {
				case <-ctx.Done():
					return
				case m.events <- event:
				}
			}
		}()
	}

	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-errCh:
		cancel()
	}
	waitGroup.Wait()
	return runErr
}

func (m *MultiAdapter) Send(ctx context.Context, outbound platform.Outbound) error {
	account, err := m.selectAccount(outbound.SelfID)
	if err != nil {
		return err
	}
	return account.adapter.Send(ctx, outbound)
}

func (m *MultiAdapter) SendGroupText(ctx context.Context, groupID, replyTo, text string, quote bool) error {
	account, err := m.selectAccount("")
	if err != nil {
		return err
	}
	return account.adapter.SendGroupText(ctx, groupID, replyTo, text, quote)
}

func (m *MultiAdapter) Call(ctx context.Context, action string, params map[string]any) (any, error) {
	account, err := m.selectAccount("")
	if err != nil {
		return nil, err
	}
	return account.adapter.Call(ctx, action, params)
}

func (m *MultiAdapter) CallFor(ctx context.Context, accountID, action string, params map[string]any) (any, error) {
	account, err := m.selectAccount(accountID)
	if err != nil {
		return nil, err
	}
	return account.adapter.Call(ctx, action, params)
}

func (m *MultiAdapter) selectAccount(identifier string) (accountRuntime, error) {
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		identifier = m.defaultName
	}
	if identifier == "" {
		return accountRuntime{}, errors.New("OneBot account is required for multi-account operation")
	}
	name, ok := m.route[identifier]
	if !ok {
		return accountRuntime{}, fmt.Errorf("OneBot account %q is not configured", identifier)
	}
	account := m.accounts[name]
	if !account.adapter.Connected() {
		return accountRuntime{}, fmt.Errorf("OneBot account %q is not connected", identifier)
	}
	return account, nil
}

func resolveAccount(raw rawAccount) (AccountConfig, error) {
	name := strings.TrimSpace(raw.Name)
	selfID := strings.TrimSpace(raw.SelfID)
	if name == "" || selfID == "" {
		return AccountConfig{}, errors.New("OneBot account name and self_id are required")
	}
	for _, current := range selfID {
		if current < '0' || current > '9' {
			return AccountConfig{}, fmt.Errorf("OneBot account %q has invalid self_id %q", name, selfID)
		}
	}
	enabled := true
	if raw.Enabled != nil {
		enabled = *raw.Enabled
	}
	transport := normalizeAccountTransport(raw.Transport)
	if transport != coreonebot.TransportForwardWebSocket &&
		transport != coreonebot.TransportReverseWebSocket &&
		transport != coreonebot.TransportHTTPSSE &&
		transport != coreonebot.TransportReverseHTTP {
		return AccountConfig{}, fmt.Errorf("OneBot account %q has unsupported transport %q", name, raw.Transport)
	}
	usesRemoteURL := transport == coreonebot.TransportForwardWebSocket ||
		transport == coreonebot.TransportHTTPSSE ||
		transport == coreonebot.TransportReverseHTTP
	if enabled && usesRemoteURL && strings.TrimSpace(raw.URL) == "" {
		return AccountConfig{}, fmt.Errorf("OneBot account %q requires url", name)
	}
	if enabled && usesRemoteURL {
		endpoint, err := url.Parse(strings.TrimSpace(raw.URL))
		validScheme := false
		if err == nil && endpoint != nil {
			validScheme = transport == coreonebot.TransportForwardWebSocket &&
				(endpoint.Scheme == "ws" || endpoint.Scheme == "wss")
			if transport == coreonebot.TransportHTTPSSE || transport == coreonebot.TransportReverseHTTP {
				validScheme = endpoint.Scheme == "http" || endpoint.Scheme == "https"
			}
		}
		if err != nil || endpoint == nil || endpoint.Host == "" || !validScheme {
			return AccountConfig{}, fmt.Errorf("OneBot account %q url has an invalid scheme for transport %q", name, transport)
		}
		if endpoint.User != nil {
			return AccountConfig{}, fmt.Errorf("OneBot account %q url userinfo is not allowed", name)
		}
	}
	usesListener := transport == coreonebot.TransportReverseWebSocket || transport == coreonebot.TransportReverseHTTP
	if enabled && usesListener && strings.TrimSpace(raw.ListenAddr) == "" {
		return AccountConfig{}, fmt.Errorf("OneBot account %q requires listen_addr", name)
	}
	if enabled && usesListener {
		if _, _, err := net.SplitHostPort(strings.TrimSpace(raw.ListenAddr)); err != nil {
			return AccountConfig{}, fmt.Errorf("OneBot account %q listen_addr is invalid: %w", name, err)
		}
	}
	token := ""
	envName := strings.TrimSpace(raw.AccessTokenEnv)
	if enabled && envName != "" {
		if !environmentNamePattern.MatchString(envName) {
			return AccountConfig{}, fmt.Errorf("OneBot account %q has invalid access_token_env %q", name, envName)
		}
		var ok bool
		token, ok = os.LookupEnv(envName)
		if !ok || token == "" {
			return AccountConfig{}, fmt.Errorf("OneBot account %q token environment variable %q is not set", name, envName)
		}
	}
	return AccountConfig{
		Name:        name,
		SelfID:      selfID,
		URL:         strings.TrimSpace(raw.URL),
		Transport:   transport,
		ListenAddr:  strings.TrimSpace(raw.ListenAddr),
		Path:        strings.TrimSpace(raw.Path),
		AccessToken: token,
		Enabled:     enabled,
	}, nil
}

func normalizeAccountTransport(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "ws", "websocket", "forward", "forward_ws", "forward-websocket":
		return coreonebot.TransportForwardWebSocket
	case "reverse", "reverse_ws", "reverse-websocket":
		return coreonebot.TransportReverseWebSocket
	case "http", "sse", "http_sse", "http-sse":
		return coreonebot.TransportHTTPSSE
	case "reverse_http", "reverse-http", "http_client", "http-client":
		return coreonebot.TransportReverseHTTP
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}
