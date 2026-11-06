// Package agent puts the Link together: its data directory, log, the pinned provider apps, the turn policy, the
// relay connection and the silent updater. The terminal, the service and the menu-bar app all run the same agent;
// only how they show things to the person differs.
package agent

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Firmary/bridge/internal/devicekey"
	"github.com/Firmary/bridge/internal/driver"
	"github.com/Firmary/bridge/internal/driver/claudecode"
	"github.com/Firmary/bridge/internal/driver/codex"
	"github.com/Firmary/bridge/internal/fsperm"
	"github.com/Firmary/bridge/internal/helper"
	"github.com/Firmary/bridge/internal/logx"
	"github.com/Firmary/bridge/internal/pairing"
	"github.com/Firmary/bridge/internal/protocol"
	"github.com/Firmary/bridge/internal/relay"
	"github.com/Firmary/bridge/internal/ticket"
	"github.com/Firmary/bridge/internal/update"
)

// Environment is one Firmary environment: its relay and the ticket-signing public keys pinned for it (current and
// next). With no pinned key the Link refuses every turn.
type Environment struct {
	RelayURL   string
	TicketKeys []string // base64 Ed25519 public keys
}

// Environments are the Firmary environments a release build knows.
var Environments = map[string]Environment{
	"prod": {RelayURL: "wss://relay.prod.firmary.app/", TicketKeys: []string{
		"tvfbE9S5T4z4RErCm4DExS9KXz8GQRqFyXtF8mGZ+2w=", // current
		"/ERMRvlkYfEhIAD+tqkxjr6vwOdMbElVfEY+ycmiRbI=", // next
	}},
	"dev": {RelayURL: "wss://relay.dev.firmary.app/"},
}

// Providers, as people name them.
type ProviderInfo struct {
	ID      string // protocol provider id
	Adapter string // router adapter
	Account string // what the person signs in to: "Claude", "ChatGPT"
	App     string // the provider's app the Link runs: "Claude Code", "Codex"
}

// Providers are the provider apps the Link runs, in hello order.
var Providers = []ProviderInfo{
	{ID: claudecode.ID, Adapter: helper.AdapterClaudeCode, Account: "Claude", App: "Claude Code"},
	{ID: codex.ID, Adapter: helper.AdapterCodex, Account: "ChatGPT", App: "Codex"},
}

// ProviderByName finds a provider by what a person types: claude, chatgpt, or the provider id.
func ProviderByName(name string) (ProviderInfo, bool) {
	switch strings.ToLower(name) {
	case "claude", "claude-code", "anthropic":
		return Providers[0], true
	case "chatgpt", "codex", "openai":
		return Providers[1], true
	}
	return ProviderInfo{}, false
}

// Options configure an Agent.
type Options struct {
	Version string
	EnvName string
	Env     Environment
	DataDir string
	Ring    string
	// Updates turns on the silent updater (release builds on prod only).
	Updates bool
	// Kind is how this Link runs: protocol.KindDesktop (the app on a person's computer) or KindServer (the always-on
	// headless service). It goes in pairing_reveal and hello.
	Kind string
}

// Agent is one Link.
type Agent struct {
	Opts    Options
	Root    string // the data directory
	EnvDir  string // its environment's directory: key, pairing, profiles
	Log     *logx.Logger
	Claude  *claudecode.Driver
	Codex   *codex.Driver
	Helper  *helper.Helper
	Updater *update.Updater

	keys      []ed25519.PublicKey
	refresh   chan struct{}
	mu        sync.Mutex
	connects  int
	client    *relay.Client             // the running connection, once Run started it
	code      string                    // a pairing code given before Run
	signingIn map[string]*pendingSignIn // provider sign-ins the relay asked for, running now
}

// decodeKey decodes a base64 public key in standard or URL alphabet, padded or not (a --ticket-key-file from the
// relay's own tooling is base64url). It returns nil when none decodes.
func decodeKey(k string) []byte {
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(k); err == nil {
			return b
		}
	}
	return nil
}

