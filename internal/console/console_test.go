package console

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// The server transcript: the design's "Always-on server" screen, changed to the secure pairing flow. The code is
// one the Helper derived (the shared test vector), not one it was given, and "Paired to" names the person.
func TestServerTranscript(t *testing.T) {
	var b bytes.Buffer
	p := &Printer{W: &b, Terminal: true}
	p.Banner("1.4.2", "linux", "amd64")
	p.ProviderReady("Claude Code")
	p.Reached()
	p.PairingCode("WFWC-XCK4")
	p.Paired("Maria Okafor", "Harlow & Pike CPA")

	want := "Firmary Helper 1.4.2 for Linux (x86_64)\n" +
		"ok  Claude Code is ready\n" +
		"ok  Reached Firmary (outgoing only, nothing to open)\n" +
		"\n" +
		"Pairing code:\n" +
		"  WFWC-XCK4\n" +
		"Check that Firmary shows the same code, then pair there.\n" +
		"If the codes don't match, don't pair. You never type this code.\n" +
		"Waiting for you to confirm in Firmary…\n" +
		"ok  Paired to Maria Okafor · Harlow & Pike CPA\n" +
		"This computer now runs only requests Firmary signs for Maria Okafor.\n" +
		"Not you? Stop the Firmary Helper now.\n"
	if b.String() != want {
		t.Fatalf("transcript:\n%s\nwant:\n%s", b.String(), want)
	}
}

// Each connection derives a new code, so a reconnect while waiting says the code changed. After a pairing, the next
// code (a new pairing after an unpair) is introduced in full again.
func TestPairingCodeAfterReconnect(t *testing.T) {
	var b bytes.Buffer
	p := &Printer{W: &b, Terminal: true}
	p.PairingCode("WFWC-XCK4")
	b.Reset()
	p.PairingCode("YM1S-0VXJ")
	want := "\nThe connection restarted, so the code changed:\n  YM1S-0VXJ\n" +
		"Use this one. Waiting for you to confirm in Firmary…\n"
	if b.String() != want {
		t.Fatalf("got:\n%s\nwant:\n%s", b.String(), want)
	}
	p.Paired("Maria Okafor", "Harlow & Pike CPA")
	b.Reset()
	p.PairingCode("WFWC-XCK4")
	if !strings.HasPrefix(b.String(), "\nPairing code:\n") {
		t.Fatalf("after a pairing, got:\n%s", b.String())
	}
}

// Off a terminal (under systemd stdout goes to journald) neither the code nor the names are printed, and the hint is
// printed once.
func TestCodeAndNamesOnlyOnATerminal(t *testing.T) {
	var b bytes.Buffer
	p := &Printer{W: &b}
	p.PairingCode("WFWC-XCK4")
	p.PairingCode("YM1S-0VXJ")
	p.Paired("Maria Okafor", "Harlow & Pike CPA")
	want := "This computer isn't paired yet. The pairing code is shown only in a terminal, never in system logs.\n" +
		"To pair, stop the Firmary Helper and start it in a terminal.\n" +
		"ok  Paired (names are shown only in a terminal, never in system logs)\n"
	if b.String() != want {
		t.Fatalf("got:\n%s\nwant:\n%s", b.String(), want)
	}
}

func TestBannerNamesEachDesktop(t *testing.T) {
	for _, c := range []struct{ goos, goarch, want string }{
		{"darwin", "arm64", "Firmary Helper 1.0.0 for macOS (arm64)\n"},
		{"windows", "amd64", "Firmary Helper 1.0.0 for Windows (x86_64)\n"},
	} {
		var b bytes.Buffer
		(&Printer{W: &b}).Banner("1.0.0", c.goos, c.goarch)
		if b.String() != c.want {
			t.Errorf("Banner(%s, %s) = %q, want %q", c.goos, c.goarch, b.String(), c.want)
		}
	}
}

func TestProviderFailed(t *testing.T) {
	var b bytes.Buffer
	(&Printer{W: &b}).ProviderFailed("Claude Code", errors.New("download failed"))
	if got, want := b.String(), "Claude Code couldn't be set up: download failed\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
