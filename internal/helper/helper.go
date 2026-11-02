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

// check returns ("", "") if the turn may run, else an error class and a reason code.
func (h *Helper) check(ctx context.Context, rec pairing.Record, t protocol.Turn) (ticket.Claims, driver.ProviderStatus, string, string) {
	var st driver.ProviderStatus
	c, err := h.Verifier.Verify(t.Ticket)
	if err != nil {
		reason := "ticket_invalid"
		if r, ok := err.(ticket.Reason); ok {
			reason = string(r)
		}
		return c, st, protocol.ClassPolicy, reason
	}
	switch {
	case c.TicketID != t.TurnID:
		return c, st, protocol.ClassPolicy, "turn_id_mismatch"
	case c.DeviceID != rec.DeviceID:
		return c, st, protocol.ClassPolicy, "not_this_device"
	case c.UserID != rec.UserID:
		return c, st, protocol.ClassPolicy, "not_paired_user"
	case c.FirmID != rec.FirmID:
		return c, st, protocol.ClassPolicy, "not_paired_firm"
	case c.Adapter != AdapterID:
		return c, st, protocol.ClassPolicy, "adapter_mismatch"
	case t.Schema != nil && *t.Schema == "":
		// An empty schema would hash like an absent one.
		return c, st, protocol.ClassPolicy, "schema_empty"
	case t.PromptHash() != c.PromptHash:
		return c, st, protocol.ClassPolicy, "prompt_hash_mismatch"
	case t.Schema != nil:
		return c, st, protocol.ClassPolicy, "schema_unsupported"
	case c.Trigger.Kind != "interactive":
		// Personal schedules need the per-automation consent (G4), which arrives in a later slice.
		return c, st, protocol.ClassPolicy, "trigger_unsupported"
	}
	st, err = h.Driver.Status(ctx, h.Profile(rec))
	switch {
	case err != nil:
		return c, st, protocol.ClassTransient, "status_failed"
	case st.Installed == "":
		return c, st, protocol.ClassFatal, "provider_not_installed"
	case st.Installed != c.Pin:
		return c, st, protocol.ClassPolicy, "pin_mismatch"
	case !st.SignedIn:
		return c, st, protocol.ClassAuth, "not_signed_in"
	case st.AccountType == "" || !slices.Contains(c.AccountTypes, st.AccountType):
		return c, st, protocol.ClassPolicy, "account_type_not_allowed"
	case c.ProviderOrgIDs != nil && (st.OrgID == "" || !slices.Contains(c.ProviderOrgIDs, st.OrgID)):
		// Present means "only these provider organisations"; an empty list allows none.
		return c, st, protocol.ClassPolicy, "provider_org_not_allowed"
	case c.ProviderOrgIDs == nil && slices.Contains(protocol.BusinessAccountTypes, st.AccountType):
		// D7: a business plan runs firm work only in the firm's own organisation, so it needs the claim.
		return c, st, protocol.ClassPolicy, "provider_org_not_allowed"
	}
	return c, st, "", ""
}

// HandleTurn checks and runs one turn. It always emits exactly one terminal event.
func (h *Helper) HandleTurn(ctx context.Context, rec pairing.Record, t protocol.Turn, emit func(protocol.StreamEvent)) {
	started := time.Now()
	c, _, class, reason := h.check(ctx, rec, t)
	if reason != "" {
		h.Log.Log(logx.TurnRejected, logx.Fields{TicketID: c.TicketID, Provider: h.Driver.ID(), ErrorClass: class, Reason: reason})
		emit(protocol.Error(class, reason))
		return
	}

	h.mu.Lock()
	if h.running == nil {
		h.running = map[string]context.CancelFunc{}
	}
	if len(h.running) >= MaxConcurrentTurns {
		h.mu.Unlock()
		h.Log.Log(logx.TurnRejected, logx.Fields{TicketID: c.TicketID, Provider: h.Driver.ID(), ErrorClass: protocol.ClassTransient, Reason: "busy"})
		emit(protocol.Error(protocol.ClassTransient, "busy"))
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

	h.Log.Log(logx.TurnStart, logx.Fields{TicketID: c.TicketID, Provider: h.Driver.ID(), Model: c.Model, Version: c.Pin})
	var done logx.Fields
	var windows []logx.Window
	err := h.Driver.Run(rctx, h.Profile(rec), driver.Turn{
		ID: c.TicketID, Model: c.Model, Context: t.Context, Prompt: t.Prompt, MaxOutputTokens: c.MaxOutputTokens,
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
