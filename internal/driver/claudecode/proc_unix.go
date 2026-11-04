//go:build !windows

package claudecode

import (
	"os/exec"
	"syscall"
	"time"
)

// setProcessGroup runs the child in its own process group and kills the whole group on cancel.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 5 * time.Second
}
