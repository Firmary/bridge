// Package pairing stores which Firmary user and firm this device is paired to, normalises the pairing code a person
// types (pairing v2: Firmary shows the code and the person types it into the Link), and derives the v1 code-match
// code.
//
// The code is a commit-then-reveal short authentication string. On each connection the Link picks a fresh
// random helper nonce Nh and sends Commit(pub, Nh) with its auth message. Only then does the relay send its
// pairing nonce Nr, and only after that does the Link reveal Nh. Code = H(pub, Nr, Nh). Each side's randomness is
// fixed before it sees the other's, so a relay impersonator can't grind a nonce that makes the Link show the code
// of a different key: it gets one 2^-40 guess per attempt.
package pairing

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
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

// Domain separation for the commitment and the code.
const (
	commitContext = "firmary-helper-pair-commit-v1"
	codeContext   = "firmary-helper-pair-v2"
)

// NonceSize is the exact size of both pairing nonces (Nh and Nr).
const NonceSize = 32

const crockfordAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var crockford = base32.NewEncoding(crockfordAlphabet).WithPadding(base32.NoPadding)

// NewNonce returns a fresh helper nonce Nh. Use a new one on every connection; never reuse one once revealed.
func NewNonce() ([]byte, error) {
	b := make([]byte, NonceSize)
	_, err := rand.Read(b)
	return b, err
}

// Commit is SHA-256(commitContext || 0x00 || pub || Nh): the commitment to Nh sent before the relay's nonce.
func Commit(pub ed25519.PublicKey, helperNonce []byte) []byte {
	h := sha256.New()
	h.Write([]byte(commitContext))
	h.Write([]byte{0})
	h.Write(pub)
	h.Write(helperNonce)
	return h.Sum(nil)
}

// Code is the pairing code: the first 40 bits of SHA-256(codeContext || 0x00 || pub || Nr || Nh), Crockford
// base32, shown as XXXX-XXXX. Every input has a fixed size (32 bytes each), so the concatenation is unambiguous.
// The Link computes it itself and Firmary computes the same value for the pending key; the person confirms that
// the two match.
func Code(pub ed25519.PublicKey, relayNonce, helperNonce []byte) string {
	h := sha256.New()
	h.Write([]byte(codeContext))
	h.Write([]byte{0})
	h.Write(pub)
	h.Write(relayNonce)
	h.Write(helperNonce)
	s := crockford.EncodeToString(h.Sum(nil)[:5])
	return s[:4] + "-" + s[4:]
}

// ErrBadCode is NormalizeCode's error: what was typed isn't eight characters of a pairing code.
var ErrBadCode = errors.New("a pairing code is eight letters and digits, like ABCD-EFGH")

// NormalizeCode turns a pairing code as a person typed it into the form the relay checks (pairing v2): upper case,
// spaces and dashes removed, the look-alikes I and L read as 1 and O as 0, then XXXX-XXXX in Crockford base 32.
func NormalizeCode(typed string) (string, error) {
	var b strings.Builder
	for _, r := range strings.ToUpper(typed) {
		switch {
		case unicode.IsSpace(r) || unicode.Is(unicode.Pd, r):
			continue
		case r == 'I' || r == 'L':
			r = '1'
		case r == 'O':
			r = '0'
		}
		if r > unicode.MaxASCII || !strings.ContainsRune(crockfordAlphabet, r) {
			return "", ErrBadCode
		}
		b.WriteRune(r)
	}
	s := b.String()
	if len(s) != 8 {
		return "", ErrBadCode
	}
	return s[:4] + "-" + s[4:], nil
}
