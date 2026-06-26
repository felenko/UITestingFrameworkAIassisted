// Package runner is the shared Runner Core (docs/02): it loads a session,
// launches the app, drives it like a human, verifies via the AI engine, and
// produces results consumed by the reporter. Both front-ends use it.
package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/felenko/uitest/internal/core/ai"
	"github.com/felenko/uitest/internal/core/event"
	"github.com/felenko/uitest/internal/core/locator"
	"github.com/felenko/uitest/internal/core/platform"
	"github.com/felenko/uitest/internal/core/result"
	"github.com/felenko/uitest/internal/core/session"
	"github.com/felenko/uitest/internal/core/vars"
)

// StepVerdict controls what the runner does with a step when BeforeEachStep is set.
type StepVerdict int

const (
	VerdictRun  StepVerdict = iota // execute the step normally
	VerdictSkip                    // skip the step; counts as passed without running
)

// StepHookEvent is the payload delivered to BeforeEachStep before a step runs.
type StepHookEvent struct {
	CaseID    string
	Phase     string
	StepIndex int
	Human     string
	Machine   []session.Command // read-only view of the step's machine commands
}

// CommandHookEvent is the payload delivered to BeforeEachCommand before each
// individual machine command within a step.
type CommandHookEvent struct {
	CaseID    string
	Phase     string
	StepIndex int
	CmdIndex  int             // 0-based index of this command within the step
	TotalCmds int             // total number of commands in this step
	Human     string          // parent step's human label
	Cmd       session.Command // this command (value copy)
	AllCmds   []session.Command
}

// Options configures a run (CLI flags / GUI settings resolve into these).
type Options struct {
	OutDir        string   // final output dir; if empty, derived from session + timestamp
	Provider      string   // AI provider override
	Filter        string   // glob over case id/tags
	IDs           []string // explicit set of case ids to run (takes precedence over Filter)
	FailFast      *bool   // override session failFast
	DryRun        bool    // validate + plan only
	Headed        bool    // interactive desktop expected
	TimeoutScale  float64 // multiply all timeouts
	NoAppLaunch   bool    // attach instead of launching
	Frontend      string  // "cli" | "gui"
	RunnerVersion string

	// BeforeEachCase, if non-nil, is called between cases. Return a non-nil error to abort the run.
	BeforeEachCase func(ctx context.Context) error

	// AfterEachCase, if non-nil, is called after each case completes with a
	// snapshot of results so far (cases completed so far, summary computed).
	// Used by the GUI to flush a live report after each case.
	AfterEachCase func(partial *result.Results)

	// BeforeEachStep, if non-nil, is called before each step executes.
	// The returned StepVerdict controls what the runner does with the step.
	// It may block (step-level debugger pause).
	BeforeEachStep func(ctx context.Context, ev StepHookEvent) StepVerdict

	// BeforeEachCommand, if non-nil, is called before each individual machine
	// command within every step. Return VerdictSkip to skip just this command;
	// VerdictRun to execute it normally. It may block (command-level pause).
	BeforeEachCommand func(ctx context.Context, ev CommandHookEvent) StepVerdict

	// OnCaseSteps, if non-nil, is called before the steps phase of each case
	// with the complete ordered step list. Used by the GUI debug mode to
	// pre-populate the multi-step command view.
	OnCaseSteps func(caseID string, steps []session.Step)

	// StepRestartRequested, if non-nil, is polled after each runStepOnce call.
	// Return true (once, consumed) to re-run the same step from the top;
	// BeforeEachCommand handles per-command skipping to reach the restart target.
	StepRestartRequested func(caseID string, stepIdx int) bool
}

// Runner executes one session.
type Runner struct {
	sess   *session.Session
	opts   Options
	bus    *event.Bus
	drv    platform.Driver
	engine *ai.Engine
	bag    *vars.Bag

	outDir      string
	shotsDir    string
	baselineDir string // session-relative approved baselines
	logFile     *os.File

	app           *appProcess
	appPID        uint32 // PID of the process we launched (may be a launcher/shim)
	uiPID         uint32 // PID that actually owns the app's UI window(s), once known
	currentWindow platform.Window
	inputWatcher  platform.InputWatcher
	topmostForced map[uintptr]forcedWindow // windows we forced topmost, keyed by handle

	provider      string
	model         string
	usedCandidate bool // any assert fell back to a candidate baseline

	locators        *locator.Store // self-healing find: selectors (next to the session file)
	unapprovedFinds int            // find: resolutions that used unapproved candidates
}

