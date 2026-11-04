// Package devicekey holds the Link's Ed25519 device key.
//
// The key is the only long-lived credential the Link has, and it is not a Firmary secret: the relay knows only
// its public half. In this slice the private key is a 0600 file (the headless/VPS form in the protocol). Desktop
// installs move it into the OS keychain in a later slice.
package devicekey

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Firmary/bridge/internal/fsperm"
)

// AuthContext is the domain-separation prefix for relay challenge signatures.
const AuthContext = "firmary-helper-auth-v1"

// Key is a loaded device key.
type Key struct {
	priv ed25519.PrivateKey
}

// LoadOrCreate loads the key at path, or creates one if no file exists. On Unix a key file not owned by the current
// user, or accessible to anyone else, is refused rather than silently used.
func LoadOrCreate(path string) (*Key, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return create(path)
	}
	if err != nil {
		return nil, err
	}
	if err := fsperm.CheckOwned(path, 0o077); err != nil {
		return nil, fmt.Errorf("device key: %w", err)
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("device key file is corrupt")
	}
	return &Key{priv: ed25519.NewKeyFromSeed(seed)}, nil
}

func create(path string) (*Key, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(dir, ".devicekey-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		tmp.Close()
		return nil, err
	}
	if _, err := tmp.WriteString(base64.StdEncoding.EncodeToString(priv.Seed()) + "\n"); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	// Link instead of rename so a concurrent creator can't be overwritten: the first key wins.
	if err := os.Link(tmp.Name(), path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return LoadOrCreate(path)
		}
		if err := os.Rename(tmp.Name(), path); err != nil {
			return nil, err
		}
	}
	return &Key{priv: priv}, nil
}

// Delete removes the key file. Used when the device is unpaired.
func Delete(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Public returns the public key.
func (k *Key) Public() ed25519.PublicKey { return k.priv.Public().(ed25519.PublicKey) }

// AuthPayload is the exact byte string signed in answer to a relay challenge. It binds the relay URL so a
// signature made for one relay can't be replayed to another.
func AuthPayload(relayURL string, nonce []byte) []byte {
	return []byte(AuthContext + "\n" + relayURL + "\n" + base64.RawURLEncoding.EncodeToString(nonce))
}

// SignAuth signs a relay challenge.
func (k *Key) SignAuth(relayURL string, nonce []byte) []byte {
	return ed25519.Sign(k.priv, AuthPayload(relayURL, nonce))
}
