package notify

import (
	"context"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The box the person types Firmary's pairing code into, on Windows: a dialog drawn by this process with Windows' own
// controls. (It used to be PowerShell's InputBox, started with a hidden window so no console flashed; Windows applies
// that hidden-window flag to the first window the child shows, so the box was invisible and "Enter pairing code…"
// seemed to do nothing.) The executable's manifest (cmd/firmary-link/winres) turns on the current Windows look and
// per-monitor DPI, so the box is sharp at any scale.

var (
	user32                      = windows.NewLazySystemDLL("user32.dll")
	gdi32                       = windows.NewLazySystemDLL("gdi32.dll")
	procDialogBoxIndirectParamW = user32.NewProc("DialogBoxIndirectParamW")
	procEndDialog               = user32.NewProc("EndDialog")
	procGetDlgItem              = user32.NewProc("GetDlgItem")
	procSendMessageW            = user32.NewProc("SendMessageW")
	procPostMessageW            = user32.NewProc("PostMessageW")
	procGetWindowTextW          = user32.NewProc("GetWindowTextW")
	procGetWindowTextLengthW    = user32.NewProc("GetWindowTextLengthW")
	procEnableWindow            = user32.NewProc("EnableWindow")
	procSetFocus                = user32.NewProc("SetFocus")
	procSetForegroundWindow     = user32.NewProc("SetForegroundWindow")
	procSetWindowPos            = user32.NewProc("SetWindowPos")
	procShowWindow              = user32.NewProc("ShowWindow")
	procIsIconic                = user32.NewProc("IsIconic")
	procLoadImageW              = user32.NewProc("LoadImageW")
	procGetSystemMetrics        = user32.NewProc("GetSystemMetrics")
	procGetDpiForWindow         = user32.NewProc("GetDpiForWindow") // Windows 10 1607 and later
	procCreateFontW             = gdi32.NewProc("CreateFontW")
	procDeleteObject            = gdi32.NewProc("DeleteObject")

	dialogProcPtr = windows.NewCallback(dialogProc)
)

const (
	wmInitDialog   = 0x0110
	wmCommand      = 0x0111
	wmDestroy      = 0x0002
	wmSetFont      = 0x0030
	wmSetIcon      = 0x0080
	wmDPIChanged   = 0x02E0
	emLimitText    = 0x00C5
	emSetCueBanner = 0x1501
	enChange       = 0x0300

	idOK     = 1
	idCancel = 2
	idPrompt = 100
	idCode   = 101

	wsPopup, wsCaption, wsSysMenu    = 0x80000000, 0x00C00000, 0x00080000
	wsChild, wsVisible, wsTabStop    = 0x40000000, 0x10000000, 0x00010000
	wsGroup                          = 0x00020000
	wsExClientEdge, wsExAppWindow    = 0x00000200, 0x00040000
	dsSetFont, dsModalFrame          = 0x40, 0x80
	dsSetForeground, dsCenter        = 0x200, 0x800
	esUppercase, esAutoHScroll       = 0x0008, 0x0080
	ssNoPrefix                       = 0x0080
	bsDefPushButton                  = 0x0001
	atomButton, atomEdit, atomStatic = 0x0080, 0x0081, 0x0082

	imageIcon, lrDefaultColor  = 1, 0
	smCXIcon, smCXSmIcon       = 11, 49
	iconSmall, iconBig         = 0, 1
	swRestore                  = 9
	swpNoSize, swpNoMove       = 0x0001, 0x0002
	hwndTopmost, hwndNoTopmost = ^uintptr(0), ^uintptr(1) // (HWND)-1, (HWND)-2
)

// codeBox is the open box. There is one at a time.
type codeBox struct {
	hwnd      uintptr
	font      uintptr
	text      string
	cancelled bool // ctx ended before the box was up
}

var (
	boxMu   sync.Mutex
	openBox *codeBox
)

func canAskText() bool { return true }

// askText shows the code box and waits for Connect (the typed text) or Cancel. While a box is open, asking again
// brings that box to the front instead of opening a second one.
func askText(ctx context.Context, title, prompt, ok string) (string, bool) {
	boxMu.Lock()
	if openBox != nil {
		h := openBox.hwnd
		boxMu.Unlock()
		if h != 0 {
			raise(h)
		}
		return "", false
	}
	b := &codeBox{}
	openBox = b
	boxMu.Unlock()
	defer func() {
		boxMu.Lock()
		openBox = nil
		boxMu.Unlock()
	}()

	tmpl := codeBoxTemplate(title, prompt, ok)
	done := make(chan uintptr, 1)
	go func() {
		// The dialog's message loop runs on this thread until the box closes; the thread ends with the goroutine.
		runtime.LockOSThread()
		var inst windows.Handle
		_ = windows.GetModuleHandleEx(0, nil, &inst)
		r, _, _ := procDialogBoxIndirectParamW.Call(uintptr(inst), uintptr(unsafe.Pointer(&tmpl[0])), 0, dialogProcPtr, 0)
		runtime.KeepAlive(tmpl)
		done <- r
	}()
	var r uintptr
	select {
	case r = <-done:
	case <-ctx.Done():
		boxMu.Lock()
		b.cancelled = true
		h := b.hwnd
		boxMu.Unlock()
		if h != 0 {
			procPostMessageW.Call(h, wmCommand, idCancel, 0)
		}
		<-done
		return "", false
	}
	if r != idOK {
		return "", false
	}
	boxMu.Lock()
	defer boxMu.Unlock()
	return b.text, true
}

func dialogProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	switch msg {
	case wmInitDialog:
		boxMu.Lock()
		b := openBox
		cancelled := b == nil || b.cancelled
		if b != nil {
			b.hwnd = hwnd
		}
		boxMu.Unlock()
		if cancelled {
			procEndDialog.Call(hwnd, idCancel)
			return 1
		}
		code := item(hwnd, idCode)
		setCodeFont(hwnd, dpiOf(hwnd))
		cue, _ := windows.UTF16PtrFromString("ABCD-EFGH")
		procSendMessageW.Call(code, emSetCueBanner, 1, uintptr(unsafe.Pointer(cue)))
		procSendMessageW.Call(code, emLimitText, 32, 0)
		procEnableWindow.Call(item(hwnd, idOK), 0)
		setIcons(hwnd)
		raise(hwnd)
		procSetFocus.Call(code)
		return 0 // the focus is set
	case wmCommand:
		switch id, notification := wparam&0xFFFF, (wparam>>16)&0xFFFF; {
		case id == idCode && notification == enChange:
			n, _, _ := procGetWindowTextLengthW.Call(item(hwnd, idCode))
			procEnableWindow.Call(item(hwnd, idOK), boolArg(n > 0))
		case id == idOK:
			text := windowText(item(hwnd, idCode))
			if text == "" {
				return 1 // Enter on an empty box: keep it open
			}
			boxMu.Lock()
			if openBox != nil {
				openBox.text = text
			}
			boxMu.Unlock()
			procEndDialog.Call(hwnd, idOK)
		case id == idCancel:
			procEndDialog.Call(hwnd, idCancel)
		}
		return 1
	case wmDPIChanged:
		// Windows rescales the dialog and its own font; the code's larger font is this box's, so it follows here.
		setCodeFont(hwnd, (wparam>>16)&0xFFFF)
		return 0 // and Windows moves and resizes the box as usual
	case wmDestroy:
		boxMu.Lock()
		if openBox != nil && openBox.font != 0 {
			procDeleteObject.Call(openBox.font)
			openBox.font = 0
		}
		boxMu.Unlock()
	}
	return 0
}

