package protocol

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Firmary/firmary-helper/internal/devicekey"
	"github.com/Firmary/firmary-helper/internal/pairing"
	"github.com/Firmary/firmary-helper/internal/ticket"
)

// The testdata files are copied from Firmary's packages/validators/ai-relay/helper/fixtures, so both sides check the
// same vectors and fixtures.

type vectors struct {
	PromptHash []struct {
		Name, Context, Prompt string
		Schema                *string
		PromptHash            string
	}
	Auth []struct {
		Name, TestSeed, RelayURL, Nonce, PublicKey, Payload, Signature string
	}
	Pairing []struct {
		Name, PublicKey, HelperNonce, RelayNonce, PairingCommit, PairingCode string
	}
}

func b64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("%q: %v", s, err)
	}
	return b
}

func TestSharedCryptoVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/relay-crypto.vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.PromptHash) == 0 || len(v.Auth) == 0 || len(v.Pairing) == 0 {
		t.Fatal("vectors file is missing a section")
	}
	enc := base64.RawURLEncoding.EncodeToString

	for _, c := range v.PromptHash {
		turn := Turn{Context: c.Context, Prompt: c.Prompt, Schema: c.Schema}
		if got := turn.PromptHash(); got != c.PromptHash {
			t.Errorf("promptHash %q: %s, want %s", c.Name, got, c.PromptHash)
		}
	}

	for _, c := range v.Auth {
		// Load the synthetic seed through the Helper's own key file path, then sign as the Helper does.
		path := filepath.Join(t.TempDir(), "device.key")
		if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(b64(t, c.TestSeed))), 0o600); err != nil {
			t.Fatal(err)
		}
		key, err := devicekey.LoadOrCreate(path)
		if err != nil {
			t.Fatal(err)
		}
		nonce := b64(t, c.Nonce)
		if got := enc(key.Public()); got != c.PublicKey {
			t.Errorf("auth %q: public key %s, want %s", c.Name, got, c.PublicKey)
		}
		if got := string(devicekey.AuthPayload(c.RelayURL, nonce)); got != c.Payload {
			t.Errorf("auth %q: payload %q, want %q", c.Name, got, c.Payload)
		}
		if got := enc(key.SignAuth(c.RelayURL, nonce)); got != c.Signature {
			t.Errorf("auth %q: signature %s, want %s", c.Name, got, c.Signature)
		}
	}

	for _, c := range v.Pairing {
		pub := ed25519.PublicKey(b64(t, c.PublicKey))
		nh, nr := b64(t, c.HelperNonce), b64(t, c.RelayNonce)
		if got := enc(pairing.Commit(pub, nh)); got != c.PairingCommit {
			t.Errorf("pairing %q: commit %s, want %s", c.Name, got, c.PairingCommit)
		}
		if got := pairing.Code(pub, nr, nh); got != c.PairingCode {
			t.Errorf("pairing %q: code %s, want %s", c.Name, got, c.PairingCode)
		}
	}
}

// roundTrip decodes doc into the Helper's type and encodes it again, as generic JSON.
func roundTrip[T any](t *testing.T, doc json.RawMessage) any {
	t.Helper()
	m, err := Decode[T](doc)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// unmodelled are contract fields the Helper doesn't read or send yet; a fixture may carry them and the round trip
// drops them. Every other field must survive the round trip unchanged.
var unmodelled = map[string]bool{
	"reconnectReason": true, "updateFailure": true, // hello: self-update arrives in a later slice
	"channel": true, // the stream-event fixture's unknown field from a newer sender
	"jti":     true, // a standard ticket claim the verifier doesn't need
}

// same reports whether got and want are equal, ignoring unmodelled fields missing from got.
func same(got, want any) bool {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return false
		}
		for k := range g {
			if _, ok := w[k]; !ok {
				return false
			}
		}
		for k, wv := range w {
			gv, ok := g[k]
			if !ok && unmodelled[k] {
				continue
			}
			if !ok || !same(gv, wv) {
				return false
			}
		}
		return true
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for i := range g {
			if !same(g[i], w[i]) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(got, want)
	}
}

// TestSharedFixturesRoundTrip: each valid shared fixture, decoded into the Helper's type and encoded again, comes
// back unchanged. This catches field names and empty values the Helper would read or send differently from the
// contract.
func TestSharedFixturesRoundTrip(t *testing.T) {
	byType := map[string]func(*testing.T, json.RawMessage) any{
		TypeChallenge: roundTrip[Challenge], TypePairingPending: roundTrip[PairingPending], TypePaired: roundTrip[Paired],
		TypeWelcome: roundTrip[Welcome], TypeTurn: roundTrip[Turn], TypeCancel: roundTrip[Cancel],
		TypeRevoke: roundTrip[Revoke], TypeError: roundTrip[RelayError], TypeAuth: roundTrip[Auth],
		TypePairingReveal: roundTrip[PairingReveal], TypeHello: roundTrip[Hello], TypeEvent: roundTrip[EventMsg],
	}
	// Messages the Helper doesn't implement yet (provider sign-in arrives in a later slice).
	notYet := map[string]bool{"signin": true, "signout": true, "signin_step": true}

	check := func(file string, decode func(*testing.T, json.RawMessage) (func(*testing.T, json.RawMessage) any, bool)) {
		for _, f := range readFixtures(t, file).Valid {
			fn, ok := decode(t, f.Document)
			if !ok {
				continue
			}
			var want any
			if err := json.Unmarshal(f.Document, &want); err != nil {
				t.Fatal(err)
			}
			if got := fn(t, f.Document); !same(got, want) {
				b, _ := json.Marshal(got)
				t.Errorf("%s %q: the Helper would send %s", file, f.Name, b)
			}
		}
	}
	messages := func(t *testing.T, doc json.RawMessage) (func(*testing.T, json.RawMessage) any, bool) {
		env, _ := Decode[Envelope](doc)
		if fn, ok := byType[env.Type]; ok {
			return fn, true
		}
		if !notYet[env.Type] {
			t.Errorf("no Helper type for message %q", env.Type)
		}
		return nil, false
	}
	only := func(fn func(*testing.T, json.RawMessage) any) func(*testing.T, json.RawMessage) (func(*testing.T, json.RawMessage) any, bool) {
		return func(*testing.T, json.RawMessage) (func(*testing.T, json.RawMessage) any, bool) { return fn, true }
	}
	check("relay-to-helper.fixtures.json", messages)
	check("helper-to-relay.fixtures.json", messages)
	check("stream-event.fixtures.json", only(roundTrip[StreamEvent]))
	check("ticket-claims.fixtures.json", only(roundTrip[ticket.Claims]))
}
