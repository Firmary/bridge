package claudecode

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Firmary/bridge/internal/driver"
	"github.com/Firmary/bridge/internal/proc"
	"github.com/Firmary/bridge/internal/protocol"
)

// SignInTimeout bounds one sign-in.
const SignInTimeout = 15 * time.Minute

var (
	ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)
	// Only a link to Anthropic's own sign-in page is ever passed on, so nothing else Claude Code prints can reach the
	// person through the Link. Claude Code 2.1.304 prints https://claude.com/cai/oauth/authorize?code=true&...
	signInURLRe = regexp.MustCompile(`https://(?:claude\.com|claude\.ai|platform\.claude\.com|console\.anthropic\.com)/[A-Za-z0-9/_.-]*oauth/authorize\?[A-Za-z0-9._~%&=+:/-]+`)
	// pastedCodeRe is the form of the code Anthropic's page shows after Authorize: "<code>#<state>".
	pastedCodeRe = regexp.MustCompile(`^[A-Za-z0-9._~-]{8,512}#[A-Za-z0-9._~-]{8,512}$`)
)

// Kinds: Claude Code's own browser sign-in, or, with no browser on this computer, its link plus a code the person
// pastes back from Anthropic's page.
func (d *Driver) Kinds() []string { return []string{driver.SignInBrowser, driver.SignInPasteCode} }

// signInScanner finds Anthropic's sign-in link in Claude Code's login output. Nothing else of that output is kept.
type signInScanner struct {
	mu    sync.Mutex
	buf   strings.Builder
	url   string
	found chan string
}

func (s *signInScanner) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.url != "" || s.buf.Len() > 64<<10 {
		return len(p), nil
	}
	s.buf.Write(p)
	if u := signInURLRe.FindString(ansiRe.ReplaceAllString(s.buf.String(), "")); u != "" {
		s.url = u
		s.found <- u
	}
	return len(p), nil
}

