// Package ticket verifies Firmary turn tickets: Ed25519-signed JWTs (alg EdDSA), valid for at most two minutes and
// usable once.
//
// Verify checks only the token itself (signature against pinned keys, header, time window, required claims,
// single use). Whether the ticket is for this device, its paired user and firm, this prompt and this provider
// account is checked by the caller against local state (internal/helper).
package ticket

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Limits.
const (
	MaxLifetime = 2 * time.Minute
	ClockSkew   = 30 * time.Second
	// SeenFor is how long a used ticket id is remembered; longer than MaxLifetime plus skew.
	SeenFor  = 10 * time.Minute
	MaxBytes = 8 << 10
)

// Trigger says why the turn runs (build plan §4.2).
type Trigger struct {
	Kind            string `json:"kind"` // "interactive" | "personal_schedule"
	SessionID       string `json:"sessionId,omitempty"`
	AutomationID    string `json:"automationId,omitempty"`
	ConsentRevision int    `json:"consentRevision,omitempty"`
}

// Claims are the ticket claims (build plan §4.3), plus accountTypes: the provider account types this turn may
// run on (D7: business-only unless the firm allows personal plans), and optional providerOrgIds.
type Claims struct {
	TicketID        string   `json:"ticketId"`
	DeviceID        string   `json:"deviceId"`
	UserID          string   `json:"userId"`
	FirmID          string   `json:"firmId"`
	ConversationID  string   `json:"conversationId"`
	Adapter         string   `json:"adapter"`
	Model           string   `json:"model"`
	Pin             string   `json:"pin"`
	Trigger         Trigger  `json:"trigger"`
	ConsentRevision *int     `json:"consentRevision,omitempty"`
	MaxOutputTokens int      `json:"maxOutputTokens"`
	PromptHash      string   `json:"promptHash"`
	AccountTypes    []string `json:"accountTypes"`
	// ProviderOrgIDs, when present, limits the turn to a provider login in one of these organisations.
	ProviderOrgIDs []string `json:"providerOrgIds,omitzero"`
	// ProfileHash binds the turn message's launch profile (protocol.ProfileHash). ProfileVersion names the layer
	// versions that produced it, for audit only. Both are required.
	ProfileHash    string `json:"profileHash,omitempty"`
	ProfileVersion string `json:"profileVersion,omitempty"`
	IssuedAt       int64  `json:"iat"`
	ExpiresAt      int64  `json:"exp"`
}

// Reason is a closed-set rejection code, safe to log and to send back as an error pattern.
type Reason string

func (r Reason) Error() string { return "ticket rejected: " + string(r) }

const (
	ReasonMalformed     Reason = "ticket_malformed"
	ReasonBadHeader     Reason = "ticket_bad_header"
	ReasonNoKeys        Reason = "ticket_no_pinned_key"
	ReasonBadSignature  Reason = "ticket_bad_signature"
	ReasonExpired       Reason = "ticket_expired"
	ReasonNotYetValid   Reason = "ticket_not_yet_valid"
	ReasonLifetime      Reason = "ticket_lifetime_too_long"
	ReasonMissingClaims Reason = "ticket_missing_claims"
	ReasonReplayed      Reason = "ticket_replayed"
	ReasonPredatesStart Reason = "ticket_predates_start"
	ReasonUnrecorded    Reason = "ticket_unrecorded"
)

// Verifier checks tickets against pinned public keys and remembers used ticket ids.
//
// So that a restart can't make a used ticket valid again, a ticket issued before the second the Verifier was created
// (process start) is refused; the server re-issues it. That alone still lets through a ticket issued up to ClockSkew
// ahead of this computer's clock, used, and presented again after a quick restart, so with Remember the used ids are
// also kept on disk.
type Verifier struct {
	keys    []ed25519.PublicKey
	now     func() time.Time
	started int64 // Unix seconds

	mu   sync.Mutex
	seen map[string]time.Time
	path string // where used ids are kept across restarts, if set
}

// NewVerifier pins keys (current and next). With no keys every ticket is refused.
func NewVerifier(keys []ed25519.PublicKey, now func() time.Time) *Verifier {
	if now == nil {
		now = time.Now
	}
	return &Verifier{keys: keys, now: now, started: now().Unix(), seen: map[string]time.Time{}}
}

