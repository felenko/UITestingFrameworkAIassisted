package ai

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolveCursorAgentWindowsFallback(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only install layout")
	}
	local := os.Getenv("LOCALAPPDATA")
	if local == "" {
		t.Skip("LOCALAPPDATA not set")
	}
	expected := filepath.Join(local, "cursor-agent", "cursor-agent.cmd")
	if _, err := os.Stat(expected); err != nil {
		t.Skip("cursor-agent not installed in default location")
	}

	t.Setenv("UITEST_CURSOR_BIN", "")
	got := ResolveCursorAgent()
	if got != expected {
		t.Fatalf("ResolveCursorAgent() = %q, want %q", got, expected)
	}
}

func TestCursorAgentAvailable(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only install layout")
	}
	local := os.Getenv("LOCALAPPDATA")
	if local == "" {
		t.Skip("LOCALAPPDATA not set")
	}
	if _, err := os.Stat(filepath.Join(local, "cursor-agent", "cursor-agent.cmd")); err != nil {
		t.Skip("cursor-agent not installed")
	}
	if !cursorAgentAvailable() {
		t.Fatal("cursorAgentAvailable() = false, want true")
	}
}
