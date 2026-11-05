// Package datadir is where the Link keeps its device key, pairing, logs, provider copies, sign-ins and updates, and
// moves the data of an install from before the rename (the beta under brand.FormerName) to it, so a paired computer stays
// paired.
package datadir

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"

	"github.com/Firmary/bridge/internal/brand"
)

// Paths returns the data directory and the one the former name used, for this OS and the given environment.
// getenv and home are passed so tests can run any OS's rules.
func Paths(goos, home string, getenv func(string) string) (current, former string) {
	switch goos {
	case "darwin":
		base := filepath.Join(home, "Library", "Application Support")
		return filepath.Join(base, brand.InstallName), filepath.Join(base, brand.FormerName)
	case "windows":
		if d := getenv("LOCALAPPDATA"); d != "" {
			return filepath.Join(d, "Firmary", "Link"), filepath.Join(d, "Firmary", "Helper")
		}
	}
	base := filepath.Join(home, ".local", "share")
	if d := getenv("XDG_DATA_HOME"); d != "" {
		base = d
	}
	return filepath.Join(base, brand.Command), filepath.Join(base, brand.FormerCommand)
}

// Default returns this computer's data directory, after moving a former install's data to it if there is one.
func Default() string {
	home, _ := os.UserHomeDir()
	current, former := Paths(runtime.GOOS, home, os.Getenv)
	return Migrate(current, former)
}

// Migrate moves former to current when current doesn't exist yet and former does, and returns the directory to use:
// current, or former if it couldn't be moved (so an install that can't be moved still finds its pairing). It never
// merges or overwrites: once current exists, former is left alone.
func Migrate(current, former string) string {
	if _, err := os.Lstat(current); !errors.Is(err, fs.ErrNotExist) {
		return current
	}
	fi, err := os.Lstat(former)
	if err != nil || !fi.IsDir() {
		return current
	}
	if err := os.MkdirAll(filepath.Dir(current), 0o700); err != nil {
		return former
	}
	if err := os.Rename(former, current); err != nil {
		return former
	}
	// The log was helper.log; the new name starts a fresh file next to the old ones.
	return current
}
