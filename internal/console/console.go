// Package console prints what the Link tells the person running it. It follows the approved design (design
// studio 5877e3252806, "Always-on server" screen) except where the design's pairing flow differs from the secure
// one the Link actually runs:
//
//   - In the design, Firmary makes the code, the install line carries it, and the server prints "the code it was
//     given". Here the Link works the code out with Firmary's relay after it connects (commit-then-reveal, see
//     package pairing). Nobody types, pastes or puts a code on a command line. Firmary shows the same code next to
//     the computer that asked, and the person pairs only if the two match.
//   - The code is new on every connection, so after a reconnect the Link says the code changed.
//   - The code is printed only to a terminal, never to a service log such as journald, where other admins of the
//     computer could read it.
//   - Confirming the code in Firmary doesn't pair by itself (security re-review M3: a code read out to someone else
//     must not bind this computer to them). Firmary then asks this computer, which names the person, their email
//     and the firm, and pairs only on "yes" typed here. Anything else declines. With no terminal to ask, the Link
//     neither accepts nor declines, and says how to answer.
//   - "Paired to" names the person and the firm. The Link then runs only turns whose tickets Firmary signed for
//     that person, and the screen says so. Like the code, the names are printed only to a terminal.
//
// Nothing printed here goes to the log: pairing codes and names are for the person's eyes only.
package console

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Firmary/bridge/internal/brand"
)

// Printer writes the Link's messages to one output.
type Printer struct {
	W io.Writer
	// Terminal is true when W is a terminal a person is watching. The pairing code is printed only then.
	Terminal bool

	// In delivers lines typed on the terminal (see Lines); nil when stdin isn't a terminal. A pairing offer is
	// accepted only with a yes typed here.
	In <-chan string

	codes  int // pairing codes shown since the last pairing
	offers int // pairing offers nobody could be asked about
}

// Lines reads r line by line, for Printer.In. The channel closes at end of input.
func Lines(r io.Reader) <-chan string {
	ch := make(chan string)
	go func() {
		defer close(ch)
		for s := bufio.NewScanner(r); s.Scan(); {
			ch <- s.Text()
		}
	}()
	return ch
}

// IsTerminal reports whether f is a terminal. Under systemd, stdout is a journald socket, so it is not.
func IsTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func (p *Printer) ok(msg string) { fmt.Fprintf(p.W, "ok  %s\n", msg) }

// Banner names the Link, its version and the computer it runs on, as "<brand.Name> 1.4.2 for Linux (x86_64)".
func (p *Printer) Banner(version, goos, goarch string) {
	fmt.Fprintf(p.W, "%s %s for %s (%s)\n", brand.Name, version, osName(goos), archName(goarch))
}

// ProviderReady says the Link's own copy of a provider app is set up.
func (p *Printer) ProviderReady(app string) { p.ok(app + " is ready") }

// ProviderFailed says a provider app could not be set up. err must not carry secrets; Ensure's errors don't.
func (p *Printer) ProviderFailed(app string, err error) {
	fmt.Fprintf(p.W, "%s couldn't be set up: %v\n", app, err)
}

// Reached says the Link got through to Firmary. The connection is outgoing only.
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

// ConfirmPairing names the person and firm a pairing offer would pair this computer to and asks for "yes" (or
// "y"); any other line declines. It doesn't answer (answered is false) when there is no terminal to show the names
// and read the answer on, when input has ended, or when ctx ends first.
func (p *Printer) ConfirmPairing(ctx context.Context, personName, personEmail, firmName string) (accept, answered bool) {
	if !p.Terminal || p.In == nil {
		if p.offers == 0 {
			fmt.Fprintf(p.W, "Someone paired this computer in Firmary. It pairs only after a yes typed on this computer, "+
				"and the names are shown only in a terminal.\nTo answer, stop the %s and start it in a terminal.\n", brand.Name)
		}
		p.offers++
		return false, false
	}
	// Anything typed before the question is not an answer to it.
drain:
	for {
		select {
		case _, ok := <-p.In:
			if !ok {
				break drain
			}
		default:
			break drain
		}
	}
	fmt.Fprintf(p.W, "\nFirmary asks to pair this computer to:\n  %s (%s)\n  %s\n"+
		"Pair only if this is you and your firm. If you didn't just pair it in Firmary yourself, decline.\n"+
		"Type yes to pair, or anything else to decline: ", personName, personEmail, firmName)
	select {
	case line, ok := <-p.In:
		if !ok {
			fmt.Fprintln(p.W, "\nNo answer. If the pairing is still open, Firmary asks again when the "+brand.Name+" reconnects.")
			return false, false
		}
		if a := strings.ToLower(strings.TrimSpace(line)); a == "yes" || a == "y" {
			return true, true
		}
		p.codes = 0
		fmt.Fprintln(p.W, "Declined. This computer isn't paired, and Firmary cancels this pairing.")
		return false, true
	case <-ctx.Done():
		fmt.Fprintln(p.W, "\nThe question ended before you answered. If the pairing is still open, Firmary asks again when the "+brand.Name+" reconnects.")
		return false, false
	}
}

// Paired names the person and firm this Link is now paired to, so someone who paired to the wrong account can
// notice and stop it. When W is not a terminal, the names are left out.
func (p *Printer) Paired(personName, firmName string) {
	p.codes, p.offers = 0, 0
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

// SigningIn starts a provider sign-in, in the design's words ("Always-on server", step 4).
func (p *Printer) SigningIn(app, account string) {
	if app == "Codex" {
		fmt.Fprintf(p.W, "Signing in to %s with %s.\n", app, account)
		return
	}
	fmt.Fprintf(p.W, "Signing in to %s.\n", app)
}

// SignInLink shows the provider's own sign-in link and, for a device link, its one-time code. They are shown to the
// person only and never logged.
func (p *Printer) SignInLink(url, code string, paste bool) {
	fmt.Fprintf(p.W, "Open this link on any browser:\n  %s\n", url)
	switch {
	case code != "":
		fmt.Fprintf(p.W, "And enter this code:\n  %s\nWaiting for sign-in…\n", code)
	case paste:
		fmt.Fprintln(p.W, "Sign in there and press Authorize. The page then shows a code.")
	default:
		fmt.Fprintln(p.W, "Waiting for sign-in…")
	}
}

// AskCode asks for the code the provider's page showed (Claude's sign-in on a computer without a browser).
func (p *Printer) AskCode(again bool) {
	if again {
		fmt.Fprint(p.W, "That isn't the code from the page. Copy the whole code and paste it here: ")
		return
	}
	fmt.Fprint(p.W, "Paste that code here and press Return: ")
}

// SignedIn says the provider sign-in finished, naming the plan (never the email: the Link doesn't read it).
func (p *Printer) SignedIn(account, plan string) {
	if plan != "" {
		p.ok(fmt.Sprintf("Signed in to %s (%s plan)", account, PlanName(plan)))
		return
	}
	p.ok("Signed in to " + account)
}

// PlanName is how a provider's plan type reads to a person.
func PlanName(plan string) string {
	switch plan {
	case "pro":
		return "Pro"
	case "max":
		return "Max"
	case "team":
		return "Team"
	case "business":
		return "Business"
	case "enterprise":
		return "Enterprise"
	case "plus":
		return "Plus"
	case "free":
		return "Free"
	}
	return plan
}

// ServiceInstalled says the Link now starts with the computer.
func (p *Printer) ServiceInstalled(server bool) {
	if server {
		p.ok("Starts with this server, keeps running after you log out")
		return
	}
	p.ok("Starts when you log in")
}
