// Package fakerelay is a local stand-in for the Firmary relay, for tests and development. It listens on loopback
// only, verifies the Link's challenge signature, pairs by a code it issued (pairing v2: Issue, then pairing_claimed)
// or one it computes from the nonces (v1), offers a confirmed binding and pairs only when the Link accepts it, mints
// turn tickets with a throwaway key, and records what the Link sends.
package fakerelay

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Firmary/bridge/internal/devicekey"
	"github.com/Firmary/bridge/internal/pairing"
	"github.com/Firmary/bridge/internal/protocol"
	"github.com/Firmary/bridge/internal/testkit"
	"github.com/Firmary/bridge/internal/ticket"
)

// Device is a device as the relay knows it: bound once its code is confirmed, paired once the Link accepts.
type Device struct {
	DeviceID, UserID, FirmID, UserName, FirmName string
	Accepted                                     bool
}

// The display names and email the fake relay sends in pairing_offer and paired.
const (
	UserName  = "Synthetic Test Person"
	UserEmail = "synthetic.person@example.test"
	FirmName  = "Synthetic Test Firm"
)

// Relay is a running fake relay.
type Relay struct {
	URL       string
	TicketKey ed25519.PrivateKey
	TicketPub ed25519.PublicKey
	// AutoConfirm pairs a pending device at once, as if the person confirmed the code.
	AutoConfirm bool

	Codes       chan string // the pending code of each pairing: issued (v2) or computed (v1)
	Reveals     chan protocol.PairingReveal
	Answers     chan string // pairing_accept or pairing_decline, as the Link answered an offer
	Hellos      chan protocol.Hello
	Events      chan protocol.EventMsg
	SigninSteps chan protocol.SigninStep

	srv *http.Server
	ln  net.Listener
	up  websocket.Upgrader

	mu       sync.Mutex
	devices  map[string]Device // base64url public key -> device
	pending  map[string]*pend  // pending code -> the connection waiting for it to be confirmed
	issued   map[string]issued // pairing v2: codes shown in "Firmary", not yet claimed
	conn     *websocket.Conn
	wmu      sync.Mutex
	current  *Device
	Connects int
}

// pend is a connection waiting for its code to be confirmed, and the names the binding is offered in.
type pend struct {
	ch           chan Device
	person, firm string
}

// issued is a code Firmary showed a person (pairing v2).
type issued struct {
	person, firm string
	expires      time.Time
}

// CodeTTL is how long an issued code lives.
const CodeTTL = 10 * time.Minute

// Issue makes a pairing code for a person and firm, as Firmary's "Show a pairing code" does (pairing v2).
func (r *Relay) Issue(personName, firmName string) string {
	b := make([]byte, 5)
	rand.Read(b)
	s := base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding).EncodeToString(b)
	code := s[:4] + "-" + s[4:]
	r.mu.Lock()
	r.issued[code] = issued{person: personName, firm: firmName, expires: time.Now().Add(CodeTTL)}
	r.mu.Unlock()
	return code
}

// Expire ends an issued code's life now.
func (r *Relay) Expire(code string) {
	r.mu.Lock()
	if i, ok := r.issued[code]; ok {
		i.expires = time.Now().Add(-time.Second)
		r.issued[code] = i
	}
	r.mu.Unlock()
}

// Start listens on addr, which must be a loopback address such as "127.0.0.1:0".
func Start(addr string) (*Relay, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return nil, errors.New("fake relay listens on loopback only")
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	r := &Relay{
		URL: "ws://" + ln.Addr().String() + "/", TicketKey: priv, TicketPub: pub,
		Codes: make(chan string, 64), Reveals: make(chan protocol.PairingReveal, 64), Answers: make(chan string, 64),
		Hellos: make(chan protocol.Hello, 64), Events: make(chan protocol.EventMsg, 1024), SigninSteps: make(chan protocol.SigninStep, 64),
		ln: ln, devices: map[string]Device{}, pending: map[string]*pend{}, issued: map[string]issued{},
	}
	r.srv = &http.Server{Handler: http.HandlerFunc(r.serve), ReadHeaderTimeout: 5 * time.Second}
	go r.srv.Serve(ln)
	return r, nil
}