// Open prepares the data directory, log and drivers. It doesn't touch the network.
func Open(o Options) (*Agent, error) {
	var keys []ed25519.PublicKey
	for _, k := range o.Env.TicketKeys {
		b := decodeKey(k)
		if len(b) != ed25519.PublicKeySize {
			return nil, errors.New("ticket key is malformed")
		}
		keys = append(keys, b)
	}
	a := &Agent{Opts: o, Root: o.DataDir, EnvDir: filepath.Join(o.DataDir, o.EnvName), keys: keys, refresh: make(chan struct{}, 1)}
	// MkdirAll doesn't fix a directory that already exists, so check every one: another local user who owns or can
	// write any of them could swap a verified provider binary, a Link update, or plant a key or pairing.
	for _, d := range []string{a.Root, a.EnvDir, filepath.Join(a.EnvDir, "profiles"), filepath.Join(a.Root, "logs"),
		filepath.Join(a.Root, "providers"), filepath.Join(a.Root, "providers", claudecode.ID),
		filepath.Join(a.Root, "providers", codex.ID), a.UpdateDir()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
		if err := fsperm.CheckOwned(d, 0o022); err != nil {
			return nil, fmt.Errorf("data directory: %w", err)
		}
	}
	log, err := logx.Open(filepath.Join(a.Root, "logs"))
	if err != nil {
		return nil, err
	}
	a.Log = log
	if a.Claude, err = claudecode.New(filepath.Join(a.Root, "providers", claudecode.ID)); err != nil {
		log.Close()
		return nil, err
	}
	if a.Codex, err = codex.New(filepath.Join(a.Root, "providers", codex.ID)); err != nil {
		log.Close()
		return nil, err
	}
	a.Updater = &update.Updater{Dir: a.UpdateDir(), Version: o.Version, Platform: update.ThisPlatform(),
		ManifestURL: update.ManifestURL, Keys: update.Keys(), Log: log}
	a.Helper = &helper.Helper{
		Providers: []helper.Provider{{Adapter: helper.AdapterClaudeCode, Driver: a.Claude}, {Adapter: helper.AdapterCodex, Driver: a.Codex}},
		Verifier:  ticket.NewVerifier(keys, nil), DataDir: a.EnvDir, Version: o.Version, Ring: o.Ring, Log: log,
		Disabled: a.Updater.Disabled, HelloExtra: a.helloExtra,
	}
	return a, nil
}

// Close closes the log.
func (a *Agent) Close() { a.Log.Close() }

// UpdateDir is where Link updates and the updater's state live.
func (a *Agent) UpdateDir() string { return filepath.Join(a.Root, "update") }

func (a *Agent) driver(id string) driver.SignInDriver {
	if id == codex.ID {
		return a.Codex
	}
	return a.Claude
}

// Driver returns the driver of a provider id.
func (a *Agent) Driver(id string) driver.SignInDriver { return a.driver(id) }

// Pin is the provider version this Link runs: the last one an accepted update manifest set, else the build's own.
func (a *Agent) Pin(id string) driver.Pin {
	if p, ok := a.Updater.Pin(id); ok {
		return p
	}
	if id == codex.ID {
		return codex.BuiltInPin
	}
	return claudecode.BuiltInPin
}

// Present reports whether the pinned copy of a provider is already on disk, so status checks can verify it without
// downloading anything.
func (a *Agent) Present(id string) bool {
	_, err := os.Stat(filepath.Join(a.Root, "providers", id, "versions", a.Pin(id).Version))
	return err == nil
}

// Ensure installs (if needed) and verifies the pinned copy of a provider.
func (a *Agent) Ensure(ctx context.Context, id string) error {
	pin := a.Pin(id)
	err := a.driver(id).Ensure(ctx, pin)
	reason := "ok"
	if err != nil {
		reason = "failed"
	}
	a.Log.Log(logx.ProviderEnsure, logx.Fields{Provider: id, Version: pin.Version, Reason: reason})
	return err
}

// EnsureLocal verifies a provider copy that is already on disk, without downloading.
func (a *Agent) EnsureLocal(ctx context.Context, id string) {
	if a.Present(id) {
		_ = a.Ensure(ctx, id)
	}
}

// Pairing returns this computer's pairing, or nil when it isn't paired.
func (a *Agent) Pairing() (*pairing.Record, error) { return pairing.Load(a.PairingPath()) }

// PairingPath and KeyPath are where the pairing record and device key live.
func (a *Agent) PairingPath() string { return filepath.Join(a.EnvDir, "pairing.json") }
func (a *Agent) KeyPath() string     { return filepath.Join(a.EnvDir, "device.key") }

