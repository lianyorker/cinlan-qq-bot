package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/message"
)

// maxInlineFileBytes caps how much of a chat file is inlined into the agent
// prompt — anything larger stays a bare [文件] marker.
const maxInlineFileBytes = 96 << 10

// inlineFileExts whitelists text-like file extensions safe to inline.
var inlineFileExts = map[string]bool{
	".txt": true, ".md": true, ".markdown": true, ".log": true,
	".json": true, ".yaml": true, ".yml": true, ".toml": true, ".ini": true,
	".csv": true, ".xml": true, ".html": true, ".css": true,
	".py": true, ".go": true, ".js": true, ".ts": true, ".vue": true,
	".java": true, ".c": true, ".h": true, ".cpp": true, ".rs": true,
	".sh": true, ".bat": true, ".ps1": true, ".sql": true,
	".lua": true, ".rb": true, ".php": true,
	".conf": true, ".cfg": true, ".env": true, ".gitignore": true,
}

// chainFileText downloads small text-like file attachments from a chain and
// returns their contents formatted for the agent prompt.
func (s *Service) chainFileText(
	ctx context.Context,
	chain message.Chain,
) string {
	var b strings.Builder
	for _, c := range chain {
		if c.Type != message.TypeFile {
			continue
		}
		ref := fileDataString(c.Data, "url", "file", "path", "file_path")
		if ref == "" {
			continue
		}
		name := fileDataString(c.Data, "name", "file_name", "fileName")
		if name == "" {
			if base := filepath.Base(strings.SplitN(ref, "?", 2)[0]); base != "." &&
				base != "/" && base != string(filepath.Separator) {
				name = base
			}
		}
		if !inlineFileExts[strings.ToLower(filepath.Ext(name))] {
			continue
		}
		if size := fileDataNumber(c.Data, "file_size", "size", "fileSize"); size > maxInlineFileBytes {
			continue
		}
		content, err := readFileReference(ctx, ref, maxInlineFileBytes)
		if err != nil || strings.TrimSpace(content) == "" {
			if err != nil {
				s.logger.Warn(
					"chat file inline fetch failed",
					"name", name,
					"error", err.Error(),
				)
			}
			continue
		}
		fmt.Fprintf(&b, "（文件 %s 的内容）:\n%s\n\n", name, content)
	}
	return b.String()
}

// readFileReference fetches a file from an http(s) URL or a local path and
// returns its text, limited to limit bytes.
func readFileReference(
	ctx context.Context,
	ref string,
	limit int64,
) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", errors.New("empty file reference")
	}
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		dlCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(dlCtx, http.MethodGet, ref, nil)
		if err != nil {
			return "", err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return "", fmt.Errorf("unexpected status %s", resp.Status)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, limit))
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(data)), nil
	}
	info, err := os.Stat(ref)
	if err != nil {
		return "", err
	}
	if info.Size() > limit {
		return "", fmt.Errorf("file exceeds %d bytes", limit)
	}
	data, err := os.ReadFile(ref)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func fileDataString(data map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := data[key]; ok {
			switch v := value.(type) {
			case string:
				if trimmed := strings.TrimSpace(v); trimmed != "" {
					return trimmed
				}
			case fmt.Stringer:
				if trimmed := strings.TrimSpace(v.String()); trimmed != "" {
					return trimmed
				}
			}
		}
	}
	return ""
}

func fileDataNumber(data map[string]any, keys ...string) int64 {
	for _, key := range keys {
		if value, ok := data[key]; ok {
			switch v := value.(type) {
			case float64:
				return int64(v)
			case int64:
				return v
			case int:
				return int64(v)
			case json.Number:
				if n, err := v.Int64(); err == nil {
					return n
				}
			case string:
				if n, err := strconv.ParseInt(
					strings.TrimSpace(v), 10, 64,
				); err == nil {
					return n
				}
			}
		}
	}
	return 0
}
