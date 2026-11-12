// Package driver defines what the Link needs from a provider app (Claude Code, Codex).
package driver

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Firmary/bridge/internal/launch"
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

// Profile is one Firmary user's provider profile on this device. Dir is owned by the Link; the user's own
// provider install and login are never used.
type Profile struct {
	UserID string
	Dir    string
}

// ProviderStatus is what the Link may report about a provider login.
type ProviderStatus struct {
	Installed   string // installed pinned version, "" if not installed
	SignedIn    bool   // signed in with a subscription plan (not an API key)
	AccountType string // provider's plan type, e.g. "pro", "max", "team", "enterprise"
	OrgID       string // provider organisation id (a UUID), if any
	// AccountEmail is the signed-in account's email as the provider reports it (CleanEmail), for the paired person's
	// own Connections page in Firmary. Never logged; "" when unknown.
	AccountEmail string
	Managed      bool // a machine-wide managed settings source for the provider was found (IT policy applies)
}

// CleanEmail returns a provider-reported email if it looks like one hello may carry (3 to 320 characters, one @, no
// spaces or control characters), else "".
func CleanEmail(s string) string {
	if len(s) < 3 || len(s) > 320 || !utf8.ValidString(s) || strings.Count(s, "@") != 1 || strings.HasPrefix(s, "@") ||
		strings.HasSuffix(s, "@") {
		return ""
	}
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) {
			return ""
		}
	}
	return s
}

// Turn is what a driver runs: already checked against its ticket.
type Turn struct {
	ID              string
	Model           string
	Context         string
	Prompt          string
	MaxOutputTokens int
	// Launch is the turn's checked launch profile. Each driver maps it to its own fixed flags or config.
	Launch launch.Profile
	// Firmary is the checked credential for Firmary's own MCP server, set exactly when Launch turns the firmary tool
	// on (launch.TurnFirmary). A driver passes it to its provider app only through a private file or that app's own
	// environment, never on a command line, and never logs it.
	Firmary *launch.Firmary
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

// Sign-in kinds. Browser and device link are as in the relay's signin_step message; paste code is Claude Code's
// sign-in for a computer without a browser, where the provider's page shows a code the person brings back here.
const (
	SignInBrowser    = "browser"
	SignInDeviceLink = "device_link"
	SignInPasteCode  = "paste_code"
)

// SignInStep is what the person needs to finish a provider's own sign-in: the provider's link and, for a device
// link, its one-time code. It is shown to that person only and never logged.
type SignInStep struct {
	Kind     string
	URL      string
	UserCode string
}

// SignInUI is how a sign-in reaches the person. Step is called once the provider's link is known. Code, used only by
// the paste-code kind, asks the person for the code the provider's page showed them; it may be nil when nobody can be
// asked, and then that kind fails.
type SignInUI struct {
	Step func(SignInStep)
	Code func(ctx context.Context) (string, error)
}

// SignInDriver is a Driver that can run the provider's own sign-in in a profile. SignIn blocks until the sign-in
// finishes, fails, or ctx ends. Kinds names the sign-in kinds the provider supports, best first for a computer with
// a browser.
type SignInDriver interface {
	Driver
	Kinds() []string
	SignIn(ctx context.Context, p Profile, kind string, ui SignInUI) error
	SignOut(ctx context.Context, p Profile) error
}

// DisplayEnv are the variables a provider's own sign-in needs to open the person's browser on a Linux desktop. They
// are passed to sign-in only, never to a turn.
var DisplayEnv = []string{"DISPLAY", "WAYLAND_DISPLAY", "DBUS_SESSION_BUS_ADDRESS", "XDG_CURRENT_DESKTOP",
	"XDG_SESSION_TYPE", "XDG_DATA_DIRS", "XDG_CONFIG_DIRS", "BROWSER"}

// WithDisplayEnv adds the inherited DisplayEnv variables to env.
func WithDisplayEnv(env, inherited []string) []string {
	for _, kv := range inherited {
		k, _, _ := strings.Cut(kv, "=")
		if slices.Contains(DisplayEnv, k) {
			env = append(env, kv)
		}
	}
	return env
}

// WriteFileAtomic replaces path with b, mode 0600, through a temporary file in the same directory, so a reader
// never sees a partial file and a link planted at path is replaced rather than followed.
func WriteFileAtomic(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
