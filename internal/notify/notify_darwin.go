package notify

import (
	"context"
	"os/exec"
	"strings"
	"time"

	"github.com/Firmary/bridge/internal/proc"
)

func osascript(ctx context.Context, script string) (string, error) {
	cmd := proc.CommandContext(ctx, "osascript", "-")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.Output()
	return string(out), err
}

func show(title, body string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = osascript(ctx, "display notification "+applescript(body)+" with title "+applescript(title))
}

func canAskText() bool { return true }

func askText(ctx context.Context, title, prompt, ok string) (string, bool) {
	script := "display dialog " + applescript(prompt) + " default answer \"\" with title " + applescript(title) +
		" buttons {\"Cancel\", " + applescript(ok) + "} default button " + applescript(ok) +
		" cancel button \"Cancel\" giving up after 600"
	out, err := osascript(ctx, script)
	if err != nil || ctx.Err() != nil || strings.Contains(out, "gave up:true") {
		return "", false
	}
	// "button returned:Connect, text returned:ABCD-EFGH, gave up:false"
	_, text, found := strings.Cut(strings.TrimSpace(out), "text returned:")
	if !found {
		return "", false
	}
	text, _, _ = strings.Cut(text, ", gave up:")
	return text, true
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