// ErrNotPaired is returned by Profile before the computer is paired: sign-ins live in the paired person's profile.
var ErrNotPaired = errors.New("this computer isn't paired yet")

// Profile is the paired person's profile for a provider.
func (a *Agent) Profile(id string) (driver.Profile, error) {
	rec, err := a.Pairing()
	if err != nil {
		return driver.Profile{}, err
	}
	if rec == nil {
		return driver.Profile{}, ErrNotPaired
	}
	return helper.Profile(a.EnvDir, *rec, id), nil
}

// Status reports a provider's state for the paired person (signed out when not paired).
func (a *Agent) Status(ctx context.Context, id string) (driver.ProviderStatus, error) {
	p, err := a.Profile(id)
	if errors.Is(err, ErrNotPaired) {
		v, _ := installed(a.driver(id))
		return driver.ProviderStatus{Installed: v}, nil
	}
	if err != nil {
		return driver.ProviderStatus{}, err
	}
	return a.driver(id).Status(ctx, p)
}

func installed(d driver.Driver) (string, string) {
	if i, ok := d.(interface{ Installed() (string, string) }); ok {
		return i.Installed()
	}
	return "", ""
}

// Names are the paired person's and firm's display names, kept for the person's own screens only.
type Names struct {
	Person string `json:"person"`
	Firm   string `json:"firm"`
}

func (a *Agent) namesPath() string { return filepath.Join(a.EnvDir, "names.json") }

// SaveNames keeps the names from a pairing (mode 0600). They are never logged or sent anywhere.
func (a *Agent) SaveNames(n Names) {
	b, _ := json.Marshal(n)
	_ = driver.WriteFileAtomic(a.namesPath(), b)
}

// LoadNames returns the names of the current pairing, if known.
func (a *Agent) LoadNames() (Names, bool) {
	var n Names
	b, err := os.ReadFile(a.namesPath())
	if err != nil || json.Unmarshal(b, &n) != nil {
		return n, false
	}
	return n, true
}

// ForgetNames removes the names after the pairing ends.
func (a *Agent) ForgetNames() { _ = os.Remove(a.namesPath()) }

func (a *Agent) refreshPath() string { return filepath.Join(a.EnvDir, ".hello-refresh") }

// RefreshHello asks the running Link (this process, or the service, through a file it watches) to send a fresh
// hello, for example after a sign-in.
func (a *Agent) RefreshHello() {
	_ = os.WriteFile(a.refreshPath(), []byte(time.Now().UTC().Format(time.RFC3339Nano)), 0o600)
	select {
	case a.refresh <- struct{}{}:
	default:
	}
}

// watchRefresh turns changes of the refresh file (another process signed in or out) and a periodic tick (a provider
// may sign the person out) into hello refreshes.
func (a *Agent) watchRefresh(ctx context.Context, out chan<- struct{}) {
	mtime := func() time.Time {
		if fi, err := os.Stat(a.refreshPath()); err == nil {
			return fi.ModTime()
		}
		return time.Time{}
	}
	last := mtime()
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	periodic := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.refresh:
		case <-tick.C:
			m := mtime()
			if m.Equal(last) && time.Since(periodic) < 15*time.Minute {
				continue
			}
			last = m
		}
		periodic = time.Now()
		select {
		case out <- struct{}{}:
		default:
		}
	}
}

// helloExtra adds the kind of Link, why this connection opened and a rolled-back update.
func (a *Agent) helloExtra(h *protocol.Hello) {
	h.Kind = a.Opts.Kind
	a.mu.Lock()
	a.connects++
	n := a.connects
	a.mu.Unlock()
	switch {
	case n > 1:
		h.ReconnectReason = "network"
	case os.Getenv(update.ReasonEnv) == "update":
		h.ReconnectReason = "update"
	default:
		h.ReconnectReason = "start"
	}
	h.UpdateFailure = update.TakeFailure(a.UpdateDir())
}

// Hooks are how the relay connection reaches the person. See relay.Config for each. ProvidersChanged says a sign-in
// the relay asked for ended.
type Hooks struct {
	Reached          func()
	NeedsCode        func()
	Claimed          func(personName, firmName, code string)
	CodeRejected     func(reason string)
	Paired           func(personName, firmName string)
	AskAccept        func(ctx context.Context, personName, personEmail, firmName string) (accept, answered bool)
	Ready            func(rec pairing.Record)
	Offline          func()
	Unpaired         func()
	ProvidersChanged func()
}

