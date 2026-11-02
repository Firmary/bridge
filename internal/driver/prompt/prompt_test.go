package prompt

import (
	"strings"
	"testing"
)

func TestSystemPromptIsEmbedded(t *testing.T) {
	if !strings.HasPrefix(System, "You are the AI assistant inside Firmary") || strings.Contains(System, "\r") {
		t.Fatalf("unexpected system prompt: %q", System)
	}
}
