package codex

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/Firmary/bridge/internal/driver"
)

// ReleaseBaseURL is OpenAI's official Codex release download location on GitHub.
const ReleaseBaseURL = "https://github.com/openai/codex/releases/download"

const (
	maxArchiveBytes = 1 << 30
	maxBinaryBytes  = 1 << 30
)

var (
	versionRe = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	sha256Re  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// targets maps the Helper's platform names (the same as the Claude Code driver's) to Codex's release targets. Linux
// builds are static (musl), so they run on glibc systems too.
var targets = map[string]string{
	"darwin-arm64": "aarch64-apple-darwin",
	"darwin-x64":   "x86_64-apple-darwin",
	"linux-arm64":  "aarch64-unknown-linux-musl",
	"linux-x64":    "x86_64-unknown-linux-musl",
	"win32-arm64":  "aarch64-pc-windows-msvc",
	"win32-x64":    "x86_64-pc-windows-msvc",
}

// Platform is this machine's platform name, e.g. "linux-x64", "darwin-arm64", "win32-x64".
func Platform() string {
	arch := map[string]string{"amd64": "x64", "arm64": "arm64"}[runtime.GOARCH]
	switch runtime.GOOS {
	case "darwin":
		return "darwin-" + arch
	case "windows":
		return "win32-" + arch
	}
	return runtime.GOOS + "-" + arch
}

func isWindows(platform string) bool { return strings.HasPrefix(platform, "win32-") }

func binaryName(platform string) string {
	if isWindows(platform) {
		return "codex.exe"
	}
	return "codex"
}

// releaseAsset is the release archive's name and the name of the one executable inside it, for example
// codex-x86_64-unknown-linux-musl.tar.gz holding codex-x86_64-unknown-linux-musl. Only that executable is installed:
// none of the companion tools of the full codex-package (exec server, code-mode host, ripgrep, bubblewrap, voice).
func releaseAsset(platform string) (archive, member string, ok bool) {
	t, ok := targets[platform]
	if !ok {
		return "", "", false
	}
	member = "codex-" + t
	if isWindows(platform) {
		member += ".exe"
	}
	return member + ".tar.gz", member, true
}

// Ensure installs the pinned Codex into Root/versions/<version>/ if it isn't there, and verifies it either way. A
// new download must pass, in order: the release archive's size limit and SHA-256 equal to Firmary's pin, an archive
// holding exactly the one expected executable, and that executable's SHA-256 equal to Firmary's pin.
func (d *Driver) Ensure(ctx context.Context, pin driver.Pin) error {
	if !versionRe.MatchString(pin.Version) {
		return errors.New("pin version is malformed")
	}
	want, wantArchive := pin.SHA256[d.platform], pin.ArchiveSHA256[d.platform]
	archive, member, ok := releaseAsset(d.platform)
	if !ok || !sha256Re.MatchString(want) || !sha256Re.MatchString(wantArchive) {
		return fmt.Errorf("pin has no checksum for platform %s", d.platform)
	}
	dir := filepath.Join(d.root, "versions", pin.Version)
	dest := filepath.Join(dir, binaryName(d.platform))
	if got, err := fileSHA256(dest); err == nil && got == want {
		d.setInstalled(pin.Version, dest)
		return nil
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	arc, err := os.CreateTemp(dir, ".download-*")
	if err != nil {
		return err
	}
	defer os.Remove(arc.Name())
	got, err := d.download(ctx, d.baseURL+"/rust-v"+pin.Version+"/"+archive, arc)
	if err != nil {
		arc.Close()
		return fmt.Errorf("download: %w", err)
	}
	if got != wantArchive {
		arc.Close()
		return errors.New("downloaded archive does not match the pinned checksum")
	}
	if _, err := arc.Seek(0, io.SeekStart); err != nil {
		arc.Close()
		return err
	}
	bin, err := os.CreateTemp(dir, ".binary-*")
	if err != nil {
		arc.Close()
		return err
	}
	defer os.Remove(bin.Name())
	got, err = extract(arc, member, bin)
	arc.Close()
	if cerr := bin.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if got != want {
		return errors.New("extracted binary does not match the pinned checksum")
	}
	if err := os.Chmod(bin.Name(), 0o700); err != nil {
		return err
	}
	if err := os.Rename(bin.Name(), dest); err != nil {
		return err
	}
	d.setInstalled(pin.Version, dest)
	return nil
}

// extract copies the archive's single regular file named member to w and returns its SHA-256. Any other entry
// (another file, a directory, a link) fails, so the archive can't place anything else.
func extract(r io.Reader, member string, w io.Writer) (string, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return "", errors.New("release archive is malformed")
	}
	tr := tar.NewReader(gz)
	sum := ""
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", errors.New("release archive is malformed")
		}
		if sum != "" || h.Name != member || h.Typeflag != tar.TypeReg || h.Size <= 0 || h.Size > maxBinaryBytes {
			return "", errors.New("release archive has unexpected contents")
		}
		hash := sha256.New()
		n, err := io.Copy(io.MultiWriter(w, hash), io.LimitReader(tr, maxBinaryBytes+1))
		if err != nil || n != h.Size {
			return "", errors.New("release archive is malformed")
		}
		sum = hex.EncodeToString(hash.Sum(nil))
	}
	if sum == "" {
		return "", errors.New("release archive has unexpected contents")
	}
	return sum, nil
}

func (d *Driver) download(ctx context.Context, url string, w io.Writer) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := d.http.Do(req)
	if err != nil {
		return "", errors.New("request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, h), io.LimitReader(resp.Body, maxArchiveBytes+1))
	if err != nil {
		return "", errors.New("read failed")
	}
	if n > maxArchiveBytes {
		return "", errors.New("response too large")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
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
