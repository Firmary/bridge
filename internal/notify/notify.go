// Package notify shows the desktop app's native notifications, its pairing question and the box the person types
// Firmary's pairing code into. Nothing secret passes on a command line: the text goes to the system's own notifier
// over stdin or an API, and a typed code comes back on stdout, so other users of the computer can't read a pairing
// code or a name from the process list.
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

// AskText asks for one line of text in a native dialog (macOS, Windows; zenity or kdialog on a Linux desktop).
// answered is false when there is no dialog to show, the person cancelled, it timed out, or ctx ended.
func AskText(ctx context.Context, title, prompt, ok string) (text string, answered bool) {
	text, answered = askText(ctx, title, prompt, ok)
	text = strings.TrimSpace(text)
	return text, answered && text != ""
}

// CanAskText reports whether this computer has a dialog AskText can show.
func CanAskText() bool { return canAskText() }

// Show shows a notification. Errors are ignored: a notification is never essential.
func Show(title, body string) { show(title, body) }
