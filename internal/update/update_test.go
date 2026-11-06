package update

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/Firmary/bridge/internal/driver"
)

type gate struct{ held, busy bool }

func (g *gate) Hold() bool {
	if g.busy || g.held {
		return false
	}
	g.held = true
	return true
}
func (g *gate) Release() { g.held = false }

type rig struct {
	t         *testing.T
	priv      ed25519.PrivateKey
	u         *Updater
	srv       *httptest.Server
	files     map[string][]byte
	restarted string
	selfTest  error
	changed   int
	staged    []driver.Pin
	pin       driver.Pin
}

func newRig(t *testing.T, version string) *rig {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	g := &rig{t: t, priv: priv, files: map[string][]byte{}, pin: driver.Pin{Version: "2.1.304", SHA256: map[string]string{"linux-x64": hexOf("old")}}}
	g.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := g.files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	t.Cleanup(g.srv.Close)
	// Every request, whatever its host (the manifest names github.com), goes to the test server.
	client := &http.Client{Transport: toServer{g.srv}}
	devKey := make([]byte, 32)
	g.u = &Updater{
		Dir: t.TempDir(), Version: version, Platform: "linux-x64", ManifestURL: g.srv.URL + "/manifest.json",
		Keys: []ed25519.PublicKey{pub}, HTTP: client, Gate: &gate{},
		DeviceKey: func() []byte { return devKey },
		SelfTest:  func(ctx context.Context, path, v string) error { return g.selfTest },
		Restart:   func(path string) error { g.restarted = path; return errors.New("test: no restart") },
		Changed:   func() { g.changed++ },
		Providers: []Provider{{
			ID:      "claude-code",
			Current: func() driver.Pin { return g.pin },
			Stage:   func(ctx context.Context, p driver.Pin) error { g.staged = append(g.staged, p); return nil },
			Apply:   func(ctx context.Context, p driver.Pin) error { g.pin = p; return nil },
		}},
	}
	return g
}

// toServer sends every request to srv, keeping its path.
type toServer struct{ srv *httptest.Server }

func (s toServer) RoundTrip(r *http.Request) (*http.Response, error) {
	u, _ := url.Parse(s.srv.URL)
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host, r.Host = u.Scheme, u.Host, u.Host
	return s.srv.Client().Transport.RoundTrip(r)
}

// exeURL is where the manifest says version's linux-x64 executable downloads from.
func exeURL(version string) string { return DownloadURL(version, "firmary-link-linux-x64") }

// exePath is exeURL's path on the test server.
func exePath(version string) string { u, _ := url.Parse(exeURL(version)); return u.Path }

// privateDir is a temporary directory only its owner can write, as the launcher requires whatever the umask.
func privateDir(t *testing.T) string {
	d := t.TempDir()
	if err := os.Chmod(d, 0o700); err != nil {
		t.Fatal(err)
	}
	return d
}

