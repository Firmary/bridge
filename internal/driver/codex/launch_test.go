//go:build !windows

package codex

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Firmary/bridge/internal/driver"
	"github.com/Firmary/bridge/internal/launch"
	"github.com/Firmary/bridge/internal/protocol"
	"github.com/Firmary/bridge/internal/testkit"
)

const instructionsMark = `model_instructions_file="<instructions file>"`

// codexVectors: for each valid shared launch profile fixture, the launch settings (as -c flags and in config.toml)
// and the settings for config.toml only. A new shared fixture fails the test until it has an entry here.
var codexVectors = map[string]struct{ args, fileOnly []string }{
	"built-in profile (version 0)": {args: []string{instructionsMark, `web_search="disabled"`}},
	"Claude Code turn with web search": {args: []string{instructionsMark, `web_search="live"`,
		`model_reasoning_effort="high"`}},
	"Codex turn with knobs": {args: []string{instructionsMark, `web_search="disabled"`, `model_reasoning_effort="low"`,
		`model_reasoning_summary="concise"`, `model_verbosity="low"`}},
	"bare: no prompt, no tools, low effort": {args: []string{instructionsMark, `web_search="disabled"`, `model_reasoning_effort="low"`}},
	"empty Claude Code knobs":               {args: []string{instructionsMark, `web_search="disabled"`, `model_reasoning_effort="xhigh"`}},
	"Codex turn keeping its built-in instructions": {args: []string{`web_search="disabled"`},
		fileOnly: []string{`developer_instructions="Answer briefly."`}},
	"Claude Code turn keeping its built-in prompt": {args: []string{instructionsMark, `web_search="disabled"`}},
}

func TestLaunchProfileVectors(t *testing.T) {
	d := &Driver{}
	prof := driver.Profile{UserID: "u1", Dir: "/profile"}
	mark := `model_instructions_file=` + tomlString(instructionsFile(prof))
	fixtures := testkit.LaunchProfileFixtures(t)
	for name, doc := range fixtures {
		v, ok := codexVectors[name]
		if !ok {
			t.Errorf("no Codex vector for shared fixture %q", name)
			continue
		}
		l, err := launch.Parse(doc)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		want := slices.Clone(v.args)
		for i, a := range want {
			if a == instructionsMark {
				want[i] = mark
			}
		}
		args, fileOnly := d.launchSettings(prof, l)
		if !slices.Equal(args, want) || !slices.Equal(fileOnly, v.fileOnly) {
			t.Errorf("%s:\n got %q %q\nwant %q %q", name, args, fileOnly, want, v.fileOnly)
		}
	}
	for name := range codexVectors {
		if _, ok := fixtures[name]; !ok {
			t.Errorf("vector %q has no shared fixture", name)
		}
	}
}

func key(setting string) string { k, _, _ := strings.Cut(setting, "="); return k }

// TestNoServerStringOnCommandLine is the canary test for the ceiling: over every profile the catalog allows, each -c
// setting has a key from the Helper's launch list and a value from its own tables or the instructions file path; the
// system prompt never reaches the command line. Launch keys never repeat a fixed key, the fixed settings come last,
// and every catalog tool maps to exact settings.
func TestNoServerStringOnCommandLine(t *testing.T) {
	const model = "gpt-model-canary"
	d := &Driver{}
	prof := driver.Profile{UserID: "u1", Dir: "/profile"}
	fixed := map[string]bool{}
	for _, s := range settings {
		fixed[key(s)] = true
	}
	own := map[string]bool{`model_instructions_file=` + tomlString(instructionsFile(prof)): true}
	for _, s := range noTools {
		own[s] = true
	}
	for _, name := range launch.Tools {
		ss, ok := toolSettings[name]
		if !ok || len(ss) == 0 {
			t.Errorf("catalog tool %s maps to %v", name, ss)
		}
		for _, s := range ss {
			own[s] = true
		}
	}
	for _, e := range launch.Efforts {
		v, ok := efforts[e]
		if !ok {
			t.Errorf("effort %s has no Codex value", e)
		}
		own[`model_reasoning_effort=`+tomlString(v)] = true
	}
	for _, v := range launch.CodexReasoningSummaries {
		own[`model_reasoning_summary=`+tomlString(v)] = true
	}
	for _, v := range launch.CodexVerbosities {
		own[`model_verbosity=`+tomlString(v)] = true
	}
	for s := range own {
		if fixed[key(s)] {
			t.Errorf("launch setting %s repeats a fixed key", s)
		}
	}
	if fixed["developer_instructions"] {
		t.Error("developer_instructions is a fixed key")
	}

	for _, tools := range [][]string{nil, launch.Tools} {
		for _, effort := range append([]string{""}, launch.Efforts...) {
			for _, summary := range append([]string{""}, launch.CodexReasoningSummaries...) {
				for _, verbosity := range append([]string{""}, launch.CodexVerbosities...) {
					for _, keep := range []bool{false, true} {
						l := launch.Profile{SystemPrompt: testkit.SystemPromptCanary, Effort: effort, Tools: tools,
							Codex: launch.Codex{ReasoningSummary: summary, Verbosity: verbosity, KeepBuiltinInstructions: keep}}
						args := d.runArgs(prof, model, l)
						if strings.Contains(strings.Join(args, "\n"), testkit.SystemPromptCanary) {
							t.Fatalf("the system prompt reached the command line for %+v", l)
						}
						var cs []string
						for i, a := range args {
							if a == "-c" {
								cs = append(cs, args[i+1])
							}
						}
						seenFixed := false
						for _, s := range cs {
							switch {
							case fixed[key(s)]:
								seenFixed = true
							case !own[s]:
								t.Fatalf("setting %q for %+v is not the Helper's own", s, l)
							case seenFixed:
								t.Fatalf("launch setting %q comes after a fixed one", s)
							}
						}
						if !slices.Contains(cs, `web_search="disabled"`) && !l.HasTool(launch.ToolWebSearch) {
							t.Fatalf("web search not disabled for %+v", l)
						}
					}
				}
			}
		}
	}
}

