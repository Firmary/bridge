//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// attachConsole connects a command run from a terminal to that terminal. The Link is built as a Windows GUI program
// (so the tray app opens no console window), and such a program has no console of its own.
func attachConsole() {
	const attachParentProcess = ^uint32(0) // ATTACH_PARENT_PROCESS
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("AttachConsole")
	if r, _, _ := proc.Call(uintptr(attachParentProcess)); r == 0 {
		return
	}
	if f, err := os.OpenFile("CONOUT$", os.O_RDWR, 0); err == nil {
		os.Stdout, os.Stderr = f, f
	}
	if f, err := os.OpenFile("CONIN$", os.O_RDWR, 0); err == nil {
		os.Stdin = f
	}
}
