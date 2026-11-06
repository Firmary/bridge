package agent

import (
	"os"
	"runtime"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/Firmary/bridge/internal/helper"
	"github.com/Firmary/bridge/internal/proc"
	"github.com/Firmary/bridge/internal/protocol"
)

// MaxDeviceName is pairing_reveal's limit on device.name, in UTF-16 code units (what the relay's schema counts).
const MaxDeviceName = 64

// PairingDevice is what pairing_reveal tells Firmary about this computer: its name (macOS ComputerName, Windows
// COMPUTERNAME, otherwise the hostname), os, arch and kind (protocol.KindDesktop or KindServer). The person sees it
// before they confirm.
func PairingDevice(kind string) *protocol.PairingDevice {
	d := &protocol.PairingDevice{Name: DeviceName(computerName()), Kind: kind}
	if goos := runtime.GOOS; goos == "darwin" || goos == "windows" || goos == "linux" {
		d.OS = goos
	}
	if a := helper.HelloArch(runtime.GOARCH); a == "x64" || a == "arm64" {
		d.Arch = a
	}
	return d
}

func computerName() string {
	switch runtime.GOOS {
	case "darwin":
		if out, err := proc.Command("/usr/sbin/scutil", "--get", "ComputerName").Output(); err == nil {
			if n := strings.TrimSpace(string(out)); n != "" {
				return n
			}
		}
	case "windows":
		if n := os.Getenv("COMPUTERNAME"); n != "" {
			return n
		}
	}
	n, _ := os.Hostname()
	return n
}

// DeviceName cleans a computer name for pairing_reveal: valid UTF-8, no control characters, spaces collapsed and
// trimmed, at most MaxDeviceName UTF-16 code units. It returns "" (not sent) when nothing is left.
func DeviceName(raw string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToValidUTF8(raw, "") {
		if unicode.IsSpace(r) || unicode.IsControl(r) || r == utf8.RuneError {
			space = b.Len() > 0
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	units := 0
	var out strings.Builder
	for _, r := range b.String() {
		n := len(utf16.Encode([]rune{r}))
		if units+n > MaxDeviceName {
			break
		}
		units += n
		out.WriteRune(r)
	}
	return strings.TrimSpace(out.String())
}
