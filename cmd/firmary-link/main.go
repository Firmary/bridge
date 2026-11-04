// Command firmary-link is the Link (see brand.Name): it keeps one outgoing connection to Firmary's relay and runs
// the person's own provider app when they ask for something in Firmary. With a display it is a menu-bar or tray app;
// on a server it runs in a terminal or as a service.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"

	"github.com/Firmary/bridge/internal/agent"
	"github.com/Firmary/bridge/internal/brand"
	"github.com/Firmary/bridge/internal/console"
	"github.com/Firmary/bridge/internal/datadir"
	"github.com/Firmary/bridge/internal/driver"
	"github.com/Firmary/bridge/internal/logx"
	"github.com/Firmary/bridge/internal/pairing"
	"github.com/Firmary/bridge/internal/service"
	"github.com/Firmary/bridge/internal/tray"
	"github.com/Firmary/bridge/internal/update"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "0.0.0-dev"

const usageText = `usage: firmary-link [command] [flags]

  (no command)        the menu-bar or tray app; without a display, the same as run
  run                 run in this terminal (or as a service)
  pair                pair this computer with Firmary, in a terminal
  status              show pairing, sign-ins, service and version
  sign-in claude|chatgpt [--device]
                      sign in to your Claude or ChatGPT plan (--device: a link for any browser)
  sign-out claude|chatgpt
  service install|uninstall|status
                      start with this computer and keep running (systemd, launchd, logon task)
  open-at-login on|off
  version

flags (any command): --data-dir DIR  --env prod|dev|local
  local only: --relay-url ws://127.0.0.1:PORT/ --ticket-key-file FILE
`

func usage() {
	fmt.Fprint(os.Stderr, usageText)
	os.Exit(2)
}

func main() {
	args := os.Args[1:]
	cmd := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	if cmd != "" && cmd != "app" {
		attachConsole()
	}
	if cmd == "version" || cmd == "--version" {
		fmt.Println(version)
		return
	}
	if cmd == "help" || cmd == "-h" || cmd == "--help" {
		fmt.Print(usageText)
		return
	}
	launch(cmd, args)
	var err error
	switch cmd {
	case "":
		err = startDefault(args)
	case "app":
		err = desktop(args)
	case "run":
		err = runTerminal(args, false)
	case "pair":
		err = runTerminal(args, true)
	case "status":
		err = status(args)
	case "sign-in", "signin":
		err = signIn(args)
	case "sign-out", "signout":
		err = signOut(args)
	case "service":
		err = serviceCmd(args)
	case "open-at-login":
		err = openAtLogin(args)
	case "self-test":
		err = selfTest(args)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, brand.Command+":", err)
		os.Exit(1)
	}
}

// launch is the launcher: a newer version the updater installed in the data directory runs instead of this one.
func launch(cmd string, args []string) {
	if cmd == "self-test" || os.Getenv(update.LaunchedEnv) != "" || !update.IsRelease(version) {
		return
	}
	dir := ""
	for i, a := range args {
		if a == "--data-dir" && i+1 < len(args) {
			dir = args[i+1]
		} else if v, ok := strings.CutPrefix(a, "--data-dir="); ok {
			dir = v
		}
	}
	if dir == "" {
		dir = datadir.Default()
	}
	if path := update.Choose(filepath.Join(dir, "update"), version); path != "" {
		if err := agent.Launch(path); err != nil {
			fmt.Fprintln(os.Stderr, brand.Command+": couldn't start the updated version:", err)
		}
	}
}

// common are the flags every command takes.
type common struct {
	env, dataDir, ring, relayURL, keyFile string
	headless, noUpdate                    bool
	dataDirSet                            bool
}

