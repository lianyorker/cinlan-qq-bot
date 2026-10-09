package qqnt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestInspectInstallationSelectsHighestBuild(t *testing.T) {
	root := t.TempDir()
	qqPath := filepath.Join(root, "QQ.exe")
	writeTestFile(t, qqPath, []byte("test"))
	writeTestVersion(t, root, "9.9.30-40000", "9.9.30-40000", "40000")
	expectedApp := writeTestVersion(t, root, "9.9.31-49738", "9.9.31-49738", "49738")

	current, err := inspectInstallation(qqPath)
	if err != nil {
		t.Fatalf("inspectInstallation() error = %v", err)
	}
	if current.Version != "9.9.31-49738" || current.AppDir != expectedApp {
		t.Fatalf("installation = %#v", current)
	}
}

func TestWritePatchedPackageChangesOnlyTarget(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "official-package.json")
	target := filepath.Join(root, "data", "qqnt-package.json")
	official := []byte(`{"name":"qq-chat","version":"9.9.31","main":"./application.asar/app_launcher/index.js"}`)
	writeTestFile(t, source, official)

	if err := writePatchedPackage(source, target); err != nil {
		t.Fatalf("writePatchedPackage() error = %v", err)
	}
	sourceAfter, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if string(sourceAfter) != string(official) {
		t.Fatalf("official package was changed: %s", sourceAfter)
	}
	targetContent, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(targetContent, &document); err != nil {
		t.Fatal(err)
	}
	if document["main"] != "./loadCinlan.js" || document["name"] != "qq-chat" {
		t.Fatalf("patched package = %#v", document)
	}
}

func TestInspectInstallationUsesActivatedVersion(t *testing.T) {
	root := t.TempDir()
	qq := filepath.Join(root, "QQ.exe")
	writeTestFile(t, qq, []byte("fixture"))
	writeTestVersion(t, root, "old", "old", "1")
	writeTestVersion(t, root, "pending-update", "pending-update", "2")
	config := filepath.Join(root, "versions", "config.json")
	writeTestFile(t, config, []byte(`{"curVersion":"old"}`))
	got, err := inspectInstallation(qq)
	if err != nil || got.Version != "old" {
		t.Fatalf("active installation = %#v, %v", got, err)
	}
	for _, content := range []string{`{"curVersion":"missing"}`, `{"curVersion":"../old"}`, `invalid`} {
		writeTestFile(t, config, []byte(content))
		if _, err := inspectInstallation(qq); err == nil {
			t.Fatalf("accepted config %s", content)
		}
	}
}

func writeTestVersion(t *testing.T, root, directory, version, build string) string {
	t.Helper()
	appDir := filepath.Join(root, "versions", directory, "resources", "app")
	writeTestFile(t, filepath.Join(appDir, "wrapper.node"), []byte("native"))
	content, err := json.Marshal(map[string]any{
		"name":         "qq-chat",
		"version":      version,
		"buildVersion": build,
		"main":         "./application.asar/app_launcher/index.js",
	})
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(appDir, "package.json"), content)
	return appDir
}

func writeTestFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}
