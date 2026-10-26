// sessiondump decrypts a sessions.db store and prints one scope's
// history — for forensics like "what did someone recall" where the
// platform already scrubbed the original text but the session kept it.
package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/session"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr,
			"usage: sessiondump <sessions.db> <encryption key> [scope substring]")
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
	for _, info := range store.List() {
		if match != "" && !strings.Contains(info.ID, match) {
			continue
		}
		fmt.Println("=== " + info.ID + " ===")
		history, _ := store.Snapshot(info.ID)
		for _, m := range history {
			fmt.Printf("%s: %s\n", m.Role, strings.ReplaceAll(m.Content, "\n", " | "))
		}
	}
}