// raise brings the box to the front: restored if minimized, on top of other windows, and focused where Windows
// allows it (otherwise its taskbar button flashes).
func raise(hwnd uintptr) {
	if r, _, _ := procIsIconic.Call(hwnd); r != 0 {
		procShowWindow.Call(hwnd, swRestore)
	}
	// Topmost and straight back: on top now, without staying above the browser that shows the code.
	procSetWindowPos.Call(hwnd, hwndTopmost, 0, 0, 0, 0, swpNoMove|swpNoSize)
	procSetWindowPos.Call(hwnd, hwndNoTopmost, 0, 0, 0, 0, swpNoMove|swpNoSize)
	procSetForegroundWindow.Call(hwnd)
}

// setCodeFont gives the code a larger Segoe UI than the dialog's, at the window's DPI.
func setCodeFont(hwnd, dpi uintptr) {
	if dpi == 0 {
		dpi = 96
	}
	face, _ := windows.UTF16PtrFromString("Segoe UI")
	height := -int32(13 * dpi / 72) // 13 pt
	f, _, _ := procCreateFontW.Call(uintptr(height), 0, 0, 0, 400, 0, 0, 0, 1 /* DEFAULT_CHARSET */, 0, 0,
		5 /* CLEARTYPE_QUALITY */, 0, uintptr(unsafe.Pointer(face)))
	if f == 0 {
		return
	}
	procSendMessageW.Call(item(hwnd, idCode), wmSetFont, f, 1)
	boxMu.Lock()
	old := uintptr(0)
	if openBox != nil {
		old, openBox.font = openBox.font, f
	}
	boxMu.Unlock()
	if old != 0 {
		procDeleteObject.Call(old)
	}
}

