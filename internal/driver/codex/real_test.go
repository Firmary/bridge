//go:build !windows

package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/Firmary/bridge/internal/protocol"
	"github.com/Firmary/bridge/internal/testkit"
)

// TestRealPinnedCodex downloads the real pinned Codex from OpenAI's GitHub release (about 120 MB), verifies it, and
// checks an empty profile. Opt in with FIRMARY_HELPER_REAL_CODEX=<install dir>; the dir is reused between runs. A
// signed-out profile must report signed out, and a turn must end in a classified auth error with no output text.
// With FIRMARY_HELPER_REAL_CODEX_PROFILE=<dir> pointing at a profile someone signed in to with their own ChatGPT
// plan, the turn must stream text and finish with done instead.
func TestRealPinnedCodex(t *testing.T) {
	root := os.Getenv("FIRMARY_HELPER_REAL_CODEX")
	if root == "" {
		t.Skip("set FIRMARY_HELPER_REAL_CODEX to an install directory to run")
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
	prof := driver.Profile{UserID: "real", Dir: realProfileDir(t)}
	signedIn := false
	if p := os.Getenv("FIRMARY_HELPER_REAL_CODEX_PROFILE"); p != "" {
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
	err = d.Run(ctx, prof, driver.Turn{ID: "real-1", Model: "gpt-5.5", Prompt: "Reply with the single word: pong",
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

// realProfileDir is outside /tmp: Codex refuses to set up its helper aliases under a temporary directory.
func realProfileDir(t *testing.T) string {
	t.Helper()
	base, _ := os.UserCacheDir()
	dir, err := os.MkdirTemp(base, "firmary-codex-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// Canaries for TestRealNothingActsOnComputer. Synthetic values only.
const (
	agentsCanary     = "AGENTS_MD_CANARY_52"
	skillCanary      = "SKILL_CANARY_18"
	gitCommitCanary  = "GIT_COMMIT_CANARY_64"
	projectMCPMarker = "MCP_RAN_CANARY"
)

// TestRealNothingActsOnComputer runs the real pinned Codex (same opt-in as TestRealPinnedCodex) against a local
// stub of OpenAI's Responses API that records every request, with a dummy key: no account, no traffic to OpenAI.
// HOME is a git repository holding an AGENTS.md, a user skill and a .codex/config.toml with an MCP server, as a
// person's own setup would. The stub first asks Codex to write files with apply_patch, the one tool Codex always
// offers. Then: the only tool offered is apply_patch, no instructions, skills or repository details reach the API,
// the MCP server never starts, no file is written, and the prompt is saved nowhere in the profile.
func TestRealNothingActsOnComputer(t *testing.T) {
	root := os.Getenv("FIRMARY_HELPER_REAL_CODEX")
	if root == "" {
		t.Skip("set FIRMARY_HELPER_REAL_CODEX to an install directory to run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	home := realProfileDir(t)
	mustWrite := func(rel, body string) {
		t.Helper()
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mcpRan := filepath.Join(home, projectMCPMarker)
	mustWrite("AGENTS.md", agentsCanary+"\n")
	mustWrite(".agents/skills/canary/SKILL.md", "---\nname: canary\ndescription: "+skillCanary+"\n---\n"+skillCanary+"\n")
	mustWrite(".codex/config.toml", fmt.Sprintf("[mcp_servers.canary]\ncommand = \"/bin/sh\"\nargs = [\"-c\", \"touch %s\"]\n", mcpRan))
	git := exec.Command("sh", "-c", "git init -q && git -c user.email=t@example.invalid -c user.name=t commit -q --allow-empty -m "+gitCommitCanary)
	git.Dir = home
	git.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home, "GIT_CONFIG_NOSYSTEM=1"}
	if out, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git: %v\n%s", err, out)
	}
	prof := driver.Profile{UserID: "u1", Dir: filepath.Join(home, ".local", "share", "firmary-helper", "prod", "profiles", "u1", "codex")}
	writeTargets := []string{filepath.Join(home, "PWNED_HOME"), "PWNED_WORK"}

	var mu sync.Mutex
	var bodies []string
	var tools [][]string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, r.URL.Path+"\n"+string(b))
		mu.Unlock()
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/responses") {
			http.Error(w, `{"error":{"message":"stub"}}`, http.StatusNotFound)
			return
		}
		var req struct {
			Tools []struct {
				Name string `json:"name"`
				Type string `json:"type"`
			} `json:"tools"`
		}
		json.Unmarshal(b, &req)
		var names []string
		for _, tl := range req.Tools {
			names = append(names, tl.Name+tl.Type)
		}
		mu.Lock()
		tools = append(tools, names)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		ev := func(v map[string]any) {
			j, _ := json.Marshal(v)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", v["type"], j)
		}
		usage := map[string]any{"input_tokens": 5, "input_tokens_details": map[string]any{"cached_tokens": 0},
			"output_tokens": 1, "output_tokens_details": map[string]any{"reasoning_tokens": 0}, "total_tokens": 6}
		var item map[string]any
		if !strings.Contains(string(b), "custom_tool_call_output") {
			patch := "*** Begin Patch\n"
			for _, p := range writeTargets {
				patch += "*** Add File: " + p + "\n+pwned\n"
			}
			item = map[string]any{"type": "custom_tool_call", "id": "ctc_1", "call_id": "call_1", "name": "apply_patch",
				"input": patch + "*** End Patch\n", "status": "completed"}
		} else {
			item = map[string]any{"type": "message", "id": "msg_1", "role": "assistant", "status": "completed",
				"content": []any{map[string]any{"type": "output_text", "text": "pong", "annotations": []any{}}}}
		}
		ev(map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_1"}})
		ev(map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item})
		ev(map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_1", "status": "completed", "usage": usage}})
	}))
	defer stub.Close()

	d, err := New(root, WithEnviron(func() []string { return []string{"PATH=/usr/bin:/bin", "HOME=" + home} }))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Ensure(ctx, BuiltInPin); err != nil {
		t.Fatal(err)
	}
	d.extraConfig = []string{
		`model_provider="firmary_stub"`, `model_providers.firmary_stub.name="stub"`,
		`model_providers.firmary_stub.base_url="` + stub.URL + `/v1"`, `model_providers.firmary_stub.env_key="FIRMARY_STUB_KEY"`,
		`model_providers.firmary_stub.wire_api="responses"`,
	}
	d.extraEnv = []string{"FIRMARY_STUB_KEY=test-dummy-not-a-key"}

	var events []protocol.StreamEvent
	err = d.Run(ctx, prof, driver.Turn{ID: "real-local", Model: "gpt-5.5", Prompt: "Reply with the single word: " + testkit.PromptCanary,
		MaxOutputTokens: 200}, func(e protocol.StreamEvent) { events = append(events, e) })
	t.Logf("events: %+v err: %v", events, err)

	mu.Lock()
	all := strings.Join(bodies, "\n")
	seenTools := tools
	mu.Unlock()
	if !strings.Contains(all, testkit.PromptCanary) || len(seenTools) < 2 {
		t.Fatalf("the stub didn't see the turn and the tool result; the test proves nothing (%d requests)", len(bodies))
	}
	if err != nil || events[len(events)-1].T != protocol.EvDone {
		t.Errorf("turn failed: %v", err)
	}
	for _, ts := range seenTools {
		for _, name := range ts {
			if name != "apply_patchcustom" {
				t.Errorf("tool offered to the model: %s", name)
			}
		}
	}
	if !strings.Contains(all, "writing is blocked by read-only sandbox") {
		t.Error("apply_patch was not rejected by the sandbox")
	}
	for _, c := range []string{agentsCanary, skillCanary, gitCommitCanary, "<environment_context>", "<skills_instructions>"} {
		if strings.Contains(all, c) {
			t.Errorf("%q from this computer reached the API", c)
		}
	}
	for _, p := range append([]string{mcpRan, filepath.Join(home, "PWNED_HOME")}, filepath.Join(prof.Dir, "work", "PWNED_WORK")) {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s was created", p)
		}
	}
	if saved := testkit.ReadDir(t, prof.Dir); strings.Contains(saved, testkit.PromptCanary) || strings.Contains(saved, "pong") {
		t.Error("the turn was saved in the profile")
	}
}
