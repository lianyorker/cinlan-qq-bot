package filedelivery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/domain"
	"github.com/lianyorker/cinlan-qq-bot/internal/message"
	"github.com/lianyorker/cinlan-qq-bot/internal/platform"
	"github.com/lianyorker/cinlan-qq-bot/internal/security"
	"github.com/lianyorker/cinlan-qq-bot/internal/tool"
)

const (
	maxCatalogBytes   = 512 << 10
	maxCatalogEntries = 128
	maxFirstMessage   = 16 << 10
	deliveryDedupeTTL = 10 * time.Minute
)

var entryIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
var numericIDPattern = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)

type Sender interface {
	Send(context.Context, platform.Outbound) error
}

type actionCaller interface {
	Call(context.Context, string, map[string]any) (any, error)
}

type MarkerStore interface {
	ClaimMarker(string) (bool, error)
	ReleaseMarker(string) error
}

type Entry struct {
	ID                   string
	Name                 string
	Aliases              []string
	Description          string
	Path                 string
	DisplayName          string
	FirstDeliveryMessage string
	PrivateOnly          bool
	AllowedGroupIDs      []string
	Size                 int64
}

type rawCatalog struct {
	Version int        `json:"version"`
	Files   []rawEntry `json:"files"`
}

type rawEntry struct {
	ID                   string   `json:"id"`
	Name                 string   `json:"name"`
	Aliases              []string `json:"aliases"`
	Description          string   `json:"description"`
	Path                 string   `json:"path"`
	DisplayName          string   `json:"display_name"`
	FirstDeliveryMessage string   `json:"first_delivery_message"`
	Enabled              *bool    `json:"enabled"`
	PrivateOnly          *bool    `json:"private_only"`
	AllowedGroupIDs      []string `json:"allowed_group_ids"`
}

type DeliveryResult struct {
	Status      string `json:"status"`
	FileID      string `json:"file_id"`
	DisplayName string `json:"display_name"`
	Message     string `json:"message"`
}

type deliverArguments struct {
	FileID string `json:"file_id"`
}

type Catalog struct {
	entries  []Entry
	byLookup map[string]Entry
	maxBytes int64
	boundary *security.Boundary

	deliveryMu sync.Mutex
	delivered  map[string]time.Time
	now        func() time.Time
}

func LoadFile(path string, maxBytes int64) (*Catalog, error) {
	return loadFile(path, maxBytes, nil)
}

func LoadFileWithinRoot(path string, maxBytes int64, allowedRoot string) (*Catalog, error) {
	boundary, err := security.NewBoundary(allowedRoot)
	if err != nil {
		return nil, err
	}
	return loadFile(path, maxBytes, boundary)
}

func loadFile(path string, maxBytes int64, boundary *security.Boundary) (*Catalog, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("file catalog path is empty")
	}
	if maxBytes <= 0 {
		return nil, errors.New("file delivery maximum size must be positive")
	}
	var absoluteCatalog string
	var err error
	if boundary != nil {
		absoluteCatalog, err = boundary.Resolve(path)
	} else {
		absoluteCatalog, err = filepath.Abs(path)
	}
	if err != nil {
		return nil, fmt.Errorf("resolve file catalog path: %w", err)
	}
	file, err := os.Open(absoluteCatalog)
	if err != nil {
		return nil, fmt.Errorf("open file catalog: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxCatalogBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read file catalog: %w", err)
	}
	if len(data) > maxCatalogBytes {
		return nil, fmt.Errorf("file catalog exceeds %d bytes", maxCatalogBytes)
	}
	var decoded rawCatalog
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, fmt.Errorf("decode file catalog: %w", err)
	}
	if decoded.Version != 0 && decoded.Version != 1 {
		return nil, fmt.Errorf("unsupported file catalog version %d", decoded.Version)
	}
	if len(decoded.Files) > maxCatalogEntries {
		return nil, fmt.Errorf("file catalog contains more than %d files", maxCatalogEntries)
	}

	catalog := &Catalog{
		entries:   make([]Entry, 0, len(decoded.Files)),
		byLookup:  make(map[string]Entry),
		maxBytes:  maxBytes,
		boundary:  boundary,
		delivered: make(map[string]time.Time),
		now:       time.Now,
	}
	baseDir := filepath.Dir(absoluteCatalog)
	for index, raw := range decoded.Files {
		if raw.Enabled != nil && !*raw.Enabled {
			continue
		}
		entry, normalizeErr := normalizeEntry(raw, baseDir, maxBytes, boundary)
		if normalizeErr != nil {
			return nil, fmt.Errorf("file catalog entry %d: %w", index+1, normalizeErr)
		}
		keys := append([]string{entry.ID}, entry.Aliases...)
		for _, key := range keys {
			lookup := normalizeLookup(key)
			if previous, exists := catalog.byLookup[lookup]; exists {
				return nil, fmt.Errorf(
					"file catalog lookup %q is shared by %q and %q",
					key,
					previous.ID,
					entry.ID,
				)
			}
			catalog.byLookup[lookup] = entry
		}
		catalog.entries = append(catalog.entries, entry)
	}
	sort.Slice(catalog.entries, func(i, j int) bool {
		return catalog.entries[i].ID < catalog.entries[j].ID
	})
	return catalog, nil
}

