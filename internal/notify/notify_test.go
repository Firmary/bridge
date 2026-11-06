package notify

import (
	"encoding/base64"
	"regexp"
	"strings"
	"testing"
)

// TestToastScriptNeverContainsText: names from the relay, with every quote PowerShell recognises (ASCII and the
// typographic U+2018 to U+201B), reach the toast intact and never appear in the script's source.
func TestToastScriptNeverContainsText(t *testing.T) {
	title := "Jahn ‘CPA’ ‚x‛ 'y'"
	body := "Paired with ’); Start-Process calc; (‘ and Müller & Söhne <b>"
	script := toastScript(title, body)
	// Plain ASCII, so no typographic quote; and only the template's own apostrophes.
	for _, r := range script {
		if r > 0x7e {
			t.Fatalf("the script isn't plain ASCII: %q", r)
		}
	}
	if strings.Count(script, "'") != strings.Count(toastScript("", ""), "'") {
		t.Fatal("an apostrophe from the text is in the script")
	}
	if strings.Contains(script, "Start-Process") || strings.Contains(script, "Jahn") {
		t.Fatal("relay text is in the script's source")
	}
	m := regexp.MustCompile(`FromBase64String\('([A-Za-z0-9+/=]+)'\)`).FindStringSubmatch(script)
	if m == nil {
		t.Fatal("no base64 toast in the script")
	}
	xml, err := base64.StdEncoding.DecodeString(m[1])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(xml), "<text>"+xmlEscape(title)+"</text><text>"+xmlEscape(body)+"</text>") {
		t.Fatalf("the toast lost the text: %s", xml)
	}
}
