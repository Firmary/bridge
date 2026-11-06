// Package relay is the Link's one outbound connection to the Firmary relay: challenge-response authentication
// with the device key, pairing (v2: the code Firmary shows, typed at this computer; v1: code match) and the accept
// step, hello, turn dispatch, provider sign-in on request, and reconnect with backoff.
package relay

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gorilla/websocket"

	"github.com/Firmary/bridge/internal/devicekey"
	"github.com/Firmary/bridge/internal/logx"
	"github.com/Firmary/bridge/internal/pairing"
	"github.com/Firmary/bridge/internal/protocol"
)

// TurnHandler runs turns. HandleTurn is called on its own goroutine and must emit a terminal event.
type TurnHandler interface {
	HandleTurn(ctx context.Context, rec pairing.Record, t protocol.Turn, emit func(protocol.StreamEvent))
	Cancel(turnID string)
	Hello(ctx context.Context, rec pairing.Record) protocol.Hello
}

// Config configures a Client.
type Config struct {
	URL             string // relay URL; also the exact string signed in the auth payload
	Env             string // environment name stored in the pairing record
	KeyPath         string
	PairingPath     string
	HelperVersion   string
	Device          *protocol.PairingDevice // sent in pairing_reveal; may be nil
	Log             *logx.Logger
	Handler         TurnHandler
	ShowConnected   func()                          // tells the person the relay was reached; may be nil
	ShowPairingCode func(code string)               // shows the code to the person; never logged
	ShowPaired      func(userName, firmName string) // tells the person who they paired to; never logged
	// AskAccept shows the person and firm a pairing offer names and asks the person at this computer whether to
	// pair. answered is false when nobody can be asked (no terminal) or ctx ended first: then the Link sends
	// nothing and the relay keeps the binding unaccepted. The arguments are never logged. Nil never answers.
	AskAccept func(ctx context.Context, personName, personEmail, firmName string) (accept, answered bool)
	// ShowReady says the connection is up and paired, after hello. ShowOffline says it ended and the Link is trying
	// again. ShowUnpaired says the pairing was removed (revoked, declined, unpaired here or unknown to the relay).
	// Each may be nil.
	ShowReady    func(rec pairing.Record)
	ShowOffline  func()
	ShowUnpaired func()

	// TypedCode is pairing v2 (design 5877e3252806): Firmary shows the code and the person types it into the Link
	// (SetCode), or it comes with the install line (Code). The Link pairs only with such a code and never shows a
	// code of its own: unpaired and without a code it doesn't connect, and waits. Off is the v1 code match
	// (ShowPairingCode), kept for the relay's older Links.
	TypedCode bool
	// Code is a code to pair with from the start (already normalised, pairing.NormalizeCode).
	Code string
	// ShowNeedsCode says the Link waits for the person to enter the code Firmary shows. ShowClaimed says the relay
	// knows the code and names who asked Firmary for it ("Connecting to Firmary as <person> · <firm>"); the person
	// confirms in Firmary or cancels (CancelPairing). ShowCodeRejected says the relay refused the code: reason is
	// "expired", "not_found" or another machine name; the code is forgotten. The names are never logged. Each may be
	// nil.
	ShowNeedsCode    func()
	ShowClaimed      func(personName, firmName, code string)
	ShowCodeRejected func(reason string)
	// SignIn runs a provider sign-in the relay asked for (signin), on its own goroutine, calling step with the
	// provider's link (and code) for Firmary to show. Nil ignores signin.
	SignIn func(ctx context.Context, rec pairing.Record, provider string, step func(protocol.SigninStep))
	// SignInCode hands the code the person pasted in Firmary (signin_code) to the paste_code sign-in running for that
	// provider under rec. Nil ignores signin_code.
	SignInCode func(rec pairing.Record, provider, code string)
	// Refresh, if set, makes the Link send a fresh hello on the live connection, for example after a provider
	// sign-in, when no turn is running.
	Refresh    <-chan struct{}
	MinBackoff time.Duration
	MaxBackoff time.Duration
}

