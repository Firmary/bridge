// Package protocol holds the relay messages and stream events the Link exchanges with the Firmary relay.
//
// These mirror the JSON Schemas that Firmary publishes for the Link (slice S1). Until those schemas are generated
// into this repo the types are written by hand here; field names follow the relay protocol (build plan §4.3).
package protocol

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
)

// MachineNameRe is the protocol's machine-name pattern (limit windows, account types, relay error codes).
var MachineNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)

// BusinessAccountTypes are, per provider app, the plan types of a provider organisation's business plan. D7 (#1888):
// business-only means the firm's own provider organisation, so a login on one of these plans runs a turn only when
// the ticket's providerOrgIds lists its org id. They match firmary-os BUSINESS_ACCOUNT_TYPES
// (apps/web/modules/ai-relay/server/devices.ts): Codex reports its ChatGPT plan type, and a ChatGPT Business
// workspace is "team" (from before the rename) or "business".
var BusinessAccountTypes = map[string][]string{
	"claude-code": {"team", "enterprise"},
	"codex":       {"team", "business", "enterprise"},
}

// UpdateFailure is a self-test failure that rolled an update back since the last hello (build plan §5.3).
type UpdateFailure struct {
	Version string `json:"version"`
	Pattern string `json:"pattern"`
}

// Message types.
const (
	// Relay -> Helper.
	TypeChallenge      = "challenge"
	TypePairingPending = "pairing_pending"
	TypePairingClaimed = "pairing_claimed"
	TypePairingOffer   = "pairing_offer"
	TypePaired         = "paired"
	TypeWelcome        = "welcome"
	TypeTurn           = "turn"
	TypeCancel         = "cancel"
	TypeRevoke         = "revoke"
	TypeError          = "error"
	TypeSignin         = "signin"
	TypeSigninCode     = "signin_code"
	// Helper -> relay.
	TypeAuth           = "auth"
	TypePairingReveal  = "pairing_reveal"
	TypePairingAccept  = "pairing_accept"
	TypePairingDecline = "pairing_decline"
	TypeHello          = "hello"
	TypeEvent          = "event"
	TypeSigninStep     = "signin_step"
	TypeSigninResult   = "signin_result"
)

