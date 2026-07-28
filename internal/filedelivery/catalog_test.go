package filedelivery

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/message"
	"github.com/lianyorker/cinlan-qq-bot/internal/platform"
	"github.com/lianyorker/cinlan-qq-bot/internal/security"
	"github.com/lianyorker/cinlan-qq-bot/internal/tool"
)

type recordingSender struct {
	outbound []platform.Outbound
	err      error
	calls    []recordedAction
	call     func(context.Context, string, map[string]any) (any, error)
}

type recordedAction struct {
	name   string
	params map[string]any
}

func (s *recordingSender) Send(_ context.Context, outbound platform.Outbound) error {
	if s.err != nil {
		return s.err
	}
	s.outbound = append(s.outbound, outbound)
	return nil
}

func (s *recordingSender) Call(
	ctx context.Context,
	name string,
	params map[string]any,
) (any, error) {
	s.calls = append(s.calls, recordedAction{name: name, params: params})
	if s.call == nil {
		return nil, errors.New("action is not configured")
	}
	return s.call(ctx, name, params)
}

func TestDeliverFileRoutesGroupRequestToPrivateAndSendsConfiguredFile(t *testing.T) {
	catalog := loadTestCatalog(t, []byte("select 1;"))
	sender := &recordingSender{}
	definition := catalog.Tool(sender, time.Second)

	groupResult, err := definition.Handler(context.Background(), tool.Call{
		Arguments: json.RawMessage(`{"file_id":"SQL"}`),
		Actor: tool.Actor{
			Platform:  "qq-native",
			ChatType:  platform.ChatGroup,
			ChatID:    "20000001",
			SelfID:    "10000001",
			UserID:    "10000002",
			MessageID: "group-message",
		},
	})
	if err != nil {
		t.Fatalf("group deliver_file error = %v", err)
	}
	groupContent, ok := groupResult.Content.(DeliveryResult)
	if !ok ||
		groupContent.Status != "sent_private" ||
		groupContent.Message == "" ||
		groupResult.Response == nil ||
		groupResult.Response.Reply != groupContent.Message ||
		len(sender.outbound) != 1 {
		t.Fatalf("group result = %#v, outbound = %#v", groupResult, sender.outbound)
	}
	if outbound := sender.outbound[0]; outbound.ChatType != platform.ChatPrivate ||
		outbound.ChatID != "10000002" {
		t.Fatalf("group private outbound = %#v", outbound)
	}

	privateCall := tool.Call{
		Arguments: json.RawMessage(`{"file_id":"数据库脚本"}`),
		Actor: tool.Actor{
			Platform:  "qq-native",
			ChatType:  platform.ChatPrivate,
			ChatID:    "10000002",
			SelfID:    "10000001",
			UserID:    "10000002",
			MessageID: "private-message",
		},
	}
	privateResult, err := definition.Handler(context.Background(), privateCall)
	if err != nil {
		t.Fatalf("private deliver_file error = %v", err)
	}
	privateContent, ok := privateResult.Content.(DeliveryResult)
	if !ok ||
		privateContent.Status != "sent" ||
		privateResult.Response == nil ||
		len(sender.outbound) != 2 {
		t.Fatalf("private result = %#v, outbound = %#v", privateResult, sender.outbound)
	}
	outbound := sender.outbound[1]
	if outbound.ChatType != platform.ChatPrivate ||
		outbound.ChatID != "10000002" ||
		len(outbound.Chain) != 1 ||
		outbound.Chain[0].Type != message.TypeFile ||
		outbound.Chain[0].Data["name"] != "schema.sql" {
		t.Fatalf("outbound = %#v", outbound)
	}
	if filePath, _ := outbound.Chain[0].Data["file"].(string); !filepath.IsAbs(filePath) {
		t.Fatalf("file path = %q, want absolute path", filePath)
	}

	duplicateResult, err := definition.Handler(context.Background(), privateCall)
	if err != nil {
		t.Fatalf("duplicate deliver_file error = %v", err)
	}
	duplicateContent := duplicateResult.Content.(DeliveryResult)
	if duplicateContent.Status != "already_sent" || len(sender.outbound) != 2 {
		t.Fatalf("duplicate result = %#v, outbound = %#v", duplicateResult, sender.outbound)
	}
}

func TestDeliverFileGuidesGroupWhenPrivateSendFails(t *testing.T) {
	catalog := loadTestCatalog(t, []byte("select 1;"))
	sender := &recordingSender{err: errors.New("not a friend")}
	definition := catalog.Tool(sender, time.Second)

	result, err := definition.Handler(context.Background(), tool.Call{
		Arguments: json.RawMessage(`{"file_id":"sql"}`),
		Actor: tool.Actor{
			Platform:  platform.PlatformQQNative,
			ChatType:  platform.ChatGroup,
			ChatID:    "20000001",
			SelfID:    "10000001",
			UserID:    "10000002",
			MessageID: "group-message",
		},
	})
	if err != nil {
		t.Fatalf("group deliver_file error = %v", err)
	}
	content := result.Content.(DeliveryResult)
	if content.Status != "private_chat_required" ||
		!strings.Contains(content.Message, "添加 QQ 10000001 为好友") ||
		result.Response == nil ||
		len(sender.outbound) != 0 {
		t.Fatalf("result = %#v, outbound = %#v", result, sender.outbound)
	}
}