// Timeouts.
const (
	handshakeTimeout = 15 * time.Second
	writeTimeout     = 10 * time.Second
	pingEvery        = 25 * time.Second
	readTimeout      = 75 * time.Second
	stableAfter      = 60 * time.Second
	maxMessageBytes  = 8 << 20
)

// CheckURL allows wss:// anywhere and ws:// only to a loopback address (the fake relay in development), and only
// in canonical form, because the auth payload signs the URL as an exact string and the relay compares it with its
// own configured constant. Canonical: lower-case scheme and host, no userinfo, no port unless it differs from the
// scheme's default, path exactly "/", no query or fragment. For example wss://relay.prod.firmary.app/
func CheckURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return errors.New("relay URL is malformed")
	}
	defaultPort := map[string]string{"wss": "443", "ws": "80"}[u.Scheme]
	if u.String() != raw || u.Host != strings.ToLower(u.Host) || u.Path != "/" || u.RawPath != "" || u.RawQuery != "" ||
		u.ForceQuery || u.Fragment != "" || (u.Port() != "" && u.Port() == defaultPort) || strings.HasSuffix(u.Hostname(), ".") {
		return errors.New("relay URL is not in canonical form (scheme://host[:port]/)")
	}
	switch u.Scheme {
	case "wss":
		return nil
	case "ws":
		host := u.Hostname()
		if ip := net.ParseIP(host); (ip != nil && ip.IsLoopback()) || host == "localhost" {
			return nil
		}
		return errors.New("relay URL must use wss:// unless it is a loopback address")
	}
	return errors.New("relay URL must use wss://")
}

// Client is a relay connection that keeps itself up.
type Client struct {
	cfg    Config
	dialer *websocket.Dialer

	mu  sync.Mutex
	key *devicekey.Key
	// Pairing v2 state. code is the typed code still to pair with; claim is who the relay said asked for it.
	// needCode: the relay doesn't know this key, so the next connection waits for a code even with a local record.
	code     string
	claim    *claim
	needCode bool
	// cancelSession ends the live connection; authing is true until it is authenticated. stop is why
	// SetCode, CancelPairing or Unpair ended it.
	cancelSession context.CancelFunc
	authing       bool
	stop          error
	wake          chan struct{}
}

// claim is a pairing_claimed's names, exactly as the relay sent them.
type claim struct{ person, firm string }

// New loads (or creates) the device key and checks the relay URL.
func New(cfg Config) (*Client, error) {
	if err := CheckURL(cfg.URL); err != nil {
		return nil, err
	}
	if cfg.MinBackoff == 0 {
		cfg.MinBackoff = time.Second
	}
	if cfg.MaxBackoff == 0 {
		cfg.MaxBackoff = time.Minute
	}
	key, err := devicekey.LoadOrCreate(cfg.KeyPath)
	if err != nil {
		return nil, err
	}
	d := &websocket.Dialer{
		HandshakeTimeout: handshakeTimeout,
		TLSClientConfig:  &tls.Config{MinVersion: tls.VersionTLS12},
		Proxy:            http.ProxyFromEnvironment,
	}
	return &Client{cfg: cfg, dialer: d, key: key, code: cfg.Code, wake: make(chan struct{}, 1)}, nil
}

var (
	errRevoked  = errors.New("device revoked")
	errReset    = errors.New("pairing reset")
	errDeclined = errors.New("pairing declined")
	// Pairing v2.
	errNeedsCode = errors.New("pairing needs a code")
	errCanceled  = errors.New("pairing canceled")
	errNewCode   = errors.New("new pairing code")
	errUnpaired  = errors.New("unpaired on this computer")
)

// SetCode gives the Link the code Firmary shows (pairing v2). It is normalised first; a pairing already under way
// starts over with it.
func (c *Client) SetCode(typed string) error {
	code, err := pairing.NormalizeCode(typed)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.code, c.claim, c.needCode = code, nil, false
	if c.authing && c.cancelSession != nil {
		c.stop = errNewCode
		c.cancelSession()
	}
	c.mu.Unlock()
	c.poke()
	return nil
}

