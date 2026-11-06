//go:build windows

package proc

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// hide starts a console program without a console window; its own children share that windowless console. The flag
// does nothing to a GUI program such as the Link itself, whose windows (the pairing-code box) must still show, which
// is why HideWindow is not set: it would hide a GUI program's first window.
func hide(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
}
