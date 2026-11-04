package update

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Firmary/bridge/internal/driver"
	"github.com/Firmary/bridge/internal/logx"
	"github.com/Firmary/bridge/internal/protocol"
)

// Gate is the turn gate: the updater swaps anything only while it holds the Link, which it can only do between
// turns (helper.Helper).
type Gate interface {
	Hold() bool
	Release()
}

// Provider is a provider app whose pin the manifest may move. Apply installs and verifies the pin (the driver's
// Ensure), swaps to it and checks the existing profile still reads; it is called while the gate is held, after Stage
// has downloaded and verified the copy without swapping.
type Provider struct {
	ID      string
	Current func() driver.Pin
	Stage   func(ctx context.Context, pin driver.Pin) error
	Apply   func(ctx context.Context, pin driver.Pin) error
}

// Updater checks Firmary's signed manifest and applies it silently. Nothing it does is shown to the person; it only
// logs metadata.
type Updater struct {
	Dir         string // <data>/update
	Version     string // the running Link's version
	Platform    string // manifest platform key, ThisPlatform()
	ManifestURL string
	Keys        []ed25519.PublicKey
	HTTP        *http.Client
	Log         *logx.Logger
	Now         func() time.Time
	// DeviceKey returns the device public key, for the rollout bucket.
	DeviceKey func() []byte
	Gate      Gate
	Providers []Provider
	// SelfTest runs a downloaded Link with `self-test` and returns nil only if it reports itself healthy as version.
	SelfTest func(ctx context.Context, path, version string) error
	// Restart replaces this process with the Link at path. It returns only on failure.
	Restart func(path string) error
	// Changed is called after provider pins or paused adapters changed, so the Link sends a fresh hello.
	Changed func()

	mu       sync.Mutex
	disabled []string
	checking sync.Mutex
}

// Every is how often the Link checks, with up to a quarter of it added at random so devices spread out.
const Every = 4 * time.Hour

// Disabled reports whether the last accepted manifest paused adapter.
func (u *Updater) Disabled(adapter string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return slices.Contains(u.disabled, adapter)
}

// Pin returns the provider pin an accepted manifest set, if any.
func (u *Updater) Pin(provider string) (driver.Pin, bool) {
	st, err := LoadState(u.Dir)
	if err != nil {
		return driver.Pin{}, false
	}
	p, ok := st.Providers[provider]
	return p, ok && p.Version != ""
}

// Run checks shortly after start and then every few hours, until ctx ends.
func (u *Updater) Run(ctx context.Context, first time.Duration) {
	if st, err := LoadState(u.Dir); err == nil {
		u.mu.Lock()
		u.disabled = st.DisabledAdapters
		u.mu.Unlock()
	}
	wait := first
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		_ = u.CheckOnce(ctx)
		wait = Every + time.Duration(rand.Int64N(int64(Every/4)))
	}
}

func (u *Updater) now() time.Time {
	if u.Now != nil {
		return u.Now()
	}
	return time.Now()
}

func (u *Updater) log(ev logx.Event, f logx.Fields) { u.Log.Log(ev, f) }

