package protocol

import (
	"encoding/json"
	"os"
	"testing"
)

type fixtures struct {
	Valid []struct {
		Name     string          `json:"name"`
		Document json.RawMessage `json:"document"`
	} `json:"valid"`
}

func readFixtures(t *testing.T, name string) fixtures {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var f fixtures
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func fixture(t *testing.T, file, name string) json.RawMessage {
	t.Helper()
	for _, v := range readFixtures(t, file).Valid {
		if v.Name == name {
			return v.Document
		}
	}
	t.Fatalf("%s: no valid fixture %q", file, name)
	return nil
}

// TestTurnFixtureMatchesTicketPromptHash: the shared turn fixture, decoded by the Helper, hashes to the shared
// ticket fixture's promptHash. The schema is a JSON string and its decoded value is hashed.
func TestTurnFixtureMatchesTicketPromptHash(t *testing.T) {
	turn, err := Decode[Turn](fixture(t, "relay-to-helper.fixtures.json", "turn with structured output schema (promptHash vector)"))
	if err != nil || turn.Schema == nil {
		t.Fatalf("turn %+v, err %v", turn, err)
	}
	var claims struct {
		PromptHash string `json:"promptHash"`
	}
	if err := json.Unmarshal(fixture(t, "ticket-claims.fixtures.json", "interactive turn"), &claims); err != nil {
		t.Fatal(err)
	}
	if got := turn.PromptHash(); got != claims.PromptHash {
		t.Fatalf("turn hashes to %s, ticket says %s", got, claims.PromptHash)
	}
}
