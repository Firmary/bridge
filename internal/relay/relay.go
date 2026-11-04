// Package relay is the Helper's one outbound connection to the Firmary relay: challenge-response authentication
// with the device key, code-match pairing and the accept step, hello, turn dispatch, and reconnect with backoff.
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
	Log             *logx.Logger
	Handler         TurnHandler
	ShowConnected   func()                          // tells the person the relay was reached; may be nil
	ShowPairingCode func(code string)               // shows the code to the person; never logged
	ShowPaired      func(userName, firmName string) // tells the person who they paired to; never logged
	// AskAccept shows the person and firm a pairing offer names and asks the person at this computer whether to
	// pair. answered is false when nobody can be asked (no terminal) or ctx ended first: then the Helper sends
	// nothing and the relay keeps the binding unaccepted. The arguments are never logged. Nil never answers.
	AskAccept  func(ctx context.Context, personName, personEmail, firmName string) (accept, answered bool)
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
}

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
	return &Client{cfg: cfg, dialer: d, key: key}, nil
}

var (
	errRevoked  = errors.New("device revoked")
	errReset    = errors.New("pairing reset")
	errDeclined = errors.New("pairing declined")
)

// Run connects and reconnects until ctx is done.
func (c *Client) Run(ctx context.Context) error {
	backoff := c.cfg.MinBackoff
	for attempt := 1; ; attempt++ {
		started := time.Now()
		err := c.session(ctx, attempt)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c.cfg.Log.Log(logx.RelayDisconnect, logx.Fields{Reason: reasonOf(err), DurationMs: time.Since(started).Milliseconds(), Attempt: attempt})
		// A revoke or reset still waits: a hostile relay must not be able to make the Helper spin.
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

func (c *Client) session(ctx context.Context, attempt int) error {
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

	rec, err := c.authenticate(sctx, cn)
	if err != nil {
		return err
	}
	c.cfg.Log.Log(logx.HelloSent, logx.Fields{DeviceID: rec.DeviceID, Version: c.cfg.HelperVersion})
	hello := c.cfg.Handler.Hello(sctx, rec)
	hello.Type = protocol.TypeHello
	if err := cn.send(hello); err != nil {
		return err
	}

	var turns sync.WaitGroup
	// Cancel running turns before waiting for them: after a dropped connection nobody receives their output, and
	// waiting with the session context live would keep the Helper offline until they finish.
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
	codeShown := false
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
				// The relay names a pairing this Helper didn't see confirmed. Never adopt it: start over with a
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
			if err := cn.send(protocol.PairingReveal{Type: protocol.TypePairingReveal,
				Nonce: base64.RawURLEncoding.EncodeToString(helperNonce)}); err != nil {
				return none, err
			}
			// No read deadline while the person confirms the code; pings keep the connection checked.
			_ = cn.ws.SetReadDeadline(time.Time{})
			codeShown = true
			if c.cfg.ShowPairingCode != nil {
				c.cfg.ShowPairingCode(pairing.Code(key.Public(), relayNonce, helperNonce))
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
			c.cfg.Log.Log(logx.Paired, logx.Fields{DeviceID: rec.DeviceID})
			if c.cfg.ShowPaired != nil {
				c.cfg.ShowPaired(userName, firmName)
			}
			return rec, nil
		case protocol.TypeError:
			return none, relayError(b)
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

// wipe deletes the pairing and the device key, then makes a fresh key so the next connection pairs anew.
func (c *Client) wipe(ev logx.Event, f logx.Fields, result error) error {
	c.cfg.Log.Log(ev, f)
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
