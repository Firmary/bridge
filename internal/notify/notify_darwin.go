package notify

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

func osascript(ctx context.Context, script string) (string, error) {
	cmd := exec.CommandContext(ctx, "osascript", "-")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.Output()
	return string(out), err
}

func show(title, body string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = osascript(ctx, "display notification "+applescript(body)+" with title "+applescript(title))
}

func confirm(ctx context.Context, title, text, yes, no string) (bool, bool) {
	script := "display dialog " + applescript(text) + " with title " + applescript(title) +
		" buttons {" + applescript(no) + ", " + applescript(yes) + "} default button " + applescript(yes) +
		" cancel button " + applescript(no) + " with icon caution giving up after 600"
	out, err := osascript(ctx, script)
	switch {
	case ctx.Err() != nil:
		return false, false
	case err != nil:
		// The cancel button exits with an error (-128): an explicit no.
		if ee, ok := err.(*exec.ExitError); ok && strings.Contains(string(ee.Stderr), "-128") {
			return false, true
		}
		return false, false
	case strings.Contains(out, "gave up:true"):
		return false, false
	}
	return strings.Contains(out, "button returned:"+yes), true
}
