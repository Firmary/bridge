// Package prompt holds Firmary's default system prompt for the provider apps the Helper runs. It replaces the
// provider's own built-in prompt (Claude Code's and Codex's coding-agent instructions): each driver passes it in
// exactly one place.
package prompt

import _ "embed"

// Default is Firmary's fallback system prompt, from firmary-system-prompt.md. Edit that file to change it. It is
// used only when the turn's launch profile has no systemPrompt. The wording is a placeholder.
//
//go:embed firmary-system-prompt.md
var Default string