func flags(name string, args []string, extra func(*flag.FlagSet)) (*common, []string) {
	c := &common{}
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	fs.Usage = usage
	fs.StringVar(&c.env, "env", "prod", "Firmary environment: prod, dev, or local (a fake relay on this computer)")
	fs.StringVar(&c.dataDir, "data-dir", "", "where the Link keeps its key, pairing, logs, sign-ins and provider copies")
	fs.StringVar(&c.ring, "ring", "stable", "update ring reported in hello")
	fs.StringVar(&c.relayURL, "relay-url", "", "local only: the fake relay's loopback URL")
	fs.StringVar(&c.keyFile, "ticket-key-file", "", "local only: file with the fake relay's base64 ticket public key")
	fs.BoolVar(&c.headless, "headless", false, "never show the menu-bar or tray app")
	fs.BoolVar(&c.noUpdate, "no-update", false, "don't update this Link or its provider apps")
	if extra != nil {
		extra(fs)
	}
	fs.Parse(args)
	c.dataDirSet = c.dataDir != ""
	if c.dataDir == "" {
		c.dataDir = datadir.Default()
	}
	return c, fs.Args()
}

func isLoopbackURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	return ip != nil && ip.IsLoopback()
}

func (c *common) open() (*agent.Agent, error) {
	var env agent.Environment
	if c.env == "local" {
		if !isLoopbackURL(c.relayURL) || c.keyFile == "" {
			return nil, errors.New("--env local needs --relay-url on a loopback address and --ticket-key-file")
		}
		b, err := os.ReadFile(c.keyFile)
		if err != nil {
			return nil, err
		}
		env = agent.Environment{RelayURL: c.relayURL, TicketKeys: []string{strings.TrimSpace(string(b))}}
	} else {
		var ok bool
		if env, ok = agent.Environments[c.env]; !ok {
			return nil, fmt.Errorf("unknown environment %q", c.env)
		}
		if c.relayURL != "" || c.keyFile != "" {
			return nil, errors.New("--relay-url and --ticket-key-file are only for --env local")
		}
	}
	return agent.Open(agent.Options{Version: version, EnvName: c.env, Env: env, DataDir: c.dataDir, Ring: c.ring,
		Updates: c.env == "prod" && !c.noUpdate})
}

// service is the service and login-item configuration for this install.
func (c *common) service(args ...string) service.Config {
	home, _ := os.UserHomeDir()
	cfg := service.Config{Exe: agent.InstalledExe(), Args: args, Home: home, Root: runtime.GOOS == "linux" && os.Geteuid() == 0}
	if c.dataDirSet {
		cfg.DataDir = c.dataDir
	}
	if c.env != "prod" {
		cfg.Args = append(cfg.Args, "--env", c.env)
	}
	if c.env == "local" {
		cfg.Args = append(cfg.Args, "--relay-url", c.relayURL, "--ticket-key-file", c.keyFile)
	}
	return cfg
}

// hasDisplay reports whether a desktop is there for the menu-bar or tray app.
func hasDisplay() bool {
	switch runtime.GOOS {
	case "windows":
		return true
	case "darwin":
		return os.Getenv("SSH_CONNECTION") == "" && os.Getenv("SSH_TTY") == ""
	}
	return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
}

func startDefault(args []string) error {
	c, _ := flags("firmary-link", args, nil)
	if tray.Available && hasDisplay() && !c.headless {
		return desktop(args)
	}
	return runTerminal(args, false)
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// lock takes the single-instance lock, or says another Link is running.
func lock(a *agent.Agent) (func(), error) {
	release, ok, err := agent.Lock(a.EnvDir)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errAlreadyRunning
	}
	return release, nil
}

var errAlreadyRunning = errors.New(brand.Name + " is already running on this computer for this account")

func desktop(args []string) error {
	if !tray.Available {
		return runTerminal(args, false)
	}
	c, _ := flags("app", args, nil)
	a, err := c.open()
	if err != nil {
		return err
	}
	defer a.Close()
	release, err := lock(a)
	if errors.Is(err, errAlreadyRunning) {
		return nil // a second double-click: the running one is in the menu bar
	}
	if err != nil {
		return err
	}
	defer release()
	// The first time the desktop app runs, it turns Open at login on (the person can turn it off in the menu).
	marker := filepath.Join(a.Root, "desktop-first-run")
	_, statErr := os.Stat(marker)
	first := errors.Is(statErr, os.ErrNotExist)
	if first {
		_ = os.WriteFile(marker, nil, 0o600)
	}
	(&tray.App{Agent: a, Version: version, Login: c.service(), FirstRun: first}).Run()
	return nil
}