func hexOf(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// manifest returns a valid manifest offering version with the given executable, sequence seq.
func (g *rig) manifest(seq int64, version string, exe []byte) Manifest {
	g.files[exePath(version)] = exe
	return Manifest{Schema: 1, Sequence: seq, Expires: time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		Helper: HelperRelease{Version: version, RolloutPercent: 100, Platforms: map[string]HelperArtifact{
			"linux-x64": {URL: exeURL(version), SHA256: hexOf(string(exe))},
		}},
		MinHelperVersion: "0.2.0", Providers: map[string]*ProviderRelease{}, DisabledAdapters: []string{},
		WhatsNew: []WhatsNew{}}
}

// publish signs m with key (the rig's own by default) and serves it from the test server.
func (g *rig) publish(m Manifest, key ed25519.PrivateKey) ([]byte, []byte) {
	body, _ := json.Marshal(m)
	if key == nil {
		key = g.priv
	}
	sig := Sign(key, body)
	g.files["/manifest.json"], g.files["/manifest.json.sig"] = body, sig
	return body, sig
}

// apply publishes m signed with the rig's key and applies it.
func (g *rig) apply(m Manifest) error {
	body, sig := g.publish(m, nil)
	return g.u.Apply(context.Background(), body, sig)
}

func (g *rig) state() State {
	st, err := LoadState(g.u.Dir)
	if err != nil {
		g.t.Fatal(err)
	}
	return st
}

// TestRefusesTamperedReplayedExpiredAndForeignManifests: each refusal leaves the Link as it was.
func TestRefusesTamperedReplayedExpiredAndForeignManifests(t *testing.T) {
	g := newRig(t, "0.2.0")
	ctx := context.Background()

	body, sig := g.publish(g.manifest(10, "0.2.1", []byte("v0.2.1")), nil)
	tampered := append([]byte{}, body...)
	tampered[len(tampered)-2] ^= 1
	if err := g.u.Apply(ctx, tampered, sig); !errors.Is(err, ErrSignature) {
		t.Fatalf("tampered: %v", err)
	}
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	fBody, fSig := g.publish(g.manifest(11, "0.2.1", []byte("v0.2.1")), other)
	if err := g.u.Apply(ctx, fBody, fSig); !errors.Is(err, ErrSignature) {
		t.Fatalf("foreign key: %v", err)
	}
	if err := g.u.Apply(ctx, body, []byte("not a signature")); !errors.Is(err, ErrSignature) {
		t.Fatalf("garbage signature: %v", err)
	}
	exp := g.manifest(12, "0.2.1", []byte("v0.2.1"))
	exp.Expires = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	if err := g.apply(exp); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired: %v", err)
	}
	if st := g.state(); st.Sequence != 0 || st.Current != "" || g.restarted != "" {
		t.Fatalf("a refused manifest changed state: %+v", st)
	}

	// Accept 20, then a validly signed older manifest (19) and a different one at 20 are replays.
	accepted := g.manifest(20, "0.2.0", []byte("same"))
	if err := g.apply(accepted); err != nil {
		t.Fatal(err)
	}
	if err := g.apply(g.manifest(19, "0.2.1", []byte("v0.2.1"))); !errors.Is(err, ErrReplayed) {
		t.Fatalf("older sequence: %v", err)
	}
	m := g.manifest(20, "0.2.1", []byte("v0.2.1"))
	if err := g.apply(m); !errors.Is(err, ErrReplayed) {
		t.Fatalf("same sequence, other content: %v", err)
	}
	if g.restarted != "" {
		t.Fatal("a replay started an update")
	}
	// The manifest already accepted, fetched again, is fine. It is the same bytes: a new manifest(20, ...) can carry
	// a later Expires second, which is other content.
	if err := g.apply(accepted); err != nil {
		t.Fatalf("refetch of the accepted manifest: %v", err)
	}
}

// TestDownloadsOnlyFromTheVersionsOwnRelease: a validly signed manifest whose Link download is anywhere but the Bridge
// release of its own version is refused before anything is fetched.
func TestDownloadsOnlyFromTheVersionsOwnRelease(t *testing.T) {
	for _, u := range []string{
		"https://evil.example/firmary-link-linux-x64",
		"https://github.com/Evil/bridge/releases/download/v0.2.1/firmary-link-linux-x64",
		"https://github.com/Firmary/bridge/releases/download/v0.2.0/firmary-link-linux-x64",
		"https://github.com/Firmary/bridge/releases/download/v0.2.1/../v0.2.0/firmary-link-linux-x64",
		"https://github.com/Firmary/bridge/releases/download/v0.2.1/sub/firmary-link-linux-x64",
		"https://github.com/Firmary/bridge/releases/download/v0.2.1/firmary-link-linux-x64?x=1",
		"https://github.com/Firmary/bridge/releases/download/v0.2.1/",
		"https://github.com.evil.example/Firmary/bridge/releases/download/v0.2.1/firmary-link-linux-x64",
		"http://github.com/Firmary/bridge/releases/download/v0.2.1/firmary-link-linux-x64",
	} {
		g := newRig(t, "0.2.0")
		m := g.manifest(1, "0.2.1", []byte("v0.2.1"))
		m.Helper.Platforms["linux-x64"] = HelperArtifact{URL: u, SHA256: hexOf("v0.2.1")}
		if err := g.apply(m); !errors.Is(err, ErrMalformed) || g.restarted != "" {
			t.Errorf("%s: err %v, restarted %q", u, err, g.restarted)
		}
	}
	g := newRig(t, "0.2.0")
	if g.apply(g.manifest(1, "0.2.1", []byte("v0.2.1"))); g.restarted != Binary(g.u.Dir, "0.2.1") {
		t.Fatalf("the release's own download wasn't installed: restarted %q", g.restarted)
	}
}

