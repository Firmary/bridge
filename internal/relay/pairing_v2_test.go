//go:build !windows

package relay

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Firmary/bridge/internal/fakerelay"
	"github.com/Firmary/bridge/internal/pairing"
	"github.com/Firmary/bridge/internal/protocol"
)

// v2Link is a Link in pairing v2 mode against a fake relay, with what it showed the person.
type v2Link struct {
	c                          *Client
	pairingPath                string
	needsCode, claimed, paired chan string
	rejected, asked            chan string
}

func startV2(t *testing.T, r *fakerelay.Relay, code string, opts ...func(*Config)) *v2Link {
	t.Helper()
	dir := t.TempDir()
	l := &v2Link{pairingPath: filepath.Join(dir, "pairing.json"), needsCode: make(chan string, 8), claimed: make(chan string, 8),
		paired: make(chan string, 8), rejected: make(chan string, 8), asked: make(chan string, 8)}
	cfg := Config{URL: r.URL, Env: "local", KeyPath: filepath.Join(dir, "device.key"), PairingPath: l.pairingPath,
		HelperVersion: "0.0.0-test", Handler: noTurns{}, MinBackoff: time.Hour, TypedCode: true, Code: code,
		Device:           &protocol.PairingDevice{Name: "homelab", OS: "linux", Arch: "x64", Kind: protocol.KindServer},
		ShowNeedsCode:    func() { l.needsCode <- "" },
		ShowClaimed:      func(person, firm, code string) { l.claimed <- person + "|" + firm + "|" + code },
		ShowCodeRejected: func(reason string) { l.rejected <- reason },
		ShowPaired:       func(user, firm string) { l.paired <- user + "|" + firm },
		ShowPairingCode:  func(code string) { t.Errorf("v2 showed a Link-derived code %s", code) },
		AskAccept: func(_ context.Context, name, _, firm string) (bool, bool) {
			l.asked <- name + "|" + firm
			return false, false
		}}
	for _, o := range opts {
		o(&cfg)
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	l.c = c
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return l
}

func got[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
	var zero T
	return zero
}

func none[T any](t *testing.T, ch <-chan T, what string) {
	t.Helper()
	select {
	case v := <-ch:
		t.Fatalf("unexpected %s: %v", what, v)
	case <-time.After(300 * time.Millisecond):
	}
}

func newRelay(t *testing.T) *fakerelay.Relay {
	t.Helper()
	r, err := fakerelay.Start("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	return r
}

// TestPairingV2TypedCode: the code the person typed goes out in pairing_reveal, normalised, with the computer's
// kind; the Link shows who asked for it; and when the person confirms in Firmary, the offer names the same person
// and firm, so the Link accepts on its own and pairs.
func TestPairingV2TypedCode(t *testing.T) {
	r := newRelay(t)
	code := r.Issue("Maria Okafor", "Harlow & Pike CPA")
	typed := " " + code[:2] + " " + code[2:] + " " // as typed: spaces, the dash where it falls
	norm, err := pairing.NormalizeCode(typed)
	if err != nil || norm != code {
		t.Fatalf("NormalizeCode(%q) = %q, %v", typed, norm, err)
	}
	l := startV2(t, r, norm)

	reveal := got(t, r.Reveals, "pairing_reveal")
	if reveal.Code != code || reveal.Device == nil || reveal.Device.Kind != protocol.KindServer {
		t.Fatalf("reveal %+v, want code %s and kind server", reveal, code)
	}
	if want := "Maria Okafor|Harlow & Pike CPA|" + code; got(t, l.claimed, "claimed") != want {
		t.Fatal("claimed names or code differ")
	}
	r.Confirm(got(t, r.Codes, "pending code"))
	if a := got(t, r.Answers, "answer"); a != protocol.TypePairingAccept {
		t.Fatalf("answered %q, want pairing_accept", a)
	}
	if p := got(t, l.paired, "paired"); p != "Maria Okafor|Harlow & Pike CPA" {
		t.Fatalf("paired to %q", p)
	}
	got(t, r.Hellos, "hello")
	none(t, l.asked, "accept prompt")
	if rec, _ := pairing.Load(l.pairingPath); rec == nil {
		t.Fatal("pairing not saved")
	}
}

// TestPairingV2OfferMismatch: an offer naming someone other than pairing_claimed did is never accepted on its own;
// the Link falls back to asking the person (here nobody answers, so nothing is sent).
func TestPairingV2OfferMismatch(t *testing.T) {
	r := newRelay(t)
	code := r.Issue("Maria Okafor", "Harlow & Pike CPA")
	l := startV2(t, r, code)
	got(t, l.claimed, "claimed")
	r.ConfirmAs(got(t, r.Codes, "pending code"), "Someone Else", "Harlow & Pike CPA")
	if a := got(t, l.asked, "accept prompt"); a != "Someone Else|Harlow & Pike CPA" {
		t.Fatalf("asked about %q", a)
	}
	none(t, r.Answers, "answer")
	none(t, l.paired, "paired")
}

// TestPairingV2Cancel: Cancel after pairing_claimed closes the connection and forgets the code. The Link waits for
// a new code instead of reconnecting, and the relay never gets an answer.
func TestPairingV2Cancel(t *testing.T) {
	r := newRelay(t)
	l := startV2(t, r, r.Issue("Maria Okafor", "Harlow & Pike CPA"))
	got(t, l.claimed, "claimed")
	pending := got(t, r.Codes, "pending code")
	l.c.CancelPairing()
	got(t, l.needsCode, "waiting for a new code")
	// The pending code went with the connection: confirming it now offers nothing.
	for deadline := time.Now().Add(5 * time.Second); r.Pending(pending); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the connection stayed open after Cancel")
		}
	}
	if r.Confirm(pending) {
		t.Fatal("confirming after Cancel still offered a binding")
	}
	none(t, r.Answers, "answer")
	l.c.mu.Lock()
	code := l.c.code
	l.c.mu.Unlock()
	if code != "" {
		t.Fatal("the code was kept after Cancel")
	}
	if n := r.ConnectCount(); n != 1 {
		t.Fatalf("%d connections, want 1: the Link reconnected after Cancel", n)
	}
}

