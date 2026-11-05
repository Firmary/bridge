package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A Mac whose login item started "Firmary Link.app", deleted after "Firmary Bridge.app" was added, opens the new app
// at login, with the agent's other settings kept. An agent whose app is still there is left alone.
func TestRepointLaunchdAfterTheOldAppIsGone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.firmary.link.plist")
	gone := filepath.Join(dir, "Firmary Link.app", "Contents", "MacOS", "firmary-link")
	exe := filepath.Join(dir, "Firmary Bridge.app", "Contents", "MacOS", "firmary-link")
	old := LaunchdPlist(Config{Exe: gone, Args: []string{"run"}, DataDir: "/d & e"}, true)
	os.WriteFile(path, []byte(old), 0o644)

	if err := repointLaunchd(path, exe); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if want := LaunchdPlist(Config{Exe: exe, Args: []string{"run"}, DataDir: "/d & e"}, true); string(b) != want {
		t.Errorf("plist:\n%s\nwant:\n%s", b, want)
	}

	// The executable it starts exists: nothing changes.
	os.MkdirAll(filepath.Dir(gone), 0o755)
	os.WriteFile(gone, nil, 0o755)
	os.WriteFile(path, []byte(old), 0o644)
	if err := repointLaunchd(path, exe); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != old || !strings.Contains(old, "Firmary Link.app") {
		t.Errorf("an agent whose app exists was changed:\n%s", b)
	}

	// No agent: nothing is written.
	if err := repointLaunchd(filepath.Join(dir, "none.plist"), exe); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "none.plist")); !os.IsNotExist(err) {
		t.Error("a plist was created")
	}
}
