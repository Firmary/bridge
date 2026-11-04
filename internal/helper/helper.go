// Package helper decides whether a turn may run, then runs it with the driver of the provider app its ticket names. It is the Link's half of
// "only you, for your own actions": a turn runs only for a valid, unused ticket issued for this device and its
// paired user and firm, for exactly this prompt, on a provider account type the ticket allows.
package helper

import (
	"context"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"time"

	"github.com/Firmary/bridge/internal/driver"
	"github.com/Firmary/bridge/internal/launch"
	"github.com/Firmary/bridge/internal/logx"
	"github.com/Firmary/bridge/internal/pairing"
	"github.com/Firmary/bridge/internal/protocol"
	"github.com/Firmary/bridge/internal/ticket"
)

// Adapter ids: the router adapters this Link serves, one per provider app.
const (
	AdapterClaudeCode = "helper-claude-code"
	AdapterCodex      = "helper-codex"
)

// AdapterID is the Claude Code adapter (kept for callers from before Codex).
const AdapterID = AdapterClaudeCode

// Provider is one provider app the Link runs turns on: the adapter a ticket names and the driver that serves it.
type Provider struct {
	Adapter string
	Driver  driver.Driver
}

// Helper handles turns for one device (the Link's turn policy).
type Helper struct {
	// Providers are the provider apps, in the order hello reports them.
	Providers []Provider
	Verifier  *ticket.Verifier
	DataDir   string
	Version   string
	Ring      string
	Log       *logx.Logger
	// TurnTimeout bounds one turn; zero means DefaultTurnTimeout.
	TurnTimeout time.Duration
	// Disabled, if set, reports an adapter Firmary's signed update manifest paused (disabledAdapters).
	Disabled func(adapter string) bool
	// HelloExtra, if set, adds the reconnect reason and any rolled-back update to a hello.
	HelloExtra func(*protocol.Hello)

	mu      sync.Mutex
	running map[string]context.CancelFunc
	held    bool
}

// MaxConcurrentTurns is one in this slice: one turn at a time across the provider apps.
const MaxConcurrentTurns = 1

// DefaultTurnTimeout ends a turn that hangs (for example a stalled provider connection), so it can't keep the
// Link busy indefinitely.
const DefaultTurnTimeout = 15 * time.Minute

// Profile is the paired user's profile directory for a provider app. User ids are validated by the pairing record.
func Profile(dataDir string, rec pairing.Record, providerID string) driver.Profile {
	return driver.Profile{UserID: rec.UserID, Dir: filepath.Join(dataDir, "profiles", rec.UserID, providerID)}
}

// Profile is the paired user's profile for d.
func (h *Helper) Profile(rec pairing.Record, d driver.Driver) driver.Profile {
	return Profile(h.DataDir, rec, d.ID())
}

// provider returns the provider serving adapter.
func (h *Helper) provider(adapter string) (Provider, bool) {
	for _, p := range h.Providers {
		if p.Adapter == adapter {
			return p, true
		}
	}
	return Provider{}, false
}

// Hello reports versions and the state of every provider app.
func (h *Helper) Hello(ctx context.Context, rec pairing.Record) protocol.Hello {
	infos := []protocol.ProviderInfo{}
	for _, p := range h.Providers {
		st, err := p.Driver.Status(ctx, h.Profile(rec, p.Driver))
		if err != nil {
			h.Log.Log(logx.ProviderStatus, logx.Fields{Provider: p.Driver.ID(), Reason: "status_failed"})
		}
		infos = append(infos, protocol.ProviderInfo{
			ID: p.Driver.ID(), PinnedVersion: st.Installed, SignedIn: st.SignedIn, AccountType: st.AccountType,
			OrgID: st.OrgID, Managed: st.Managed,
		})
	}
	hello := protocol.Hello{
		Type:          protocol.TypeHello,
		HelperVersion: h.Version,
		OS:            runtime.GOOS,
		Arch:          HelloArch(runtime.GOARCH),
		Ring:          h.Ring,
		Providers:     infos,
		// Consent screens arrive with the setup slice; until then nothing beyond revision 0 is accepted.
		AcceptedConsentRevision: 0,
		Launch:                  launch.Capabilities(),
	}
	if h.HelloExtra != nil {
		h.HelloExtra(&hello)
	}
	return hello
}

// Idle reports whether no turn is running. The updater swaps versions only then.
func (h *Helper) Idle() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.running) == 0 && !h.held
}