// New creates a runner. bus may be nil (a no-op bus is created).
func New(sess *session.Session, opts Options, bus *event.Bus) *Runner {
	if bus == nil {
		bus = event.New()
	}
	if opts.TimeoutScale <= 0 {
		opts.TimeoutScale = 1.0
	}
	if opts.Frontend == "" {
		opts.Frontend = "cli"
	}
	provider := sess.Session.AI.Provider
	if opts.Provider != "" {
		provider = opts.Provider
	}
	return &Runner{
		sess:     sess,
		opts:     opts,
		bus:      bus,
		drv:      platform.New(),
		bag:      vars.New(sess.Variables),
		provider: provider,
		model:    sess.Session.AI.Model,
	}
}

// scale applies the timeout-scale factor to a duration.
func (r *Runner) scale(d time.Duration) time.Duration {
	return time.Duration(float64(d) * r.opts.TimeoutScale)
}

func (r *Runner) logf(level, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if r.logFile != nil {
		fmt.Fprintf(r.logFile, "%s [%s] %s\n", time.Now().Format(time.RFC3339), level, msg)
	}
	r.bus.Log(level, msg)
}

// Run executes the session and returns the results plus a process exit code
// (docs/02 §1 exit codes).
func (r *Runner) Run(ctx context.Context) (res *result.Results, code int) {
	defer func() {
		if r.logFile != nil {
			r.logFile.Close()
		}
	}()

	results := &result.Results{
		Session:       r.sess.Session.Name,
		Frontend:      r.opts.Frontend,
		RunnerVersion: r.opts.RunnerVersion,
		Environment:   r.environment(),
		Provider:      r.provider,
		Model:         r.model,
		Application:   r.sess.Session.Application.Path,
		StartedAt:     time.Now(),
	}

	// A panic anywhere in the run must still yield partial results and a
	// run.finished event, so the front-end can write a report and the UI is
	// not left hanging on a vanished run.
	defer func() {
		if rec := recover(); rec != nil {
			r.logf("error", "run aborted by internal panic: %v\n%s", rec, debug.Stack())
			results.FinishedAt = time.Now()
			res = r.finishRun(results, exitFailed)
			code = exitFailed
		}
	}()

	cases := r.selectCases()

	_ = r.drv.SetDPIAware()
	r.bus.Publish(event.Event{Type: event.RunStarted, Session: r.sess.Session.Name, Total: len(cases)})

	if len(cases) == 0 {
		r.logf("warn", "no test cases matched the selection or filter")
		results.FinishedAt = time.Now()
		return r.finishRun(results, exitOK), exitOK
	}

	if err := r.setupOutput(); err != nil {
		r.logf("error", "preparing output: %v", err)
		results.FinishedAt = time.Now()
		code := exitSetup(err)
		return r.finishRun(results, code), code
	}
	r.bag.Set("session.outDir", r.outDir)
	r.bag.Set("timestamp", time.Now().Format("20060102-150405"))
	r.loadLocators()

	if r.opts.DryRun {
		r.logf("info", "dry-run: %d case(s) would execute; not launching app", len(cases))
		results.FinishedAt = time.Now()
		results.Summary.Total = len(cases)
		results.Summary.Skipped = len(cases)
		return r.finishRun(results, 0), 0
	}

	// Doctor: provider + capture reachable.
	if code, err := r.doctor(); err != nil {
		r.logf("error", "environment check failed: %v", err)
		results.FinishedAt = time.Now()
		return r.finishRun(results, code), code
	}

	// Launch app (skip for monitor/utility sessions that have no application path).
	if !r.opts.NoAppLaunch && r.sess.Session.Application.Path != "" {
		if err := r.launchApp(ctx); err != nil {
			r.logf("error", "application failed to launch: %v", err)
			results.FinishedAt = time.Now()
			code := exitSetup(err)
			return r.finishRun(results, code), code
		}
	} else {
		r.attachMainWindow()
	}

	// Start watching for real user input so actions can detect intervention.
	if r.focusGuard() {
		if iw, err := r.drv.WatchInput(); err != nil {
			r.logf("warn", "focus guard: input watcher unavailable: %v", err)
		} else {
			r.inputWatcher = iw
			defer func() {
				r.inputWatcher.Stop()
				r.inputWatcher = nil
			}()
		}
	}

	// One-time session bootstrap, after the app is ready and before any case.
	r.runSessionHook(ctx, "session", "setup", r.sess.Session.Setup)

	aborted := false
	for i := range cases {
		select {
		case <-ctx.Done():
			aborted = true
		default:
		}
		if aborted {
			break
		}
		if r.opts.BeforeEachCase != nil {
			if err := r.opts.BeforeEachCase(ctx); err != nil {
				aborted = true
				break
			}
		}
		cr := r.runCase(ctx, &cases[i])
		results.Cases = append(results.Cases, cr)
		if r.opts.AfterEachCase != nil {
			snap := *results
			snap.Cases = make([]result.Case, len(results.Cases))
			copy(snap.Cases, results.Cases)
			snap.FinishedAt = time.Now()
			r.computeSummary(&snap)
			r.opts.AfterEachCase(&snap)
		}
		if cr.Status == result.StatusFailed || cr.Status == result.StatusError {
			if r.failFast(&cases[i]) {
				r.logf("warn", "failFast: stopping after %s", cr.ID)
				break
			}
		}
	}

	if aborted {
		r.logf("info", "run cancelled — leaving the application open")
	} else {
		r.shutdownApp()
	}
	r.finishLocators()

	results.FinishedAt = time.Now()
	if aborted {
		return r.finishRun(results, exitAborted), exitAborted
	}
	return r.finishRun(results, exitFor(results)), exitFor(results)
}

