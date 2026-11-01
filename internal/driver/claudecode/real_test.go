//go:build !windows

package claudecode

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Firmary/firmary-helper/internal/driver"
	"github.com/Firmary/firmary-helper/internal/protocol"
)

// TestRealPinnedClaudeCode downloads the real pinned Claude Code from Anthropic (about 290 MB), verifies it, and
// runs it in an empty profile. Opt in with FIRMARY_HELPER_REAL_CLAUDE=<install dir>; the dir is reused between
// runs. A signed-out profile must report signed out, and a turn must end in a classified auth error with no
// output text. With FIRMARY_HELPER_REAL_PROFILE=<dir> pointing at a profile someone signed in to with their own
// plan, the turn must stream text and finish with done instead.
func TestRealPinnedClaudeCode(t *testing.T) {
	root := os.Getenv("FIRMARY_HELPER_REAL_CLAUDE")
	if root == "" {
		t.Skip("set FIRMARY_HELPER_REAL_CLAUDE to an install directory to run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	d, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Ensure(ctx, BuiltInPin); err != nil {
		t.Fatal(err)
	}
	prof := driver.Profile{UserID: "real", Dir: t.TempDir()}
	signedIn := false
	if p := os.Getenv("FIRMARY_HELPER_REAL_PROFILE"); p != "" {
		prof.Dir, signedIn = p, true
	}
	st, err := d.Status(ctx, prof)
	if err != nil {
		t.Fatal(err)
	}
	if st.Installed != BuiltInPin.Version || st.SignedIn != signedIn {
		t.Fatalf("status %+v", st)
	}

	var events []protocol.StreamEvent
	err = d.Run(ctx, prof, driver.Turn{ID: "real-1", Model: "claude-sonnet-4-5", Prompt: "Reply with the single word: pong",
		MaxOutputTokens: 200}, func(e protocol.StreamEvent) { events = append(events, e) })
	t.Logf("events: %+v", events)
	last := events[len(events)-1]
	if signedIn {
		if err != nil || last.T != protocol.EvDone {
			t.Fatalf("signed-in turn failed: %v", err)
		}
		return
	}
	var f *driver.Failure
	if !errors.As(err, &f) || f.Class != protocol.ClassAuth || last.T != protocol.EvError {
		t.Fatalf("got %v, want an auth failure", err)
	}
	for _, e := range events {
		if e.T == protocol.EvTextDelta {
			t.Fatalf("signed-out turn emitted text %q", e.Text)
		}
	}
}
