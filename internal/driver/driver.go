// Package driver defines what the Helper needs from a provider app (Claude Code, Codex).
package driver

import (
	"context"

	"github.com/Firmary/bridge/internal/protocol"
)

// Pin is the exact provider app version Firmary's signed manifest names, with the SHA-256 of the binary per
// platform (for example "linux-x64"). Ensure refuses a platform the pin has no checksum for.
type Pin struct {
	Version string
	SHA256  map[string]string
	// ArchiveSHA256 is, for a provider that ships its binary inside an archive (Codex), the SHA-256 of that release
	// archive per platform, as the provider publishes it. Both it and SHA256 must match.
	ArchiveSHA256 map[string]string
}

// Profile is one Firmary user's provider profile on this device. Dir is owned by the Helper; the user's own
// provider install and login are never used.
type Profile struct {
	UserID string
	Dir    string
}

// ProviderStatus is what the Helper may report about a provider login. It never includes the account email.
type ProviderStatus struct {
	Installed   string // installed pinned version, "" if not installed
	SignedIn    bool   // signed in with a subscription plan (not an API key)
	AccountType string // provider's plan type, e.g. "pro", "max", "team", "enterprise"
	OrgID       string // provider organisation id (a UUID), if any
	Managed     bool   // a machine-wide managed settings source for the provider was found (IT policy applies)
}

// Turn is what a driver runs: already checked against its ticket.
type Turn struct {
	ID              string
	Model           string
	Context         string
	Prompt          string
	MaxOutputTokens int
}

// Failure is a classified run failure: an error class and a pattern name. It never carries provider text.
type Failure struct {
	Class   string
	Pattern string
}

func (f *Failure) Error() string { return f.Class + ": " + f.Pattern }

// Driver is one provider app.
type Driver interface {
	ID() string
	// Ensure installs and verifies the pinned copy. It is idempotent.
	Ensure(ctx context.Context, pin Pin) error
	// Status reports the profile's login state.
	Status(ctx context.Context, p Profile) (ProviderStatus, error)
	// Run runs one turn, emitting events. It always ends by emitting exactly one done or error event, and returns
	// a *Failure matching the error event, or nil after done.
	Run(ctx context.Context, p Profile, t Turn, emit func(protocol.StreamEvent)) error
}

// Sign-in kinds, as in the relay's signin_step message.
const (
	SignInBrowser    = "browser"
	SignInDeviceLink = "device_link"
)

// SignInStep is what the person needs to finish a provider's own sign-in: the provider's link and, for a device
// link, its one-time code. It is shown to that person only and never logged.
type SignInStep struct {
	Kind     string
	URL      string
	UserCode string
}

// SignInDriver is a Driver that can run the provider's own sign-in in a profile (Codex now; Claude Code in a later
// slice). SignIn blocks until the sign-in finishes, fails, or ctx ends, calling step once the link is known.
type SignInDriver interface {
	Driver
	SignIn(ctx context.Context, p Profile, kind string, step func(SignInStep)) error
	SignOut(ctx context.Context, p Profile) error
}
