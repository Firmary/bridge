package claudecode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Firmary/bridge/internal/driver"
	"github.com/Firmary/bridge/internal/driver/prompt"
	"github.com/Firmary/bridge/internal/launch"
	"github.com/Firmary/bridge/internal/proc"
	"github.com/Firmary/bridge/internal/protocol"
)

// ModelRe is what a model id may look like. It starts with a letter so it can never be read as a flag.
var ModelRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._:\[\]-]{0,99}$`)

// MaxOutputTokensCap bounds the ticket's maxOutputTokens.
const MaxOutputTokensCap = 128000

// fixedArgs: print mode with streamed JSON and nothing that could act on this computer. No MCP servers but the one
// --mcp-config names (only Firmary's own, written by this package; --strict-mcp-config ignores every other MCP
// configuration), no settings files (user, project or local), no slash commands or skills, nothing saved to resume,
// and only the built-in tools named in --tools, which come from the catalog (toolNames), never from the server. A
// launch profile can't change any of these.
func fixedArgs(model, tools string) []string {
	return []string{
		"-p", "--input-format", "text", "--output-format", "stream-json", "--verbose", "--include-partial-messages",
		"--tools", tools, "--strict-mcp-config", "--setting-sources", "", "--disable-slash-commands",
		"--no-session-persistence", "--permission-mode", "default", "--model", model,
	}
}

// toolNames maps each launch catalog tool to the built-in Claude Code tools it turns on (--tools), and allowNames to
// the tools --allowedTools lets run without a prompt. WebSearch runs on Anthropic's servers; WebFetch, which fetches
// from this computer, is never included. The firmary tool turns on no built-in tool: --tools only lists built-ins and
// leaves MCP tools alone (Claude Code 2.1.304 --help: "Specify the list of available tools from the built-in set"), so
// the --tools value stays "" and the tools of the one MCP server in --mcp-config are offered. "mcp__firmary" allows
// every tool of that server and nothing else (Claude Code's permission rule for a whole MCP server).
var (
	toolNames = map[string][]string{
		launch.ToolWebSearch: {"WebSearch"},
		launch.ToolFirmary:   nil,
	}
	allowNames = map[string][]string{
		launch.ToolWebSearch: {"WebSearch"},
		launch.ToolFirmary:   {"mcp__" + firmaryServer},
	}
)

// firmaryServer is the name of Firmary's MCP server in the turn's MCP config, and so the prefix of its tool names.
const firmaryServer = "firmary"

// efforts maps each launch effort to Claude Code's --effort level (low, medium, high, xhigh, max on 2.1.304).
var efforts = map[string]string{"low": "low", "medium": "medium", "high": "high", "xhigh": "xhigh", "max": "max"}

// runArgs: the fixed flags, then the launch profile's knobs, each mapped to a flag and value from this package's own
// tables. The only server string that reaches the command line is the model (from the ticket, checked by ModelRe);
// max turns is a checked number, and the system prompt goes in a file whose path is passed.
func runArgs(p driver.Profile, model string, l launch.Profile) []string {
	var tools, allowed []string
	for _, name := range l.Tools {
		tools = append(tools, toolNames[name]...)
		allowed = append(allowed, allowNames[name]...)
	}
	args := fixedArgs(model, strings.Join(tools, ","))
	if l.HasTool(launch.ToolFirmary) {
		// The file holds the turn's access token, so it is passed by path (0600, removed when the run ends).
		args = append(args, "--mcp-config", mcpConfigFile(p))
	}
	if len(allowed) > 0 {
		// Under --permission-mode default nobody can approve a tool in -p, so a tool runs only if it is allowed here.
		// A tool means an agent loop, so it is always bounded: by the profile's maxTurns, else by the ceiling.
		turns := l.MaxTurns
		if turns <= 0 {
			turns = launch.MaxTurns
		}
		args = append(args, "--allowedTools", strings.Join(allowed, ","), "--max-turns", strconv.Itoa(turns))
	}
	if e, ok := efforts[l.Effort]; ok {
		args = append(args, "--effort", e)
	}
	return append(args, systemPromptArgs(p, l)...)
}

// systemPromptFile is where the turn's system prompt is written for --system-prompt-file.
func systemPromptFile(p driver.Profile) string {
	return filepath.Join(p.Dir, "firmary-system-prompt.md")
}

// mcpConfigFile is where the turn's MCP config for --mcp-config is written when the firmary tool is on.
func mcpConfigFile(p driver.Profile) string {
	return filepath.Join(p.Dir, "firmary-mcp.json")
}

// mcpConfig is the --mcp-config document: Firmary's MCP server over Streamable HTTP with the turn's bearer token.
type mcpConfig struct {
	MCPServers map[string]mcpServer `json:"mcpServers"`
}

type mcpServer struct {
	Type    string            `json:"type"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

