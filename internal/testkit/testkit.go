// Package testkit has test fixtures shared by package tests: a signed fake Claude Code release server and a fake
// `claude` executable. It is imported only from _test.go files.
package testkit

import (
	"bytes"
	"crypto/sha256"
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
	EnvCanary     = "sk-ant-oat01-ENV_CANARY_e6"
	// SlowPrompt makes the fake claude sleep for a minute before replying.
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
		`","orgId":"org-1","orgName":"` + OrgCanary + `","subscriptionType":"team"}`
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
