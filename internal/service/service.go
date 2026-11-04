// Package service starts the Link with the computer: as an always-on service (a systemd unit on Linux, a launchd
// agent on macOS, a logon task on Windows) and as the desktop app's "Open at login" item.
package service

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/Firmary/bridge/internal/brand"
)

// Config says what the service runs.
type Config struct {
	Exe     string   // the installed Link executable
	Args    []string // arguments, e.g. ["run"]
	Home    string   // the user's home directory
	Root    bool     // running as root (Linux): a system unit instead of a user unit
	Env     []string // extra environment, KEY=VALUE
	DataDir string   // non-default data directory, passed as --data-dir
}

// UnitName is the systemd unit.
const UnitName = brand.Command + ".service"

// Status is what Status reports.
type Status struct {
	Installed bool
	Running   bool
	Kind      string // "systemd user unit", "systemd system unit", "launchd agent", "logon task", "login item"
	Path      string
	Detail    string
}

// Memory limits of the systemd unit. The Link itself is small; a turn runs Claude Code or Codex under it.
const (
	MemoryHigh = "1536M"
	MemoryMax  = "2G"
)

func quoteSystemd(s string) string {
	if !strings.ContainsAny(s, " \t\"'\\") {
		return s
	}
	return strconv.Quote(s)
}

func (c Config) args() []string {
	a := append([]string{}, c.Args...)
	if c.DataDir != "" {
		a = append(a, "--data-dir", c.DataDir)
	}
	return a
}

// SystemdUnit is the unit file text.
func SystemdUnit(c Config) string {
	var b strings.Builder
	exec := quoteSystemd(c.Exe)
	for _, a := range c.args() {
		exec += " " + quoteSystemd(a)
	}
	fmt.Fprintf(&b, "[Unit]\nDescription=%s (connects your own Claude or ChatGPT plan to Firmary)\n", brand.Name)
	b.WriteString("Documentation=https://github.com/Firmary/bridge\nAfter=network-online.target\nWants=network-online.target\n\n")
	b.WriteString("[Service]\nType=simple\n")
	fmt.Fprintf(&b, "ExecStart=%s\n", exec)
	b.WriteString("Restart=always\nRestartSec=5\n")
	fmt.Fprintf(&b, "MemoryHigh=%s\nMemoryMax=%s\nTasksMax=512\nNoNewPrivileges=yes\n", MemoryHigh, MemoryMax)
	if c.Root {
		fmt.Fprintf(&b, "Environment=HOME=%s\n", quoteSystemd(c.Home))
	}
	for _, e := range c.Env {
		fmt.Fprintf(&b, "Environment=%s\n", quoteSystemd(e))
	}
	target := "default.target"
	if c.Root {
		target = "multi-user.target"
	}
	fmt.Fprintf(&b, "\n[Install]\nWantedBy=%s\n", target)
	return b.String()
}

// unitPath is where the systemd unit goes: the user's own units, or the system's when root.
func unitPath(c Config) string {
	if c.Root {
		return filepath.Join("/etc/systemd/system", UnitName)
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(c.Home, ".config")
	}
	return filepath.Join(base, "systemd", "user", UnitName)
}

func systemctl(c Config, args ...string) *exec.Cmd {
	if !c.Root {
		args = append([]string{"--user"}, args...)
	}
	return exec.Command("systemctl", args...)
}

func run(cmd *exec.Cmd) error {
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return fmt.Errorf("%s: %v %s", strings.Join(cmd.Args, " "), err, msg)
	}
	return nil
}

// Result says what Install did, for the person.
type Result struct {
	Path string
	// Lingering is true when a Linux user unit keeps running after the person logs out.
	Lingering bool
}

// Install installs and starts the always-on service.
func Install(c Config) (Result, error) {
	switch runtime.GOOS {
	case "linux":
		return installSystemd(c)
	case "darwin":
		return installLaunchd(c, true)
	case "windows":
		return installTask(c)
	}
	return Result{}, fmt.Errorf("not supported on %s", runtime.GOOS)
}

