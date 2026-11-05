//go:build !windows

package claudecode

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Firmary/bridge/internal/driver"
	"github.com/Firmary/bridge/internal/testkit"
)

// fakeLogin is Claude Code 2.1.304's `auth login --claudeai` as seen through a pipe: it prints only the link whose page
// ends on a code, then reads a pasted code. Its own browser callback never comes here.
const fakeLogin = `#!/bin/sh
if [ "$1 $2" = "auth status" ]; then
  if [ -f "$CLAUDE_CONFIG_DIR/signed-in" ]; then
    echo '{"loggedIn":true,"authMethod":"claude.ai","apiProvider":"firstParty","subscriptionType":"pro"}'
  else
    echo '{"loggedIn":false}'
  fi
  exit 0
fi
printf 'Opening browser to sign in...\nIf the browser did not open, visit: https://claude.com/cai/oauth/authorize?code=true&redirect_uri=https%%3A%%2F%%2Fplatform.claude.com%%2Foauth%%2Fcode%%2Fcallback&state=s1\nPaste code here if prompted > '
read code
[ "$code" = "goodcode1234#state5678" ] || { echo 'Login failed: Request failed with status code 400'; exit 1; }
touch "$CLAUDE_CONFIG_DIR/signed-in"
`

// TestBrowserSignInTakesPastedCode: the browser kind passes on Claude Code's link and writes a code pasted from its
// page to Claude Code, so a sign-in finished on that page completes instead of waiting out SignInTimeout. A malformed
// code is asked for again; a code Anthropic refuses fails at once with its own pattern.
func TestBrowserSignInTakesPastedCode(t *testing.T) {
	rel := testkit.NewRelease(t, "2.1.304", testPlatform, []byte(fakeLogin))
	d, err := New(t.TempDir(), WithRelease(rel.URL, rel.Keyring, nil), WithPlatform(testPlatform),
		WithEnviron(func() []string { return []string{"PATH=/usr/bin:/bin"} }))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := d.Ensure(ctx, driver.Pin{Version: "2.1.304", SHA256: map[string]string{testPlatform: rel.SHA256}}); err != nil {
		t.Fatal(err)
	}
	signIn := func(codes ...string) (driver.SignInStep, int, error) {
		var step driver.SignInStep
		asked := 0
		prof := driver.Profile{UserID: "u1", Dir: filepath.Join(t.TempDir(), "u1")}
		err := d.SignIn(ctx, prof, driver.SignInBrowser, driver.SignInUI{
			Step: func(s driver.SignInStep) { step = s },
			Code: func(context.Context) (string, error) {
				asked++
				return codes[asked-1], nil
			},
		})
		return step, asked, err
	}

	step, asked, err := signIn("not a code", " goodcode1234#state5678\n")
	if err != nil || asked != 2 {
		t.Fatalf("sign-in with a good code: %v after %d asks", err, asked)
	}
	if step.Kind != driver.SignInBrowser || !strings.HasPrefix(step.URL, "https://claude.com/cai/oauth/authorize?") {
		t.Fatalf("step %+v", step)
	}

	_, _, err = signIn("wrongcode123#state5678")
	var f *driver.Failure
	if !errors.As(err, &f) || f.Pattern != "signin_code_refused" {
		t.Fatalf("refused code: %v", err)
	}
}
