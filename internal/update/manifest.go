// Package update is the Link's silent self-update (build plan §5.2 and §5.3, slice S5): Firmary's signed update
// manifest, checked in this order: the detached Ed25519 signature against the update keys pinned in this build, the
// sequence (strictly above the last accepted one), the expiry, then each download's SHA-256. A new Link version goes
// into the data directory next to the current one, passes a self-test, and is switched to only between turns; if it
// then fails to start, the launcher rolls back to the previous version. The pinned Claude Code and Codex copies follow
// the manifest's provider pins the same way. Provider sign-ins (profiles) are never touched.
package update

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// PublicKeys are the update-signing public keys pinned in this build, current and next, so the signing key can
// rotate without a release. They are separate from the ticket keys. The private keys live only in Firmary's release
// environment (GitHub Actions secret LINK_UPDATE_SIGNING_KEY) and its 1Password vault.
var PublicKeys = []string{
	"9kksKqaEvhrsOEeOY3ydBJfe4dt4XVhsxDB6uLpQIz8=", // current
	"q07RuF8poOncGLp29Rvu5OQue8EJBfcJzU6v33f2N38=", // next
}

// Keys decodes PublicKeys.
func Keys() []ed25519.PublicKey {
	var out []ed25519.PublicKey
	for _, k := range PublicKeys {
		b, err := base64.StdEncoding.DecodeString(k)
		if err != nil || len(b) != ed25519.PublicKeySize {
			panic("update: malformed pinned key")
		}
		out = append(out, b)
	}
	return out
}

// ManifestURL is where the stable channel's manifest lives; the signature is at ManifestURL + ".sig".
const ManifestURL = "https://github.com/Firmary/bridge/releases/latest/download/manifest.json"

// DownloadBase is where every Link executable a manifest offers must download from: the Bridge release of the
// manifest's own version, DownloadBase + "v" + version + "/" + file. A manifest naming any other host, repository or
// release is malformed, so even a validly signed one can't point a Link elsewhere.
const DownloadBase = "https://github.com/Firmary/bridge/releases/download/"

// DownloadURL is the URL of file in the Bridge release of version.
func DownloadURL(version, file string) string { return DownloadBase + "v" + version + "/" + file }

// Size limits for what the updater reads.
const (
	MaxManifestBytes  = 1 << 20
	MaxSignatureBytes = 1 << 10
	MaxBinaryBytes    = 512 << 20
)

// Manifest is firmary-os packages/validators/src/ai-relay/update-manifest.ts UpdateManifest. Unknown fields are
// ignored, so a newer manifest never stops an older Link from updating.
type Manifest struct {
	Schema           int                         `json:"schema"`
	Sequence         int64                       `json:"sequence"`
	Expires          string                      `json:"expires"`
	Helper           HelperRelease               `json:"helper"`
	MinHelperVersion string                      `json:"minHelperVersion"`
	Providers        map[string]*ProviderRelease `json:"providers"`
	DisabledAdapters []string                    `json:"disabledAdapters"`
	ConsentRevision  int                         `json:"consentRevision"`
	WhatsNew         []WhatsNew                  `json:"whatsNew"`
}

// HelperRelease is the Link release the manifest offers.
type HelperRelease struct {
	Version        string                    `json:"version"`
	RolloutPercent int                       `json:"rolloutPercent"`
	Platforms      map[string]HelperArtifact `json:"platforms"`
}

// HelperArtifact is one platform's Link executable.
type HelperArtifact struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

// ProviderRelease is a provider app pin. Source names how the Link verifies the copy.
type ProviderRelease struct {
	Version   string                      `json:"version"`
	Source    string                      `json:"source"`
	Platforms map[string]ProviderArtifact `json:"platforms"`
}

// ProviderArtifact is one platform's provider executable checksum. ArchiveSHA256 is this Link's addition for a
// provider that ships the executable in an archive (Codex): the manifest schema's objects are open, so it is allowed.
type ProviderArtifact struct {
	SHA256        string `json:"sha256"`
	URL           string `json:"url,omitempty"`
	ArchiveSHA256 string `json:"archiveSha256,omitempty"`
}

// WhatsNew is one release note.
type WhatsNew struct {
	Version string `json:"version"`
	Text    string `json:"text"`
}

// Provider sources the Link knows (firmary-os PROVIDER_RELEASE_SOURCES).
var Sources = map[string]string{"claude-code": "anthropic-release-manifest", "codex": "openai-github-release"}

var (
	versionRe     = regexp.MustCompile(`^\d{1,6}\.\d{1,6}\.\d{1,6}(?:-[0-9A-Za-z.-]{1,64})?$`)
	sha256Re      = regexp.MustCompile(`^[a-f0-9]{64}$`)
	fileNameRe    = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)
	platformKeyRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	machineNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)
)

// Reasons a manifest is refused. They are logged as the reason code.
var (
	ErrSignature = errors.New("bad_signature")
	ErrMalformed = errors.New("malformed")
	ErrReplayed  = errors.New("replayed")
	ErrExpired   = errors.New("expired")
)

// Verify checks sig, a base64 Ed25519 signature over the exact manifest bytes, against any of keys.
func Verify(body, sig []byte, keys []ed25519.PublicKey) error {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil || len(raw) != ed25519.SignatureSize {
		return ErrSignature
	}
	for _, k := range keys {
		if ed25519.Verify(k, body, raw) {
			return nil
		}
	}
	return ErrSignature
}

