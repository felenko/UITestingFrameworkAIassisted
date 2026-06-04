package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/felenko/uitest/internal/core/event"
)

// logLevels ranks verbosity for filtering.
var logLevels = map[string]int{"error": 0, "warn": 1, "info": 2, "debug": 3, "trace": 4}

// console renders event-bus events as terminal log lines (docs/02 §1).
type console struct {
	level int
}

func newConsole(level string) *console {
	l, ok := logLevels[level]
	if !ok {
		l = logLevels["info"]
	}
	return &console{level: l}
}

func (c *console) handle(e event.Event) {
	switch e.Type {
	case event.RunStarted:
		fmt.Printf("▶ Running %q — %d case(s)\n", e.Session, e.Total)
	case event.CaseStarted:
		fmt.Printf("\n● %s — %s\n", e.CaseID, e.CaseName)
	case event.StepStarted:
		fmt.Printf("  · [%s/%d] %s\n", e.Phase, e.StepIndex, e.Human)
		if e.MachineDesc != "" && c.level >= logLevels["debug"] {
			fmt.Printf("      %s\n", e.MachineDesc)
		}
	case event.StepFinished:
		if e.Status != "passed" {
			fmt.Printf("    %s %s step\n", mark(e.Status), e.Status)
		}
	case event.ScreenshotCaptured:
		if c.level >= logLevels["debug"] {
			fmt.Printf("      📷 %s (%s)\n", e.Path, e.Which)
		}
	case event.AssertFinished:
		fmt.Printf("    %s assert: %s\n", mark(e.Status), truncate(e.Question, 70))
		if c.level >= logLevels["info"] && e.RawAnswer != "" {
			fmt.Printf("        AI: %s\n", truncate(strings.ReplaceAll(e.RawAnswer, "\n", " "), 80))
		}
	case event.CaseFinished:
		fmt.Printf("  %s %s (%dms)\n", mark(e.Status), e.Status, e.DurationMs)
	case event.RunFinished:
		// summary printed by caller
	case event.Log:
		if lvl, ok := logLevels[e.Level]; ok && lvl <= c.level {
			fmt.Fprintf(os.Stderr, "    [%s] %s\n", e.Level, e.Message)
		}
	}
}

func mark(status string) string {
	switch status {
	case "passed":
		return "✓"
	case "failed":
		return "✗"
	case "error":
		return "⚠"
	case "skipped":
		return "—"
	default:
		return "·"
	}
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
