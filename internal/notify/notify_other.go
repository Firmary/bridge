//go:build !darwin && !windows

package notify

import (
	"context"

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

// confirm has no native dialog on Linux desktops: the tray menu asks instead.
func confirm(context.Context, string, string, string, string) (bool, bool) { return false, false }
