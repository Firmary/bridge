//go:build !windows

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// The background service, for a Linux server without systemd (a Docker container, WSL without systemd, a small VPS
// image): a supervisor process, detached from the terminal, that runs the Link and starts it again whenever it
// stops, as systemd's Restart=always would. With cron it also starts again when the server restarts. Its files live
// in the data directory:
//
//	background.json  what it runs (the install record; written by Install, removed by Uninstall)
//	background.lock  held by the running supervisor, with its process id in it
//	background.log   the Link's output (what a unit would send to the journal)

const (
	backgroundRecord = "background.json"
	backgroundLock   = "background.lock"
	backgroundLog    = "background.log"
	// cronMarker ends the crontab line Install adds, so Uninstall (and a reinstall) finds it.
	cronMarker = "# firmary-link background"
	// logLimit is how big background.log grows before the supervisor starts it afresh.
	logLimit = 1 << 20
)

// restartDelay is how long the supervisor waits before starting the Link again: short after a long run, doubling
// (up to a minute) while it keeps stopping straight away. A variable for tests.
var restartDelay = 5 * time.Second

type backgroundCmd struct {
	Exe  string   `json:"exe"`
	Args []string `json:"args"`
	Env  []string `json:"env,omitempty"`
}

func readRecord(dir string) (backgroundCmd, error) {
	var r backgroundCmd
	b, err := os.ReadFile(filepath.Join(dir, backgroundRecord))
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(b, &r); err != nil || r.Exe == "" {
		return r, fmt.Errorf("%s is damaged; run service install again", backgroundRecord)
	}
	return r, nil
}

// tryLock takes the supervisor lock. ok is false when a running supervisor holds it.
func tryLock(dir string) (f *os.File, ok bool, err error) {
	f, err = os.OpenFile(filepath.Join(dir, backgroundLock), os.O_CREATE|os.O_RDWR, 0o600)
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
	return f, true, nil
}

// supervisorRunning reports whether a supervisor holds the lock in dir, and its process id.
func supervisorRunning(dir string) (pid int, running bool) {
	f, ok, err := tryLock(dir)
	if err != nil {
		return 0, false
	}
	if ok {
		f.Close()
		return 0, false
	}
	b, _ := os.ReadFile(filepath.Join(dir, backgroundLock))
	pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	return pid, true
}

func installBackground(c Config) (Result, error) {
	dir := c.dataDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Result{}, err
	}
	rec := backgroundCmd{Exe: c.Exe, Args: c.args(), Env: c.Env}
	b, _ := json.MarshalIndent(rec, "", "  ")
	path := filepath.Join(dir, backgroundRecord)
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		return Result{}, err
	}
	// A reinstall runs the new command: stop the supervisor that runs the old one.
	if err := stopBackground(c); err != nil {
		return Result{}, err
	}
	if err := startBackground(c); err != nil {
		return Result{}, err
	}
	return Result{Path: path, Lingering: true, Background: true, AtBoot: cronRunning() && addCron(c.Exe, dir) == nil}, nil
}

