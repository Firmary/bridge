package update

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"

	"github.com/Firmary/bridge/internal/brand"
	"github.com/Firmary/bridge/internal/driver"
	"github.com/Firmary/bridge/internal/fsperm"
	"github.com/Firmary/bridge/internal/protocol"
)

// LaunchedEnv is set in the environment of a Link the launcher (or an update) started from the versions directory,
// so it doesn't launch again. ReasonEnv carries why it started ("update" after a self-update).
const (
	LaunchedEnv = "FIRMARY_LINK_LAUNCHED"
	ReasonEnv   = "FIRMARY_LINK_START_REASON"
)

// maxTrialStarts is how many times a new version may start without proving itself before the launcher rolls back.
const maxTrialStarts = 2

// State is what the updater remembers, in <dir>/state.json. It never holds anything secret.
type State struct {
	// Sequence and ManifestSHA256 are the last accepted manifest's.
	Sequence       int64  `json:"sequence,omitempty"`
	ManifestSHA256 string `json:"manifestSha256,omitempty"`
	// Providers are the provider pins taken from accepted manifests and installed.
	Providers map[string]driver.Pin `json:"providers,omitempty"`
	// DisabledAdapters are the adapters the last accepted manifest paused.
	DisabledAdapters []string `json:"disabledAdapters,omitempty"`

	// Current is the version in versions/ the launcher runs, with its executable's checksum; empty means the
	// installed executable. Previous is the one to roll back to.
	Current        string `json:"current,omitempty"`
	CurrentSHA256  string `json:"currentSha256,omitempty"`
	Previous       string `json:"previous,omitempty"`
	PreviousSHA256 string `json:"previousSha256,omitempty"`
	// Trial is true from the switch to Current until it has run long enough to be kept; TrialStarts counts its
	// starts meanwhile.
	Trial       bool `json:"trial,omitempty"`
	TrialStarts int  `json:"trialStarts,omitempty"`
	// Skip is a version that failed its self-test or rolled back; it isn't tried again.
	Skip string `json:"skip,omitempty"`
	// Failure is a rolled-back update not yet reported in a hello.
	Failure *protocol.UpdateFailure `json:"failure,omitempty"`
}

// LoadState reads dir/state.json; a missing file is an empty state.
func LoadState(dir string) (State, error) {
	var st State
	b, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return State{}, err
	}
	return st, nil
}

// SaveState replaces dir/state.json atomically.
func SaveState(dir string, st State) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return driver.WriteFileAtomic(filepath.Join(dir, "state.json"), b)
}

// ExeName is the Link's executable name on goos.
func ExeName(goos string) string {
	if goos == "windows" {
		return brand.Command + ".exe"
	}
	return brand.Command
}

// Binary is where version v's executable lives under dir.
func Binary(dir, v string) string {
	return filepath.Join(dir, "versions", v, ExeName(runtime.GOOS))
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

// rollBack goes back to the previous version and records why.
func rollBack(st *State, pattern string) {
	st.Failure = &protocol.UpdateFailure{Version: st.Current, Pattern: pattern}
	st.Skip = st.Current
	st.Current, st.CurrentSHA256 = st.Previous, st.PreviousSHA256
	st.Previous, st.PreviousSHA256 = "", ""
	st.Trial, st.TrialStarts = false, 0
}

// Choose is the launcher: it decides which executable runs, given the installed one's version. It returns the path
// of a newer version in dir to start instead, or "" to carry on in this process. It counts the starts of a version
// on trial and rolls back to the previous one when a new version keeps failing to start, or when its executable no
// longer matches the checksum it was installed with. An installed executable newer than the updates (the person
// installed a new release) wins.
//
// dir is the data directory's update directory. Nothing in it is read or run unless the data directory, dir, and
// the chosen version's directories and executable belong to the person and no one else can write them: another local
// user who could would otherwise choose what this process starts, before Agent.Open checks the same directories.
func Choose(dir, installed string) string {
	if !private(filepath.Dir(dir), dir) {
		return ""
	}
	st, err := LoadState(dir)
	if err != nil {
		return ""
	}
	for st.Current != "" {
		if Compare(st.Current, installed) <= 0 {
			st.Current, st.CurrentSHA256, st.Previous, st.PreviousSHA256, st.Trial, st.TrialStarts = "", "", "", "", false, 0
			break
		}
		if st.Trial {
			if st.TrialStarts >= maxTrialStarts {
				rollBack(&st, "start_failed")
				continue
			}
			st.TrialStarts++
		}
		path := Binary(dir, st.Current)
		if !private(filepath.Dir(filepath.Dir(path)), filepath.Dir(path), path) {
			return ""
		}
		if got, err := fileSHA256(path); err != nil || got != st.CurrentSHA256 {
			rollBack(&st, "checksum_mismatch")
			continue
		}
		if SaveState(dir, st) != nil {
			return ""
		}
		return path
	}
	_ = SaveState(dir, st)
	return ""
}

// private reports whether every path belongs to the current user and no one else can write it.
func private(paths ...string) bool {
	for _, p := range paths {
		if fsperm.CheckOwned(p, 0o022) != nil {
			return false
		}
	}
	return true
}

// Commit keeps a version on trial: the running version has started, reached Firmary or run for a while.
func Commit(dir, running string) error {
	st, err := LoadState(dir)
	if err != nil || !st.Trial || st.Current != running {
		return err
	}
	st.Trial, st.TrialStarts = false, 0
	return SaveState(dir, st)
}

// TakeFailure returns a rolled-back update to report in hello, once.
func TakeFailure(dir string) *protocol.UpdateFailure {
	st, err := LoadState(dir)
	if err != nil || st.Failure == nil {
		return nil
	}
	f := st.Failure
	st.Failure = nil
	if SaveState(dir, st) != nil {
		return nil
	}
	return f
}