// runTerminal runs the Link in a terminal or under a service manager. With pairOnly it stops as soon as the
// computer is paired (the `pair` command), and fails if the pairing is declined.
func runTerminal(args []string, pairOnly bool) error {
	c, _ := flags("run", args, nil)
	a, err := c.open()
	if err != nil {
		return err
	}
	defer a.Close()
	out := &console.Printer{W: os.Stdout, Terminal: console.IsTerminal(os.Stdout)}
	if console.IsTerminal(os.Stdin) {
		// Only a person at a terminal can accept a pairing offer.
		out.In = console.Lines(os.Stdin)
	}

	if pairOnly {
		if rec, _ := a.Pairing(); rec != nil {
			names, _ := a.LoadNames()
			if names.Firm != "" {
				out.Paired(names.Person, names.Firm)
			} else {
				fmt.Println("ok  This computer is already paired.")
			}
			return nil
		}
		if out.In == nil || !out.Terminal {
			return errors.New("pairing needs a terminal: run `firmary-link pair` in one")
		}
		// A running service holds the connection; pause it while pairing here.
		svc := c.service("run")
		if st := service.Query(svc); st.Running {
			if err := service.Stop(svc); err == nil {
				defer service.Start(svc)
			}
		}
	}
	release, err := lock(a)
	if err != nil {
		return err
	}
	defer release()

	ctx, stop := signalContext()
	defer stop()
	out.Banner(version, runtime.GOOS, runtime.GOARCH)
	if !pairOnly {
		for _, p := range agent.Providers {
			if err := a.Ensure(ctx, p.ID); err != nil {
				(&console.Printer{W: os.Stderr}).ProviderFailed(p.App, err)
			} else {
				out.ProviderReady(p.App)
			}
		}
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var reached sync.Once // say it once, not on every reconnect
	var result error
	hooks := agent.Hooks{
		Reached:     func() { reached.Do(out.Reached) },
		PairingCode: out.PairingCode,
		Paired:      out.Paired,
		AskAccept:   out.ConfirmPairing,
	}
	if pairOnly {
		hooks.Ready = func(pairing.Record) { cancel() }
		hooks.Unpaired = func() {
			result = errors.New("not paired")
			cancel()
		}
	}
	if err := a.Run(ctx, hooks); err != nil {
		return err
	}
	if pairOnly && result == nil && ctx.Err() != nil {
		if rec, _ := a.Pairing(); rec == nil {
			return errors.New("not paired")
		}
	}
	return result
}

func providerArg(args []string) (agent.ProviderInfo, error) {
	if len(args) != 1 {
		return agent.ProviderInfo{}, errors.New("say which: claude or chatgpt")
	}
	p, ok := agent.ProviderByName(args[0])
	if !ok {
		return p, fmt.Errorf("unknown provider %q: claude or chatgpt", args[0])
	}
	return p, nil
}

func signIn(args []string) error {
	var device bool
	c, rest := flags("sign-in", reorder(args), func(fs *flag.FlagSet) {
		fs.BoolVar(&device, "device", false, "sign in with a link you open on any browser (for a computer without one)")
	})
	p, err := providerArg(rest)
	if err != nil {
		return err
	}
	a, err := c.open()
	if err != nil {
		return err
	}
	defer a.Close()
	prof, err := a.Profile(p.ID)
	if errors.Is(err, agent.ErrNotPaired) {
		return errors.New("pair this computer first: run `firmary-link pair`")
	}
	if err != nil {
		return err
	}
	ctx, stop := signalContext()
	defer stop()
	out := &console.Printer{W: os.Stdout, Terminal: true}
	if !a.Present(p.ID) {
		fmt.Printf("Setting up %s (a one-time download)…\n", p.App)
	}
	if err := a.Ensure(ctx, p.ID); err != nil {
		return fmt.Errorf("%s couldn't be set up: %w", p.App, err)
	}
	d := a.Driver(p.ID)
	kind := driver.SignInBrowser
	if device || !hasDisplay() {
		kind = d.Kinds()[1]
	}
	in := bufio.NewReader(os.Stdin)
	asked := 0
	out.SigningIn(p.App, p.Account)
	err = d.SignIn(ctx, prof, kind, driver.SignInUI{
		Step: func(s driver.SignInStep) {
			if s.Kind == driver.SignInBrowser {
				fmt.Printf("Your browser opens %s's sign-in page. If it doesn't, open this link:\n  %s\nWaiting for sign-in…\n", p.Account, s.URL)
				return
			}
			out.SignInLink(s.URL, s.UserCode, s.Kind == driver.SignInPasteCode)
		},
		Code: func(ctx context.Context) (string, error) {
			out.AskCode(asked > 0)
			asked++
			line, err := in.ReadString('\n')
			if err != nil && line == "" {
				return "", err
			}
			return line, nil
		},
	})
	a.RefreshHello()
	if err != nil {
		f := logx.Fields{Provider: p.ID, Reason: "failed"}
		var fail *driver.Failure
		if errors.As(err, &fail) {
			f.ErrorClass, f.Pattern = fail.Class, fail.Pattern
		}
		a.Log.Log(logx.ProviderSignIn, f)
		return fmt.Errorf("%s sign-in didn't finish (%v)", p.Account, err)
	}
	a.Log.Log(logx.ProviderSignIn, logx.Fields{Provider: p.ID, Reason: "ok"})
	st, _ := d.Status(ctx, prof)
	out.SignedIn(p.Account, st.AccountType)
	return nil
}

func signOut(args []string) error {
	c, rest := flags("sign-out", reorder(args), nil)
	p, err := providerArg(rest)
	if err != nil {
		return err
	}
	a, err := c.open()
	if err != nil {
		return err
	}
	defer a.Close()
	prof, err := a.Profile(p.ID)
	if err != nil {
		return err
	}
	ctx, stop := signalContext()
	defer stop()
	a.EnsureLocal(ctx, p.ID)
	if err := a.Driver(p.ID).SignOut(ctx, prof); err != nil {
		return err
	}
	a.RefreshHello()
	a.Log.Log(logx.ProviderSignOut, logx.Fields{Provider: p.ID})
	fmt.Printf("ok  Signed out of %s on this computer\n", p.Account)
	return nil
}

func status(args []string) error {
	c, _ := flags("status", args, nil)
	a, err := c.open()
	if err != nil {
		return err
	}
	defer a.Close()
	ctx, stop := signalContext()
	defer stop()
	(&console.Printer{W: os.Stdout}).Banner(version, runtime.GOOS, runtime.GOARCH)
	fmt.Printf("Data:     %s\n", a.Root)
	rec, err := a.Pairing()
	switch {
	case err != nil:
		fmt.Printf("Pairing:  unreadable (%v)\n", err)
	case rec == nil:
		fmt.Println("Pairing:  not paired (run: firmary-link pair)")
	default:
		names, ok := a.LoadNames()
		if ok && console.IsTerminal(os.Stdout) {
			fmt.Printf("Pairing:  paired to %s · %s\n", names.Person, names.Firm)
		} else {
			fmt.Println("Pairing:  paired")
		}
	}
	for _, p := range agent.Providers {
		a.EnsureLocal(ctx, p.ID)
		st, err := a.Status(ctx, p.ID)
		line := "not signed in"
		switch {
		case err != nil:
			line = "couldn't check (" + err.Error() + ")"
		case st.Installed == "":
			line = p.App + " not set up yet"
		case st.SignedIn:
			line = "signed in"
			if st.AccountType != "" {
				line += " · " + console.PlanName(st.AccountType) + " plan"
			}
		}
		if err == nil && !st.SignedIn && rec != nil {
			line += " (run: firmary-link sign-in " + strings.ToLower(p.Account) + ")"
		}
		pin := a.Pin(p.ID).Version
		fmt.Printf("%-9s %s · %s %s\n", p.Account+":", line, p.App, pin)
	}
	svc := service.Query(c.service("run"))
	switch {
	case svc.Installed && svc.Running:
		fmt.Printf("Service:  running (%s)\n", svc.Kind)
	case svc.Installed:
		fmt.Printf("Service:  installed, not running (%s %s)\n", svc.Kind, svc.Detail)
	default:
		fmt.Println("Service:  not installed (run: firmary-link service install)")
	}
	st, _ := update.LoadState(a.UpdateDir())
	switch {
	case !update.IsRelease(version):
		fmt.Println("Updates:  off (development build)")
	case !a.Opts.Updates:
		fmt.Println("Updates:  off (" + c.env + " environment)")
	case st.Sequence > 0:
		fmt.Printf("Updates:  on · manifest %d\n", st.Sequence)
	default:
		fmt.Println("Updates:  on")
	}
	return nil
}

func serviceCmd(args []string) error {
	c, rest := flags("service", reorder(args), nil)
	if len(rest) != 1 {
		return errors.New("service install, uninstall or status")
	}
	cfg := c.service("run")
	switch rest[0] {
	case "install":
		if !update.IsRelease(version) && c.env == "prod" && os.Getenv("FIRMARY_LINK_ALLOW_DEV_SERVICE") == "" {
			return errors.New("a development build doesn't install a service")
		}
		res, err := service.Install(cfg)
		if err != nil {
			return err
		}
		server := runtime.GOOS == "linux"
		(&console.Printer{W: os.Stdout}).ServiceInstalled(server && res.Lingering)
		if server && !res.Lingering {
			fmt.Println("    It starts when you log in. To keep it running after you log out, ask an admin to run:")
			fmt.Printf("    sudo loginctl enable-linger %s\n", os.Getenv("USER"))
		}
	case "uninstall":
		if err := service.Uninstall(cfg); err != nil {
			return err
		}
		fmt.Println("ok  Removed the service. Your pairing and sign-ins stay on this computer.")
	case "status":
		st := service.Query(cfg)
		fmt.Printf("%s: installed=%v running=%v %s %s\n", st.Kind, st.Installed, st.Running, st.Path, st.Detail)
	default:
		return errors.New("service install, uninstall or status")
	}
	return nil
}

func openAtLogin(args []string) error {
	c, rest := flags("open-at-login", reorder(args), nil)
	if len(rest) != 1 || (rest[0] != "on" && rest[0] != "off") {
		return errors.New("open-at-login on or off")
	}
	if err := service.SetOpenAtLogin(c.service(), rest[0] == "on"); err != nil {
		return err
	}
	fmt.Printf("ok  Open at login is %s\n", rest[0])
	return nil
}

// selfTest is what an update runs before the Link switches to it.
func selfTest(args []string) error {
	c, _ := flags("self-test", args, nil)
	c.noUpdate = true
	a, err := c.open()
	if err != nil {
		return err
	}
	defer a.Close()
	ctx, stop := signalContext()
	defer stop()
	if err := a.SelfTest(ctx); err != nil {
		return err
	}
	fmt.Println("self-test ok " + version)
	return nil
}

// reorder moves flags before positional arguments, so `sign-in claude --device` parses like `sign-in --device claude`.
func reorder(args []string) []string {
	var fl, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			fl = append(fl, a)
			name := strings.TrimLeft(a, "-")
			if !strings.Contains(name, "=") && takesValue(name) && i+1 < len(args) {
				fl = append(fl, args[i+1])
				i++
			}
			continue
		}
		pos = append(pos, a)
	}
	return append(fl, pos...)
}

func takesValue(name string) bool {
	switch name {
	case "env", "data-dir", "ring", "relay-url", "ticket-key-file":
		return true
	}
	return false
}
