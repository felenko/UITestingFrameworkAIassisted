package ai

import (
	"strings"
	"testing"
)

func TestBuildAssertPromptCursorUsesScreenshotPath(t *testing.T) {
	got := buildAssertPrompt("cursor", "Is the shell open?", `C:\out\shot.png`)
	for _, want := range []string{
		"strict image classifier",
		"C:\\out\\shot.png",
		"Question: Is the shell open?",
		"YES or NO",
		"Do not explain",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("cursor prompt missing %q:\n%s", want, got)
		}
	}
	// Must be a single line: cursor-agent runs via a .cmd batch wrapper that can
	// truncate args at embedded newlines.
	if strings.ContainsAny(got, "\n\r") {
		t.Fatalf("cursor prompt must be single-line, got:\n%q", got)
	}
}

func TestBuildExtractPromptCursorSingleLine(t *testing.T) {
	got := buildExtractPrompt("cursor", "What is the\nbalance?", `C:\out\shot.png`)
	if strings.ContainsAny(got, "\n\r") {
		t.Fatalf("cursor extract prompt must be single-line, got:\n%q", got)
	}
}

func TestBuildAssertPromptClaudeKeepsPathHint(t *testing.T) {
	got := buildAssertPrompt("claude", "Is the shell open?", `C:\out\shot.png`)
	for _, want := range []string{"Open and view that image file", `C:\out\shot.png`} {
		if !strings.Contains(got, want) {
			t.Fatalf("claude prompt missing %q:\n%s", want, got)
		}
	}
}
