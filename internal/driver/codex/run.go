package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"

	"github.com/Firmary/bridge/internal/driver"
	"github.com/Firmary/bridge/internal/launch"
	"github.com/Firmary/bridge/internal/protocol"
)

// ModelRe is what a model id may look like. It starts with a letter so it can never be read as a flag.
var ModelRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._:\[\]-]{0,99}$`)

// MaxOutputTokensCap bounds the ticket's maxOutputTokens. Codex has no setting that caps a turn's output, so the
// value is only checked, not enforced.
const MaxOutputTokensCap = 128000

// runArgs: non-interactive exec with JSONL events, the prompt read from stdin ("-"), and nothing that could act on
// this computer or be saved. --strict-config makes an unknown key (a setting a new pin dropped) fail the run.
// The launch profile's settings come as -c flags too, except the prompt text, which is only in config.toml.
func (d *Driver) runArgs(p driver.Profile, model string, l launch.Profile) []string {
	args := []string{
		"exec", "--json", "--strict-config", "--skip-git-repo-check", "--ephemeral", "--ignore-rules",
		"--sandbox", "read-only", "--color", "never", "--model", model,
	}
	launchArgs, _ := d.launchSettings(p, l)
	args = append(args, configArgs(launchArgs)...)
	return append(args, "-")
}

// input is the text written to Codex's stdin. The prompt never goes on the command line, where other local users
// could read it in the process list. It is wrapped the same way as for Claude Code.
func input(t driver.Turn) string {
	return "<firmary_context>\n" + t.Context + "\n</firmary_context>\n\n" + t.Prompt
}

// Run runs one turn with the pinned Codex under the profile.
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
	// Held until Codex exits (deferred), so no status, sign-in or sign-out rewrites this turn's prompt or config.
	firmary := t.Launch.HasTool(launch.ToolFirmary)
	if firmary != (t.Firmary != nil) {
		// The helper sends a credential exactly when the profile turns the firmary tool on.
		return fail(protocol.ClassPolicy, string(launch.ReasonFirmaryInvalid))
	}
	m := d.profileLock(p)
	m.Lock()
	defer m.Unlock()
	if err := d.prepare(p, t.Launch, t.Firmary); err != nil {
		return fail(protocol.ClassFatal, "profile_unavailable")
	}

	rctx, stop := context.WithCancel(ctx)
	defer stop()
	cmd := d.command(rctx, bin, p, d.runArgs(p, t.Model, t.Launch)...)
	if t.Firmary != nil {
		// Only this Codex gets the token, in its own environment (the Link's inherited one never carries it: Env
		// passes an allowlist).
		cmd.Env = append(cmd.Env, FirmaryTokenEnv+"="+t.Firmary.AccessToken)
	}
	cmd.Stdin = strings.NewReader(input(t))
	// stderr is kept in memory, bounded, only to classify a failure. It is never logged or forwarded.
	var stderr bytes.Buffer
	cmd.Stderr = &limitedWriter{w: &stderr, n: 64 << 10}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fail(protocol.ClassFatal, "spawn_failed")
	}
	if err := cmd.Start(); err != nil {
		return fail(protocol.ClassFatal, "spawn_failed")
	}

	st := streamState{model: t.Model, webSearch: t.Launch.HasTool(launch.ToolWebSearch), firmary: firmary}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for sc.Scan() {
		for _, e := range st.line(sc.Bytes()) {
			emit(e)
		}
		if st.toolUsed {
			stop() // defence in depth: settings allow no tool to run, so one showing up ends the turn
			break
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
	if st.toolUsed {
		return fail(protocol.ClassPolicy, "tool_use_blocked")
	}
	if st.completed && !st.failed && waitErr == nil {
		emit(protocol.Done(st.threadID, st.tokens))
		return nil
	}
	// Codex's own error events first: its stderr also carries unrelated noise (for example a failed model-list
	// refresh), so it is only a fallback.
	class, pattern := Classify(st.errorText.String())
	if pattern == "unclassified" {
		class, pattern = Classify(stderr.String())
	}
	if !st.completed && !st.failed && class == protocol.ClassFatal {
		pattern = "no_result"
	}
	if pattern == "usage_limit" {
		// Codex reports when the limit resets only as localized local-time text, so no resetsAt is sent.
		emit(protocol.Limit(""))
	}
	return fail(class, pattern)
}

// streamState turns `codex exec --json` lines into stream events.
type streamState struct {
	model     string
	webSearch bool // the launch profile turned web search on, so its items are expected
	firmary   bool // the launch profile turned Firmary's tools on, so calls to its MCP server are expected
	answered  bool // an agent message was sent; the next one (after a tool call) starts a new paragraph
	started   bool
	threadID  string
	completed bool
	failed    bool
	toolUsed  bool
	tokens    *protocol.TokenCounts
	errorText strings.Builder // Codex's own error messages, kept only for classification
}

type execLine struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
	Message  string `json:"message"`
	Error    *struct {
		Message string `json:"message"`
	} `json:"error"`
	Item *struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Message string `json:"message"`
		Server  string `json:"server"` // mcp_tool_call: the MCP server's name
	} `json:"item"`
	Usage *struct {
		InputTokens       int64 `json:"input_tokens"`
		CachedInputTokens int64 `json:"cached_input_tokens"`
		CacheWriteTokens  int64 `json:"cache_write_input_tokens"`
		OutputTokens      int64 `json:"output_tokens"`
	} `json:"usage"`
}

// toolItems are exec item types that mean something ran on this computer or outside the model.
var toolItems = map[string]bool{
	"command_execution": true, "file_change": true, "mcp_tool_call": true, "web_search": true,
	"collab_tool_call": true,
}

func (s *streamState) addError(msg string) {
	if s.errorText.Len() < 64<<10 {
		s.errorText.WriteString(msg + "\n")
	}
}

func (s *streamState) line(b []byte) []protocol.StreamEvent {
	var m execLine
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	switch m.Type {
	case "thread.started":
		s.threadID = m.ThreadID
		if !s.started {
			s.started = true
			// exec doesn't name the model it used; it is the one the ticket asked for.
			return []protocol.StreamEvent{protocol.Start(s.model)}
		}
	case "item.started", "item.updated", "item.completed":
		if m.Item == nil {
			return nil
		}
		if m.Item.Type == "web_search" && s.webSearch {
			return nil
		}
		// Firmary's own MCP server runs on Firmary, which checks the person's permissions and approvals itself.
		// A call to any other server is still a tool that shouldn't exist and ends the turn.
		if m.Item.Type == "mcp_tool_call" && s.firmary && m.Item.Server == firmaryServer {
			return nil
		}
		if toolItems[m.Item.Type] {
			s.toolUsed = true
			return nil
		}
		// An "error" item is a Codex warning (for example a feature that failed closed), not model output.
		if m.Type == "item.completed" && m.Item.Type == "agent_message" && m.Item.Text != "" {
			text := m.Item.Text
			if s.answered {
				text = "\n\n" + text
			}
			s.answered = true
			return []protocol.StreamEvent{protocol.TextDelta(text)}
		}
	case "turn.completed":
		s.completed = true
		if u := m.Usage; u != nil {
			s.tokens = &protocol.TokenCounts{Input: u.InputTokens, Output: u.OutputTokens, CacheRead: u.CachedInputTokens,
				CacheWrite: u.CacheWriteTokens}
		}
	case "turn.failed":
		s.failed = true
		if m.Error != nil {
			s.addError(m.Error.Message)
		}
	case "error":
		s.addError(m.Message)
	}
	return nil
}
