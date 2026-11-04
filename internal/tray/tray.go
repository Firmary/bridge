//go:build !darwin || cgo

// Package tray is the desktop app: the Link in the macOS menu bar, the Windows notification area or a Linux desktop's
// tray. It shows the pairing code and status, asks the one pairing question, and offers sign-in, Open at login,
// Check for updates and Quit. It opens no window and listens on nothing; notifications appear only when the Link
// needs the person (a pairing code, a pairing question, a finished or failed sign-in, an update that failed).
package tray

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"

	"fyne.io/systray"

	"github.com/Firmary/bridge/internal/agent"
	"github.com/Firmary/bridge/internal/brand"
	"github.com/Firmary/bridge/internal/console"
	"github.com/Firmary/bridge/internal/driver"
	"github.com/Firmary/bridge/internal/notify"
	"github.com/Firmary/bridge/internal/pairing"
	"github.com/Firmary/bridge/internal/service"
	"github.com/Firmary/bridge/internal/update"
)

var (
	//go:embed icons/tray-template.png
	iconTemplate []byte
	//go:embed icons/tray.png
	iconPNG []byte
	//go:embed icons/tray.ico
	iconICO []byte
)

// Available reports whether this build has the desktop app.
const Available = true

// App is the desktop app around one agent.
type App struct {
	Agent   *agent.Agent
	Version string
	// Login is the login item's configuration (the installed executable).
	Login service.Config
	// FirstRun turns Open at login on, once, the first time the desktop app runs.
	FirstRun bool

	ctx    context.Context
	cancel context.CancelFunc

	mu        sync.Mutex
	phase     string // starting, offline, waiting, offer, paired, connected
	code      string
	names     agent.Names
	paired    bool
	offer     *offer
	providers map[string]provider

	m menu
}

type offer struct {
	person, email, firm string
	answer              chan bool
}

type provider struct {
	signedIn bool
	plan     string
	busy     string // what is happening right now, e.g. "Setting up Claude Code…"
}

type menu struct {
	status, code, codeHint, offerWho, offerFirm, accept, decline *systray.MenuItem
	provStatus, provAction                                       map[string]*systray.MenuItem
	login, update, quit                                          *systray.MenuItem
}

// Run runs the desktop app until the person quits. It must be called on the main goroutine.
func (a *App) Run() {
	a.ctx, a.cancel = context.WithCancel(context.Background())
	a.providers = map[string]provider{}
	a.phase = "starting"
	if rec, _ := a.Agent.Pairing(); rec != nil {
		a.paired = true
		a.names, _ = a.Agent.LoadNames()
	}
	systray.Run(a.ready, func() { a.cancel() })
}

func (a *App) ready() {
	switch runtime.GOOS {
	case "darwin":
		systray.SetTemplateIcon(iconTemplate, iconTemplate)
	case "windows":
		systray.SetIcon(iconICO)
	default:
		systray.SetIcon(iconPNG)
	}
	systray.SetTooltip(brand.Name)

	title := systray.AddMenuItem(brand.Name+" "+a.Version, "")
	title.Disable()
	m := &a.m
	m.status = systray.AddMenuItem("Starting…", "")
	m.status.Disable()
	m.code = systray.AddMenuItem("", "")
	m.code.Disable()
	m.codeHint = systray.AddMenuItem("Check that Firmary shows the same code, then pair there", "")
	m.codeHint.Disable()
	m.offerWho = systray.AddMenuItem("", "")
	m.offerWho.Disable()
	m.offerFirm = systray.AddMenuItem("", "")
	m.offerFirm.Disable()
	m.accept = systray.AddMenuItem("Pair this computer", "Only if this is you and your firm")
	m.decline = systray.AddMenuItem("Don't pair", "")
	systray.AddSeparator()
	m.provStatus, m.provAction = map[string]*systray.MenuItem{}, map[string]*systray.MenuItem{}
	for _, p := range agent.Providers {
		m.provStatus[p.ID] = systray.AddMenuItem(p.Account+": not signed in", "")
		m.provStatus[p.ID].Disable()
		m.provAction[p.ID] = systray.AddMenuItem("Sign in to "+p.Account, "")
	}
	systray.AddSeparator()
	m.login = systray.AddMenuItemCheckbox("Open at login", "", service.OpenAtLogin(a.Login))
	m.update = systray.AddMenuItem("Check for updates", "")
	m.quit = systray.AddMenuItem("Quit "+brand.Name, "")

	if a.FirstRun && !m.login.Checked() {
		if service.SetOpenAtLogin(a.Login, true) == nil {
			m.login.Check()
		}
	}
	a.render()

	go a.clicks()
	go a.runAgent()
}

