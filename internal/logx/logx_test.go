package logx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFreeTextCannotReachTheLog(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	canary := "CANARY prompt text sk-ant-oat01-secret"
	l.Log(TurnError, Fields{TicketID: canary, Model: canary, Pattern: canary, Reason: canary, ErrorClass: canary,
		Windows: []Window{{Name: canary, ResetsAt: canary}}})
	l.Log(Event(canary), Fields{})
	l.Log(TurnDone, Fields{TicketID: "t_123", Model: "claude-sonnet-4-5", OutputTokens: 7})
	l.Close()

	b, err := os.ReadFile(filepath.Join(dir, fileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "CANARY") || strings.Contains(string(b), "sk-ant") {
		t.Fatalf("free text reached the log:\n%s", b)
	}
	if !strings.Contains(string(b), `"ticketId":"t_123"`) {
		t.Fatalf("metadata missing:\n%s", b)
	}
	if st, _ := os.Stat(filepath.Join(dir, fileName)); st.Mode().Perm() != 0o600 {
		t.Fatalf("log mode %v, want 0600", st.Mode().Perm())
	}
}

func TestRotatesAtFiveFilesOfOneMB(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 60000; i++ {
		l.Log(TurnDone, Fields{TicketID: "t_0123456789abcdef", Model: "claude-sonnet-4-5", OutputTokens: 12345})
	}
	l.Close()
	entries, _ := os.ReadDir(dir)
	if len(entries) != Files {
		t.Fatalf("got %d files, want %d", len(entries), Files)
	}
	for _, e := range entries {
		info, _ := e.Info()
		if info.Size() > MaxFileBytes {
			t.Fatalf("%s is %d bytes", e.Name(), info.Size())
		}
	}
}
