//go:build !windows

package agent

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

func hideWindow(*exec.Cmd) {}

// replace execs path in this process: same PID, so systemd and launchd keep supervising it.
func replace(path string, env []string) error {
	args := append([]string{path}, os.Args[1:]...)
	return syscall.Exec(path, args, env)
}

// Lock takes the single-instance lock in dir, so two Links never share a device key and connection. release
// drops it; ok is false when another Link holds it.
func Lock(dir string) (release func(), ok bool, err error) {
	f, err := os.OpenFile(filepath.Join(dir, "run.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return func() { f.Close() }, true, nil
}
