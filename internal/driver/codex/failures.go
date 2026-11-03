package codex

import (
	"regexp"

	"github.com/Firmary/bridge/internal/protocol"
)

// patterns are checked in order against Codex's own error messages and stderr. Each has a name that is safe to log
// and to send to Firmary; the matched text is never kept. Shapes are from the S0.2 spike's recorded 0.166.0 runs.
var patterns = []struct {
	class, name string
	re          *regexp.Regexp
}{
	{protocol.ClassAuth, "not_logged_in", regexp.MustCompile(`(?i)not logged in|please (log|sign) ?in|codex login`)},
	{protocol.ClassAuth, "token_expired_or_revoked", regexp.MustCompile(`(?i)(refresh|access) token.*(expired|revoked|invalid)|token_expired|refresh_token_reused`)},
	{protocol.ClassAuth, "workspace_not_allowed", regexp.MustCompile(`(?i)workspace.*(not allowed|restricted|forced)`)},
	{protocol.ClassAuth, "unauthorized", regexp.MustCompile(`(?i)unauthori[sz]ed|\b401\b|\b403\b|forbidden`)},
	{protocol.ClassAuth, "authentication", regexp.MustCompile(`(?i)authentication|invalid_api_key`)},
	{protocol.ClassRateLimit, "usage_limit", regexp.MustCompile(`(?i)usage limit|usage_limit|usageLimitExceeded`)},
	{protocol.ClassRateLimit, "rate_limit", regexp.MustCompile(`(?i)rate.?limit|\b429\b|too many requests`)},
	{protocol.ClassPolicy, "model_not_available", regexp.MustCompile(`(?i)model.*(not supported|not found|does not exist|not available)`)},
	{protocol.ClassFatal, "config_rejected", regexp.MustCompile(`(?i)error loading config|unknown field|unknown variant`)},
	{protocol.ClassTransient, "overloaded", regexp.MustCompile(`(?i)overloaded|server_is_overloaded|\b529\b|\b503\b`)},
	{protocol.ClassTransient, "transient", regexp.MustCompile(`(?i)\b5\d\d\b|stream disconnected|connection (reset|refused|failed)|timed? ?out|network|error sending request`)},
}

// Classify returns an error class and pattern name for Codex error text. Unmatched text is fatal/unclassified.
func Classify(text string) (class, pattern string) {
	for _, p := range patterns {
		if p.re.MatchString(text) {
			return p.class, p.name
		}
	}
	return protocol.ClassFatal, "unclassified"
}
