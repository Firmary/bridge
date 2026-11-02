// Package console prints what the Helper tells the person running it. It follows the approved design (design
// studio 5877e3252806, "Always-on server" screen) except where the design's pairing flow differs from the secure
// one the Helper actually runs:
//
//   - In the design, Firmary makes the code, the install line carries it, and the server prints "the code it was
//     given". Here the Helper works the code out with Firmary's relay after it connects (commit-then-reveal, see
//     package pairing). Nobody types, pastes or puts a code on a command line. Firmary shows the same code next to
//     the computer that asked, and the person pairs only if the two match.
//   - The code is new on every connection, so after a reconnect the Helper says the code changed.
//   - The code is printed only to a terminal, never to a service log such as journald, where other admins of the
//     computer could read it.
//   - "Paired to" names the person and the firm. The Helper then runs only turns whose tickets Firmary signed for
//     that person, and the screen says so. Like the code, the names are printed only to a terminal.
//
// Nothing printed here goes to the log: pairing codes and names are for the person's eyes only.
package console

import (
	"fmt"
	"io"
	"os"

	"github.com/Firmary/bridge/internal/brand"
)

// Printer writes the Helper's messages to one output.
type Printer struct {
	W io.Writer
	// Terminal is true when W is a terminal a person is watching. The pairing code is printed only then.
	Terminal bool

	codes int // pairing codes shown since the last pairing
}

// IsTerminal reports whether f is a terminal. Under systemd, stdout is a journald socket, so it is not.
func IsTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func (p *Printer) ok(msg string) { fmt.Fprintf(p.W, "ok  %s\n", msg) }

// Banner names the Helper, its version and the computer it runs on, as "<brand.Name> 1.4.2 for Linux (x86_64)".
func (p *Printer) Banner(version, goos, goarch string) {
	fmt.Fprintf(p.W, "%s %s for %s (%s)\n", brand.Name, version, osName(goos), archName(goarch))
}

// ProviderReady says the Helper's own copy of a provider app is set up.
func (p *Printer) ProviderReady(app string) { p.ok(app + " is ready") }

// ProviderFailed says a provider app could not be set up. err must not carry secrets; Ensure's errors don't.
func (p *Printer) ProviderFailed(app string, err error) {
	fmt.Fprintf(p.W, "%s couldn't be set up: %v\n", app, err)
}

// Reached says the Helper got through to Firmary. The connection is outgoing only.
func (p *Printer) Reached() { p.ok("Reached Firmary (outgoing only, nothing to open)") }

// PairingCode shows the code this connection derived. The person compares it with the one Firmary shows; it is
// never typed anywhere. A later call means the connection restarted and the code changed. When W is not a
// terminal, the code is withheld and the person is told where to see it.
func (p *Printer) PairingCode(code string) {
	defer func() { p.codes++ }()
	switch {
	case !p.Terminal:
		if p.codes == 0 {
			fmt.Fprintf(p.W, "This computer isn't paired yet. The pairing code is shown only in a terminal, never in "+
				"system logs.\nTo pair, stop the %s and start it in a terminal.\n", brand.Name)
		}
	case p.codes == 0:
		fmt.Fprintf(p.W, "\nPairing code:\n  %s\n"+
			"Check that Firmary shows the same code, then pair there.\n"+
			"If the codes don't match, don't pair. You never type this code.\n"+
			"Waiting for you to confirm in Firmary…\n", code)
	default:
		fmt.Fprintf(p.W, "\nThe connection restarted, so the code changed:\n  %s\n"+
			"Use this one. Waiting for you to confirm in Firmary…\n", code)
	}
}

// Paired names the person and firm this Helper is now paired to, so someone who paired to the wrong account can
// notice and stop it. When W is not a terminal, the names are left out.
func (p *Printer) Paired(personName, firmName string) {
	p.codes = 0
	if !p.Terminal {
		p.ok("Paired (names are shown only in a terminal, never in system logs)")
		return
	}
	p.ok("Paired to " + personName + " · " + firmName)
	fmt.Fprintln(p.W, "This computer now runs only requests Firmary signs for "+personName+".")
	fmt.Fprintln(p.W, "Not you? Stop the "+brand.Name+" now.")
}

func osName(goos string) string {
	switch goos {
	case "darwin":
		return "macOS"
	case "windows":
		return "Windows"
	case "linux":
		return "Linux"
	}
	return goos
}

func archName(goarch string) string {
	if goarch == "amd64" {
		return "x86_64"
	}
	return goarch
}