// SetCode gives the Link the pairing code Firmary shows (pairing v2), before or while it runs. The code is
// normalised; a malformed one is refused with pairing.ErrBadCode.
func (a *Agent) SetCode(typed string) error {
	code, err := pairing.NormalizeCode(typed)
	if err != nil {
		return err
	}
	a.mu.Lock()
	c := a.client
	if c == nil {
		a.code = code
	}
	a.mu.Unlock()
	if c != nil {
		return c.SetCode(code)
	}
	return nil
}

// CancelPairing is the person's Cancel while connecting with a code: the connection closes and the code is
// forgotten.
func (a *Agent) CancelPairing() {
	a.mu.Lock()
	c := a.client
	a.code = ""
	a.mu.Unlock()
	if c != nil {
		c.CancelPairing()
	}
}

// Unpair removes this computer's pairing: the pairing, its names and the device key are deleted here, and the Link
// waits for a new code. Without a running connection (the unpair command) it deletes the files itself.
func (a *Agent) Unpair() error {
	a.cancelSignIns()
	a.mu.Lock()
	c := a.client
	a.mu.Unlock()
	if c != nil {
		return c.Unpair()
	}
	a.ForgetNames()
	if err := pairing.Delete(a.PairingPath()); err != nil {
		return err
	}
	if err := devicekey.Delete(a.KeyPath()); err != nil {
		return err
	}
	a.Log.Log(logx.Unpaired, logx.Fields{})
	return nil
}

// pendingSignIn is a provider sign-in the relay asked for: the pairing it signs in for, the latest connection's send,
// and the step once known.
type pendingSignIn struct {
	rec    pairing.Record
	cancel context.CancelFunc
	send   func(protocol.SigninStep)
	last   *protocol.SigninStep
	// codes takes the code the person pasted in Firmary, for a paste_code sign-in.
	codes chan string
}

// cancelSignIns stops every provider sign-in still running: they belong to a pairing that just ended, and their link
// or code must never reach whoever pairs next.
func (a *Agent) cancelSignIns() {
	a.mu.Lock()
	for id, s := range a.signingIn {
		s.cancel()
		delete(a.signingIn, id)
	}
	a.mu.Unlock()
}

