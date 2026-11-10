//go:build !windows

package claudecode

import (
	"context"
	"encoding/json"
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

// TestRealFirmaryTools runs the real pinned Claude Code (same opt-in as TestRealPinnedClaudeCode) with the firmary
// tool on, against a stub of Anthropic's API and a fake Firmary MCP server. The stub asks for the MCP tool, then
// answers once it sees the tool's result. It checks on the pin that --tools "" still offers the MCP server's tools and
// no built-in one, that --allowedTools mcp__firmary lets the call run in print mode with no prompt, that the server
// gets the turn's bearer token, and that the token is left nowhere in the profile afterwards.
func TestRealFirmaryTools(t *testing.T) {
	root := os.Getenv("FIRMARY_HELPER_REAL_CLAUDE")
	if root == "" {
		t.Skip("set FIRMARY_HELPER_REAL_CLAUDE to an install directory to run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	const toolName = "mcp__firmary__" + testkit.FirmaryToolName
	var mu sync.Mutex
	var offered [][]string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if !strings.HasSuffix(r.URL.Path, "/v1/messages") {
			http.Error(w, "{}", http.StatusNotFound)
			return
		}
		var req struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		json.Unmarshal(b, &req)
		var names []string
		for _, tl := range req.Tools {
			names = append(names, tl.Name)
		}
		main := slices.Contains(names, toolName)
		if main {
			mu.Lock()
			offered = append(offered, names)
			mu.Unlock()
		}
		callTool := main && !strings.Contains(string(b), testkit.FirmaryToolResult)
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
		if callTool {
			stop = "tool_use"
			ev("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "tool_use",
				"id": "toolu_firmary1", "name": toolName, "input": map[string]any{}}})
			ev("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "input_json_delta",
				"partial_json": `{}`}})
		} else {
			ev("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
			ev("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "text_delta", "text": "pong"}})
		}
		ev("content_block_stop", map[string]any{"index": 0})
		ev("message_delta", map[string]any{"delta": map[string]any{"stop_reason": stop}, "usage": map[string]any{"output_tokens": 1}})
		ev("message_stop", map[string]any{})
	}))
	defer stub.Close()
	mcp := testkit.NewFirmaryMCP(t, testkit.FirmaryToken)
	// Slower than the provider's default wait for an optional MCP server (Codex: 1 second), so the turn proves it waits.
	mcp.InitDelay = 1500 * time.Millisecond

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
	l, err := launch.Parse(`{"schema":1,"systemPrompt":"Use Firmary's tools.","tools":["firmary"],"maxTurns":3}`)
	if err != nil {
		t.Fatal(err)
	}
	prof := driver.Profile{UserID: "u1", Dir: filepath.Join(home, "profile")}
	var events []protocol.StreamEvent
	err = d.Run(ctx, prof, driver.Turn{ID: "real-firmary", Model: "claude-sonnet-4-6", Prompt: "Ping Firmary.",
		MaxOutputTokens: 200, Launch: l, Firmary: &launch.Firmary{URL: mcp.URL, AccessToken: testkit.FirmaryToken}},
		func(e protocol.StreamEvent) { events = append(events, e) })
	t.Logf("events %+v, mcp methods %v", events, mcp.Methods())
	if err != nil || events[len(events)-1].T != protocol.EvDone {
		t.Fatalf("turn failed: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(offered) < 2 {
		t.Fatalf("the model was offered Firmary's tool in %d requests, want the call and the answer", len(offered))
	}
	for _, names := range offered {
		if !slices.Equal(names, []string{toolName}) {
			t.Errorf("tools offered %v, want only %s", names, toolName)
		}
	}
	if !slices.Contains(mcp.Methods(), "tools/call") {
		t.Error("the MCP tool was never called: the call needed a prompt or was refused")
	}
	if mcp.Unauthorized() > 0 {
		t.Errorf("%d requests reached Firmary without the turn's token", mcp.Unauthorized())
	}
	if _, err := os.Stat(mcpConfigFile(prof)); !os.IsNotExist(err) {
		t.Errorf("the MCP config outlived the run: %v", err)
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
