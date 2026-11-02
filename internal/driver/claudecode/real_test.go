//go:build !windows

package claudecode

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Firmary/bridge/internal/driver"
	"github.com/Firmary/bridge/internal/driver/prompt"
	"github.com/Firmary/bridge/internal/protocol"
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

// Canaries for TestRealNoLocalContextReachesAPI. Synthetic values only.
const (
	gitCommitCanary    = "GIT_COMMIT_CANARY_91"
	gitUntrackedCanary = "UNTRACKED_FILE_CANARY_37"
	claudeMDCanary     = "CLAUDE_MD_CANARY_80"
)

// TestRealNoLocalContextReachesAPI runs the real pinned Claude Code (same opt-in as TestRealPinnedClaudeCode) with
// HOME set to a git repository above the profile, as a dotfiles setup would have. Claude Code is pointed at a local
// stub that records every request and answers 400, with a dummy key: no account, no traffic to Anthropic's API.
// Nothing about the repository (commit messages, untracked file names) may appear in any request, and a prompt
// starting with "/" must be sent to the model rather than handled locally. Firmary's system prompt must replace
// Claude Code's built-in one, and no CLAUDE.md (above the profile, or in its config dir) may be read.
func TestRealNoLocalContextReachesAPI(t *testing.T) {
	root := os.Getenv("FIRMARY_HELPER_REAL_CLAUDE")
	if root == "" {
		t.Skip("set FIRMARY_HELPER_REAL_CLAUDE to an install directory to run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	home := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.email=test@example.invalid", "-c", "user.name=test"}, args...)...)
		cmd.Dir = home
		cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home, "GIT_CONFIG_NOSYSTEM=1"}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("commit", "-q", "--allow-empty", "-m", gitCommitCanary)
	if err := os.WriteFile(filepath.Join(home, gitUntrackedCanary), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "CLAUDE.md"), []byte(claudeMDCanary+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var bodies, systems []string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var req struct {
			System json.RawMessage `json:"system"`
		}
		if json.Unmarshal(b, &req) == nil && len(req.System) > 0 {
			mu.Lock()
			systems = append(systems, string(req.System))
			mu.Unlock()
		}
		mu.Lock()
		bodies = append(bodies, r.URL.Path+"\n"+string(b))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"recorded by test stub"}}`)
	}))
	defer stub.Close()

	d, err := New(root, WithEnviron(func() []string { return []string{"PATH=/usr/bin:/bin", "HOME=" + home} }))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Ensure(ctx, BuiltInPin); err != nil {
		t.Fatal(err)
	}
	d.extraEnv = map[string]string{
		"ANTHROPIC_BASE_URL": stub.URL, "ANTHROPIC_API_KEY": "test-dummy-not-a-key",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
	}
	prof := driver.Profile{UserID: "u1", Dir: filepath.Join(home, ".local", "share", "firmary-helper", "prod", "profiles", "u1", "claude-code")}
	// A user memory file in the profile's own config dir must not be read either.
	if err := os.MkdirAll(filepath.Join(prof.Dir, "config"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prof.Dir, "config", "CLAUDE.md"), []byte(claudeMDCanary+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_ = d.Run(ctx, prof, driver.Turn{ID: "real-git", Model: "claude-sonnet-4-5", Prompt: "Reply with the single word: pong",
		MaxOutputTokens: 200}, func(protocol.StreamEvent) {})
	// A prompt that starts with "/" must reach the model as text, not run as a local command.
	_ = d.Run(ctx, prof, driver.Turn{ID: "real-slash", Model: "claude-sonnet-4-5", Prompt: "/logout",
		MaxOutputTokens: 200}, func(protocol.StreamEvent) {})

	mu.Lock()
	all := strings.Join(bodies, "\n")
	mu.Unlock()
	if !strings.Contains(all, "/v1/messages") || !strings.Contains(all, "pong") {
		t.Fatalf("the stub saw no messages request with the prompt; the test proves nothing (%d requests)", len(bodies))
	}
	// Firmary's prompt replaces Claude Code's built-in one. Claude Code still adds its own billing header and a
	// one-line SDK identity block before it; those can't be switched off.
	if len(systems) == 0 {
		t.Fatal("no request carried a system prompt")
	}
	for _, sys := range systems {
		var blocks []struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(sys), &blocks); err != nil || len(blocks) == 0 || blocks[len(blocks)-1].Text != prompt.Default {
			t.Errorf("Firmary's system prompt is not the last system block: %s", sys)
		}
		total := 0
		for _, b := range blocks[:max(len(blocks)-1, 0)] {
			if total += len(b.Text); len(b.Text) > 200 {
				t.Errorf("Claude Code added a long system block of its own: %.200q", b.Text)
			}
		}
		// Claude Code's own default prompt is 6-14 KB, depending on the model; its billing and identity lines are
		// about 140 bytes.
		if total > 400 {
			t.Errorf("Claude Code added %d bytes of system text before Firmary's prompt", total)
		}
	}
	if strings.Contains(all, claudeMDCanary) {
		t.Error("a CLAUDE.md reached the API")
	}
	if !strings.Contains(all, "/logout") {
		t.Error(`the prompt "/logout" did not reach the API as text`)
	}
	for _, c := range []string{gitCommitCanary, gitUntrackedCanary} {
		if strings.Contains(all, c) {
			t.Errorf("%s from the repository above the profile reached the API", c)
		}
	}
}
