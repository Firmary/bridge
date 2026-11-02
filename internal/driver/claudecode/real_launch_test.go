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
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Firmary/bridge/internal/driver"
	"github.com/Firmary/bridge/internal/driver/prompt"
	"github.com/Firmary/bridge/internal/launch"
	"github.com/Firmary/bridge/internal/protocol"
	"github.com/Firmary/bridge/internal/testkit"
)

// TestRealLaunchProfile runs the real pinned Claude Code (same opt-in as TestRealPinnedClaudeCode) against a local
// stub that records each request and answers 400, with a dummy key: no account, no traffic to Anthropic. It checks
// each launch profile knob on the pin: with the built-in profile no tool is offered and Firmary's default prompt is
// the system prompt; with web search on, WebSearch is the only tool, the effort is sent, and the profile's prompt
// replaces Claude Code's; with replaceBuiltinInstructions false, Claude Code's own prompt stays and the profile's is
// appended.
func TestRealLaunchProfile(t *testing.T) {
	root := os.Getenv("FIRMARY_HELPER_REAL_CLAUDE")
	if root == "" {
		t.Skip("set FIRMARY_HELPER_REAL_CLAUDE to an install directory to run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	type request struct {
		System []struct {
			Text string `json:"text"`
		} `json:"system"`
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
		OutputConfig struct {
			Effort string `json:"effort"`
		} `json:"output_config"`
	}
	var mu sync.Mutex
	var reqs []request
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var req request
		if strings.HasSuffix(r.URL.Path, "/v1/messages") && json.Unmarshal(b, &req) == nil {
			mu.Lock()
			reqs = append(reqs, req)
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"recorded by test stub"}}`)
	}))
	defer stub.Close()

	home := t.TempDir()
	d, err := New(root, WithEnviron(func() []string { return []string{"PATH=/usr/bin:/bin", "HOME=" + home} }))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Ensure(ctx, BuiltInPin); err != nil {
		t.Fatal(err)
	}
	d.extraEnv = map[string]string{"ANTHROPIC_BASE_URL": stub.URL, "ANTHROPIC_API_KEY": "test-dummy-not-a-key",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1"}
	prof := driver.Profile{UserID: "u1", Dir: filepath.Join(home, "profile")}

	run := func(profile string) []request {
		t.Helper()
		l, err := launch.Parse(profile)
		if err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		reqs = nil
		mu.Unlock()
		_ = d.Run(ctx, prof, driver.Turn{ID: "real-launch", Model: "claude-sonnet-4-6", Prompt: "Reply with the single word: pong",
			MaxOutputTokens: 200, Launch: l}, func(protocol.StreamEvent) {})
		mu.Lock()
		defer mu.Unlock()
		if len(reqs) == 0 {
			t.Fatalf("%s: the stub saw no messages request; the test proves nothing", profile)
		}
		return slices.Clone(reqs)
	}
	toolNames := func(r request) []string {
		var names []string
		for _, tl := range r.Tools {
			names = append(names, tl.Name)
		}
		return names
	}
	systemText := func(r request) string {
		var b strings.Builder
		for _, s := range r.System {
			b.WriteString(s.Text)
		}
		return b.String()
	}

	// Built-in profile: no tools, Firmary's default prompt, Claude Code's default effort.
	for _, r := range run(`{"schema":1}`) {
		if len(r.Tools) != 0 {
			t.Errorf("built-in profile offered tools %v", toolNames(r))
		}
		if len(r.System) == 0 || r.System[len(r.System)-1].Text != prompt.Default {
			t.Errorf("built-in profile: Firmary's default prompt is not the last system block")
		}
	}

	// Web search on, low effort, the profile's own prompt.
	custom := "You are a test assistant. " + testkit.SystemPromptCanary
	for _, r := range run(`{"schema":1,"systemPrompt":"` + custom + `","effort":"low","tools":["web_search"],"maxTurns":2}`) {
		if names := toolNames(r); !slices.Equal(names, []string{"WebSearch"}) {
			t.Errorf("web search profile offered tools %v, want only WebSearch", names)
		}
		if r.OutputConfig.Effort != "low" {
			t.Errorf("effort %q, want low", r.OutputConfig.Effort)
		}
		if len(r.System) == 0 || r.System[len(r.System)-1].Text != custom {
			t.Errorf("the profile's prompt is not the last system block")
		}
		if sys := systemText(r); len(sys) > len(custom)+400 {
			t.Errorf("Claude Code added %d bytes of its own system text", len(sys)-len(custom))
		}
	}

	// Keep Claude Code's built-in prompt: it stays, and the profile's prompt is appended to it.
	for _, r := range run(`{"schema":1,"systemPrompt":"` + custom + `","claudeCode":{"replaceBuiltinInstructions":false}}`) {
		sys := systemText(r)
		if !strings.Contains(sys, custom) || len(sys) < 4000 {
			t.Errorf("keep built-in prompt: %d bytes of system text, profile prompt present: %v", len(sys), strings.Contains(sys, custom))
		}
		if len(r.Tools) != 0 {
			t.Errorf("keep built-in prompt offered tools %v", toolNames(r))
		}
	}
}

// TestRealMaxTurns runs the real pinned Claude Code (same opt-in) against a stub that asks for a web search on every
// turn, so only --max-turns can end the loop: the turn must stop after the profile's maxTurns with max_turns.
func TestRealMaxTurns(t *testing.T) {
	root := os.Getenv("FIRMARY_HELPER_REAL_CLAUDE")
	if root == "" {
		t.Skip("set FIRMARY_HELPER_REAL_CLAUDE to an install directory to run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var mu sync.Mutex
	mainTurns := 0
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if !strings.HasSuffix(r.URL.Path, "/v1/messages") {
			http.Error(w, "{}", http.StatusNotFound)
			return
		}
		var req struct {
			Tools []struct {
				Name string `json:"name"`
				Type string `json:"type"`
			} `json:"tools"`
		}
		json.Unmarshal(b, &req)
		main := len(req.Tools) == 1 && req.Tools[0].Name == "WebSearch" && req.Tools[0].Type == ""
		w.Header().Set("Content-Type", "text/event-stream")
		ev := func(typ string, v map[string]any) {
			v["type"] = typ
			j, _ := json.Marshal(v)
			io.WriteString(w, "event: "+typ+"\ndata: "+string(j)+"\n\n")
		}
		usage := map[string]any{"input_tokens": 5, "output_tokens": 1}
		ev("message_start", map[string]any{"message": map[string]any{"id": "msg_1", "type": "message", "role": "assistant",
			"model": "claude-sonnet-4-6", "content": []any{}, "stop_reason": nil, "usage": usage}})
		stop := "end_turn"
		if main {
			mu.Lock()
			mainTurns++
			n := mainTurns
			mu.Unlock()
			stop = "tool_use"
			ev("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "tool_use",
				"id": "toolu_" + strings.Repeat("a", n), "name": "WebSearch", "input": map[string]any{}}})
			ev("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "input_json_delta",
				"partial_json": `{"query":"firmary test query"}`}})
		} else {
			ev("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
			ev("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "text_delta", "text": "no results"}})
		}
		ev("content_block_stop", map[string]any{"index": 0})
		ev("message_delta", map[string]any{"delta": map[string]any{"stop_reason": stop}, "usage": map[string]any{"output_tokens": 1}})
		ev("message_stop", map[string]any{})
	}))
	defer stub.Close()

	home := t.TempDir()
	d, err := New(root, WithEnviron(func() []string { return []string{"PATH=/usr/bin:/bin", "HOME=" + home} }))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Ensure(ctx, BuiltInPin); err != nil {
		t.Fatal(err)
	}
	d.extraEnv = map[string]string{"ANTHROPIC_BASE_URL": stub.URL, "ANTHROPIC_API_KEY": "test-dummy-not-a-key",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1"}
	l, err := launch.Parse(`{"schema":1,"tools":["web_search"],"maxTurns":2}`)
	if err != nil {
		t.Fatal(err)
	}
	var events []protocol.StreamEvent
	err = d.Run(ctx, driver.Profile{UserID: "u1", Dir: filepath.Join(home, "profile")}, driver.Turn{ID: "real-turns",
		Model: "claude-sonnet-4-6", Prompt: "Search the web for anything", MaxOutputTokens: 200, Launch: l},
		func(e protocol.StreamEvent) { events = append(events, e) })
	mu.Lock()
	n := mainTurns
	mu.Unlock()
	t.Logf("main turns %d, events %+v, err %v", n, events, err)
	var f *driver.Failure
	if n != 2 || !errors.As(err, &f) || f.Pattern != "max_turns" {
		t.Fatalf("got %d model turns and %v, want 2 and max_turns", n, err)
	}
}
