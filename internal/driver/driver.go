// Package driver defines what the Helper needs from a provider app (Claude Code now, Codex later).
package driver

import (
	"context"

	"github.com/Firmary/firmary-helper/internal/protocol"
)

// Pin is the exact provider app version Firmary's signed manifest names, with the SHA-256 of the binary per
// platform (for example "linux-x64"). Ensure refuses a platform the pin has no checksum for.
type Pin struct {
	Version string
	SHA256  map[string]string
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
	Org         string // provider organisation name, if any
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

// Driver is one provider app. SignIn and SignOut arrive in a later slice.
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
