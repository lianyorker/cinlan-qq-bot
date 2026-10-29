package bot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/lianyorker/cinlan-qq-bot/internal/domain"
	"github.com/lianyorker/cinlan-qq-bot/internal/message"
)

const (
	maxMemeFilesPerChat = 200
	maxMemeBytes        = 20 << 20
)

// memeDirFor returns the platform-scoped per-chat meme pool directory.
// Pools saved before the platform scope existed ("group_123") are
// renamed on first touch so collected memes carry over.
func (s *Service) memeDirFor(platform, chatType, chatID string) string {
	if s.memeDir == "" {
		return ""
	}
	dir := filepath.Join(s.memeDir, platform+"_"+chatType+"_"+chatID)
	legacy := filepath.Join(s.memeDir, chatType+"_"+chatID)
	if plat := strings.TrimSpace(platform); plat != "" && legacy != dir {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			if _, err := os.Stat(legacy); err == nil {
				// Best-effort rename — if it fails the old pool stays put
				// and the chat just starts a fresh scoped pool.
				_ = os.Rename(legacy, dir)
			}
		}
	}
	return dir
}

// collectMemes saves every image in the incoming chain into the per-chat
// meme pool, then asks the vision model for a short meaning tag stored in a
// sidecar file so later queries can retrieve the right sticker.
func (s *Service) collectMemes(platform, chatType, chatID string, refs []string) {
	dir := s.memeDirFor(platform, chatType, chatID)
	if dir == "" || len(refs) == 0 {
		return
	}
	for _, ref := range refs {
		path, err := saveMemeImage(ref, dir)
		if err != nil {
			s.logger.Warn(
				"meme collect failed",
				"chat", chatID,
				"ref", truncateRef(ref),
				"error", err.Error(),
			)
			continue
		}
		if !s.describeMeme(path) {
			// Vision said this is not sticker material (chat screenshot,
			// photo, document…) — drop it so it can never be resent as
			// a 表情包. The file lives only for the tagging call.
			_ = os.Remove(path)
		}
	}
	evictOldMemes(dir, maxMemeFilesPerChat)
}

