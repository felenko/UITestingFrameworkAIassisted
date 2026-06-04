package ai

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Adapter abstracts one AI provider CLI (docs/02 §4.2). Each adapter declares
// how it receives the image, how it receives the question, and how its stdout
// maps to a verdict (parsing is shared via ParseVerdict).
type Adapter interface {
	Name() string
	Available() bool
	// BuildCommand returns the subprocess to run for a prompt that already
	// embeds the screenshot path.
	BuildCommand(ctx context.Context, prompt, imagePath, model string) *exec.Cmd
}

// NewAdapter returns the adapter for a provider name.
func NewAdapter(provider string) (Adapter, bool) {
	switch provider {
	case "claude":
		return claudeAdapter{}, true
	case "codex":
		return codexAdapter{}, true
	case "cursor":
		return cursorAdapter{}, true
	default:
		return nil, false
	}
}

// envOverride lets users retune a provider invocation without recompiling.
// Format: a command line with {prompt} and {image} placeholders, e.g.
//   UITEST_CLAUDE_CMD="claude -p {prompt} --allowedTools Read"
func envOverride(key, prompt, imagePath string, ctx context.Context) *exec.Cmd {
	tmpl := os.Getenv(key)
	if tmpl == "" {
		return nil
	}
	fields := strings.Fields(tmpl)
	for i, f := range fields {
		f = strings.ReplaceAll(f, "{prompt}", prompt)
		f = strings.ReplaceAll(f, "{image}", imagePath)
		fields[i] = f
	}
	if len(fields) == 0 {
		return nil
	}
	return exec.CommandContext(ctx, fields[0], fields[1:]...)
}

// --- claude (Claude Code CLI) ------------------------------------------------

type claudeAdapter struct{}

func (claudeAdapter) Name() string { return "claude" }

func (claudeAdapter) Available() bool {
	_, err := exec.LookPath("claude")
	return err == nil
}

func (claudeAdapter) BuildCommand(ctx context.Context, prompt, imagePath, model string) *exec.Cmd {
	if c := envOverride("UITEST_CLAUDE_CMD", prompt, imagePath, ctx); c != nil {
		return c
	}
	// `claude -p` runs non-interactively. Allow the Read tool and grant access
	// to the screenshot's directory so it can view the image referenced in the
	// prompt.
	args := []string{"-p", prompt, "--allowedTools", "Read"}
	if dir := filepath.Dir(imagePath); dir != "" {
		args = append(args, "--add-dir", dir)
	}
	if model != "" && model != "default" {
		args = append(args, "--model", model)
	}
	return exec.CommandContext(ctx, "claude", args...)
}

// --- codex (OpenAI Codex CLI) ------------------------------------------------

type codexAdapter struct{}

func (codexAdapter) Name() string { return "codex" }

func (codexAdapter) Available() bool {
	_, err := exec.LookPath("codex")
	return err == nil
}

func (codexAdapter) BuildCommand(ctx context.Context, prompt, imagePath, model string) *exec.Cmd {
	if c := envOverride("UITEST_CODEX_CMD", prompt, imagePath, ctx); c != nil {
		return c
	}
	args := []string{"exec"}
	if model != "" && model != "default" {
		args = append(args, "-m", model)
	}
	args = append(args, prompt)
	return exec.CommandContext(ctx, "codex", args...)
}

// --- cursor (Cursor agent CLI) ----------------------------------------------

type cursorAdapter struct{}

func (cursorAdapter) Name() string { return "cursor" }

func (cursorAdapter) Available() bool {
	_, err := exec.LookPath("cursor-agent")
	return err == nil
}

func (cursorAdapter) BuildCommand(ctx context.Context, prompt, imagePath, model string) *exec.Cmd {
	if c := envOverride("UITEST_CURSOR_CMD", prompt, imagePath, ctx); c != nil {
		return c
	}
	args := []string{"-p"}
	if model != "" && model != "default" {
		args = append(args, "--model", model)
	}
	args = append(args, prompt)
	return exec.CommandContext(ctx, "cursor-agent", args...)
}
