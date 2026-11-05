//go:build !windows

package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The supervisor runs the recorded command, starts it again when it stops, holds its lock while it runs, and stops
// the command with it.
func TestSuperviseRestartsAndStops(t *testing.T) {
	restartDelay = 10 * time.Millisecond
	t.Cleanup(func() { restartDelay = 5 * time.Second })
	dir := t.TempDir()
	runs := filepath.Join(dir, "runs")
	// Exits at once the first two times, then keeps running like the Link.
	script := `echo run >> "$1"; [ "$(wc -l < "$1")" -ge 3 ] && exec sleep 60; exit 1`
	rec, _ := json.Marshal(backgroundCmd{Exe: "/bin/sh", Args: []string{"-c", script, "sh", runs}})
	if err := os.WriteFile(filepath.Join(dir, backgroundRecord), rec, 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Supervise(ctx, dir) }()
	waitFor(t, func() bool { b, _ := os.ReadFile(runs); return strings.Count(string(b), "run") == 3 })
	pid, running := supervisorRunning(dir)
	if !running || pid != os.Getpid() {
		t.Fatalf("supervisorRunning = %d, %v; want this process", pid, running)
	}
	if st := queryBackground(Config{DataDir: dir}); !st.Installed || !st.Running || st.Kind != "background process" {
		t.Fatalf("queryBackground = %+v", st)
	}
	// A second supervisor leaves the running one alone.
	if err := Supervise(context.Background(), dir); err != nil {
		t.Fatal(err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Supervise didn't stop")
	}
	if _, running := supervisorRunning(dir); running {
		t.Fatal("lock still held after stop")
	}
}

func TestCrontabLine(t *testing.T) {
	line := cronLine("/usr/local/bin/firmary-link", "/root/it's here")
	want := `@reboot '/usr/local/bin/firmary-link' supervise '/root/it'\''s here' >/dev/null 2>&1 # firmary-link background`
	if line != want {
		t.Fatalf("cronLine = %q", line)
	}
	mine := "@reboot /old/firmary-link supervise /x >/dev/null 2>&1 " + cronMarker
	if got := withCron("MAILTO=me\n0 * * * * backup\n"+mine+"\n", line); got != "MAILTO=me\n0 * * * * backup\n"+line+"\n" {
		t.Fatalf("withCron replaced to %q", got)
	}
	if got := withCron("0 * * * * backup\n"+mine+"\n", ""); got != "0 * * * * backup\n" {
		t.Fatalf("withCron removed to %q", got)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatal("timed out")
}
