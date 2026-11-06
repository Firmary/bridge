package ticket

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVerify(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Unix(1_800_000_000, 0)
	good := func() Claims {
		return Claims{TicketID: "t1", DeviceID: "d1", UserID: "u1", FirmID: "f1", Adapter: "helper-claude-code",
			Model: "claude-sonnet-4-5", Pin: "2.1.304", Trigger: Trigger{Kind: "interactive", SessionID: "s1"},
			MaxOutputTokens: 1000, PromptHash: "ab", AccountTypes: []string{"team"}, ProfileHash: "cd", ProfileVersion: "p0.f0.u0.t0",
			IssuedAt: now.Unix(), ExpiresAt: now.Add(2 * time.Minute).Unix()}
	}
	with := func(f func(*Claims)) string { c := good(); f(&c); return Sign(priv, c) }
	header := func(h string) string {
		pb, _ := json.Marshal(good())
		s := base64.RawURLEncoding.EncodeToString([]byte(h)) + "." + base64.RawURLEncoding.EncodeToString(pb)
		return s + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, []byte(s)))
	}
	tampered := func() string {
		tok := Sign(priv, good())
		p := strings.Split(tok, ".")
		c := good()
		c.UserID = "someone-else"
		pb, _ := json.Marshal(c)
		return p[0] + "." + base64.RawURLEncoding.EncodeToString(pb) + "." + p[2]
	}

	cases := []struct {
		name  string
		token string
		want  error
	}{
		{"valid", Sign(priv, good()), nil},
		{"wrong key", Sign(otherPriv, good()), ReasonBadSignature},
		{"tampered claims", tampered(), ReasonBadSignature},
		{"alg none", header(`{"alg":"none"}`), ReasonBadHeader},
		{"alg HS256", header(`{"alg":"HS256"}`), ReasonBadHeader},
		{"crit header", header(`{"alg":"EdDSA","crit":["x"]}`), ReasonBadHeader},
		{"jwk header", header(`{"alg":"EdDSA","jwk":{}}`), ReasonBadHeader},
		{"expired", with(func(c *Claims) { c.IssuedAt -= 300; c.ExpiresAt -= 300 }), ReasonExpired},
		{"issued in the future", with(func(c *Claims) { c.IssuedAt += 300; c.ExpiresAt += 300 }), ReasonNotYetValid},
		{"lifetime over 2 min", with(func(c *Claims) { c.ExpiresAt = c.IssuedAt + 600 }), ReasonLifetime},
		{"no account types", with(func(c *Claims) { c.AccountTypes = nil; c.TicketID = "t2" }), ReasonMissingClaims},
		{"no profile hash", with(func(c *Claims) { c.ProfileHash = ""; c.TicketID = "t4" }), ReasonMissingClaims},
		{"no profile version", with(func(c *Claims) { c.ProfileVersion = ""; c.TicketID = "t5" }), ReasonMissingClaims},
		{"garbage", "a.b", ReasonMalformed},
	}
	v := NewVerifier([]ed25519.PublicKey{pub}, func() time.Time { return now })
	for _, tc := range cases {
		_, err := v.Verify(tc.token)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, err, tc.want)
		}
	}
	// The valid ticket above was used once; using it again is a replay.
	if _, err := v.Verify(Sign(priv, good())); !errors.Is(err, ReasonReplayed) {
		t.Errorf("replay: got %v", err)
	}
	// Next key in the pin set also verifies.
	nextPub, nextPriv, _ := ed25519.GenerateKey(rand.Reader)
	v2 := NewVerifier([]ed25519.PublicKey{pub, nextPub}, func() time.Time { return now })
	if _, err := v2.Verify(Sign(nextPriv, good())); err != nil {
		t.Errorf("next key: %v", err)
	}
	// A ticket issued before this Verifier (the process) started is refused, even if unused and unexpired: the
	// ids a previous process saw are gone after a restart.
	v3 := NewVerifier([]ed25519.PublicKey{pub}, func() time.Time { return now })
	early := with(func(c *Claims) { c.TicketID = "t3"; c.IssuedAt -= 1; c.ExpiresAt -= 1 })
	if _, err := v3.Verify(early); !errors.Is(err, ReasonPredatesStart) {
		t.Errorf("issued before start: got %v", err)
	}
	// No pinned keys: refuse everything.
	if _, err := NewVerifier(nil, nil).Verify(Sign(priv, good())); !errors.Is(err, ReasonNoKeys) {
		t.Errorf("no keys: got %v", err)
	}
}

// TestUsedTicketStaysUsedAfterRestart: a ticket issued ahead of this computer's clock (within ClockSkew), used, and
// presented again to a Link restarted a few seconds later is a replay, not a fresh ticket.
func TestUsedTicketStaysUsedAfterRestart(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	keys := []ed25519.PublicKey{pub}
	now := time.Unix(1_800_000_000, 0)
	clock := func() time.Time { return now }
	tok := func(id string, iat time.Time) string {
		return Sign(priv, Claims{TicketID: id, DeviceID: "d1", UserID: "u1", FirmID: "f1", Adapter: "helper-claude-code",
			Model: "m", Pin: "2.1.304", Trigger: Trigger{Kind: "interactive"}, MaxOutputTokens: 1, PromptHash: "ab",
			AccountTypes: []string{"team"}, ProfileHash: "cd", ProfileVersion: "p0", IssuedAt: iat.Unix(),
			ExpiresAt: iat.Add(MaxLifetime).Unix()})
	}
	path := filepath.Join(t.TempDir(), "used-tickets.json")
	ahead := tok("t1", now.Add(20*time.Second))

	before := NewVerifier(keys, clock)
	before.Remember(path)
	if _, err := before.Verify(ahead); err != nil {
		t.Fatal(err)
	}
	now = now.Add(5 * time.Second) // the Link restarts
	after := NewVerifier(keys, clock)
	after.Remember(path)
	if _, err := after.Verify(ahead); !errors.Is(err, ReasonReplayed) {
		t.Fatalf("replay after restart: got %v", err)
	}
	if _, err := after.Verify(tok("t2", now)); err != nil {
		t.Fatalf("a new ticket after restart: %v", err)
	}

	// The record can't be read: any ticket the previous process could have used is refused, later ones run.
	os.WriteFile(path, []byte("{not json"), 0o600)
	unsure := NewVerifier(keys, clock)
	unsure.Remember(path)
	if _, err := unsure.Verify(tok("t3", now.Add(ClockSkew-time.Second))); !errors.Is(err, ReasonPredatesStart) {
		t.Fatalf("unreadable record, ticket within the skew: got %v", err)
	}
	now = now.Add(ClockSkew)
	if _, err := unsure.Verify(tok("t4", now)); err != nil {
		t.Fatalf("unreadable record, ticket after the skew: %v", err)
	}

	// A use that can't be recorded doesn't run.
	nowhere := NewVerifier(keys, clock)
	nowhere.Remember(filepath.Join(t.TempDir(), "missing", "used-tickets.json"))
	if _, err := nowhere.Verify(tok("t5", now)); !errors.Is(err, ReasonUnrecorded) {
		t.Fatalf("unrecorded use: got %v", err)
	}
}
