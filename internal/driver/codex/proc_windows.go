//go:build windows

package codex

import (
	"os/exec"
	"time"
)

// setProcessGroup kills the child on cancel. Killing its descendants on Windows (a job object) comes with the
// Windows installer slice.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.WaitDelay = 5 * time.Second
}
