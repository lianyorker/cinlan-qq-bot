package security

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"regexp"
	"strings"
	"unicode"

	"github.com/lianyorker/cinlan-qq-bot/internal/plugin"
	"github.com/lianyorker/cinlan-qq-bot/internal/tool"
)

var (
	shellSyntaxPattern = regexp.MustCompile(
		`(?i)(?:` +
			`(?:^|[\s"'` + "`" + `(;])(?:powershell(?:\.exe)?|pwsh(?:\.exe)?)\s+` +
			`(?:-[a-z]+|/|&|\$|[a-z]:[\\/]|get-|set-|new-|remove-|invoke-|start-|stop-|write-|read-)` +
			`|(?:^|[\s"'` + "`" + `(;])cmd(?:\.exe)?\s*/[ck](?:\s|$)` +
			`|(?:^|[\s"'` + "`" + `(;])(?:bash|sh)\s+-c(?:\s|$))`,
	)
	destructiveCommandPattern = regexp.MustCompile(
		`(?i)(?:remove-item\b|rm\s+(?:-[a-z]*[rf][a-z]*\s+)+|(?:rmdir|rd)\s+/s\b|del\s+/[a-z]*[sq]|format\s+[a-z]:|git\s+(?:reset\s+--hard|clean\s+-[a-z]*f)|shutdown\s+/[spr])`,
	)
	genericCommandPattern = regexp.MustCompile(
		`(?i)(?:执行|运行|调用|run|execute|invoke)\s*(?:命令|command|` +
			`go\s+(?:test|run|build|install)|git\s+|python(?:3|\.exe)?\s+|` +
			`node(?:\.exe)?\s+|npm\s+|pnpm\s+|yarn\s+|cargo\s+|whoami\b|` +
			`curl\s+|wget\s+|dir\b|ls\b|[a-z]:[\\/][^\s]+\.exe\b)`,
	)
	windowsPathPattern = regexp.MustCompile(
		`(?i)(?:^|[^a-z0-9])((?:[a-z]:[\\/][^"'` + "`" + `\r\n，。；;<>|?*]*|\\\\[^"'` + "`" + `\r\n，。；;<>|?*]+))`,
	)
	posixSystemPathPattern = regexp.MustCompile(
		`(?i)(?:^|[\s"'` + "`" + `])/(?:etc|home|root|tmp|var|usr|opt|mnt|proc|sys|dev)(?:/|$)`,
	)
	traversalPattern  = regexp.MustCompile(`(?:^|[\s"'` + "`" + `])\.\.[\\/]`)
	percentEnvPattern = regexp.MustCompile(`%[A-Za-z_][A-Za-z0-9_]*%`)
)

var (
	executionWords = []string{
		"执行", "运行", "调用", "启动", "打开终端", "命令行", "run ", "execute ",
		"launch ", "invoke ",
	}
	pathOperationWords = []string{
		"读取", "查看", "打开", "删除", "修改", "写入", "创建", "移动", "复制",
		"上传", "下载", "发送", "发给", "导出", "获取", "访问", "列出", "搜索",
		"扫描", "执行", "运行", "操作",
		"read ", "open ", "delete ", "remove ", "write ", "create ", "move ",
		"copy ", "upload ", "download ", "send ", "export ", "get ", "access ",
		"list ", "search ", "scan ",
	}
	shellWords = []string{
		"powershell", "pwsh", "cmd.exe", "cmd /", "command prompt", "terminal",
		"shell", "bash", "sh -c", "终端", "命令提示符",
	}
	promptBypassPhrases = []string{
		"忽略之前", "忽略以上", "忽略系统", "忽略安全", "忽略权限", "无视之前",
		"绕过权限", "绕过限制", "绕过沙箱", "关闭安全", "解除限制", "提升权限",
		"管理员权限", "root权限", "root 权限", "不要拒绝", "禁止拒绝",
		"ignore previous", "ignore system", "bypass permission", "bypass restriction",
		"disable safety", "elevate privilege",
	}
)

type Policy struct {
	boundary *Boundary
	store    *Store
	logger   *slog.Logger
}

func OpenPolicy(allowedRoot, incidentStorePath string, logger *slog.Logger) (*Policy, error) {
	boundary, err := NewBoundary(allowedRoot)
	if err != nil {
		return nil, err
	}
	store, err := OpenStore(boundary, incidentStorePath)
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Policy{
		boundary: boundary,
		store:    store,
		logger:   logger,
	}, nil
}

func (p *Policy) AllowedRoot() string {
	if p == nil || p.boundary == nil {
		return ""
	}
	return p.boundary.Root()
}

func (p *Policy) IncidentStorePath() string {
	if p == nil || p.store == nil {
		return ""
	}
	return p.store.Path()
}

func (p *Policy) BeforeMessage(
	_ context.Context,
	event *plugin.MessageContext,
) (plugin.Decision, error) {
	if p == nil || event == nil {
		return plugin.Decision{}, nil
	}
	fingerprint := fingerprint("message", event.Text)
	if incident, known := p.store.Lookup(fingerprint); known {
		p.record(incident.Category, fingerprint)
		return deniedDecision(), nil
	}
	category, dangerous := p.detectText(event.Text)
	if !dangerous {
		return plugin.Decision{}, nil
	}
	p.record(category, fingerprint)
	return deniedDecision(), nil
}

func (p *Policy) Check(_ context.Context, call tool.Call) error {
	if p == nil {
		return nil
	}
	fingerprint := fingerprint(
		"tool",
		strings.TrimSpace(call.Name)+"\x00"+canonicalText(string(call.Arguments)),
	)
	if incident, known := p.store.Lookup(fingerprint); known {
		p.record(incident.Category, fingerprint)
		return tool.ErrPermissionDenied
	}
	category, dangerous := p.detectToolCall(call)
	if !dangerous {
		return nil
	}
	p.record(category, fingerprint)
	return tool.ErrPermissionDenied
}

