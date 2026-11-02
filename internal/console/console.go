// Package console prints what the Helper tells the person running it, in the words of the approved design
// (design studio 5877e3252806, "Always-on server" screen). It is the only terminal the design shows, so the copy
// is plain and each finished step starts with "ok".
//
// Nothing printed here goes to the log: pairing codes and firm names are for the person's eyes only.
package console

import (
	"fmt"
	"io"

	"github.com/Firmary/bridge/internal/brand"
)

// Printer writes the Helper's messages to one terminal.
type Printer struct{ W io.Writer }

func (p Printer) ok(msg string) { fmt.Fprintf(p.W, "ok  %s\n", msg) }

// Banner names the Helper, its version and the computer it runs on, as "<brand.Name> 1.4.2 for Linux (x86_64)".
func (p Printer) Banner(version, goos, goarch string) {
	fmt.Fprintf(p.W, "%s %s for %s (%s)\n", brand.Name, version, osName(goos), archName(goarch))
}

// ProviderReady says the Helper's own copy of a provider app is set up.
func (p Printer) ProviderReady(app string) { p.ok(app + " is ready") }

// ProviderFailed says a provider app could not be set up. err must not carry secrets; Ensure's errors don't.
func (p Printer) ProviderFailed(app string, err error) {
	fmt.Fprintf(p.W, "%s couldn't be set up: %v\n", app, err)
}

// Reached says the Helper got through to Firmary. The connection is outgoing only.
func (p Printer) Reached() { p.ok("Reached Firmary (outgoing only, nothing to open)") }

// PairingCode shows the code the person confirms in Firmary.
func (p Printer) PairingCode(code string) {
	fmt.Fprintf(p.W, "Confirm this code in Firmary:\n  %s\nWaiting for you to confirm…\n", code)
}

// Paired names the firm this Helper is now paired to, so a person who paired to the wrong one can notice.
func (p Printer) Paired(firmName string) {
	p.ok("Paired to " + firmName)
	fmt.Fprintln(p.W, "Not your firm? Stop the "+brand.Name+" and unpair it in Firmary.")
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