func (a *App) runAgent() {
	ag := a.Agent
	for _, p := range agent.Providers {
		a.setBusy(p.ID, "Setting up "+p.App+"…")
		err := ag.Ensure(a.ctx, p.ID)
		a.setBusy(p.ID, "")
		if err != nil && a.ctx.Err() == nil {
			notify.Show(brand.Name, p.App+" couldn't be set up. "+brand.Name+" tries again when it restarts.")
		}
	}
	a.refreshProviders()
	go func() {
		t := time.NewTicker(15 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-a.ctx.Done():
				return
			case <-t.C:
				a.refreshProviders()
			}
		}
	}()
	err := ag.Run(a.ctx, agent.Hooks{
		Reached: func() { a.set(func() { a.phase = "reached" }) },
		PairingCode: func(code string) {
			a.set(func() { a.phase, a.code = "waiting", code })
			notify.Show("Pairing code "+code, "Check that Firmary shows the same code, then pair there.")
		},
		AskAccept: a.ask,
		Paired: func(person, firm string) {
			a.set(func() {
				a.phase, a.paired, a.names, a.code, a.offer = "connected", true, agent.Names{Person: person, Firm: firm}, "", nil
			})
			notify.Show("Paired to "+person+" · "+firm, "This computer now runs only requests Firmary signs for "+person+". Not you? Unpair it in Firmary.")
		},
		Ready: func(pairing.Record) {
			a.set(func() { a.phase, a.paired, a.code = "connected", true, "" })
			a.set(func() { a.names, _ = a.Agent.LoadNames() })
			go a.refreshProviders()
		},
		Offline: func() { a.set(func() { a.phase = "offline" }) },
		Unpaired: func() {
			a.set(func() { a.paired, a.names, a.providers = false, agent.Names{}, map[string]provider{} })
		},
	})
	if err != nil && a.ctx.Err() == nil {
		notify.Show(brand.Name+" stopped", err.Error())
		systray.Quit()
	}
}

// ask puts a pairing offer to the person: in the menu, and in a native dialog where there is one. Whichever they
// answer first counts. Nothing is accepted without them.
func (a *App) ask(ctx context.Context, person, email, firm string) (bool, bool) {
	o := &offer{person: person, email: email, firm: firm, answer: make(chan bool, 1)}
	a.set(func() { a.phase, a.offer = "offer", o })
	defer a.set(func() {
		if a.offer == o {
			a.offer = nil
			if a.phase == "offer" {
				a.phase = "reached"
			}
		}
	})
	text := fmt.Sprintf("Firmary asks to pair this computer to:\n\n%s (%s)\n%s\n\nPair only if this is you and your firm. "+
		"If you didn't just pair it in Firmary yourself, don't pair.", person, email, firm)
	notify.Show("Pair this computer?", person+" · "+firm+". Answer in the "+brand.Name+" menu.")
	dctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		if yes, ok := notify.Confirm(dctx, brand.Name, text, "Pair", "Don't pair"); ok {
			select {
			case o.answer <- yes:
			default:
			}
		}
	}()
	select {
	case yes := <-o.answer:
		if !yes {
			notify.Show("Not paired", "This computer isn't paired, and Firmary cancels this pairing.")
		}
		return yes, true
	case <-ctx.Done():
		return false, false
	}
}

func (a *App) set(f func()) {
	a.mu.Lock()
	f()
	a.mu.Unlock()
	a.render()
}

func (a *App) setBusy(id, busy string) {
	a.set(func() {
		p := a.providers[id]
		p.busy = busy
		a.providers[id] = p
	})
}

func (a *App) refreshProviders() {
	for _, p := range agent.Providers {
		st, err := a.Agent.Status(a.ctx, p.ID)
		if err != nil {
			continue
		}
		a.set(func() {
			cur := a.providers[p.ID]
			cur.signedIn, cur.plan = st.SignedIn, st.AccountType
			a.providers[p.ID] = cur
		})
	}
}

// statusLine is the status in the menu: Not paired, Waiting for code, Paired to <firm>, Connected, Offline.
func (a *App) statusLine() string {
	firm := a.names.Firm
	paired := "Paired"
	if firm != "" {
		paired = "Paired to " + firm
	}
	switch a.phase {
	case "waiting":
		return "Not paired · waiting for you to confirm the code in Firmary"
	case "offer":
		return "Firmary asks to pair this computer"
	case "connected":
		return "Connected · " + paired
	case "offline":
		if a.paired {
			return "Offline · " + paired + " · trying again"
		}
		return "Offline · not paired · trying again"
	}
	if a.paired {
		return paired + " · connecting…"
	}
	return "Not paired"
}

