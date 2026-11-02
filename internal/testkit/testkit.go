// Package testkit has test fixtures shared by package tests: a signed fake Claude Code release server, a fake Codex
// GitHub release, and fake `claude` and `codex` executables. It is imported only from _test.go files.
package testkit

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

// Canaries appear in every input the Helper must never log. Tests assert none of them reaches a log file.
const (
	PromptCanary  = "PROMPT_CANARY_7f3a"
	ContextCanary = "CONTEXT_CANARY_91bd"
	OutputCanary  = "OUTPUT_CANARY_c2e8"
	StderrCanary  = "STDERR_CANARY_5d10"
	EmailCanary   = "email-canary-44@example.com"
	OrgCanary     = "OrgCanary0a"
	// OrgID is the provider organisation id the fake claude reports.
	OrgID     = "0f6c2b1e-5d4a-4c3b-9a28-7e1f0d9c8b7a"
	EnvCanary = "sk-ant-oat01-ENV_CANARY_e6"
	// CodexAccountID is the ChatGPT account (workspace) id the fake codex's login reports.
	CodexAccountID = "5b0e4c1d-2a3f-4e6b-8c7d-9f0a1b2c3d4e"
	// DeviceCode is the one-time code the fake codex prints for a device-link sign-in.
	DeviceCode = "WXYZ-12345"
	// SlowPrompt makes the fake claude or codex sleep for a minute before replying.
	SlowPrompt = "SLOW_TURN_PROMPT_3b"
)

// Release is a fake Anthropic release bucket for one version and platform.
type Release struct {
	URL     string
	Keyring openpgp.EntityList
	SHA256  string
	// Tamper switches, set before calling Ensure.
	BadSignature  bool
	ServeOtherBin []byte
}

// NewRelease serves manifest.json, manifest.json.sig (signed with a fresh test key) and the binary.
func NewRelease(t *testing.T, version, platform string, bin []byte) *Release {
	t.Helper()
	ent, err := openpgp.NewEntity("Test Release", "", "test@example.invalid", &packet.Config{Algorithm: packet.PubKeyAlgoEdDSA})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(bin)
	r := &Release{Keyring: openpgp.EntityList{ent}, SHA256: hex.EncodeToString(sum[:])}
	name := "claude"
	if strings.HasPrefix(platform, "win32-") {
		name = "claude.exe"
	}
	manifest, _ := json.Marshal(map[string]any{
		"version": version,
		"platforms": map[string]any{
			platform: map[string]any{"binary": name, "checksum": r.SHA256, "size": len(bin)},
		},
	})
	var sig bytes.Buffer
	if err := openpgp.DetachSign(&sig, ent, bytes.NewReader(manifest), nil); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/"+version+"/manifest.json", func(w http.ResponseWriter, _ *http.Request) { w.Write(manifest) })
	mux.HandleFunc("/"+version+"/manifest.json.sig", func(w http.ResponseWriter, _ *http.Request) {
		s := sig.Bytes()
		if r.BadSignature {
			s = append([]byte(nil), s...)
			s[len(s)-5] ^= 0xff
		}
		w.Write(s)
	})
	mux.HandleFunc("/"+version+"/"+platform+"/"+name, func(w http.ResponseWriter, _ *http.Request) {
		if r.ServeOtherBin != nil {
			w.Write(r.ServeOtherBin)
			return
		}
		w.Write(bin)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	r.URL = srv.URL
	return r
}

// FakeClaude returns a POSIX shell script standing in for `claude`. `auth status` prints a signed-in Team login;
// a run records its environment, arguments and stdin under dumpDir, writes a canary to stderr, and streams a reply
// that contains OutputCanary. A prompt containing SlowPrompt sleeps for a minute first.
func FakeClaude(dumpDir string) []byte {
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
	status := `{"loggedIn":true,"authMethod":"claude.ai","apiProvider":"firstParty","email":"` + EmailCanary +
		`","orgId":"` + OrgID + `","orgName":"` + OrgCanary + `","subscriptionType":"team"}`
	lines := []string{
		`{"type":"system","subtype":"init","model":"claude-sonnet-4-5","session_id":"sess-1","tools":[]}`,
		`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","rateLimitType":"five_hour","unifiedWindows":{"five_hour":{"utilization":0.25,"resetsAt":1800000000}}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello "}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"` + OutputCanary + `"}}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Hello ` + OutputCanary + `"}]}}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"Hello ` + OutputCanary + `","session_id":"sess-1","terminal_reason":"completed","usage":{"input_tokens":12,"output_tokens":3}}`,
	}
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString(`if [ "$1" = "auth" ]; then printf '%s\n' ` + q(status) + "; exit 0; fi\n")
	b.WriteString("env > " + q(filepath.Join(dumpDir, "env")) + "\n")
	b.WriteString(`for a in "$@"; do printf '[%s]\n' "$a"; done > ` + q(filepath.Join(dumpDir, "args")) + "\n")
	b.WriteString("pwd > " + q(filepath.Join(dumpDir, "cwd")) + "\n")
	b.WriteString("cat > " + q(filepath.Join(dumpDir, "stdin")) + "\n")
	b.WriteString("if grep -q " + SlowPrompt + " " + q(filepath.Join(dumpDir, "stdin")) + "; then sleep 60; fi\n")
	b.WriteString("echo " + q(StderrCanary+" raw provider error text") + " >&2\n")
	for _, l := range lines {
		b.WriteString("printf '%s\\n' " + q(l) + "\n")
	}
	return []byte(b.String())
}

// ReadDir concatenates every regular file under dir (for scanning logs).
func ReadDir(t *testing.T, dir string) string {
	t.Helper()
	var all strings.Builder
	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			b, _ := os.ReadFile(p)
			all.Write(b)
		}
		return nil
	})
	return all.String()
}

