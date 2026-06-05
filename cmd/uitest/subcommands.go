package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/felenko/uitest/internal/core/ai"
	"github.com/felenko/uitest/internal/core/platform"
	"github.com/felenko/uitest/internal/core/result"
	"github.com/felenko/uitest/internal/core/session"
)

// cmdValidate parses + schema-validates a session (docs/02 §1).
func cmdValidate(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "error: validate requires <session.yaml>")
		return 2
	}
	if _, err := session.Load(args[0]); err != nil {
		fmt.Fprintf(os.Stderr, "INVALID: %v\n", err)
		return 2
	}
	fmt.Printf("OK: %s is valid\n", args[0])
	return 0
}

// cmdSchema prints the JSON Schema for TestSession.yaml (generated from the Go
// types) to stdout, or to a file with -o. Editors point at this for
// autocomplete, hover hints, and validation.
func cmdSchema(args []string) int {
	fs := newFlagSet("schema")
	out := fs.String("o", "", "write schema to this file instead of stdout")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	data, err := session.GenerateSchema()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: generating schema: %v\n", err)
		return 2
	}
	if *out != "" {
		if err := os.WriteFile(*out, data, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "error: writing %s: %v\n", *out, err)
			return 2
		}
		fmt.Printf("wrote %s\n", *out)
		return 0
	}
	os.Stdout.Write(data)
	return 0
}

// cmdList prints the cases/steps the runner would execute.
func cmdList(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "error: list requires <session.yaml>")
		return 2
	}
	sess, err := session.Load(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 2
	}
	fmt.Printf("Session: %s\n", sess.Session.Name)
	fmt.Printf("App: %s\n", sess.Session.Application.Path)
	fmt.Printf("Provider: %s\n\n", sess.Session.AI.Provider)
	for _, tc := range sess.TestCases {
		fmt.Printf("● %s — %s", tc.ID, tc.Name)
		if len(tc.Tags) > 0 {
			fmt.Printf("  [%s]", strings.Join(tc.Tags, ", "))
		}
		fmt.Println()
		printSteps("setup", tc.Setup)
		printSteps("steps", tc.Steps)
		fmt.Printf("    ✓ validation: %s\n", tc.Validation.Human)
		for i, a := range tc.Validation.Assert {
			label := a.Human
			if label == "" {
				label = a.Question
			}
			fmt.Printf("        assert[%d] (%s, expect %s): %s\n", i, a.Action, defExpect(a.Expect), truncate(label, 70))
		}
		printSteps("teardown", tc.Teardown)
		fmt.Println()
	}
	return 0
}

func printSteps(phase string, steps []session.Step) {
	for i, s := range steps {
		fmt.Printf("    %s[%d] %s\n", phase, i, s.Human)
	}
}

func defExpect(e string) string {
	if e == "" {
		return "yes"
	}
	return e
}

// cmdDoctor checks AI CLIs, screen capture, and input availability (docs/02 §1).
func cmdDoctor(args []string) int {
	fmt.Println("uitest doctor")
	ok := true

	for _, p := range []string{"claude", "codex", "cursor"} {
		adapter, _ := ai.NewAdapter(p)
		status := "not found"
		mark := "✗"
		if adapter != nil && adapter.Available() {
			status, mark = "available", "✓"
		}
		fmt.Printf("  %s AI provider %-7s: %s\n", mark, p, status)
	}

	drv := platform.New()
	_ = drv.SetDPIAware()
	if _, err := drv.CaptureScreen(); err != nil {
		fmt.Printf("  ✗ screen capture: %v\n", err)
		ok = false
	} else {
		b := drv.ScreenBounds()
		fmt.Printf("  ✓ screen capture: OK (%dx%d @ %.2gx DPI)\n", b.Width, b.Height, drv.DPIScale())
	}

	// At least the default provider must be reachable.
	claude, _ := ai.NewAdapter("claude")
	if claude == nil || !claude.Available() {
		fmt.Println("\nNo default AI provider (claude) found — assertions will fail.")
		ok = false
	}
	if !ok {
		return 2
	}
	fmt.Println("\nEnvironment looks good.")
	return 0
}

// cmdApprove promotes recorded actual images into baselines (docs/05 §6).
func cmdApprove(args []string) int {
	fs := newFlagSet("approve")
	caseID := fs.String("case", "", "only this case id")
	assertID := fs.String("assert", "", "only this assert id")
	all := fs.Bool("all", false, "approve all (including failed)")
	baselines := fs.String("baselines", "./baselines", "baselines output dir")

	dir, ok := parseWithPath(fs, args, "approve")
	if !ok {
		return 2
	}

	data, err := os.ReadFile(filepath.Join(dir, "results.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot read results.json in %s: %v\n", dir, err)
		return 2
	}
	var res result.Results
	if err := json.Unmarshal(data, &res); err != nil {
		fmt.Fprintf(os.Stderr, "error: bad results.json: %v\n", err)
		return 2
	}

	promoted := 0
	for _, c := range res.Cases {
		if *caseID != "" && c.ID != *caseID {
			continue
		}
		for _, a := range c.Validation.Assert {
			if *assertID != "" && a.ID != *assertID {
				continue
			}
			if !*all && a.Status != result.StatusPassed {
				continue
			}
			if a.Actual.Image == "" {
				continue
			}
			src := filepath.Join(dir, filepath.FromSlash(a.Actual.Image))
			dst := filepath.Join(*baselines, c.ID, a.ID+".png")
			if err := copyFile(src, dst); err != nil {
				fmt.Fprintf(os.Stderr, "  ✗ %s/%s: %v\n", c.ID, a.ID, err)
				continue
			}
			fmt.Printf("  ✓ approved %s/%s → %s\n", c.ID, a.ID, dst)
			promoted++
		}
	}
	fmt.Printf("\nApproved %d baseline(s).\n", promoted)
	return 0
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