// writeMCPConfig writes the turn's MCP config (0600). The token goes in this file only: never on the command line,
// where other local users could read it, nor in the environment.
func writeMCPConfig(p driver.Profile, f launch.Firmary) error {
	b, err := json.Marshal(mcpConfig{MCPServers: map[string]mcpServer{firmaryServer: {
		Type: "http", URL: f.URL, Headers: map[string]string{"Authorization": "Bearer " + f.AccessToken},
	}}})
	if err != nil {
		return err
	}
	return driver.WriteFileAtomic(mcpConfigFile(p), b)
}

// systemPromptText is the turn's system prompt: the launch profile's, else Firmary's built-in default for the
// profile's tools.
func systemPromptText(l launch.Profile) string {
	if l.SystemPrompt != "" {
		return l.SystemPrompt
	}
	return prompt.Fallback(l.HasTool(launch.ToolWebSearch))
}

// writeSystemPrompt writes the turn's resolved system prompt to the profile (0600, rewritten every turn). It goes
// in a file, never on the command line: it holds firm and personal instructions, and other local users can read the
// process list.
func writeSystemPrompt(p driver.Profile, text string) error {
	return driver.WriteFileAtomic(systemPromptFile(p), []byte(text))
}

// systemPromptArgs is the one place the Claude Code driver passes Firmary's system prompt. --system-prompt-file
// replaces Claude Code's built-in prompt (the default); a profile that keeps the built-in prompt appends Firmary's
// with --append-system-prompt-file instead.
func systemPromptArgs(p driver.Profile, l launch.Profile) []string {
	if l.ClaudeCode.KeepBuiltinInstructions {
		return []string{"--append-system-prompt-file", systemPromptFile(p)}
	}
	return []string{"--system-prompt-file", systemPromptFile(p)}
}

// input is the text written to Claude Code's stdin. The prompt never goes on the command line, where other local
// users could read it in the process list. It is always wrapped, even with no context, so the prompt is never the
// start of stdin: Claude Code handles input starting with "/" as a local command instead of sending it to the model.
func input(t driver.Turn) string {
	return "<firmary_context>\n" + t.Context + "\n</firmary_context>\n\n" + t.Prompt
}

