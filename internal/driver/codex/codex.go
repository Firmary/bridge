// Package codex drives OpenAI's Codex CLI: a pinned copy owned by the Helper, run with its own CODEX_HOME per
// profile, the update check off, nothing that could act on the computer, and a scrubbed environment.
package codex

import (
	"bytes"
	"context"
	"encoding/base64"
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
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Firmary/bridge/internal/driver"
	"github.com/Firmary/bridge/internal/driver/prompt"
	"github.com/Firmary/bridge/internal/launch"
	"github.com/Firmary/bridge/internal/protocol"
)

// ID is the provider id used in hello, signin_step and logs.
const ID = "codex"

// Driver is the Codex driver.
type Driver struct {
	root         string
	baseURL      string
	http         *http.Client
	platform     string
	environ      func() []string
	managedPaths []string
	// extraConfig and extraEnv are set only by in-package tests (to point a real binary at a recording stub API).
	// extraConfig is added to the launch settings, before the Helper's fixed settings.
	extraConfig []string
	extraEnv    []string

	mu      sync.Mutex
	version string
	binary  string
}

// Option configures a Driver. Options exist for tests; production uses the defaults.
type Option func(*Driver)

// WithRelease points Ensure at another release server.
func WithRelease(baseURL string, c *http.Client) Option {
	return func(d *Driver) {
		d.baseURL = baseURL
		if c != nil {
			d.http = c
		}
	}
}

// WithPlatform overrides the detected platform.
func WithPlatform(p string) Option { return func(d *Driver) { d.platform = p } }

// WithEnviron overrides the inherited environment source (default os.Environ).
func WithEnviron(f func() []string) Option { return func(d *Driver) { d.environ = f } }

// WithManagedPaths overrides where Status looks for managed configuration (default ManagedPaths()).
func WithManagedPaths(paths ...string) Option { return func(d *Driver) { d.managedPaths = paths } }

// ManagedPaths are the machine-wide places Codex reads system and managed configuration from (config.toml,
// requirements.toml, managed_config.toml, system skills; on macOS also the MDM preferences domain). They apply on
// top of the Helper's profile and the Helper must not get around them; it reports them so Firmary's firm policy can
// decide. A ChatGPT workspace's cloud-managed requirements also apply and can't be seen from here.
func ManagedPaths() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{"/etc/codex", "/Library/Managed Preferences/com.openai.codex.plist"}
	case "windows":
		pd := os.Getenv("ProgramData")
		if pd == "" {
			pd = `C:\ProgramData`
		}
		return []string{filepath.Join(pd, "OpenAI", "Codex")}
	}
	return []string{"/etc/codex"}
}