// SignIn runs `claude auth login --claudeai` (a Claude subscription, never an API key) in the profile.
//
// Claude Code does both at once: it opens the person's browser on Anthropic's page, taking the result on a short-lived
// localhost callback of its own, and prints a link to open on any browser, whose page ends on a code to paste back
// (the only link it prints). ui.Step gets that link. ui.Code, if set, asks for the code, which is written to Claude
// Code; for the browser kind it is optional (Claude Code may finish through its own callback first), for paste code
// it is required. Without ui.Code the browser kind can only finish in the browser Claude Code opened.
// SignIn returns nil only if the profile then reports signed in. The sign-in is kept in the profile's own
// configuration directory (on macOS, Claude Code keeps it in the login Keychain under that directory's own item), so
// the person's own Claude Code is never touched.
func (d *Driver) SignIn(ctx context.Context, p driver.Profile, kind string, ui driver.SignInUI) error {
	_, bin := d.Installed()
	if bin == "" {
		return &driver.Failure{Class: protocol.ClassFatal, Pattern: "provider_not_installed"}
	}
	switch kind {
	case driver.SignInBrowser:
	case driver.SignInPasteCode:
		if ui.Code == nil {
			return &driver.Failure{Class: protocol.ClassPolicy, Pattern: "signin_kind_invalid"}
		}
	default:
		return &driver.Failure{Class: protocol.ClassPolicy, Pattern: "signin_kind_invalid"}
	}
	if err := prepare(p); err != nil {
		return &driver.Failure{Class: protocol.ClassFatal, Pattern: "profile_unavailable"}
	}
	ctx, cancel := context.WithTimeout(ctx, SignInTimeout)
	defer cancel()
	cmd := proc.CommandContext(ctx, bin, "auth", "login", "--claudeai")
	cmd.Dir = workDir(p)
	cmd.Env = Env(d.environ(), configDir(p), nil)
	if kind == driver.SignInBrowser {
		cmd.Env = driver.WithDisplayEnv(cmd.Env, d.environ())
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return &driver.Failure{Class: protocol.ClassFatal, Pattern: "internal"}
	}
	sc := &signInScanner{found: make(chan string, 1)}
	cmd.Stdout, cmd.Stderr = sc, sc
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return &driver.Failure{Class: protocol.ClassFatal, Pattern: "signin_start_failed"}
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	var runErr error
	codeGiven := false
	select {
	case u := <-sc.found:
		if ui.Step != nil {
			ui.Step(driver.SignInStep{Kind: kind, URL: u})
		}
		if ui.Code != nil {
			code, waitErr, gone, cerr := askCode(ctx, ui.Code, exited)
			switch {
			case gone:
				// Claude Code ended before the person gave a code: through its own browser callback, or failing. The
				// status check below decides.
				runErr = waitErr
			case cerr != nil:
				cancel()
				<-exited
				return cerr
			default:
				_, _ = io.WriteString(stdin, code+"\n")
				codeGiven = true
				runErr = <-exited
			}
		} else {
			runErr = <-exited
		}
	case runErr = <-exited:
		if sc.url == "" {
			stdin.Close()
			if ctx.Err() == nil {
				return &driver.Failure{Class: protocol.ClassFatal, Pattern: "signin_link_missing"}
			}
		}
	}
	stdin.Close()
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return &driver.Failure{Class: protocol.ClassTransient, Pattern: "signin_timeout"}
	case ctx.Err() != nil:
		return &driver.Failure{Class: protocol.ClassFatal, Pattern: "cancelled"}
	case runErr != nil && codeGiven:
		// Anthropic refused the code (Claude Code 2.1.304 exits at once: "Login failed: ... 400").
		return &driver.Failure{Class: protocol.ClassAuth, Pattern: "signin_code_refused"}
	case runErr != nil:
		return &driver.Failure{Class: protocol.ClassAuth, Pattern: "signin_failed"}
	}
	st, err := d.Status(context.WithoutCancel(ctx), p)
	if err != nil || !st.SignedIn {
		return &driver.Failure{Class: protocol.ClassAuth, Pattern: "not_signed_in"}
	}
	keepPrivate(configDir(p))
	return nil
}

// askCode asks the person for the code until they give a well-formed one. gone is true, with Claude Code's exit
// error, if Claude Code exits first.
func askCode(ctx context.Context, ask func(context.Context) (string, error), exited <-chan error) (code string, waitErr error, gone bool, err error) {
	type answer struct {
		code string
		err  error
	}
	actx, cancel := context.WithCancel(ctx)
	defer cancel()
	for {
		got := make(chan answer, 1)
		go func() {
			c, err := ask(actx)
			got <- answer{strings.TrimSpace(c), err}
		}()
		select {
		case a := <-got:
			if a.err != nil {
				return "", nil, false, &driver.Failure{Class: protocol.ClassFatal, Pattern: "cancelled"}
			}
			if pastedCodeRe.MatchString(a.code) {
				return a.code, nil, false, nil
			}
			// Not a code from Anthropic's page; ask again.
		case werr := <-exited:
			return "", werr, true, nil
		case <-ctx.Done():
			return "", nil, false, &driver.Failure{Class: protocol.ClassFatal, Pattern: "cancelled"}
		}
	}
}

// keepPrivate makes the profile's credential file readable by this account only, whatever Claude Code wrote.
func keepPrivate(dir string) {
	_ = os.Chmod(filepath.Join(dir, ".credentials.json"), 0o600)
}

// SignOut runs `claude auth logout` in the profile.
func (d *Driver) SignOut(ctx context.Context, p driver.Profile) error {
	_, bin := d.Installed()
	if bin == "" {
		return errors.New("provider not installed")
	}
	if err := prepare(p); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := proc.CommandContext(ctx, bin, "auth", "logout")
	cmd.Dir = workDir(p)
	cmd.Env = Env(d.environ(), configDir(p), nil)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	setProcessGroup(cmd)
	if err := cmd.Run(); err != nil {
		return errors.New("logout failed")
	}
	return nil
}
