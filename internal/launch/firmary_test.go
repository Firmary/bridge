package launch

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/Firmary/bridge/internal/protocol"
)

const goodToken = "fmcp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func cred(url, token string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"url": url, "accessToken": token})
	return b
}

// TestTurnFirmary: the credential comes exactly with the firmary tool, its token has the contract's form, and its
// endpoint is Firmary's for the Link's environment.
func TestTurnFirmary(t *testing.T) {
	on, err := Parse(`{"schema":1,"tools":["firmary"],"maxTurns":10}`)
	if err != nil {
		t.Fatal(err)
	}
	off, _ := Parse(`{"schema":1,"tools":["web_search"]}`)
	for _, c := range []struct{ env, url string }{
		{"prod", "https://firmary.app/api/mcp"},
		{"prod", "https://os.firmary.app/api/mcp"},
		{"dev", "https://dev-os.firmary.app/api/mcp"},
		{"local", "http://127.0.0.1:3000/api/mcp"},
		{"local", "https://localhost:8443/api/mcp"},
		{"local", "http://[::1]:3000/api/mcp"},
	} {
		f, err := TurnFirmary(on, cred(c.url, goodToken), c.env)
		if err != nil || f == nil || f.URL != c.url || f.AccessToken != goodToken {
			t.Errorf("%s %s: %+v %v", c.env, c.url, f, err)
		}
	}
	// Keys other than url and accessToken are ignored, as in every relay message.
	if _, err := TurnFirmary(on, json.RawMessage(`{"url":"https://firmary.app/api/mcp","accessToken":"`+goodToken+`","x":1}`), "prod"); err != nil {
		t.Errorf("extra key: %v", err)
	}
	if f, err := TurnFirmary(off, nil, "prod"); f != nil || err != nil {
		t.Errorf("tool off, no credential: %+v %v", f, err)
	}

	for _, c := range []struct {
		name string
		p    Profile
		raw  json.RawMessage
		env  string
	}{
		{"tool on, no credential", on, nil, "prod"},
		{"credential, tool off", off, cred("https://firmary.app/api/mcp", goodToken), "prod"},
		{"wrong host", on, cred("https://firmary.example/api/mcp", goodToken), "prod"},
		{"lookalike host", on, cred("https://evilfirmary.app/api/mcp", goodToken), "prod"},
		{"suffix host", on, cred("https://firmary.app.evil.example/api/mcp", goodToken), "prod"},
		{"empty label", on, cred("https://.firmary.app/api/mcp", goodToken), "prod"},
		{"http in prod", on, cred("http://firmary.app/api/mcp", goodToken), "prod"},
		{"port in prod", on, cred("https://firmary.app:8443/api/mcp", goodToken), "prod"},
		{"loopback in prod", on, cred("http://127.0.0.1:3000/api/mcp", goodToken), "prod"},
		{"Firmary host in local", on, cred("https://firmary.app/api/mcp", goodToken), "local"},
		{"unknown environment", on, cred("https://firmary.app/api/mcp", goodToken), ""},
		{"wrong path", on, cred("https://firmary.app/mcp", goodToken), "prod"},
		{"trailing slash", on, cred("https://firmary.app/api/mcp/", goodToken), "prod"},
		{"query string", on, cred("https://firmary.app/api/mcp?x=1", goodToken), "prod"},
		{"fragment", on, cred("https://firmary.app/api/mcp#x", goodToken), "prod"},
		{"userinfo", on, cred("https://u:p@firmary.app/api/mcp", goodToken), "prod"},
		{"escaped path", on, cred("https://firmary.app/api/%6Dcp", goodToken), "prod"},
		{"other prefix", on, cred("https://firmary.app/api/mcp", "fmrt_"+goodToken[5:]), "prod"},
		{"short token", on, cred("https://firmary.app/api/mcp", goodToken[:47]), "prod"},
		{"long token", on, cred("https://firmary.app/api/mcp", goodToken+"A"), "prod"},
		{"token character", on, cred("https://firmary.app/api/mcp", goodToken[:47]+"."), "prod"},
		{"null", on, json.RawMessage(`null`), "prod"},
		{"not an object", on, json.RawMessage(`"https://firmary.app/api/mcp"`), "prod"},
		{"no url", on, json.RawMessage(`{"accessToken":"` + goodToken + `"}`), "prod"},
		{"no token", on, json.RawMessage(`{"url":"https://firmary.app/api/mcp"}`), "prod"},
		{"url as a number", on, json.RawMessage(`{"url":1,"accessToken":"` + goodToken + `"}`), "prod"},
		{"repeated key", on, json.RawMessage(`{"url":"https://firmary.app/api/mcp","accessToken":"` + goodToken +
			`","accessToken":"` + goodToken + `"}`), "prod"},
	} {
		if f, err := TurnFirmary(c.p, c.raw, c.env); !errors.Is(err, ReasonFirmaryInvalid) || f != nil {
			t.Errorf("%s: got %+v %v, want firmary_tools_invalid", c.name, f, err)
		}
	}
}

// TestFirmaryRedacted: formatting a credential never shows the token or the endpoint.
func TestFirmaryRedacted(t *testing.T) {
	f := Firmary{URL: "https://firmary.app/api/mcp", AccessToken: goodToken}
	for _, s := range []string{fmt.Sprint(f), fmt.Sprintf("%v %+v %#v", f, &f, f)} {
		if strings.Contains(s, goodToken) || strings.Contains(s, "firmary.app") {
			t.Errorf("formatted credential: %s", s)
		}
	}
}

// TestSharedFirmaryFixtures: the shared turn fixture with Firmary tools is accepted in prod, and the shared invalid
// credentials are refused.
func TestSharedFirmaryFixtures(t *testing.T) {
	raw, err := os.ReadFile("../protocol/testdata/relay-to-helper.fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Valid, Invalid []struct {
			Name, Path string
			Document   json.RawMessage
		}
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	check := func(doc json.RawMessage) error {
		turn, err := protocol.Decode[protocol.Turn](doc)
		if err != nil {
			return err
		}
		p, err := Parse(turn.Profile)
		if err != nil {
			return err
		}
		_, err = TurnFirmary(p, turn.Firmary, "prod")
		return err
	}
	valid, invalid := 0, 0
	for _, c := range f.Valid {
		if turn, _ := protocol.Decode[protocol.Turn](c.Document); turn.Type == protocol.TypeTurn && len(turn.Firmary) > 0 {
			valid++
			if err := check(c.Document); err != nil {
				t.Errorf("valid %q refused: %v", c.Name, err)
			}
		}
	}
	for _, c := range f.Invalid {
		if strings.HasPrefix(c.Path, "/firmary") {
			invalid++
			if err := check(c.Document); !errors.Is(err, ReasonFirmaryInvalid) {
				t.Errorf("invalid %q: got %v", c.Name, err)
			}
		}
	}
	if valid == 0 || invalid == 0 {
		t.Fatalf("%d valid and %d invalid Firmary fixtures", valid, invalid)
	}
}
