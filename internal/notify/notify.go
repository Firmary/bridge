// Package notify shows the desktop app's native notifications and its one question (pairing). Nothing passes on a
// command line: the text goes to the system's own notifier over stdin or an API, so other users of the computer
// can't read a pairing code or a name from the process list.
package notify

import (
	"context"
	"strings"
)

// applescript quotes s as an AppleScript string.
func applescript(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\r", " ", "\n", `\n`).Replace(s) + `"`
}

// powershell quotes s as a PowerShell single-quoted string.
func powershell(s string) string {
	return "'" + strings.ReplaceAll(strings.NewReplacer("\r", " ", "\n", " ").Replace(s), "'", "''") + "'"
}

// xmlEscape escapes s for a toast's XML.
func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;").Replace(s)
}

// Confirm asks a yes/no question in a native dialog where the system has one (macOS, Windows). answered is false
// when there is no dialog to show, the dialog timed out, or ctx ended.
func Confirm(ctx context.Context, title, text, yes, no string) (accept, answered bool) {
	return confirm(ctx, title, text, yes, no)
}

// Show shows a notification. Errors are ignored: a notification is never essential.
func Show(title, body string) { show(title, body) }