// CancelPairing is the person's Cancel while pairing: the connection closes, the code is forgotten, and the Link waits
// for a new one. It does nothing once the computer is paired.
func (c *Client) CancelPairing() {
	c.mu.Lock()
	c.code, c.claim, c.needCode = "", nil, true
	if c.authing && c.cancelSession != nil {
		c.stop = errCanceled
		c.cancelSession()
	}
	c.mu.Unlock()
	c.poke()
}

// Unpair removes this computer's pairing here: the connection closes, the pairing and the device key are deleted,
// and the Link waits for a new code. Firmary sees the computer go offline; the person removes it there.
func (c *Client) Unpair() error {
	c.mu.Lock()
	c.code, c.claim, c.needCode = "", nil, true
	live := c.cancelSession != nil
	if live {
		c.stop = errUnpaired
		c.cancelSession()
	}
	c.mu.Unlock()
	c.poke()
	if live {
		return nil // the session wipes as it ends
	}
	return c.wipe(logx.Unpaired, logx.Fields{}, nil)
}

func (c *Client) poke() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// waiting reports whether the next connection must wait for a typed code.
func (c *Client) waiting() bool {
	if !c.cfg.TypedCode {
		return false
	}
	c.mu.Lock()
	code, need := c.code, c.needCode
	c.mu.Unlock()
	if code != "" {
		return false
	}
	if need {
		return true
	}
	local, err := pairing.Load(c.cfg.PairingPath)
	return err == nil && local == nil
}

// waitForCode returns once the Link may connect: always for v1; for v2 when it has a code or a pairing.
func (c *Client) waitForCode(ctx context.Context) error {
	shown := false
	for c.waiting() {
		if !shown && c.cfg.ShowNeedsCode != nil {
			c.cfg.ShowNeedsCode()
		}
		shown = true
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.wake:
		}
	}
	return nil
}

// takeStop returns (and clears) why SetCode, CancelPairing or Unpair ended the session, if they did.
func (c *Client) takeStop() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	err := c.stop
	c.stop = nil
	return err
}

// Run connects and reconnects until ctx is done.
func (c *Client) Run(ctx context.Context) error {
	backoff := c.cfg.MinBackoff
	for attempt := 1; ; attempt++ {
		if err := c.waitForCode(ctx); err != nil {
			return err
		}
		started := time.Now()
		err := c.session(ctx, attempt)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		waiting := c.waiting()
		if c.cfg.ShowOffline != nil && !waiting {
			c.cfg.ShowOffline()
		}
		c.cfg.Log.Log(logx.RelayDisconnect, logx.Fields{Reason: reasonOf(err), DurationMs: time.Since(started).Milliseconds(), Attempt: attempt})
		if waiting || errors.Is(err, errNewCode) {
			// Pairing waits for the person (a new code), or they just gave one: no backoff either way.
			backoff = c.cfg.MinBackoff
			continue
		}
		// A revoke or reset still waits: a hostile relay must not be able to make the Link spin.
		if errors.Is(err, errRevoked) || errors.Is(err, errReset) || errors.Is(err, errDeclined) || time.Since(started) > stableAfter {
			backoff = c.cfg.MinBackoff
		}
		// Full jitter: wait a random time up to the current backoff.
		wait := time.Duration(rand.Int64N(int64(backoff))) + c.cfg.MinBackoff/2
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		backoff = min(backoff*2, c.cfg.MaxBackoff)
	}
}

// sessionError carries a closed-set reason code for logging.
type sessionError string

func (e sessionError) Error() string { return string(e) }

// relayError is the reason for a relay error message. The code is used only if it is a machine name, so a hostile
// relay can't put text into the log.
func relayError(b []byte) sessionError {
	m, _ := protocol.Decode[protocol.RelayError](b)
	if !protocol.MachineNameRe.MatchString(m.Code) {
		return sessionError("bad_relay_error")
	}
	return sessionError("relay_error_" + m.Code)
}

func reasonOf(err error) string {
	var se sessionError
	switch {
	case err == nil:
		return "closed"
	case errors.Is(err, errRevoked):
		return "revoked"
	case errors.Is(err, errReset):
		return "pairing_reset"
	case errors.Is(err, errDeclined):
		return "pairing_declined"
	case errors.Is(err, errNeedsCode):
		return "pairing_needs_code"
	case errors.Is(err, errCanceled):
		return "pairing_canceled"
	case errors.Is(err, errNewCode):
		return "pairing_new_code"
	case errors.Is(err, errUnpaired):
		return "unpaired"
	case errors.As(err, &se):
		return string(se)
	}
	return "connection_lost"
}