// TarEntry is one file in a test release archive.
type TarEntry struct {
	Name string
	Body []byte
	Type byte // tar.TypeReg if zero
}

// TarGz builds a gzip-compressed tar archive. A non-regular entry is written as a symlink to /etc/passwd.
func TarGz(t *testing.T, entries ...TarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e.Name, Mode: 0o755, Size: int64(len(e.Body)), Typeflag: e.Type}
		if h.Typeflag == 0 {
			h.Typeflag = tar.TypeReg
		}
		if h.Typeflag != tar.TypeReg {
			h.Size, h.Linkname = 0, "/etc/passwd"
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			tw.Write(e.Body)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	gz.Close()
	return buf.Bytes()
}

// CodexRelease is a fake GitHub release for one Codex version and target: it serves
// /rust-v<version>/codex-<target>.tar.gz holding the one executable.
type CodexRelease struct {
	URL           string
	SHA256        string // the executable
	ArchiveSHA256 string // the archive
	// ServeArchive, if set before calling Ensure, is served instead of the genuine archive.
	ServeArchive []byte
}

// NewCodexRelease serves a genuine archive for target (for example "x86_64-unknown-linux-musl") holding bin.
func NewCodexRelease(t *testing.T, version, target string, bin []byte) *CodexRelease {
	t.Helper()
	member := "codex-" + target
	archive := TarGz(t, TarEntry{Name: member, Body: bin})
	sum, asum := sha256.Sum256(bin), sha256.Sum256(archive)
	r := &CodexRelease{SHA256: hex.EncodeToString(sum[:]), ArchiveSHA256: hex.EncodeToString(asum[:])}
	mux := http.NewServeMux()
	mux.HandleFunc("/rust-v"+version+"/"+member+".tar.gz", func(w http.ResponseWriter, _ *http.Request) {
		if r.ServeArchive != nil {
			w.Write(r.ServeArchive)
			return
		}
		w.Write(archive)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	r.URL = srv.URL
	return r
}

// CodexAuthJSON is a synthetic ChatGPT auth.json, as Codex writes into CODEX_HOME after sign-in: an unsigned
// id_token with plan type planType, the CodexAccountID workspace and EmailCanary, and canary tokens.
func CodexAuthJSON(planType string) string {
	b64 := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	claims := `{"email":"` + EmailCanary + `","https://api.openai.com/auth":{"chatgpt_plan_type":"` + planType +
		`","chatgpt_account_id":"` + CodexAccountID + `","chatgpt_user_id":"user-synthetic"}}`
	idToken := b64(`{"alg":"none","typ":"JWT"}`) + "." + b64(claims) + ".c3ludGhldGlj"
	return `{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"tokens":{"id_token":"` + idToken +
		`","access_token":"` + EnvCanary + `","refresh_token":"` + EnvCanary + `","account_id":"` + CodexAccountID + `"}}`
}

// FakeCodex returns a POSIX shell script standing in for `codex`. `login` prints a sign-in link (and, with
// --device-auth, DeviceCode) to stderr the way Codex 0.166.0 does, then writes CodexAuthJSON("business") into
// CODEX_HOME; `login status` reports from that file; `logout` removes it. `exec` records its environment, arguments,
// working directory and stdin under dumpDir, writes a canary to stderr, and prints a JSONL turn that contains
// OutputCanary. A prompt containing SlowPrompt sleeps for a minute first.
func FakeCodex(dumpDir string) []byte {
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
	lines := []string{
		`{"type":"thread.started","thread_id":"thread-1"}`,
		`{"type":"item.completed","item":{"id":"item_0","type":"error","message":"a Codex warning ` + StderrCanary + `"}}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"id":"item_1","type":"reasoning","text":"thinking"}}`,
		`{"type":"item.completed","item":{"id":"item_2","type":"agent_message","text":"Hello ` + OutputCanary + `"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":21,"cached_input_tokens":4,"cache_write_input_tokens":2,"output_tokens":3,"reasoning_output_tokens":1}}`,
	}
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString(`if [ "$1" = "login" ] && [ "$2" = "status" ]; then` + "\n")
	b.WriteString(`  if [ -f "$CODEX_HOME/auth.json" ]; then echo 'Logged in using ChatGPT' >&2; exit 0; fi` + "\n")
	b.WriteString("  echo 'Not logged in' >&2; exit 1\nfi\n")
	b.WriteString(`if [ "$1" = "login" ]; then` + "\n")
	b.WriteString(`  case " $* " in *" --device-auth "*)` + "\n")
	b.WriteString(`    printf '\033[1mFollow these steps\033[0m\n   https://auth.openai.com/codex/device\n\n   Enter this one-time code\n   \033[94m%s\033[0m\n' ` + q(DeviceCode) + " >&2;;\n")
	b.WriteString(`  *) printf 'Starting local login server on http://localhost:1455.\nnavigate to this URL:\n\nhttps://auth.openai.com/oauth/authorize?response_type=code&client_id=app_X&state=abc\n\n' >&2;;` + "\n")
	b.WriteString("  esac\n  sleep 0.2\n")
	b.WriteString(`  printf '%s' ` + q(CodexAuthJSON("business")) + ` > "$CODEX_HOME/auth.json"; exit 0` + "\nfi\n")
	b.WriteString(`if [ "$1" = "logout" ]; then rm -f "$CODEX_HOME/auth.json"; exit 0; fi` + "\n")
	b.WriteString("env > " + q(filepath.Join(dumpDir, "env")) + "\n")
	b.WriteString(`for a in "$@"; do printf '[%s]\n' "$a"; done > ` + q(filepath.Join(dumpDir, "args")) + "\n")
	b.WriteString("pwd > " + q(filepath.Join(dumpDir, "cwd")) + "\n")
	b.WriteString("cat > " + q(filepath.Join(dumpDir, "stdin")) + "\n")
	b.WriteString("if grep -q " + SlowPrompt + " " + q(filepath.Join(dumpDir, "stdin")) + "; then sleep 60; fi\n")
	b.WriteString("echo " + q(StderrCanary+" raw provider error text") + " >&2\n")
	for _, l := range lines {
		b.WriteString("printf '%s\\n' " + q(l) + "\n")
	}
	return []byte(b.String())
}