// setIcons puts the app's icon (the executable's APP icon) in the title bar and on the taskbar button.
func setIcons(hwnd uintptr) {
	var inst windows.Handle
	if windows.GetModuleHandleEx(0, nil, &inst) != nil {
		return
	}
	name, _ := windows.UTF16PtrFromString("APP")
	for _, s := range []struct{ which, metric uintptr }{{iconSmall, smCXSmIcon}, {iconBig, smCXIcon}} {
		size, _, _ := procGetSystemMetrics.Call(s.metric)
		icon, _, _ := procLoadImageW.Call(uintptr(inst), uintptr(unsafe.Pointer(name)), imageIcon, size, size, lrDefaultColor)
		if icon != 0 {
			procSendMessageW.Call(hwnd, wmSetIcon, s.which, icon)
		}
	}
}

func dpiOf(hwnd uintptr) uintptr {
	if procGetDpiForWindow.Find() != nil {
		return 96
	}
	dpi, _, _ := procGetDpiForWindow.Call(hwnd)
	return dpi
}

func item(hwnd uintptr, id uintptr) uintptr {
	h, _, _ := procGetDlgItem.Call(hwnd, id)
	return h
}

func windowText(hwnd uintptr) string {
	n, _, _ := procGetWindowTextLengthW.Call(hwnd)
	buf := make([]uint16, n+1)
	procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), n+1)
	return windows.UTF16ToString(buf)
}

func boolArg(b bool) uintptr {
	if b {
		return 1
	}
	return 0
}

// codeBoxTemplate lays out the box in dialog units: the prompt, the code field, then Connect and Cancel at the
// bottom right, in Windows' own dialog font (Segoe UI 9).
func codeBoxTemplate(title, prompt, ok string) []uint32 {
	const width, margin, buttonW, buttonH, gap = 240, 10, 54, 15, 6
	inner := int16(width - 2*margin)
	type control struct {
		atom, id       uint16
		style, exStyle uint32
		x, y, w, h     int16
		text           string
	}
	controls := []control{
		{atomStatic, idPrompt, wsChild | wsVisible | ssNoPrefix, 0, margin, margin, inner, 26, prompt},
		{atomEdit, idCode, wsChild | wsVisible | wsTabStop | wsGroup | esUppercase | esAutoHScroll, wsExClientEdge,
			margin, 40, inner, 16, ""},
		{atomButton, idOK, wsChild | wsVisible | wsTabStop | wsGroup | bsDefPushButton, 0,
			width - margin - 2*buttonW - gap, 68, buttonW, buttonH, ok},
		{atomButton, idCancel, wsChild | wsVisible | wsTabStop, 0, width - margin - buttonW, 68, buttonW, buttonH, "Cancel"},
	}
	var w []uint16
	dword := func(v uint32) { w = append(w, uint16(v), uint16(v>>16)) }
	str := func(s string) {
		u, _ := windows.UTF16FromString(s) // includes the terminating zero
		w = append(w, u...)
	}
	align := func() {
		if len(w)%2 == 1 {
			w = append(w, 0)
		}
	}
	// DLGTEMPLATE, then no menu, the default class, the title and the font.
	dword(wsPopup | wsCaption | wsSysMenu | dsModalFrame | dsSetFont | dsSetForeground | dsCenter)
	dword(wsExAppWindow)
	w = append(w, uint16(len(controls)), 0, 0, width, 68+buttonH+margin)
	w = append(w, 0, 0)
	str(title)
	w = append(w, 9)
	str("Segoe UI")
	for _, c := range controls {
		align() // each DLGITEMTEMPLATE starts on a DWORD
		dword(c.style)
		dword(c.exStyle)
		w = append(w, uint16(c.x), uint16(c.y), uint16(c.w), uint16(c.h), c.id, 0xFFFF, c.atom)
		str(c.text)
		w = append(w, 0) // no creation data
	}
	out := make([]uint32, (len(w)+1)/2)
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(&out[0])), len(out)*2), w)
	return out
}
