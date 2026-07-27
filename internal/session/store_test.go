package session

import (
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

func TestStoreHandoffClearAndExpiry(t *testing.T) {
	now := time.Unix(100, 0)
	store := newWithClock(10, time.Hour, func() time.Time { return now })

	store.AddExchange("session", "question", "answer")
	store.SetHandoff("session", true)
	store.Clear("session")

	history, handoff := store.Snapshot("session")
	if len(history) != 0 || !handoff {
		t.Fatalf("after clear: history=%#v handoff=%v", history, handoff)
	}

	now = now.Add(time.Hour)
	if history, handoff := store.Snapshot("session"); history != nil || handoff {
		t.Fatalf("expired session returned history=%#v handoff=%v", history, handoff)
	}
}

func TestStorePersistsAndRestoresWithoutMessageLeakInList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	store, err := Open(10, time.Hour, path)
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

	restored, err := Open(10, time.Hour, path)
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
