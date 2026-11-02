//go:build !windows

package codex

import (
	"context"
	"encoding/json"
	"fmt"
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

// TestRealLaunchProfile runs the real pinned Codex (same opt-in as TestRealPinnedCodex) against a local stub of
// OpenAI's Responses API that records each request and answers "pong", with a dummy key: no account, no traffic to
// OpenAI. It checks each launch profile knob on the pin: with the built-in profile web search is off and Firmary's
// default prompt replaces Codex's instructions; with web search on, the web_search tool is offered and the effort
// (max as xhigh), reasoning summary and verbosity are sent; with replaceBuiltinInstructions false, Codex keeps its own
// instructions and the profile's prompt arrives as a developer message.
func TestRealLaunchProfile(t *testing.T) {
	root := os.Getenv("FIRMARY_HELPER_REAL_CODEX")
	if root == "" {
		t.Skip("set FIRMARY_HELPER_REAL_CODEX to an install directory to run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	type request struct {
		Instructions string `json:"instructions"`
		Tools        []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"tools"`
		Input []struct {
			Role    string `json:"role"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"input"`
		Reasoning *struct {
			Effort  string `json:"effort"`
			Summary string `json:"summary"`
		} `json:"reasoning"`
		Text *struct {
			Verbosity string `json:"verbosity"`
		} `json:"text"`
	}
	var mu sync.Mutex
	var reqs []request
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/responses") {
			http.Error(w, `{"error":{"message":"stub"}}`, http.StatusNotFound)
			return
		}
		var req request
		json.Unmarshal(b, &req)
		mu.Lock()
		reqs = append(reqs, req)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		ev := func(v map[string]any) {
			j, _ := json.Marshal(v)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", v["type"], j)
		}
		usage := map[string]any{"input_tokens": 5, "input_tokens_details": map[string]any{"cached_tokens": 0},
			"output_tokens": 1, "output_tokens_details": map[string]any{"reasoning_tokens": 0}, "total_tokens": 6}
		ev(map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_1"}})
		ev(map[string]any{"type": "response.output_item.done", "output_index": 0, "item": map[string]any{"type": "message",
			"id": "msg_1", "role": "assistant", "status": "completed",
			"content": []any{map[string]any{"type": "output_text", "text": "pong", "annotations": []any{}}}}})
		ev(map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_1", "status": "completed", "usage": usage}})
	}))
	defer stub.Close()

	home := realProfileDir(t)
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
		var events []protocol.StreamEvent
		err = d.Run(ctx, prof, driver.Turn{ID: "real-launch", Model: "gpt-5.5", Prompt: "Reply with the single word: pong",
			MaxOutputTokens: 200, Launch: l}, func(e protocol.StreamEvent) { events = append(events, e) })
		if err != nil || events[len(events)-1].T != protocol.EvDone {
			t.Fatalf("%s: turn failed: %v %+v", profile, err, events)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(reqs) == 0 {
			t.Fatalf("%s: the stub saw no request; the test proves nothing", profile)
		}
		return slices.Clone(reqs)
	}
	tools := func(r request) []string {
		var names []string
		for _, tl := range r.Tools {
			names = append(names, tl.Name+tl.Type)
		}
		return names
	}
	// developer returns the request's developer messages. Codex 0.166.0 sends its base instructions (Firmary's prompt
	// when it replaces them) as the first, and developer_instructions as the next.
	developer := func(r request) []string {
		var out []string
		for _, in := range r.Input {
			if in.Role == "developer" && len(in.Content) == 1 {
				out = append(out, in.Content[0].Text)
			}
		}
		return out
	}
	base := func(r request) string {
		if r.Instructions != "" {
			return r.Instructions
		}
		if dev := developer(r); len(dev) > 0 {
			return dev[0]
		}
		return ""
	}

	// Built-in profile: web search off, Firmary's default prompt in place of Codex's.
	for _, r := range run(`{"schema":1}`) {
		if names := tools(r); !slices.Equal(names, []string{"apply_patchcustom"}) {
			t.Errorf("built-in profile offered tools %v", names)
		}
		if base(r) != strings.TrimSpace(prompt.Default) || len(developer(r)) != 1 {
			t.Errorf("built-in profile instructions %.80q, %d developer messages", base(r), len(developer(r)))
		}
	}

	// Web search on, the knobs, the profile's own prompt.
	custom := "You are a test assistant. " + testkit.SystemPromptCanary
	for _, r := range run(`{"schema":1,"systemPrompt":"` + custom + `","effort":"max","tools":["web_search"],` +
		`"codex":{"reasoningSummary":"concise","verbosity":"low"}}`) {
		if names := tools(r); !slices.Contains(names, "web_search") || len(names) != 2 || !slices.Contains(names, "apply_patchcustom") {
			t.Errorf("web search profile offered tools %v, want apply_patch and web_search", names)
		}
		if r.Reasoning == nil || r.Reasoning.Effort != "xhigh" || r.Reasoning.Summary != "concise" {
			t.Errorf("reasoning %+v, want xhigh and concise", r.Reasoning)
		}
		if r.Text == nil || r.Text.Verbosity != "low" {
			t.Errorf("text %+v, want verbosity low", r.Text)
		}
		if base(r) != custom || len(developer(r)) != 1 {
			t.Errorf("instructions %.80q, want the profile's prompt", base(r))
		}
	}

	// Keep Codex's built-in instructions: the profile's prompt is a developer message.
	for _, r := range run(`{"schema":1,"systemPrompt":"` + custom + `","codex":{"replaceBuiltinInstructions":false}}`) {
		if !strings.HasPrefix(base(r), builtInPrompt) {
			t.Errorf("keep built-in instructions: instructions %.80q", base(r))
		}
		if dev := developer(r); len(dev) != 2 || dev[1] != custom {
			t.Errorf("the profile's prompt is not the developer message after Codex's instructions: %.300q", dev)
		}
		if names := tools(r); !slices.Equal(names, []string{"apply_patchcustom"}) {
			t.Errorf("keep built-in instructions offered tools %v", names)
		}
	}
}
