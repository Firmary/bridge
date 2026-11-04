package notify

import (
	"context"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// show raises a Windows toast through PowerShell's WinRT access, with the text on stdin and no window.
func show(title, body string) {
	xml := "<toast><visual><binding template='ToastGeneric'><text>" + xmlEscape(title) + "</text><text>" +
		xmlEscape(body) + "</text></binding></visual></toast>"
	script := strings.Join([]string{
		"[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] | Out-Null",
		"[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType = WindowsRuntime] | Out-Null",
		"$x = New-Object Windows.Data.Xml.Dom.XmlDocument",
		"$x.LoadXml(" + powershell(xml) + ")",
		"$t = [Windows.UI.Notifications.ToastNotification]::new($x)",
		// PowerShell's own app id, which Windows lets show toasts without registering one.
		"[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\\WindowsPowerShell\\v1.0\\powershell.exe').Show($t)",
	}, "\n")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", "-")
	cmd.Stdin = strings.NewReader(script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	_ = cmd.Run()
}

func canAskText() bool { return true }

// askText shows the system InputBox through PowerShell, with the text on stdin and the answer on stdout. The box
// returns "" when the person cancels.
func askText(ctx context.Context, title, prompt, ok string) (string, bool) {
	script := strings.Join([]string{
		"Add-Type -AssemblyName Microsoft.VisualBasic",
		"$r = [Microsoft.VisualBasic.Interaction]::InputBox(" + powershell(prompt) + ", " + powershell(title) + ", '')",
		"[Console]::Out.Write($r)",
	}, "\n")
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", "-")
	cmd.Stdin = strings.NewReader(script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	out, err := cmd.Output()
	if err != nil || ctx.Err() != nil {
		return "", false
	}
	return string(out), true
}

const (
	mbYesNo         = 0x00000004
	mbIconQuestion  = 0x00000020
	mbSetForeground = 0x00010000
	mbTopmost       = 0x00040000
	idYes           = 6
	idNo            = 7
)

func confirm(ctx context.Context, title, text, yes, no string) (bool, bool) {
	// MessageBox's buttons are Yes and No; the text says which is which.
	text += "\n\nYes: " + yes + "\nNo: " + no
	t, _ := windows.UTF16PtrFromString(text)
	c, _ := windows.UTF16PtrFromString(title)
	done := make(chan int32, 1)
	go func() {
		r, _ := windows.MessageBox(0, t, c, mbYesNo|mbIconQuestion|mbSetForeground|mbTopmost)
		done <- r
	}()
	select {
	case r := <-done:
		switch r {
		case idYes:
			return true, true
		case idNo:
			return false, true
		}
		return false, false
	case <-ctx.Done():
		return false, false
	}
}
