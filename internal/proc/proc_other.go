//go:build !windows

package proc

import "os/exec"

// hide does nothing: macOS and Linux open no window for a child process.
func hide(*exec.Cmd) {}
