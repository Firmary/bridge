//go:build windows

package service

import (
	"errors"
	"os/exec"
	"strings"
	"syscall"

	"golang.org/x/sys/windows/registry"

	"github.com/Firmary/bridge/internal/brand"
)

// The logon task and the Run value are named brand.InstallName; the installer uses the same names.
const runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`

func hidden(cmd *exec.Cmd) *exec.Cmd {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	return cmd
}

func commandLine(c Config) string {
	s := `"` + c.Exe + `"`
	for _, a := range c.args() {
		if strings.ContainsAny(a, " \t") {
			a = `"` + a + `"`
		}
		s += " " + a
	}
	return s
}

// installTask makes a logon task for this user, so the Link starts at sign-in and restarts if it stops. Where
// Windows refuses a logon task without administrator rights, the per-user Run value does the same job.
func installTask(c Config) (Result, error) {
	err := run(hidden(exec.Command("schtasks", "/Create", "/F", "/TN", brand.InstallName, "/SC", "ONLOGON", "/RL", "LIMITED",
		"/TR", commandLine(c))))
	if err != nil {
		if err := setRunKey(commandLine(c), true); err != nil {
			return Result{}, err
		}
	}
	return Result{Path: brand.InstallName, Lingering: false}, startTask(c)
}

func uninstallTask(c Config) error {
	_ = run(hidden(exec.Command("schtasks", "/Delete", "/F", "/TN", brand.InstallName)))
	return nil
}

func queryTask(c Config) Status {
	st := Status{Kind: "logon task", Path: brand.InstallName}
	out, err := hidden(exec.Command("schtasks", "/Query", "/TN", brand.InstallName, "/FO", "LIST")).Output()
	if err == nil {
		st.Installed = true
		st.Running = strings.Contains(string(out), "Running")
	}
	return st
}

func stopTask() error { return run(hidden(exec.Command("schtasks", "/End", "/TN", brand.InstallName))) }

func startTask(cs ...Config) error {
	if err := run(hidden(exec.Command("schtasks", "/Run", "/TN", brand.InstallName))); err == nil || len(cs) == 0 {
		return err
	}
	cmd := exec.Command(cs[0].Exe, cs[0].args()...)
	return cmd.Start()
}

func setRunKey(cmdline string, on bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		if err := k.DeleteValue(brand.InstallName); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return err
		}
		return nil
	}
	return k.SetStringValue(brand.InstallName, cmdline)
}

func runKey() (string, bool) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return "", false
	}
	defer k.Close()
	v, _, err := k.GetStringValue(brand.InstallName)
	return v, err == nil
}
