package pairing

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"
)

// TestNormalizeCode: what a person types becomes the relay's ABCD-EFGH form, and the result matches the contract's
// pairing code pattern (internal/protocol/testdata, copied from firmary-os).
func TestNormalizeCode(t *testing.T) {
	raw, err := os.ReadFile("../protocol/testdata/helper-to-relay.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		OneOf []struct {
			Properties map[string]struct {
				Pattern string `json:"pattern"`
			} `json:"properties"`
		} `json:"oneOf"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	var pattern *regexp.Regexp
	for _, m := range schema.OneOf {
		if p := m.Properties["code"].Pattern; p != "" {
			pattern = regexp.MustCompile(p)
		}
	}
	if pattern == nil {
		t.Fatal("no pairing_reveal code pattern in the schema")
	}
	for in, want := range map[string]string{
		"R7KD-4MPX":    "R7KD-4MPX",
		"r7kd4mpx":     "R7KD-4MPX",
		" r7kd 4mpx\n": "R7KD-4MPX",
		"R7-KD-4M-PX":  "R7KD-4MPX",
		"r7kd–4mpx":    "R7KD-4MPX", // an en dash pasted from a web page
		"IL0O-abcd":    "1100-ABCD",
		"oooo-iiii":    "0000-1111",
	} {
		got, err := NormalizeCode(in)
		if err != nil || got != want {
			t.Errorf("NormalizeCode(%q) = %q, %v; want %q", in, got, err, want)
			continue
		}
		if !pattern.MatchString(got) {
			t.Errorf("NormalizeCode(%q) = %q doesn't match the contract pattern %s", in, got, pattern)
		}
	}
	for _, bad := range []string{"", "R7KD-4MP", "R7KD-4MPXY", "R7KD-4MPU", "R7KD_4MPX", "R7KD-4MPÉ", "R7KD-4MP!"} {
		if got, err := NormalizeCode(bad); err == nil {
			t.Errorf("NormalizeCode(%q) = %q, want an error", bad, got)
		}
	}
}
