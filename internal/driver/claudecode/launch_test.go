//go:build !windows

package claudecode

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Firmary/bridge/internal/driver"
	"github.com/Firmary/bridge/internal/driver/prompt"
	"github.com/Firmary/bridge/internal/launch"
	"github.com/Firmary/bridge/internal/protocol"
	"github.com/Firmary/bridge/internal/testkit"
)

const promptFileMark = "<prompt file>"

// claudeVectors: for each valid shared launch profile fixture, the --tools value and the flags after the fixed ones.
// A new shared fixture fails the test until it has an entry here.
var claudeVectors = map[string]struct {
	tools string
	tail  []string
}{
	"built-in profile (version 0)":                 {"", []string{"--system-prompt-file", promptFileMark}},
	"Claude Code turn with web search":             {"WebSearch", []string{"--allowedTools", "WebSearch", "--max-turns", "4", "--effort", "high", "--system-prompt-file", promptFileMark}},
	"Codex turn with knobs":                        {"", []string{"--effort", "low", "--system-prompt-file", promptFileMark}},
	"bare: no prompt, no tools, low effort":        {"", []string{"--effort", "low", "--system-prompt-file", promptFileMark}},
	"empty Claude Code knobs":                      {"", []string{"--effort", "max", "--system-prompt-file", promptFileMark}},
	"Codex turn keeping its built-in instructions": {"", []string{"--system-prompt-file", promptFileMark}},
	"Claude Code turn keeping its built-in prompt": {"", []string{"--append-system-prompt-file", promptFileMark}},
}

func TestLaunchProfileVectors(t *testing.T) {
	prof := driver.Profile{UserID: "u1", Dir: "/profile"}
	fixtures := testkit.LaunchProfileFixtures(t)
	for name, doc := range fixtures {
		v, ok := claudeVectors[name]
		if !ok {
			t.Errorf("no Claude Code vector for shared fixture %q", name)
			continue
		}
		l, err := launch.Parse(doc)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		want := fixedArgs("claude-sonnet-4-5", v.tools)
		for _, a := range v.tail {
			if a == promptFileMark {
				a = systemPromptFile(prof)
			}
			want = append(want, a)
		}
		if got := runArgs(prof, "claude-sonnet-4-5", l); !slices.Equal(got, want) {
			t.Errorf("%s:\n got %q\nwant %q", name, got, want)
		}
	}
	// Web search without maxTurns is still bounded, by the ceiling (review F5).
	l, err := launch.Parse(`{"schema":1,"tools":["web_search"]}`)
	if err != nil {
		t.Fatal(err)
	}
	want := append(fixedArgs("claude-sonnet-4-5", "WebSearch"), "--allowedTools", "WebSearch", "--max-turns", "10",
		"--system-prompt-file", systemPromptFile(prof))
	if got := runArgs(prof, "claude-sonnet-4-5", l); !slices.Equal(got, want) {
		t.Errorf("web search without maxTurns:\n got %q\nwant %q", got, want)
	}
	// maxTurns without a tool adds nothing: there is no loop to bound.
	if l, _ := launch.Parse(`{"schema":1,"maxTurns":3}`); slices.Contains(runArgs(prof, "m", l), "--max-turns") {
		t.Error("--max-turns passed with no tool on")
	}
	for name := range claudeVectors {
		if _, ok := fixtures[name]; !ok {
			t.Errorf("vector %q has no shared fixture", name)
		}
	}
}

// TestNoServerStringOnCommandLine is the canary test for the ceiling: over every profile the catalog allows, each
// argument is one of the Link's own fixed strings or table values, the model, or the prompt file path; the system
// prompt never is. Every catalog tool maps to an exact tool list, and no profile flag repeats a fixed one.
func TestNoServerStringOnCommandLine(t *testing.T) {
	const model = "claude-model-canary"
	prof := driver.Profile{UserID: "u1", Dir: "/profile"}
	own := map[string]bool{"--allowedTools": true, "--max-turns": true, "--effort": true, "--system-prompt-file": true,
		"--append-system-prompt-file": true, "--mcp-config": true}
	for _, a := range fixedArgs("", "") {
		if own[a] {
			t.Errorf("profile flag %s is also a fixed flag", a)
		}
	}
	for _, a := range fixedArgs("", "") {
		own[a] = true
	}
	toolSets := [][]string{nil, {launch.ToolWebSearch}, {launch.ToolFirmary}, launch.Tools}
	for _, name := range launch.Tools {
		names, ok := toolNames[name]
		allow := allowNames[name]
		if !ok || len(allow) == 0 || slices.Contains(names, "WebFetch") || slices.Contains(allow, "WebFetch") {
			t.Errorf("catalog tool %s maps to %v, allowing %v", name, names, allow)
		}
	}
	for _, set := range toolSets {
		var names, allow []string
		for _, name := range set {
			names, allow = append(names, toolNames[name]...), append(allow, allowNames[name]...)
		}
		own[strings.Join(names, ",")], own[strings.Join(allow, ",")] = true, true
	}
	for _, e := range launch.Efforts {
		v, ok := efforts[e]
		if !ok {
			t.Errorf("effort %s has no flag value", e)
		}
		own[v] = true
	}
	for n := 1; n <= launch.MaxTurns; n++ {
		own[strconv.Itoa(n)] = true
	}
	own[model], own[systemPromptFile(prof)], own[mcpConfigFile(prof)] = true, true, true

	for _, tools := range toolSets {
		for _, effort := range append([]string{""}, launch.Efforts...) {
			for _, keep := range []bool{false, true} {
				for turns := 0; turns <= launch.MaxTurns; turns++ {
					l := launch.Profile{SystemPrompt: testkit.SystemPromptCanary, Effort: effort, Tools: tools, MaxTurns: turns,
						ClaudeCode: launch.ClaudeCode{KeepBuiltinInstructions: keep}}
					for _, a := range runArgs(prof, model, l) {
						if !own[a] || strings.Contains(a, testkit.SystemPromptCanary) {
							t.Fatalf("argument %q for %+v is not the Link's own", a, l)
						}
					}
				}
			}
		}
	}
}