func (a *App) render() {
	m := &a.m
	if m.status == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	m.status.SetTitle(a.statusLine())
	if a.phase == "waiting" && a.code != "" {
		m.code.SetTitle("Pairing code: " + a.code)
		m.code.Show()
		m.codeHint.Show()
	} else {
		m.code.Hide()
		m.codeHint.Hide()
	}
	if o := a.offer; o != nil {
		m.offerWho.SetTitle(o.person + " (" + o.email + ")")
		m.offerFirm.SetTitle(o.firm)
		for _, it := range []*systray.MenuItem{m.offerWho, m.offerFirm, m.accept, m.decline} {
			it.Show()
		}
	} else {
		for _, it := range []*systray.MenuItem{m.offerWho, m.offerFirm, m.accept, m.decline} {
			it.Hide()
		}
	}
	for _, p := range agent.Providers {
		st, act := m.provStatus[p.ID], m.provAction[p.ID]
		cur := a.providers[p.ID]
		switch {
		case cur.busy != "":
			st.SetTitle(p.Account + ": " + cur.busy)
			act.Disable()
		case cur.signedIn:
			line := p.Account + ": signed in"
			if cur.plan != "" {
				line += " · " + console.PlanName(cur.plan) + " plan"
			}
			st.SetTitle(line)
			act.SetTitle("Sign out of " + p.Account)
			act.Enable()
		default:
			st.SetTitle(p.Account + ": not signed in")
			act.SetTitle("Sign in to " + p.Account)
			if a.paired {
				act.Enable()
			} else {
				act.Disable()
				st.SetTitle(p.Account + ": sign in after pairing")
			}
		}
	}
}

func (a *App) clicks() {
	m := &a.m
	actions := map[string]<-chan struct{}{}
	for id, it := range m.provAction {
		actions[id] = it.ClickedCh
	}
	claude, chatgpt := agent.Providers[0].ID, agent.Providers[1].ID
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-m.accept.ClickedCh:
			a.answer(true)
		case <-m.decline.ClickedCh:
			a.answer(false)
		case <-actions[claude]:
			go a.toggleSignIn(claude)
		case <-actions[chatgpt]:
			go a.toggleSignIn(chatgpt)
		case <-m.login.ClickedCh:
			on := !m.login.Checked()
			if err := service.SetOpenAtLogin(a.Login, on); err == nil {
				if on {
					m.login.Check()
				} else {
					m.login.Uncheck()
				}
			}
		case <-m.update.ClickedCh:
			go a.checkForUpdates()
		case <-m.quit.ClickedCh:
			systray.Quit()
			return
		}
	}
}

func (a *App) answer(yes bool) {
	a.mu.Lock()
	o := a.offer
	a.mu.Unlock()
	if o != nil {
		select {
		case o.answer <- yes:
		default:
		}
	}
}

func (a *App) toggleSignIn(id string) {
	info, _ := agent.ProviderByName(id)
	a.mu.Lock()
	cur := a.providers[id]
	a.mu.Unlock()
	if cur.busy != "" {
		return
	}
	prof, err := a.Agent.Profile(id)
	if err != nil {
		notify.Show(brand.Name, "Pair this computer in Firmary first, then sign in to "+info.Account+".")
		return
	}
	d := a.Agent.Driver(id)
	if cur.signedIn {
		a.setBusy(id, "signing out…")
		err := d.SignOut(a.ctx, prof)
		a.setBusy(id, "")
		if err != nil {
			notify.Show(brand.Name, "Couldn't sign out of "+info.Account+".")
		}
		a.Agent.RefreshHello()
		a.refreshProviders()
		return
	}
	a.setBusy(id, "Setting up "+info.App+"…")
	if err := a.Agent.Ensure(a.ctx, id); err != nil {
		a.setBusy(id, "")
		notify.Show(brand.Name, info.App+" couldn't be set up. Check the internet connection and try again.")
		return
	}
	a.setBusy(id, "finish signing in in your browser…")
	err = d.SignIn(a.ctx, prof, driver.SignInBrowser, driver.SignInUI{})
	a.setBusy(id, "")
	a.refreshProviders()
	a.Agent.RefreshHello()
	var f *driver.Failure
	switch {
	case err == nil:
		a.mu.Lock()
		plan := a.providers[id].plan
		a.mu.Unlock()
		msg := "Signed in to " + info.Account
		if plan != "" {
			msg += " (" + console.PlanName(plan) + " plan)"
		}
		notify.Show(msg, "Firmary can now use your "+info.Account+" plan on this computer.")
	case errors.As(err, &f) && f.Pattern == "cancelled":
	default:
		notify.Show(info.Account+" sign-in didn't finish", "Try again from the "+brand.Name+" menu.")
	}
}

func (a *App) checkForUpdates() {
	if !update.IsRelease(a.Version) || !a.Agent.Opts.Updates {
		notify.Show(brand.Name, "This build doesn't update itself.")
		return
	}
	a.m.update.Disable()
	defer a.m.update.Enable()
	if err := a.Agent.CheckForUpdates(a.ctx); err != nil && !errors.Is(err, update.ErrReplayed) {
		notify.Show(brand.Name, "Couldn't check for updates. "+brand.Name+" tries again by itself.")
		return
	}
	// A newer version restarts the Link; still here means this is the latest for this computer.
	notify.Show(brand.Name, brand.Name+" "+a.Version+" is up to date.")
}
