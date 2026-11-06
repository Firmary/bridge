// Package proc makes every child process the Link starts, so none of them opens a window. The Link is a Windows GUI
// program with no console of its own, so each console program it starts (claude, codex, schtasks, PowerShell) would
// otherwise get a new console window that flashes up and closes. TestOnlyProcStarts keeps this the only way to start one.
package proc

import (
	"context"
	"os/exec"
)

// Command is exec.Command for a child that runs without a window.
func Command(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	hide(cmd)
	return cmd
}

// CommandContext is exec.CommandContext for a child that runs without a window.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	hide(cmd)
	return cmd
}
