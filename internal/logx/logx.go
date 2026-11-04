// Package logx is the Helper's only local log.
//
// It accepts a closed set of metadata fields (the "Logged" column of the never-log list) and has no free-text
// field, so prompts, context, outputs, raw provider errors, environment variables, credentials, sign-in URLs and
// codes have nowhere to go. Every string field must look like an identifier (no spaces, bounded length); anything
// else is written as "[invalid]". The lint test in this package keeps other packages from logging another way.
package logx

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

// Event is a log event type. Use the constants below.
type Event string

const (
	HelperStart      Event = "helper_start"
	HelperStop       Event = "helper_stop"
	RelayConnect     Event = "relay_connect"
	RelayDisconnect  Event = "relay_disconnect"
	RelayAuthed      Event = "relay_authenticated"
	PairingPending   Event = "pairing_pending"
	PairingOffered   Event = "pairing_offered"
	PairingAccepted  Event = "pairing_accepted"
	PairingDeclined  Event = "pairing_declined"
	Paired           Event = "paired"
	Revoked          Event = "revoked"
	HelloSent        Event = "hello_sent"
	UnknownMessage   Event = "unknown_message"
	ProviderEnsure   Event = "provider_ensure"
	ProviderStatus   Event = "provider_status"
	TurnRejected     Event = "turn_rejected"
	TurnStart        Event = "turn_start"
	TurnDone         Event = "turn_done"
	TurnError        Event = "turn_error"
	TurnCancel       Event = "turn_cancel"
	ProviderUsage    Event = "provider_usage"
	ProviderLimitHit Event = "provider_limit"
)

// Window is one provider limit window, as the provider reports it.
type Window struct {
	Name        string   `json:"name"`
	UsedPercent *float64 `json:"usedPercent,omitempty"`
	ResetsAt    string   `json:"resetsAt,omitempty"`
}

// Fields is everything a log line may carry. There is deliberately no message or free-text field.
type Fields struct {
	TicketID     string   `json:"ticketId,omitempty"`
	DeviceID     string   `json:"deviceId,omitempty"`
	Provider     string   `json:"provider,omitempty"`
	Model        string   `json:"model,omitempty"`
	Version      string   `json:"version,omitempty"`
	DurationMs   int64    `json:"durationMs,omitempty"`
	InputTokens  int64    `json:"inputTokens,omitempty"`
	OutputTokens int64    `json:"outputTokens,omitempty"`
	Windows      []Window `json:"windows,omitempty"`
	ErrorClass   string   `json:"errorClass,omitempty"`
	Pattern      string   `json:"pattern,omitempty"`
	Reason       string   `json:"reason,omitempty"`
	Attempt      int      `json:"attempt,omitempty"`
}

const (
	// MaxFileBytes and Files are the rotation policy: 5 files of 1 MB.
	MaxFileBytes = 1 << 20
	Files        = 5
	fileName     = "helper.log"
)

var safe = regexp.MustCompile(`^[A-Za-z0-9._:@/+-]{0,128}$`)

func clean(s string) string {
	if safe.MatchString(s) {
		return s
	}
	return "[invalid]"
}

func (f Fields) sanitized() Fields {
	f.TicketID, f.DeviceID, f.Provider, f.Model = clean(f.TicketID), clean(f.DeviceID), clean(f.Provider), clean(f.Model)
	f.Version, f.ErrorClass, f.Pattern, f.Reason = clean(f.Version), clean(f.ErrorClass), clean(f.Pattern), clean(f.Reason)
	if f.Windows != nil {
		ws := make([]Window, len(f.Windows))
		for i, w := range f.Windows {
			ws[i] = Window{Name: clean(w.Name), UsedPercent: w.UsedPercent, ResetsAt: clean(w.ResetsAt)}
		}
		f.Windows = ws
	}
	return f
}

// Logger writes JSON lines to dir/helper.log with size-based rotation. A nil *Logger discards.
type Logger struct {
	mu   sync.Mutex
	dir  string
	f    *os.File
	size int64
	now  func() time.Time
}

// Open creates dir (0700) and opens the log for appending.
func Open(dir string) (*Logger, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	l := &Logger{dir: dir, now: time.Now}
	if err := l.open(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *Logger) open() error {
	f, err := os.OpenFile(filepath.Join(l.dir, fileName), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	l.f, l.size = f, st.Size()
	return nil
}

func (l *Logger) rotate() error {
	l.f.Close()
	for i := Files - 1; i >= 1; i-- {
		from := filepath.Join(l.dir, fileName)
		if i > 1 {
			from = fmt.Sprintf("%s.%d", from, i-1)
		}
		to := fmt.Sprintf("%s.%d", filepath.Join(l.dir, fileName), i)
		if _, err := os.Stat(from); err == nil {
			if err := os.Rename(from, to); err != nil {
				return err
			}
		}
	}
	return l.open()
}

// Log writes one event. Errors writing the log are dropped: logging must never break a turn.
func (l *Logger) Log(ev Event, f Fields) {
	if l == nil {
		return
	}
	line, err := json.Marshal(struct {
		TS string `json:"ts"`
		Ev string `json:"ev"`
		Fields
	}{l.now().UTC().Format(time.RFC3339Nano), clean(string(ev)), f.sanitized()})
	if err != nil {
		return
	}
	line = append(line, '\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return
	}
	if l.size+int64(len(line)) > MaxFileBytes && l.size > 0 {
		if err := l.rotate(); err != nil {
			return
		}
	}
	n, _ := l.f.Write(line)
	l.size += int64(n)
}

// Close closes the log file.
func (l *Logger) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}
