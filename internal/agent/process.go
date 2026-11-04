package agent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Firmary/bridge/internal/update"
)

// InstalledExe is the Link executable the person installed: the one the launcher started this process from, or this
// process's own. Login items and services always point at it, never at a version in the data directory.
func InstalledExe() string {
	if p := os.Getenv(update.LaunchedEnv); p != "" && filepath.IsAbs(p) {
		return p
	}
	p, err := os.Executable()
	if err != nil {
		return os.Args[0]
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return p
}

// childEnv is this process's environment with the launcher variables set.
func childEnv(reason string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, update.LaunchedEnv+"=") && !strings.HasPrefix(kv, update.ReasonEnv+"=") {
			env = append(env, kv)
		}
	}
	env = append(env, update.LaunchedEnv+"="+InstalledExe())
	if reason != "" {
		env = append(env, update.ReasonEnv+"="+reason)
	}
	return env
}

// selfTestCommand runs a downloaded Link's self-test on this data directory.
func selfTestCommand(ctx context.Context, path, root string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, path, "self-test", "--data-dir", root)
	cmd.Env = childEnv("")
	hideWindow(cmd)
	return cmd
}

// Restart replaces this process with the Link at path, with the same arguments: the switch to an update.
func Restart(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("restart needs an absolute path")
	}
	return replace(path, childEnv("update"))
}

// Launch runs the Link at path (an installed update the launcher chose) in place of this one.
func Launch(path string) error { return replace(path, childEnv("")) }
