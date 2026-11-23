// Package codex drives OpenAI's Codex CLI: a pinned copy owned by the Link, run with its own CODEX_HOME per
// profile, the update check off, nothing that could act on the computer, and a scrubbed environment.
package codex

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
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Firmary/bridge/internal/brand"
	"github.com/Firmary/bridge/internal/driver"
	"github.com/Firmary/bridge/internal/driver/prompt"
	"github.com/Firmary/bridge/internal/launch"
	"github.com/Firmary/bridge/internal/proc"
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
	// extraConfig is added to the launch settings, before the Link's fixed settings.
	extraConfig []string
	extraEnv    []string

	// clientVersion is the Link's own version, sent in app-server's clientInfo.
	clientVersion string

	mu       sync.Mutex
	version  string
	binary   string
	accounts map[string]accountEntry // by profile directory; see account

	// locks holds one mutex per profile directory. A turn holds its profile's from writing its prompt and config
	// until Codex exits, so nothing rewrites them under it (see prepareIdle).
	locksMu sync.Mutex
	locks   map[string]*sync.Mutex
}

// profileLock returns the mutex of profile p.
func (d *Driver) profileLock(p driver.Profile) *sync.Mutex {
	d.locksMu.Lock()
	defer d.locksMu.Unlock()
	if d.locks == nil {
		d.locks = map[string]*sync.Mutex{}
	}
	m, ok := d.locks[p.Dir]
	if !ok {
		m = &sync.Mutex{}
		d.locks[p.Dir] = m
	}
	return m
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

// WithClientVersion sets the Link version Codex's app-server is told (default "0.0.0-dev", as an unreleased build).
func WithClientVersion(v string) Option { return func(d *Driver) { d.clientVersion = v } }

// WithPlatform overrides the detected platform.
func WithPlatform(p string) Option { return func(d *Driver) { d.platform = p } }

// WithEnviron overrides the inherited environment source (default os.Environ).
func WithEnviron(f func() []string) Option { return func(d *Driver) { d.environ = f } }

// WithManagedPaths overrides where Status looks for managed configuration (default ManagedPaths()).
func WithManagedPaths(paths ...string) Option { return func(d *Driver) { d.managedPaths = paths } }

// ManagedPaths are the machine-wide places Codex reads system and managed configuration from (config.toml,
// requirements.toml, managed_config.toml, system skills; on macOS also the MDM preferences domain). They apply on
// top of the Link's profile and the Link must not get around them; it reports them so Firmary's firm policy can
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
	d := &Driver{root: root, baseURL: ReleaseBaseURL, platform: Platform(), environ: os.Environ, clientVersion: "0.0.0-dev",
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

// FirmaryTokenEnv is the variable that carries the turn's Firmary MCP access token, set only in the environment of
// that turn's Codex (bearer_token_env_var), never on its command line or in its config.
const FirmaryTokenEnv = "FIRMARY_MCP_TOKEN"

// firmaryServer is the name of Firmary's MCP server in config.toml, and the server name its exec items carry.
const firmaryServer = "firmary"

// firmarySettings are the config.toml settings for Firmary's own MCP server when the turn's profile turns the firmary
// tool on, else none. Keys are from config.schema.json at rust-v0.166.0 (RawMcpServerConfig). They are for
// config.toml only: the endpoint is the server's string, so it never goes on the command line.
//   - url and bearer_token_env_var: Streamable HTTP with "Authorization: Bearer $FIRMARY_MCP_TOKEN".
//   - default_tools_approval_mode="approve": under approval_policy="never", a tool that isn't annotated read-only
//     would otherwise ask for approval and be refused ("MCP tool call requires approval, but approval policy is
//     never", core/src/mcp_tool_call.rs). Firmary asks the person itself for anything that needs approval.
//   - required=true: Codex waits for the server before the first model request instead of leaving its tools out
//     after the default 1-second grace for optional servers, and fails the turn if it can't connect.
func firmarySettings(f *launch.Firmary) []string {
	if f == nil {
		return nil
	}
	const k = "mcp_servers." + firmaryServer + "."
	return []string{
		k + `url=` + tomlString(f.URL),
		k + `bearer_token_env_var=` + tomlString(FirmaryTokenEnv),
		k + `default_tools_approval_mode="approve"`,
		k + `required=true`,
	}
}

// systemPromptText is the turn's system prompt: the launch profile's, else Firmary's built-in default for the
// profile's tools.
func systemPromptText(l launch.Profile) string {
	if l.SystemPrompt != "" {
		return l.SystemPrompt
	}
	return prompt.Fallback(l.HasTool(launch.ToolWebSearch), l.HasTool(launch.ToolFirmary))
}

// prepare creates the profile and rewrites its config.toml and Firmary's system prompt file for the launch profile
// and the turn's Firmary credential (nil for none), so a change made by Codex or anyone else doesn't survive to the
// next run. Global instruction files in CODEX_HOME are removed: Codex sends them with every turn.
func (d *Driver) prepare(p driver.Profile, l launch.Profile, fm *launch.Firmary) error {
	if err := makeDirs(p); err != nil {
		return err
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
	fileOnly = append(fileOnly, firmarySettings(fm)...)
	return driver.WriteFileAtomic(filepath.Join(codexHome(p), "config.toml"), []byte(configTOML(append(args, fileOnly...))))
}

func makeDirs(p driver.Profile) error {
	for _, dir := range []string{p.Dir, codexHome(p), workDir(p)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// prepareIdle prepares the profile for a command other than a turn (status, sign-in, sign-out). While a turn holds
// the profile, its prompt and config.toml are that turn's and are left alone: rewriting them before Codex has read
// them would replace or drop the firm's prompt (review F1). They already carry the fixed settings, which these
// commands also get as -c flags. It never waits for a turn, so hello and status don't stall behind one.
func (d *Driver) prepareIdle(p driver.Profile) error {
	m := d.profileLock(p)
	if !m.TryLock() {
		return makeDirs(p)
	}
	defer m.Unlock()
	return d.prepare(p, launch.Profile{}, nil)
}

// command builds a Codex invocation in the profile: Link settings both in config.toml and as -c session flags
// (which outrank a project's .codex/config.toml), scrubbed environment, the empty work directory, no stdin.
func (d *Driver) command(ctx context.Context, bin string, p driver.Profile, args ...string) *exec.Cmd {
	cmd := proc.CommandContext(ctx, bin, args...)
	cmd.Dir = workDir(p)
	cmd.Env = append(Env(d.environ(), codexHome(p)), d.extraEnv...)
	cmd.Stdin = nil
	setProcessGroup(cmd)
	return cmd
}

// withConfig puts the -c flags for the Link's settings, with the built-in launch profile, after a subcommand.
func (d *Driver) withConfig(p driver.Profile, sub ...string) []string {
	args, _ := d.launchSettings(p, launch.Profile{})
	return append(sub, configArgs(args)...)
}

// The Link names itself to Codex's app-server as what it is (initialize's clientInfo, which Codex puts in its
// User-Agent), the way T3 Code sends "T3 Code": never another app's name.
const (
	clientName  = brand.Command
	clientTitle = brand.Name
)

// account is who is signed in to a profile, as Codex itself reports it.
type account struct{ planType, orgID, email string }

// accountEntry is a profile's last account/read answer, with the Codex binary and the size and time of the sign-in
// file it was read under. The file is only stat'ed, never opened: Codex rewrites it on every sign-in and token
// refresh, which is when the Link asks again (T3 Code watches the same file for the same reason). Status runs before
// every turn, and starting app-server each time would add most of a second to it.
type accountEntry struct {
	bin   string
	size  int64
	mtime time.Time
	acct  account
	ok    bool
}

// accountIDRe is the form of a ChatGPT account (workspace) id. Anything else is not reported, so it can never match
// a ticket's providerOrgIds.
var accountIDRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// signInFile is where Codex keeps the profile's sign-in (cli_auth_credentials_store="file"). The Link never reads it.
func signInFile(p driver.Profile) string { return filepath.Join(codexHome(p), "auth.json") }

// account returns the profile's account, from the cache while the sign-in file is unchanged, else from Codex.
func (d *Driver) account(ctx context.Context, bin string, p driver.Profile) (account, bool, error) {
	info, statErr := os.Stat(signInFile(p))
	d.mu.Lock()
	e, hit := d.accounts[p.Dir]
	d.mu.Unlock()
	if hit && statErr == nil && e.bin == bin && e.size == info.Size() && e.mtime.Equal(info.ModTime()) {
		return e.acct, e.ok, nil
	}
	acct, ok, err := d.readAccount(ctx, bin, p)
	if err != nil {
		return account{}, false, err
	}
	// Stat'ed before asking: if Codex rewrote the file in between, the next Status sees a new time and asks again.
	if statErr == nil {
		d.mu.Lock()
		if d.accounts == nil {
			d.accounts = map[string]accountEntry{}
		}
		d.accounts[p.Dir] = accountEntry{bin: bin, size: info.Size(), mtime: info.ModTime(), acct: acct, ok: ok}
		d.mu.Unlock()
	}
	return acct, ok, nil
}

// forgetAccount drops the profile's cached account, so the next Status asks Codex.
func (d *Driver) forgetAccount(p driver.Profile) {
	d.mu.Lock()
	delete(d.accounts, p.Dir)
	d.mu.Unlock()
}

// rpcMessage is one JSON-RPC message from app-server: a response (id with result or error), a notification (method
// only) or a request to the client (id and method).
type rpcMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

// readAccount asks the profile's own Codex who is signed in, the way T3 Code does: `codex app-server` over stdio,
// initialize, then account/read without a token refresh. Codex answers from its own sign-in, and looks up the
// workspace with OpenAI itself; the Link never opens auth.json or sees a token. ok is false for anything but a
// ChatGPT sign-in (an API key, no sign-in). Nothing Codex says beyond the answer is kept, and errors carry none of it.
func (d *Driver) readAccount(ctx context.Context, bin string, p driver.Profile) (acct account, ok bool, err error) {
	cmd := d.command(ctx, bin, p, d.withConfig(p, "app-server")...)
	in, err := cmd.StdinPipe()
	if err != nil {
		return account{}, false, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return account{}, false, err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return account{}, false, errors.New("app-server did not start")
	}
	// Closing stdin ends app-server; Wait reaps it (the context's cancel kills it if it hangs).
	defer func() { in.Close(); _ = cmd.Wait() }()
	enc, dec := json.NewEncoder(in), json.NewDecoder(out)
	send := func(m map[string]any) error { return enc.Encode(m) }
	if err := send(map[string]any{"method": "initialize", "id": 1, "params": map[string]any{
		"clientInfo": map[string]any{"name": clientName, "title": clientTitle, "version": d.clientVersion},
		// The workspace (workspaceRouting) is an experimental field of account/read.
		"capabilities": map[string]any{"experimentalApi": true},
	}}); err != nil {
		return account{}, false, errors.New("app-server closed")
	}
	for {
		var m rpcMessage
		if err := dec.Decode(&m); err != nil {
			return account{}, false, errors.New("app-server ended before answering")
		}
		if m.ID == nil || m.Method != "" {
			continue // a notification, or a request to the Link, which account/read never makes
		}
		if m.Error != nil && string(m.Error) != "null" {
			return account{}, false, errors.New("account/read failed")
		}
		switch string(m.ID) {
		case "1":
			if send(map[string]any{"method": "initialized"}) != nil ||
				send(map[string]any{"method": "account/read", "id": 2, "params": map[string]any{"refreshToken": false}}) != nil {
				return account{}, false, errors.New("app-server closed")
			}
		case "2":
			acct, ok = parseAccount(m.Result)
			return acct, ok, nil
		}
	}
}

// parseAccount reads account/read's answer (Codex 0.166.0's GetAccountResponse, experimental fields on). Only a
// ChatGPT account counts. The plan type must be a machine name and the workspace id a UUID, else they are left out.
func parseAccount(result json.RawMessage) (account, bool) {
	var r struct {
		Account *struct {
			Type     string `json:"type"`
			Email    string `json:"email"`
			PlanType string `json:"planType"`
		} `json:"account"`
		WorkspaceRouting *struct {
			ChatgptAccountID string `json:"chatgptAccountId"`
		} `json:"workspaceRouting"`
	}
	if json.Unmarshal(result, &r) != nil || r.Account == nil || r.Account.Type != "chatgpt" {
		return account{}, false
	}
	var a account
	if protocol.MachineNameRe.MatchString(r.Account.PlanType) {
		a.planType = r.Account.PlanType
	}
	if r.WorkspaceRouting != nil && accountIDRe.MatchString(r.WorkspaceRouting.ChatgptAccountID) {
		a.orgID = r.WorkspaceRouting.ChatgptAccountID
	}
	a.email = driver.CleanEmail(r.Account.Email)
	return a, true
}

// Status runs `codex login status` in the profile. Signed in means Codex itself says "Logged in using ChatGPT" and
// its account/read reports a ChatGPT account; anything else reports signed out. The plan, workspace and email are
// account/read's (for hello's accountEmail, design 5877e3252806: the person's own Connections page; never logged).
func (d *Driver) Status(ctx context.Context, p driver.Profile) (driver.ProviderStatus, error) {
	managed := d.managed()
	version, bin := d.Installed()
	st := driver.ProviderStatus{Installed: version, Managed: managed}
	if bin == "" {
		return driver.ProviderStatus{Managed: managed}, nil
	}
	if err := d.prepareIdle(p); err != nil {
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
	acct, ok, err := d.account(ctx, bin, p)
	if err != nil {
		return st, err
	}
	if !ok {
		return st, nil
	}
	st.SignedIn, st.AccountType, st.OrgID, st.AccountEmail = true, acct.planType, acct.orgID, acct.email
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
func (d *Driver) SignIn(ctx context.Context, p driver.Profile, kind string, ui driver.SignInUI) error {
	step := ui.Step
	if step == nil {
		step = func(driver.SignInStep) {}
	}
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
	if err := d.prepareIdle(p); err != nil {
		return &driver.Failure{Class: protocol.ClassFatal, Pattern: "profile_unavailable"}
	}
	ctx, cancel := context.WithTimeout(ctx, SignInTimeout)
	defer cancel()
	cmd := d.command(ctx, bin, p, d.withConfig(p, args...)...)
	if kind == driver.SignInBrowser {
		// Codex opens the person's browser itself.
		cmd.Env = driver.WithDisplayEnv(cmd.Env, d.environ())
	}
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
	d.forgetAccount(p)
	st, err := d.Status(context.WithoutCancel(ctx), p)
	if err != nil || !st.SignedIn {
		return &driver.Failure{Class: protocol.ClassAuth, Pattern: "not_signed_in"}
	}
	// The sign-in stays readable by this account only.
	_ = os.Chmod(signInFile(p), 0o600)
	return nil
}

// Kinds: Codex's browser sign-in, or its device link for a computer without a browser.
func (d *Driver) Kinds() []string { return []string{driver.SignInBrowser, driver.SignInDeviceLink} }

// SignOut runs `codex logout` in the profile.
func (d *Driver) SignOut(ctx context.Context, p driver.Profile) error {
	_, bin := d.Installed()
	if bin == "" {
		return errors.New("provider not installed")
	}
	if err := d.prepareIdle(p); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	d.forgetAccount(p)
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
