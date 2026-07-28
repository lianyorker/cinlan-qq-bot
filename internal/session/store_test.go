package session

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreKeepsCompleteRecentExchanges(t *testing.T) {
	now := time.Unix(100, 0)
	store := newWithClock(3, time.Hour, func() time.Time { return now })

	store.AddExchange("session", "u1", "a1")
	store.AddExchange("session", "u2", "a2")

	history, handoff := store.Snapshot("session")
	if handoff {
		t.Fatalf("handoff = true, want false")
	}
	if len(history) != 2 || history[0].Content != "u2" || history[1].Content != "a2" {
		t.Fatalf("history = %#v, want latest complete exchange", history)
	}
}

func TestEncryptedSQLitePersistsWithoutPlaintext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.db")
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x41}, 32))
	store, err := OpenEncryptedSQLite(20, time.Hour, path, key, "")
	if err != nil {
		t.Fatalf("OpenEncryptedSQLite() error = %v", err)
	}
	scope := "qq-native:self:10001:group:30003"
	store.AddExchange(scope, "sensitive question", "private answer")
	store.ApplyCompression(scope, "encrypted summary", "learned preference", 0)
	persona := "private-persona"
	store.UpdateSettings(scope, &persona, nil)
	marker := "file-delivery:v1:qq-native:self:10001:user:20002:file:sql"
	claimed, err := store.ClaimMarker(marker)
	if err != nil || !claimed {
		t.Fatalf("ClaimMarker() = %v, %v", claimed, err)
	}
	if err := store.PersistenceError(); err != nil {
		t.Fatalf("PersistenceError() = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	for _, entry := range listFiles(t, dir) {
		data, err := os.ReadFile(entry)
		if err != nil {
			t.Fatalf("ReadFile(%q) error = %v", entry, err)
		}
		for _, plaintext := range []string{
			scope,
			"sensitive question",
			"private answer",
			"encrypted summary",
			"learned preference",
			"private-persona",
			marker,
		} {
			if bytes.Contains(data, []byte(plaintext)) {
				t.Fatalf("%q contains plaintext %q", entry, plaintext)
			}
		}
	}

	restored, err := OpenEncryptedSQLite(20, time.Hour, path, key, "")
	if err != nil {
		t.Fatalf("restore OpenEncryptedSQLite() error = %v", err)
	}
	defer restored.Close()
	snapshot := restored.SnapshotState(scope)
	if snapshot.Summary != "encrypted summary" ||
		snapshot.Memory != "learned preference" ||
		snapshot.Settings.Persona != persona {
		t.Fatalf("restored snapshot = %#v", snapshot)
	}
	claimed, err = restored.ClaimMarker(marker)
	if err != nil || claimed {
		t.Fatalf("restored ClaimMarker() = %v, %v", claimed, err)
	}
	if err := restored.ReleaseMarker(marker); err != nil {
		t.Fatalf("ReleaseMarker() error = %v", err)
	}
	claimed, err = restored.ClaimMarker(marker)
	if err != nil || !claimed {
		t.Fatalf("reclaimed marker = %v, %v", claimed, err)
	}
}

func TestEncryptedSQLiteMigratesAndArchivesLegacyJSON(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "sessions.json")
	legacy, err := openLegacy(20, time.Hour, legacyPath)
	if err != nil {
		t.Fatalf("Open() legacy error = %v", err)
	}
	legacy.AddExchange("qq-native:self:1:group:2", "legacy question", "legacy answer")
	if err := legacy.PersistenceError(); err != nil {
		t.Fatalf("legacy PersistenceError() = %v", err)
	}

	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32))
	store, err := OpenEncryptedSQLite(
		20,
		time.Hour,
		filepath.Join(dir, "sessions.db"),
		key,
		legacyPath,
	)
	if err != nil {
		t.Fatalf("OpenEncryptedSQLite() migration error = %v", err)
	}
	defer store.Close()
	history, _ := store.Snapshot("qq-native:self:1:group:2")
	if len(history) != 2 || history[1].Content != "legacy answer" {
		t.Fatalf("migrated history = %#v", history)
	}
	if _, err := os.Stat(legacyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy plaintext still exists: %v", err)
	}
	archive, err := os.ReadFile(legacyPath + ".migrated.enc")
	if err != nil {
		t.Fatalf("encrypted legacy archive missing: %v", err)
	}
	if !bytes.HasPrefix(archive, []byte(encryptedArchiveHeader)) ||
		bytes.Contains(archive, []byte("legacy question")) {
		t.Fatalf("legacy archive is not encrypted")
	}
}

func listFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			files = append(files, path)
		}
		return nil
	}); err != nil {
		t.Fatalf("WalkDir() error = %v", err)
	}
	return files
}

func TestStoreHandoffClearAndExpiry(t *testing.T) {
	now := time.Unix(100, 0)
	store := newWithClock(10, time.Hour, func() time.Time { return now })

	store.AddExchange("session", "question", "answer")
	store.ApplyCompression("session", "summary", "memory", 0)
	store.SetHandoff("session", true)
	store.Clear("session")

	snapshot := store.SnapshotState("session")
	if len(snapshot.History) != 0 ||
		!snapshot.Handoff ||
		snapshot.Summary != "" ||
		snapshot.Memory != "" {
		t.Fatalf("after clear: snapshot=%#v", snapshot)
	}

	now = now.Add(time.Hour)
	if history, handoff := store.Snapshot("session"); history != nil || handoff {
		t.Fatalf("expired session returned history=%#v handoff=%v", history, handoff)
	}
}

func TestStorePersistsAndRestoresWithoutMessageLeakInList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	store, err := openLegacy(10, time.Hour, path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	store.AddExchange("qq:group:1:user:2", "question", "answer")
	store.SetHandoff("qq:group:1:user:2", true)
	persona := "sales"
	provider := "backup"
	store.UpdateSettings("qq:group:1:user:2", &persona, &provider)
	if err := store.PersistenceError(); err != nil {
		t.Fatalf("PersistenceError() = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("session file missing: %v", err)
	}

	restored, err := openLegacy(10, time.Hour, path)
	if err != nil {
		t.Fatalf("restore Open() error = %v", err)
	}
	history, handoff := restored.Snapshot("qq:group:1:user:2")
	if len(history) != 2 || !handoff || history[1].Content != "answer" {
		t.Fatalf("restored session = %#v, handoff=%v", history, handoff)
	}
	settings := restored.Settings("qq:group:1:user:2")
	if settings.Persona != "sales" || settings.Provider != "backup" {
		t.Fatalf("restored settings = %#v", settings)
	}
	info := restored.List()
	if len(info) != 1 || info[0].HistoryCount != 2 || !info[0].Handoff ||
		info[0].Persona != "sales" || info[0].Provider != "backup" {
		t.Fatalf("List() = %#v", info)
	}
}

func TestApplyCompressionSnapshotPreservesAppendedMessages(t *testing.T) {
	store := New(20, time.Hour)
	store.AddExchange("session", "first", "answer one")
	store.AddExchange("session", "second", "answer two")
	snapshot := store.SnapshotState("session")
	store.AddExchange("session", "new while compressing", "new answer")

	if !store.ApplyCompressionSnapshot(
		"session",
		"old exchanges summarized",
		"stable preference",
		2,
		snapshot.History,
	) {
		t.Fatal("unchanged snapshot prefix was rejected")
	}
	current := store.SnapshotState("session")
	if len(current.History) != 4 ||
		current.History[0].Content != "second" ||
		current.History[2].Content != "new while compressing" ||
		current.Summary != "old exchanges summarized" ||
		current.Memory != "stable preference" {
		t.Fatalf("compressed state = %#v", current)
	}

	stale := snapshot.History
	if store.ApplyCompressionSnapshot("session", "stale", "", 2, stale) {
		t.Fatal("stale snapshot was applied")
	}
}
