// Package launch parses and checks a turn's launch profile: how the turn runs on the person's plan (system prompt,
// effort, tools and a few named provider knobs), set by Firmary per turn (design
// reviews/relay-launch-profile-design-2026-11-02.md §1 and §3, contract firmary-os
// packages/validators/src/ai-relay/launch-profile.ts).
//
// This is the Helper's hard ceiling. A profile can only choose among the named settings below, each with checked
// values; each driver turns them into flags or config with its own code. No profile field is ever forwarded as a raw
// flag, environment variable or config key, so nothing that runs on this computer (shell, files, local fetch, MCP,
// hooks, other permission modes, other endpoints or keys) can be switched on remotely. Parse refuses the whole profile
// on anything it doesn't know: it never runs a turn with part of the profile dropped, since a dropped part could be a
// firm's locked instructions.
package launch

import (
	"bytes"
	"encoding/json"
	"slices"
	"unicode/utf16"

	"github.com/Firmary/bridge/internal/protocol"
)

// Schema is the only profile schema version this build parses.
const Schema = 1

// Limits, the same as the contract's.
const (
	MaxSystemPrompt = 32_768 // UTF-16 code units, as the server counts them
	MaxTurns        = 10
)

// Tool names in the Helper's catalog. v1 holds only provider-hosted web search, which runs on Anthropic's or OpenAI's
// servers, never on this computer. Each driver maps every entry to its own fixed flags or config keys.
const ToolWebSearch = "web_search"

// Tools is the catalog, in the order the Helper declares it.
var Tools = []string{ToolWebSearch}

// Efforts are the provider-neutral effort levels.
var Efforts = []string{"low", "medium", "high", "xhigh", "max"}

// Codex knob values.
var (
	CodexReasoningSummaries = []string{"none", "auto", "concise", "detailed"}
	CodexVerbosities        = []string{"low", "medium", "high"}
)

// Knob names, as field names under the profile's claudeCode and codex objects.
var (
	ClaudeKnobs = []string{"replaceBuiltinInstructions"}
	CodexKnobs  = []string{"reasoningSummary", "verbosity", "replaceBuiltinInstructions"}
)

// Reason is a closed-set refusal code, safe to log and to send back as an error pattern.
type Reason string

func (r Reason) Error() string { return "launch profile refused: " + string(r) }

const (
	// ReasonInvalid: not a valid schema-1 profile (unknown field, value out of range, tool outside the catalog...).
	ReasonInvalid Reason = "profile_invalid"
	// ReasonUnsupported: a schema version this build doesn't know.
	ReasonUnsupported Reason = "profile_unsupported"
)

// Profile is a parsed, checked launch profile. The zero value of each field means "absent".
type Profile struct {
	// SystemPrompt is the whole system prompt, assembled by the server; "" means the Helper's built-in default.
	SystemPrompt string
	// Effort is one of Efforts; "" means the provider app's own default.
	Effort string
	// Tools are catalog names, each at most once; empty means no tools at all.
	Tools []string
	// MaxTurns bounds the agent loop when a tool is on (1..MaxTurns); 0 means absent. Ignored with no tools.
	MaxTurns   int
	ClaudeCode ClaudeCode
	Codex      Codex
}

// ClaudeCode holds the Claude Code knobs.
type ClaudeCode struct {
	// KeepBuiltinInstructions: the profile set replaceBuiltinInstructions to false, so Claude Code keeps its own
	// system prompt and Firmary's is appended. The default (absent or true) replaces it.
	KeepBuiltinInstructions bool
}

// Codex holds the Codex knobs.
type Codex struct {
	ReasoningSummary string // one of CodexReasoningSummaries, or ""
	Verbosity        string // one of CodexVerbosities, or ""
	// KeepBuiltinInstructions: as for ClaudeCode; Firmary's prompt is then added as developer instructions.
	KeepBuiltinInstructions bool
}

// HasTool reports whether the profile turns on a catalog tool.
func (p Profile) HasTool(name string) bool { return slices.Contains(p.Tools, name) }

// Capabilities is what this build declares in hello: the server never sends a profile field, tool or knob outside it.
func Capabilities() *protocol.LaunchCapabilities {
	return &protocol.LaunchCapabilities{
		ProfileSchemas: []int{Schema},
		Tools:          slices.Clone(Tools),
		ClaudeKnobs:    slices.Clone(ClaudeKnobs),
		CodexKnobs:     slices.Clone(CodexKnobs),
	}
}