// Run runs one turn with the pinned Claude Code under the profile.
func (d *Driver) Run(ctx context.Context, p driver.Profile, t driver.Turn, emit func(protocol.StreamEvent)) error {
	fail := func(class, pattern string) error {
		emit(protocol.Error(class, pattern))
		return &driver.Failure{Class: class, Pattern: pattern}
	}
	_, bin := d.Installed()
	if bin == "" {
		return fail(protocol.ClassFatal, "provider_not_installed")
	}
	if !ModelRe.MatchString(t.Model) {
		return fail(protocol.ClassPolicy, "model_invalid")
	}
	if t.MaxOutputTokens <= 0 || t.MaxOutputTokens > MaxOutputTokensCap {
		return fail(protocol.ClassPolicy, "max_output_tokens_invalid")
	}
	if err := prepare(p); err != nil {
		return fail(protocol.ClassFatal, "profile_unavailable")
	}
	if err := writeSystemPrompt(p, systemPromptText(t.Launch)); err != nil {
		return fail(protocol.ClassFatal, "profile_unavailable")
	}
	// A config left by a run that never got to clean up (a crash) holds a token that has since lapsed; it goes either
	// way. The helper sends a credential exactly when the profile turns the firmary tool on.
	if err := os.Remove(mcpConfigFile(p)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fail(protocol.ClassFatal, "profile_unavailable")
	}
	if t.Launch.HasTool(launch.ToolFirmary) != (t.Firmary != nil) {
		return fail(protocol.ClassPolicy, string(launch.ReasonFirmaryInvalid))
	}
	if t.Firmary != nil {
		defer os.Remove(mcpConfigFile(p))
		if err := writeMCPConfig(p, *t.Firmary); err != nil {
			return fail(protocol.ClassFatal, "profile_unavailable")
		}
	}

	cmd := proc.CommandContext(ctx, bin, runArgs(p, t.Model, t.Launch)...)
	cmd.Dir = workDir(p)
	set := map[string]string{"CLAUDE_CODE_MAX_OUTPUT_TOKENS": strconv.Itoa(t.MaxOutputTokens)}
	for k, v := range d.extraEnv {
		set[k] = v
	}
	cmd.Env = Env(d.environ(), configDir(p), set)
	cmd.Stdin = strings.NewReader(input(t))
	// stderr is kept in memory, bounded, only to classify a failure. It is never logged or forwarded.
	var stderr bytes.Buffer
	cmd.Stderr = &limitedWriter{w: &stderr, n: 64 << 10}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fail(protocol.ClassFatal, "spawn_failed")
	}
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return fail(protocol.ClassFatal, "spawn_failed")
	}

	var st streamState
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for sc.Scan() {
		for _, e := range st.line(sc.Bytes()) {
			emit(e)
		}
	}
	_, _ = io.Copy(io.Discard, stdout)
	waitErr := cmd.Wait()

	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fail(protocol.ClassTransient, "turn_timeout")
	}
	if ctx.Err() != nil {
		return fail(protocol.ClassFatal, "cancelled")
	}
	if st.result != nil && !st.result.IsError && waitErr == nil {
		emit(protocol.Done(st.result.SessionID, st.result.tokens()))
		return nil
	}
	var text strings.Builder
	text.WriteString(st.assistantError)
	if st.result != nil {
		text.WriteString("\n" + st.result.Result + "\n" + st.result.TerminalReason + "\n" + st.result.Subtype)
	}
	text.WriteString("\n" + stderr.String())
	class, pattern := Classify(text.String())
	if st.result == nil && class == protocol.ClassFatal {
		pattern = "no_result"
	}
	return fail(class, pattern)
}

// streamState turns Claude Code's stream-json lines into stream events.
type streamState struct {
	started        bool
	deltaSeen      bool
	assistantError string
	result         *resultMsg
}