// Sign returns the detached signature for body, in the form Verify reads.
func Sign(priv ed25519.PrivateKey, body []byte) []byte {
	return []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, body)) + "\n")
}

// Parse decodes a manifest and checks the parts the Link acts on. Anything it would not act on is not checked, so a
// newer manifest stays readable.
func Parse(body []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return m, ErrMalformed
	}
	if m.Schema != 1 || m.Sequence < 1 || !versionRe.MatchString(m.Helper.Version) || m.Helper.RolloutPercent < 0 ||
		m.Helper.RolloutPercent > 100 || !versionRe.MatchString(m.MinHelperVersion) {
		return m, ErrMalformed
	}
	if _, err := time.Parse(time.RFC3339, m.Expires); err != nil {
		return m, ErrMalformed
	}
	for k, a := range m.Helper.Platforms {
		file, inRelease := strings.CutPrefix(a.URL, DownloadURL(m.Helper.Version, ""))
		if !platformKeyRe.MatchString(k) || !inRelease || !fileNameRe.MatchString(file) || !sha256Re.MatchString(a.SHA256) {
			return m, ErrMalformed
		}
	}
	for _, a := range m.DisabledAdapters {
		if !machineNameRe.MatchString(a) {
			return m, ErrMalformed
		}
	}
	return m, nil
}

// Check refuses a manifest at or below the last accepted sequence (a replay; equal means nothing new and is
// reported as ErrReplayed too, which the caller treats as "already applied") or past its expiry.
func Check(m Manifest, lastSequence int64, now time.Time) error {
	if m.Sequence <= lastSequence {
		return ErrReplayed
	}
	exp, _ := time.Parse(time.RFC3339, m.Expires)
	if !now.Before(exp) {
		return ErrExpired
	}
	return nil
}

// Pin returns the manifest's pin for a provider, if it names one this Link can verify: a known source, a release
// version, and checksums in the right form. ok is false otherwise, and the Link keeps its current pin.
func (m Manifest) Pin(provider string) (version string, sha, archive map[string]string, ok bool) {
	p := m.Providers[provider]
	if p == nil || p.Source != Sources[provider] || !versionRe.MatchString(p.Version) || strings.Contains(p.Version, "-") {
		return "", nil, nil, false
	}
	sha, archive = map[string]string{}, map[string]string{}
	for k, a := range p.Platforms {
		if !platformKeyRe.MatchString(k) || !sha256Re.MatchString(a.SHA256) {
			return "", nil, nil, false
		}
		sha[k] = a.SHA256
		if a.ArchiveSHA256 != "" {
			if !sha256Re.MatchString(a.ArchiveSHA256) {
				return "", nil, nil, false
			}
			archive[k] = a.ArchiveSHA256
		}
	}
	return p.Version, sha, archive, true
}

// Platform is the manifest's platform key for this Link build: darwin-universal, windows-x64, linux-arm64, ...
func Platform(goos, goarch string) string {
	if goos == "darwin" {
		return "darwin-universal"
	}
	arch := goarch
	if arch == "amd64" {
		arch = "x64"
	}
	return goos + "-" + arch
}

// ThisPlatform is Platform for the running build.
func ThisPlatform() string { return Platform(runtime.GOOS, runtime.GOARCH) }

// Bucket places a device in 0..99 for staged rollouts, by a stable hash of its device public key.
func Bucket(devicePublicKey []byte) int {
	h := sha256.New()
	h.Write([]byte("firmary-link-rollout-v1\n"))
	h.Write(devicePublicKey)
	return int(binary.BigEndian.Uint64(h.Sum(nil)[:8]) % 100)
}

// InRollout reports whether a device in bucket gets a release rolled out to percent of devices.
func InRollout(bucket, percent int) bool { return bucket < percent }

// Compare compares release versions as firmary-os versionAtLeast does: numerically, and a pre-release below its
// release. Malformed versions compare below everything.
func Compare(a, b string) int {
	pa, oka := parseVersion(a)
	pb, okb := parseVersion(b)
	switch {
	case !oka && !okb:
		return 0
	case !oka:
		return -1
	case !okb:
		return 1
	}
	for i := 0; i < 3; i++ {
		if pa.n[i] != pb.n[i] {
			if pa.n[i] < pb.n[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case pa.pre == pb.pre:
		return 0
	case pa.pre == "":
		return 1
	case pb.pre == "":
		return -1
	case pa.pre < pb.pre:
		return -1
	}
	return 1
}

type version struct {
	n   [3]int
	pre string
}

func parseVersion(s string) (version, bool) {
	var v version
	if !versionRe.MatchString(s) {
		return v, false
	}
	core, pre, _ := strings.Cut(s, "-")
	v.pre = pre
	for i, p := range strings.SplitN(core, ".", 3) {
		n, err := strconv.Atoi(p)
		if err != nil {
			return v, false
		}
		v.n[i] = n
	}
	return v, true
}

// IsRelease reports whether v is a plain release version (no pre-release part). Development and pre-release builds
// never update themselves.
func IsRelease(v string) bool { return versionRe.MatchString(v) && !strings.Contains(v, "-") }

// String describes a manifest for errors in tests.
func (m Manifest) String() string {
	return fmt.Sprintf("manifest %d (%s)", m.Sequence, m.Helper.Version)
}
