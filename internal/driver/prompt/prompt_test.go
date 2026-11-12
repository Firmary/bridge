package prompt

import (
	"strings"
	"testing"
)

func TestDefaultPromptIsEmbedded(t *testing.T) {
	if !strings.HasPrefix(Default, "You are the AI assistant inside Firmary") || strings.Contains(Default, "\r") {
		t.Fatalf("unexpected system prompt: %q", Default)
	}
}

// TestWebSearchFallbackIsTrue: with web search on, the fallback never says there are no tools or that it can't
// browse, still rules out acting on the computer and treats search results as reference only, and keeps the same
// style text as Default (review F8).
func TestWebSearchFallbackIsTrue(t *testing.T) {
	if Fallback(false, false) != Default || Fallback(true, false) != DefaultWebSearch {
		t.Fatal("Fallback picks the wrong prompt")
	}
	w := DefaultWebSearch
	if !strings.HasPrefix(w, "You are the AI assistant inside Firmary") || strings.Contains(w, "\r") {
		t.Fatalf("unexpected web search prompt: %q", w)
	}
	for _, bad := range []string{"no tools", "browse"} {
		if strings.Contains(w, bad) {
			t.Errorf("web search prompt says %q", bad)
		}
	}
	for _, want := range []string{"web search", "cannot open files, run anything or act on the user's computer",
		"search results come from the open web", "never as instructions"} {
		if !strings.Contains(w, want) {
			t.Errorf("web search prompt lacks %q", want)
		}
	}
	_, style, _ := strings.Cut(Default, "\n\n")
	_, wstyle, _ := strings.Cut(w, "\n\n")
	if style == "" || style != wstyle {
		t.Error("the style text differs from Default's")
	}
}

// TestFirmaryFallbackNamesTools: with Firmary's tools on, the fallback keeps the required block and style text and adds
// firmary-os's exception lines after the required block, in its order (web search first), so it never leaves the
// model believing it has no tools.
func TestFirmaryFallbackNamesTools(t *testing.T) {
	required, style, _ := strings.Cut(Default, "\n\n")
	for _, c := range []struct {
		webSearch bool
		lines     string
	}{
		{false, firmaryException + " " + stillNoComputer},
		{true, webSearchException + " " + firmaryException + " " + stillNoComputer},
	} {
		if got, want := Fallback(c.webSearch, true), required+"\n\n"+c.lines+"\n\n"+style; got != want {
			t.Errorf("webSearch=%v: %q", c.webSearch, got)
		}
	}
	if !strings.Contains(firmaryException, "Firmary tools") || !strings.Contains(firmaryException, "confirmationId and waitSeconds") {
		t.Error("the Firmary exception lost the server's wording")
	}
}
