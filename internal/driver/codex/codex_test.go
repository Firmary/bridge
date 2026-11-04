//go:build !windows

package codex

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Firmary/bridge/internal/driver"
	"github.com/Firmary/bridge/internal/driver/prompt"
	"github.com/Firmary/bridge/internal/launch"
	"github.com/Firmary/bridge/internal/protocol"
	"github.com/Firmary/bridge/internal/testkit"
)

const (
	testPlatform = "linux-x64"
	testTarget   = "x86_64-unknown-linux-musl"
	testVersion  = "0.166.0"
)

func testPin(rel *testkit.CodexRelease) driver.Pin {
	return driver.Pin{Version: testVersion, SHA256: map[string]string{testPlatform: rel.SHA256},
		ArchiveSHA256: map[string]string{testPlatform: rel.ArchiveSHA256}}
}

// TestBuiltInPinCoversPlatforms: every platform the Link builds for has both checksums.
func TestBuiltInPinCoversPlatforms(t *testing.T) {
	for p := range targets {
		if !sha256Re.MatchString(BuiltInPin.SHA256[p]) || !sha256Re.MatchString(BuiltInPin.ArchiveSHA256[p]) {
			t.Errorf("%s: missing checksum", p)
		}
	}
}

func TestEnsureVerifiesArchiveAndBinary(t *testing.T) {
	bin := []byte("#!/bin/sh\necho genuine\n")
	rel := testkit.NewCodexRelease(t, testVersion, testTarget, bin)
	newDriver := func(root string) *Driver {
		d, err := New(root, WithRelease(rel.URL, nil), WithPlatform(testPlatform))
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	pin := testPin(rel)
	ctx := context.Background()
	member := "codex-" + testTarget

	// A pin that disagrees with the archive, a swapped archive, and archives with the right checksum pinned but
	// unexpected contents are each refused, and leave nothing behind.
	wrong := testPin(rel)
	wrong.ArchiveSHA256 = map[string]string{testPlatform: strings.Repeat("0", 64)}
	if err := newDriver(t.TempDir()).Ensure(ctx, wrong); err == nil {
		t.Fatal("archive pin mismatch accepted")
	}
	root := t.TempDir()
	rel.ServeArchive = testkit.TarGz(t, testkit.TarEntry{Name: member, Body: []byte("#!/bin/sh\necho evil\n")})
	if err := newDriver(root).Ensure(ctx, pin); err == nil {
		t.Fatal("swapped archive accepted")
	}
	for name, arc := range map[string][]byte{
		"evil binary":  testkit.TarGz(t, testkit.TarEntry{Name: member, Body: []byte("evil")}),
		"extra file":   testkit.TarGz(t, testkit.TarEntry{Name: member, Body: bin}, testkit.TarEntry{Name: "../../evil", Body: bin}),
		"symlink":      testkit.TarGz(t, testkit.TarEntry{Name: member, Type: tar.TypeSymlink}),
		"wrong member": testkit.TarGz(t, testkit.TarEntry{Name: "bin/codex", Body: bin}),
		"not gzip":     []byte("not an archive"),
	} {
		rel.ServeArchive = arc
		p := testPin(rel)
		// Pin the archive actually served, so only the content checks can refuse it.
		p.ArchiveSHA256 = map[string]string{testPlatform: sha256Hex(arc)}
		if err := newDriver(root).Ensure(ctx, p); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if m, _ := filepath.Glob(filepath.Join(root, "versions", testVersion, "*")); len(m) != 0 {
		t.Fatalf("rejected downloads left files: %v", m)
	}
	rel.ServeArchive = nil

	// Genuine install, then an idempotent re-check that doesn't trust the server again.
	d := newDriver(root)
	if err := d.Ensure(ctx, pin); err != nil {
		t.Fatal(err)
	}
	v, path := d.Installed()
	if b, _ := os.ReadFile(path); v != testVersion || string(b) != string(bin) || filepath.Base(path) != "codex" {
		t.Fatalf("installed %q at %s", v, path)
	}
	rel.ServeArchive = []byte("changed upstream")
	if err := newDriver(root).Ensure(ctx, pin); err != nil {
		t.Fatalf("re-ensure: %v", err)
	}
	// A local file altered after install is replaced by a verified download.
	os.WriteFile(path, []byte("tampered"), 0o700)
	rel.ServeArchive = nil
	if err := newDriver(root).Ensure(ctx, pin); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != string(bin) {
		t.Fatal("tampered local copy was kept")
	}
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func installFake(t *testing.T, inherited []string) (*Driver, string) {
	t.Helper()
	dump := t.TempDir()
	rel := testkit.NewCodexRelease(t, testVersion, testTarget, testkit.FakeCodex(dump))
	d, err := New(t.TempDir(), WithRelease(rel.URL, nil), WithPlatform(testPlatform), WithManagedPaths(),
		WithEnviron(func() []string { return inherited }))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Ensure(context.Background(), testPin(rel)); err != nil {
		t.Fatal(err)
	}
	return d, dump
}

// TestEnvironmentScrub signs in and runs a turn with a fake codex that dumps what it received. Only allowlisted
// variables may reach it, plus the profile's CODEX_HOME; the prompt goes on stdin only; the Link's settings are on
// the command line and in the profile's config.toml.
func TestEnvironmentScrub(t *testing.T) {
	inherited := []string{
		"PATH=/usr/bin:/bin", "HOME=/home/someone", "LANG=en_US.UTF-8", "LC_ALL=C", "https_proxy=http://proxy:3128",
		"OPENAI_API_KEY=" + testkit.EnvCanary, "CODEX_API_KEY=" + testkit.EnvCanary, "OPENAI_BASE_URL=https://evil.example",
		"CODEX_HOME=/home/someone/.codex", "CODEX_SQLITE_HOME=/home/someone/evil", "RUST_LOG=trace",
		"ANTHROPIC_API_KEY=" + testkit.EnvCanary, "AWS_SECRET_ACCESS_KEY=" + testkit.EnvCanary, "GITHUB_TOKEN=" + testkit.EnvCanary,
		"AZURE_OPENAI_API_KEY=" + testkit.EnvCanary, "LD_PRELOAD=/tmp/evil.so", "DYLD_INSERT_LIBRARIES=/tmp/evil.dylib",
		"Path=/x", "path=/x",
	}
	d, dump := installFake(t, inherited)
	ctx := context.Background()
	prof := driver.Profile{UserID: "u1", Dir: filepath.Join(t.TempDir(), "u1")}

	if st, err := d.Status(ctx, prof); err != nil || st.SignedIn || st.Installed != testVersion {
		t.Fatalf("status before sign-in %+v %v", st, err)
	}
	var steps []driver.SignInStep
	if err := d.SignIn(ctx, prof, driver.SignInDeviceLink, driver.SignInUI{Step: func(s driver.SignInStep) { steps = append(steps, s) }}); err != nil {
		t.Fatalf("sign-in: %v", err)
	}
	if len(steps) != 1 || steps[0] != (driver.SignInStep{Kind: driver.SignInDeviceLink, URL: "https://auth.openai.com/codex/device",
		UserCode: testkit.DeviceCode}) {
		t.Fatalf("steps %+v", steps)
	}
	st, err := d.Status(ctx, prof)
	if err != nil || !st.SignedIn || st.AccountType != "business" || st.OrgID != testkit.CodexAccountID {
		t.Fatalf("status %+v %v", st, err)
	}

	var events []protocol.StreamEvent
	err = d.Run(ctx, prof, driver.Turn{ID: "t1", Model: "gpt-5.5", Context: testkit.ContextCanary,
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
		t.Fatalf("a scrubbed value reached codex:\n%s", envb)
	}
	// PWD, SHLVL, OLDPWD and _ are set by the shell running the fake itself.
	allowedSeen := []string{"PATH", "HOME", "LANG", "LC_ALL", "https_proxy", "CODEX_HOME", "PWD", "SHLVL", "OLDPWD", "_"}
	for k := range got {
		if !slices.Contains(allowedSeen, k) {
			t.Errorf("unexpected variable reached codex: %s", k)
		}
	}
	if got["CODEX_HOME"] != filepath.Join(prof.Dir, "home") || got["PATH"] != "/usr/bin:/bin" || got["HOME"] != "/home/someone" {
		t.Errorf("helper settings wrong: %v", got)
	}

	args, _ := os.ReadFile(filepath.Join(dump, "args"))
	if strings.Contains(string(args), testkit.PromptCanary) || strings.Contains(string(args), testkit.ContextCanary) {
		t.Error("prompt or context passed on the command line")
	}
	if !strings.HasPrefix(string(args), "[exec]\n[--json]\n") || !strings.HasSuffix(string(args), "\n[-]\n") {
		t.Errorf("not `exec --json ... -`:\n%s", args)
	}
	for _, want := range []string{"[--ephemeral]", "[--ignore-rules]", "[--strict-config]", "[--sandbox]\n[read-only]\n",
		"[check_for_update_on_startup=false]", "[features.shell_tool=false]", "[project_doc_max_bytes=0]",
		`[forced_login_method="chatgpt"]`, `[history.persistence="none"]`, "[skills.include_instructions=false]",
		"[features.apps=false]", `[approval_policy="never"]`} {
		if !strings.Contains(string(args), want) {
			t.Errorf("missing %q in args:\n%s", want, args)
		}
	}
	// Firmary's system prompt replaces Codex's built-in instructions: the profile's copy of it, named on the command
	// line and in config.toml.
	instr := filepath.Join(prof.Dir, "home", "firmary-system-prompt.md")
	if b, _ := os.ReadFile(instr); string(b) != prompt.Default {
		t.Errorf("instructions file %q", b)
	}
	if !strings.Contains(string(args), `[model_instructions_file="`+instr+`"]`) {
		t.Errorf("model_instructions_file missing in args:\n%s", args)
	}
	cfg, _ := os.ReadFile(filepath.Join(prof.Dir, "home", "config.toml"))
	if !strings.Contains(string(cfg), `model_instructions_file = "`+instr+`"`+"\n") {
		t.Errorf("model_instructions_file missing in config.toml:\n%s", cfg)
	}
	if !strings.Contains(string(cfg), "check_for_update_on_startup = false\n") || !strings.Contains(string(cfg), `cli_auth_credentials_store = "file"`) {
		t.Errorf("config.toml:\n%s", cfg)
	}
	stdin, _ := os.ReadFile(filepath.Join(dump, "stdin"))
	if !strings.HasPrefix(string(stdin), "<firmary_context>\n") || !strings.Contains(string(stdin), testkit.PromptCanary) ||
		!strings.Contains(string(stdin), testkit.ContextCanary) {
		t.Errorf("stdin missing prompt or context: %q", stdin)
	}
	cwd, _ := os.ReadFile(filepath.Join(dump, "cwd"))
	if strings.TrimSpace(string(cwd)) != filepath.Join(prof.Dir, "work") {
		t.Errorf("cwd %q", cwd)
	}

	var kinds []string
	for _, e := range events {
		kinds = append(kinds, e.T)
		if strings.Contains(e.Text, testkit.StderrCanary) {
			t.Error("a Codex warning was sent as output")
		}
	}
	if want := []string{"start", "text_delta", "done"}; !slices.Equal(kinds, want) {
		t.Fatalf("events %v, want %v", kinds, want)
	}
	last := events[len(events)-1]
	if events[1].Text != "Hello "+testkit.OutputCanary || last.ProviderSessionID != "thread-1" || last.Tokens == nil ||
		*last.Tokens != (protocol.TokenCounts{Input: 21, Output: 3, CacheRead: 4, CacheWrite: 2}) {
		t.Fatalf("events %+v", events)
	}

	if err := d.SignOut(ctx, prof); err != nil {
		t.Fatal(err)
	}
	if st, _ := d.Status(ctx, prof); st.SignedIn {
		t.Fatal("still signed in after sign-out")
	}
}

// TestBrowserSignIn: the browser flow passes on only OpenAI's authorize link.
func TestBrowserSignIn(t *testing.T) {
	d, _ := installFake(t, []string{"PATH=/usr/bin:/bin"})
	var steps []driver.SignInStep
	prof := driver.Profile{UserID: "u1", Dir: t.TempDir()}
	if err := d.SignIn(context.Background(), prof, driver.SignInBrowser, driver.SignInUI{Step: func(s driver.SignInStep) { steps = append(steps, s) }}); err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 || steps[0].Kind != driver.SignInBrowser || steps[0].UserCode != "" ||
		steps[0].URL != "https://auth.openai.com/oauth/authorize?response_type=code&client_id=app_X&state=abc" {
		t.Fatalf("steps %+v", steps)
	}
	if err := d.SignIn(context.Background(), prof, "paste_key", driver.SignInUI{}); err == nil {
		t.Fatal("unknown sign-in kind accepted")
	}
}

// TestSignInScannerIgnoresOtherLinks: a link on any other host, or a code before the device link, is never passed on.
func TestSignInScannerIgnoresOtherLinks(t *testing.T) {
	var steps []driver.SignInStep
	s := &signInScanner{kind: driver.SignInDeviceLink, step: func(st driver.SignInStep) { steps = append(steps, st) }}
	s.Write([]byte("AAAA-BBBBB https://evil.example/codex/device https://auth.openai.com.evil.example/codex/device\n"))
	if len(steps) != 0 {
		t.Fatalf("steps %+v", steps)
	}
	b := &signInScanner{kind: driver.SignInBrowser, step: func(st driver.SignInStep) { steps = append(steps, st) }}
	b.Write([]byte("http://auth.openai.com/oauth/authorize?x=1 https://evil.example/oauth/authorize?x=1\n"))
	if len(steps) != 0 {
		t.Fatalf("steps %+v", steps)
	}
}

// TestStatusNeedsChatGPTLogin: an API-key login, or a ChatGPT login whose claims aren't machine names or a UUID,
// reports signed out or reports nothing beyond signed in.
func TestStatusNeedsChatGPTLogin(t *testing.T) {
	home := t.TempDir()
	write := func(s string) { os.WriteFile(filepath.Join(home, "auth.json"), []byte(s), 0o600) }
	write(`{"auth_mode":"apikey","OPENAI_API_KEY":"` + testkit.EnvCanary + `"}`)
	if _, _, _, ok := readAccount(home); ok {
		t.Error("API key login accepted")
	}
	write(strings.Replace(testkit.CodexAuthJSON("business"), `"OPENAI_API_KEY":null`, `"OPENAI_API_KEY":"sk-x"`, 1))
	if _, _, _, ok := readAccount(home); ok {
		t.Error("ChatGPT login with an API key accepted")
	}
	write(testkit.CodexAuthJSON("Business Plan"))
	if plan, acct, _, ok := readAccount(home); !ok || plan != "" || acct != testkit.CodexAccountID {
		t.Errorf("got %q %q %v", plan, acct, ok)
	}
}

// TestPrepareRemovesGlobalInstructions: AGENTS.md in CODEX_HOME would be sent with every turn, and a changed copy of
// Firmary's system prompt is put back.
func TestPrepareRemovesGlobalInstructions(t *testing.T) {
	d, _ := New(t.TempDir())
	prof := driver.Profile{UserID: "u1", Dir: t.TempDir()}
	os.MkdirAll(codexHome(prof), 0o700)
	for _, n := range []string{"AGENTS.md", "AGENTS.override.md"} {
		os.WriteFile(filepath.Join(codexHome(prof), n), []byte("x"), 0o600)
	}
	os.WriteFile(filepath.Join(codexHome(prof), "config.toml"), []byte("[mcp_servers.x]\ncommand = \"/bin/sh\"\n"), 0o600)
	os.WriteFile(instructionsFile(prof), []byte("changed"), 0o600)
	if err := d.prepare(prof, launch.Profile{}); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"AGENTS.md", "AGENTS.override.md"} {
		if _, err := os.Stat(filepath.Join(codexHome(prof), n)); err == nil {
			t.Errorf("%s kept", n)
		}
	}
	if cfg, _ := os.ReadFile(filepath.Join(codexHome(prof), "config.toml")); strings.Contains(string(cfg), "mcp_servers") {
		t.Error("config.toml not replaced")
	}
	if b, _ := os.ReadFile(instructionsFile(prof)); string(b) != prompt.Default {
		t.Error("system prompt file not replaced")
	}
}

// TestStreamFailures: a usage limit becomes a limit event and a rate_limit error; a tool item ends the turn.
func TestStreamFailures(t *testing.T) {
	var s streamState
	s.line([]byte(`{"type":"turn.started"}`))
	s.line([]byte(`{"type":"error","message":"You’ve hit your usage limit. Try again at 7:24 AM."}`))
	s.line([]byte(`{"type":"turn.failed","error":{"message":"You’ve hit your usage limit. Try again at 7:24 AM."}}`))
	if c, p := Classify(s.errorText.String()); !s.failed || c != protocol.ClassRateLimit || p != "usage_limit" {
		t.Fatalf("got %s %s", c, p)
	}
	if c, p := Classify("Reconnecting... 1/5 (unexpected status 401 Unauthorized: x, url: http://127.0.0.1/v1/responses)"); c != protocol.ClassAuth || p != "unauthorized" {
		t.Fatalf("401: %s %s", c, p)
	}
	var u streamState
	u.line([]byte(`{"type":"item.started","item":{"id":"i","type":"command_execution","command":"ls"}}`))
	if !u.toolUsed {
		t.Fatal("tool item not flagged")
	}
}

// TestManagedConfigReported: an empty or missing managed directory is not managed; a file, or any entry in a
// directory, is.
func TestManagedConfigReported(t *testing.T) {
	empty, used := t.TempDir(), t.TempDir()
	missing := filepath.Join(t.TempDir(), "missing")
	plist := filepath.Join(t.TempDir(), "com.openai.codex.plist")
	status := func(paths ...string) bool {
		d, _ := New(t.TempDir(), WithManagedPaths(paths...))
		st, _ := d.Status(context.Background(), driver.Profile{UserID: "u1", Dir: t.TempDir()})
		return st.Managed
	}
	if status(empty, missing, plist) {
		t.Error("empty or missing managed paths reported as managed")
	}
	os.WriteFile(filepath.Join(used, "requirements.toml"), []byte("x"), 0o600)
	os.WriteFile(plist, []byte("x"), 0o600)
	if !status(empty, used) || !status(plist) {
		t.Error("managed configuration not reported")
	}
}