// Hold stops new turns from starting (they are refused as busy) if none is running, and reports whether it did.
// Release undoes it. The updater holds the Link while it restarts into a new version.
func (h *Helper) Hold() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.running) > 0 || h.held {
		return false
	}
	h.held = true
	return true
}

// Release lets turns start again after Hold.
func (h *Helper) Release() {
	h.mu.Lock()
	h.held = false
	h.mu.Unlock()
}

// HelloArch is the protocol's name for a Go architecture: amd64 is "x64".
func HelloArch(goarch string) string {
	if goarch == "amd64" {
		return "x64"
	}
	return goarch
}

// Cancel stops a running turn.
func (h *Helper) Cancel(turnID string) {
	h.mu.Lock()
	cancel, ok := h.running[turnID]
	h.mu.Unlock()
	if ok {
		h.Log.Log(logx.TurnCancel, logx.Fields{TicketID: turnID})
		cancel()
	}
}

// checkTurn returns ("", "") and the turn's launch profile if its ticket and profile allow it to run, else an error
// class and a reason code. It doesn't touch the provider.
func (h *Helper) checkTurn(rec pairing.Record, t protocol.Turn) (ticket.Claims, launch.Profile, string, string) {
	var lp launch.Profile
	c, err := h.Verifier.Verify(t.Ticket)
	if err != nil {
		reason := "ticket_invalid"
		if r, ok := err.(ticket.Reason); ok {
			reason = string(r)
		}
		return c, lp, protocol.ClassPolicy, reason
	}
	switch {
	case c.TicketID != t.TurnID:
		return c, lp, protocol.ClassPolicy, "turn_id_mismatch"
	case c.DeviceID != rec.DeviceID:
		return c, lp, protocol.ClassPolicy, "not_this_device"
	case c.UserID != rec.UserID:
		return c, lp, protocol.ClassPolicy, "not_paired_user"
	case c.FirmID != rec.FirmID:
		return c, lp, protocol.ClassPolicy, "not_paired_firm"
	case !h.serves(c.Adapter):
		return c, lp, protocol.ClassPolicy, "adapter_mismatch"
	case h.Disabled != nil && h.Disabled(c.Adapter):
		// Paused by Firmary's signed update manifest, for a Link that was offline when the server stopped it.
		return c, lp, protocol.ClassPolicy, "adapter_disabled"
	case t.Schema != nil && *t.Schema == "":
		// An empty schema would hash like an absent one.
		return c, lp, protocol.ClassPolicy, "schema_empty"
	case t.PromptHash() != c.PromptHash:
		return c, lp, protocol.ClassPolicy, "prompt_hash_mismatch"
	case t.Schema != nil:
		return c, lp, protocol.ClassPolicy, "schema_unsupported"
	case t.Profile == "":
		// Required: the ticket's profileHash binds a profile the relay must forward.
		return c, lp, protocol.ClassPolicy, string(launch.ReasonInvalid)
	case protocol.ProfileHash(t.Profile) != c.ProfileHash:
		return c, lp, protocol.ClassPolicy, "profile_hash_mismatch"
	case c.Trigger.Kind != "interactive":
		// Personal schedules need the per-automation consent (G4), which arrives in a later slice.
		return c, lp, protocol.ClassPolicy, "trigger_unsupported"
	}
	// Only bytes the ticket signed are parsed. Anything outside the ceiling refuses the whole turn.
	if lp, err = launch.Parse(t.Profile); err != nil {
		reason := launch.ReasonInvalid
		if r, ok := err.(launch.Reason); ok {
			reason = r
		}
		return c, lp, protocol.ClassPolicy, string(reason)
	}
	return c, lp, "", ""
}

func (h *Helper) serves(adapter string) bool {
	_, ok := h.provider(adapter)
	return ok
}

