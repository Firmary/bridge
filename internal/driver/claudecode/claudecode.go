// Package claudecode drives Anthropic's Claude Code CLI: a pinned copy owned by the Helper, run with its own
// profile directory, Anthropic's updater off, no tools, and a scrubbed environment.
package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sync"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"

	"github.com/Firmary/firmary-helper/internal/driver"
)

// ID is the provider id used in hello and logs.
const ID = "claude-code"

// Driver is the Claude Code driver.
type Driver struct {
	root     string
	baseURL  string
	keyring  openpgp.EntityList
	http     *http.Client
	platform string
	environ  func() []string
	// extraEnv is set only by in-package tests (to point a real binary at a recording stub API).
	extraEnv    map[string]string
	managedDirs []string

	mu      sync.Mutex
	version string
	binary  string
}

// Option configures a Driver. Options exist for tests; production uses the defaults.
type Option func(*Driver)

// WithRelease points Ensure at another release server and signing keyring.
func WithRelease(baseURL string, kr openpgp.EntityList, c *http.Client) Option {
	return func(d *Driver) {
		d.baseURL, d.keyring = baseURL, kr
		if c != nil {
			d.http = c
		}
	}
}

// WithPlatform overrides the detected platform.
func WithPlatform(p string) Option { return func(d *Driver) { d.platform = p } }

// WithManagedDirs overrides where Status looks for managed settings (default ManagedDirs()).
func WithManagedDirs(dirs ...string) Option { return func(d *Driver) { d.managedDirs = dirs } }

// ManagedDirs are the machine-wide directories Claude Code reads managed settings from (managed-settings.json,
// managed-settings.d/, managed-mcp.json). They apply even with --setting-sources "", and the Helper must not get
// around them; it reports them so Firmary's firm policy can decide.
func ManagedDirs() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{"/Library/Application Support/ClaudeCode"}
	case "windows":
		pf := os.Getenv("ProgramFiles")
		if pf == "" {
			pf = `C:\Program Files`
		}
		return []string{filepath.Join(pf, "ClaudeCode")}
	}
	return []string{"/etc/claude-code"}
}

// managed reports whether any managed-settings directory has something in it. A directory that exists but can't
// be read counts as managed.
func (d *Driver) managed() bool {
	for _, dir := range d.managedDirs {
		entries, err := os.ReadDir(dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil || len(entries) > 0 {
			return true
		}
	}
	return false
}

// WithEnviron overrides the inherited environment source (default os.Environ).
func WithEnviron(f func() []string) Option { return func(d *Driver) { d.environ = f } }

// New returns a driver that installs into root (for example <data>/providers/claude-code).
func New(root string, opts ...Option) (*Driver, error) {
	d := &Driver{root: root, baseURL: ReleaseBaseURL, platform: Platform(), environ: os.Environ,
		http: &http.Client{Timeout: 30 * time.Minute}, managedDirs: ManagedDirs()}
	for _, o := range opts {
		o(d)
	}
	if d.keyring == nil {
		kr, err := AnthropicKeyring()
		if err != nil {
			return nil, err
		}
		d.keyring = kr
	}
	return d, nil
}

func (d *Driver) ID() string { return ID }

func (d *Driver) setInstalled(version, path string) {
	d.mu.Lock()
	d.version, d.binary = version, path
	d.mu.Unlock()
}

// Installed returns the verified pinned version and binary path, or empty strings before Ensure succeeds.
func (d *Driver) Installed() (version, path string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.version, d.binary
}

func configDir(p driver.Profile) string { return filepath.Join(p.Dir, "config") }
func workDir(p driver.Profile) string   { return filepath.Join(p.Dir, "work") }

func prepare(p driver.Profile) error {
	for _, dir := range []string{p.Dir, configDir(p), workDir(p)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// authStatus is the subset of `claude auth status --json` the Helper reads. Email is deliberately not read. orgId is
// read so a ticket can bind a turn to one provider organisation (providerOrgIds), not just a plan type.
type authStatus struct {
	LoggedIn         bool   `json:"loggedIn"`
	AuthMethod       string `json:"authMethod"`
	APIProvider      string `json:"apiProvider"`
	APIKeySource     string `json:"apiKeySource"`
	SubscriptionType string `json:"subscriptionType"`
	OrgID            string `json:"orgId"`
	OrgName          string `json:"orgName"`
}

// orgIDRe is the form of an Anthropic organisation id. Anything else is not reported, so it can never match a
// ticket's providerOrgIds.
var orgIDRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Status runs `claude auth status --json` in the profile. Signed in means a claude.ai subscription login with
// Anthropic as the API provider and no API key in play; anything else reports signed out.
func (d *Driver) Status(ctx context.Context, p driver.Profile) (driver.ProviderStatus, error) {
	managed := d.managed()
	version, bin := d.Installed()
	if bin == "" {
		return driver.ProviderStatus{Managed: managed}, nil
	}
	if err := prepare(p); err != nil {
		return driver.ProviderStatus{Managed: managed}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "auth", "status", "--json")
	cmd.Dir = workDir(p)
	cmd.Env = Env(d.environ(), configDir(p), nil)
	cmd.Stdin = nil
	cmd.Stderr = io.Discard
	var out bytes.Buffer
	cmd.Stdout = &limitedWriter{w: &out, n: 64 << 10}
	setProcessGroup(cmd)
	runErr := cmd.Run()
	var s authStatus
	if err := json.Unmarshal(out.Bytes(), &s); err != nil {
		if runErr != nil {
			return driver.ProviderStatus{Installed: version, Managed: managed}, errors.New("auth status failed")
		}
		return driver.ProviderStatus{Installed: version, Managed: managed}, errors.New("auth status output is malformed")
	}
	st := driver.ProviderStatus{Installed: version, Managed: managed}
	if s.LoggedIn && s.AuthMethod == "claude.ai" && s.APIProvider == "firstParty" && (s.APIKeySource == "" || s.APIKeySource == "none") {
		st.SignedIn, st.AccountType, st.Org = true, s.SubscriptionType, s.OrgName
		if orgIDRe.MatchString(s.OrgID) {
			st.OrgID = s.OrgID
		}
	}
	return st, nil
}

type limitedWriter struct {
	w io.Writer
	n int64
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n <= 0 {
		return len(p), nil
	}
	q := p
	if int64(len(q)) > l.n {
		q = q[:l.n]
	}
	n, err := l.w.Write(q)
	l.n -= int64(n)
	if err != nil {
		return n, err
	}
	return len(p), nil
}
