package ticket

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
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
			MaxOutputTokens: 1000, PromptHash: "ab", AccountTypes: []string{"team"},
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
	// No pinned keys: refuse everything.
	if _, err := NewVerifier(nil, nil).Verify(Sign(priv, good())); !errors.Is(err, ReasonNoKeys) {
		t.Errorf("no keys: got %v", err)
	}
}