type resultMsg struct {
	Subtype        string `json:"subtype"`
	IsError        bool   `json:"is_error"`
	Result         string `json:"result"`
	SessionID      string `json:"session_id"`
	TerminalReason string `json:"terminal_reason"`
	Usage          struct {
		InputTokens   int64 `json:"input_tokens"`
		OutputTokens  int64 `json:"output_tokens"`
		CacheRead     int64 `json:"cache_read_input_tokens"`
		CacheCreation int64 `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

func (r *resultMsg) tokens() *protocol.TokenCounts {
	u := r.Usage
	return &protocol.TokenCounts{Input: u.InputTokens, Output: u.OutputTokens, CacheRead: u.CacheRead, CacheWrite: u.CacheCreation}
}

type rateWindow struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    *float64 `json:"resetsAt"`
}

type streamLine struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Model   string `json:"model"`
	Error   string `json:"error"`
	Event   struct {
		Type  string `json:"type"`
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	} `json:"event"`
	Message struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
	RateLimitInfo *struct {
		Status         string                `json:"status"`
		RateLimitType  string                `json:"rateLimitType"`
		Utilization    *float64              `json:"utilization"`
		ResetsAt       *float64              `json:"resetsAt"`
		UnifiedWindows map[string]rateWindow `json:"unifiedWindows"`
	} `json:"rate_limit_info"`
}

func isoTime(sec *float64) string {
	if sec == nil {
		return ""
	}
	s := *sec
	if s > 1e12 { // already milliseconds
		s /= 1000
	}
	return time.Unix(int64(s), 0).UTC().Format(time.RFC3339)
}

// limitWindow converts one Claude Code rate-limit window. Claude Code reports utilization as a 0-1 fraction; the
// protocol wants a 0-100 percentage. A window whose name isn't a machine name is dropped.
func limitWindow(name string, utilization, resetsAt *float64) (protocol.LimitWindow, bool) {
	if !protocol.MachineNameRe.MatchString(name) {
		return protocol.LimitWindow{}, false
	}
	w := protocol.LimitWindow{Window: name, ResetsAt: isoTime(resetsAt)}
	if utilization != nil && !math.IsNaN(*utilization) && !math.IsInf(*utilization, 0) {
		p := min(max(*utilization*100, 0), 100)
		w.UsedPercent = &p
	}
	return w, true
}

func (s *streamState) line(b []byte) []protocol.StreamEvent {
	var m streamLine
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	switch m.Type {
	case "system":
		if m.Subtype == "init" && !s.started {
			s.started = true
			return []protocol.StreamEvent{protocol.Start(m.Model)}
		}
	case "stream_event":
		if m.Event.Type == "content_block_delta" && m.Event.Delta.Type == "text_delta" && m.Event.Delta.Text != "" {
			s.deltaSeen = true
			return []protocol.StreamEvent{protocol.TextDelta(m.Event.Delta.Text)}
		}
	case "assistant":
		// An assistant message with an error is Claude Code's own error text (for example "Not logged in"), not
		// model output: it is kept only for classification.
		if m.Error != "" {
			s.assistantError = m.Error
			for _, c := range m.Message.Content {
				s.assistantError += "\n" + c.Text
			}
			return nil
		}
		seen := s.deltaSeen
		s.deltaSeen = false
		if seen {
			return nil
		}
		var out []protocol.StreamEvent
		for _, c := range m.Message.Content {
			if c.Type == "text" && c.Text != "" {
				out = append(out, protocol.TextDelta(c.Text))
			}
		}
		return out
	case "rate_limit_event":
		info := m.RateLimitInfo
		if info == nil {
			return nil
		}
		var ws []protocol.LimitWindow
		names := make([]string, 0, len(info.UnifiedWindows))
		for name := range info.UnifiedWindows {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			w := info.UnifiedWindows[name]
			if lw, ok := limitWindow(name, w.Utilization, w.ResetsAt); ok && len(ws) < protocol.MaxLimitWindows {
				ws = append(ws, lw)
			}
		}
		if len(info.UnifiedWindows) == 0 && info.RateLimitType != "" {
			if lw, ok := limitWindow(info.RateLimitType, info.Utilization, info.ResetsAt); ok {
				ws = []protocol.LimitWindow{lw}
			}
		}
		if info.Status == "rejected" {
			return []protocol.StreamEvent{protocol.Limit(isoTime(info.ResetsAt))}
		}
		if len(ws) > 0 {
			return []protocol.StreamEvent{protocol.Usage(ws, nil)}
		}
	case "result":
		var r resultMsg
		if json.Unmarshal(b, &r) == nil {
			s.result = &r
		}
	}
	return nil
}
