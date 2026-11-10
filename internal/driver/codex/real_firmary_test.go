//go:build !windows

package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
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
	"github.com/Firmary/bridge/internal/launch"
	"github.com/Firmary/bridge/internal/protocol"
	"github.com/Firmary/bridge/internal/testkit"
)

// TestRealFirmaryTools runs the real pinned Codex (same opt-in as TestRealPinnedCodex) with the firmary tool on,
// against a stub of OpenAI's Responses API and a fake Firmary MCP server whose one tool has no annotations (so Codex's
// own rules would treat it as a write). The stub calls the tool, then answers once it sees the tool's output. It checks
// on the pin, under --strict-config and the Link's locked settings, that the MCP server's tool is offered, that the
// call runs under approval_policy="never" without being refused, that the server gets the turn's bearer token, that
// the turn ends done (the stream parser lets Firmary's mcp_tool_call items through), and that the token is left
// nowhere in the profile afterwards.
func TestRealFirmaryTools(t *testing.T) {
	root := os.Getenv("FIRMARY_HELPER_REAL_CODEX")
	if root == "" {
		t.Skip("set FIRMARY_HELPER_REAL_CODEX to an install directory to run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	type tool struct {
		Type  string `json:"type"`
		Name  string `json:"name"`
		Tools []tool `json:"tools"` // a namespace's own tools
	}
	var mu sync.Mutex
	var offered [][]string
	var outputs []string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/responses") {
			http.Error(w, `{"error":{"message":"stub"}}`, http.StatusNotFound)
			return
		}
		var req struct {
			Tools []tool `json:"tools"`
			Input []struct {
				Type   string `json:"type"`
				Output any    `json:"output"`
				Tools  []tool `json:"tools"` // tool_search_output: the tools the search loaded
			} `json:"input"`
		}
		json.Unmarshal(b, &req)
		// Codex 0.166.0 defers every MCP tool behind its client-side tool_search, so the model first searches, then
		// calls what the search loaded.
		var names []string
		var call, namespace string
		find := func(tools []tool) {
			for _, tl := range tools {
				names = append(names, tl.Type+":"+tl.Name)
				if strings.Contains(tl.Name, testkit.FirmaryToolName) {
					call = tl.Name
				}
				for _, sub := range tl.Tools {
					names = append(names, tl.Name+"/"+sub.Name)
					if strings.Contains(sub.Name, testkit.FirmaryToolName) {
						call, namespace = sub.Name, tl.Name
					}
				}
			}
		}
		find(req.Tools)
		answered, searched := false, false
		for _, in := range req.Input {
			switch in.Type {
			case "function_call_output":
				o, _ := json.Marshal(in.Output)
				answered = true
				mu.Lock()
				outputs = append(outputs, string(o))
				mu.Unlock()
			case "tool_search_output":
				searched = true
				find(in.Tools)
			}
		}
		search := !searched && slices.Contains(names, "tool_search:")
		mu.Lock()
		offered = append(offered, names)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		ev := func(v map[string]any) {
			j, _ := json.Marshal(v)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", v["type"], j)
		}
		usage := map[string]any{"input_tokens": 5, "input_tokens_details": map[string]any{"cached_tokens": 0},
			"output_tokens": 1, "output_tokens_details": map[string]any{"reasoning_tokens": 0}, "total_tokens": 6}
		ev(map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_1"}})
		if search {
			ev(map[string]any{"type": "response.output_item.done", "output_index": 0, "item": map[string]any{
				"type": "tool_search_call", "call_id": "ts_firmary1", "status": "completed", "execution": "client",
				"arguments": map[string]any{"query": "ping firmary"}}})
		} else if call != "" && !answered {
			item := map[string]any{"type": "function_call", "id": "fc_1", "call_id": "call_firmary1", "name": call,
				"arguments": "{}", "status": "completed"}
			if namespace != "" {
				item["namespace"] = namespace
			}
			ev(map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item})
		} else {
			ev(map[string]any{"type": "response.output_item.done", "output_index": 0, "item": map[string]any{"type": "message",
				"id": "msg_1", "role": "assistant", "status": "completed",
				"content": []any{map[string]any{"type": "output_text", "text": "pong", "annotations": []any{}}}}})
		}
		ev(map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_1", "status": "completed", "usage": usage}})
	}))
	defer stub.Close()
	mcp := testkit.NewFirmaryMCP(t, testkit.FirmaryToken)
	// Slower than the provider's default wait for an optional MCP server (Codex: 1 second), so the turn proves it waits.
	mcp.InitDelay = 1500 * time.Millisecond

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
	l, err := launch.Parse(`{"schema":1,"systemPrompt":"Use Firmary's tools.","tools":["firmary"],"maxTurns":3}`)
	if err != nil {
		t.Fatal(err)
	}
	var events []protocol.StreamEvent
	err = d.Run(ctx, prof, driver.Turn{ID: "real-firmary", Model: "gpt-5.5", Prompt: "Ping Firmary.", MaxOutputTokens: 200,
		Launch: l, Firmary: &launch.Firmary{URL: mcp.URL, AccessToken: testkit.FirmaryToken}},
		func(e protocol.StreamEvent) { events = append(events, e) })
	mu.Lock()
	defer mu.Unlock()
	t.Logf("events %+v, mcp methods %v, tools offered %v, tool outputs %v", events, mcp.Methods(), offered, outputs)
	if err != nil || events[len(events)-1].T != protocol.EvDone {
		t.Fatalf("turn failed: %v", err)
	}
	if len(offered) < 2 {
		t.Fatalf("%d model requests, want the call and the answer", len(offered))
	}
	if !slices.Contains(mcp.Methods(), "tools/call") || len(outputs) == 0 || !strings.Contains(outputs[0], testkit.FirmaryToolResult) {
		t.Errorf("the MCP tool's result never reached the model: the call needed approval or was refused")
	}
	if mcp.Unauthorized() > 0 {
		t.Errorf("%d requests reached Firmary without the turn's token", mcp.Unauthorized())
	}
	filepath.WalkDir(home, func(path string, de fs.DirEntry, err error) error {
		if err != nil || de.IsDir() {
			return nil
		}
		if b, err := os.ReadFile(path); err == nil && strings.Contains(string(b), testkit.FirmaryToken) {
			t.Errorf("the token was left in %s", path)
		}
		return nil
	})
}
