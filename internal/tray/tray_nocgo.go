//go:build darwin && !cgo

// Package tray is the desktop app. This build has none: the macOS menu bar needs cgo, so a cgo-less macOS build
// (the cross-compile check in CI) runs the terminal Link instead.
package tray

import (
	"github.com/Firmary/bridge/internal/agent"
	"github.com/Firmary/bridge/internal/service"
)

// Available reports whether this build has the desktop app.
const Available = false

// App is the desktop app around one agent.
type App struct {
	Agent    *agent.Agent
	Version  string
	Login    service.Config
	FirstRun bool
}

// Run is never called when Available is false.
func (a *App) Run() { panic("tray: no desktop app in this build") }
