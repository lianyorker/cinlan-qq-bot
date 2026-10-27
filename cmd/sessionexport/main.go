// sessionexport decrypts a sessions.db store and streams NDJSON —
// one {"scope","role","text"} per line — for offline eval gates
// (repeat ratio, persona-leak sampling, group open rate).
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/session"
)

type line struct {
	Scope string `json:"scope"`
	Role  string `json:"role"`
	Text  string `json:"text"`
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr,
			"usage: sessionexport <sessions.db> <encryption key> [scope substring]")
		os.Exit(2)
	}
	store, err := session.OpenEncryptedSQLite(
		20, 24*time.Hour, os.Args[1], os.Args[2], "",
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer store.Close()
	match := ""
	if len(os.Args) > 3 {
		match = os.Args[3]
	}
	out := json.NewEncoder(os.Stdout)
	for _, info := range store.List() {
		if match != "" && !strings.Contains(info.ID, match) {
			continue
		}
		history, _ := store.Snapshot(info.ID)
		for _, m := range history {
			out.Encode(line{Scope: info.ID, Role: m.Role, Text: m.Content})
		}
	}
}
