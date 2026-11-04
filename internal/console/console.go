// Package console prints what the Link tells the person running it. It follows the approved design (design
// studio 5877e3252806, "Always-on server" screen): Firmary shows a pairing code, the install line or
// `firmary-link pair` carries it (or the person types it at the prompt), the Link prints it with "Confirm this code
// in Firmary" and pairs once the person confirms there (pairing v2, see package relay). Where it adds to the design:
//
//   - Before the code, the Link names who asked Firmary for it ("Connecting to Firmary as <person> · <firm>"), so a
//     code read out by someone else is noticed. Typing the code here is the computer's own yes (security re-review
//     M3); if the binding Firmary then offers names anyone else, the Link asks again and pairs only on "yes" typed
//     here.
//   - "Paired to" names the person and the firm. The Link then runs only turns whose tickets Firmary signed for
//     that person, and the screen says so. The names are printed only to a terminal, never to a service log such as
//     journald, where other admins of the computer could read them.
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

	needs  int // times the Link said it waits for a code, since the last pairing
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

// NeedsCode says the Link isn't paired and waits for the code Firmary shows. It is said once until the next pairing.
func (p *Printer) NeedsCode() {
	if p.needs++; p.needs > 1 {
		return
	}
	fmt.Fprintf(p.W, "This computer isn't paired yet. In Firmary, open Settings › Connections and add this computer "+
		"to get a pairing code, then run:\n    %s pair --code ABCD-EFGH\n", brand.Command)
}

// AskPairingCode asks for the pairing code Firmary shows (`firmary-link pair` on a terminal). again says the last answer
// wasn't a code.
func (p *Printer) AskPairingCode(again bool) {
	if again {
		fmt.Fprint(p.W, "That isn't a pairing code. It has eight letters and digits, like ABCD-EFGH: ")
		return
	}
	fmt.Fprint(p.W, "Enter the pairing code Firmary shows (Settings › Connections): ")
}

// Claimed says Firmary knows the code and who asked for it, prints the code to confirm, and waits. The names are
// printed only to a terminal.
func (p *Printer) Claimed(personName, firmName, code string) {
	if p.Terminal {
		p.ok("Connecting to Firmary as " + personName + " · " + firmName)
	}
	fmt.Fprintf(p.W, "Confirm this code in Firmary:\n  %s\nWaiting for you to confirm…\n", code)
}

// CodeRejected says Firmary refused the code: reason is "expired", "not_found" or another machine name.
func (p *Printer) CodeRejected(reason string) {
	p.needs = 1 // what to do next is said here
	if reason == "expired" {
		fmt.Fprintln(p.W, "That code has expired. Show a new code in Firmary and enter it here.")
		return
	}
	fmt.Fprintln(p.W, "That code didn't work. Show a new code in Firmary and enter it here.")
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
	p.needs, p.offers = 0, 0
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

// SignedIn says the provider sign-in finished, naming the account's email (when the provider reports it) and plan.
func (p *Printer) SignedIn(account, plan, email string) {
	msg := "Signed in to " + account
	if email != "" {
		msg += " as " + email
	}
	if plan != "" {
		msg += " (" + PlanName(plan) + " plan)"
	}
	p.ok(msg)
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
