// Package pairing stores which Firmary user and firm this device is paired to, and derives the pairing code the
// person compares between the Helper and Firmary.
package pairing

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// Record is the local pairing. It holds ids only; there is no secret in it.
type Record struct {
	DeviceID string `json:"deviceId"`
	UserID   string `json:"userId"`
	FirmID   string `json:"firmId"`
	Env      string `json:"env"`
}

var idRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ValidID reports whether s is a well-formed id. Ids are used in file paths, so this is strict.
func ValidID(s string) bool { return idRe.MatchString(s) }

// Validate checks every id.
func (r Record) Validate() error {
	for name, v := range map[string]string{"deviceId": r.DeviceID, "userId": r.UserID, "firmId": r.FirmID, "env": r.Env} {
		if !ValidID(v) {
			return fmt.Errorf("pairing %s is not a valid id", name)
		}
	}
	return nil
}

// Load reads the record. A missing file returns (nil, nil): the device is not paired.
func Load(path string) (*Record, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r Record
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, errors.New("pairing file is corrupt")
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return &r, nil
}

// Save writes the record atomically with mode 0600.
func Save(path string, r Record) error {
	if err := r.Validate(); err != nil {
		return err
	}
	b, _ := json.Marshal(r)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".pairing-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_ = tmp.Chmod(0o600)
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Delete removes the record.
func Delete(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// codeContext domain-separates the pairing code hash.
const codeContext = "firmary-helper-pair-v1"

var crockford = base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding)

// Code is the pairing code for a device public key and the relay's challenge nonce: 40 bits, shown as XXXX-XXXX.
// The Helper computes it itself, so a relay can't make it display a code that belongs to a different key, and
// Firmary computes the same value for the pending key. The person confirms that the two match.
func Code(pub ed25519.PublicKey, nonce []byte) string {
	h := sha256.New()
	h.Write([]byte(codeContext))
	h.Write([]byte{0})
	h.Write(pub)
	h.Write(nonce)
	s := crockford.EncodeToString(h.Sum(nil)[:5])
	return s[:4] + "-" + s[4:]
}