type conn struct {
	ws *websocket.Conn
	mu sync.Mutex
}

func (c *conn) send(v any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.ws.SetWriteDeadline(time.Now().Add(writeTimeout))
	return c.ws.WriteJSON(v)
}

func (c *conn) read() ([]byte, string, error) {
	_, b, err := c.ws.ReadMessage()
	if err != nil {
		return nil, "", err
	}
	_ = c.ws.SetReadDeadline(time.Now().Add(readTimeout))
	var env protocol.Envelope
	if json.Unmarshal(b, &env) != nil {
		return nil, "", sessionError("malformed_message")
	}
	return b, env.Type, nil
}

func (c *Client) session(ctx context.Context, attempt int) (err error) {
	c.cfg.Log.Log(logx.RelayConnect, logx.Fields{Attempt: attempt})
	ws, _, err := c.dialer.DialContext(ctx, c.cfg.URL, nil)
	if err != nil {
		return sessionError("dial_failed")
	}
	if c.cfg.ShowConnected != nil {
		c.cfg.ShowConnected()
	}
	ws.SetReadLimit(maxMessageBytes)
	_ = ws.SetReadDeadline(time.Now().Add(handshakeTimeout))
	cn := &conn{ws: ws}
	sctx, cancel := context.WithCancel(ctx)
	defer func() {
		cancel()
		ws.Close()
	}()
	go func() {
		<-sctx.Done()
		_ = ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
		ws.Close()
	}()
	go func() {
		t := time.NewTicker(pingEvery)
		defer t.Stop()
		for {
			select {
			case <-sctx.Done():
				return
			case <-t.C:
				if ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeTimeout)) != nil {
					return
				}
			}
		}
	}()
	ws.SetPongHandler(func(string) error { return ws.SetReadDeadline(time.Now().Add(readTimeout)) })

	c.mu.Lock()
	c.cancelSession, c.authing, c.stop = cancel, true, nil
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.cancelSession, c.authing = nil, false
		c.mu.Unlock()
	}()
	// Whichever way the session ends, SetCode, CancelPairing or Unpair stopping it decides the result. This runs
	// after running turns were cancelled and waited for (deferred below, so they run first).
	defer func() {
		switch stop := c.takeStop(); {
		case errors.Is(stop, errUnpaired):
			err = c.wipe(logx.Unpaired, logx.Fields{}, errUnpaired)
		case errors.Is(stop, errCanceled):
			c.cfg.Log.Log(logx.PairingCanceled, logx.Fields{})
			err = stop
		case stop != nil:
			err = stop
		}
	}()
	rec, err := c.authenticate(sctx, cn)
	c.mu.Lock()
	c.authing = false
	c.mu.Unlock()
	if err != nil {
		return err
	}
	c.cfg.Log.Log(logx.HelloSent, logx.Fields{DeviceID: rec.DeviceID, Version: c.cfg.HelperVersion})
	hello := c.cfg.Handler.Hello(sctx, rec)
	hello.Type = protocol.TypeHello
	if err := cn.send(hello); err != nil {
		return err
	}
	if c.cfg.ShowReady != nil {
		c.cfg.ShowReady(rec)
	}
	if c.cfg.Refresh != nil {
		go c.refresh(sctx, cn, rec)
	}

	var turns sync.WaitGroup
	// Cancel running turns before waiting for them: after a dropped connection nobody receives their output, and
	// waiting with the session context live would keep the Link offline until they finish.
	defer func() {
		cancel()
		turns.Wait()
	}()
	for {
		b, typ, err := cn.read()
		if err != nil {
			return err
		}
		switch typ {
		case protocol.TypeSignin:
			m, err := protocol.Decode[protocol.Signin](b)
			if err != nil || c.cfg.SignIn == nil {
				c.cfg.Log.Log(logx.UnknownMessage, logx.Fields{Reason: typ})
				continue
			}
			// A sign-in outlives a reconnect (a device code lasts 15 minutes): it runs under the Link's context, and
			// its steps go out on the connection that asked.
			go c.cfg.SignIn(ctx, rec, m.Provider, func(s protocol.SigninStep) {
				s.Type, s.Provider = protocol.TypeSigninStep, m.Provider
				_ = cn.send(s)
			})
		case protocol.TypeSigninCode:
			m, err := protocol.Decode[protocol.SigninCode](b)
			if err != nil || c.cfg.SignInCode == nil || !protocol.ValidSigninCode(m.Code) {
				c.cfg.Log.Log(logx.UnknownMessage, logx.Fields{Reason: typ})
				continue
			}
			c.cfg.SignInCode(rec, m.Provider, m.Code)
		case protocol.TypeTurn:
			t, err := protocol.Decode[protocol.Turn](b)
			if err != nil {
				return sessionError("malformed_message")
			}
			turns.Add(1)
			go func() {
				defer turns.Done()
				c.cfg.Handler.HandleTurn(sctx, rec, t, func(e protocol.StreamEvent) {
					_ = cn.send(protocol.EventMsg{Type: protocol.TypeEvent, TurnID: t.TurnID, Event: e})
				})
			}()
		case protocol.TypeCancel:
			m, err := protocol.Decode[protocol.Cancel](b)
			if err == nil {
				c.cfg.Handler.Cancel(m.TurnID)
			}
		case protocol.TypeRevoke:
			cancel()
			turns.Wait()
			return c.revoke()
		case protocol.TypeError:
			return relayError(b)
		default:
			c.cfg.Log.Log(logx.UnknownMessage, logx.Fields{Reason: typ})
		}
	}
}