// TestRunWithLaunchProfile: the profile's prompt is written to the 0600 prompt file and never reaches the command
// line, and web search, effort and max turns arrive as their flags.
func TestRunWithLaunchProfile(t *testing.T) {
	dump := t.TempDir()
	rel := testkit.NewRelease(t, "2.1.304", testPlatform, testkit.FakeClaude(dump))
	d, err := New(t.TempDir(), WithRelease(rel.URL, rel.Keyring, nil), WithPlatform(testPlatform))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Ensure(context.Background(), driver.Pin{Version: "2.1.304", SHA256: map[string]string{testPlatform: rel.SHA256}}); err != nil {
		t.Fatal(err)
	}
	prof := driver.Profile{UserID: "u1", Dir: filepath.Join(t.TempDir(), "u1")}
	l, err := launch.Parse(`{"schema":1,"systemPrompt":"` + testkit.SystemPromptCanary + `","effort":"high","tools":["web_search"],"maxTurns":3}`)
	if err != nil {
		t.Fatal(err)
	}
	err = d.Run(context.Background(), prof, driver.Turn{ID: "t1", Model: "claude-sonnet-4-5", Prompt: testkit.PromptCanary,
		MaxOutputTokens: 1000, Launch: l}, func(protocol.StreamEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(filepath.Join(dump, "args"))
	if strings.Contains(string(args), testkit.SystemPromptCanary) {
		t.Error("the profile's system prompt reached the command line")
	}
	for _, want := range []string{"[--tools]\n[WebSearch]\n", "[--allowedTools]\n[WebSearch]\n", "[--max-turns]\n[3]\n",
		"[--effort]\n[high]\n", "[--system-prompt-file]\n[" + systemPromptFile(prof) + "]\n"} {
		if !strings.Contains(string(args), want) {
			t.Errorf("missing %q in args:\n%s", want, args)
		}
	}
	if b, _ := os.ReadFile(systemPromptFile(prof)); string(b) != testkit.SystemPromptCanary {
		t.Errorf("prompt file holds %q", b)
	}
	if fi, err := os.Stat(systemPromptFile(prof)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("prompt file mode: %v %v", fi, err)
	}
}

// TestFallbackPromptFollowsTools: a profile without systemPrompt gets the built-in prompt that matches its tools, and
// a profile's own prompt always wins (review F8).
func TestFallbackPromptFollowsTools(t *testing.T) {
	for doc, want := range map[string]string{
		`{"schema":1}`:                                              prompt.Default,
		`{"schema":1,"tools":[]}`:                                   prompt.Default,
		`{"schema":1,"tools":["web_search"]}`:                       prompt.DefaultWebSearch,
		`{"schema":1,"tools":["firmary"]}`:                          prompt.Fallback(false, true),
		`{"schema":1,"tools":["web_search","firmary"]}`:             prompt.Fallback(true, true),
		`{"schema":1,"systemPrompt":"Own.","tools":["web_search"]}`: "Own.",
	} {
		l, err := launch.Parse(doc)
		if err != nil {
			t.Fatal(err)
		}
		if got := systemPromptText(l); got != want {
			t.Errorf("%s: prompt %.60q", doc, got)
		}
	}
}

// TestCodexKnobsIgnored pins that the Codex knob block has no effect on Claude Code (review F9; SCHEMA-CHANGES.md §8).
func TestCodexKnobsIgnored(t *testing.T) {
	prof := driver.Profile{UserID: "u1", Dir: "/profile"}
	a, _ := launch.Parse(`{"schema":1,"effort":"max"}`)
	b, err := launch.Parse(`{"schema":1,"effort":"max","codex":{"reasoningSummary":"detailed","verbosity":"high","replaceBuiltinInstructions":false}}`)
	if err != nil {
		t.Fatal(err)
	}
	if x, y := runArgs(prof, "m", a), runArgs(prof, "m", b); !slices.Equal(x, y) {
		t.Errorf("Codex knobs changed Claude Code's flags: %q vs %q", x, y)
	}
}

// TestRunWithFirmaryTools: with the firmary tool on, the turn's MCP config is a 0600 file holding Firmary's endpoint
// and the bearer token, passed by path with --mcp-config next to --allowedTools mcp__firmary and a turn limit, with
// --tools still "" and --strict-mcp-config kept; the token never reaches the command line or the environment, and the
// file is gone once the run ends. Without the tool none of this is there, and a credential without the tool (or the
// tool without one) refuses the turn.
func TestRunWithFirmaryTools(t *testing.T) {
	dump := t.TempDir()
	rel := testkit.NewRelease(t, "2.1.304", testPlatform, testkit.FakeClaude(dump))
	d, err := New(t.TempDir(), WithRelease(rel.URL, rel.Keyring, nil), WithPlatform(testPlatform))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Ensure(context.Background(), driver.Pin{Version: "2.1.304", SHA256: map[string]string{testPlatform: rel.SHA256}}); err != nil {
		t.Fatal(err)
	}
	prof := driver.Profile{UserID: "u1", Dir: filepath.Join(t.TempDir(), "u1")}
	cred := &launch.Firmary{URL: "https://firmary.app/api/mcp", AccessToken: testkit.FirmaryToken}
	run := func(profile string, f *launch.Firmary) error {
		t.Helper()
		os.Remove(filepath.Join(dump, "mcp-config"))
		l, err := launch.Parse(profile)
		if err != nil {
			t.Fatal(err)
		}
		return d.Run(context.Background(), prof, driver.Turn{ID: "t1", Model: "claude-sonnet-4-5", Prompt: testkit.PromptCanary,
			MaxOutputTokens: 1000, Launch: l, Firmary: f}, func(protocol.StreamEvent) {})
	}

	if err := run(`{"schema":1,"tools":["web_search","firmary"],"maxTurns":10}`, cred); err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(filepath.Join(dump, "args"))
	env, _ := os.ReadFile(filepath.Join(dump, "env"))
	if strings.Contains(string(args)+string(env), testkit.FirmaryToken) || strings.Contains(string(args), "firmary.app") {
		t.Errorf("the token or endpoint reached the command line or environment:\n%s", args)
	}
	for _, want := range []string{"[--tools]\n[WebSearch]\n", "[--strict-mcp-config]\n",
		"[--mcp-config]\n[" + mcpConfigFile(prof) + "]\n[--allowedTools]\n[WebSearch,mcp__firmary]\n[--max-turns]\n[10]\n"} {
		if !strings.Contains(string(args), want) {
			t.Errorf("missing %q in args:\n%s", want, args)
		}
	}
	b, _ := os.ReadFile(filepath.Join(dump, "mcp-config"))
	if want := `{"mcpServers":{"firmary":{"type":"http","url":"https://firmary.app/api/mcp","headers":{"Authorization":"Bearer ` +
		testkit.FirmaryToken + `"}}}}`; string(b) != want {
		t.Errorf("MCP config %s", b)
	}
	if mode, _ := os.ReadFile(filepath.Join(dump, "mcp-config-mode")); strings.TrimSpace(string(mode)) != "-rw-------" {
		t.Errorf("MCP config mode %q", mode)
	}
	if _, err := os.Stat(mcpConfigFile(prof)); !os.IsNotExist(err) {
		t.Errorf("the MCP config outlived the run: %v", err)
	}

	// Firmary's tools alone: no built-in tool.
	if err := run(`{"schema":1,"tools":["firmary"],"maxTurns":4}`, cred); err != nil {
		t.Fatal(err)
	}
	args, _ = os.ReadFile(filepath.Join(dump, "args"))
	if !strings.Contains(string(args), "[--tools]\n[]\n") || !strings.Contains(string(args), "[--allowedTools]\n[mcp__firmary]\n[--max-turns]\n[4]\n") {
		t.Errorf("firmary only:\n%s", args)
	}

	if err := run(`{"schema":1,"tools":["web_search"]}`, nil); err != nil {
		t.Fatal(err)
	}
	args, _ = os.ReadFile(filepath.Join(dump, "args"))
	if strings.Contains(string(args), "--mcp-config") || strings.Contains(string(args), "mcp__") {
		t.Errorf("MCP flags without the firmary tool:\n%s", args)
	}
	if _, err := os.Stat(filepath.Join(dump, "mcp-config")); !os.IsNotExist(err) {
		t.Error("an MCP config was passed without the firmary tool")
	}

	var f *driver.Failure
	for name, err := range map[string]error{
		"tool without credential": run(`{"schema":1,"tools":["firmary"]}`, nil),
		"credential without tool": run(`{"schema":1}`, cred),
	} {
		if !errors.As(err, &f) || f.Pattern != string(launch.ReasonFirmaryInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
