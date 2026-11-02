package launch

import (
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
)

// The shared fixtures and vectors are copied from Firmary's packages/validators/ai-relay/helper/fixtures into
// internal/protocol/testdata.

// TestSharedProfileFixtures: every valid shared profile parses and every invalid one is refused, with
// profile_unsupported only for an unknown schema version.
func TestSharedProfileFixtures(t *testing.T) {
	raw, err := os.ReadFile("../protocol/testdata/launch-profile.fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Valid, Invalid []struct {
			Name     string
			Path     string
			Document json.RawMessage
		}
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Valid) == 0 || len(f.Invalid) == 0 {
		t.Fatal("fixtures file is missing a section")
	}
	for _, c := range f.Valid {
		if _, err := Parse(string(c.Document)); err != nil {
			t.Errorf("valid %q refused: %v", c.Name, err)
		}
	}
	for _, c := range f.Invalid {
		want := ReasonInvalid
		if c.Name == "unknown schema version" {
			want = ReasonUnsupported
		}
		if _, err := Parse(string(c.Document)); !errors.Is(err, want) {
			t.Errorf("invalid %q: got %v, want %s", c.Name, err, want)
		}
	}

	var v struct {
		ProfileHash []struct{ Name, Profile string }
	}
	raw, _ = os.ReadFile("../protocol/testdata/launch-profile.vectors.json")
	if err := json.Unmarshal(raw, &v); err != nil || len(v.ProfileHash) == 0 {
		t.Fatalf("vectors: %v", err)
	}
	for _, c := range v.ProfileHash {
		if _, err := Parse(c.Profile); err != nil {
			t.Errorf("vector %q refused: %v", c.Name, err)
		}
	}
}

// TestParseValues: what a parsed profile holds, including the replace switches' default.
func TestParseValues(t *testing.T) {
	p, err := Parse(`{"schema":1,"systemPrompt":"Answer.","effort":"xhigh","tools":["web_search"],"maxTurns":4,` +
		`"claudeCode":{"replaceBuiltinInstructions":false},"codex":{"reasoningSummary":"concise","verbosity":"low","replaceBuiltinInstructions":true}}`)
	if err != nil {
		t.Fatal(err)
	}
	want := Profile{SystemPrompt: "Answer.", Effort: "xhigh", Tools: []string{"web_search"}, MaxTurns: 4,
		ClaudeCode: ClaudeCode{KeepBuiltinInstructions: true}, Codex: Codex{ReasoningSummary: "concise", Verbosity: "low"}}
	if p.SystemPrompt != want.SystemPrompt || p.Effort != want.Effort || !slices.Equal(p.Tools, want.Tools) ||
		p.MaxTurns != want.MaxTurns || p.ClaudeCode != want.ClaudeCode || p.Codex != want.Codex {
		t.Fatalf("got %+v, want %+v", p, want)
	}
	if p, err := Parse(`{"schema":1}`); err != nil || p.ClaudeCode.KeepBuiltinInstructions || p.Codex.KeepBuiltinInstructions ||
		p.SystemPrompt != "" || len(p.Tools) != 0 {
		t.Fatalf("built-in profile: %+v %v", p, err)
	}
	// JavaScript reads 4.0 as 4, so the contract does too.
	if p, err := Parse(`{"schema":1.0,"maxTurns":4.0}`); err != nil || p.MaxTurns != 4 {
		t.Fatalf("integral numbers: %+v %v", p, err)
	}
}

// TestParseRefusesWhatGoWouldAccept: cases the contract refuses but Go's own JSON decoding would let through, plus
// the Helper's own checks (repeated tools, the prompt's size in UTF-16 units).
func TestParseRefusesWhatGoWouldAccept(t *testing.T) {
	long := func(n int, s string) string { return `{"schema":1,"systemPrompt":"` + strings.Repeat(s, n) + `"}` }
	for _, c := range []struct{ name, doc string }{
		{"key in another case", `{"Schema":1}`},
		{"field in another case", `{"schema":1,"SystemPrompt":"x"}`},
		{"knob in another case", `{"schema":1,"codex":{"Verbosity":"low"}}`},
		{"null effort", `{"schema":1,"effort":null}`},
		{"null prompt", `{"schema":1,"systemPrompt":null}`},
		{"null tools", `{"schema":1,"tools":null}`},
		{"null knobs", `{"schema":1,"codex":null}`},
		{"null switch", `{"schema":1,"claudeCode":{"replaceBuiltinInstructions":null}}`},
		{"number as a string", `{"schema":"1"}`},
		{"max turns as a string", `{"schema":1,"maxTurns":"4"}`},
		{"repeated tool", `{"schema":1,"tools":["web_search","web_search"]}`},
		{"repeated key", `{"schema":1,"effort":"low","effort":"max"}`},
		{"trailing data", `{"schema":1} {"schema":1}`},
		{"not an object", `[{"schema":1}]`},
		{"tool in another case", `{"schema":1,"tools":["Web_Search"]}`},
		{"prompt over the limit", long(32_769, "a")},
		{"astral characters count twice", long(16_385, "😀")},
	} {
		if _, err := Parse(c.doc); !errors.Is(err, ReasonInvalid) {
			t.Errorf("%s: got %v, want profile_invalid", c.name, err)
		}
	}
	if _, err := Parse(long(32_768, "a")); err != nil {
		t.Errorf("prompt at the limit: %v", err)
	}
	if _, err := Parse(`{"schema":2,"anything":true}`); !errors.Is(err, ReasonUnsupported) {
		t.Errorf("schema 2: got %v, want profile_unsupported", err)
	}
}

// TestCapabilitiesMatchCatalog: hello declares exactly the catalog and knobs Parse accepts.
func TestCapabilitiesMatchCatalog(t *testing.T) {
	c := Capabilities()
	if !slices.Equal(c.ProfileSchemas, []int{Schema}) || !slices.Equal(c.Tools, Tools) ||
		!slices.Equal(c.ClaudeKnobs, ClaudeKnobs) || !slices.Equal(c.CodexKnobs, CodexKnobs) {
		t.Fatalf("capabilities %+v", c)
	}
	if !slices.Equal(Tools, []string{"web_search"}) {
		t.Fatalf("the catalog grew to %v: each new entry needs its own security review (design §3)", Tools)
	}
	for _, k := range ClaudeKnobs {
		if _, err := Parse(`{"schema":1,"claudeCode":{"` + k + `":` + sample(k) + `}}`); err != nil {
			t.Errorf("declared Claude Code knob %s refused: %v", k, err)
		}
	}
	for _, k := range CodexKnobs {
		if _, err := Parse(`{"schema":1,"codex":{"` + k + `":` + sample(k) + `}}`); err != nil {
			t.Errorf("declared Codex knob %s refused: %v", k, err)
		}
	}
}

func sample(knob string) string {
	switch knob {
	case "reasoningSummary":
		return `"auto"`
	case "verbosity":
		return `"high"`
	}
	return "false"
}