// TestPairingV2NoCode: unpaired and with no code, the Link doesn't connect; a code entered later starts pairing.
func TestPairingV2NoCode(t *testing.T) {
	r := newRelay(t)
	l := startV2(t, r, "")
	got(t, l.needsCode, "waiting for a code")
	none(t, r.Reveals, "reveal")
	if err := l.c.SetCode("R7KD-4MP"); err == nil {
		t.Fatal("SetCode accepted a malformed code")
	}
	code := r.Issue("Maria Okafor", "Harlow & Pike CPA")
	if err := l.c.SetCode(code); err != nil {
		t.Fatal(err)
	}
	if reveal := got(t, r.Reveals, "pairing_reveal"); reveal.Code != code {
		t.Fatalf("revealed code %q, want %q", reveal.Code, code)
	}
	got(t, l.claimed, "claimed")
}

// TestPairingV2CodeRefused: a code the relay doesn't know, or one that expired, is forgotten and the Link waits for
// a new one.
func TestPairingV2CodeRefused(t *testing.T) {
	for _, c := range []struct{ name, want string }{{"not found", "not_found"}, {"expired", "expired"}} {
		t.Run(c.name, func(t *testing.T) {
			r := newRelay(t)
			code := r.Issue("Maria Okafor", "Harlow & Pike CPA")
			if c.want == "expired" {
				r.Expire(code)
			} else {
				code = "0000-0000"
			}
			l := startV2(t, r, code)
			if reason := got(t, l.rejected, "code refused"); reason != c.want {
				t.Fatalf("refused as %q, want %q", reason, c.want)
			}
			got(t, l.needsCode, "waiting for a new code")
			none(t, l.claimed, "claimed")
			if n := r.ConnectCount(); n != 1 {
				t.Fatalf("%d connections, want 1", n)
			}
		})
	}
}

// TestRelaySignin: Firmary's signin runs the provider sign-in, and its link and code go back as signin_step; a code
// pasted into Firmary (signin_code) reaches the sign-in, and its result goes back as signin_result.
func TestRelaySignin(t *testing.T) {
	r := newRelay(t)
	r.AutoConfirm = true
	asked := make(chan string, 1)
	startV2(t, r, r.Issue("Maria Okafor", "Harlow & Pike CPA"), func(c *Config) {
		c.SignIn = func(_ context.Context, _ pairing.Record, provider string, step func(protocol.SigninStep)) {
			asked <- provider
			step(protocol.SigninStep{Kind: "device_link", URL: "https://auth.openai.com/codex/device", UserCode: "DWKQ-MPFZ"})
		}
		c.SignInCode = func(_ pairing.Record, provider, code string, result func(protocol.SigninResult)) {
			asked <- provider + " " + code
			result(protocol.SigninResult{OK: true})
		}
	})
	got(t, r.Hellos, "hello")
	if err := r.SendRaw(protocol.Signin{Type: protocol.TypeSignin, Provider: "codex"}); err != nil {
		t.Fatal(err)
	}
	if p := got(t, asked, "sign-in"); p != "codex" {
		t.Fatalf("sign-in for %q", p)
	}
	want := protocol.SigninStep{Type: protocol.TypeSigninStep, Provider: "codex", Kind: "device_link",
		URL: "https://auth.openai.com/codex/device", UserCode: "DWKQ-MPFZ"}
	if st := got(t, r.SigninSteps, "signin_step"); st != want {
		t.Fatalf("signin_step %+v, want %+v", st, want)
	}

	if err := r.SendRaw(protocol.SigninCode{Type: protocol.TypeSigninCode, Provider: "claude-code", Code: "abc123456#def456789"}); err != nil {
		t.Fatal(err)
	}
	if p := got(t, asked, "sign-in code"); p != "claude-code abc123456#def456789" {
		t.Fatalf("code %q", p)
	}
	wantRes := protocol.SigninResult{Type: protocol.TypeSigninResult, Provider: "claude-code", OK: true}
	if res := got(t, r.SigninResults, "signin_result"); res != wantRes {
		t.Fatalf("signin_result %+v, want %+v", res, wantRes)
	}
}
