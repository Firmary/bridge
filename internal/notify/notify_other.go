//go:build !darwin && !windows

package notify

import (
	"context"
	"os/exec"
	"strings"

	"github.com/godbus/dbus/v5"
)

// show sends a desktop notification over the session bus (org.freedesktop.Notifications).
func show(title, body string) {
	conn, err := dbus.SessionBus()
	if err != nil {
		return
	}
	obj := conn.Object("org.freedesktop.Notifications", "/org/freedesktop/Notifications")
	_ = obj.Call("org.freedesktop.Notifications.Notify", 0, title, uint32(0), "", title, body,
		[]string{}, map[string]dbus.Variant{}, int32(-1)).Err
}

// textDialog is the desktop's own text-entry dialog program: zenity (GNOME and most others) or kdialog (KDE).
func textDialog() string {
	for _, p := range []string{"zenity", "kdialog"} {
		if path, err := exec.LookPath(p); err == nil {
			return path
		}
	}
	return ""
}

func canAskText() bool { return textDialog() != "" }

// askText runs zenity or kdialog. The title and prompt are on its command line (they hold nothing private); the
// typed text comes back on its stdout.
func askText(ctx context.Context, title, prompt, ok string) (string, bool) {
	path := textDialog()
	if path == "" {
		return "", false
	}
	args := []string{"--title", title, "--inputbox", prompt}
	if strings.HasSuffix(path, "zenity") {
		args = []string{"--entry", "--title", title, "--text", prompt, "--ok-label", ok, "--timeout", "600"}
	}
	out, err := exec.CommandContext(ctx, path, args...).Output()
	if err != nil || ctx.Err() != nil {
		return "", false
	}
	return strings.TrimRight(string(out), "\r\n"), true
}

// confirm has no native dialog on Linux desktops: the tray menu asks instead.
func confirm(context.Context, string, string, string, string) (bool, bool) { return false, false }
