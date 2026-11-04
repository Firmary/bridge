package codex

import (
	"runtime"
	"sort"
	"strings"
)

// passthrough is the allowlist of inherited environment variables. Everything else in the Link's environment
// (OPENAI_API_KEY, CODEX_API_KEY, OPENAI_BASE_URL, the user's own CODEX_HOME, RUST_LOG, cloud credentials, other
// providers' keys, ...) is dropped, so the pinned Codex can only use the ChatGPT plan signed in to its profile.
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
	"SSL_CERT_FILE": true, "SSL_CERT_DIR": true, "CODEX_CA_CERTIFICATE": true,
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

// Env builds the child environment: allowlisted inherited variables, then the profile's CODEX_HOME.
func Env(inherited []string, codexHome string) []string {
	env := map[string]string{}
	for _, kv := range inherited {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" || !allowed(k) {
			continue
		}
		env[k] = v
	}
	env["CODEX_HOME"] = codexHome
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}
