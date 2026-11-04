package service

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Firmary/bridge/internal/brand"
)

// The desktop app's "Open at login": a login item that starts the menu-bar or tray app when the person logs in.
// macOS: a launchd agent that runs once at login. Windows: the per-user Run value (the installer sets it too).
// Linux desktops: an XDG autostart entry.

func autostartPath(home string) string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "autostart", brand.Command+".desktop")
}

func desktopEntry(c Config) string {
	exec := `"` + strings.ReplaceAll(c.Exe, `"`, `\"`) + `"`
	return "[Desktop Entry]\nType=Application\nName=" + brand.Name + "\nExec=" + exec +
		"\nIcon=" + brand.Command + "\nX-GNOME-Autostart-enabled=true\nNoDisplay=false\nTerminal=false\n"
}

// SetOpenAtLogin turns the login item on or off. c.Args is ignored: the login item starts the desktop app.
func SetOpenAtLogin(c Config, on bool) error {
	c.Args = nil
	switch runtime.GOOS {
	case "darwin":
		if !on {
			if Query(c).Kind == "launchd agent" {
				return nil // the always-on agent stays
			}
			return uninstallLaunchd(c)
		}
		if st := Query(c); st.Installed {
			return nil
		}
		_, err := installLaunchd(c, false)
		return err
	case "windows":
		return setRunKey(`"`+c.Exe+`"`, on)
	}
	path := autostartPath(c.Home)
	if !on {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(desktopEntry(c)), 0o644)
}

// OpenAtLogin reports whether the login item (or the always-on service) is on.
func OpenAtLogin(c Config) bool {
	switch runtime.GOOS {
	case "darwin":
		return Query(c).Installed
	case "windows":
		_, ok := runKey()
		return ok || Query(c).Installed
	}
	_, err := os.Stat(autostartPath(c.Home))
	return err == nil
}
