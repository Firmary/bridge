// Package prompt holds Firmary's default system prompt for the provider apps the Link runs. It replaces the
// provider's own built-in prompt (Claude Code's and Codex's coding-agent instructions): each driver passes it in
// exactly one place.
package prompt

import _ "embed"

// Default is Firmary's fallback system prompt, from firmary-system-prompt.md: the required block and the default
// style text, worded as decided on 2026-11-02 (#2105) and the same as firmary-os DEFAULT_REQUIRED_INSTRUCTIONS and
// DEFAULT_STYLE_INSTRUCTIONS. It is used only when the turn's launch profile has no systemPrompt; Firmary's platform
// page edits the text every other turn uses.
//
//go:embed firmary-system-prompt.md
var Default string

// DefaultWebSearch is Default with the required block's tool sentence made true for a turn whose profile turns web
// search on (firmary-system-prompt-web-search.md). Default says there are no tools, which would be false there.
//
//go:embed firmary-system-prompt-web-search.md
var DefaultWebSearch string

// Fallback is the built-in prompt for a turn whose launch profile has no systemPrompt.
func Fallback(webSearch bool) string {
	if webSearch {
		return DefaultWebSearch
	}
	return Default
}
