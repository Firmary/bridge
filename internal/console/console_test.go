package console

import (
	"bytes"
	"errors"
	"testing"
)

// The server transcript from the design's "Always-on server" screen, for the lines the Helper prints today.
func TestServerTranscriptMatchesDesign(t *testing.T) {
	var b bytes.Buffer
	p := Printer{W: &b}
	p.Banner("1.4.2", "linux", "amd64")
	p.ProviderReady("Claude Code")
	p.Reached()
	p.PairingCode("HTRW-KQPN")
	p.Paired("Harlow & Pike CPA")

	want := "Firmary Helper 1.4.2 for Linux (x86_64)\n" +
		"ok  Claude Code is ready\n" +
		"ok  Reached Firmary (outgoing only, nothing to open)\n" +
		"Confirm this code in Firmary:\n" +
		"  HTRW-KQPN\n" +
		"Waiting for you to confirm…\n" +
		"ok  Paired to Harlow & Pike CPA\n" +
		"Not your firm? Stop the Firmary Helper and unpair it in Firmary.\n"
	if b.String() != want {
		t.Fatalf("transcript:\n%s\nwant:\n%s", b.String(), want)
	}
}

func TestBannerNamesEachDesktop(t *testing.T) {
	for _, c := range []struct{ goos, goarch, want string }{
		{"darwin", "arm64", "Firmary Helper 1.0.0 for macOS (arm64)\n"},
		{"windows", "amd64", "Firmary Helper 1.0.0 for Windows (x86_64)\n"},
	} {
		var b bytes.Buffer
		Printer{W: &b}.Banner("1.0.0", c.goos, c.goarch)
		if b.String() != c.want {
			t.Errorf("Banner(%s, %s) = %q, want %q", c.goos, c.goarch, b.String(), c.want)
		}
	}
}

func TestProviderFailed(t *testing.T) {
	var b bytes.Buffer
	Printer{W: &b}.ProviderFailed("Claude Code", errors.New("download failed"))
	if got, want := b.String(), "Claude Code couldn't be set up: download failed\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
