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
	"time"

	"github.com/felenko/uitest/internal/core/ai"
	"github.com/felenko/uitest/internal/core/event"
	"github.com/felenko/uitest/internal/core/platform"
	"github.com/felenko/uitest/internal/core/result"
	"github.com/felenko/uitest/internal/core/session"
	"github.com/felenko/uitest/internal/core/vars"
)

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
func (r *Runner) Run(ctx context.Context) (*result.Results, int) {
	if err := r.setupOutput(); err != nil {
		return nil, exitSetup(fmt.Errorf("preparing output: %w", err))
	}
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

	_ = r.drv.SetDPIAware()
	r.bag.Set("session.outDir", r.outDir)
	r.bag.Set("timestamp", time.Now().Format("20060102-150405"))

	cases := r.selectCases()
	r.bus.Publish(event.Event{Type: event.RunStarted, Session: r.sess.Session.Name, Total: len(cases)})

	if r.opts.DryRun {
		r.logf("info", "dry-run: %d case(s) would execute; not launching app", len(cases))
		results.FinishedAt = time.Now()
		results.Summary.Total = len(cases)
		results.Summary.Skipped = len(cases)
		return results, 0
	}

	// Doctor: provider + capture reachable.
	if code, err := r.doctor(); err != nil {
		r.logf("error", "environment check failed: %v", err)
		results.FinishedAt = time.Now()
		return results, code
	}

	// Launch app.
	if !r.opts.NoAppLaunch {
		if err := r.launchApp(ctx); err != nil {
			r.logf("error", "application failed to launch: %v", err)
			results.FinishedAt = time.Now()
			return results, exitSetup(err)
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
		cr := r.runCase(ctx, &cases[i])
		results.Cases = append(results.Cases, cr)
		if cr.Status == result.StatusFailed || cr.Status == result.StatusError {
			if r.failFast(&cases[i]) {
				r.logf("warn", "failFast: stopping after %s", cr.ID)
				break
			}
		}
	}

	r.shutdownApp()

	results.FinishedAt = time.Now()
	r.computeSummary(results)
	r.bus.Publish(event.Event{
		Type:    event.RunFinished,
		Status:  string(overallStatus(results)),
		Total:   results.Summary.Total,
	})

	if aborted {
		return results, exitAborted
	}
	return results, exitFor(results)
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
