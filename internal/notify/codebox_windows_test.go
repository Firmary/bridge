package notify

import (
	"context"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procIsWindowVisible = user32.NewProc("IsWindowVisible")
	procIsWindowEnabled = user32.NewProc("IsWindowEnabled")
	procSetWindowTextW  = user32.NewProc("SetWindowTextW")
)

// shown waits for the code box to be on screen and returns its window. Windows shows a dialog once it has set it
// up, so this is the check that it actually appears.
func shown(t *testing.T) uintptr {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		boxMu.Lock()
		h := uintptr(0)
		if openBox != nil {
			h = openBox.hwnd
		}
		boxMu.Unlock()
		if v, _, _ := procIsWindowVisible.Call(h); h != 0 && v != 0 {
			return h
		}
	}
	t.Fatal("the code box never appeared")
	return 0
}

type answer struct {
	text string
	ok   bool
}

func ask(ctx context.Context) chan answer {
	ch := make(chan answer, 1)
	go func() {
		text, ok := AskText(ctx, "Pairing", "Type the code Firmary shows.", "Connect")
		ch <- answer{text, ok}
	}()
	return ch
}

func wait(t *testing.T, ch chan answer) answer {
	t.Helper()
	select {
	case a := <-ch:
		return a
	case <-time.After(10 * time.Second):
		t.Fatal("the code box didn't close")
		return answer{}
	}
}

func setText(h uintptr, s string) {
	p, _ := windows.UTF16PtrFromString(s)
	procSetWindowTextW.Call(h, uintptr(unsafe.Pointer(p)))
}

// The regression: the box must be on screen, take a code and hand it back on Connect.
func TestCodeBoxShowsAndReturnsTheCode(t *testing.T) {
	ch := ask(context.Background())
	h := shown(t)
	if e, _, _ := procIsWindowEnabled.Call(item(h, idOK)); e != 0 {
		t.Error("Connect is enabled before a code is typed")
	}
	if title := windowText(h); title != "Pairing" {
		t.Errorf("title %q", title)
	}
	if prompt := windowText(item(h, idPrompt)); prompt != "Type the code Firmary shows." {
		t.Errorf("prompt %q", prompt)
	}
	setText(item(h, idCode), "abcd-efgh")
	if e, _, _ := procIsWindowEnabled.Call(item(h, idOK)); e == 0 {
		t.Error("Connect stays disabled after a code is typed")
	}
	procPostMessageW.Call(h, wmCommand, idOK, 0)
	a := wait(t, ch)
	if !a.ok || !strings.EqualFold(a.text, "ABCD-EFGH") {
		t.Fatalf("got %q, %v; want ABCD-EFGH", a.text, a.ok)
	}
}

func TestCodeBoxCancel(t *testing.T) {
	ch := ask(context.Background())
	h := shown(t)
	setText(item(h, idCode), "ABCD-EFGH")
	procPostMessageW.Call(h, wmCommand, idCancel, 0)
	if a := wait(t, ch); a.ok {
		t.Fatalf("Cancel answered %q", a.text)
	}
}

// Asking while a box is open (a second click on Enter pairing code…) raises that box; it doesn't open another.
func TestCodeBoxOneAtATime(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	first := ask(ctx)
	h := shown(t)
	if a := wait(t, ask(context.Background())); a.ok {
		t.Fatal("a second box answered")
	}
	if v, _, _ := procIsWindowVisible.Call(h); v == 0 {
		t.Fatal("the first box went away")
	}
	cancel()
	if a := wait(t, first); a.ok {
		t.Fatal("a cancelled context answered")
	}
}

func TestCodeBoxContextEndsBeforeItOpens(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if a := wait(t, ask(ctx)); a.ok {
		t.Fatal("answered after the context ended")
	}
}