func uninstallBackground(c Config) error {
	dir := c.dataDir()
	if err := stopBackground(c); err != nil {
		return err
	}
	_ = removeCron()
	if err := os.Remove(filepath.Join(dir, backgroundRecord)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func backgroundInstalled(c Config) bool {
	_, err := os.Stat(filepath.Join(c.dataDir(), backgroundRecord))
	return err == nil
}

func queryBackground(c Config) Status {
	dir := c.dataDir()
	st := Status{Kind: "background process", Path: filepath.Join(dir, backgroundRecord), Installed: backgroundInstalled(c)}
	_, st.Running = supervisorRunning(dir)
	st.Detail = "inactive"
	if st.Running {
		st.Detail = "active"
	}
	return st
}

// startBackground starts the supervisor, detached from this terminal and session, and waits until it holds its lock.
func startBackground(c Config) error {
	dir := c.dataDir()
	rec, err := readRecord(dir)
	if err != nil {
		return err
	}
	if _, running := supervisorRunning(dir); running {
		return nil
	}
	cmd := exec.Command(rec.Exe, SuperviseCommand, dir)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if _, running := supervisorRunning(dir); running {
			return nil
		}
		select {
		case err := <-exited:
			if _, running := supervisorRunning(dir); running {
				return nil // another supervisor won the lock
			}
			return fmt.Errorf("the background process stopped at once (%v); see %s", err, filepath.Join(dir, backgroundLog))
		case <-time.After(50 * time.Millisecond):
		}
	}
	return errors.New("the background process didn't start")
}

// stopBackground stops a running supervisor (and the Link under it) and waits until it is gone.
func stopBackground(c Config) error {
	dir := c.dataDir()
	pid, running := supervisorRunning(dir)
	if !running {
		return nil
	}
	if pid > 1 {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if _, running := supervisorRunning(dir); !running {
			return nil
		}
	}
	return errors.New("the background process didn't stop")
}

// Supervise is the background supervisor (the hidden `supervise DIR` command): it runs the Link the install record in
// dir names, and starts it again whenever it stops, until ctx ends. A supervisor that is already running wins: then
// Supervise returns at once.
func Supervise(ctx context.Context, dir string) error {
	rec, err := readRecord(dir)
	if err != nil {
		return err
	}
	lock, ok, err := tryLock(dir)
	if err != nil || !ok {
		return err
	}
	defer lock.Close()
	_ = lock.Truncate(0)
	_, _ = lock.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)

	delay := restartDelay
	for {
		started := time.Now()
		superviseOnce(ctx, dir, rec)
		if ctx.Err() != nil {
			return nil
		}
		if time.Since(started) > time.Minute {
			delay = restartDelay
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(delay):
		}
		delay = min(delay*2, time.Minute)
	}
}

// superviseOnce runs the Link until it exits, or until ctx ends, when it asks the Link to stop (and kills it if it
// doesn't within ten seconds).
func superviseOnce(ctx context.Context, dir string, rec backgroundCmd) {
	log := openLog(filepath.Join(dir, backgroundLog))
	if log != nil {
		defer log.Close()
	}
	cmd := exec.Command(rec.Exe, rec.Args...)
	cmd.Env = append(os.Environ(), rec.Env...)
	if log != nil {
		cmd.Stdout, cmd.Stderr = log, log
	}
	if err := cmd.Start(); err != nil {
		if log != nil {
			fmt.Fprintf(log, "couldn't start: %v\n", err)
		}
		return
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	}
}

// openLog opens the background log for appending, starting it afresh once it passes logLimit.
func openLog(path string) *os.File {
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if fi, err := os.Stat(path); err == nil && fi.Size() > logLimit {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return nil
	}
	return f
}

// shellQuote quotes s for a POSIX shell (cron runs its lines with /bin/sh).
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// cronLine is the crontab line that starts the supervisor when the server restarts.
func cronLine(exe, dir string) string {
	return "@reboot " + shellQuote(exe) + " " + SuperviseCommand + " " + shellQuote(dir) + " >/dev/null 2>&1 " + cronMarker
}

// withCron returns the crontab text with this Link's line replaced by line (removed when line is empty).
func withCron(crontab, line string) string {
	var out []string
	for _, l := range strings.Split(crontab, "\n") {
		if l != "" && !strings.HasSuffix(l, cronMarker) {
			out = append(out, l)
		}
	}
	if line != "" {
		out = append(out, line)
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, "\n") + "\n"
}

// cronRunning reports whether a cron daemon runs here, so an @reboot line would be run (WSL, say, has cron installed
// but doesn't start it).
func cronRunning() bool {
	comms, _ := filepath.Glob("/proc/[0-9]*/comm")
	for _, p := range comms {
		b, _ := os.ReadFile(p)
		switch strings.TrimSpace(string(b)) {
		case "cron", "crond", "busybox crond":
			return true
		}
	}
	return false
}

// addCron makes the supervisor start when the server restarts, with the user's crontab. It fails when cron isn't
// installed.
func addCron(exe, dir string) error { return setCron(cronLine(exe, dir)) }

func removeCron() error { return setCron("") }

func setCron(line string) error {
	if _, err := exec.LookPath("crontab"); err != nil {
		return err
	}
	// `crontab -l` fails when the user has no crontab yet: that is an empty one.
	current, _ := exec.Command("crontab", "-l").Output()
	next := withCron(string(current), line)
	if next == string(current) {
		return nil
	}
	cmd := exec.Command("crontab", "-")
	cmd.Stdin = strings.NewReader(next)
	return run(cmd)
}