// relaySignIn runs the provider sign-in Firmary asked for over the relay, in the paired person's profile, and passes
// the provider's link (and code) to Firmary. The desktop app opens the browser here; a server uses the provider's
// device link, opened on any browser. Claude Code has no device link: on a server Firmary shows its link, and the
// code Anthropic's page shows after Authorize comes back from Firmary (signin_code, relaySignInCode).
func (a *Agent) relaySignIn(ctx context.Context, rec pairing.Record, provider string, step func(protocol.SigninStep), done func()) {
	var p ProviderInfo
	for _, q := range Providers {
		if q.ID == provider {
			p = q
		}
	}
	if p.ID == "" {
		a.Log.Log(logx.ProviderSignIn, logx.Fields{Reason: "unknown_provider"})
		return
	}
	d := a.driver(p.ID)
	kind := driver.SignInBrowser
	if a.Opts.Kind == protocol.KindServer {
		kind = d.Kinds()[1]
		if kind != driver.SignInDeviceLink && kind != driver.SignInPasteCode {
			a.Log.Log(logx.ProviderSignIn, logx.Fields{Provider: p.ID, Reason: "relay_no_device_link"})
			return
		}
	}
	a.mu.Lock()
	if a.signingIn == nil {
		a.signingIn = map[string]*pendingSignIn{}
	}
	if s := a.signingIn[p.ID]; s != nil && s.rec != rec {
		// A sign-in for an earlier pairing: stop it, so its link never reaches this person and its sign-in never
		// lands in the earlier person's profile.
		s.cancel()
		delete(a.signingIn, p.ID)
	}
	if s := a.signingIn[p.ID]; s != nil {
		// One sign-in per provider at a time. Firmary asking again (after a reconnect, say) gets its link on the
		// connection that asked, now if it is known, else when the provider prints it.
		s.send = step
		last := s.last
		a.mu.Unlock()
		if last != nil {
			step(*last)
		}
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &pendingSignIn{rec: rec, cancel: cancel, send: step, codes: make(chan string, 1)}
	a.signingIn[p.ID] = s
	a.mu.Unlock()
	defer func() {
		cancel()
		a.mu.Lock()
		if a.signingIn[p.ID] == s {
			delete(a.signingIn, p.ID)
		}
		a.mu.Unlock()
		a.RefreshHello()
		if done != nil {
			done()
		}
	}()
	if err := a.Ensure(ctx, p.ID); err != nil {
		a.Log.Log(logx.ProviderSignIn, logx.Fields{Provider: p.ID, Reason: "ensure_failed"})
		return
	}
	ui := driver.SignInUI{Step: func(st driver.SignInStep) {
		ps := protocol.SigninStep{Kind: st.Kind, URL: st.URL, UserCode: st.UserCode}
		a.mu.Lock()
		s.last = &ps
		send := s.send
		a.mu.Unlock()
		send(ps)
	}}
	if kind == driver.SignInPasteCode {
		ui.Code = func(ctx context.Context) (string, error) {
			select {
			case code := <-s.codes:
				return code, nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
	}
	err := d.SignIn(ctx, helper.Profile(a.EnvDir, rec, p.ID), kind, ui)
	f := logx.Fields{Provider: p.ID, Reason: "relay_ok"}
	var fail *driver.Failure
	if errors.As(err, &fail) {
		f.Reason, f.ErrorClass, f.Pattern = "relay_failed", fail.Class, fail.Pattern
	} else if err != nil {
		f.Reason = "relay_failed"
	}
	a.Log.Log(logx.ProviderSignIn, f)
}

// relaySignInCode hands the code the person pasted in Firmary to the paste_code sign-in running for provider, if that
// sign-in belongs to this same pairing. A code for no such sign-in, or arriving while an earlier one waits to be read,
// is dropped: Firmary shows the person where the sign-in stands, and they can paste again.
func (a *Agent) relaySignInCode(rec pairing.Record, provider, code string) {
	a.mu.Lock()
	s := a.signingIn[provider]
	a.mu.Unlock()
	if s == nil || s.rec != rec || s.codes == nil {
		a.Log.Log(logx.ProviderSignIn, logx.Fields{Provider: provider, Reason: "code_without_signin"})
		return
	}
	select {
	case s.codes <- code:
	default:
	}
}

// Run keeps the relay connection up and the updater running until ctx ends.
func (a *Agent) Run(ctx context.Context, h Hooks) error {
	url := a.Opts.Env.RelayURL
	refresh := make(chan struct{}, 1)
	go a.watchRefresh(ctx, refresh)
	var committed sync.Once
	// The pending code moves into the client and the client becomes the one SetCode and CancelPairing reach in one
	// step, so a code typed while the Link starts is never stranded.
	a.mu.Lock()
	client, err := relay.New(relay.Config{
		URL: url, Env: a.Opts.EnvName, HelperVersion: a.Opts.Version, Device: PairingDevice(a.Opts.Kind), Log: a.Log,
		Handler: a.Helper, KeyPath: a.KeyPath(), PairingPath: a.PairingPath(),
		ShowConnected: h.Reached,
		// Pairing v2 only: the Link pairs with the code Firmary shows, and never shows one of its own.
		TypedCode:        true,
		Code:             a.code,
		ShowNeedsCode:    h.NeedsCode,
		ShowClaimed:      h.Claimed,
		ShowCodeRejected: h.CodeRejected,
		SignIn: func(ctx context.Context, rec pairing.Record, provider string, step func(protocol.SigninStep)) {
			a.relaySignIn(ctx, rec, provider, step, h.ProvidersChanged)
		},
		SignInCode: a.relaySignInCode,
		ShowPaired: func(person, firm string) {
			a.SaveNames(Names{Person: person, Firm: firm})
			if h.Paired != nil {
				h.Paired(person, firm)
			}
		},
		AskAccept: h.AskAccept,
		ShowReady: func(rec pairing.Record) {
			// A new version proves itself by getting through to Firmary.
			committed.Do(func() { a.commitUpdate() })
			if h.Ready != nil {
				h.Ready(rec)
			}
		},
		ShowOffline: h.Offline,
		ShowUnpaired: func() {
			a.cancelSignIns()
			a.ForgetNames()
			if h.Unpaired != nil {
				h.Unpaired()
			}
		},
		Refresh: refresh,
	})
	if err == nil {
		a.code, a.client = "", client
	}
	a.mu.Unlock()
	if err != nil {
		return err
	}
	defer func() {
		a.mu.Lock()
		a.client = nil
		a.mu.Unlock()
	}()
	if a.Opts.Updates && update.IsRelease(a.Opts.Version) {
		a.startUpdater(ctx)
		// Offline for a long time still keeps a new version that runs.
		go func() {
			select {
			case <-ctx.Done():
			case <-time.After(10 * time.Minute):
				committed.Do(func() { a.commitUpdate() })
			}
		}()
	}
	if err := client.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

func (a *Agent) commitUpdate() {
	if st, err := update.LoadState(a.UpdateDir()); err == nil && st.Trial && st.Current == a.Opts.Version {
		if update.Commit(a.UpdateDir(), a.Opts.Version) == nil {
			a.Log.Log(logx.UpdateCommitted, logx.Fields{Version: a.Opts.Version})
		}
	}
}

// devicePublicKey is the device key's public half, for the rollout bucket.
func (a *Agent) devicePublicKey() []byte {
	k, err := devicekey.LoadOrCreate(a.KeyPath())
	if err != nil {
		return nil
	}
	return k.Public()
}

func (a *Agent) startUpdater(ctx context.Context) {
	a.configureUpdater()
	go a.Updater.Run(ctx, 30*time.Second)
}

// configureUpdater connects the updater to this Link: its device key, turn gate, self-test, restart and providers.
func (a *Agent) configureUpdater() {
	u := a.Updater
	if u.Gate != nil {
		return
	}
	u.DeviceKey = a.devicePublicKey
	u.Gate = a.Helper
	u.SelfTest = a.selfTestBinary
	u.Restart = func(path string) error { return Restart(path) }
	u.Changed = a.RefreshHello
	for _, p := range Providers {
		id := p.ID
		u.Providers = append(u.Providers, update.Provider{
			ID:      id,
			Current: func() driver.Pin { return a.Pin(id) },
			Stage: func(ctx context.Context, pin driver.Pin) error {
				// A second driver on the same directory downloads and verifies the copy without switching to it.
				var d driver.Driver
				var err error
				if id == codex.ID {
					d, err = codex.New(filepath.Join(a.Root, "providers", id))
				} else {
					d, err = claudecode.New(filepath.Join(a.Root, "providers", id))
				}
				if err != nil {
					return err
				}
				return d.Ensure(ctx, pin)
			},
			Apply: func(ctx context.Context, pin driver.Pin) error {
				old := a.Pin(id)
				d := a.driver(id)
				if err := d.Ensure(ctx, pin); err != nil {
					return err
				}
				// The person's existing sign-in must still read under the new copy; otherwise go back.
				if prof, err := a.Profile(id); err == nil {
					if _, err := d.Status(ctx, prof); err != nil {
						_ = d.Ensure(ctx, old)
						return err
					}
				}
				return nil
			},
		})
	}
}

// selfTestBinary runs a downloaded Link with `self-test`: it must start, read this data directory, and say it is
// the version the manifest named.
func (a *Agent) selfTestBinary(ctx context.Context, path, version string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	out, err := selfTestCommand(ctx, path, a.Root).Output()
	if err != nil {
		return err
	}
	if !strings.Contains(string(out), "self-test ok "+version) {
		return errors.New("self-test didn't confirm the version")
	}
	return nil
}

// SelfTest checks this Link can run on this data directory: the key and pairing read, and the provider copies on
// disk verify and report. It changes nothing.
func (a *Agent) SelfTest(ctx context.Context) error {
	if _, err := a.Pairing(); err != nil {
		return fmt.Errorf("pairing: %w", err)
	}
	if _, err := os.Stat(a.KeyPath()); err == nil {
		if _, err := devicekey.LoadOrCreate(a.KeyPath()); err != nil {
			return err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, p := range Providers {
		if !a.Present(p.ID) {
			continue
		}
		if err := a.driver(p.ID).Ensure(ctx, a.Pin(p.ID)); err != nil {
			return fmt.Errorf("%s: %w", p.App, err)
		}
		if _, err := a.Status(ctx, p.ID); err != nil {
			return fmt.Errorf("%s status: %w", p.App, err)
		}
	}
	return nil
}

// CheckForUpdates checks the manifest now (the desktop app's "Check for updates").
func (a *Agent) CheckForUpdates(ctx context.Context) error {
	a.configureUpdater()
	return a.Updater.CheckOnce(ctx)
}
