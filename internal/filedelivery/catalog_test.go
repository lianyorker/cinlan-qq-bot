package filedelivery

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/message"
	"github.com/lianyorker/cinlan-qq-bot/internal/platform"
	"github.com/lianyorker/cinlan-qq-bot/internal/tool"
)

type recordingSender struct {
	outbound []platform.Outbound
	err      error
}

func (s *recordingSender) Send(_ context.Context, outbound platform.Outbound) error {
	if s.err != nil {
		return s.err
	}
	s.outbound = append(s.outbound, outbound)
	return nil
}

func TestDeliverFileRequiresPrivateChatAndSendsConfiguredFile(t *testing.T) {
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
		groupContent.Status != "private_chat_required" ||
		groupContent.Message == "" ||
		groupResult.Response == nil ||
		groupResult.Response.Reply != groupContent.Message ||
		len(sender.outbound) != 0 {
		t.Fatalf("group result = %#v, outbound = %#v", groupResult, sender.outbound)
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
		len(sender.outbound) != 1 {
		t.Fatalf("private result = %#v, outbound = %#v", privateResult, sender.outbound)
	}
	outbound := sender.outbound[0]
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
	if duplicateContent.Status != "already_sent" || len(sender.outbound) != 1 {
		t.Fatalf("duplicate result = %#v, outbound = %#v", duplicateResult, sender.outbound)
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
