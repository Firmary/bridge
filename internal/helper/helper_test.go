//go:build !windows

package helper_test

import (
	"context"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Firmary/firmary-helper/internal/driver"
	"github.com/Firmary/firmary-helper/internal/driver/claudecode"
	"github.com/Firmary/firmary-helper/internal/fakerelay"
	"github.com/Firmary/firmary-helper/internal/helper"
	"github.com/Firmary/firmary-helper/internal/logx"
	"github.com/Firmary/firmary-helper/internal/pairing"
	"github.com/Firmary/firmary-helper/internal/protocol"
	"github.com/Firmary/firmary-helper/internal/relay"
	"github.com/Firmary/firmary-helper/internal/testkit"
	"github.com/Firmary/firmary-helper/internal/ticket"
)

func recv[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		panic("unreachable")
	}
}

// terminal collects one turn's events up to its done or error event.
func terminal(t *testing.T, r *fakerelay.Relay, turnID string) []protocol.StreamEvent {
	t.Helper()
	var evs []protocol.StreamEvent
	for {
		m := recv(t, r.Events, "event for "+turnID)
		if m.TurnID != turnID {
			t.Fatalf("event for %s while waiting for %s", m.TurnID, turnID)
		}
		evs = append(evs, m.Event)
		if m.Event.T == protocol.EvDone || m.Event.T == protocol.EvError {
			return evs
		}
	}
}

// rig is a Helper with a fake Claude Code release, connected to a fake relay and paired.
type rig struct {
	r                          *fakerelay.Relay
	h                          *helper.Helper
	log                        *logx.Logger
	data, keyPath, pairingPath string
	shown, paired              chan string
	code                       string
	hello                      protocol.Hello
	dev                        fakerelay.Device
	cancel                     context.CancelFunc
	runDone                    chan struct{}
}

const pinVersion = "2.1.304"

