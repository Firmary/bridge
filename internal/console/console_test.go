package console

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

// The server transcript: the design's "Always-on server" screen. The code is the one Firmary showed (pairing v2),
// the Link names who asked for it, and "Paired to" names the person.
func TestServerTranscript(t *testing.T) {
	var b bytes.Buffer
	p := &Printer{W: &b, Terminal: true}
	p.Banner("1.4.2", "linux", "amd64")
	p.ProviderReady("Claude Code")
	p.Reached()
	p.Claimed("Maria Okafor", "Harlow & Pike CPA", "HTRW-KQPN")
	p.Paired("Maria Okafor", "Harlow & Pike CPA")
	p.SignedIn("Claude", "team", "maria@harlowpike.example")

	want := "Firmary Link 1.4.2 for Linux (x86_64)\n" +
		"ok  Claude Code is ready\n" +
		"ok  Reached Firmary (outgoing only, nothing to open)\n" +
		"ok  Connecting to Firmary as Maria Okafor · Harlow & Pike CPA\n" +
		"Confirm this code in Firmary:\n" +
		"  HTRW-KQPN\n" +
		"Waiting for you to confirm…\n" +
		"ok  Paired to Maria Okafor · Harlow & Pike CPA\n" +
		"This computer now runs only requests Firmary signs for Maria Okafor.\n" +
		"Not you? Stop the Firmary Link now.\n" +
		"ok  Signed in to Claude as maria@harlowpike.example (Team plan)\n"
	if b.String() != want {
		t.Fatalf("transcript:\n%s\nwant:\n%s", b.String(), want)
	}
}

// Off a terminal (under systemd stdout goes to journald) the names are not printed, and the hint is printed once.
func TestNamesOnlyOnATerminal(t *testing.T) {
	var b bytes.Buffer
	p := &Printer{W: &b}
	p.NeedsCode()
	p.NeedsCode()
	p.Paired("Maria Okafor", "Harlow & Pike CPA")
	want := "This computer isn't paired yet. In Firmary, open Settings › Connections and add this computer to get a " +
		"pairing code, then run:\n    firmary-link pair --code ABCD-EFGH\n" +
		"ok  Paired (names are shown only in a terminal, never in system logs)\n"
	if b.String() != want {
		t.Fatalf("got:\n%s\nwant:\n%s", b.String(), want)
	}
}

// answerOnPrompt types answer once the pairing question has been printed.
type answerOnPrompt struct {
	bytes.Buffer
	in     chan string
	answer string
}

func (w *answerOnPrompt) Write(b []byte) (int, error) {
	if bytes.Contains(b, []byte("Type yes")) {
		w.in <- w.answer
	}
	return w.Buffer.Write(b)
}

// The accept step pairs only on a yes typed after the question; anything else, including a yes typed before it,
// declines. Off a terminal it doesn't answer and doesn't print the names.
func TestConfirmPairing(t *testing.T) {
	for _, c := range []struct {
		typeahead, answer string
		accept            bool
	}{
		{"", " YES ", true},
		{"", "y", true},
		{"yes", "", false},
		{"", "no", false},
	} {
		w := &answerOnPrompt{in: make(chan string, 2), answer: c.answer}
		if c.typeahead != "" {
			w.in <- c.typeahead
		}
		p := &Printer{W: w, Terminal: true, In: w.in}
		accept, answered := p.ConfirmPairing(context.Background(), "Maria Okafor", "maria@example.com", "Harlow & Pike CPA")
		if accept != c.accept || !answered {
			t.Errorf("typeahead %q, answer %q: accept %v, answered %v", c.typeahead, c.answer, accept, answered)
		}
		want := "\nFirmary asks to pair this computer to:\n  Maria Okafor (maria@example.com)\n  Harlow & Pike CPA\n" +
			"Pair only if this is you and your firm. If you didn't just pair it in Firmary yourself, decline.\n" +
			"Type yes to pair, or anything else to decline: "
		if !c.accept {
			want += "Declined. This computer isn't paired, and Firmary cancels this pairing.\n"
		}
		if w.String() != want {
			t.Errorf("answer %q printed:\n%s\nwant:\n%s", c.answer, w.String(), want)
		}
	}

	var b bytes.Buffer
	p := &Printer{W: &b, In: make(chan string)}
	for range 2 {
		if accept, answered := p.ConfirmPairing(context.Background(), "Maria Okafor", "maria@example.com", "Harlow & Pike CPA"); accept || answered {
			t.Fatalf("off a terminal: accept %v, answered %v", accept, answered)
		}
	}
	want := "Someone paired this computer in Firmary. It pairs only after a yes typed on this computer, and the names " +
		"are shown only in a terminal.\nTo answer, stop the Firmary Link and start it in a terminal.\n"
	if b.String() != want {
		t.Fatalf("off a terminal printed:\n%s\nwant:\n%s", b.String(), want)
	}
}

func TestBannerNamesEachDesktop(t *testing.T) {
	for _, c := range []struct{ goos, goarch, want string }{
		{"darwin", "arm64", "Firmary Link 1.0.0 for macOS (arm64)\n"},
		{"windows", "amd64", "Firmary Link 1.0.0 for Windows (x86_64)\n"},
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