// idler is a TurnHandler that can say whether a turn is running.
type idler interface{ Idle() bool }

// refresh sends a fresh hello whenever Refresh fires, waiting for any running turn to end first: a provider status
// check must not run under a turn.
func (c *Client) refresh(ctx context.Context, cn *conn, rec pairing.Record) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.cfg.Refresh:
		}
		if h, ok := c.cfg.Handler.(idler); ok {
			for !h.Idle() {
				select {
				case <-ctx.Done():
					return
				case <-time.After(2 * time.Second):
				}
			}
		}
		hello := c.cfg.Handler.Hello(ctx, rec)
		hello.Type = protocol.TypeHello
		if ctx.Err() != nil || cn.send(hello) != nil {
			return
		}
		c.cfg.Log.Log(logx.HelloSent, logx.Fields{DeviceID: rec.DeviceID, Version: c.cfg.HelperVersion, Reason: "refresh"})
	}
}

// authenticate answers the challenge and returns this device's pairing, pairing first if the relay asks.
func (c *Client) authenticate(ctx context.Context, cn *conn) (pairing.Record, error) {
	var none pairing.Record
	b, typ, err := cn.read()
	if err != nil {
		return none, err
	}
	if typ != protocol.TypeChallenge {
		return none, sessionError("expected_challenge")
	}
	ch, err := protocol.Decode[protocol.Challenge](b)
	if err != nil {
		return none, err
	}
	nonce, err := base64.RawURLEncoding.DecodeString(ch.Nonce)
	if err != nil || len(nonce) < 32 || len(nonce) > 256 {
		return none, sessionError("bad_challenge")
	}
	c.mu.Lock()
	key := c.key
	c.mu.Unlock()
	// A fresh helper nonce per connection, committed to now and revealed only after the relay's pairing nonce.
	helperNonce, err := pairing.NewNonce()
	if err != nil {
		return none, sessionError("rand_failed")
	}
	err = cn.send(protocol.Auth{
		Type:          protocol.TypeAuth,
		PublicKey:     base64.RawURLEncoding.EncodeToString(key.Public()),
		Signature:     base64.RawURLEncoding.EncodeToString(key.SignAuth(c.cfg.URL, nonce)),
		HelperVersion: c.cfg.HelperVersion,
		PairingCommit: base64.RawURLEncoding.EncodeToString(pairing.Commit(key.Public(), helperNonce)),
	})
	if err != nil {
		return none, err
	}
	local, err := pairing.Load(c.cfg.PairingPath)
	if err != nil {
		return none, sessionError("pairing_unreadable")
	}
	c.mu.Lock()
	code, claimed := c.code, c.claim // pairing v2: the typed code, and who asked for it (from an earlier connection)
	c.mu.Unlock()
	if !c.cfg.TypedCode {
		code, claimed = "", nil
	}
	codeShown := false            // this connection revealed its nonce
	claimedHere := false          // and the relay named who asked for the code
	var offered *pairing.Record   // the binding offered on this connection
	accepted := false             // this computer said yes to it
	var pending <-chan readResult // a read started while the person was asked

	for {
		var b []byte
		var typ string
		if pending != nil {
			r := <-pending
			pending = nil
			b, typ, err = r.b, r.typ, r.err
		} else {
			b, typ, err = cn.read()
		}
		if err != nil {
			return none, err
		}
		switch typ {
		case protocol.TypeWelcome:
			m, err := protocol.Decode[protocol.Welcome](b)
			if err != nil {
				return none, err
			}
			rec := pairing.Record{DeviceID: m.DeviceID, UserID: m.UserID, FirmID: m.FirmID, Env: c.cfg.Env}
			if err := rec.Validate(); err != nil {
				return none, sessionError("bad_welcome")
			}
			if local == nil || *local != rec {
				// The relay names a pairing this Link didn't see confirmed. Never adopt it: start over with a
				// new key, so the next connection pairs by code match.
				return none, c.wipe(logx.PairingPending, logx.Fields{Reason: "pairing_mismatch"}, errReset)
			}
			c.cfg.Log.Log(logx.RelayAuthed, logx.Fields{DeviceID: rec.DeviceID})
			return rec, nil
		case protocol.TypePairingPending:
			if codeShown || offered != nil {
				// One relay nonce per connection: a second one would let the relay try again against the same Nh.
				return none, sessionError("unexpected_message")
			}
			m, err := protocol.Decode[protocol.PairingPending](b)
			if err != nil {
				return none, err
			}
			relayNonce, err := base64.RawURLEncoding.DecodeString(m.PairingNonce)
			if err != nil || len(relayNonce) != pairing.NonceSize {
				return none, sessionError("bad_pairing_nonce")
			}
			if local != nil {
				// The relay no longer knows this key. Keep the local record until a confirmed pairing replaces it.
				c.cfg.Log.Log(logx.PairingPending, logx.Fields{DeviceID: local.DeviceID, Reason: "relay_forgot_device"})
			} else {
				c.cfg.Log.Log(logx.PairingPending, logx.Fields{})
			}
			if c.cfg.TypedCode && code == "" {
				// Pairing v2 pairs only with the code Firmary shows: wait for the person to enter it.
				c.mu.Lock()
				c.needCode = true
				c.mu.Unlock()
				return none, errNeedsCode
			}
			if err := cn.send(protocol.PairingReveal{Type: protocol.TypePairingReveal,
				Nonce: base64.RawURLEncoding.EncodeToString(helperNonce), Device: c.cfg.Device, Code: code}); err != nil {
				return none, err
			}
			// No read deadline while the person confirms the code; pings keep the connection checked.
			_ = cn.ws.SetReadDeadline(time.Time{})
			codeShown = true
			if code == "" && c.cfg.ShowPairingCode != nil {
				c.cfg.ShowPairingCode(pairing.Code(key.Public(), relayNonce, helperNonce))
			}
		case protocol.TypePairingClaimed:
			// Pairing v2: only after this connection revealed a typed code, once, and before any offer.
			if !codeShown || code == "" || claimedHere || offered != nil {
				return none, sessionError("unexpected_message")
			}
			m, err := protocol.Decode[protocol.PairingClaimed](b)
			if err != nil {
				return none, err
			}
			personName, okName := displayName(m.PersonName)
			firmName, okFirm := displayName(m.FirmName)
			if !okName || !okFirm {
				return none, sessionError("bad_pairing_claimed")
			}
			claimedHere, claimed = true, &claim{person: m.PersonName, firm: m.FirmName}
			c.mu.Lock()
			if c.code == code {
				c.claim = claimed
			}
			c.mu.Unlock()
			c.cfg.Log.Log(logx.PairingClaimed, logx.Fields{})
			if c.cfg.ShowClaimed != nil {
				c.cfg.ShowClaimed(personName, firmName, code)
			}
		case protocol.TypePairingOffer:
			// Security re-review M3: a code read out to someone else must not bind this computer to them. The relay
			// offers the binding a person confirmed in Firmary, after this connection's code or (a binding never
			// answered on an earlier connection) right after auth, and pairs only on this computer's own yes.
			if offered != nil {
				return none, sessionError("unexpected_message")
			}
			m, err := protocol.Decode[protocol.PairingOffer](b)
			if err != nil {
				return none, err
			}
			rec := pairing.Record{DeviceID: m.DeviceID, UserID: m.UserID, FirmID: m.FirmID, Env: c.cfg.Env}
			personName, okName := displayName(m.PersonName)
			personEmail, okEmail := displayName(m.PersonEmail)
			firmName, okFirm := displayName(m.FirmName)
			if rec.Validate() != nil || !okName || !okEmail || !okFirm {
				return none, sessionError("bad_pairing_offer")
			}
			offered = &rec
			c.cfg.Log.Log(logx.PairingOffered, logx.Fields{DeviceID: rec.DeviceID})
			if code != "" && claimed != nil && m.PersonName == claimed.person && m.FirmName == claimed.firm {
				// Pairing v2: the person typed Firmary's code on this computer and saw who asked for it, so that was
				// the local yes (M3). The offer must name the same person and firm, byte for byte.
				if err := cn.send(protocol.PairingAnswer{Type: protocol.TypePairingAccept, DeviceID: rec.DeviceID}); err != nil {
					return none, err
				}
				accepted = true
				c.cfg.Log.Log(logx.PairingAccepted, logx.Fields{DeviceID: rec.DeviceID, Reason: "typed_code"})
				_ = cn.ws.SetReadDeadline(time.Now().Add(handshakeTimeout))
				continue
			}
			// No read deadline while the person reads the offer: the relay withdraws it after its own timeout.
			_ = cn.ws.SetReadDeadline(time.Time{})
			accept, answered, next := c.ask(ctx, cn, personName, personEmail, firmName)
			pending = next
			switch {
			case !answered:
				// Nobody could be asked (no terminal), or the relay spoke first. Never accept for the person, and
				// don't decline either: the relay treats a decline as the person disowning the binding and revokes
				// it. Wait; the relay withdraws the offer when its accept timeout ends, then offers it again on
				// the next connection, where someone at a terminal can answer.
				c.cfg.Log.Log(logx.PairingOffered, logx.Fields{DeviceID: rec.DeviceID, Reason: "unanswered"})
			case accept:
				if err := cn.send(protocol.PairingAnswer{Type: protocol.TypePairingAccept, DeviceID: rec.DeviceID}); err != nil {
					return none, err
				}
				accepted = true
				c.cfg.Log.Log(logx.PairingAccepted, logx.Fields{DeviceID: rec.DeviceID})
				_ = cn.ws.SetReadDeadline(time.Now().Add(handshakeTimeout))
			default:
				// The relay revokes the binding. Start over with a new key, as after a revoke, whether or not the
				// decline got through: the old key's binding can then never be accepted.
				_ = cn.send(protocol.PairingAnswer{Type: protocol.TypePairingDecline, DeviceID: rec.DeviceID})
				return none, c.wipe(logx.PairingDeclined, logx.Fields{DeviceID: rec.DeviceID}, errDeclined)
			}
		case protocol.TypePaired:
			if !accepted {
				// Pairing only ever follows this computer's own yes to the offer.
				return none, sessionError("paired_without_accept")
			}
			m, err := protocol.Decode[protocol.Paired](b)
			if err != nil {
				return none, err
			}
			userName, okUser := displayName(m.UserName)
			firmName, okFirm := displayName(m.FirmName)
			if !okUser || !okFirm {
				return none, sessionError("bad_paired")
			}
			rec := pairing.Record{DeviceID: m.DeviceID, UserID: m.UserID, FirmID: m.FirmID, Env: c.cfg.Env}
			if rec != *offered {
				return none, sessionError("paired_not_offered")
			}
			if err := pairing.Save(c.cfg.PairingPath, rec); err != nil {
				return none, sessionError("pairing_save_failed")
			}
			c.mu.Lock()
			c.code, c.claim, c.needCode = "", nil, false
			c.mu.Unlock()
			c.cfg.Log.Log(logx.Paired, logx.Fields{DeviceID: rec.DeviceID})
			if c.cfg.ShowPaired != nil {
				c.cfg.ShowPaired(userName, firmName)
			}
			return rec, nil
		case protocol.TypeError:
			err := relayError(b)
			if reason, ok := strings.CutPrefix(string(err), "relay_error_pairing_code_"); ok && code != "" {
				// The relay refused the typed code (pairing_code_not_found, pairing_code_expired): forget it, and wait
				// for the person to enter a new one.
				c.mu.Lock()
				if c.code == code {
					c.code, c.claim, c.needCode = "", nil, true
				}
				c.mu.Unlock()
				if c.cfg.ShowCodeRejected != nil {
					c.cfg.ShowCodeRejected(reason)
				}
			}
			return none, err
		default:
			return none, sessionError("unexpected_message")
		}
	}
}

