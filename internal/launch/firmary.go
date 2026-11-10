package launch

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
)

// ReasonFirmaryInvalid refuses a turn whose Firmary tools don't match its profile: the profile turns the firmary tool
// on and the turn carries no valid credential, or the turn carries one the profile didn't ask for.
const ReasonFirmaryInvalid Reason = "firmary_tools_invalid"

// FirmaryPath is the only path Firmary's MCP endpoint may have.
const FirmaryPath = "/api/mcp"

var (
	// firmaryTokenRe is the form of a Firmary MCP access token (contract RelayTurn.firmary.accessToken).
	firmaryTokenRe = regexp.MustCompile(`^fmcp_[A-Za-z0-9_-]{43}$`)
	// firmaryURLRe is the contract's form of the endpoint, plus a bracketed [::1] for a local relay. It rules out
	// userinfo, a query, a fragment and any other path before the URL is parsed.
	firmaryURLRe = regexp.MustCompile(`^https?://(?:\[::1\]|[A-Za-z0-9.-]+)(?::[0-9]{1,5})?/api/mcp$`)
)

// maxFirmaryURL is the contract's limit on the endpoint's length.
const maxFirmaryURL = 300

// Firmary is a turn's credential for Firmary's own MCP server: its endpoint and a short-lived access token scoped to
// the paired person and this one turn. It comes with the turn, outside the signed profile, and only when the
// profile turns the firmary tool on. Neither field is ever logged or put on a command line; String hides both.
type Firmary struct {
	URL         string
	AccessToken string
}

// String and GoString keep the token and endpoint out of anything that formats a Firmary with %v or %#v.
func (Firmary) String() string   { return "launch.Firmary{redacted}" }
func (Firmary) GoString() string { return "launch.Firmary{redacted}" }

// TurnFirmary checks a turn's optional firmary object (raw, as received; empty when absent) against its parsed profile
// and the Link's environment, and returns the credential when the profile turns the firmary tool on. It refuses with
// ReasonFirmaryInvalid when the tool and the credential don't come together, or the credential isn't valid. Keys
// other than url and accessToken are ignored, as in every relay message; a repeated key is refused.
func TurnFirmary(p Profile, raw json.RawMessage, env string) (*Firmary, error) {
	on, sent := p.HasTool(ToolFirmary), len(raw) > 0
	switch {
	case !on && !sent:
		return nil, nil
	case on != sent:
		return nil, ReasonFirmaryInvalid
	}
	m, ok := object(raw)
	if !ok {
		return nil, ReasonFirmaryInvalid
	}
	var f Firmary
	if f.URL, ok = str(m["url"]); !ok || !firmaryURLAllowed(f.URL, env) {
		return nil, ReasonFirmaryInvalid
	}
	if f.AccessToken, ok = str(m["accessToken"]); !ok || !firmaryTokenRe.MatchString(f.AccessToken) {
		return nil, ReasonFirmaryInvalid
	}
	return &f, nil
}

// firmaryURLAllowed reports whether raw is Firmary's MCP endpoint for env. prod and dev: https on firmary.app or one
// of its subdomains, on the default port. local (a fake relay on this computer): http or https on a loopback name,
// any port. Any other environment allows nothing.
func firmaryURLAllowed(raw, env string) bool {
	if len(raw) > maxFirmaryURL || !firmaryURLRe.MatchString(raw) {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" ||
		u.Path != FirmaryPath || u.RawPath != "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	switch env {
	case "prod", "dev":
		return u.Scheme == "https" && u.Port() == "" && firmaryHost(host)
	case "local":
		return (u.Scheme == "http" || u.Scheme == "https") && (host == "127.0.0.1" || host == "localhost" || host == "::1")
	}
	return false
}

// firmaryHost reports whether host is firmary.app or a subdomain of it, each label non-empty.
func firmaryHost(host string) bool {
	if host == "firmary.app" {
		return true
	}
	sub, ok := strings.CutSuffix(host, ".firmary.app")
	if !ok || sub == "" {
		return false
	}
	for _, label := range strings.Split(sub, ".") {
		if label == "" || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
	}
	return true
}