// TestUpdateSwitchesBetweenTurnsAfterSelfTest: a newer version in the rollout is downloaded, checked, self-tested
// and switched to only when no turn runs; a download that doesn't match its checksum, or a version that fails its
// self-test, never is.
func TestUpdateSwitchesBetweenTurnsAfterSelfTest(t *testing.T) {
	g := newRig(t, "0.2.0")
	ctx := context.Background()

	// Wrong bytes served for the version: refused.
	m := g.manifest(1, "0.2.1", []byte("genuine"))
	g.publish(m, nil)
	g.files[exePath("0.2.1")] = []byte("evil")
	if err := g.u.CheckOnce(ctx); err == nil || g.restarted != "" {
		t.Fatalf("checksum mismatch: err %v restarted %q", err, g.restarted)
	}

	// Self-test failure: not switched, not tried again, reported once.
	g.selfTest = errors.New("broken")
	g.publish(g.manifest(2, "0.2.1", []byte("genuine")), nil)
	if err := g.u.CheckOnce(ctx); err == nil || g.restarted != "" {
		t.Fatalf("self-test failure: err %v restarted %q", err, g.restarted)
	}
	if st := g.state(); st.Skip != "0.2.1" || st.Current != "" || st.Failure == nil || st.Failure.Pattern != "self_test_failed" {
		t.Fatalf("state after self-test failure %+v", st)
	}
	if f := TakeFailure(g.u.Dir); f == nil || f.Version != "0.2.1" || TakeFailure(g.u.Dir) != nil {
		t.Fatal("failure not reported exactly once")
	}

	// A good version while a turn runs: waits; then switches.
	g.selfTest = nil
	g.publish(g.manifest(3, "0.2.2", []byte("good")), nil)
	gt := g.u.Gate.(*gate)
	gt.busy = true
	cctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	g.u.CheckOnce(cctx)
	cancel()
	if g.restarted != "" {
		t.Fatal("switched during a turn")
	}
	gt.busy = false
	g.u.CheckOnce(ctx)
	if g.restarted != Binary(g.u.Dir, "0.2.2") {
		t.Fatalf("restarted %q", g.restarted)
	}
	if b, _ := os.ReadFile(g.restarted); string(b) != "good" {
		t.Fatalf("installed %q", b)
	}
	// The test's Restart returns an error, so the switch was undone and the gate released.
	if st := g.state(); st.Current != "" || gt.held {
		t.Fatalf("after a failed restart: %+v held %v", st, gt.held)
	}
}

// TestRolloutAndMinimumVersion: a device outside the rollout stays put unless it is below minHelperVersion.
func TestRolloutAndMinimumVersion(t *testing.T) {
	g := newRig(t, "0.2.0")
	ctx := context.Background()
	key := make([]byte, 32)
	bucket := Bucket(key)
	m := g.manifest(1, "0.2.1", []byte("v"))
	m.Helper.RolloutPercent = bucket // bucket < percent is false
	g.publish(m, nil)
	g.u.CheckOnce(ctx)
	if g.restarted != "" {
		t.Fatal("updated outside the rollout")
	}
	m = g.manifest(2, "0.2.1", []byte("v"))
	m.Helper.RolloutPercent, m.MinHelperVersion = 0, "0.2.1"
	g.publish(m, nil)
	g.u.CheckOnce(ctx)
	if g.restarted == "" {
		t.Fatal("a Link below minHelperVersion didn't update")
	}
	if Bucket(key) != bucket || Bucket([]byte("other key")) == Bucket(key) && Bucket([]byte("x")) == Bucket(key) {
		t.Fatal("bucket is not stable")
	}

	// A development build never updates.
	d := newRig(t, "0.0.0-dev")
	d.publish(d.manifest(1, "0.2.1", []byte("v")), nil)
	d.u.CheckOnce(ctx)
	if d.restarted != "" {
		t.Fatal("a dev build updated")
	}
}

// TestLauncherRollsBack: the launcher runs the new version, and goes back to the previous one when it keeps failing
// to start or its executable changed on disk. A committed version stays.
func TestLauncherRollsBack(t *testing.T) {
	dir := privateDir(t)
	write := func(v, body string) string {
		p := Binary(dir, v)
		os.MkdirAll(filepath.Dir(p), 0o700)
		os.WriteFile(p, []byte(body), 0o700)
		return hexOf(body)
	}
	sum1, sum2 := write("0.2.1", "one"), write("0.2.2", "two")

	// 0.2.2 just installed by the updater (first start counted), 0.2.1 before it, installed executable 0.2.0.
	SaveState(dir, State{Current: "0.2.2", CurrentSHA256: sum2, Previous: "0.2.1", PreviousSHA256: sum1, Trial: true, TrialStarts: 1})
	if got := Choose(dir, "0.2.0"); got != Binary(dir, "0.2.2") {
		t.Fatalf("second start: %q", got)
	}
	// It failed again: roll back to 0.2.1.
	if got := Choose(dir, "0.2.0"); got != Binary(dir, "0.2.1") {
		t.Fatalf("after two failed starts: %q", got)
	}
	st, _ := LoadState(dir)
	if st.Current != "0.2.1" || st.Trial || st.Skip != "0.2.2" || st.Failure == nil || st.Failure.Pattern != "start_failed" {
		t.Fatalf("state %+v", st)
	}

	// A committed version keeps running.
	SaveState(dir, State{Current: "0.2.2", CurrentSHA256: sum2, Trial: true, TrialStarts: 1})
	if err := Commit(dir, "0.2.2"); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if got := Choose(dir, "0.2.0"); got != Binary(dir, "0.2.2") {
			t.Fatalf("committed version: %q", got)
		}
	}

	// Its executable changed on disk: back to the installed one.
	os.WriteFile(Binary(dir, "0.2.2"), []byte("tampered"), 0o700)
	if got := Choose(dir, "0.2.0"); got != "" {
		t.Fatalf("tampered executable still launched: %q", got)
	}
	if st, _ := LoadState(dir); st.Failure == nil || st.Failure.Pattern != "checksum_mismatch" {
		t.Fatalf("state %+v", st)
	}

	// A newer installed executable wins over an older update.
	SaveState(dir, State{Current: "0.2.1", CurrentSHA256: sum1})
	if got := Choose(dir, "0.3.0"); got != "" {
		t.Fatalf("older update chosen over a newer install: %q", got)
	}
}

