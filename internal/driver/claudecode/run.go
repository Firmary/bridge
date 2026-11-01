package claudecode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Firmary/firmary-helper/internal/driver"
	"github.com/Firmary/firmary-helper/internal/protocol"
)

// ModelRe is what a model id may look like. It starts with a letter so it can never be read as a flag.
var ModelRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._:\[\]-]{0,99}$`)

// MaxOutputTokensCap bounds the ticket's maxOutputTokens.
const MaxOutputTokensCap = 128000

// runArgs: print mode with streamed JSON, and nothing that could act on this computer. No tools, no MCP servers,
// no settings files (user, project or local), no slash commands or skills, nothing saved to resume.
func runArgs(model string) []string {
	return []string{
		"-p", "--input-format", "text", "--output-format", "stream-json", "--verbose", "--include-partial-messages",
		"--tools", "", "--strict-mcp-config", "--setting-sources", "", "--disable-slash-commands",
		"--no-session-persistence", "--permission-mode", "default", "--model", model,
	}
}

// input is the text written to Claude Code's stdin. The prompt never goes on the command line, where other local
// users could read it in the process list.
func input(t driver.Turn) string {
	if t.Context == "" {
		return t.Prompt
	}
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

	cmd := exec.CommandContext(ctx, bin, runArgs(t.Model)...)
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
	return &protocol.TokenCounts{Input: u.InputTokens, Output: u.OutputTokens, CacheRead: u.CacheRead, CacheCreation: u.CacheCreation}
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
		for name, w := range info.UnifiedWindows {
			ws = append(ws, protocol.LimitWindow{Name: name, Utilization: w.Utilization, ResetsAt: isoTime(w.ResetsAt)})
		}
		if len(ws) == 0 && info.RateLimitType != "" {
			ws = []protocol.LimitWindow{{Name: info.RateLimitType, Utilization: info.Utilization, ResetsAt: isoTime(info.ResetsAt)}}
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
