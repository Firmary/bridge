package codex

import (
	"encoding/json"
	"strings"

	"github.com/Firmary/bridge/internal/brand"
)

// settings are the Link's Codex settings, as TOML dotted keys and values. Keys are from the pinned release's own
// config.schema.json (codex-rs/core/config.schema.json at rust-v0.166.0); the pinned binary runs with
// --strict-config, so a key that a later pin drops fails the turn instead of being silently ignored.
//
// Firmary's system prompt, effort, web search and the Codex knobs are per-turn settings from the launch profile (see
// launchSettings). They are written before these, and a test checks they never share a key.
//
// Together they mean nothing in a turn can act on this computer: no shell, no file edits (the one tool Codex always
// offers, apply_patch, comes from the model catalog and can't be switched off; the read-only sandbox and "never"
// approval reject every write it tries), no MCP servers, apps, plugins, hooks, skills, memories, browser or computer
// use, no AGENTS.md or other project instructions, no environment context, and nothing saved. Web search, which OpenAI
// runs, is off unless the launch profile turns it on, and so is Firmary's own MCP server, which runs on Firmary
// (firmarySettings).
var settings = []string{
	// Updates come only from Firmary's pin: Codex's own update check and prompt are off.
	`check_for_update_on_startup=false`,
	// Credentials stay in this profile's CODEX_HOME (not a machine-wide keychain), and only a ChatGPT login is accepted.
	`cli_auth_credentials_store="file"`,
	`forced_login_method="chatgpt"`,
	// Tools that do run (apply_patch) are sandboxed read-only and are never escalated for approval.
	`approval_policy="never"`,
	`sandbox_mode="read-only"`,
	// No project instruction files (AGENTS.md and fallbacks) and no parent-directory project root.
	`project_doc_max_bytes=0`,
	`project_root_markers=[]`,
	// Nothing about this computer (working directory, shell, files) in the prompt.
	`include_environment_context=false`,
	`include_permissions_instructions=false`,
	`include_apps_instructions=false`,
	`include_collaboration_mode_instructions=false`,
	// Nothing saved, nothing sent to OpenAI beyond the turn.
	`history.persistence="none"`,
	`analytics.enabled=false`,
	`feedback.enabled=false`,
	`memories.generate_memories=false`,
	// No skills, apps or plugins.
	`skills.include_instructions=false`,
	`skills.bundled.enabled=false`,
	`apps._default.enabled=false`,
	`plugins._default.enabled=false`,
	`tools.experimental_request_user_input.enabled=false`,
	// Tool features, each off.
	`features.shell_tool=false`,
	`features.unified_exec=false`,
	`features.shell_snapshot=false`,
	`features.apply_patch_freeform=false`,
	`features.view_image=false`,
	`features.js_repl=false`,
	`features.code_mode=false`,
	`features.code_mode_host=false`,
	`features.multi_agent=false`,
	`features.apps=false`,
	`features.plugins=false`,
	`features.memories=false`,
	`features.hooks=false`,
	`features.plugin_hooks=false`,
	`features.browser_use=false`,
	`features.computer_use=false`,
	`features.image_generation=false`,
	`features.tool_search=false`,
	`features.tool_suggest=false`,
	`features.skill_search=false`,
	`features.goals=false`,
	`features.request_permissions_tool=false`,
	`features.standalone_web_search=false`,
	`features.remote_control=false`,
	`features.sleep_tool=false`,
}

// tomlString quotes s as a TOML basic string. JSON string escapes are all valid in TOML basic strings; JSON leaves
// DEL (U+007F) raw, which TOML forbids in a basic string, so it is escaped too.
func tomlString(s string) string {
	b, _ := json.Marshal(s)
	return strings.ReplaceAll(string(b), "\x7f", `\u007f`)
}

// configTOML renders the launch settings, then the fixed settings, as config.toml (top-level dotted keys).
func configTOML(perTurn []string) string {
	var b strings.Builder
	b.WriteString("# Written by the " + brand.Name + " before every run. Changes here are replaced.\n")
	for _, s := range append(append([]string(nil), perTurn...), settings...) {
		k, v, _ := strings.Cut(s, "=")
		b.WriteString(k + " = " + v + "\n")
	}
	return b.String()
}

// configArgs renders the launch settings, then the fixed settings, as -c session flags, so a fixed setting is always
// the last word. Codex ranks these above a project's .codex/config.toml and the user config; only machine-wide
// managed configuration ranks higher.
func configArgs(perTurn []string) []string {
	var out []string
	for _, s := range append(append([]string(nil), perTurn...), settings...) {
		out = append(out, "-c", s)
	}
	return out
}
