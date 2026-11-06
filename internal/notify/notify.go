// Package notify shows the desktop app's native notifications, its pairing question and the box the person types
// Firmary's pairing code into. Nothing secret passes on a command line: the text goes to the system's own notifier
// over stdin or an API, and a typed code comes back on stdout, so other users of the computer can't read a pairing
// code or a name from the process list.
package notify

import (
	"context"
	"encoding/base64"
	"strings"
)

// applescript quotes s as an AppleScript string.
func applescript(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\r", " ", "\n", `\n`).Replace(s) + `"`
}

// toastScript is the PowerShell that raises a Windows toast with title and body. The toast's XML goes in as base64,
// so no text (a firm or device name from the relay) is ever part of the script's source: PowerShell treats the
// typographic quotes U+2018 to U+201B as quotes too, which no single-quote escaping covers.
func toastScript(title, body string) string {
	xml := "<toast><visual><binding template='ToastGeneric'><text>" + xmlEscape(title) + "</text><text>" +
		xmlEscape(body) + "</text></binding></visual></toast>"
	return strings.Join([]string{
		"[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] | Out-Null",
		"[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType = WindowsRuntime] | Out-Null",
		"$x = New-Object Windows.Data.Xml.Dom.XmlDocument",
		"$x.LoadXml([Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('" + base64.StdEncoding.EncodeToString([]byte(xml)) + "')))",
		"$t = [Windows.UI.Notifications.ToastNotification]::new($x)",
		// PowerShell's own app id, which Windows lets show toasts without registering one.
		"[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\\WindowsPowerShell\\v1.0\\powershell.exe').Show($t)",
	}, "\n")
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