type readResult struct {
	b   []byte
	typ string
	err error
}

// ask puts an offer to the person while it keeps reading the connection, so pings are still answered and a relay
// that withdraws the offer (its accept timeout, or a newer connection for this device) ends the question. The caller
// takes its next message from next.
func (c *Client) ask(ctx context.Context, cn *conn, personName, personEmail, firmName string) (accept, answered bool, next <-chan readResult) {
	read := make(chan readResult, 1)
	go func() {
		b, typ, err := cn.read()
		read <- readResult{b, typ, err}
	}()
	if c.cfg.AskAccept == nil {
		return false, false, read
	}
	actx, cancel := context.WithCancel(ctx)
	defer cancel()
	type reply struct{ accept, answered bool }
	replied := make(chan reply, 1)
	go func() {
		a, ok := c.cfg.AskAccept(actx, personName, personEmail, firmName)
		replied <- reply{a, ok}
	}()
	select {
	case r := <-replied:
		return r.accept, r.answered, read
	case r := <-read:
		// The relay spoke, or the connection ended, before the person answered: the question is void.
		cancel()
		<-replied
		again := make(chan readResult, 1)
		again <- r
		return false, false, again
	}
}

// maxDisplayName bounds a person's or firm's display name, in characters.
const maxDisplayName = 200

