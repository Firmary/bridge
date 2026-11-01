//go:build !windows

package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Firmary/firmary-helper/internal/driver"
	"github.com/Firmary/firmary-helper/internal/protocol"
	"github.com/Firmary/firmary-helper/internal/testkit"
)

const testPlatform = "linux-x64"

func TestEmbeddedAnthropicKey(t *testing.T) {
	if _, err := AnthropicKeyring(); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureVerifiesSignatureAndPin(t *testing.T) {
	bin := []byte("#!/bin/sh\necho genuine\n")
	rel := testkit.NewRelease(t, "2.1.304", testPlatform, bin)
	newDriver := func(root string) *Driver {
		d, err := New(root, WithRelease(rel.URL, rel.Keyring, nil), WithPlatform(testPlatform))
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	pin := driver.Pin{Version: "2.1.304", SHA256: map[string]string{testPlatform: rel.SHA256}}
	ctx := context.Background()

	// Bad signature, a pin that disagrees with the manifest, and a swapped binary are each refused.
	rel.BadSignature = true
	if err := newDriver(t.TempDir()).Ensure(ctx, pin); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("bad signature: %v", err)
	}
	rel.BadSignature = false
	wrongPin := driver.Pin{Version: "2.1.304", SHA256: map[string]string{testPlatform: strings.Repeat("0", 64)}}
	if err := newDriver(t.TempDir()).Ensure(ctx, wrongPin); err == nil {
		t.Fatal("pin mismatch accepted")
	}
	rel.ServeOtherBin = []byte("#!/bin/sh\necho evil\n")
	root := t.TempDir()
	if err := newDriver(root).Ensure(ctx, pin); err == nil {
		t.Fatal("swapped binary accepted")
	}
	if m, _ := filepath.Glob(filepath.Join(root, "versions", "2.1.304", "*")); len(m) != 0 {
		t.Fatalf("rejected download left files: %v", m)
	}
	rel.ServeOtherBin = nil

	// Genuine install, then an idempotent re-check that doesn't trust the server again.
	d := newDriver(root)
	if err := d.Ensure(ctx, pin); err != nil {
		t.Fatal(err)
	}
	v, path := d.Installed()
	if b, _ := os.ReadFile(path); v != "2.1.304" || string(b) != string(bin) {
		t.Fatalf("installed %q at %s", v, path)
	}
	rel.ServeOtherBin = []byte("changed upstream")
	if err := newDriver(root).Ensure(ctx, pin); err != nil {
		t.Fatalf("re-ensure: %v", err)
	}
	// A local file altered after install is replaced by a verified download.
	os.WriteFile(path, []byte("tampered"), 0o700)
	rel.ServeOtherBin = nil
	if err := newDriver(root).Ensure(ctx, pin); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != string(bin) {
		t.Fatal("tampered local copy was kept")
	}
}

// TestEnvironmentScrub runs a turn with a fake claude that dumps what it received. Only allowlisted variables
// may reach it, plus the Helper's own profile and updater settings.
func TestEnvironmentScrub(t *testing.T) {
	dump := t.TempDir()
	rel := testkit.NewRelease(t, "2.1.304", testPlatform, testkit.FakeClaude(dump))
	inherited := []string{
		"PATH=/usr/bin:/bin", "HOME=/home/someone", "LANG=en_US.UTF-8", "LC_ALL=C", "https_proxy=http://proxy:3128",
		"ANTHROPIC_API_KEY=" + testkit.EnvCanary, "ANTHROPIC_AUTH_TOKEN=" + testkit.EnvCanary,
		"CLAUDE_CODE_OAUTH_TOKEN=" + testkit.EnvCanary, "ANTHROPIC_BASE_URL=https://evil.example",
		"CLAUDE_CODE_USE_BEDROCK=1", "CLAUDE_CODE_USE_VERTEX=1", "ANTHROPIC_MODEL=other", "OPENAI_API_KEY=" + testkit.EnvCanary,
		"AWS_SECRET_ACCESS_KEY=" + testkit.EnvCanary, "GITHUB_TOKEN=" + testkit.EnvCanary, "CLAUDE_CONFIG_DIR=/home/someone/.claude",
		"CODEX_HOME=/home/someone/.codex", "NODE_OPTIONS=--require /tmp/evil.js", "LD_PRELOAD=/tmp/evil.so",
		"DYLD_INSERT_LIBRARIES=/tmp/evil.dylib", "Path=/x", "path=/x",
	}
	d, err := New(t.TempDir(), WithRelease(rel.URL, rel.Keyring, nil), WithPlatform(testPlatform),
		WithEnviron(func() []string { return inherited }))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := d.Ensure(ctx, driver.Pin{Version: "2.1.304", SHA256: map[string]string{testPlatform: rel.SHA256}}); err != nil {
		t.Fatal(err)
	}
	prof := driver.Profile{UserID: "u1", Dir: filepath.Join(t.TempDir(), "u1")}

	st, err := d.Status(ctx, prof)
	if err != nil || !st.SignedIn || st.AccountType != "team" || st.Org != testkit.OrgCanary {
		t.Fatalf("status %+v %v", st, err)
	}

	var events []protocol.StreamEvent
	err = d.Run(ctx, prof, driver.Turn{ID: "t1", Model: "claude-sonnet-4-5", Context: testkit.ContextCanary,
		Prompt: testkit.PromptCanary, MaxOutputTokens: 1000}, func(e protocol.StreamEvent) { events = append(events, e) })
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	envb, _ := os.ReadFile(filepath.Join(dump, "env"))
	got := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(string(envb)), "\n") {
		k, v, _ := strings.Cut(l, "=")
		got[k] = v
	}
	if strings.Contains(string(envb), testkit.EnvCanary) || strings.Contains(string(envb), "evil") {
		t.Fatalf("a scrubbed value reached claude:\n%s", envb)
	}
	// PWD and SHLVL are set by the shell running the fake itself.
	allowedSeen := []string{"PATH", "HOME", "LANG", "LC_ALL", "https_proxy", "CLAUDE_CONFIG_DIR", "DISABLE_UPDATES",
		"DISABLE_AUTOUPDATER", "CLAUDE_CODE_MAX_OUTPUT_TOKENS", "PWD", "SHLVL", "OLDPWD", "_"}
	for k := range got {
		if !slices.Contains(allowedSeen, k) {
			t.Errorf("unexpected variable reached claude: %s", k)
		}
	}
	if got["CLAUDE_CONFIG_DIR"] != filepath.Join(prof.Dir, "config") || got["DISABLE_UPDATES"] != "1" ||
		got["CLAUDE_CODE_MAX_OUTPUT_TOKENS"] != "1000" || got["PATH"] != "/usr/bin:/bin" {
		t.Errorf("helper settings wrong: %v", got)
	}

	args, _ := os.ReadFile(filepath.Join(dump, "args"))
	if strings.Contains(string(args), testkit.PromptCanary) || strings.Contains(string(args), testkit.ContextCanary) {
		t.Error("prompt or context passed on the command line")
	}
	for _, want := range []string{"[--tools]\n[]\n", "[--strict-mcp-config]", "[--setting-sources]\n[]\n", "[--disable-slash-commands]", "[--no-session-persistence]"} {
		if !strings.Contains(string(args), want) {
			t.Errorf("missing %q in args:\n%s", want, args)
		}
	}
	stdin, _ := os.ReadFile(filepath.Join(dump, "stdin"))
	if !strings.Contains(string(stdin), testkit.PromptCanary) || !strings.Contains(string(stdin), testkit.ContextCanary) {
		t.Errorf("stdin missing prompt or context: %q", stdin)
	}
	cwd, _ := os.ReadFile(filepath.Join(dump, "cwd"))
	if strings.TrimSpace(string(cwd)) != filepath.Join(prof.Dir, "work") {
		t.Errorf("cwd %q", cwd)
	}

	var kinds []string
	for _, e := range events {
		kinds = append(kinds, e.T)
	}
	want := []string{"start", "usage", "text_delta", "text_delta", "done"}
	if !slices.Equal(kinds, want) {
		t.Fatalf("events %v, want %v", kinds, want)
	}
	if last := events[len(events)-1]; last.Tokens == nil || last.Tokens.Output != 3 || last.ProviderSessionID != "sess-1" {
		t.Fatalf("done %+v", last)
	}
}
