package service

import (
	"encoding/xml"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Firmary/bridge/internal/brand"
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
		_ = exec.Command("launchctl", "bootout", launchdTarget()).Run()
		if err := run(exec.Command("launchctl", "bootstrap", launchdDomain(), path)); err != nil {
			return Result{}, err
		}
	}
	return Result{Path: path, Lingering: true}, nil
}

func uninstallLaunchd(c Config) error {
	_ = exec.Command("launchctl", "bootout", launchdTarget()).Run()
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
	out, err := exec.Command("launchctl", "print", launchdTarget()).Output()
	st.Running = err == nil && strings.Contains(string(out), "state = running")
	return st
}
