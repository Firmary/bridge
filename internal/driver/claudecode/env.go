package claudecode

import (
	"runtime"
	"sort"
	"strings"
)

// passthrough is the allowlist of inherited environment variables. Everything else in the Helper's environment
// (API keys, OAuth tokens, base-URL and Bedrock/Vertex/gateway switches, cloud credentials, the user's own
// CLAUDE_CONFIG_DIR, ...) is dropped, so the pinned Claude Code can only use the plan signed in to its profile.
var passthrough = map[string]bool{
	// Process basics.
	"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "SHELL": true, "TMPDIR": true, "TZ": true,
	"LANG": true, "LANGUAGE": true, "TERM": true, "XDG_RUNTIME_DIR": true,
	// Windows basics.
	"SYSTEMROOT": true, "SYSTEMDRIVE": true, "WINDIR": true, "COMSPEC": true, "PATHEXT": true, "TEMP": true,
	"TMP": true, "USERPROFILE": true, "USERNAME": true, "HOMEDRIVE": true, "HOMEPATH": true, "APPDATA": true,
	"LOCALAPPDATA": true, "PROGRAMDATA": true,
	// Corporate networks: a proxy and an extra CA bundle set by the user or IT. They can't change who is billed.
	"HTTPS_PROXY": true, "HTTP_PROXY": true, "NO_PROXY": true, "ALL_PROXY": true,
	"SSL_CERT_FILE": true, "SSL_CERT_DIR": true, "NODE_EXTRA_CA_CERTS": true,
}

func allowed(key string) bool {
	if runtime.GOOS == "windows" {
		key = strings.ToUpper(key) // Windows variable names are case-insensitive.
	}
	switch key {
	case "http_proxy", "https_proxy", "no_proxy", "all_proxy":
		return true
	}
	return passthrough[key] || strings.HasPrefix(key, "LC_")
}

// Env builds the child environment: allowlisted inherited variables, then the Helper's own fixed settings. set is
// supplied by this package only, never from relay input.
func Env(inherited []string, configDir string, set map[string]string) []string {
	env := map[string]string{}
	for _, kv := range inherited {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" || !allowed(k) {
			continue
		}
		env[k] = v
	}
	env["CLAUDE_CONFIG_DIR"] = configDir
	// Anthropic's documented switch for distributing a pinned Claude Code through your own channel.
	env["DISABLE_UPDATES"] = "1"
	env["DISABLE_AUTOUPDATER"] = "1"
	for k, v := range set {
		env[k] = v
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}