// Uninstall stops and removes the always-on service.
func Uninstall(c Config) error {
	switch runtime.GOOS {
	case "linux":
		return uninstallSystemd(c)
	case "darwin":
		return uninstallLaunchd(c)
	case "windows":
		return uninstallTask(c)
	}
	return fmt.Errorf("not supported on %s", runtime.GOOS)
}

// Query reports the always-on service.
func Query(c Config) Status {
	switch runtime.GOOS {
	case "linux":
		return querySystemd(c)
	case "darwin":
		return queryLaunchd(c)
	case "windows":
		return queryTask(c)
	}
	return Status{}
}

// Stop and Start stop and start an installed service (for pairing in a terminal while it would hold the
// connection). They do nothing when no service is installed.
func Stop(c Config) error {
	if !Query(c).Running {
		return nil
	}
	switch runtime.GOOS {
	case "linux":
		return run(systemctl(c, "stop", UnitName))
	case "darwin":
		return run(exec.Command("launchctl", "bootout", launchdTarget()))
	case "windows":
		return stopTask()
	}
	return nil
}

// Start starts an installed service that isn't running.
func Start(c Config) error {
	st := Query(c)
	if !st.Installed || st.Running {
		return nil
	}
	switch runtime.GOOS {
	case "linux":
		return run(systemctl(c, "start", UnitName))
	case "darwin":
		return run(exec.Command("launchctl", "bootstrap", launchdDomain(), launchdPlist(c.Home)))
	case "windows":
		return startTask()
	}
	return nil
}

func installSystemd(c Config) (Result, error) {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return Result{}, errors.New("systemd isn't available here; run `firmary-link run` under your own supervisor instead")
	}
	path := unitPath(c)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Result{}, err
	}
	if err := os.WriteFile(path, []byte(SystemdUnit(c)), 0o644); err != nil {
		return Result{}, err
	}
	if err := run(systemctl(c, "daemon-reload")); err != nil {
		return Result{}, userBusHint(c, err)
	}
	// An absolute path also works when the unit file is outside systemd's search path (another HOME).
	if err := run(systemctl(c, "enable", path)); err != nil {
		return Result{}, err
	}
	if err := run(systemctl(c, "restart", UnitName)); err != nil {
		return Result{}, err
	}
	res := Result{Path: path, Lingering: c.Root}
	if !c.Root {
		// Keep the user's services running after they log out (and start them at boot).
		user := os.Getenv("USER")
		if user == "" {
			user = strconv.Itoa(os.Getuid())
		}
		_ = run(exec.Command("loginctl", "enable-linger", user))
		out, _ := exec.Command("loginctl", "show-user", user, "--property=Linger", "--value").Output()
		res.Lingering = strings.TrimSpace(string(out)) == "yes"
	}
	return res, nil
}

func userBusHint(c Config, err error) error {
	if c.Root || os.Getenv("XDG_RUNTIME_DIR") != "" {
		return err
	}
	return fmt.Errorf("%w (log in to this user directly over SSH, not with su, so systemd starts your user session)", err)
}

func uninstallSystemd(c Config) error {
	path := unitPath(c)
	_ = run(systemctl(c, "disable", "--now", UnitName))
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return run(systemctl(c, "daemon-reload"))
}

func querySystemd(c Config) Status {
	kind := "systemd user unit"
	if c.Root {
		kind = "systemd system unit"
	}
	st := Status{Kind: kind, Path: unitPath(c)}
	if _, err := os.Stat(st.Path); err == nil {
		st.Installed = true
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		return st
	}
	out, _ := systemctl(c, "is-active", UnitName).Output()
	st.Detail = strings.TrimSpace(string(out))
	st.Running = st.Detail == "active"
	return st
}