// object decodes a JSON object into its members, keeping each value raw. Keys are matched exactly later (Go's own
// struct decoding matches them case-insensitively, which the contract doesn't). A repeated key is refused.
func object(b []byte) (map[string]json.RawMessage, bool) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, false
	}
	m := map[string]json.RawMessage{}
	for dec.More() {
		t, err := dec.Token()
		k, ok := t.(string)
		if err != nil || !ok {
			return nil, false
		}
		var v json.RawMessage
		if dec.Decode(&v) != nil {
			return nil, false
		}
		if _, dup := m[k]; dup {
			return nil, false
		}
		m[k] = v
	}
	if t, err := dec.Token(); err != nil || t != json.Delim('}') {
		return nil, false
	}
	if _, err := dec.Token(); err == nil { // trailing data
		return nil, false
	}
	return m, true
}

// str decodes a JSON string, refusing null and every other type.
func str(v json.RawMessage) (string, bool) {
	if len(v) == 0 || v[0] != '"' {
		return "", false
	}
	var s string
	return s, json.Unmarshal(v, &s) == nil
}

// oneOf decodes a string that must be one of values.
func oneOf(v json.RawMessage, values []string) (string, bool) {
	s, ok := str(v)
	return s, ok && slices.Contains(values, s)
}

// boolean decodes true or false, refusing null and every other type.
func boolean(v json.RawMessage) (bool, bool) {
	switch string(v) {
	case "true":
		return true, true
	case "false":
		return false, true
	}
	return false, false
}

// integer decodes a JSON number with an integral value (2 and 2.0 alike, as JavaScript reads them).
func integer(v json.RawMessage) (int, bool) {
	var n json.Number
	if len(v) == 0 || (v[0] != '-' && (v[0] < '0' || v[0] > '9')) || json.Unmarshal(v, &n) != nil {
		return 0, false
	}
	f, err := n.Float64()
	if err != nil || f != float64(int64(f)) || f < -1<<31 || f > 1<<31 {
		return 0, false
	}
	return int(f), true
}

// Parse checks raw, the turn's profile text, against the ceiling. Call it only after the text's hash matched the
// ticket. It refuses with ReasonUnsupported for another schema version and ReasonInvalid for anything else wrong.
func Parse(raw string) (Profile, error) {
	var p Profile
	m, ok := object([]byte(raw))
	if !ok {
		return p, ReasonInvalid
	}
	sv, ok := m["schema"]
	if !ok {
		return p, ReasonInvalid
	}
	if n, ok := integer(sv); !ok || n < 1 {
		return p, ReasonInvalid
	} else if n != Schema {
		return p, ReasonUnsupported
	}
	for k, v := range m {
		ok := true
		switch k {
		case "schema":
		case "systemPrompt":
			p.SystemPrompt, ok = str(v)
			ok = ok && p.SystemPrompt != "" && len(utf16.Encode([]rune(p.SystemPrompt))) <= MaxSystemPrompt
		case "effort":
			p.Effort, ok = oneOf(v, Efforts)
		case "tools":
			ok = parseTools(v, &p)
		case "maxTurns":
			p.MaxTurns, ok = integer(v)
			ok = ok && p.MaxTurns >= 1 && p.MaxTurns <= MaxTurns
		case "claudeCode":
			ok = parseClaudeCode(v, &p.ClaudeCode)
		case "codex":
			ok = parseCodex(v, &p.Codex)
		default:
			ok = false
		}
		if !ok {
			return Profile{}, ReasonInvalid
		}
	}
	return p, nil
}

// parseTools: an array of catalog names, none repeated.
func parseTools(v json.RawMessage, p *Profile) bool {
	if len(v) == 0 || v[0] != '[' {
		return false
	}
	var items []json.RawMessage
	if json.Unmarshal(v, &items) != nil || len(items) > len(Tools) {
		return false
	}
	for _, it := range items {
		name, ok := oneOf(it, Tools)
		if !ok || slices.Contains(p.Tools, name) {
			return false
		}
		p.Tools = append(p.Tools, name)
	}
	return true
}

func parseClaudeCode(v json.RawMessage, c *ClaudeCode) bool {
	m, ok := object(v)
	if !ok {
		return false
	}
	for k, v := range m {
		switch k {
		case "replaceBuiltinInstructions":
			replace, ok := boolean(v)
			if !ok {
				return false
			}
			c.KeepBuiltinInstructions = !replace
		default:
			return false
		}
	}
	return true
}

func parseCodex(v json.RawMessage, c *Codex) bool {
	m, ok := object(v)
	if !ok {
		return false
	}
	for k, v := range m {
		ok := true
		switch k {
		case "reasoningSummary":
			c.ReasoningSummary, ok = oneOf(v, CodexReasoningSummaries)
		case "verbosity":
			c.Verbosity, ok = oneOf(v, CodexVerbosities)
		case "replaceBuiltinInstructions":
			var replace bool
			replace, ok = boolean(v)
			c.KeepBuiltinInstructions = !replace
		default:
			ok = false
		}
		if !ok {
			return false
		}
	}
	return true
}
