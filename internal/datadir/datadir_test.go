package datadir

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPaths(t *testing.T) {
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }
	for _, c := range []struct{ goos, current, former string }{
		{"linux", "/h/.local/share/firmary-link", "/h/.local/share/firmary-helper"},
		{"darwin", "/h/Library/Application Support/Firmary Link", "/h/Library/Application Support/Firmary Helper"},
	} {
		cur, old := Paths(c.goos, "/h", getenv)
		if cur != filepath.FromSlash(c.current) || old != filepath.FromSlash(c.former) {
			t.Errorf("%s: %q %q", c.goos, cur, old)
		}
	}
	env["XDG_DATA_HOME"] = "/x"
	if cur, old := Paths("linux", "/h", getenv); cur != filepath.FromSlash("/x/firmary-link") || old != filepath.FromSlash("/x/firmary-helper") {
		t.Errorf("xdg: %q %q", cur, old)
	}
	env["LOCALAPPDATA"] = "/l"
	if cur, old := Paths("windows", "/h", getenv); cur != filepath.FromSlash("/l/Firmary/Link") || old != filepath.FromSlash("/l/Firmary/Helper") {
		t.Errorf("windows: %q %q", cur, old)
	}
}

// TestMigrateKeepsAPairedInstallPaired: a beta install's data moves to the new name with its key and pairing, and
// is never merged into or over an existing directory.
func TestMigrateKeepsAPairedInstallPaired(t *testing.T) {
	root := t.TempDir()
	former, current := filepath.Join(root, "firmary-helper"), filepath.Join(root, "share", "firmary-link")
	files := map[string]string{"prod/device.key": "key", "prod/pairing.json": `{"deviceId":"d"}`,
		"prod/profiles/u/claude-code/config/.credentials.json": "sign-in"}
	for name, body := range files {
		p := filepath.Join(former, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(p), 0o700)
		os.WriteFile(p, []byte(body), 0o600)
	}
	if got := Migrate(current, former); got != current {
		t.Fatalf("Migrate = %q, want %q", got, current)
	}
	for name, body := range files {
		b, err := os.ReadFile(filepath.Join(current, filepath.FromSlash(name)))
		if err != nil || string(b) != body {
			t.Errorf("%s not moved: %q %v", name, b, err)
		}
	}
	if _, err := os.Stat(former); !os.IsNotExist(err) {
		t.Error("former directory still there")
	}

	// Once the new directory exists, a former one (say, an old copy run again) is left alone.
	os.MkdirAll(filepath.Join(former, "prod"), 0o700)
	os.WriteFile(filepath.Join(former, "prod", "device.key"), []byte("other"), 0o600)
	if got := Migrate(current, former); got != current {
		t.Fatalf("Migrate = %q", got)
	}
	if b, _ := os.ReadFile(filepath.Join(current, "prod", "device.key")); string(b) != "key" {
		t.Error("existing data overwritten")
	}

	// A fresh computer: nothing to move.
	fresh := filepath.Join(root, "fresh")
	if got := Migrate(fresh, filepath.Join(root, "none")); got != fresh {
		t.Fatalf("Migrate = %q", got)
	}
}