// managed reports whether any managed path is a file or a directory with something in it. One that exists but
// can't be read counts as managed.
func (d *Driver) managed() bool {
	for _, p := range d.managedPaths {
		info, err := os.Stat(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil || !info.IsDir() {
			return true
		}
		entries, err := os.ReadDir(p)
		if err != nil || len(entries) > 0 {
			return true
		}
	}
	return false
}

// New returns a driver that installs into root (for example <data>/providers/codex).
func New(root string, opts ...Option) (*Driver, error) {
	d := &Driver{root: root, baseURL: ReleaseBaseURL, platform: Platform(), environ: os.Environ,
		http: &http.Client{Timeout: 30 * time.Minute}, managedPaths: ManagedPaths()}
	for _, o := range opts {
		o(d)
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

// codexHome is the profile's CODEX_HOME. workDir is the empty directory turns run in.
func codexHome(p driver.Profile) string { return filepath.Join(p.Dir, "home") }
func workDir(p driver.Profile) string   { return filepath.Join(p.Dir, "work") }

// instructionsFile is where the profile's copy of Firmary's system prompt is written for model_instructions_file.
func instructionsFile(p driver.Profile) string {
	return filepath.Join(codexHome(p), "firmary-system-prompt.md")
}

// efforts maps each launch effort to Codex's model_reasoning_effort. Codex has no level above xhigh, so max is xhigh.
var efforts = map[string]string{"low": "low", "medium": "medium", "high": "high", "xhigh": "xhigh", "max": "xhigh"}

// toolSettings maps each launch catalog tool to the Codex settings that turn it on, and noTools are the settings
// for every catalog tool off. web_search is the only one: OpenAI runs the search; nothing runs on this computer.
var (
	toolSettings = map[string][]string{launch.ToolWebSearch: {`web_search="live"`}}
	noTools      = []string{`web_search="disabled"`}
)

// launchSettings are the settings that depend on the profile and the turn's launch profile, each a key and value
// from this package's own tables: Firmary's system prompt, effort, the catalog tools and the Codex knobs. The prompt
// text itself is in fileOnly, settings for config.toml only, never the command line; args are safe to pass as -c
// flags too. The fixed settings come after both and never share a key with them.
func (d *Driver) launchSettings(p driver.Profile, l launch.Profile) (args, fileOnly []string) {
	if l.Codex.KeepBuiltinInstructions {
		fileOnly = []string{`developer_instructions=` + tomlString(systemPromptText(l))}
	} else {
		args = append(args, `model_instructions_file=`+tomlString(instructionsFile(p)))
	}
	if l.HasTool(launch.ToolWebSearch) {
		args = append(args, toolSettings[launch.ToolWebSearch]...)
	} else {
		args = append(args, noTools...)
	}
	if e, ok := efforts[l.Effort]; ok {
		args = append(args, `model_reasoning_effort=`+tomlString(e))
	}
	if slices.Contains(launch.CodexReasoningSummaries, l.Codex.ReasoningSummary) {
		args = append(args, `model_reasoning_summary=`+tomlString(l.Codex.ReasoningSummary))
	}
	if slices.Contains(launch.CodexVerbosities, l.Codex.Verbosity) {
		args = append(args, `model_verbosity=`+tomlString(l.Codex.Verbosity))
	}
	return append(args, d.extraConfig...), fileOnly
}

// systemPromptText is the turn's system prompt: the launch profile's, else Firmary's built-in default.
func systemPromptText(l launch.Profile) string {
	if l.SystemPrompt != "" {
		return l.SystemPrompt
	}
	return prompt.Default
}

// prepare creates the profile and rewrites its config.toml and Firmary's system prompt file for the launch profile,
// so a change made by Codex or anyone else doesn't survive to the next run. Global instruction files in CODEX_HOME
// are removed: Codex sends them with every turn.
func (d *Driver) prepare(p driver.Profile, l launch.Profile) error {
	for _, dir := range []string{p.Dir, codexHome(p), workDir(p)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	for _, name := range []string{"AGENTS.md", "AGENTS.override.md"} {
		if err := os.Remove(filepath.Join(codexHome(p), name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if err := driver.WriteFileAtomic(instructionsFile(p), []byte(systemPromptText(l))); err != nil {
		return err
	}
	args, fileOnly := d.launchSettings(p, l)
	return driver.WriteFileAtomic(filepath.Join(codexHome(p), "config.toml"), []byte(configTOML(append(args, fileOnly...))))
}

// command builds a Codex invocation in the profile: Helper settings both in config.toml and as -c session flags
// (which outrank a project's .codex/config.toml), scrubbed environment, the empty work directory, no stdin.
func (d *Driver) command(ctx context.Context, bin string, p driver.Profile, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = workDir(p)
	cmd.Env = append(Env(d.environ(), codexHome(p)), d.extraEnv...)
	cmd.Stdin = nil
	setProcessGroup(cmd)
	return cmd
}

// withConfig puts the -c flags for the Helper's settings, with the built-in launch profile, after a subcommand.
func (d *Driver) withConfig(p driver.Profile, sub ...string) []string {
	args, _ := d.launchSettings(p, launch.Profile{})
	return append(sub, configArgs(args)...)
}

// authFile is the subset of CODEX_HOME/auth.json the Helper reads. Tokens are never kept: only the id_token's
// plan type and ChatGPT account id claims are decoded, and the email is deliberately not read (consent, #1888).
type authFile struct {
	AuthMode     string  `json:"auth_mode"`
	OpenAIAPIKey *string `json:"OPENAI_API_KEY"`
	Tokens       *struct {
		IDToken string `json:"id_token"`
	} `json:"tokens"`
}

type idClaims struct {
	Auth struct {
		PlanType  string `json:"chatgpt_plan_type"`
		AccountID string `json:"chatgpt_account_id"`
	} `json:"https://api.openai.com/auth"`
}

// accountIDRe is the form of a ChatGPT account (workspace) id. Anything else is not reported, so it can never match
// a ticket's providerOrgIds.
var accountIDRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// readAccount returns the plan type and account id of a ChatGPT login in the profile, or ok=false for anything else
// (no file, an API key, another auth mode, an unreadable token).
func readAccount(home string) (planType, accountID string, ok bool) {
	f, err := os.Open(filepath.Join(home, "auth.json"))
	if err != nil {
		return "", "", false
	}
	defer f.Close()
	var a authFile
	if json.NewDecoder(io.LimitReader(f, 1<<20)).Decode(&a) != nil {
		return "", "", false
	}
	if a.AuthMode != "chatgpt" || (a.OpenAIAPIKey != nil && *a.OpenAIAPIKey != "") || a.Tokens == nil {
		return "", "", false
	}
	parts := strings.Split(a.Tokens.IDToken, ".")
	if len(parts) != 3 {
		return "", "", false
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return "", "", false
	}
	var c idClaims
	if json.Unmarshal(payload, &c) != nil {
		return "", "", false
	}
	if protocol.MachineNameRe.MatchString(c.Auth.PlanType) {
		planType = c.Auth.PlanType
	}
	if accountIDRe.MatchString(c.Auth.AccountID) {
		accountID = c.Auth.AccountID
	}
	return planType, accountID, true
}

// Status runs `codex login status` in the profile. Signed in means Codex itself says "Logged in using ChatGPT" and
// the profile holds a ChatGPT login with no API key; anything else reports signed out.
func (d *Driver) Status(ctx context.Context, p driver.Profile) (driver.ProviderStatus, error) {
	managed := d.managed()
	version, bin := d.Installed()
	st := driver.ProviderStatus{Installed: version, Managed: managed}
	if bin == "" {
		return driver.ProviderStatus{Managed: managed}, nil
	}
	if err := d.prepare(p, launch.Profile{}); err != nil {
		return st, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := d.command(ctx, bin, p, d.withConfig(p, "login", "status")...)
	var out bytes.Buffer
	w := &limitedWriter{w: &out, n: 64 << 10}
	cmd.Stdout, cmd.Stderr = w, w
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return st, errors.New("login status timed out")
	}
	loggedIn := false
	for _, l := range strings.Split(stripANSI(out.String()), "\n") {
		switch strings.TrimSpace(l) {
		case "Logged in using ChatGPT":
			loggedIn = runErr == nil
		case "Not logged in":
			return st, nil
		}
	}
	if !loggedIn {
		if runErr != nil {
			return st, errors.New("login status failed")
		}
		return st, nil
	}
	plan, account, ok := readAccount(codexHome(p))
	if !ok {
		return st, nil
	}
	st.SignedIn, st.AccountType, st.OrgID = true, plan, account
	return st, nil
}

var (
	ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)
	// Only links on OpenAI's own sign-in host are ever passed on, so nothing else Codex prints can reach Firmary.
	browserURLRe = regexp.MustCompile(`https://auth\.openai\.com/oauth/authorize\?[A-Za-z0-9._~%&=+:/-]+`)
	deviceURLRe  = regexp.MustCompile(`https://auth\.openai\.com/codex/device\b`)
	userCodeRe   = regexp.MustCompile(`\b[A-Z0-9]{4}-[A-Z0-9]{4,6}\b`)
)

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// signInScanner finds the sign-in link (and code) in Codex's login output. Nothing else of that output is kept.
type signInScanner struct {
	kind    string
	buf     strings.Builder
	url     string
	code    string
	emitted bool
	step    func(driver.SignInStep)
}

func (s *signInScanner) Write(p []byte) (int, error) {
	if s.emitted || s.buf.Len() > 64<<10 {
		return len(p), nil
	}
	s.buf.Write(p)
	text := stripANSI(s.buf.String())
	switch s.kind {
	case driver.SignInBrowser:
		s.url = browserURLRe.FindString(text)
		if s.url != "" {
			s.emitted = true
			s.step(driver.SignInStep{Kind: s.kind, URL: s.url})
		}
	case driver.SignInDeviceLink:
		if s.url == "" {
			s.url = deviceURLRe.FindString(text)
		}
		if s.url != "" {
			// The code is printed after the link.
			s.code = userCodeRe.FindString(text[strings.Index(text, s.url):])
		}
		if s.url != "" && s.code != "" {
			s.emitted = true
			s.step(driver.SignInStep{Kind: s.kind, URL: s.url, UserCode: s.code})
		}
	}
	return len(p), nil
}

// SignInTimeout bounds one sign-in. Codex's device code lasts 15 minutes.
const SignInTimeout = 15 * time.Minute

// SignIn runs Codex's own ChatGPT sign-in in the profile: `codex login` (browser, which also listens on
// localhost:1455 for the redirect while it runs) or `codex login --device-auth` (a link and a one-time code, for a
// computer without a browser). step is called once with OpenAI's link. SignIn returns nil only if the profile then
// reports signed in.
func (d *Driver) SignIn(ctx context.Context, p driver.Profile, kind string, step func(driver.SignInStep)) error {
	_, bin := d.Installed()
	if bin == "" {
		return &driver.Failure{Class: protocol.ClassFatal, Pattern: "provider_not_installed"}
	}
	args := []string{"login"}
	switch kind {
	case driver.SignInBrowser:
	case driver.SignInDeviceLink:
		args = append(args, "--device-auth")
	default:
		return &driver.Failure{Class: protocol.ClassPolicy, Pattern: "signin_kind_invalid"}
	}
	if err := d.prepare(p, launch.Profile{}); err != nil {
		return &driver.Failure{Class: protocol.ClassFatal, Pattern: "profile_unavailable"}
	}
	ctx, cancel := context.WithTimeout(ctx, SignInTimeout)
	defer cancel()
	cmd := d.command(ctx, bin, p, d.withConfig(p, args...)...)
	sc := &signInScanner{kind: kind, step: step}
	cmd.Stdout, cmd.Stderr = sc, sc
	runErr := cmd.Run()
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return &driver.Failure{Class: protocol.ClassTransient, Pattern: "signin_timeout"}
	case ctx.Err() != nil:
		return &driver.Failure{Class: protocol.ClassFatal, Pattern: "cancelled"}
	case !sc.emitted:
		return &driver.Failure{Class: protocol.ClassFatal, Pattern: "signin_link_missing"}
	case runErr != nil:
		return &driver.Failure{Class: protocol.ClassAuth, Pattern: "signin_failed"}
	}
	st, err := d.Status(context.WithoutCancel(ctx), p)
	if err != nil || !st.SignedIn {
		return &driver.Failure{Class: protocol.ClassAuth, Pattern: "not_signed_in"}
	}
	return nil
}

// SignOut runs `codex logout` in the profile.
func (d *Driver) SignOut(ctx context.Context, p driver.Profile) error {
	_, bin := d.Installed()
	if bin == "" {
		return errors.New("provider not installed")
	}
	if err := d.prepare(p, launch.Profile{}); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := d.command(ctx, bin, p, d.withConfig(p, "logout")...)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Run(); err != nil {
		return errors.New("logout failed")
	}
	return nil
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
