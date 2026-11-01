//go:build !windows

package helper_test

import (
	"context"
	"crypto/ed25519"
	"os"
	"path/filepath"
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

// TestHelperAgainstFakeRelay pairs by code match, says hello, streams a turn, refuses bad tickets, reconnects,
// handles revoke, and then checks that nothing the Helper must never log reached its data directory.
func TestHelperAgainstFakeRelay(t *testing.T) {
	const version = "2.1.304"
	platform := "linux-x64"
	data := t.TempDir()
	dump := t.TempDir()

	rel := testkit.NewRelease(t, version, platform, testkit.FakeClaude(dump))
	environ := func() []string {
		return append(os.Environ(), "ANTHROPIC_API_KEY="+testkit.EnvCanary, "CLAUDE_CODE_OAUTH_TOKEN="+testkit.EnvCanary)
	}
	drv, err := claudecode.New(filepath.Join(data, "providers", "claude-code"),
		claudecode.WithRelease(rel.URL, rel.Keyring, nil), claudecode.WithPlatform(platform), claudecode.WithEnviron(environ))
	if err != nil {
		t.Fatal(err)
	}
	pin := driver.Pin{Version: version, SHA256: map[string]string{platform: rel.SHA256}}
	if err := drv.Ensure(context.Background(), pin); err != nil {
		t.Fatal(err)
	}

	r, err := fakerelay.Start("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	log, err := logx.Open(filepath.Join(data, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	h := &helper.Helper{Driver: drv, Pin: pin, Verifier: ticket.NewVerifier([]ed25519.PublicKey{r.TicketPub}, nil),
		DataDir: data, Version: "0.0.0-test", Ring: "internal", Log: log}
	shown := make(chan string, 4)
	pairingPath := filepath.Join(data, "pairing.json")
	keyPath := filepath.Join(data, "device.key")
	client, err := relay.New(relay.Config{URL: r.URL, Env: "local", KeyPath: keyPath, PairingPath: pairingPath,
		HelperVersion: "0.0.0-test", Log: log, Handler: h, ShowPairingCode: func(c string) { shown <- c },
		MinBackoff: 10 * time.Millisecond, MaxBackoff: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() { client.Run(ctx); close(runDone) }()

	// Pairing: the code the Helper shows is the code Firmary computes for the pending key.
	code := recv(t, shown, "pairing code")
	if relayCode := recv(t, r.Codes, "relay code"); relayCode != code {
		t.Fatalf("helper shows %s, relay computed %s", code, relayCode)
	}
	if !r.Confirm(code) {
		t.Fatal("confirm failed")
	}
	hello := recv(t, r.Hellos, "hello")
	p := hello.Providers[0]
	if p.ID != "claude-code" || !p.SignedIn || p.AccountType != "team" || p.PinnedVersion != version || hello.Ring != "internal" {
		t.Fatalf("hello %+v", hello)
	}
	dev, _ := r.WaitConnected(ctx)
	if rec, _ := pairing.Load(pairingPath); rec == nil || rec.UserID != dev.UserID {
		t.Fatalf("pairing record %+v", rec)
	}

	// A turn streams from the pinned (fake) Claude Code.
	c := r.Claims(dev, version, testkit.ContextCanary, testkit.PromptCanary)
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
	c3.UserID = "usr_someoneelse"
	reject("another user", "not_paired_user", c3, "", testkit.PromptCanary)
	c4 := r.Claims(dev, version, "", testkit.PromptCanary)
	c4.AccountTypes = []string{"enterprise"}
	reject("account type", "account_type_not_allowed", c4, "", testkit.PromptCanary)
	c5 := r.Claims(dev, "9.9.9", "", testkit.PromptCanary)
	reject("pin", "pin_mismatch", c5, "", testkit.PromptCanary)
	c6 := r.Claims(dev, version, "", testkit.PromptCanary)
	c6.Trigger = ticket.Trigger{Kind: "personal_schedule", AutomationID: "a1"}
	reject("schedule", "trigger_unsupported", c6, "", testkit.PromptCanary)

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

	cancel()
	<-runDone
	log.Close()

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