func (c *Catalog) List() []Entry {
	if c == nil {
		return nil
	}
	result := make([]Entry, len(c.entries))
	for index, entry := range c.entries {
		result[index] = entry
		result[index].Aliases = append([]string(nil), entry.Aliases...)
		result[index].AllowedGroupIDs = append(
			[]string(nil),
			entry.AllowedGroupIDs...,
		)
	}
	return result
}

func (c *Catalog) Tool(
	sender Sender,
	timeout time.Duration,
	markers MarkerStore,
) tool.Definition {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ids := make([]string, 0, len(c.entries))
	for _, entry := range c.entries {
		ids = append(ids, entry.ID)
	}
	return tool.Definition{
		Name:        "deliver_file",
		Description: c.toolDescription(),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file_id": map[string]any{
					"type":        "string",
					"enum":        ids,
					"description": "管理员文件目录中的稳定文件 ID，不是本地路径。",
				},
			},
			"required":             []string{"file_id"},
			"additionalProperties": false,
		},
		Permission: tool.PermissionEveryone,
		Timeout:    timeout,
		Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
			return c.deliver(ctx, sender, markers, call)
		},
	}
}

func (c *Catalog) deliver(
	ctx context.Context,
	sender Sender,
	markers MarkerStore,
	call tool.Call,
) (tool.Result, error) {
	var arguments deliverArguments
	decoder := json.NewDecoder(bytes.NewReader(call.Arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&arguments); err != nil {
		return tool.Result{}, fmt.Errorf("decode deliver_file arguments: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return tool.Result{}, errors.New("decode deliver_file arguments: trailing JSON data")
	}
	entry, ok := c.byLookup[normalizeLookup(arguments.FileID)]
	if !ok {
		return tool.Result{}, fmt.Errorf("file ID %q is not configured", arguments.FileID)
	}
	if call.Actor.ChatType != platform.ChatPrivate &&
		call.Actor.ChatType != platform.ChatGroup {
		return tool.Result{}, fmt.Errorf("unsupported delivery chat type %q", call.Actor.ChatType)
	}
	if strings.TrimSpace(call.Actor.ChatID) == "" {
		return tool.Result{}, errors.New("delivery chat ID is empty")
	}
	if sender == nil {
		return tool.Result{}, errors.New("file delivery sender is not configured")
	}
	if err := c.authorizeDelivery(ctx, sender, call.Actor, entry); err != nil {
		return tool.Result{}, err
	}
	if err := c.validateFile(entry); err != nil {
		return tool.Result{}, err
	}

	dedupeKey := deliveryKey(call.Actor, entry.ID)
	if dedupeKey != "" && c.alreadyDelivered(dedupeKey) {
		messageText := "该文件已经发送，请在当前会话中查看。"
		return tool.Result{Content: DeliveryResult{
			Status:      "already_sent",
			FileID:      entry.ID,
			DisplayName: entry.DisplayName,
			Message:     messageText,
		}, Response: textResponse(messageText)}, nil
	}
	if call.Actor.ChatType == platform.ChatGroup && entry.PrivateOnly {
		if strings.TrimSpace(call.Actor.UserID) != "" {
			claim, claimErr := claimFirstDelivery(
				markers,
				call.Actor,
				entry,
				platform.ChatPrivate,
				call.Actor.UserID,
			)
			if claimErr != nil {
				c.forgetDelivery(dedupeKey)
				return tool.Result{}, claimErr
			}
			if claim.fresh {
				if err := sender.Send(ctx, platform.Outbound{
					ChatType: platform.ChatPrivate,
					ChatID:   call.Actor.UserID,
					SelfID:   call.Actor.SelfID,
					Chain:    message.Chain{message.Text(entry.FirstDeliveryMessage)},
				}); err != nil {
					_ = rollbackFirstDelivery(markers, claim)
					c.forgetDelivery(dedupeKey)
					return privateRequiredResult(call.Actor, entry), nil
				}
			}
			if err := sender.Send(ctx, platform.Outbound{
				ChatType: platform.ChatPrivate,
				ChatID:   call.Actor.UserID,
				SelfID:   call.Actor.SelfID,
				Chain:    message.Chain{message.File(entry.Path, entry.DisplayName)},
			}); err == nil {
				messageText := "已通过私聊发送文件，请查收。"
				return tool.Result{Content: DeliveryResult{
					Status:      "sent_private",
					FileID:      entry.ID,
					DisplayName: entry.DisplayName,
					Message:     messageText,
				}, Response: textResponse(messageText)}, nil
			} else if rollbackErr := rollbackFirstDelivery(markers, claim); rollbackErr != nil {
				c.forgetDelivery(dedupeKey)
				return tool.Result{}, fmt.Errorf(
					"send private file %q: %v; rollback first delivery: %w",
					entry.ID,
					err,
					rollbackErr,
				)
			}
		}
		if dedupeKey != "" {
			c.forgetDelivery(dedupeKey)
		}
		return privateRequiredResult(call.Actor, entry), nil
	}
	claim, err := claimFirstDelivery(
		markers,
		call.Actor,
		entry,
		call.Actor.ChatType,
		call.Actor.ChatID,
	)
	if err != nil {
		c.forgetDelivery(dedupeKey)
		return tool.Result{}, err
	}
	if claim.fresh {
		if err := sender.Send(ctx, platform.Outbound{
			ChatType: call.Actor.ChatType,
			ChatID:   call.Actor.ChatID,
			SelfID:   call.Actor.SelfID,
			Chain:    message.Chain{message.Text(entry.FirstDeliveryMessage)},
		}); err != nil {
			_ = rollbackFirstDelivery(markers, claim)
			c.forgetDelivery(dedupeKey)
			return tool.Result{}, fmt.Errorf(
				"send first delivery message for %q: %w",
				entry.ID,
				err,
			)
		}
	}
	if err := sender.Send(ctx, platform.Outbound{
		ChatType: call.Actor.ChatType,
		ChatID:   call.Actor.ChatID,
		SelfID:   call.Actor.SelfID,
		Chain:    message.Chain{message.File(entry.Path, entry.DisplayName)},
	}); err != nil {
		if rollbackErr := rollbackFirstDelivery(markers, claim); rollbackErr != nil {
			err = fmt.Errorf("%v; rollback first delivery: %w", err, rollbackErr)
		}
		if dedupeKey != "" {
			c.forgetDelivery(dedupeKey)
		}
		return tool.Result{}, fmt.Errorf("send file %q: %w", entry.ID, err)
	}
	messageText := "文件已发送，请查收。"
	return tool.Result{Content: DeliveryResult{
		Status:      "sent",
		FileID:      entry.ID,
		DisplayName: entry.DisplayName,
		Message:     messageText,
	}, Response: textResponse(messageText)}, nil
}

func normalizeEntry(
	raw rawEntry,
	baseDir string,
	maxBytes int64,
	boundary *security.Boundary,
) (Entry, error) {
	entry := Entry{
		ID:          strings.TrimSpace(raw.ID),
		Name:        strings.TrimSpace(raw.Name),
		Description: strings.TrimSpace(raw.Description),
		DisplayName: strings.TrimSpace(raw.DisplayName),
		FirstDeliveryMessage: strings.TrimSpace(
			strings.ReplaceAll(raw.FirstDeliveryMessage, `\n`, "\n"),
		),
		PrivateOnly: true,
	}
	if !entryIDPattern.MatchString(entry.ID) {
		return Entry{}, fmt.Errorf("invalid file ID %q", raw.ID)
	}
	if entry.Name == "" {
		entry.Name = entry.ID
	}
	if raw.PrivateOnly != nil {
		entry.PrivateOnly = *raw.PrivateOnly
	}
	if len(entry.FirstDeliveryMessage) > maxFirstMessage {
		return Entry{}, fmt.Errorf(
			"file %q first_delivery_message exceeds %d bytes",
			entry.ID,
			maxFirstMessage,
		)
	}
	seenGroupIDs := make(map[string]struct{}, len(raw.AllowedGroupIDs))
	for _, groupID := range raw.AllowedGroupIDs {
		groupID = strings.TrimSpace(groupID)
		if !numericIDPattern.MatchString(groupID) {
			return Entry{}, fmt.Errorf(
				"file %q has invalid allowed group ID %q",
				entry.ID,
				groupID,
			)
		}
		if _, exists := seenGroupIDs[groupID]; exists {
			return Entry{}, fmt.Errorf(
				"file %q repeats allowed group ID %q",
				entry.ID,
				groupID,
			)
		}
		seenGroupIDs[groupID] = struct{}{}
		entry.AllowedGroupIDs = append(entry.AllowedGroupIDs, groupID)
	}
	seenAliases := make(map[string]struct{}, len(raw.Aliases))
	for _, alias := range raw.Aliases {
		alias = strings.TrimSpace(alias)
		if alias == "" {
			continue
		}
		lookup := normalizeLookup(alias)
		if lookup == normalizeLookup(entry.ID) {
			continue
		}
		if _, exists := seenAliases[lookup]; exists {
			continue
		}
		seenAliases[lookup] = struct{}{}
		entry.Aliases = append(entry.Aliases, alias)
	}
	configuredPath := strings.TrimSpace(raw.Path)
	if configuredPath == "" {
		return Entry{}, fmt.Errorf("file %q has an empty path", entry.ID)
	}
	if !filepath.IsAbs(configuredPath) {
		configuredPath = filepath.Join(baseDir, configuredPath)
	}
	var resolvedPath string
	var err error
	if boundary != nil {
		resolvedPath, err = boundary.Resolve(configuredPath)
	} else {
		var absolutePath string
		absolutePath, err = filepath.Abs(configuredPath)
		if err == nil {
			resolvedPath, err = filepath.EvalSymlinks(absolutePath)
		}
	}
	if err != nil {
		return Entry{}, fmt.Errorf("resolve file %q target: %w", entry.ID, err)
	}
	entry.Path = resolvedPath
	if entry.DisplayName == "" {
		entry.DisplayName = filepath.Base(entry.Path)
	}
	if entry.DisplayName != filepath.Base(entry.DisplayName) ||
		entry.DisplayName == "." ||
		entry.DisplayName == ".." ||
		entry.DisplayName == string(filepath.Separator) {
		return Entry{}, fmt.Errorf("file %q has an invalid display_name", entry.ID)
	}
	info, err := os.Stat(entry.Path)
	if err != nil {
		return Entry{}, fmt.Errorf("stat file %q: %w", entry.ID, err)
	}
	if !info.Mode().IsRegular() {
		return Entry{}, fmt.Errorf("file %q path is not a regular file", entry.ID)
	}
	if info.Size() > maxBytes {
		return Entry{}, fmt.Errorf(
			"file %q size %d exceeds maximum %d",
			entry.ID,
			info.Size(),
			maxBytes,
		)
	}
	entry.Size = info.Size()
	return entry, nil
}

func (c *Catalog) authorizeDelivery(
	ctx context.Context,
	sender Sender,
	actor tool.Actor,
	entry Entry,
) error {
	if len(entry.AllowedGroupIDs) == 0 {
		return nil
	}
	if actor.ChatType == platform.ChatGroup {
		if containsString(entry.AllowedGroupIDs, strings.TrimSpace(actor.ChatID)) {
			return nil
		}
		return tool.ErrPermissionDenied
	}

	userID := strings.TrimSpace(actor.UserID)
	if userID == "" {
		userID = strings.TrimSpace(actor.ChatID)
	}
	if !numericIDPattern.MatchString(userID) {
		return tool.ErrPermissionDenied
	}
	caller, ok := sender.(actionCaller)
	if !ok {
		return tool.ErrPermissionDenied
	}
	for _, groupID := range entry.AllowedGroupIDs {
		result, err := caller.Call(ctx, "get_group_member_info", map[string]any{
			"group_id": groupID,
			"user_id":  userID,
		})
		if err != nil {
			continue
		}
		member, known := decodeMembershipResult(result, groupID, userID)
		if known && member {
			return nil
		}
	}
	return tool.ErrPermissionDenied
}

func decodeMembershipResult(result any, groupID, userID string) (bool, bool) {
	var encoded []byte
	switch current := result.(type) {
	case json.RawMessage:
		encoded = current
	case []byte:
		encoded = current
	default:
		var err error
		encoded, err = json.Marshal(result)
		if err != nil {
			return false, false
		}
	}
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return false, false
	}
	if current, exists := payload["group_id"]; exists &&
		jsonID(current) != groupID {
		return false, false
	}
	if current, exists := payload["user_id"]; exists &&
		jsonID(current) != userID {
		return false, false
	}
	if member, exists := payload["member"].(bool); exists {
		return member, true
	}
	if _, exists := payload["user_id"]; exists {
		// Standard OneBot get_group_member_info returns the member object
		// itself; a successful response proves membership.
		return true, true
	}
	return false, false
}

func jsonID(value any) string {
	switch current := value.(type) {
	case string:
		return strings.TrimSpace(current)
	case json.Number:
		return current.String()
	default:
		return strings.TrimSpace(fmt.Sprint(current))
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func (c *Catalog) validateFile(entry Entry) error {
	if c.boundary != nil {
		resolved, err := c.boundary.Resolve(entry.Path)
		if err != nil {
			return fmt.Errorf("validate configured file %q boundary: %w", entry.ID, err)
		}
		if resolved != entry.Path {
			return fmt.Errorf("configured file %q target changed", entry.ID)
		}
	}
	info, err := os.Stat(entry.Path)
	if err != nil {
		return fmt.Errorf("stat configured file %q: %w", entry.ID, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("configured file %q is not a regular file", entry.ID)
	}
	if info.Size() > c.maxBytes {
		return fmt.Errorf(
			"configured file %q size %d exceeds maximum %d",
			entry.ID,
			info.Size(),
			c.maxBytes,
		)
	}
	return nil
}

func (c *Catalog) toolDescription() string {
	var builder strings.Builder
	builder.WriteString("发送管理员预先配置的文件。用户索要下列文件时必须调用此工具；")
	builder.WriteString("只提交 file_id，禁止提交或猜测本地路径。")
	for _, entry := range c.entries {
		builder.WriteString("\n- ")
		builder.WriteString(entry.ID)
		builder.WriteString(": ")
		builder.WriteString(entry.Name)
		if len(entry.Aliases) > 0 {
			builder.WriteString("；别名：")
			builder.WriteString(strings.Join(entry.Aliases, "、"))
		}
		if entry.Description != "" {
			builder.WriteString("；")
			builder.WriteString(entry.Description)
		}
		if entry.PrivateOnly {
			builder.WriteString("；仅私聊发送")
		}
	}
	return builder.String()
}

func privateGuide(selfID string, entry Entry) string {
	keyword := entry.ID
	if selfID = strings.TrimSpace(selfID); selfID != "" {
		return fmt.Sprintf(
			"该文件仅通过私聊发送。请先添加 QQ %s 为好友，然后私聊发送“%s”获取。",
			selfID,
			keyword,
		)
	}
	return fmt.Sprintf(
		"该文件仅通过私聊发送。请先添加机器人为好友，然后私聊发送“%s”获取。",
		keyword,
	)
}

type firstDeliveryClaim struct {
	key   string
	fresh bool
}

func claimFirstDelivery(
	markers MarkerStore,
	actor tool.Actor,
	entry Entry,
	targetChatType, targetChatID string,
) (firstDeliveryClaim, error) {
	if entry.FirstDeliveryMessage == "" {
		return firstDeliveryClaim{}, nil
	}
	if markers == nil {
		return firstDeliveryClaim{}, errors.New(
			"first delivery marker store is not configured",
		)
	}
	recipientType := "user"
	recipientID := strings.TrimSpace(actor.UserID)
	if recipientID == "" && targetChatType == platform.ChatPrivate {
		recipientID = strings.TrimSpace(targetChatID)
	}
	if recipientID == "" {
		recipientType = "chat"
		recipientID = strings.TrimSpace(targetChatID)
	}
	key := strings.Join([]string{
		"file-delivery:v1",
		strings.TrimSpace(actor.Platform),
		"self",
		strings.TrimSpace(actor.SelfID),
		recipientType,
		recipientID,
		"file",
		entry.ID,
	}, "\x00")
	fresh, err := markers.ClaimMarker(key)
	if err != nil {
		return firstDeliveryClaim{}, fmt.Errorf(
			"claim first delivery marker for %q: %w",
			entry.ID,
			err,
		)
	}
	return firstDeliveryClaim{key: key, fresh: fresh}, nil
}

func rollbackFirstDelivery(
	markers MarkerStore,
	claim firstDeliveryClaim,
) error {
	if !claim.fresh || claim.key == "" {
		return nil
	}
	return markers.ReleaseMarker(claim.key)
}

func privateRequiredResult(actor tool.Actor, entry Entry) tool.Result {
	messageText := privateGuide(actor.SelfID, entry)
	return tool.Result{Content: DeliveryResult{
		Status:      "private_chat_required",
		FileID:      entry.ID,
		DisplayName: entry.DisplayName,
		Message:     messageText,
	}, Response: textResponse(messageText)}
}

func deliveryKey(actor tool.Actor, fileID string) string {
	if strings.TrimSpace(actor.MessageID) == "" {
		return ""
	}
	return strings.Join([]string{
		actor.Platform,
		actor.SelfID,
		actor.ChatType,
		actor.ChatID,
		actor.MessageID,
		fileID,
	}, "\x00")
}

func (c *Catalog) alreadyDelivered(key string) bool {
	now := c.now()
	c.deliveryMu.Lock()
	defer c.deliveryMu.Unlock()
	for current, deliveredAt := range c.delivered {
		if now.Sub(deliveredAt) >= deliveryDedupeTTL {
			delete(c.delivered, current)
		}
	}
	if _, exists := c.delivered[key]; exists {
		return true
	}
	c.delivered[key] = now
	return false
}

func (c *Catalog) forgetDelivery(key string) {
	c.deliveryMu.Lock()
	delete(c.delivered, key)
	c.deliveryMu.Unlock()
}

func normalizeLookup(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func textResponse(text string) *domain.AgentResponse {
	return &domain.AgentResponse{
		Reply: text,
		Chain: message.Chain{message.Text(text)},
	}
}