func TestDeliverFileGroupGuideWithoutUserIDCanRetry(t *testing.T) {
	catalog := loadTestCatalog(t, []byte("select 1;"))
	definition := catalog.Tool(&recordingSender{}, time.Second)
	call := tool.Call{
		Arguments: json.RawMessage(`{"file_id":"sql"}`),
		Actor: tool.Actor{
			Platform:  platform.PlatformQQNative,
			ChatType:  platform.ChatGroup,
			ChatID:    "20000001",
			SelfID:    "10000001",
			MessageID: "group-message",
		},
	}
	for attempt := 0; attempt < 2; attempt++ {
		result, err := definition.Handler(context.Background(), call)
		if err != nil {
			t.Fatalf("attempt %d error = %v", attempt+1, err)
		}
		content := result.Content.(DeliveryResult)
		if content.Status != "private_chat_required" {
			t.Fatalf("attempt %d result = %#v", attempt+1, result)
		}
	}
}

func TestDeliverFileRestrictsGroupRequestToConfiguredGroup(t *testing.T) {
	catalog := loadTestCatalogWithGroups(t, []string{"20000001"})
	sender := &recordingSender{}
	definition := catalog.Tool(sender, time.Second)

	allowed := tool.Call{
		Arguments: json.RawMessage(`{"file_id":"sql"}`),
		Actor: tool.Actor{
			Platform:  platform.PlatformQQNative,
			ChatType:  platform.ChatGroup,
			ChatID:    "20000001",
			UserID:    "10000002",
			MessageID: "allowed-message",
		},
	}
	if _, err := definition.Handler(context.Background(), allowed); err != nil {
		t.Fatalf("allowed group delivery error = %v", err)
	}
	if len(sender.outbound) != 1 || len(sender.calls) != 0 {
		t.Fatalf("allowed group outbound = %#v, calls = %#v", sender.outbound, sender.calls)
	}

	denied := allowed
	denied.Actor.ChatID = "20000002"
	denied.Actor.MessageID = "denied-message"
	if _, err := definition.Handler(context.Background(), denied); !errors.Is(
		err,
		tool.ErrPermissionDenied,
	) {
		t.Fatalf("unconfigured group delivery error = %v", err)
	}
	if len(sender.outbound) != 1 || len(sender.calls) != 0 {
		t.Fatalf("denied group outbound = %#v, calls = %#v", sender.outbound, sender.calls)
	}
}

func TestDeliverFileChecksPrivateUserGroupMembership(t *testing.T) {
	tests := []struct {
		name      string
		result    any
		callErr   error
		wantErr   bool
		wantSends int
	}{
		{
			name: "native member",
			result: map[string]any{
				"group_id": "20000001",
				"user_id":  "10000002",
				"member":   true,
			},
			wantSends: 1,
		},
		{
			name: "onebot member object",
			result: json.RawMessage(
				`{"group_id":20000001,"user_id":10000002,"nickname":"member"}`,
			),
			wantSends: 1,
		},
		{
			name: "not a member",
			result: map[string]any{
				"group_id": "20000001",
				"user_id":  "10000002",
				"member":   false,
			},
			wantErr: true,
		},
		{
			name:    "query failure",
			callErr: errors.New("group cache unavailable"),
			wantErr: true,
		},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			catalog := loadTestCatalogWithGroups(t, []string{"20000001"})
			sender := &recordingSender{
				call: func(
					_ context.Context,
					name string,
					params map[string]any,
				) (any, error) {
					if name != "get_group_member_info" ||
						params["group_id"] != "20000001" ||
						params["user_id"] != "10000002" {
						t.Fatalf("membership action = %q %#v", name, params)
					}
					return current.result, current.callErr
				},
			}
			definition := catalog.Tool(sender, time.Second)
			_, err := definition.Handler(context.Background(), tool.Call{
				Arguments: json.RawMessage(`{"file_id":"sql"}`),
				Actor: tool.Actor{
					Platform:  platform.PlatformQQNative,
					ChatType:  platform.ChatPrivate,
					ChatID:    "10000002",
					UserID:    "10000002",
					MessageID: "private-message",
				},
			})
			if current.wantErr && !errors.Is(err, tool.ErrPermissionDenied) {
				t.Fatalf("private delivery error = %v", err)
			}
			if !current.wantErr && err != nil {
				t.Fatalf("private delivery error = %v", err)
			}
			if len(sender.calls) != 1 || len(sender.outbound) != current.wantSends {
				t.Fatalf("calls = %#v, outbound = %#v", sender.calls, sender.outbound)
			}
		})
	}
}