// Close stops the relay.
func (r *Relay) Close() { r.srv.Close() }

// newID mints a random (version 4) uuid, the form the relay schemas require for ids.
func newID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

func (r *Relay) send(v any) error {
	r.mu.Lock()
	c := r.conn
	r.mu.Unlock()
	if c == nil {
		return errors.New("no helper connected")
	}
	r.wmu.Lock()
	defer r.wmu.Unlock()
	return c.WriteJSON(v)
}

func (r *Relay) serve(w http.ResponseWriter, req *http.Request) {
	c, err := r.up.Upgrade(w, req, nil)
	if err != nil {
		return
	}
	defer c.Close()
	nonce := make([]byte, 32)
	rand.Read(nonce)
	if c.WriteJSON(protocol.Challenge{Type: protocol.TypeChallenge, Nonce: base64.RawURLEncoding.EncodeToString(nonce)}) != nil {
		return
	}
	var auth protocol.Auth
	if c.ReadJSON(&auth) != nil || auth.Type != protocol.TypeAuth {
		return
	}
	pub, err1 := base64.RawURLEncoding.DecodeString(auth.PublicKey)
	sig, err2 := base64.RawURLEncoding.DecodeString(auth.Signature)
	commit, err3 := base64.RawURLEncoding.DecodeString(auth.PairingCommit)
	if err1 != nil || err2 != nil || err3 != nil || len(pub) != ed25519.PublicKeySize || len(commit) != sha256.Size ||
		!ed25519.Verify(pub, devicekey.AuthPayload(r.URL, nonce), sig) {
		c.WriteJSON(protocol.RelayError{Type: protocol.TypeError, Code: "bad_auth"})
		return
	}

	// One reader for the rest of the connection.
	msgs := make(chan []byte, 64)
	go func() {
		defer close(msgs)
		for {
			_, b, err := c.ReadMessage()
			if err != nil {
				return
			}
			msgs <- b
		}
	}()

	r.mu.Lock()
	r.Connects++
	dev, known := r.devices[auth.PublicKey]
	r.mu.Unlock()
	if !known {
		// Server side of commit-then-reveal: Nr only after the commitment (in auth), then the reveal must match it.
		relayNonce := make([]byte, pairing.NonceSize)
		rand.Read(relayNonce)
		if c.WriteJSON(protocol.PairingPending{Type: protocol.TypePairingPending,
			PairingNonce: base64.RawURLEncoding.EncodeToString(relayNonce)}) != nil {
			return
		}
		b, ok := <-msgs
		if !ok {
			return
		}
		var reveal protocol.PairingReveal
		json.Unmarshal(b, &reveal)
		helperNonce, err := base64.RawURLEncoding.DecodeString(reveal.Nonce)
		if reveal.Type != protocol.TypePairingReveal || err != nil || len(helperNonce) != pairing.NonceSize ||
			!bytes.Equal(pairing.Commit(pub, helperNonce), commit) {
			c.WriteJSON(protocol.RelayError{Type: protocol.TypeError, Code: "bad_pairing_reveal"})
			return
		}
		r.Reveals <- reveal
		p := &pend{ch: make(chan Device, 1), person: UserName, firm: FirmName}
		code := pairing.Code(pub, relayNonce, helperNonce)
		if reveal.Code != "" {
			// Pairing v2: the code must be one Firmary issued, live, and it is used up by this claim.
			code = reveal.Code
			r.mu.Lock()
			is, ok := r.issued[code]
			delete(r.issued, code)
			r.mu.Unlock()
			switch {
			case !ok:
				c.WriteJSON(protocol.RelayError{Type: protocol.TypeError, Code: "pairing_code_not_found"})
				return
			case time.Now().After(is.expires):
				c.WriteJSON(protocol.RelayError{Type: protocol.TypeError, Code: "pairing_code_expired"})
				return
			}
			p.person, p.firm = is.person, is.firm
			if c.WriteJSON(protocol.PairingClaimed{Type: protocol.TypePairingClaimed, FirmName: is.firm, PersonName: is.person}) != nil {
				return
			}
		}
		confirmed := p.ch
		r.mu.Lock()
		r.pending[code] = p
		r.mu.Unlock()
		// The pending code lives only as long as this connection.
		defer func() {
			r.mu.Lock()
			delete(r.pending, code)
			r.mu.Unlock()
		}()
		r.Codes <- code
		if r.AutoConfirm {
			r.Confirm(code)
		}
	wait:
		for {
			select {
			case dev = <-confirmed:
				break wait
			case _, ok := <-msgs:
				if !ok {
					return
				}
			}
		}
		r.mu.Lock()
		r.devices[auth.PublicKey] = dev
		r.mu.Unlock()
	}
	if dev.Accepted {
		if c.WriteJSON(protocol.Welcome{Type: protocol.TypeWelcome, DeviceID: dev.DeviceID, UserID: dev.UserID, FirmID: dev.FirmID}) != nil {
			return
		}
	} else {
		// The accept step: a bound key that this computer hasn't accepted is offered on every connection, and pairs
		// only on pairing_accept. A decline revokes the binding. No answer: the offer waits.
		if c.WriteJSON(protocol.PairingOffer{Type: protocol.TypePairingOffer, DeviceID: dev.DeviceID, UserID: dev.UserID,
			FirmID: dev.FirmID, FirmName: dev.FirmName, PersonName: dev.UserName, PersonEmail: UserEmail}) != nil {
			return
		}
		b, ok := <-msgs
		if !ok {
			return
		}
		var answer protocol.PairingAnswer
		json.Unmarshal(b, &answer)
		if answer.DeviceID != dev.DeviceID {
			c.WriteJSON(protocol.RelayError{Type: protocol.TypeError, Code: "malformed_accept"})
			return
		}
		r.Answers <- answer.Type
		switch answer.Type {
		case protocol.TypePairingAccept:
			dev.Accepted = true
			r.mu.Lock()
			r.devices[auth.PublicKey] = dev
			r.mu.Unlock()
			if c.WriteJSON(protocol.Paired{Type: protocol.TypePaired, DeviceID: dev.DeviceID, UserID: dev.UserID, FirmID: dev.FirmID,
				UserName: dev.UserName, FirmName: dev.FirmName}) != nil {
				return
			}
		case protocol.TypePairingDecline:
			r.mu.Lock()
			delete(r.devices, auth.PublicKey)
			r.mu.Unlock()
			c.WriteJSON(protocol.RelayError{Type: protocol.TypeError, Code: "pairing_declined"})
			return
		default:
			c.WriteJSON(protocol.RelayError{Type: protocol.TypeError, Code: "unexpected_message"})
			return
		}
	}

	r.mu.Lock()
	r.conn, r.current = c, &dev
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		if r.conn == c {
			r.conn, r.current = nil, nil
		}
		r.mu.Unlock()
	}()
	for b := range msgs {
		var env protocol.Envelope
		json.Unmarshal(b, &env)
		switch env.Type {
		case protocol.TypeHello:
			var h protocol.Hello
			json.Unmarshal(b, &h)
			r.Hellos <- h
		case protocol.TypeEvent:
			var e protocol.EventMsg
			json.Unmarshal(b, &e)
			r.Events <- e
		case protocol.TypeSigninStep:
			var st protocol.SigninStep
			json.Unmarshal(b, &st)
			r.SigninSteps <- st
		}
	}
}

