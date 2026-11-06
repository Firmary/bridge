package notify

import (
	"context"
	"strings"
	"time"

	"golang.org/x/sys/windows"

	"github.com/Firmary/bridge/internal/proc"
)

// show raises a Windows toast through PowerShell's WinRT access, with the text on stdin and no window.
func show(title, body string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := proc.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", "-")
	cmd.Stdin = strings.NewReader(toastScript(title, body))
	_ = cmd.Run()
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
