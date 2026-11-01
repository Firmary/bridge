package claudecode

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/ProtonMail/go-crypto/openpgp"

	"github.com/Firmary/firmary-helper/internal/driver"
)

// Anthropic's Claude Code release signing key, from https://downloads.claude.ai/keys/claude-code.asc.
// Fingerprint 31DD DE24 DDFA B679 F42D 7BD2 BAA9 29FF 1A7E CACE, as published in the Claude Code setup docs
// ("Binary integrity and code signing").
//
//go:embed anthropic-release-key.asc
var anthropicReleaseKey []byte

const anthropicReleaseFingerprint = "31DDDE24DDFAB679F42D7BD2BAA929FF1A7ECACE"

// ReleaseBaseURL is Anthropic's release bucket.
const ReleaseBaseURL = "https://downloads.claude.ai/claude-code-releases"

const (
	maxManifestBytes = 1 << 20
	maxSigBytes      = 64 << 10
	maxBinaryBytes   = 1 << 30
)

var (
	versionRe = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	sha256Re  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// AnthropicKeyring parses the embedded release key and checks its fingerprint.
func AnthropicKeyring() (openpgp.EntityList, error) {
	kr, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(anthropicReleaseKey))
	if err != nil {
		return nil, err
	}
	if len(kr) != 1 || strings.ToUpper(hex.EncodeToString(kr[0].PrimaryKey.Fingerprint)) != anthropicReleaseFingerprint {
		return nil, errors.New("embedded Anthropic release key has an unexpected fingerprint")
	}
	return kr, nil
}

// Platform is Anthropic's platform name for this machine, e.g. "linux-x64", "darwin-arm64", "win32-x64".
func Platform() string {
	arch := map[string]string{"amd64": "x64", "arm64": "arm64"}[runtime.GOARCH]
	switch runtime.GOOS {
	case "darwin":
		return "darwin-" + arch
	case "windows":
		return "win32-" + arch
	case "linux":
		if m, _ := filepath.Glob("/lib/ld-musl-*.so.1"); len(m) > 0 {
			return "linux-" + arch + "-musl"
		}
		return "linux-" + arch
	}
	return runtime.GOOS + "-" + arch
}

func binaryName(platform string) string {
	if strings.HasPrefix(platform, "win32-") {
		return "claude.exe"
	}
	return "claude"
}

type releaseManifest struct {
	Version   string `json:"version"`
	Platforms map[string]struct {
		Binary   string `json:"binary"`
		Checksum string `json:"checksum"`
		Size     int64  `json:"size"`
	} `json:"platforms"`
}

// Ensure installs the pinned Claude Code into Root/versions/<version>/ if it isn't there, and verifies it either
// way. A new download must pass, in order: Anthropic's GPG signature on the release manifest, the manifest's
// version, the manifest checksum equal to Firmary's pin, and the downloaded binary's size and SHA-256.
func (d *Driver) Ensure(ctx context.Context, pin driver.Pin) error {
	if !versionRe.MatchString(pin.Version) {
		return errors.New("pin version is malformed")
	}
	want := pin.SHA256[d.platform]
	if !sha256Re.MatchString(want) {
		return fmt.Errorf("pin has no checksum for platform %s", d.platform)
	}
	dir := filepath.Join(d.root, "versions", pin.Version)
	dest := filepath.Join(dir, binaryName(d.platform))
	if got, err := fileSHA256(dest); err == nil && got == want {
		d.setInstalled(pin.Version, dest)
		return nil
	}

	base := d.baseURL + "/" + pin.Version
	manifest, err := d.get(ctx, base+"/manifest.json", maxManifestBytes)
	if err != nil {
		return fmt.Errorf("release manifest: %w", err)
	}
	sig, err := d.get(ctx, base+"/manifest.json.sig", maxSigBytes)
	if err != nil {
		return fmt.Errorf("release manifest signature: %w", err)
	}
	if err := checkSignature(d.keyring, manifest, sig); err != nil {
		return err
	}
	var m releaseManifest
	if err := json.Unmarshal(manifest, &m); err != nil {
		return errors.New("release manifest is malformed")
	}
	if m.Version != pin.Version {
		return errors.New("release manifest version differs from the pin")
	}
	p, ok := m.Platforms[d.platform]
	if !ok || p.Checksum != want {
		return errors.New("release manifest checksum differs from the pin")
	}
	if p.Size <= 0 || p.Size > maxBinaryBytes || p.Binary != binaryName(d.platform) {
		return errors.New("release manifest entry is malformed")
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".download-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	got, n, err := d.download(ctx, base+"/"+d.platform+"/"+p.Binary, tmp, p.Size)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	if n != p.Size || got != want {
		return errors.New("downloaded binary does not match the pinned checksum")
	}
	if err := os.Chmod(tmp.Name(), 0o700); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), dest); err != nil {
		return err
	}
	d.setInstalled(pin.Version, dest)
	return nil
}

func checkSignature(kr openpgp.EntityList, manifest, sig []byte) error {
	var err error
	if bytes.HasPrefix(bytes.TrimSpace(sig), []byte("-----BEGIN")) {
		_, err = openpgp.CheckArmoredDetachedSignature(kr, bytes.NewReader(manifest), bytes.NewReader(sig), nil)
	} else {
		_, err = openpgp.CheckDetachedSignature(kr, bytes.NewReader(manifest), bytes.NewReader(sig), nil)
	}
	if err != nil {
		return errors.New("release manifest signature is not valid for Anthropic's release key")
	}
	return nil
}

func (d *Driver) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := d.http.Do(req)
	if err != nil {
		return nil, errors.New("request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, errors.New("read failed")
	}
	if int64(len(b)) > limit {
		return nil, errors.New("response too large")
	}
	return b, nil
}

func (d *Driver) download(ctx context.Context, url string, w io.Writer, size int64) (string, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", 0, err
	}
	resp, err := d.http.Do(req)
	if err != nil {
		return "", 0, errors.New("request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("status %d", resp.StatusCode)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, h), io.LimitReader(resp.Body, size+1))
	if err != nil {
		return "", n, errors.New("read failed")
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
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
