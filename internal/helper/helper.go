// Package helper decides whether a turn may run, then runs it with the driver. It is the Helper's half of
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

// AdapterID is the router adapter this Helper serves with the Claude Code driver.
const AdapterID = "helper-claude-code"

// Helper handles turns for one device.
type Helper struct {
	Driver   driver.Driver
	Pin      driver.Pin
	Verifier *ticket.Verifier
	DataDir  string
	Version  string
	Ring     string
	Log      *logx.Logger
	// TurnTimeout bounds one turn; zero means DefaultTurnTimeout.
	TurnTimeout time.Duration

	mu      sync.Mutex
	running map[string]context.CancelFunc
}

// MaxConcurrentTurns is one in this slice: one provider profile, one turn at a time.
const MaxConcurrentTurns = 1

// DefaultTurnTimeout ends a turn that hangs (for example a stalled provider connection), so it can't keep the
// Helper busy indefinitely.
const DefaultTurnTimeout = 15 * time.Minute

// Profile is the paired user's Claude Code profile directory. User ids are validated by the pairing record.
func (h *Helper) Profile(rec pairing.Record) driver.Profile {
	return driver.Profile{UserID: rec.UserID, Dir: filepath.Join(h.DataDir, "profiles", rec.UserID, h.Driver.ID())}
}

// Hello reports versions and provider state.
func (h *Helper) Hello(ctx context.Context, rec pairing.Record) protocol.Hello {
	st, err := h.Driver.Status(ctx, h.Profile(rec))
	if err != nil {
		h.Log.Log(logx.ProviderStatus, logx.Fields{Provider: h.Driver.ID(), Reason: "status_failed"})
	}
	return protocol.Hello{
		Type:          protocol.TypeHello,
		HelperVersion: h.Version,
		OS:            runtime.GOOS,
		Arch:          HelloArch(runtime.GOARCH),
		Ring:          h.Ring,
		Providers: []protocol.ProviderInfo{{
			ID: h.Driver.ID(), PinnedVersion: st.Installed, SignedIn: st.SignedIn, AccountType: st.AccountType,
			OrgID: st.OrgID, Managed: st.Managed,
		}},
		// Consent screens arrive with the setup slice; until then nothing beyond revision 0 is accepted.
		AcceptedConsentRevision: 0,
		Launch:                  launch.Capabilities(),
	}
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
	case c.Adapter != AdapterID:
		return c, lp, protocol.ClassPolicy, "adapter_mismatch"
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

// checkProvider returns ("", "") if the profile's provider login may run the ticket's turn, else an error class and
// a reason code. Call it only while holding the turn slot: Status may prepare the provider profile, which must not
// happen under a running turn.
func (h *Helper) checkProvider(ctx context.Context, rec pairing.Record, c ticket.Claims) (string, string) {
	st, err := h.Driver.Status(ctx, h.Profile(rec))
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
	case c.ProviderOrgIDs == nil && slices.Contains(protocol.BusinessAccountTypes, st.AccountType):
		// D7: a business plan runs firm work only in the firm's own organisation, so it needs the claim.
		return protocol.ClassPolicy, "provider_org_not_allowed"
	}
	return "", ""
}

// HandleTurn checks and runs one turn. It always emits exactly one terminal event.
func (h *Helper) HandleTurn(ctx context.Context, rec pairing.Record, t protocol.Turn, emit func(protocol.StreamEvent)) {
	started := time.Now()
	reject := func(c ticket.Claims, class, reason string) {
		h.Log.Log(logx.TurnRejected, logx.Fields{TicketID: c.TicketID, Provider: h.Driver.ID(), ErrorClass: class, Reason: reason})
		emit(protocol.Error(class, reason))
	}
	c, lp, class, reason := h.checkTurn(rec, t)
	if reason != "" {
		reject(c, class, reason)
		return
	}

	// The slot is taken before the provider is asked anything, so a turn that will be refused as busy never touches
	// the provider profile of the turn that is running.
	h.mu.Lock()
	if h.running == nil {
		h.running = map[string]context.CancelFunc{}
	}
	if len(h.running) >= MaxConcurrentTurns {
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
	defer func() {
		h.mu.Lock()
		delete(h.running, c.TicketID)
		h.mu.Unlock()
		cancel()
	}()
	if class, reason := h.checkProvider(rctx, rec, c); reason != "" {
		reject(c, class, reason)
		return
	}

	h.Log.Log(logx.TurnStart, logx.Fields{TicketID: c.TicketID, Provider: h.Driver.ID(), Model: c.Model, Version: c.Pin})
	var done logx.Fields
	var windows []logx.Window
	err := h.Driver.Run(rctx, h.Profile(rec), driver.Turn{
		ID: c.TicketID, Model: c.Model, Context: t.Context, Prompt: t.Prompt, MaxOutputTokens: c.MaxOutputTokens, Launch: lp,
	}, func(e protocol.StreamEvent) {
		switch e.T {
		case protocol.EvUsage:
			windows = windows[:0]
			for _, w := range e.Windows {
				windows = append(windows, logx.Window{Name: w.Window, UsedPercent: w.UsedPercent, ResetsAt: w.ResetsAt})
			}
		case protocol.EvLimit:
			h.Log.Log(logx.ProviderLimitHit, logx.Fields{TicketID: c.TicketID, Provider: h.Driver.ID()})
		case protocol.EvDone:
			if e.Tokens != nil {
				done.InputTokens, done.OutputTokens = e.Tokens.Input, e.Tokens.Output
			}
		}
		emit(e)
	})
	f := logx.Fields{TicketID: c.TicketID, Provider: h.Driver.ID(), Model: c.Model, DurationMs: time.Since(started).Milliseconds(),
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
