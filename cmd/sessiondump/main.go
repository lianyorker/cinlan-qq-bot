// sessiondump decrypts a sessions.db store and prints one scope's
// history — for forensics like "what did someone recall" where the
// platform already scrubbed the original text but the session kept it.
// --ndjson emits one {scope,role,text} JSON line per entry so eval/eval.js
// can consume the export directly.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/session"
)

func main() {
	args := os.Args[1:]
	ndjson := false
	if len(args) > 0 && args[0] == "--ndjson" {
		ndjson = true
		args = args[1:]
	}
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr,
			"usage: sessiondump [--ndjson] <sessions.db> <encryption key> [scope substring]")
		os.Exit(2)
	}
	store, err := session.OpenEncryptedSQLite(
		20, 24*time.Hour, args[0], args[1], "",
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer store.Close()
	match := ""
	if len(args) > 2 {
		match = args[2]
	}
	type row struct {
		Scope string `json:"scope"`
		Role  string `json:"role"`
		Text  string `json:"text"`
	}
	enc := json.NewEncoder(os.Stdout)
	for _, info := range store.List() {
		if match != "" && !strings.Contains(info.ID, match) {
			continue
		}
		history, _ := store.Snapshot(info.ID)
		if ndjson {
			for _, m := range history {
				enc.Encode(row{Scope: info.ID, Role: m.Role, Text: m.Content})
			}
			continue
		}
		fmt.Println("=== " + info.ID + " ===")
		for _, m := range history {
			fmt.Printf("%s: %s\n", m.Role, strings.ReplaceAll(m.Content, "\n", " | "))
		}
	}
}
