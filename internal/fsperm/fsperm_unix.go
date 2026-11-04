//go:build !windows

// Package fsperm checks that the Link's data directory and key file belong to the person running it. Another
// local user who owns or can write them could swap the verified Claude Code binary, plant a device key or pairing,
// or read the logs.
package fsperm

import (
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

// CheckOwned refuses path unless it is owned by the current user and none of the forbidden permission bits are
// set (for example 0o022 for a directory, 0o077 for a key file).
func CheckOwned(path string, forbidden fs.FileMode) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); !ok || int(sys.Uid) != os.Getuid() {
		return fmt.Errorf("%s is not owned by the current user; refusing to use it", path)
	}
	if perm := st.Mode().Perm(); perm&forbidden != 0 {
		return fmt.Errorf("%s is accessible to other users (mode %v); refusing to use it", path, perm)
	}
	return nil
}
