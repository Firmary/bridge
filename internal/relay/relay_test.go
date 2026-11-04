//go:build !windows

package relay

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Firmary/bridge/internal/fakerelay"
	"github.com/Firmary/bridge/internal/pairing"
	"github.com/Firmary/bridge/internal/protocol"
)

var b64 = base64.RawURLEncoding

// scriptedRelay runs script on the Helper's first connection; later connections are closed at once.
func scriptedRelay(t *testing.T, script func(c *websocket.Conn)) string {
	t.Helper()
	var n atomic.Int32
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		if n.Add(1) == 1 {
			script(c)
		}
	}))
	t.Cleanup(srv.Close)
	return "ws://" + strings.TrimPrefix(srv.URL, "http://") + "/"
}

func runHelper(t *testing.T, url string, shown chan<- string) {
	t.Helper()
	dir := t.TempDir()
	c, err := New(Config{URL: url, Env: "local", KeyPath: filepath.Join(dir, "device.key"),
		PairingPath: filepath.Join(dir, "pairing.json"), HelperVersion: "0.0.0-test",
		ShowConnected:   func() { shown <- "reached" },
		ShowPairingCode: func(code string) { shown <- code }, MinBackoff: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

func nonce() []byte {
	b := make([]byte, pairing.NonceSize)
	rand.Read(b)
	return b
}

// TestPairingCommitThenReveal checks the Helper's half of the pairing protocol: it commits to its nonce before it
// sees the relay's, reveals it only after, shows the code over both, and accepts only one relay nonce per
// connection.
func TestPairingCommitThenReveal(t *testing.T) {
	type result struct {
		pub, commit, relayNonce, helperNonce []byte
		secondReveal                         bool
	}
	got := make(chan result, 1)
	url := scriptedRelay(t, func(c *websocket.Conn) {
		var res result
		defer func() { got <- res }()
		c.WriteJSON(protocol.Challenge{Type: protocol.TypeChallenge, Nonce: b64.EncodeToString(nonce())})
		var auth protocol.Auth
		if c.ReadJSON(&auth) != nil {
			return
		}
		res.pub, _ = b64.DecodeString(auth.PublicKey)
		res.commit, _ = b64.DecodeString(auth.PairingCommit)
		res.relayNonce = nonce()
		c.WriteJSON(protocol.PairingPending{Type: protocol.TypePairingPending, PairingNonce: b64.EncodeToString(res.relayNonce)})
		var reveal protocol.PairingReveal
		if c.ReadJSON(&reveal) != nil || reveal.Type != protocol.TypePairingReveal {
			return
		}
		res.helperNonce, _ = b64.DecodeString(reveal.Nonce)
		// A second relay nonce on the same connection must be refused, not answered.
		c.WriteJSON(protocol.PairingPending{Type: protocol.TypePairingPending, PairingNonce: b64.EncodeToString(nonce())})
		c.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, _, err := c.ReadMessage()
		res.secondReveal = err == nil
	})
	shown := make(chan string, 4)
	runHelper(t, url, shown)

	var res result
	select {
	case res = <-got:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out")
	}
	if len(res.helperNonce) != pairing.NonceSize || !bytes.Equal(pairing.Commit(ed25519.PublicKey(res.pub), res.helperNonce), res.commit) {
		t.Fatal("the revealed nonce does not match the commitment sent before the relay nonce")
	}
	// The person first sees that Firmary was reached, then the code (design: "Always-on server", step 2).
	if first := <-shown; first != "reached" {
		t.Fatalf("first message shown was %q, want the reached line before the code", first)
	}
	if code := <-shown; code != pairing.Code(res.pub, res.relayNonce, res.helperNonce) {
		t.Fatalf("shown code %s is not the code over pub, Nr and Nh", code)
	}
	if res.secondReveal {
		t.Fatal("the Helper answered a second relay nonce on the same connection")
	}
}

// TestPairingRefusesBadRelayNonce: a relay nonce that isn't exactly 32 bytes gets no reveal and no code.
func TestPairingRefusesBadRelayNonce(t *testing.T) {
	revealed := make(chan bool, 1)
	url := scriptedRelay(t, func(c *websocket.Conn) {
		c.WriteJSON(protocol.Challenge{Type: protocol.TypeChallenge, Nonce: b64.EncodeToString(nonce())})
		var auth protocol.Auth
		c.ReadJSON(&auth)
		c.WriteJSON(protocol.PairingPending{Type: protocol.TypePairingPending, PairingNonce: b64.EncodeToString(nonce()[:16])})
		c.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, _, err := c.ReadMessage()
		revealed <- err == nil
	})
	shown := make(chan string, 4)
	runHelper(t, url, shown)
	if <-revealed {
		t.Fatal("the Helper revealed its nonce for a short relay nonce")
	}
	if first := <-shown; first != "reached" {
		t.Fatalf("first message shown was %q, want the reached line", first)
	}
	select {
	case <-shown:
		t.Fatal("a code was shown")
	default:
	}
}

type noTurns struct{}

func (noTurns) HandleTurn(context.Context, pairing.Record, protocol.Turn, func(protocol.StreamEvent)) {
}
func (noTurns) Cancel(string)                                        {}
func (noTurns) Hello(context.Context, pairing.Record) protocol.Hello { return protocol.Hello{} }

// TestPairingAcceptStep (security re-review M3): once the code is confirmed in Firmary, the relay offers the binding
// and the Helper pairs only on the person's yes. A no sends pairing_decline and starts over with a new key. With
// nobody to ask, the Helper sends nothing: the binding is neither accepted nor revoked.
func TestPairingAcceptStep(t *testing.T) {
	for _, c := range []struct {
		name             string
		accept, answered bool
		want             string // the Helper's answer to the offer; "" for none
	}{
		{"accept", true, true, protocol.TypePairingAccept},
		{"decline", false, true, protocol.TypePairingDecline},
		{"no terminal", false, false, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, err := fakerelay.Start("127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(r.Close)
			r.AutoConfirm = true
			dir := t.TempDir()
			keyPath, pairingPath := filepath.Join(dir, "device.key"), filepath.Join(dir, "pairing.json")
			asked, paired := make(chan string, 4), make(chan string, 4)
			cl, err := New(Config{URL: r.URL, Env: "local", KeyPath: keyPath, PairingPath: pairingPath,
				HelperVersion: "0.0.0-test", Handler: noTurns{}, MinBackoff: time.Hour,
				AskAccept: func(_ context.Context, name, email, firm string) (bool, bool) {
					asked <- name + "|" + email + "|" + firm
					return c.accept, c.answered
				},
				ShowPaired: func(user, firm string) { paired <- user + "|" + firm }})
			if err != nil {
				t.Fatal(err)
			}
			oldKey, _ := os.ReadFile(keyPath)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { cl.Run(ctx); close(done) }()
			t.Cleanup(func() { cancel(); <-done })

			select {
			case got := <-asked:
				if want := fakerelay.UserName + "|" + fakerelay.UserEmail + "|" + fakerelay.FirmName; got != want {
					t.Fatalf("asked about %q, want %q", got, want)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("the offer was never put to the person")
			}
			var answer string
			select {
			case answer = <-r.Answers:
			case <-time.After(500 * time.Millisecond):
			}
			if answer != c.want {
				t.Fatalf("Helper answered %q, want %q", answer, c.want)
			}

			switch c.want {
			case protocol.TypePairingAccept:
				select {
				case got := <-paired:
					if got != fakerelay.UserName+"|"+fakerelay.FirmName {
						t.Fatalf("paired to %q", got)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("not paired after accept")
				}
				select {
				case <-r.Hellos:
				case <-time.After(10 * time.Second):
					t.Fatal("no hello after paired")
				}
				if rec, _ := pairing.Load(pairingPath); rec == nil {
					t.Fatal("pairing not saved")
				}
			case protocol.TypePairingDecline:
				for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
					if key, _ := os.ReadFile(keyPath); len(key) > 0 && !bytes.Equal(key, oldKey) {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("device key kept after a decline")
					}
				}
				fallthrough
			default:
				select {
				case <-paired:
					t.Fatal("paired without an accept")
				case <-r.Hellos:
					t.Fatal("hello without an accept")
				default:
				}
				if rec, _ := pairing.Load(pairingPath); rec != nil {
					t.Fatal("pairing saved without an accept")
				}
			}
		})
	}
}

func TestRelayErrorCode(t *testing.T) {
	for msg, want := range map[string]string{
		`{"type":"error","code":"bad_auth"}`:                    "relay_error_bad_auth",
		`{"type":"error","code":"Key sk-ant-x is not allowed"}`: "bad_relay_error",
		`{"type":"error","code":"a/b"}`:                         "bad_relay_error",
		`{"type":"error"}`:                                      "bad_relay_error",
	} {
		if got := string(relayError([]byte(msg))); got != want {
			t.Errorf("%s: %q, want %q", msg, got, want)
		}
	}
}

func TestDisplayName(t *testing.T) {
	for in, want := range map[string]string{
		"  Synthetic Firm LLP ": "Synthetic Firm LLP",
		"Evil\x1b[2J‮Firm\r\n":  "Evil[2JFirm",
		"Grün & Söhne 🏢":        "Grün & Söhne 🏢",
	} {
		if got, ok := displayName(in); !ok || got != want {
			t.Errorf("displayName(%q) = %q, %v", in, got, ok)
		}
	}
	for _, bad := range []string{"", " ‮\x00 ", strings.Repeat("x", maxDisplayName+1), "\xff"} {
		if _, ok := displayName(bad); ok {
			t.Errorf("displayName(%q) accepted", bad)
		}
	}
}

func TestCheckURLCanonical(t *testing.T) {
	for _, ok := range []string{"wss://relay.prod.firmary.app/", "wss://relay.example:8443/", "ws://127.0.0.1:4000/"} {
		if err := CheckURL(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"wss://relay.prod.firmary.app", "wss://Relay.prod.firmary.app/", "WSS://relay.prod.firmary.app/",
		"wss://relay.prod.firmary.app:443/", "wss://relay.prod.firmary.app./", "wss://relay.prod.firmary.app/x",
		"wss://relay.prod.firmary.app/?", "wss://relay.prod.firmary.app/#f", "wss://u@relay.prod.firmary.app/",
		"ws://relay.prod.firmary.app/", "https://relay.prod.firmary.app/"} {
		if err := CheckURL(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}
