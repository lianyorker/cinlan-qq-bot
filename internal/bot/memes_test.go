package bot

import (
	"os"
	"path/filepath"
	"testing"
)

func touchMemeFile(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("\x89PNGfake"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

// Group draws must never reach into private pools: an image collected in
// a private chat resurfacing in a group is exactly the leak the pool
// split exists to prevent. Private draws may still see the full union —
// the recipient is the same person the images came from.
func TestPickMemeGroupNeverDrawsPrivatePools(t *testing.T) {
	root := t.TempDir()
	s := &Service{memeDir: root}
	groupPath := touchMemeFile(
		t, filepath.Join(root, "qq-onebot_group_30003"), "a.png",
	)
	privatePath := touchMemeFile(
		t, filepath.Join(root, "qq-onebot_private_10001"), "b.png",
	)
	legacyPath := touchMemeFile(
		t, filepath.Join(root, "qq-onebot_unknown"), "c.png",
	)
	touchMemeFile(t, filepath.Join(root, "discord_group_1"), "d.png")

	for i := 0; i < 32; i++ {
		got, err := s.pickMeme("qq-onebot", "group", "30003", "")
		if err != nil {
			t.Fatalf("group pick: %v", err)
		}
		if got != groupPath {
			t.Fatalf("group draw leaked %q (private=%q legacy=%q)", got, privatePath, legacyPath)
		}
	}
	for i := 0; i < 32; i++ {
		got, err := s.pickMeme("qq-onebot", "private", "10001", "")
		if err != nil {
			t.Fatalf("private pick: %v", err)
		}
		if got != groupPath && got != privatePath && got != legacyPath {
			t.Fatalf("private draw out of platform scope: %q", got)
		}
	}
}
