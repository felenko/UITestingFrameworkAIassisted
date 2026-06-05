// Command uitest is the CLI front-end over the shared Runner Core (docs/02 §1).
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/felenko/uitest/internal/core/event"
	"github.com/felenko/uitest/internal/core/report"
	"github.com/felenko/uitest/internal/core/runner"
	"github.com/felenko/uitest/internal/core/session"
)

// Version is the runner version stamped into reports.
const Version = "0.2.0"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "run":
		os.Exit(cmdRun(args))
	case "validate":
		os.Exit(cmdValidate(args))
	case "list":
		os.Exit(cmdList(args))
	case "doctor":
		os.Exit(cmdDoctor(args))
	case "approve":
		os.Exit(cmdApprove(args))
	case "schema":
		os.Exit(cmdSchema(args))
	case "-h", "--help", "help":
		usage()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Print(`uitest — AI-assisted UI test runner

Usage:
  uitest run <session.yaml> [options]
  uitest validate <session.yaml>
  uitest list <session.yaml>
  uitest doctor
  uitest approve <results-dir> [--case ID] [--assert ID] [--all] [--baselines DIR]
  uitest schema [-o <file>]   Emit the TestSession.yaml JSON Schema (for editors)

Run options:
  --out <dir>            Output dir (default ./test-results/<timestamp>)
  --ai <provider>        claude | codex | cursor
  --filter <glob>        Run only cases whose id/tags match
  --fail-fast            Stop on first failed case
  --dry-run              Validate + print plan; don't launch or act
  --headed / --headless  Whether an interactive desktop is expected (default headed)
  --timeout-scale <f>    Multiply all timeouts (default 1.0)
  --log-level <level>    error|warn|info|debug|trace (default info)
  --no-app-launch        Attach to an already-running app
  --report-embed         Inline CSS/JS/screenshots into a single report.html
  --open                 Open report.html when finished

Exit codes: 0 all passed · 1 some failed · 2 setup error · 3 aborted
`)
}

func cmdRun(args []string) int {
	fs := newFlagSet("run")
	out := fs.String("out", "", "output dir")
	ai := fs.String("ai", "", "AI provider override")
	filter := fs.String("filter", "", "case id/tag glob")
	failFast := fs.Bool("fail-fast", false, "stop on first failure")
	dryRun := fs.Bool("dry-run", false, "validate + plan only")
	headed := fs.Bool("headed", true, "interactive desktop expected")
	headless := fs.Bool("headless", false, "no interactive desktop")
	timeoutScale := fs.Float64("timeout-scale", 1.0, "timeout multiplier")
	logLevel := fs.String("log-level", "info", "log level")
	noLaunch := fs.Bool("no-app-launch", false, "attach to running app")
	embed := fs.Bool("report-embed", false, "self-contained report.html")
	open := fs.Bool("open", false, "open report when finished")

	path, ok := parseWithPath(fs, args, "run")
	if !ok {
		return 2
	}

	sess, err := session.Load(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 2
	}

	opts := runner.Options{
		OutDir:        *out,
		Provider:      *ai,
		Filter:        *filter,
		DryRun:        *dryRun,
		Headed:        *headed && !*headless,
		TimeoutScale:  *timeoutScale,
		NoAppLaunch:   *noLaunch,
		Frontend:      "cli",
		RunnerVersion: Version,
	}
	if flagSet(fs, "fail-fast") {
		opts.FailFast = failFast
	}

	bus := event.New()
	bus.Subscribe(newConsole(*logLevel).handle)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	r := runner.New(sess, opts, bus)
	results, code := r.Run(ctx)

	if results != nil && !*dryRun {
		reportPath, err := report.WriteAll(r.OutDir(), results, *embed)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error writing report: %v\n", err)
			if code == 0 {
				code = 2
			}
		} else {
			fmt.Printf("\n%s", report.Summarize(results))
			fmt.Printf("\nReport: %s\n", reportPath)
			if *open {
				openInBrowser(reportPath)
			}
		}
	}
	return code
}