// displayName makes a relay-supplied name safe to print on a terminal: control characters and bidi overrides are
// removed (so the name can't move the cursor or disguise itself), and it must be 1 to maxDisplayName characters.
func displayName(s string) (string, bool) {
	if !utf8.ValidString(s) {
		return "", false
	}
	s = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) {
			return -1
		}
		return r
	}, s))
	n := utf8.RuneCountInString(s)
	return s, n > 0 && n <= maxDisplayName
}

// revoke handles the relay's revoke: the device was unpaired.
func (c *Client) revoke() error { return c.wipe(logx.Revoked, logx.Fields{}, errRevoked) }

// wipe deletes the pairing and the device key, then makes a fresh key so the next connection pairs anew. A typed
// code is forgotten too.
func (c *Client) wipe(ev logx.Event, f logx.Fields, result error) error {
	c.cfg.Log.Log(ev, f)
	c.mu.Lock()
	c.code, c.claim = "", nil
	c.mu.Unlock()
	if c.cfg.ShowUnpaired != nil {
		c.cfg.ShowUnpaired()
	}
	if err := pairing.Delete(c.cfg.PairingPath); err != nil {
		return sessionError("revoke_failed")
	}
	if err := devicekey.Delete(c.cfg.KeyPath); err != nil {
		return sessionError("revoke_failed")
	}
	key, err := devicekey.LoadOrCreate(c.cfg.KeyPath)
	if err != nil {
		return sessionError("revoke_failed")
	}
	c.mu.Lock()
	c.key = key
	c.mu.Unlock()
	return result
}
