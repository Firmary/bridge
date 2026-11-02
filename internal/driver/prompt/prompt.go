// Package prompt holds Firmary's system prompt for the provider apps the Helper runs. It replaces the provider's own
// built-in prompt (Claude Code's and Codex's coding-agent instructions): each driver passes it in exactly one place.
package prompt

import _ "embed"

// System is Firmary's system prompt, from firmary-system-prompt.md. Edit that file to change it.
//
//go:embed firmary-system-prompt.md
var System string