func TestLoadFileRejectsInvalidAllowedGroupIDs(t *testing.T) {
	tests := []struct {
		name     string
		groupIDs string
	}{
		{name: "invalid", groupIDs: `["not-a-group"]`},
		{name: "duplicate", groupIDs: `["20000001", "20000001"]`},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(
				filepath.Join(dir, "schema.sql"),
				[]byte("select 1;"),
				0o600,
			); err != nil {
				t.Fatal(err)
			}
			content := []byte(`{
  "version": 1,
  "files": [{
    "id": "sql",
    "path": "schema.sql",
    "allowed_group_ids": ` + current.groupIDs + `
  }]
}`)
			if err := os.WriteFile(
				filepath.Join(dir, "files.json"),
				content,
				0o600,
			); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadFile(
				filepath.Join(dir, "files.json"),
				1024,
			); err == nil {
				t.Fatal("LoadFile() accepted invalid allowed_group_ids")
			}
		})
	}
}

func TestLoadFileRejectsOversizedFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "large.sql"), []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeCatalog(t, dir)
	if _, err := LoadFile(filepath.Join(dir, "files.json"), 4); err == nil {
		t.Fatal("LoadFile() accepted an oversized file")
	}
}

func TestLoadFileWithinRootRejectsExternalCatalogAndEntry(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "schema.sql"), []byte("select 1;"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeCatalog(t, outside)
	if _, err := LoadFileWithinRoot(
		filepath.Join(outside, "files.json"),
		1024,
		root,
	); !errors.Is(err, security.ErrOutsideAllowedRoot) {
		t.Fatalf("external catalog error = %v", err)
	}

	content := []byte(`{
  "version": 1,
  "files": [{
    "id": "sql",
    "path": "` + strings.ReplaceAll(filepath.Join(outside, "schema.sql"), `\`, `\\`) + `"
  }]
}`)
	if err := os.WriteFile(filepath.Join(root, "files.json"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFileWithinRoot("files.json", 1024, root); !errors.Is(err, security.ErrOutsideAllowedRoot) {
		t.Fatalf("external entry error = %v", err)
	}
}

func TestToolSchemaEnumeratesConfiguredIDs(t *testing.T) {
	catalog := loadTestCatalog(t, []byte("select 1;"))
	definition := catalog.Tool(&recordingSender{}, time.Second)
	properties := definition.Parameters["properties"].(map[string]any)
	fileID := properties["file_id"].(map[string]any)
	enum := fileID["enum"].([]string)
	if len(enum) != 1 || enum[0] != "sql" {
		t.Fatalf("file_id enum = %#v", enum)
	}
	if definition.Description == "" {
		t.Fatal("tool description is empty")
	}
}

func TestDeliverFileRejectsUnconfiguredPathArgument(t *testing.T) {
	catalog := loadTestCatalog(t, []byte("select 1;"))
	definition := catalog.Tool(&recordingSender{}, time.Second)
	_, err := definition.Handler(context.Background(), tool.Call{
		Arguments: json.RawMessage(`{"file_id":"sql","path":"C:\\secret.txt"}`),
		Actor: tool.Actor{
			ChatType: platform.ChatPrivate,
			ChatID:   "10000002",
		},
	})
	if err == nil {
		t.Fatal("deliver_file accepted an unconfigured path argument")
	}
}

func loadTestCatalog(t *testing.T, content []byte) *Catalog {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "schema.sql"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	writeCatalog(t, dir)
	catalog, err := LoadFile(filepath.Join(dir, "files.json"), 1024)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	return catalog
}

func loadTestCatalogWithGroups(t *testing.T, groupIDs []string) *Catalog {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(dir, "schema.sql"),
		[]byte("select 1;"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	encodedGroups, err := json.Marshal(groupIDs)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte(`{
  "version": 1,
  "files": [{
    "id": "sql",
    "name": "SQL 脚本",
    "path": "schema.sql",
    "display_name": "schema.sql",
    "private_only": true,
    "allowed_group_ids": ` + string(encodedGroups) + `
  }]
}`)
	if err := os.WriteFile(filepath.Join(dir, "files.json"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := LoadFile(filepath.Join(dir, "files.json"), 1024)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	return catalog
}

func writeCatalog(t *testing.T, dir string) {
	t.Helper()
	content := []byte(`{
  "version": 1,
  "files": [
    {
      "id": "sql",
      "name": "SQL 脚本",
      "aliases": ["sql", "数据库脚本"],
      "description": "数据库初始化脚本",
      "path": "schema.sql",
      "display_name": "schema.sql",
      "private_only": true
    }
  ]
}`)
	if err := os.WriteFile(filepath.Join(dir, "files.json"), content, 0o600); err != nil {
		t.Fatal(err)
	}
}
