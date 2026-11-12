//go:build !windows

package codex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Firmary/bridge/internal/driver"
	"github.com/Firmary/bridge/internal/driver/prompt"
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
// setting has a key from the Link's launch list and a value from its own tables or the instructions file path; the
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
		if name == launch.ToolFirmary {
			// Its settings carry the server's endpoint, so they are in config.toml only (TestFirmarySettings).
			for _, s := range firmarySettings(&launch.Firmary{}) {
				if fixed[key(s)] {
					t.Errorf("Firmary setting %s repeats a fixed key", s)
				}
			}
			continue
		}
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
								t.Fatalf("setting %q for %+v is not the Link's own", s, l)
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

// TestStatusDuringTurnKeepsTurnFiles: a status check (as every incoming turn and hello makes) while a turn is
// starting doesn't rewrite that turn's prompt or config.toml before Codex reads them, and doesn't wait for the turn
// to end (review F1). The fake codex reads its files only after a delay, as a real one starting up might.
func TestStatusDuringTurnKeepsTurnFiles(t *testing.T) {
	dump := t.TempDir()
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
	bin := "#!/bin/sh\n" +
		`if [ "$1" = "login" ]; then echo 'Logged in using ChatGPT' >&2; exit 0; fi` + "\n" +
		"cat > /dev/null\ntouch " + q(filepath.Join(dump, "started")) + "\nsleep 1\n" +
		`cp "$CODEX_HOME/config.toml" ` + q(filepath.Join(dump, "config.toml")) + "\n" +
		`cp "$CODEX_HOME/firmary-system-prompt.md" ` + q(filepath.Join(dump, "prompt.md")) + "\n" +
		`printf '%s\n' '{"type":"thread.started","thread_id":"t"}' '{"type":"turn.completed"}'` + "\n"
	rel := testkit.NewCodexRelease(t, testVersion, testTarget, []byte(bin))
	d, err := New(t.TempDir(), WithRelease(rel.URL, nil), WithPlatform(testPlatform), WithManagedPaths(),
		WithEnviron(func() []string { return []string{"PATH=/usr/bin:/bin"} }))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Ensure(context.Background(), testPin(rel)); err != nil {
		t.Fatal(err)
	}
	prof := driver.Profile{UserID: "u1", Dir: filepath.Join(t.TempDir(), "u1")}

	for _, profile := range []string{
		`{"schema":1,"systemPrompt":"` + testkit.SystemPromptCanary + `"}`,
		`{"schema":1,"systemPrompt":"` + testkit.SystemPromptCanary + `","codex":{"replaceBuiltinInstructions":false}}`,
	} {
		os.Remove(filepath.Join(dump, "started"))
		l, err := launch.Parse(profile)
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			done <- d.Run(context.Background(), prof, driver.Turn{ID: "t1", Model: "gpt-5.5", Prompt: "hi", MaxOutputTokens: 100,
				Launch: l}, func(protocol.StreamEvent) {})
		}()
		for i := 0; ; i++ {
			if _, err := os.Stat(filepath.Join(dump, "started")); err == nil {
				break
			}
			if i > 500 {
				t.Fatal("the fake codex never started")
			}
			time.Sleep(10 * time.Millisecond)
		}
		start := time.Now()
		if _, err := d.Status(context.Background(), prof); err != nil {
			t.Fatal(err)
		}
		if waited := time.Since(start); waited > 700*time.Millisecond {
			t.Errorf("status waited %v for the turn", waited)
		}
		if err := <-done; err != nil {
			t.Fatalf("%s: %v", profile, err)
		}
		cfg, _ := os.ReadFile(filepath.Join(dump, "config.toml"))
		prompt, _ := os.ReadFile(filepath.Join(dump, "prompt.md"))
		if !strings.Contains(string(cfg)+string(prompt), testkit.SystemPromptCanary) {
			t.Errorf("%s: the turn's prompt was replaced while Codex started\nconfig.toml:\n%s", profile, cfg)
		}
	}
	// With no turn running, status prepares the profile as before.
	if _, err := d.Status(context.Background(), prof); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := os.ReadFile(filepath.Join(prof.Dir, "home", "config.toml")); strings.Contains(string(cfg), testkit.SystemPromptCanary) {
		t.Error("an idle status left the last turn's prompt in config.toml")
	}
}

// TestLossyMappings pins what hello.launch can't say and SCHEMA-CHANGES.md §8 documents (review F9): effort max runs
// as xhigh, maxTurns has no effect, and the Claude Code knobs are ignored. Change those docs if this test changes.
func TestLossyMappings(t *testing.T) {
	d := &Driver{}
	prof := driver.Profile{UserID: "u1", Dir: "/profile"}
	settings := func(doc string) []string {
		l, err := launch.Parse(doc)
		if err != nil {
			t.Fatal(err)
		}
		args, fileOnly := d.launchSettings(prof, l)
		return append(args, fileOnly...)
	}
	if a, b := settings(`{"schema":1,"effort":"max"}`), settings(`{"schema":1,"effort":"xhigh"}`); !slices.Equal(a, b) {
		t.Errorf("effort max %q differs from xhigh %q", a, b)
	}
	base := settings(`{"schema":1,"tools":["web_search"]}`)
	for _, doc := range []string{`{"schema":1,"tools":["web_search"],"maxTurns":1}`,
		`{"schema":1,"tools":["web_search"],"claudeCode":{"replaceBuiltinInstructions":false}}`} {
		if got := settings(doc); !slices.Equal(got, base) {
			t.Errorf("%s changed the Codex settings: %q", doc, got)
		}
	}
}

