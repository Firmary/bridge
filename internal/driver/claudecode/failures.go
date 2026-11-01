package claudecode

import (
	"regexp"

	"github.com/Firmary/firmary-helper/internal/protocol"
)

// patterns are checked in order. Each has a name that is safe to log and to send to Firmary; the matched text is
// never kept. Ported from the fleet's failure classifier.
var patterns = []struct {
	class, name string
	re          *regexp.Regexp
}{
	{protocol.ClassAuth, "authentication_failed", regexp.MustCompile(`(?i)authentication_failed`)},
	{protocol.ClassAuth, "not_logged_in", regexp.MustCompile(`(?i)not logged in|please run /login`)},
	{protocol.ClassAuth, "oauth_token_invalid", regexp.MustCompile(`(?i)OAuth access token is invalid`)},
	{protocol.ClassAuth, "oauth_org_not_allowed", regexp.MustCompile(`(?i)oauth_org_not_allowed`)},
	{protocol.ClassAuth, "subscription_access", regexp.MustCompile(`(?i)subscription access`)},
	{protocol.ClassAuth, "invalid_token", regexp.MustCompile(`(?i)\binvalid (bearer |access |oauth )?(token|api key)\b`)},
	{protocol.ClassAuth, "token_expired_or_revoked", regexp.MustCompile(`(?i)token.*(expired|revoked)`)},
	{protocol.ClassAuth, "unauthorized", regexp.MustCompile(`(?i)unauthori[sz]ed|\b401\b`)},
	{protocol.ClassAuth, "authentication", regexp.MustCompile(`(?i)authentication`)},
	{protocol.ClassRateLimit, "rate_limit", regexp.MustCompile(`(?i)rate.?limit|usage limit|\b429\b|limit reached`)},
	{protocol.ClassTransient, "overloaded", regexp.MustCompile(`(?i)overloaded|\b529\b`)},
	{protocol.ClassTransient, "transient", regexp.MustCompile(`(?i)\b5\d\d\b|ECONNRESET|ETIMEDOUT|EAI_AGAIN|socket hang up|network|timeout|api_error`)},
}

// Classify returns an error class and pattern name for provider error text. Unmatched text is fatal/unclassified.
func Classify(text string) (class, pattern string) {
	for _, p := range patterns {
		if p.re.MatchString(text) {
			return p.class, p.name
		}
	}
	return protocol.ClassFatal, "unclassified"
}