// Confirm confirms a pending pairing by its code, as the person would in Firmary. The Link is then offered the
// binding, naming the person and firm the code was issued for (v2) or the fake's own (v1).
func (r *Relay) Confirm(code string) bool {
	r.mu.Lock()
	p, ok := r.pending[code]
	r.mu.Unlock()
	return ok && r.ConfirmAs(code, p.person, p.firm)
}

// ConfirmAs confirms a pending pairing but offers the binding in other names, as a relay that binds the computer to
// someone other than who asked for the code would.
func (r *Relay) ConfirmAs(code, personName, firmName string) bool {
	r.mu.Lock()
	p, ok := r.pending[code]
	delete(r.pending, code)
	r.mu.Unlock()
	if ok {
		p.ch <- Device{DeviceID: newID(), UserID: newID(), FirmID: newID(), UserName: personName, FirmName: firmName}
	}
	return ok
}

// Pending reports whether a connection still waits for this code to be confirmed.
func (r *Relay) Pending(code string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.pending[code]
	return ok
}

// Current returns the device on the live connection.
func (r *Relay) Current() (Device, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current == nil {
		return Device{}, false
	}
	return *r.current, true
}

// WaitConnected waits until a paired Link is connected.
func (r *Relay) WaitConnected(ctx context.Context) (Device, error) {
	for {
		if d, ok := r.Current(); ok {
			return d, nil
		}
		select {
		case <-ctx.Done():
			return Device{}, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// The built-in launch profile (version 0): the Link's embedded prompt, no tools. Claims binds it and SendTurn sends
// it.
const (
	BuiltInProfile        = `{"schema":1}`
	BuiltInProfileVersion = "p0.f0.u0.t0"
)

// Claims returns valid claims for a turn on the connected device. They allow business plans, so, as D7 requires,
// they bind the turn to the firm's provider organisation: the one the fake claude reports.
func (r *Relay) Claims(d Device, pin, context, prompt string) ticket.Claims {
	now := time.Now()
	return ticket.Claims{
		TicketID: newID(), DeviceID: d.DeviceID, UserID: d.UserID, FirmID: d.FirmID,
		ConversationID: newID(), Adapter: "helper-claude-code", Model: "claude-sonnet-4-5", Pin: pin,
		Trigger: ticket.Trigger{Kind: "interactive", SessionID: newID()}, MaxOutputTokens: 2000,
		PromptHash: protocol.PromptHash(context, prompt, nil), AccountTypes: []string{"pro", "max", "team", "enterprise"},
		ProviderOrgIDs: []string{testkit.OrgID}, ProfileHash: protocol.ProfileHash(BuiltInProfile),
		ProfileVersion: BuiltInProfileVersion, IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Minute).Unix(),
	}
}

// SendTurn signs c and sends the turn with the built-in launch profile.
func (r *Relay) SendTurn(c ticket.Claims, context, prompt string) error {
	return r.SendTurnProfile(c, context, prompt, BuiltInProfile)
}

// SendTurnProfile signs c and sends the turn with profile as its launch profile text. c.ProfileHash is sent as set.
func (r *Relay) SendTurnProfile(c ticket.Claims, context, prompt, profile string) error {
	return r.send(protocol.Turn{Type: protocol.TypeTurn, TurnID: c.TicketID, Ticket: ticket.Sign(r.TicketKey, c), Context: context,
		Prompt: prompt, Profile: profile})
}

// SendRaw sends any message.
func (r *Relay) SendRaw(v any) error { return r.send(v) }

// Drop closes the live connection, as a relay restart would.
func (r *Relay) Drop() {
	r.mu.Lock()
	c := r.conn
	r.mu.Unlock()
	if c != nil {
		c.Close()
	}
}

// Forget removes every paired device, as an unpair while the Link was offline would.
func (r *Relay) Forget() {
	r.mu.Lock()
	r.devices = map[string]Device{}
	r.mu.Unlock()
}

// ConnectCount is how many authenticated connections the relay has seen.
func (r *Relay) ConnectCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Connects
}