// finishRun computes the summary, publishes run.finished for the GUI/CLI, and
// returns the results struct. Always call on every exit path so the UI is not
// left with cases stuck in "pending".
func (r *Runner) finishRun(results *result.Results, code int) *result.Results {
	r.computeSummary(results)
	status := "failed"
	switch code {
	case exitOK:
		status = string(overallStatus(results))
	case exitAborted:
		status = "cancelled"
	}
	r.bus.Publish(event.Event{
		Type:    event.RunFinished,
		Status:  status,
		Total:   results.Summary.Total,
	})
	return results
}

func (r *Runner) environment() result.Environment {
	b := r.drv.ScreenBounds()
	return result.Environment{
		OS:     runtime.GOOS,
		Screen: fmt.Sprintf("%dx%d@%.2gdpi", b.Width, b.Height, r.drv.DPIScale()),
	}
}

func (r *Runner) setupOutput() error {
	out := r.opts.OutDir
	if out == "" {
		base := r.sess.Session.Settings.OutDir
		if base == "" {
			base = session.DefaultOutDir
		}
		out = filepath.Join(base, time.Now().Format("20060102-150405"))
	}
	abs, err := filepath.Abs(out)
	if err == nil {
		out = abs
	}
	r.outDir = out
	r.shotsDir = filepath.Join(out, "screenshots")
	r.baselineDir = filepath.Join(filepath.Dir(r.sess.SourcePath), "baselines")
	for _, d := range []string{r.outDir, r.shotsDir, filepath.Join(r.outDir, "assets")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	f, err := os.Create(filepath.Join(r.outDir, "run.log"))
	if err != nil {
		return err
	}
	r.logFile = f
	r.engine = ai.New(ai.Config{
		Provider: r.provider,
		Model:    r.model,
		Timeout:  r.scale(r.sess.Session.AI.Timeout.Or(session.DefaultAITimeout)),
		Retries:  r.sess.Session.AI.Retries,
		Samples:  r.sess.Session.AI.Samples,
	}, func(level, msg string) { r.logf(level, "%s", msg) })
	r.logf("info", "output directory: %s", r.outDir)
	return nil
}

// OutDir exposes the resolved output directory (set after setupOutput).
func (r *Runner) OutDir() string { return r.outDir }

// Driver returns the platform driver used by this runner. Callers (e.g. the
// GUI debug controller) may call platform-level operations while the runner is
// paused at a step without creating a separate driver instance.
func (r *Runner) Driver() platform.Driver { return r.drv }

func (r *Runner) computeSummary(res *result.Results) {
	res.UnverifiedBaselines = r.usedCandidate
	res.Summary.Total = len(res.Cases)
	for _, c := range res.Cases {
		switch c.Status {
		case result.StatusPassed:
			res.Summary.Passed++
		case result.StatusFailed:
			res.Summary.Failed++
		case result.StatusError:
			res.Summary.Errors++
		case result.StatusSkipped:
			res.Summary.Skipped++
		}
	}
}

func overallStatus(res *result.Results) result.Status {
	if res.Summary.Failed > 0 || res.Summary.Errors > 0 {
		return result.StatusFailed
	}
	return result.StatusPassed
}

const (
	exitOK      = 0
	exitFailed  = 1
	exitSetupC  = 2
	exitAborted = 3
)

func exitSetup(error) int { return exitSetupC }

func exitFor(res *result.Results) int {
	if res.Summary.Failed > 0 || res.Summary.Errors > 0 {
		return exitFailed
	}
	return exitOK
}

func (r *Runner) failFast(tc *session.TestCase) bool {
	if tc.FailFast != nil {
		return *tc.FailFast
	}
	if r.opts.FailFast != nil {
		return *r.opts.FailFast
	}
	return r.sess.Session.Settings.FailFast
}