// TestLauncherRunsOnlyPrivateVersions: a version is started only when the data directory, the update directory, the
// version's directory and its executable can't be written by anyone else; otherwise the installed one runs.
func TestLauncherRunsOnlyPrivateVersions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fsperm.CheckOwned is not implemented on Windows")
	}
	data := privateDir(t)
	dir := filepath.Join(data, "update")
	p := Binary(dir, "0.2.1")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(p, []byte("one"), 0o700)
	SaveState(dir, State{Current: "0.2.1", CurrentSHA256: hexOf("one")})
	if got := Choose(dir, "0.2.0"); got != p {
		t.Fatalf("private version not chosen: %q", got)
	}
	for _, open := range []string{data, dir, filepath.Join(dir, "versions"), filepath.Dir(p), p} {
		mode := os.FileMode(0o700)
		os.Chmod(open, 0o777)
		if got := Choose(dir, "0.2.0"); got != "" {
			t.Errorf("%s writable by others, still launched %q", open, got)
		}
		os.Chmod(open, mode)
	}
	if got := Choose(dir, "0.2.0"); got != p {
		t.Fatalf("not chosen again once private: %q", got)
	}
}

// TestProviderPinsMoveBetweenTurns: a new provider pin is staged first, then applied while the gate is held, and
// remembered; an unknown source is ignored.
func TestProviderPinsMoveBetweenTurns(t *testing.T) {
	g := newRig(t, "0.2.0")
	m := g.manifest(1, "0.2.0", []byte("same"))
	m.Providers["claude-code"] = &ProviderRelease{Version: "2.1.400", Source: "anthropic-release-manifest",
		Platforms: map[string]ProviderArtifact{"linux-x64": {SHA256: hexOf("new")}}}
	m.Providers["codex"] = &ProviderRelease{Version: "9.9.9", Source: "somewhere-else",
		Platforms: map[string]ProviderArtifact{"linux-x64": {SHA256: hexOf("x")}}}
	m.DisabledAdapters = []string{"helper-codex"}
	g.publish(m, nil)
	if err := g.u.CheckOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if g.pin.Version != "2.1.400" || len(g.staged) != 1 || g.u.Gate.(*gate).held {
		t.Fatalf("pin %+v staged %d", g.pin, len(g.staged))
	}
	if p, ok := g.u.Pin("claude-code"); !ok || p.Version != "2.1.400" {
		t.Fatalf("pin not remembered: %+v", p)
	}
	if _, ok := g.u.Pin("codex"); ok {
		t.Fatal("unknown source accepted")
	}
	if !g.u.Disabled("helper-codex") || g.u.Disabled("helper-claude-code") || g.changed == 0 {
		t.Fatal("disabled adapters not applied")
	}
}

func TestCompareAndPlatform(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{{"0.2.0", "0.2.0", 0}, {"0.2.1", "0.2.0", 1}, {"0.10.0", "0.9.9", 1}, {"0.2.0-beta.1", "0.2.0", -1}, {"0.0.0-dev", "0.1.0", -1}, {"junk", "0.0.1", -1}} {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%s, %s) = %d", c.a, c.b, got)
		}
	}
	if Platform("darwin", "arm64") != "darwin-universal" || Platform("windows", "amd64") != "windows-x64" || Platform("linux", "arm64") != "linux-arm64" {
		t.Error("platform keys")
	}
	if ExeName(runtime.GOOS) == "" || len(Keys()) != 2 {
		t.Error("pinned keys")
	}
}
