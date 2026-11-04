// Command link-release is the release tool for the Link (not shipped to people): it makes update-signing keys, and
// writes and signs the update manifest a release publishes.
//
//	link-release keygen --out FILE                 write a new private key (base64 seed, mode 0600); print the public key
//	link-release manifest --version V --dist DIR --base-url URL --out DIR
//	link-release refresh --in manifest.json --out DIR
//	link-release verify --manifest FILE --sig FILE
//
// manifest and refresh sign with the base64 Ed25519 seed in LINK_UPDATE_SIGNING_KEY, and refuse a key whose public
// half isn't pinned in this build (update.PublicKeys). The key is never printed.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Firmary/bridge/internal/brand"
	"github.com/Firmary/bridge/internal/driver"
	"github.com/Firmary/bridge/internal/driver/claudecode"
	"github.com/Firmary/bridge/internal/driver/codex"
	"github.com/Firmary/bridge/internal/update"
)

// helperPlatforms are the manifest platform keys a release ships an updater executable for, as
// dist/firmary-link-<platform>[.exe].
var helperPlatforms = []string{"darwin-universal", "linux-x64", "linux-arm64", "windows-x64", "windows-arm64"}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "keygen":
		err = keygen(os.Args[2:])
	case "manifest":
		err = manifest(os.Args[2:])
	case "refresh":
		err = refresh(os.Args[2:])
	case "verify":
		err = verify(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "link-release:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: link-release keygen|manifest|refresh|verify ...")
	os.Exit(2)
}

func keygen(args []string) error {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	out := fs.String("out", "", "file for the private key")
	fs.Parse(args)
	if *out == "" {
		return errors.New("--out is required")
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(f, base64.StdEncoding.EncodeToString(priv.Seed())); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Println(base64.StdEncoding.EncodeToString(pub))
	return nil
}

// signingKey reads LINK_UPDATE_SIGNING_KEY and checks it matches a pinned public key.
func signingKey() (ed25519.PrivateKey, error) {
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(os.Getenv("LINK_UPDATE_SIGNING_KEY")))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("LINK_UPDATE_SIGNING_KEY is missing or malformed")
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	if !slices.ContainsFunc(update.Keys(), func(k ed25519.PublicKey) bool { return k.Equal(pub) }) {
		return nil, errors.New("LINK_UPDATE_SIGNING_KEY is not one of the update keys pinned in this build")
	}
	return priv, nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// providers are the provider pins built into this source tree. A pin bump on main followed by a manifest refresh
// moves every Link to the new provider version without a Link release.
func providers() map[string]*update.ProviderRelease {
	rel := func(source string, p driver.Pin) *update.ProviderRelease {
		r := &update.ProviderRelease{Version: p.Version, Source: source, Platforms: map[string]update.ProviderArtifact{}}
		for k, sha := range p.SHA256 {
			r.Platforms[k] = update.ProviderArtifact{SHA256: sha, ArchiveSHA256: p.ArchiveSHA256[k]}
		}
		return r
	}
	return map[string]*update.ProviderRelease{
		claudecode.ID: rel(update.Sources[claudecode.ID], claudecode.BuiltInPin),
		codex.ID:      rel(update.Sources[codex.ID], codex.BuiltInPin),
	}
}

func write(m update.Manifest, outDir string) error {
	priv, err := signingKey()
	if err != nil {
		return err
	}
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if _, err := update.Parse(body); err != nil {
		return fmt.Errorf("the manifest doesn't parse: %w", err)
	}
	sig := update.Sign(priv, body)
	if err := update.Verify(body, sig, update.Keys()); err != nil {
		return err
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, "manifest.json"), body, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, "manifest.json.sig"), sig, 0o644); err != nil {
		return err
	}
	fmt.Printf("signed manifest %d for %s %s, expires %s\n", m.Sequence, brand.Name, m.Helper.Version, m.Expires)
	return nil
}

// sequence is strictly increasing across releases and refreshes: the time in seconds, above any earlier one.
func sequence(prev int64) int64 { return max(time.Now().Unix(), prev+1) }

func expires(days int) string {
	return time.Now().UTC().Add(time.Duration(days) * 24 * time.Hour).Truncate(time.Second).Format(time.RFC3339)
}

func manifest(args []string) error {
	fs := flag.NewFlagSet("manifest", flag.ExitOnError)
	version := fs.String("version", "", "Link version, e.g. 0.2.0")
	dist := fs.String("dist", "dist", "directory with firmary-link-<platform>[.exe]")
	baseURL := fs.String("base-url", "", "https URL the release's assets download from")
	out := fs.String("out", "dist", "where to write manifest.json and manifest.json.sig")
	rollout := fs.Int("rollout", 100, "percent of devices that get this version")
	minVersion := fs.String("min-version", "0.2.0", "minHelperVersion: older Links update whatever the rollout")
	days := fs.Int("expires-days", 30, "days until the manifest expires")
	note := fs.String("whats-new", "", "one line of release notes")
	fs.Parse(args)
	if !update.IsRelease(*version) || !strings.HasPrefix(*baseURL, "https://") {
		return errors.New("--version must be a release version and --base-url https")
	}
	m := update.Manifest{Schema: 1, Sequence: sequence(0), Expires: expires(*days), MinHelperVersion: *minVersion,
		Helper:           update.HelperRelease{Version: *version, RolloutPercent: *rollout, Platforms: map[string]update.HelperArtifact{}},
		Providers:        providers(),
		DisabledAdapters: []string{},
		WhatsNew:         []update.WhatsNew{},
	}
	if *note != "" {
		m.WhatsNew = append(m.WhatsNew, update.WhatsNew{Version: *version, Text: *note})
	}
	for _, p := range helperPlatforms {
		name := brand.Command + "-" + p
		if strings.HasPrefix(p, "windows-") {
			name += ".exe"
		}
		sum, err := fileSHA256(filepath.Join(*dist, name))
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		m.Helper.Platforms[p] = update.HelperArtifact{URL: strings.TrimSuffix(*baseURL, "/") + "/" + name, SHA256: sum}
	}
	return write(m, *out)
}

func refresh(args []string) error {
	fs := flag.NewFlagSet("refresh", flag.ExitOnError)
	in := fs.String("in", "", "the current manifest.json")
	out := fs.String("out", "", "where to write the refreshed manifest")
	days := fs.Int("expires-days", 30, "days until the manifest expires")
	fs.Parse(args)
	body, err := os.ReadFile(*in)
	if err != nil {
		return err
	}
	m, err := update.Parse(body)
	if err != nil {
		return err
	}
	m.Sequence, m.Expires, m.Providers = sequence(m.Sequence), expires(*days), providers()
	return write(m, *out)
}

func verify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	mf := fs.String("manifest", "manifest.json", "manifest file")
	sf := fs.String("sig", "manifest.json.sig", "signature file")
	fs.Parse(args)
	body, err := os.ReadFile(*mf)
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(*sf)
	if err != nil {
		return err
	}
	if err := update.Verify(body, sig, update.Keys()); err != nil {
		return err
	}
	m, err := update.Parse(body)
	if err != nil {
		return err
	}
	if err := update.Check(m, 0, time.Now()); err != nil {
		return err
	}
	fmt.Printf("ok  %s: signed by a pinned update key, expires %s\n", m, m.Expires)
	return nil
}
