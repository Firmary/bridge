// Package fakerelay is a local stand-in for the Firmary relay, for tests and development. It listens on loopback
// only, verifies the Helper's challenge signature, computes the pairing code server-side, mints turn tickets with
// a throwaway key, and records what the Helper sends.
package fakerelay

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Firmary/firmary-helper/internal/devicekey"
	"github.com/Firmary/firmary-helper/internal/pairing"
	"github.com/Firmary/firmary-helper/internal/protocol"
	"github.com/Firmary/firmary-helper/internal/ticket"
)

// Device is a paired device as the relay knows it.
type Device struct{ DeviceID, UserID, FirmID string }

// Relay is a running fake relay.
type Relay struct {
	URL       string
	TicketKey ed25519.PrivateKey
	TicketPub ed25519.PublicKey
	// AutoConfirm pairs a pending device at once, as if the person confirmed the code.
	AutoConfirm bool

	Codes  chan string
	Hellos chan protocol.Hello
	Events chan protocol.EventMsg

	srv *http.Server
	ln  net.Listener
	up  websocket.Upgrader

	mu       sync.Mutex
	devices  map[string]Device // base64url public key -> device
	pending  map[string]chan Device
	conn     *websocket.Conn
	wmu      sync.Mutex
	current  *Device
	Connects int
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
		Codes: make(chan string, 64), Hellos: make(chan protocol.Hello, 64), Events: make(chan protocol.EventMsg, 1024),
		ln: ln, devices: map[string]Device{}, pending: map[string]chan Device{},
	}
	r.srv = &http.Server{Handler: http.HandlerFunc(r.serve), ReadHeaderTimeout: 5 * time.Second}
	go r.srv.Serve(ln)
	return r, nil
}

// Close stops the relay.
func (r *Relay) Close() { r.srv.Close() }

func newID(prefix string) string {
	b := make([]byte, 6)
	rand.Read(b)
	return prefix + hex.EncodeToString(b)
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
	if err1 != nil || err2 != nil || len(pub) != ed25519.PublicKeySize ||
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
	if known {
		if c.WriteJSON(protocol.Welcome{Type: protocol.TypeWelcome, DeviceID: dev.DeviceID, UserID: dev.UserID, FirmID: dev.FirmID}) != nil {
			return
		}
	} else {
		code := pairing.Code(pub, nonce)
		confirmed := make(chan Device, 1)
		r.mu.Lock()
		r.pending[code] = confirmed
		r.mu.Unlock()
		if c.WriteJSON(protocol.PairingPending{Type: protocol.TypePairingPending}) != nil {
			return
		}
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
		if c.WriteJSON(protocol.Paired{Type: protocol.TypePaired, DeviceID: dev.DeviceID, UserID: dev.UserID, FirmID: dev.FirmID}) != nil {
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
		}
	}
}

// Confirm confirms a pending pairing by its code, as the person would in Firmary.
func (r *Relay) Confirm(code string) bool {
	r.mu.Lock()
	ch, ok := r.pending[code]
	delete(r.pending, code)
	r.mu.Unlock()
	if ok {
		ch <- Device{DeviceID: newID("dev_"), UserID: newID("usr_"), FirmID: newID("firm_")}
	}
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

// WaitConnected waits until a paired Helper is connected.
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

// Claims returns valid claims for a turn on the connected device.
func (r *Relay) Claims(d Device, pin, context, prompt string) ticket.Claims {
	now := time.Now()
	return ticket.Claims{
		TicketID: newID("tkt_"), DeviceID: d.DeviceID, UserID: d.UserID, FirmID: d.FirmID,
		ConversationID: newID("conv_"), Adapter: "helper-claude-code", Model: "claude-sonnet-4-5", Pin: pin,
		Trigger: ticket.Trigger{Kind: "interactive", SessionID: newID("sess_")}, MaxOutputTokens: 2000,
		PromptHash: protocol.PromptHash(context, prompt, nil), AccountTypes: []string{"pro", "max", "team", "enterprise"},
		IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Minute).Unix(),
	}
}

// SendTurn signs c and sends the turn.
func (r *Relay) SendTurn(c ticket.Claims, context, prompt string) error {
	return r.send(protocol.Turn{Type: protocol.TypeTurn, TurnID: c.TicketID, Ticket: ticket.Sign(r.TicketKey, c), Context: context, Prompt: prompt})
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

// Forget removes every paired device, as an unpair while the Helper was offline would.
func (r *Relay) Forget() {
	r.mu.Lock()
	r.devices = map[string]Device{}
	r.mu.Unlock()
}