// checkProvider returns ("", "") if the profile's provider login may run the ticket's turn, else an error class and
// a reason code. Call it only while holding the turn slot: Status may prepare the provider profile, which must not
// happen under a running turn.
func (h *Helper) checkProvider(ctx context.Context, rec pairing.Record, d driver.Driver, c ticket.Claims) (string, string) {
	st, err := d.Status(ctx, h.Profile(rec, d))
	switch {
	case err != nil:
		return protocol.ClassTransient, "status_failed"
	case st.Installed == "":
		return protocol.ClassFatal, "provider_not_installed"
	case st.Installed != c.Pin:
		return protocol.ClassPolicy, "pin_mismatch"
	case !st.SignedIn:
		return protocol.ClassAuth, "not_signed_in"
	case st.AccountType == "" || !slices.Contains(c.AccountTypes, st.AccountType):
		return protocol.ClassPolicy, "account_type_not_allowed"
	case c.ProviderOrgIDs != nil && (st.OrgID == "" || !slices.Contains(c.ProviderOrgIDs, st.OrgID)):
		// Present means "only these provider organisations"; an empty list allows none.
		return protocol.ClassPolicy, "provider_org_not_allowed"
	case c.ProviderOrgIDs == nil && slices.Contains(protocol.BusinessAccountTypes[d.ID()], st.AccountType):
		// D7: a business plan runs firm work only in the firm's own organisation, so it needs the claim.
		return protocol.ClassPolicy, "provider_org_not_allowed"
	}
	return "", ""
}

// HandleTurn checks and runs one turn. It always emits exactly one terminal event.
func (h *Helper) HandleTurn(ctx context.Context, rec pairing.Record, t protocol.Turn, emit func(protocol.StreamEvent)) {
	started := time.Now()
	provider := ""
	reject := func(c ticket.Claims, class, reason string) {
		h.Log.Log(logx.TurnRejected, logx.Fields{TicketID: c.TicketID, Provider: provider, ErrorClass: class, Reason: reason})
		emit(protocol.Error(class, reason))
	}
	c, lp, class, reason := h.checkTurn(rec, t)
	if reason != "" {
		reject(c, class, reason)
		return
	}
	p, _ := h.provider(c.Adapter)
	drv := p.Driver
	provider = drv.ID()

	// The slot is taken before the provider is asked anything, so a turn that will be refused as busy never touches
	// the provider profile of the turn that is running.
	h.mu.Lock()
	if h.running == nil {
		h.running = map[string]context.CancelFunc{}
	}
	if len(h.running) >= MaxConcurrentTurns || h.held {
		h.mu.Unlock()
		reject(c, protocol.ClassTransient, "busy")
		return
	}
	timeout := h.TurnTimeout
	if timeout <= 0 {
		timeout = DefaultTurnTimeout
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	h.running[c.TicketID] = cancel
	h.mu.Unlock()
	// The slot is freed before the terminal event goes out: the relay may send the next turn as soon as it sees it,
	// and that turn must not be refused as busy.
	var freeOnce sync.Once
	free := func() {
		freeOnce.Do(func() {
			h.mu.Lock()
			delete(h.running, c.TicketID)
			h.mu.Unlock()
		})
	}
	defer func() {
		free()
		cancel()
	}()
	if class, reason := h.checkProvider(rctx, rec, drv, c); reason != "" {
		free()
		reject(c, class, reason)
		return
	}

	h.Log.Log(logx.TurnStart, logx.Fields{TicketID: c.TicketID, Provider: provider, Model: c.Model, Version: c.Pin})
	var done logx.Fields
	var windows []logx.Window
	err := drv.Run(rctx, h.Profile(rec, drv), driver.Turn{
		ID: c.TicketID, Model: c.Model, Context: t.Context, Prompt: t.Prompt, MaxOutputTokens: c.MaxOutputTokens, Launch: lp,
	}, func(e protocol.StreamEvent) {
		switch e.T {
		case protocol.EvUsage:
			windows = windows[:0]
			for _, w := range e.Windows {
				windows = append(windows, logx.Window{Name: w.Window, UsedPercent: w.UsedPercent, ResetsAt: w.ResetsAt})
			}
		case protocol.EvLimit:
			h.Log.Log(logx.ProviderLimitHit, logx.Fields{TicketID: c.TicketID, Provider: provider})
		case protocol.EvDone:
			if e.Tokens != nil {
				done.InputTokens, done.OutputTokens = e.Tokens.Input, e.Tokens.Output
			}
		}
		if e.T == protocol.EvDone || e.T == protocol.EvError {
			free()
		}
		emit(e)
	})
	f := logx.Fields{TicketID: c.TicketID, Provider: provider, Model: c.Model, DurationMs: time.Since(started).Milliseconds(),
		InputTokens: done.InputTokens, OutputTokens: done.OutputTokens, Windows: windows}
	if err != nil {
		f.ErrorClass, f.Pattern = protocol.ClassFatal, "internal"
		if fail, ok := err.(*driver.Failure); ok {
			f.ErrorClass, f.Pattern = fail.Class, fail.Pattern
		}
		h.Log.Log(logx.TurnError, f)
		return
	}
	h.Log.Log(logx.TurnDone, f)
}