// describeMeme asks the vision model whether the image is sticker
// material and writes the emotion tag sidecar when it is. Returns
// false for screenshots/photos/documents — the caller deletes those.
// When no agent is attached the image is kept untagged (old pools
// still work without a vision path).
func (s *Service) describeMeme(path string) bool {
	if s.agent == nil {
		return true
	}
	metaPath := path + ".meta"
	if _, err := os.Stat(metaPath); err == nil {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := s.agent.Reply(ctx, domain.AgentRequest{
		RequestID: "meme-" + filepath.Base(path),
		Text: "判断这张图是不是可以反复使用的表情包/梗图。" +
			"如果是聊天截图、屏幕截图、普通照片、真人照片或自拍、露骨/色情内容、文档、界面截图等不适合当表情包的图，" +
			"只回答「跳过」两个字；如果是表情包，用不超过15个字的中文概括它的含义或情绪" +
			"（比如：嘲笑、疑问、无语、加油、谢谢、吃瓜、生气），只输出概括本身。",
		Chain: message.Chain{message.Attachment(
			message.TypeImage,
			map[string]any{"file_path": path},
		)},
	})
	if err != nil || strings.TrimSpace(resp.Reply) == "" {
		// Tagging unavailable — keep the image rather than starving the pool.
		return true
	}
	tag := strings.TrimSpace(resp.Reply)
	if strings.Contains(tag, "跳过") || strings.Contains(tag, "截图") {
		return false
	}
	if len([]rune(tag)) > 40 {
		tag = string([]rune(tag)[:40])
	}
	_ = os.WriteFile(metaPath, []byte(tag), 0o600)
	return true
}

// pickMeme returns a stored meme matching query, or a random one when no
// query is given or nothing matches. Group draws mix every group-sourced
// pool on the platform but never touch private pools — a picture collected
// in a private chat must not resurface in a group. Private draws see the
// full platform union: the recipient is the same person the images came
// from, so nothing leaks outward. The vision gate at collect time keeps
// non-meme junk out of the pool entirely. chatID is kept in the signature
// for callers but no longer scopes the draw.
func (s *Service) pickMeme(platform, chatType, chatID, query string) (string, error) {
	if s.memeDir == "" {
		return "", errors.New("meme pool disabled")
	}
	poolDirs, err := os.ReadDir(s.memeDir)
	if err != nil {
		return "", errors.New("还没偷到表情包，多发点图我存一下")
	}
	type candidate struct {
		name  string
		score int
	}
	files := make([]candidate, 0, 128)
	prefix := platform + "_"
	groupPrefix := platform + "_group_"
	for _, pd := range poolDirs {
		if !pd.IsDir() || !strings.HasPrefix(pd.Name(), prefix) {
			continue
		}
		// Group targets only mix group-sourced pools; a private pool
		// (or an unscoped legacy dir) stays out of group chat.
		if chatType == "group" && !strings.HasPrefix(pd.Name(), groupPrefix) {
			continue
		}
		dir := filepath.Join(s.memeDir, pd.Name())
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || strings.HasSuffix(e.Name(), ".meta") {
				continue
			}
			score := 0
			if query != "" {
				if meta, err := os.ReadFile(
					filepath.Join(dir, e.Name()+".meta"),
				); err == nil {
					for _, word := range strings.FieldsFunc(
						query,
						func(r rune) bool {
							return unicode.IsSpace(r) || unicode.IsPunct(r)
						},
					) {
						if word != "" && strings.Contains(string(meta), word) {
							score++
						}
					}
					if strings.Contains(string(meta), query) {
						score++
					}
				}
			}
			files = append(files, candidate{filepath.Join(dir, e.Name()), score})
		}
	}
	if len(files) == 0 {
		return "", errors.New("还没偷到表情包，多发点图我存一下")
	}
	best := files[rand.Intn(len(files))]
	bestScore := 0
	if query != "" {
		pool := make([]candidate, 0, len(files))
		for _, f := range files {
			if f.score > bestScore {
				bestScore = f.score
			}
		}
		if bestScore > 0 {
			for _, f := range files {
				if f.score == bestScore {
					pool = append(pool, f)
				}
			}
			best = pool[rand.Intn(len(pool))]
		}
	}
	return best.name, nil
}

// saveMemeImage downloads or copies an image into dir under a content-hash
// filename so identical memes dedupe naturally.
func saveMemeImage(ref, dir string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", errors.New("empty image reference")
	}
	var data []byte
	lower := strings.ToLower(ref)
	switch {
	case strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://"):
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, ref, nil)
		if err != nil {
			return "", err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("download http %d", resp.StatusCode)
		}
		limited, err := io.ReadAll(io.LimitReader(resp.Body, maxMemeBytes))
		if err != nil {
			return "", err
		}
		data = limited
	default:
		local := strings.TrimPrefix(ref, "file://")
		read, err := os.ReadFile(local)
		if err != nil {
			return "", err
		}
		if len(read) > maxMemeBytes {
			return "", errors.New("image too large")
		}
		data = read
	}
	if len(data) == 0 {
		return "", errors.New("empty image")
	}
	ext := ".png"
	switch http.DetectContentType(data[:min(512, len(data))]) {
	case "image/jpeg":
		ext = ".jpg"
	case "image/webp":
		ext = ".webp"
	case "image/gif":
		ext = ".gif"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:12]) + ext
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); err == nil {
		return path, nil // already pooled
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// evictOldMemes keeps the pool under cap by deleting the oldest files.
func evictOldMemes(dir string, cap int) {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) <= cap {
		return
	}
	type fileInfo struct {
		name    string
		modTime time.Time
	}
	files := make([]fileInfo, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, fileInfo{e.Name(), info.ModTime()})
	}
	// Oldest first.
	for i := 0; i < len(files)-1; i++ {
		for j := i + 1; j < len(files); j++ {
			if files[j].modTime.Before(files[i].modTime) {
				files[i], files[j] = files[j], files[i]
			}
		}
	}
	for i := 0; i < len(files)-cap; i++ {
		_ = os.Remove(filepath.Join(dir, files[i].name))
	}
}

func truncateRef(ref string) string {
	if len(ref) > 80 {
		return ref[:80] + "…"
	}
	return ref
}