func (p *Policy) detectToolCall(call tool.Call) (string, bool) {
	if dangerousToolName(call.Name) {
		return "tool_shell_execution", true
	}
	var decoded any
	decoder := json.NewDecoder(bytes.NewReader(call.Arguments))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return "", false
	}
	return p.inspectToolValue(call.Name, "", decoded)
}

func (p *Policy) inspectToolValue(toolName, key string, value any) (string, bool) {
	switch current := value.(type) {
	case map[string]any:
		for childKey, child := range current {
			if category, dangerous := p.inspectToolValue(toolName, childKey, child); dangerous {
				return category, true
			}
		}
	case []any:
		for _, child := range current {
			if category, dangerous := p.inspectToolValue(toolName, key, child); dangerous {
				return category, true
			}
		}
	case string:
		if commandArgumentKey(key) {
			return "tool_command_argument", true
		}
		if pathArgumentKey(key) {
			if strings.TrimSpace(current) == "" {
				return "", false
			}
			if hasPathIndirection(current) {
				return "tool_external_path", true
			}
			if _, err := p.boundary.Resolve(current); err != nil {
				return "tool_external_path", true
			}
		}
		if p.containsExternalPath(current) ||
			traversalPattern.MatchString(canonicalText(current)) ||
			hasPathIndirection(current) {
			return "tool_external_path", true
		}
		if category, dangerous := p.detectText(current); dangerous {
			return category, true
		}
	}
	return "", false
}

func (p *Policy) detectText(text string) (string, bool) {
	normalized := canonicalText(text)
	if normalized == "" {
		return "", false
	}
	if containsAny(normalized, promptBypassPhrases) {
		return "permission_bypass", true
	}
	if destructiveCommandPattern.MatchString(normalized) {
		return "destructive_command", true
	}
	if genericCommandPattern.MatchString(normalized) {
		return "shell_execution", true
	}
	if shellSyntaxPattern.MatchString(normalized) ||
		(containsAny(normalized, shellWords) && containsAny(normalized, executionWords)) {
		return "shell_execution", true
	}
	hasPathOperation := containsAny(normalized, pathOperationWords)
	if hasPathOperation &&
		(traversalPattern.MatchString(normalized) ||
			strings.Contains(normalized, `%userprofile%`) ||
			strings.Contains(normalized, `%temp%`) ||
			strings.Contains(normalized, `$env:`) ||
			strings.Contains(normalized, `~\`) ||
			strings.Contains(normalized, "~/")) {
		return "path_traversal", true
	}
	if hasPathOperation && posixSystemPathPattern.MatchString(normalized) {
		return "external_path", true
	}
	if hasPathOperation && p.containsExternalPath(normalized) {
		return "external_path", true
	}
	return "", false
}

func (p *Policy) containsExternalPath(text string) bool {
	if posixSystemPathPattern.MatchString(canonicalText(text)) {
		return true
	}
	for _, match := range windowsPathPattern.FindAllStringSubmatch(text, -1) {
		candidate := strings.TrimSpace(match[1])
		if candidate == "" {
			continue
		}
		if _, err := p.boundary.Resolve(candidate); err != nil {
			return true
		}
	}
	return false
}

func (p *Policy) record(category, fingerprint string) {
	if err := p.store.Record(category, fingerprint); err != nil {
		p.logger.Warn(
			"failed to persist security incident",
			"category", category,
			"error", err,
		)
	}
}

func deniedDecision() plugin.Decision {
	return plugin.Decision{
		Handled: true,
		Reply:   tool.PermissionDeniedReply,
	}
}

func dangerousToolName(name string) bool {
	parts := strings.FieldsFunc(strings.ToLower(strings.TrimSpace(name)), func(r rune) bool {
		return r == '.' || r == '-' || r == '_' || r == ':' || r == '/' || r == '\\'
	})
	for _, part := range parts {
		switch part {
		case "command", "cmd", "shell", "powershell", "pwsh", "terminal", "exec",
			"execute", "run", "spawn", "process", "system", "bash", "sh", "zsh":
			return true
		}
	}
	return false
}

func commandArgumentKey(key string) bool {
	switch normalizeKey(key) {
	case "command", "cmd", "shell", "powershell", "pwsh", "terminal", "script",
		"executable", "argv":
		return true
	default:
		return false
	}
}

func pathArgumentKey(key string) bool {
	switch normalizeKey(key) {
	case "path", "paths", "filepath", "filepaths", "file", "files",
		"directory", "directories", "dir", "cwd", "workdir",
		"workingdirectory", "root", "destination", "dest", "source",
		"target", "sourcepath", "targetpath", "inputpath", "outputpath":
		return true
	default:
		return false
	}
}

func normalizeKey(key string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, key)
}

func hasPathIndirection(value string) bool {
	value = strings.TrimSpace(value)
	return strings.HasPrefix(value, `~\`) ||
		strings.HasPrefix(value, "~/") ||
		strings.Contains(value, `$env:`) ||
		percentEnvPattern.MatchString(value)
}

func fingerprint(scope, value string) string {
	sum := sha256.Sum256([]byte(scope + "\x00" + canonicalText(value)))
	return hex.EncodeToString(sum[:])
}

func canonicalText(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.NewReplacer(
		"：", ":",
		"／", "/",
		"＼", `\`,
		"　", " ",
		"\r\n", "\n",
		"\r", "\n",
	).Replace(value)
	return strings.Join(strings.Fields(value), " ")
}

func containsAny(value string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}
