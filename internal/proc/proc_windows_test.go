//go:build windows

package proc

import (
	"context"
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"
)

// TestNoWindow: both constructors start the child without a console window, and the child still runs.
func TestNoWindow(t *testing.T) {
	for _, cmd := range []*exec.Cmd{Command("cmd.exe", "/c", "exit 0"), CommandContext(context.Background(), "cmd.exe", "/c", "exit 0")} {
		if cmd.SysProcAttr == nil || cmd.SysProcAttr.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
			t.Fatalf("%v: no CREATE_NO_WINDOW", cmd.Args)
		}
		if err := cmd.Run(); err != nil {
			t.Fatalf("%v: %v", cmd.Args, err)
		}
	}
}