// TestRunWithFirmaryTools: with the firmary tool on, config.toml names Firmary's MCP server with its bearer token
// variable, tools that run without Codex's own approval and a required start; the token is only in that Codex's
// environment, and neither it nor the endpoint is on the command line. Without the tool there is no MCP server and no
// token variable, and a credential without the tool (or the tool without one) refuses the turn.
func TestRunWithFirmaryTools(t *testing.T) {
	d, dump := installFake(t, []string{"PATH=/usr/bin:/bin", FirmaryTokenEnv + "=inherited-must-not-pass"})
	prof := driver.Profile{UserID: "u1", Dir: filepath.Join(t.TempDir(), "u1")}
	os.MkdirAll(filepath.Join(prof.Dir, "home"), 0o700)
	os.WriteFile(filepath.Join(prof.Dir, "home", "auth.json"), []byte(testkit.CodexAuthJSON("business")), 0o600)
	cred := &launch.Firmary{URL: "https://firmary.app/api/mcp", AccessToken: testkit.FirmaryToken}
	run := func(profile string, f *launch.Firmary) error {
		t.Helper()
		l, err := launch.Parse(profile)
		if err != nil {
			t.Fatal(err)
		}
		return d.Run(context.Background(), prof, driver.Turn{ID: "t1", Model: "gpt-5.5", Prompt: testkit.PromptCanary,
			MaxOutputTokens: 1000, Launch: l, Firmary: f}, func(protocol.StreamEvent) {})
	}
	read := func(name string) string { b, _ := os.ReadFile(name); return string(b) }

	if err := run(`{"schema":1,"tools":["firmary"],"maxTurns":10}`, cred); err != nil {
		t.Fatal(err)
	}
	args, env, cfg := read(filepath.Join(dump, "args")), read(filepath.Join(dump, "env")), read(filepath.Join(prof.Dir, "home", "config.toml"))
	if strings.Contains(args, testkit.FirmaryToken) || strings.Contains(args, "firmary.app") || strings.Contains(args, "mcp_servers") {
		t.Errorf("Firmary's settings reached the command line:\n%s", args)
	}
	if !strings.Contains(env, FirmaryTokenEnv+"="+testkit.FirmaryToken+"\n") || strings.Contains(env, "inherited-must-not-pass") {
		t.Errorf("token variable in the environment:\n%s", env)
	}
	for _, want := range []string{`mcp_servers.firmary.url = "https://firmary.app/api/mcp"`,
		`mcp_servers.firmary.bearer_token_env_var = "FIRMARY_MCP_TOKEN"`,
		`mcp_servers.firmary.default_tools_approval_mode = "approve"`, `mcp_servers.firmary.required = true`,
		`approval_policy = "never"`, `web_search = "disabled"`} {
		if !strings.Contains(cfg, want+"\n") {
			t.Errorf("config.toml has no %s:\n%s", want, cfg)
		}
	}
	if strings.Contains(cfg, testkit.FirmaryToken) {
		t.Error("the token was written to config.toml")
	}

	if err := run(`{"schema":1}`, nil); err != nil {
		t.Fatal(err)
	}
	env, cfg = read(filepath.Join(dump, "env")), read(filepath.Join(prof.Dir, "home", "config.toml"))
	if strings.Contains(env, FirmaryTokenEnv) || strings.Contains(cfg, "mcp_servers") {
		t.Errorf("Firmary's server without the tool:\n%s\n%s", env, cfg)
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

// TestFirmaryToolItems: a call to Firmary's MCP server is expected only when the profile turned its tools on; any
// other MCP server's call still ends the turn.
func TestFirmaryToolItems(t *testing.T) {
	firmary := []byte(`{"type":"item.started","item":{"id":"m_1","type":"mcp_tool_call","server":"firmary","tool":"x","status":"in_progress"}}`)
	other := []byte(`{"type":"item.started","item":{"id":"m_2","type":"mcp_tool_call","server":"other","tool":"x","status":"in_progress"}}`)
	off, on, onOther := streamState{}, streamState{firmary: true}, streamState{firmary: true}
	off.line(firmary)
	on.line(firmary)
	onOther.line(other)
	if !off.toolUsed || on.toolUsed || !onOther.toolUsed {
		t.Fatalf("off: %v, on: %v, other server: %v", off.toolUsed, on.toolUsed, onOther.toolUsed)
	}
}

// TestAgentMessagesAreParagraphs: an agent message after a Firmary tool call starts a new paragraph instead of running
// on from the one before it.
func TestAgentMessagesAreParagraphs(t *testing.T) {
	s := streamState{firmary: true}
	var text strings.Builder
	for _, l := range []string{
		`{"type":"item.completed","item":{"id":"a_1","type":"agent_message","text":"Let me look that up."}}`,
		`{"type":"item.completed","item":{"id":"m_1","type":"mcp_tool_call","server":"firmary","tool":"x","status":"completed"}}`,
		`{"type":"item.completed","item":{"id":"a_2","type":"agent_message","text":"It is due Friday."}}`,
	} {
		for _, e := range s.line([]byte(l)) {
			text.WriteString(e.Text)
		}
	}
	if got := text.String(); got != "Let me look that up.\n\nIt is due Friday." {
		t.Fatalf("text %q", got)
	}
}
