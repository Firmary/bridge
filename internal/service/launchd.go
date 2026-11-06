package service

import (
	"encoding/xml"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/Firmary/bridge/internal/brand"
	"github.com/Firmary/bridge/internal/proc"
)

func launchdDomain() string { return "gui/" + strconv.Itoa(os.Getuid()) }
func launchdTarget() string { return launchdDomain() + "/" + brand.BundleID }

func launchdPlist(home string) string {
	return filepath.Join(home, "Library", "LaunchAgents", brand.BundleID+".plist")
}

func xmlText(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// LaunchdPlist is the launchd agent. keepAlive restarts the Link if it crashes (not when the person quits it).
func LaunchdPlist(c Config, keepAlive bool) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + brand.BundleID + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + xmlText(c.Exe) + `</string>
`)
	for _, a := range c.args() {
		b.WriteString("\t\t<string>" + xmlText(a) + "</string>\n")
	}
	b.WriteString("\t</array>\n\t<key>RunAtLoad</key>\n\t<true/>\n\t<key>ProcessType</key>\n\t<string>Interactive</string>\n")
	if keepAlive {
		b.WriteString("\t<key>KeepAlive</key>\n\t<dict>\n\t\t<key>SuccessfulExit</key>\n\t\t<false/>\n\t</dict>\n")
	}
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

func installLaunchd(c Config, start bool) (Result, error) {
	path := launchdPlist(c.Home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Result{}, err
	}
	if err := os.WriteFile(path, []byte(LaunchdPlist(c, start)), 0o644); err != nil {
		return Result{}, err
	}
	if start {
		_ = proc.Command("launchctl", "bootout", launchdTarget()).Run()
		if err := run(proc.Command("launchctl", "bootstrap", launchdDomain(), path)); err != nil {
			return Result{}, err
		}
	}
	return Result{Path: path, Lingering: true}, nil
}

func uninstallLaunchd(c Config) error {
	_ = proc.Command("launchctl", "bootout", launchdTarget()).Run()
	if err := os.Remove(launchdPlist(c.Home)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func queryLaunchd(c Config) Status {
	st := Status{Kind: "launchd agent", Path: launchdPlist(c.Home)}
	if b, err := os.ReadFile(st.Path); err == nil {
		st.Installed = true
		if !strings.Contains(string(b), "<key>KeepAlive</key>") {
			st.Kind = "login item"
		}
	}
	out, err := proc.Command("launchctl", "print", launchdTarget()).Output()
	st.Running = err == nil && strings.Contains(string(out), "state = running")
	return st
}

// Repoint points the macOS launchd agent (the login item or the always-on agent) at c.Exe when the executable it
// starts is gone. A Mac that had the app under its former name (brand.InstallName) and replaced it with the app under
// brand.Name keeps opening at login once the old copy is deleted. An agent whose executable is still there is left
// alone.
func Repoint(c Config) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	return repointLaunchd(launchdPlist(c.Home), c.Exe)
}

func repointLaunchd(path, exe string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	plist := string(b)
	old, ok := launchdExe(plist)
	if !ok || old == exe {
		return nil
	}
	if _, err := os.Stat(old); !errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	plist = strings.Replace(plist, "<string>"+xmlText(old)+"</string>", "<string>"+xmlText(exe)+"</string>", 1)
	return os.WriteFile(path, []byte(plist), 0o644)
}

// launchdExe reads the executable, the first ProgramArguments entry, from a plist LaunchdPlist wrote.
func launchdExe(plist string) (string, bool) {
	_, rest, ok := strings.Cut(plist, "<key>ProgramArguments</key>")
	if !ok {
		return "", false
	}
	_, rest, ok = strings.Cut(rest, "<string>")
	if !ok {
		return "", false
	}
	escaped, _, ok := strings.Cut(rest, "</string>")
	if !ok {
		return "", false
	}
	var exe string
	if err := xml.Unmarshal([]byte("<s>"+escaped+"</s>"), &exe); err != nil {
		return "", false
	}
	return exe, true
}
