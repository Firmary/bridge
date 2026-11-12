// Package prompt holds Firmary's default system prompt for the provider apps the Link runs. It replaces the
// provider's own built-in prompt (Claude Code's and Codex's coding-agent instructions): each driver passes it in
// exactly one place.
package prompt

import (
	_ "embed"
	"strings"
)

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

// Firmary's tools exception lines, worded as firmary-os adds them after the required block when the profile turns a
// tool on (packages/adapters/src/ai-router/launch-profile.ts toolLine), so the fallback says what the turn can do.
const (
	webSearchException = "Exception: for this request you may use web search, which your provider runs, to look things " +
		"up on the public web."
	firmaryException = "Exception: for this request you may use the Firmary tools to look up and change the firm's " +
		"records as the person you are helping, with exactly their access. Name the records you used. A change can " +
		"need their approval: when a tool answers that it is waiting for approval, tell them in one short sentence " +
		"what you asked to do and that they can allow or deny it in Firmary, then call the tool again with its " +
		"confirmationId and waitSeconds to wait for their answer. If they deny it, don't try another way to make the " +
		"same change."
	stillNoComputer = "You still cannot open files, run anything or act on the user's computer."
)

// Fallback is the built-in prompt for a turn whose launch profile has no systemPrompt. With Firmary's tools on it is
// Default with the server's exception lines after the required block (its first paragraph), as firmary-os assembles
// it, so it never tells the model it has no tools when it has them.
func Fallback(webSearch, firmary bool) string {
	if !firmary {
		if webSearch {
			return DefaultWebSearch
		}
		return Default
	}
	lines := []string{firmaryException, stillNoComputer}
	if webSearch {
		lines = append([]string{webSearchException}, lines...)
	}
	required, style, _ := strings.Cut(Default, "\n\n")
	return required + "\n\n" + strings.Join(lines, " ") + "\n\n" + style
}