func (u *Updater) get(ctx context.Context, url string, limit int64, w io.Writer) error {
	if !strings.HasPrefix(url, "https://") {
		return errors.New("not https")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	c := u.HTTP
	if c == nil {
		c = &http.Client{Timeout: 30 * time.Minute}
	}
	resp, err := c.Do(req)
	if err != nil {
		return errors.New("request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	n, err := io.Copy(w, io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return errors.New("read failed")
	}
	if n > limit {
		return errors.New("too large")
	}
	return nil
}

// CheckOnce fetches, verifies and applies the manifest once. Every refusal is logged with its reason and leaves the
// Link as it was.
func (u *Updater) CheckOnce(ctx context.Context) error {
	u.checking.Lock()
	defer u.checking.Unlock()
	var body, sig bytes.Buffer
	if err := u.get(ctx, u.ManifestURL, MaxManifestBytes, &body); err != nil {
		u.log(logx.UpdateCheck, logx.Fields{Reason: "manifest_unavailable"})
		return err
	}
	if err := u.get(ctx, u.ManifestURL+".sig", MaxSignatureBytes, &sig); err != nil {
		u.log(logx.UpdateCheck, logx.Fields{Reason: "signature_unavailable"})
		return err
	}
	return u.Apply(ctx, body.Bytes(), sig.Bytes())
}

// Apply verifies a fetched manifest and its signature, then applies it: the provider pins, the paused adapters and,
// when this device is in the rollout, a newer Link.
func (u *Updater) Apply(ctx context.Context, body, sig []byte) error {
	if err := Verify(body, sig, u.Keys); err != nil {
		u.log(logx.UpdateRejected, logx.Fields{Reason: err.Error()})
		return err
	}
	m, err := Parse(body)
	if err != nil {
		u.log(logx.UpdateRejected, logx.Fields{Reason: err.Error()})
		return err
	}
	st, err := LoadState(u.Dir)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	if err := Check(m, st.Sequence, u.now()); err != nil {
		// The manifest already accepted, fetched again, is not a replay: its pending work (a download that failed
		// last time) is retried. Anything else at or below the last sequence is.
		if !(errors.Is(err, ErrReplayed) && m.Sequence == st.Sequence && digest == st.ManifestSHA256) {
			u.log(logx.UpdateRejected, logx.Fields{Reason: err.Error(), Attempt: int(m.Sequence % 1_000_000)})
			return err
		}
		exp, _ := time.Parse(time.RFC3339, m.Expires)
		if !u.now().Before(exp) {
			u.log(logx.UpdateRejected, logx.Fields{Reason: ErrExpired.Error()})
			return ErrExpired
		}
	} else {
		st.Sequence, st.ManifestSHA256 = m.Sequence, digest
		st.DisabledAdapters = slices.Clone(m.DisabledAdapters)
		if err := SaveState(u.Dir, st); err != nil {
			return err
		}
		u.mu.Lock()
		changed := !slices.Equal(u.disabled, st.DisabledAdapters)
		u.disabled = st.DisabledAdapters
		u.mu.Unlock()
		u.log(logx.UpdateCheck, logx.Fields{Reason: "accepted", Version: m.Helper.Version})
		if changed && u.Changed != nil {
			u.Changed()
		}
	}

	pinsChanged := false
	for _, p := range u.Providers {
		version, sha, archive, ok := m.Pin(p.ID)
		if !ok {
			continue
		}
		pin := driver.Pin{Version: version, SHA256: sha}
		if len(archive) > 0 {
			pin.ArchiveSHA256 = archive
		}
		cur := p.Current()
		if cur.Version == pin.Version && maps.Equal(cur.SHA256, pin.SHA256) && maps.Equal(cur.ArchiveSHA256, pin.ArchiveSHA256) {
			continue
		}
		if err := u.movePin(ctx, p, pin); err != nil {
			u.log(logx.UpdateFailed, logx.Fields{Provider: p.ID, Version: pin.Version, Reason: "provider_update_failed"})
			continue
		}
		pinsChanged = true
		u.log(logx.UpdateApplied, logx.Fields{Provider: p.ID, Version: pin.Version})
	}
	if pinsChanged && u.Changed != nil {
		u.Changed()
	}
	return u.maybeUpdateLink(ctx, m)
}

// movePin downloads and verifies the new provider copy, then swaps to it between turns and records it.
func (u *Updater) movePin(ctx context.Context, p Provider, pin driver.Pin) error {
	if err := p.Stage(ctx, pin); err != nil {
		return err
	}
	if !u.hold(ctx) {
		return ctx.Err()
	}
	defer u.Gate.Release()
	if err := p.Apply(ctx, pin); err != nil {
		return err
	}
	st, err := LoadState(u.Dir)
	if err != nil {
		return err
	}
	if st.Providers == nil {
		st.Providers = map[string]driver.Pin{}
	}
	st.Providers[p.ID] = pin
	return SaveState(u.Dir, st)
}

// hold waits until no turn is running and holds the gate. It gives up when ctx ends.
func (u *Updater) hold(ctx context.Context) bool {
	for !u.Gate.Hold() {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(5 * time.Second):
		}
	}
	return true
}

// maybeUpdateLink installs and switches to the manifest's Link version when it is newer than this one and this
// device is in its rollout (or this Link is below minHelperVersion).
func (u *Updater) maybeUpdateLink(ctx context.Context, m Manifest) error {
	v := m.Helper.Version
	if !IsRelease(u.Version) || !IsRelease(v) || Compare(v, u.Version) <= 0 {
		return nil
	}
	st, err := LoadState(u.Dir)
	if err != nil || st.Skip == v {
		return err
	}
	art, ok := m.Helper.Platforms[u.Platform]
	if !ok {
		return nil
	}
	forced := Compare(u.Version, m.MinHelperVersion) < 0
	if !forced && !InRollout(Bucket(u.DeviceKey()), m.Helper.RolloutPercent) {
		u.log(logx.UpdateCheck, logx.Fields{Reason: "not_in_rollout", Version: v})
		return nil
	}

	path := Binary(u.Dir, v)
	if err := u.download(ctx, art, path); err != nil {
		u.log(logx.UpdateFailed, logx.Fields{Version: v, Reason: "download_failed"})
		return err
	}
	if err := u.SelfTest(ctx, path, v); err != nil {
		st, _ := LoadState(u.Dir)
		st.Skip = v
		st.Failure = &protocol.UpdateFailure{Version: v, Pattern: "self_test_failed"}
		_ = SaveState(u.Dir, st)
		u.log(logx.UpdateFailed, logx.Fields{Version: v, Reason: "self_test_failed"})
		if u.Changed != nil {
			u.Changed()
		}
		return err
	}
	if !u.hold(ctx) {
		return ctx.Err()
	}
	st, err = LoadState(u.Dir)
	if err != nil {
		u.Gate.Release()
		return err
	}
	running := st.Current
	runningSHA := st.CurrentSHA256
	if running != u.Version {
		// This process is the installed executable: rolling back means going back to it.
		running, runningSHA = "", ""
	}
	st.Previous, st.PreviousSHA256 = running, runningSHA
	st.Current, st.CurrentSHA256 = v, art.SHA256
	// The restart below is the version's first start.
	st.Trial, st.TrialStarts = true, 1
	if err := SaveState(u.Dir, st); err != nil {
		u.Gate.Release()
		return err
	}
	u.log(logx.UpdateApplied, logx.Fields{Version: v, Reason: "restarting"})
	err = u.Restart(path)
	// Still here: the restart failed. Undo the switch.
	st, _ = LoadState(u.Dir)
	rollBack(&st, "restart_failed")
	_ = SaveState(u.Dir, st)
	u.Gate.Release()
	u.log(logx.UpdateFailed, logx.Fields{Version: v, Reason: "restart_failed"})
	return err
}

// download fetches a Link executable to path, verifying its size and SHA-256 before it is moved into place.
func (u *Updater) download(ctx context.Context, art HelperArtifact, path string) error {
	if got, err := fileSHA256(path); err == nil && got == art.SHA256 {
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".download-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	err = u.get(ctx, art.URL, MaxBinaryBytes, io.MultiWriter(tmp, h))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != art.SHA256 {
		return errors.New("download does not match the manifest checksum")
	}
	if err := os.Chmod(tmp.Name(), 0o700); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
