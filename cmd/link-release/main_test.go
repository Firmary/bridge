package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Firmary/bridge/internal/update"
)

// pinTestKey pins a fresh update key for the test and puts its private half in LINK_UPDATE_SIGNING_KEY.
func pinTestKey(t *testing.T) ed25519.PrivateKey {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	saved := update.PublicKeys
	update.PublicKeys = append([]string{base64.StdEncoding.EncodeToString(pub)}, saved...)
	t.Cleanup(func() { update.PublicKeys = saved })
	t.Setenv("LINK_UPDATE_SIGNING_KEY", base64.StdEncoding.EncodeToString(priv.Seed()))
	return priv
}

// release writes manifest.json and manifest.json.sig, signed by key, offering 0.3.4 from url.
func release(t *testing.T, key ed25519.PrivateKey, url string) string {
	dir := t.TempDir()
	m := update.Manifest{Schema: 1, Sequence: 100, Expires: time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		MinHelperVersion: "0.2.0", Helper: update.HelperRelease{Version: "0.3.4", RolloutPercent: 100,
			Platforms: map[string]update.HelperArtifact{"linux-x64": {URL: url, SHA256: strings.Repeat("a", 64)}}}}
	body, _ := json.Marshal(m)
	os.WriteFile(filepath.Join(dir, "manifest.json"), body, 0o644)
	os.WriteFile(filepath.Join(dir, "manifest.json.sig"), update.Sign(key, body), 0o644)
	return dir
}

// TestRefreshResignsOnlyWhatFirmarySigned: a manifest swapped on the release, signed by any other key or naming a
// download outside its own release, is never re-signed.
func TestRefreshResignsOnlyWhatFirmarySigned(t *testing.T) {
	key := pinTestKey(t)
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	own := update.DownloadURL("0.3.4", "firmary-link-linux-x64")

	for name, in := range map[string]string{
		"foreign key":      release(t, other, own),
		"foreign download": release(t, key, "https://evil.example/firmary-link-linux-x64"),
	} {
		out := t.TempDir()
		if err := refresh([]string{"--in", filepath.Join(in, "manifest.json"), "--out", out}); err == nil {
			t.Errorf("%s: re-signed", name)
		}
		if _, err := os.Stat(filepath.Join(out, "manifest.json")); err == nil {
			t.Errorf("%s: wrote a manifest", name)
		}
	}
	unsigned := release(t, key, own)
	os.Remove(filepath.Join(unsigned, "manifest.json.sig"))
	if refresh([]string{"--in", filepath.Join(unsigned, "manifest.json"), "--out", t.TempDir()}) == nil {
		t.Error("re-signed a manifest with no signature")
	}

	in, out := release(t, key, own), t.TempDir()
	if err := refresh([]string{"--in", filepath.Join(in, "manifest.json"), "--out", out}); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(out, "manifest.json"))
	sig, _ := os.ReadFile(filepath.Join(out, "manifest.json.sig"))
	if err := update.Verify(body, sig, update.Keys()); err != nil {
		t.Fatal(err)
	}
	if m, _ := update.Parse(body); m.Sequence <= 100 || m.Helper.Platforms["linux-x64"].URL != own {
		t.Fatalf("refreshed %+v", m)
	}
}

// TestManifestNamesTheVersionsOwnRelease: every download a new manifest offers is in the Bridge release of its
// version.
func TestManifestNamesTheVersionsOwnRelease(t *testing.T) {
	pinTestKey(t)
	dist := t.TempDir()
	for _, p := range helperPlatforms {
		name := "firmary-link-" + p
		if strings.HasPrefix(p, "windows-") {
			name += ".exe"
		}
		os.WriteFile(filepath.Join(dist, name), []byte(p), 0o644)
	}
	if err := manifest([]string{"--version", "0.3.5", "--dist", dist, "--out", dist}); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(dist, "manifest.json"))
	m, err := update.Parse(body)
	if err != nil || len(m.Helper.Platforms) != len(helperPlatforms) {
		t.Fatalf("%v %+v", err, m)
	}
	for p, a := range m.Helper.Platforms {
		if !strings.HasPrefix(a.URL, "https://github.com/Firmary/bridge/releases/download/v0.3.5/firmary-link-") {
			t.Errorf("%s: %s", p, a.URL)
		}
	}
}
