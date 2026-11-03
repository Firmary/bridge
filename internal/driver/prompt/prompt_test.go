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