// startRig starts the Helper and pairs it by code match. configure, if set, adjusts the Helper before it starts.
func startRig(t *testing.T, configure func(*helper.Helper)) *rig {
	t.Helper()
	platform := "linux-x64"
	data := t.TempDir()
	dump := t.TempDir()

	rel := testkit.NewRelease(t, pinVersion, platform, testkit.FakeClaude(dump))
	environ := func() []string {
		return append(os.Environ(), "ANTHROPIC_API_KEY="+testkit.EnvCanary, "CLAUDE_CODE_OAUTH_TOKEN="+testkit.EnvCanary)
	}
	managedDir := t.TempDir()
	os.WriteFile(filepath.Join(managedDir, "managed-settings.json"), []byte("{}"), 0o600)
	drv, err := claudecode.New(filepath.Join(data, "providers", "claude-code"),
		claudecode.WithRelease(rel.URL, rel.Keyring, nil), claudecode.WithPlatform(platform), claudecode.WithEnviron(environ),
		claudecode.WithManagedDirs(managedDir))
	if err != nil {
		t.Fatal(err)
	}
	pin := driver.Pin{Version: pinVersion, SHA256: map[string]string{platform: rel.SHA256}}
	if err := drv.Ensure(context.Background(), pin); err != nil {
		t.Fatal(err)
	}

	r, err := fakerelay.Start("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)

	log, err := logx.Open(filepath.Join(data, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	h := &helper.Helper{Driver: drv, Pin: pin, Verifier: ticket.NewVerifier([]ed25519.PublicKey{r.TicketPub}, nil),
		DataDir: data, Version: "0.0.0-test", Ring: "internal", Log: log}
	if configure != nil {
		configure(h)
	}
	g := &rig{r: r, h: h, log: log, data: data, shown: make(chan string, 4), paired: make(chan string, 4), runDone: make(chan struct{}),
		pairingPath: filepath.Join(data, "pairing.json"), keyPath: filepath.Join(data, "device.key")}
	client, err := relay.New(relay.Config{URL: r.URL, Env: "local", KeyPath: g.keyPath, PairingPath: g.pairingPath,
		HelperVersion: "0.0.0-test", Log: log, Handler: h, ShowPairingCode: func(c string) { g.shown <- c },
		ShowPaired: func(name string) { g.paired <- name },
		MinBackoff: 10 * time.Millisecond, MaxBackoff: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	g.cancel = cancel
	go func() { client.Run(ctx); close(g.runDone) }()
	t.Cleanup(g.stop)

	// Pairing: the code the Helper shows is the code Firmary computes for the pending key.
	g.code = recv(t, g.shown, "pairing code")
	if relayCode := recv(t, r.Codes, "relay code"); relayCode != g.code {
		t.Fatalf("helper shows %s, relay computed %s", g.code, relayCode)
	}
	if !r.Confirm(g.code) {
		t.Fatal("confirm failed")
	}
	if name := recv(t, g.paired, "paired to"); name != fakerelay.FirmName {
		t.Fatalf("paired to %q", name)
	}
	g.hello = recv(t, r.Hellos, "hello")
	g.dev, _ = r.WaitConnected(ctx)
	return g
}

// stop stops the Helper and closes its log. It is safe to call more than once.
func (g *rig) stop() {
	g.cancel()
	<-g.runDone
	g.log.Close()
}

func TestHelloArch(t *testing.T) {
	if got := helper.HelloArch("amd64"); got != "x64" {
		t.Errorf("amd64 -> %q, want x64", got)
	}
	if got := helper.HelloArch("arm64"); got != "arm64" {
		t.Errorf("arm64 -> %q, want arm64", got)
	}
}

// TestHelperAgainstFakeRelay pairs by code match, says hello, streams a turn, refuses bad tickets, reconnects,
// handles revoke, and then checks that nothing the Helper must never log reached its data directory.
func TestHelperAgainstFakeRelay(t *testing.T) {
	const version = pinVersion
	g := startRig(t, nil)
	r, data, keyPath, pairingPath, shown, code, dev := g.r, g.data, g.keyPath, g.pairingPath, g.shown, g.code, g.dev
	hello := g.hello
	p := hello.Providers[0]
	if p.ID != "claude-code" || !p.SignedIn || p.AccountType != "team" || p.PinnedVersion != version || hello.Ring != "internal" ||
		p.OrgID != testkit.OrgID || !p.Managed {
		t.Fatalf("hello %+v", hello)
	}
	if rec, _ := pairing.Load(pairingPath); rec == nil || rec.UserID != dev.UserID {
		t.Fatalf("pairing record %+v", rec)
	}

	// A turn streams from the pinned (fake) Claude Code.
	c := r.Claims(dev, version, testkit.ContextCanary, testkit.PromptCanary)
	c.ProviderOrgIDs = []string{"11111111-2222-4333-8444-555555555555", testkit.OrgID}
	uuid := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	for _, id := range []string{dev.DeviceID, dev.UserID, dev.FirmID, c.TicketID, c.ConversationID, c.Trigger.SessionID} {
		if !uuid.MatchString(id) {
			t.Errorf("fake relay id %q is not a uuid", id)
		}
	}
	if err := r.SendTurn(c, testkit.ContextCanary, testkit.PromptCanary); err != nil {
		t.Fatal(err)
	}
	evs := terminal(t, r, c.TicketID)
	var text strings.Builder
	for _, e := range evs {
		text.WriteString(e.Text)
	}
	if evs[0].T != protocol.EvStart || evs[len(evs)-1].T != protocol.EvDone || !strings.Contains(text.String(), testkit.OutputCanary) {
		t.Fatalf("turn events %+v", evs)
	}

	// Refusals, each with its reason as the pattern.
	reject := func(name, want string, c ticket.Claims, ctxText, prompt string) {
		t.Helper()
		if err := r.SendTurn(c, ctxText, prompt); err != nil {
			t.Fatal(err)
		}
		evs := terminal(t, r, c.TicketID)
		if len(evs) != 1 || evs[0].T != protocol.EvError || evs[0].Pattern != want {
			t.Errorf("%s: got %+v, want error %s", name, evs, want)
		}
	}
	reject("replay", "ticket_replayed", c, testkit.ContextCanary, testkit.PromptCanary)
	c2 := r.Claims(dev, version, testkit.ContextCanary, testkit.PromptCanary)
	reject("prompt swapped", "prompt_hash_mismatch", c2, testkit.ContextCanary, testkit.PromptCanary+" and also something else")
	c3 := r.Claims(dev, version, "", testkit.PromptCanary)
	c3.UserID = "7e6d5c4b-3a29-4180-9f7e-6d5c4b3a2918"
	reject("another user", "not_paired_user", c3, "", testkit.PromptCanary)
	c4 := r.Claims(dev, version, "", testkit.PromptCanary)
	c4.AccountTypes = []string{"enterprise"}
	reject("account type", "account_type_not_allowed", c4, "", testkit.PromptCanary)
	c5 := r.Claims(dev, "9.9.9", "", testkit.PromptCanary)
	reject("pin", "pin_mismatch", c5, "", testkit.PromptCanary)
	c6 := r.Claims(dev, version, "", testkit.PromptCanary)
	c6.Trigger = ticket.Trigger{Kind: "personal_schedule", AutomationID: "a1"}
	reject("schedule", "trigger_unsupported", c6, "", testkit.PromptCanary)
	c7 := r.Claims(dev, version, "", testkit.PromptCanary)
	c7.ProviderOrgIDs = []string{"11111111-2222-4333-8444-555555555555"}
	reject("another provider org", "provider_org_not_allowed", c7, "", testkit.PromptCanary)
	c8 := r.Claims(dev, version, "", testkit.PromptCanary)
	c8.ProviderOrgIDs = []string{}
	reject("empty provider org list", "provider_org_not_allowed", c8, "", testkit.PromptCanary)
	c9 := r.Claims(dev, version, "", testkit.PromptCanary)
	empty := ""
	if err := r.SendRaw(protocol.Turn{Type: protocol.TypeTurn, TurnID: c9.TicketID, Ticket: ticket.Sign(r.TicketKey, c9),
		Prompt: testkit.PromptCanary, Schema: &empty}); err != nil {
		t.Fatal(err)
	}
	if evs := terminal(t, r, c9.TicketID); len(evs) != 1 || evs[0].Pattern != "schema_empty" {
		t.Errorf("empty schema: got %+v, want error schema_empty", evs)
	}

	// Reconnect after the relay drops the connection.
	r.Drop()
	recv(t, r.Hellos, "hello after reconnect")

	// Revoke wipes the pairing and the key; the Helper comes back as a new, unpaired device.
	oldKey, _ := os.ReadFile(keyPath)
	if err := r.SendRaw(protocol.Revoke{Type: protocol.TypeRevoke}); err != nil {
		t.Fatal(err)
	}
	code2 := recv(t, shown, "pairing code after revoke")
	if code2 == code {
		t.Fatal("same pairing code after revoke")
	}
	if rec, _ := pairing.Load(pairingPath); rec != nil {
		t.Fatal("pairing kept after revoke")
	}
	if newKey, _ := os.ReadFile(keyPath); string(newKey) == string(oldKey) {
		t.Fatal("device key kept after revoke")
	}

	g.stop()

	// Never-log: scan everything the Helper wrote (except the provider binary itself, which is the fake script).
	var written strings.Builder
	for _, e := range []string{"logs", "pairing.json", "profiles"} {
		written.WriteString(testkit.ReadDir(t, filepath.Join(data, e)))
	}
	all := written.String()
	for _, canary := range []string{testkit.PromptCanary, testkit.ContextCanary, testkit.OutputCanary, testkit.StderrCanary,
		testkit.EmailCanary, testkit.OrgCanary, testkit.EnvCanary, code, code2, "sk-ant"} {
		if strings.Contains(all, canary) {
			t.Errorf("never-logged value %q found in the Helper's files", canary)
		}
	}
	logs := testkit.ReadDir(t, filepath.Join(data, "logs"))
	for _, ev := range []string{"paired", "hello_sent", "turn_start", "turn_done", "turn_rejected", "revoked", "relay_disconnect"} {
		if !strings.Contains(logs, `"ev":"`+ev+`"`) {
			t.Errorf("log has no %s event", ev)
		}
	}
	if !strings.Contains(logs, `"outputTokens":3`) || !strings.Contains(logs, `"reason":"prompt_hash_mismatch"`) {
		t.Errorf("log metadata missing:\n%s", logs)
	}
}

// TestDroppedConnectionCancelsTurn: when the connection drops during a turn, the turn is cancelled and the Helper
// reconnects at once instead of waiting for the turn to finish. A turn that runs past the Helper's deadline ends
// with a turn_timeout error.
// noAccountType reports the provider signed in but with no (reportable) account type.
type noAccountType struct{ driver.Driver }

func (d noAccountType) Status(ctx context.Context, p driver.Profile) (driver.ProviderStatus, error) {
	st, err := d.Driver.Status(ctx, p)
	st.AccountType = ""
	return st, err
}

// TestEmptyAccountTypeRefused: a signed-in provider without an account type never runs a turn, even for a ticket
// that lists an empty account type.
func TestEmptyAccountTypeRefused(t *testing.T) {
	g := startRig(t, func(h *helper.Helper) { h.Driver = noAccountType{h.Driver} })
	c := g.r.Claims(g.dev, pinVersion, "", testkit.PromptCanary)
	c.AccountTypes = []string{"team", ""}
	if err := g.r.SendTurn(c, "", testkit.PromptCanary); err != nil {
		t.Fatal(err)
	}
	if evs := terminal(t, g.r, c.TicketID); len(evs) != 1 || evs[0].Pattern != "account_type_not_allowed" {
		t.Fatalf("got %+v, want error account_type_not_allowed", evs)
	}
}

func TestDroppedConnectionCancelsTurn(t *testing.T) {
	g := startRig(t, func(h *helper.Helper) { h.TurnTimeout = 2 * time.Second })
	r := g.r

	// The fake claude sleeps for a minute on this prompt; the deadline ends it after 2 s.
	c := r.Claims(g.dev, pinVersion, "", testkit.SlowPrompt)
	if err := r.SendTurn(c, "", testkit.SlowPrompt); err != nil {
		t.Fatal(err)
	}
	evs := terminal(t, r, c.TicketID)
	if last := evs[len(evs)-1]; last.T != protocol.EvError || last.Pattern != "turn_timeout" {
		t.Fatalf("slow turn ended with %+v, want turn_timeout", evs)
	}

	// Drop the connection half a second into another slow turn: hello must come again well before the turn
	// (or its deadline) would have ended.
	c2 := r.Claims(g.dev, pinVersion, "", testkit.SlowPrompt+" again")
	if err := r.SendTurn(c2, "", testkit.SlowPrompt+" again"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	dropped := time.Now()
	r.Drop()
	recv(t, r.Hellos, "hello after drop")
	if d := time.Since(dropped); d > time.Second {
		t.Fatalf("hello came %v after the drop; the running turn held the reconnect", d)
	}
}