// Remember keeps used ticket ids in path as well as in memory, and takes in the ones a previous process left there.
// If path exists but can't be read, every ticket that process could have used (issued before this start plus
// ClockSkew) is refused instead.
func (v *Verifier) Remember(path string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.path = path
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	var used map[string]int64
	if err != nil || json.Unmarshal(b, &used) != nil {
		v.started = v.now().Add(ClockSkew).Unix()
		return
	}
	for id, at := range used {
		v.seen[id] = time.Unix(at, 0)
	}
}

// save replaces v.path with the used ids, atomically and readable only by the person. v.mu is held.
func (v *Verifier) save() error {
	used := make(map[string]int64, len(v.seen))
	for id, t := range v.seen {
		used[id] = t.Unix()
	}
	b, err := json.Marshal(used)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(v.path), ".used-tickets-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), v.path)
}

var b64 = base64.RawURLEncoding

// Verify checks the token and, if it passes, marks its ticket id used.
func (v *Verifier) Verify(token string) (Claims, error) {
	var c Claims
	if len(v.keys) == 0 {
		return c, ReasonNoKeys
	}
	if len(token) > MaxBytes {
		return c, ReasonMalformed
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return c, ReasonMalformed
	}
	hb, err1 := b64.DecodeString(parts[0])
	pb, err2 := b64.DecodeString(parts[1])
	sig, err3 := b64.DecodeString(parts[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return c, ReasonMalformed
	}
	var h map[string]any
	if json.Unmarshal(hb, &h) != nil {
		return c, ReasonMalformed
	}
	// Only {alg: EdDSA, typ?: JWT, kid?: string}. Anything else (crit, jku, jwk, x5u, ...) is refused.
	if h["alg"] != "EdDSA" {
		return c, ReasonBadHeader
	}
	for k, val := range h {
		switch k {
		case "alg":
		case "typ":
			if val != "JWT" {
				return c, ReasonBadHeader
			}
		case "kid":
			if _, ok := val.(string); !ok {
				return c, ReasonBadHeader
			}
		default:
			return c, ReasonBadHeader
		}
	}
	signed := []byte(parts[0] + "." + parts[1])
	ok := false
	for _, k := range v.keys {
		if len(k) == ed25519.PublicKeySize && ed25519.Verify(k, signed, sig) {
			ok = true
			break
		}
	}
	if !ok {
		return c, ReasonBadSignature
	}
	dec := json.NewDecoder(bytes.NewReader(pb))
	if dec.Decode(&c) != nil {
		return Claims{}, ReasonMalformed
	}
	now := v.now()
	iat, exp := time.Unix(c.IssuedAt, 0), time.Unix(c.ExpiresAt, 0)
	switch {
	case c.IssuedAt == 0 || c.ExpiresAt == 0:
		return Claims{}, ReasonMissingClaims
	case exp.Sub(iat) > MaxLifetime || !exp.After(iat):
		return Claims{}, ReasonLifetime
	case iat.After(now.Add(ClockSkew)):
		return Claims{}, ReasonNotYetValid
	case !now.Before(exp.Add(ClockSkew)):
		return Claims{}, ReasonExpired
	case c.IssuedAt < v.started:
		return Claims{}, ReasonPredatesStart
	}
	if c.TicketID == "" || c.DeviceID == "" || c.UserID == "" || c.FirmID == "" || c.Adapter == "" ||
		c.Model == "" || c.Pin == "" || c.PromptHash == "" || c.Trigger.Kind == "" || c.MaxOutputTokens <= 0 ||
		len(c.AccountTypes) == 0 || c.ProfileHash == "" || c.ProfileVersion == "" {
		return Claims{}, ReasonMissingClaims
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	for id, t := range v.seen {
		if now.Sub(t) > SeenFor {
			delete(v.seen, id)
		}
	}
	if _, dup := v.seen[c.TicketID]; dup {
		return Claims{}, ReasonReplayed
	}
	v.seen[c.TicketID] = now
	// A ticket whose use couldn't be recorded isn't run: after a restart it would be usable again.
	if v.path != "" && v.save() != nil {
		return Claims{}, ReasonUnrecorded
	}
	return c, nil
}

// Sign makes a ticket. The Link never signs tickets; this exists for the fake relay and tests.
func Sign(priv ed25519.PrivateKey, c Claims) string {
	hb, _ := json.Marshal(map[string]string{"alg": "EdDSA", "typ": "JWT"})
	pb, _ := json.Marshal(c)
	signed := b64.EncodeToString(hb) + "." + b64.EncodeToString(pb)
	return signed + "." + b64.EncodeToString(ed25519.Sign(priv, []byte(signed)))
}