// Device kinds (pairing_reveal device.kind and hello kind): the app on a person's computer, or the always-on
// headless service on a VPS or home server.
const (
	KindDesktop = "desktop"
	KindServer  = "server"
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

// Auth answers a challenge: the device public key and its signature over devicekey.AuthPayload. PairingCommit
// (base64url, 32 bytes) is pairing.Commit over this connection's fresh helper nonce; it is sent on every connection
// and used only if the relay answers pairing_pending.
type Auth struct {
	Type          string `json:"type"`
	PublicKey     string `json:"publicKey"`
	Signature     string `json:"signature"`
	HelperVersion string `json:"helperVersion"`
	PairingCommit string `json:"pairingCommit"`
}

// PairingPending: the relay doesn't know this key yet. PairingNonce is the relay's nonce Nr (base64url, exactly 32
// bytes), sent only after the commitment. The Link answers with PairingReveal, shows its code and waits for
// Paired.
type PairingPending struct {
	Type         string `json:"type"`
	PairingNonce string `json:"pairingNonce"`
}

// PairingReveal reveals the helper nonce Nh (base64url, 32 bytes) committed to in Auth.
type PairingReveal struct {
	Type  string `json:"type"`
	Nonce string `json:"nonce"`
	// Device is the computer's own claim about itself, shown to the person before they confirm (optional).
	Device *PairingDevice `json:"device,omitempty"`
	// Code is pairing v2: the code Firmary showed the person, as typed at this computer and normalised
	// (pairing.NormalizeCode). Empty is the v1 exchange, where both sides derive the code from the nonces.
	Code string `json:"code,omitempty"`
}

// PairingDevice is pairing_reveal's device: the computer's name (1 to 64 characters), os, arch and kind (KindDesktop
// or KindServer).
type PairingDevice struct {
	Name string `json:"name,omitempty"`
	OS   string `json:"os,omitempty"`
	Arch string `json:"arch,omitempty"`
	Kind string `json:"kind,omitempty"`
}

// PairingClaimed (pairing v2): the code this Link sent is live, and this is who asked Firmary for it. The Link shows
// "Connecting to Firmary as <person> · <firm>" with the code and Cancel. The names are for the person's eyes only:
// never logged or stored.
type PairingClaimed struct {
	Type       string `json:"type"`
	FirmName   string `json:"firmName"`
	PersonName string `json:"personName"`
}

// PairingOffer: a person confirmed this key in Firmary (or confirmed it earlier and this computer never answered).
// The Link shows the person and firm and sends PairingAnswer: pairing_accept only after an explicit yes on this
// computer, pairing_decline on anything else. Until it accepts, the relay sends no welcome, tickets or turns, and it
// offers the binding again on every connection. The names and email are for the person's eyes only: never logged
// or stored.
type PairingOffer struct {
	Type        string `json:"type"`
	DeviceID    string `json:"deviceId"`
	UserID      string `json:"userId"`
	FirmID      string `json:"firmId"`
	FirmName    string `json:"firmName"`
	PersonName  string `json:"personName"`
	PersonEmail string `json:"personEmail"`
}

// PairingAnswer is pairing_accept or pairing_decline (Type) for the offer's DeviceID.
type PairingAnswer struct {
	Type     string `json:"type"`
	DeviceID string `json:"deviceId"`
}

// Paired: this computer accepted the PairingOffer, binding this key to the offered person. It never comes without
// the Link's pairing_accept on the same connection, and its ids must be the offer's. UserName is the person's display name
// and FirmName the firm's; the Link shows both ("Paired to <person> · <firm>") so pairing to the wrong account is
// noticed, and never logs or stores them.
type Paired struct {
	Type     string `json:"type"`
	DeviceID string `json:"deviceId"`
	UserID   string `json:"userId"`
	FirmID   string `json:"firmId"`
	UserName string `json:"userName"`
	FirmName string `json:"firmName"`
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
	PinnedVersion string `json:"pinnedVersion,omitempty"` // empty before the provider is installed
	SignedIn      bool   `json:"signedIn"`
	AccountType   string `json:"accountType,omitempty"`
	// The provider organisation's display name is deliberately never sent (consent, #1888); OrgID is all D7 needs.
	OrgID string `json:"orgId,omitempty"`
	// AccountEmail is the signed-in account's email as the provider reports it, for the paired person's own
	// Connections page. Never logged; omitted when unknown.
	AccountEmail string `json:"accountEmail,omitempty"`
	// Managed: machine-wide managed settings for the provider were found, so IT policy (hooks, env, gateways)
	// applies to turns. False means none were found by the Link's checks.
	Managed bool `json:"managed"`
}

// Hello is sent after authentication on every connection.
type Hello struct {
	Type                    string         `json:"type"`
	HelperVersion           string         `json:"helperVersion"`
	OS                      string         `json:"os"`
	Arch                    string         `json:"arch"`
	Ring                    string         `json:"ring"`
	Kind                    string         `json:"kind,omitempty"` // KindDesktop or KindServer
	Providers               []ProviderInfo `json:"providers"`
	AcceptedConsentRevision int            `json:"acceptedConsentRevision"`
	// Launch is what launch profiles this build can carry out (internal/launch). Every Link sends it.
	Launch *LaunchCapabilities `json:"launch,omitempty"`
	// ReconnectReason says why this connection opened: start, network, update, crash or server_closed.
	ReconnectReason string `json:"reconnectReason,omitempty"`
	// UpdateFailure is an update the Link rolled back since the last hello.
	UpdateFailure *UpdateFailure `json:"updateFailure,omitempty"`
}

// LaunchCapabilities lists the launch profile schemas, catalog tools and provider knobs (field names under the
// profile's claudeCode and codex objects) this Link build maps. The server never sends anything else.
type LaunchCapabilities struct {
	ProfileSchemas []int    `json:"profileSchemas"`
	Tools          []string `json:"tools"`
	ClaudeKnobs    []string `json:"claudeKnobs"`
	CodexKnobs     []string `json:"codexKnobs"`
}

// Turn asks the Link to run one turn. TurnID must equal the ticket's ticketId. Schema is the structured-output
// JSON Schema sent as a JSON string; nil means absent. Profile is the turn's launch profile as JSON text
// (internal/launch), bound by the ticket's profileHash; the Link refuses a turn without one. Firmary is the optional
// {url, accessToken} credential for Firmary's own MCP server, sent only when the profile turns the firmary tool on;
// it is kept raw here so a malformed one refuses the turn (launch.TurnFirmary), not the connection. Never logged.
type Turn struct {
	Type    string          `json:"type"`
	TurnID  string          `json:"turnId"`
	Ticket  string          `json:"ticket"`
	Context string          `json:"context"`
	Prompt  string          `json:"prompt"`
	Schema  *string         `json:"schema,omitempty"`
	Profile string          `json:"profile,omitempty"`
	Firmary json.RawMessage `json:"firmary,omitempty"`
}

// PromptHash hashes the turn's decoded context, prompt and schema strings (not their raw JSON tokens).
func (t Turn) PromptHash() string {
	var schema []byte
	if t.Schema != nil {
		schema = []byte(*t.Schema)
	}
	return PromptHash(t.Context, t.Prompt, schema)
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

// Signin asks the Link to start a provider's own sign-in in the paired person's profile. The Link answers with
// SigninStep once the provider's link is known.
type Signin struct {
	Type     string `json:"type"`
	Provider string `json:"provider"`
}

// SigninStep is the provider's own sign-in link and, for a device link, its one-time code, which Firmary shows the
// person. Never logged. PasteCode says the provider's page may end on a code for the person to paste into Firmary,
// which comes back as SigninCode (Claude Code: its link always ends on that code; on a desktop it also opened its own
// browser sign-in, which needs no code). Kind "paste_code" (a server) has no browser here at all.
type SigninStep struct {
	Type      string `json:"type"`
	Provider  string `json:"provider"`
	Kind      string `json:"kind"` // "browser", "device_link" or "paste_code"
	URL       string `json:"url,omitempty"`
	UserCode  string `json:"userCode,omitempty"`
	PasteCode bool   `json:"pasteCode,omitempty"`
}

// SigninCode is the code the provider's page showed the person, pasted into Firmary, for the sign-in that is running.
// Never logged.
type SigninCode struct {
	Type     string `json:"type"`
	Provider string `json:"provider"`
	Code     string `json:"code"`
}

// SigninResult says how the provider sign-in ended, sent when it ends. A pasted code that isn't one (Pattern
// "signin_code_invalid") is answered at once while the sign-in keeps waiting for a good one.
type SigninResult struct {
	Type     string `json:"type"`
	Provider string `json:"provider"`
	OK       bool   `json:"ok"`
	Pattern  string `json:"pattern,omitempty"`
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

// LimitWindow is one provider limit window, only as the provider reports it. Window is a machine name; UsedPercent
// is 0-100.
type LimitWindow struct {
	Window        string   `json:"window"`
	UsedPercent   *float64 `json:"usedPercent,omitempty"`
	WindowMinutes int64    `json:"windowMinutes,omitempty"`
	ResetsAt      string   `json:"resetsAt,omitempty"`
}

// MaxLimitWindows is the most windows one usage event carries.
const MaxLimitWindows = 16

// TokenCounts are token counts as the provider reports them.
type TokenCounts struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cacheRead,omitempty"`
	CacheWrite int64 `json:"cacheWrite,omitempty"`
}

// StreamEvent is the tagged union from build plan §4.2. Use the constructors so each kind carries only its fields.
// An error carries a class and a pattern name, never raw provider text.
type StreamEvent struct {
	T                 string        `json:"t"`
	Model             string        `json:"model,omitempty"`
	Text              string        `json:"text,omitempty"`
	Windows           []LimitWindow `json:"windows,omitzero"` // usage always has it, possibly []
	Tokens            *TokenCounts  `json:"tokens,omitempty"`
	ResetsAt          string        `json:"resetsAt,omitempty"`
	Class             string        `json:"class,omitempty"`
	Pattern           string        `json:"pattern,omitempty"`
	ProviderSessionID string        `json:"providerSessionId,omitempty"`
}

func Start(model string) StreamEvent    { return StreamEvent{T: EvStart, Model: model} }
func TextDelta(text string) StreamEvent { return StreamEvent{T: EvTextDelta, Text: text} }
func Usage(ws []LimitWindow, tok *TokenCounts) StreamEvent {
	if ws == nil {
		ws = []LimitWindow{} // the contract requires windows on a usage event
	}
	return StreamEvent{T: EvUsage, Windows: ws, Tokens: tok}
}
func Limit(resetsAt string) StreamEvent { return StreamEvent{T: EvLimit, ResetsAt: resetsAt} }
func Error(class, pattern string) StreamEvent {
	return StreamEvent{T: EvError, Class: class, Pattern: pattern}
}
func Done(sessionID string, tok *TokenCounts) StreamEvent {
	return StreamEvent{T: EvDone, ProviderSessionID: sessionID, Tokens: tok}
}

// Domain separation for the prompt and profile hashes.
const (
	promptHashContext  = "firmary-turn-v1"
	profileHashContext = "firmary-launch-profile-v1"
)

// PromptHash is the hash a ticket's promptHash claim must equal: lowercase hex SHA-256 over the context string,
// then each of context, prompt and schema as an 8-byte big-endian length followed by its bytes. A missing schema
// hashes as zero length.
func PromptHash(context, prompt string, schema []byte) string {
	return lengthPrefixedHash(promptHashContext, []byte(context), []byte(prompt), schema)
}

// ProfileHash is the hash a ticket's profileHash claim must equal: the same recipe as PromptHash with its own context
// string and one part, the turn's decoded profile string exactly as received (never re-serialised).
func ProfileHash(profile string) string {
	return lengthPrefixedHash(profileHashContext, []byte(profile))
}

// lengthPrefixedHash is lowercase hex SHA-256 over context, then each part as an 8-byte big-endian length and its
// bytes.
func lengthPrefixedHash(context string, parts ...[]byte) string {
	h := sha256.New()
	h.Write([]byte(context))
	var n [8]byte
	for _, part := range parts {
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
