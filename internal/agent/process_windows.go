//go:build windows

package agent

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"
)

func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}

// replace starts path with this process's arguments and ends this one. Windows can't exec in place, and keeps
// a running .exe locked, which is why updates live in their own versions directory. The launcher waits for the
// child so a logon task sees it run; an update switch exits at once. A command run from a terminal attaches to it
// itself (cmd/firmary-link attachConsole).
func replace(path string, env []string) error {
	cmd := exec.Command(path, os.Args[1:]...)
	cmd.Env = env
	if err := cmd.Start(); err != nil {
		return err
	}
	for _, kv := range env {
		if kv == "FIRMARY_LINK_START_REASON=update" {
			os.Exit(0)
		}
	}
	if err := cmd.Wait(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			os.Exit(ee.ExitCode())
		}
		os.Exit(1)
	}
	os.Exit(0)
	return nil
}

// Lock takes the single-instance lock in dir, so two Links never share a device key and connection.
func Lock(dir string) (release func(), ok bool, err error) {
	f, err := os.OpenFile(filepath.Join(dir, "run.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	ol := new(windows.Overlapped)
	err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ol)
	if err != nil {
		f.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return func() { f.Close() }, true, nil
}