// TestRunWithLaunchProfile: keeping Codex's built-in instructions puts the profile's prompt in config.toml as
// developer instructions and never on the command line, and web search and the knobs arrive as their settings.
func TestRunWithLaunchProfile(t *testing.T) {
	d, dump := installFake(t, []string{"PATH=/usr/bin:/bin"})
	prof := driver.Profile{UserID: "u1", Dir: filepath.Join(t.TempDir(), "u1")}
	os.MkdirAll(filepath.Join(prof.Dir, "home"), 0o700)
	os.WriteFile(filepath.Join(prof.Dir, "home", "auth.json"), []byte(testkit.CodexAuthJSON("business")), 0o600)
	l, err := launch.Parse(`{"schema":1,"systemPrompt":"` + testkit.SystemPromptCanary + `","effort":"max","tools":["web_search"],` +
		`"codex":{"verbosity":"high","replaceBuiltinInstructions":false}}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Run(context.Background(), prof, driver.Turn{ID: "t1", Model: "gpt-5.5", Prompt: testkit.PromptCanary,
		MaxOutputTokens: 1000, Launch: l}, func(protocol.StreamEvent) {}); err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(filepath.Join(dump, "args"))
	if strings.Contains(string(args), testkit.SystemPromptCanary) {
		t.Error("the profile's system prompt reached the command line")
	}
	for _, want := range []string{`[web_search="live"]`, `[model_reasoning_effort="xhigh"]`, `[model_verbosity="high"]`} {
		if !strings.Contains(string(args), want) {
			t.Errorf("missing %q in args:\n%s", want, args)
		}
	}
	if strings.Contains(string(args), "model_instructions_file") || strings.Contains(string(args), "web_search=\"disabled\"") {
		t.Errorf("args:\n%s", args)
	}
	cfg, _ := os.ReadFile(filepath.Join(prof.Dir, "home", "config.toml"))
	if !strings.Contains(string(cfg), `developer_instructions = "`+testkit.SystemPromptCanary+`"`+"\n") ||
		strings.Contains(string(cfg), "model_instructions_file") {
		t.Errorf("config.toml:\n%s", cfg)
	}
}

// TestWebSearchItems: a web search item ends the turn as a blocked tool unless the profile turned web search on.
func TestWebSearchItems(t *testing.T) {
	line := []byte(`{"type":"item.started","item":{"id":"ws_1","type":"web_search"}}`)
	off := streamState{}
	off.line(line)
	on := streamState{webSearch: true}
	on.line(line)
	if !off.toolUsed || on.toolUsed {
		t.Fatalf("web search off: toolUsed=%v; on: toolUsed=%v", off.toolUsed, on.toolUsed)
	}
	shell := streamState{webSearch: true}
	shell.line([]byte(`{"type":"item.started","item":{"id":"c_1","type":"command_execution"}}`))
	if !shell.toolUsed {
		t.Fatal("a command ran with web search on")
	}
}

// TestTOMLStringEscapesControls: no character TOML forbids in a basic string (C0 controls other than tab, and DEL)
// is left raw, and a quote can't end the string early.
func TestTOMLStringEscapesControls(t *testing.T) {
	var all strings.Builder
	for r := rune(0); r < 0x80; r++ {
		all.WriteRune(r)
	}
	all.WriteString("  \"\"\" '''")
	q := tomlString(all.String())
	body := q[1 : len(q)-1]
	for i := 0; i < len(body); i++ {
		if c := body[i]; (c < 0x20 && c != '\t') || c == 0x7f {
			t.Fatalf("raw control character %#x in %q", c, q)
		}
		if body[i] == '"' && (i == 0 || body[i-1] != '\\') {
			t.Fatalf("unescaped quote in %q", q)
		}
	}
	if !strings.Contains(q, `\u007f`) || !strings.HasPrefix(q, `"`) || !strings.HasSuffix(q, `"`) {
		t.Fatalf("tomlString: %q", q)
	}
	if got := configTOML([]string{`developer_instructions=` + tomlString("a\x7fb")}); !strings.Contains(got, `developer_instructions = "a\u007fb"`+"\n") {
		t.Fatalf("config.toml:\n%s", got)
	}
}
