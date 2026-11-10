//go:build !windows

package claudecode

import (
	"context"
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
