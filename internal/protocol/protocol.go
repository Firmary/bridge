// Package protocol holds the relay messages and stream events the Helper exchanges with the Firmary relay.
//
// These mirror the JSON Schemas that Firmary publishes for the Helper (slice S1). Until those schemas are generated
// into this repo the types are written by hand here; field names follow the relay protocol (build plan §4.3).
package protocol

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
)

// Message types.
const (
	// Relay -> Helper.
	TypeChallenge      = "challenge"
	TypePairingPending = "pairing_pending"
	TypePaired         = "paired"
	TypeWelcome        = "welcome"
	TypeTurn           = "turn"
	TypeCancel         = "cancel"
	TypeRevoke         = "revoke"
	TypeError          = "error"
	// Helper -> relay.
	TypeAuth  = "auth"
	TypeHello = "hello"
	TypeEvent = "event"
)

// Envelope is used to read a message's type before decoding the rest.
type Envelope struct {
	Type string `json:"type"`
}

// Challenge is the relay's first message. Nonce is base64url, at least 32 bytes.
type Challenge struct {
	Type  string `json:"type"`
	Nonce string `json:"nonce"`
}

// Auth answers a challenge: the device public key and its signature over devicekey.AuthPayload.
type Auth struct {
	Type          string `json:"type"`
	PublicKey     string `json:"publicKey"`
	Signature     string `json:"signature"`
	HelperVersion string `json:"helperVersion"`
}

// PairingPending: the relay doesn't know this key yet. The Helper shows its pairing code and waits for Paired.
type PairingPending struct {
	Type string `json:"type"`
}

// Paired: the person confirmed the code in Firmary, binding this key to them.
type Paired struct {
	Type     string `json:"type"`
	DeviceID string `json:"deviceId"`
	UserID   string `json:"userId"`
	FirmID   string `json:"firmId"`
}

// Welcome: the relay recognised an already-paired key.
type Welcome struct {
	Type     string `json:"type"`
	DeviceID string `json:"deviceId"`
	UserID   string `json:"userId"`
	FirmID   string `json:"firmId"`
}

// ProviderInfo is one provider's state in Hello.
type ProviderInfo struct {
	ID            string `json:"id"`
	PinnedVersion string `json:"pinnedVersion"`
	SignedIn      bool   `json:"signedIn"`
	AccountType   string `json:"accountType,omitempty"`
	Org           string `json:"org,omitempty"`
	OrgID         string `json:"orgId,omitempty"`
	// Managed: machine-wide managed settings for the provider were found, so IT policy (hooks, env, gateways)
	// applies to turns. False means none were found by the Helper's checks.
	Managed bool `json:"managed"`
}

// Hello is sent after authentication on every connection.
type Hello struct {
	Type                    string         `json:"type"`
	HelperVersion           string         `json:"helperVersion"`
	OS                      string         `json:"os"`
	Arch                    string         `json:"arch"`
	Ring                    string         `json:"ring"`
	Providers               []ProviderInfo `json:"providers"`
	AcceptedConsentRevision int            `json:"acceptedConsentRevision"`
}

// Turn asks the Helper to run one turn. TurnID must equal the ticket's ticketId.
type Turn struct {
	Type    string          `json:"type"`
	TurnID  string          `json:"turnId"`
	Ticket  string          `json:"ticket"`
	Context string          `json:"context"`
	Prompt  string          `json:"prompt"`
	Schema  json.RawMessage `json:"schema,omitempty"`
}

// Cancel stops a running turn.
type Cancel struct {
	Type   string `json:"type"`
	TurnID string `json:"turnId"`
}

// Revoke means the device was unpaired: wipe the pairing.
type Revoke struct {
	Type string `json:"type"`
}

// RelayError is a relay-side refusal, by code.
type RelayError struct {
	Type string `json:"type"`
	Code string `json:"code"`
}

// EventMsg carries one stream event for a turn.
type EventMsg struct {
	Type   string      `json:"type"`
	TurnID string      `json:"turnId"`
	Event  StreamEvent `json:"event"`
}

// Stream event kinds.
const (
	EvStart     = "start"
	EvTextDelta = "text_delta"
	EvUsage     = "usage"
	EvLimit     = "limit"
	EvError     = "error"
	EvDone      = "done"
)

// Error classes.
const (
	ClassAuth      = "auth"
	ClassRateLimit = "rate_limit"
	ClassTransient = "transient"
	ClassFatal     = "fatal"
	ClassPolicy    = "policy"
)

// LimitWindow is one provider limit window, only as the provider reports it.
type LimitWindow struct {
	Name        string   `json:"name"`
	Utilization *float64 `json:"utilization,omitempty"`
	ResetsAt    string   `json:"resetsAt,omitempty"`
}

// TokenCounts are token counts as the provider reports them.
type TokenCounts struct {
	Input         int64 `json:"input"`
	Output        int64 `json:"output"`
	CacheRead     int64 `json:"cacheRead,omitempty"`
	CacheCreation int64 `json:"cacheCreation,omitempty"`
}

// StreamEvent is the tagged union from build plan §4.2. Use the constructors so each kind carries only its fields.
// An error carries a class and a pattern name, never raw provider text.
type StreamEvent struct {
	T                 string        `json:"t"`
	Model             string        `json:"model,omitempty"`
	Text              string        `json:"text,omitempty"`
	Windows           []LimitWindow `json:"windows,omitempty"`
	Tokens            *TokenCounts  `json:"tokens,omitempty"`
	ResetsAt          string        `json:"resetsAt,omitempty"`
	Class             string        `json:"class,omitempty"`
	Pattern           string        `json:"pattern,omitempty"`
	ProviderSessionID string        `json:"providerSessionId,omitempty"`
}

func Start(model string) StreamEvent    { return StreamEvent{T: EvStart, Model: model} }
func TextDelta(text string) StreamEvent { return StreamEvent{T: EvTextDelta, Text: text} }
func Usage(ws []LimitWindow, tok *TokenCounts) StreamEvent {
	return StreamEvent{T: EvUsage, Windows: ws, Tokens: tok}
}
func Limit(resetsAt string) StreamEvent { return StreamEvent{T: EvLimit, ResetsAt: resetsAt} }
func Error(class, pattern string) StreamEvent {
	return StreamEvent{T: EvError, Class: class, Pattern: pattern}
}
func Done(sessionID string, tok *TokenCounts) StreamEvent {
	return StreamEvent{T: EvDone, ProviderSessionID: sessionID, Tokens: tok}
}

// promptHashContext domain-separates the prompt hash.
const promptHashContext = "firmary-turn-v1"

// PromptHash is the hash a ticket's promptHash claim must equal: lowercase hex SHA-256 over the context string,
// then each of context, prompt and schema as an 8-byte big-endian length followed by its bytes. A missing schema
// hashes as zero length.
func PromptHash(context, prompt string, schema []byte) string {
	h := sha256.New()
	h.Write([]byte(promptHashContext))
	var n [8]byte
	for _, part := range [][]byte{[]byte(context), []byte(prompt), schema} {
		binary.BigEndian.PutUint64(n[:], uint64(len(part)))
		h.Write(n[:])
		h.Write(part)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Decode reads a message of a known type. Unknown fields are ignored so the relay can add fields first.
func Decode[T any](b []byte) (T, error) {
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		var zero T
		return zero, errors.New("malformed relay message")
	}
	return v, nil
}
