package runner

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/felenko/uitest/internal/core/event"
	"github.com/felenko/uitest/internal/core/result"
	"github.com/felenko/uitest/internal/core/session"
)

// runCase executes one test case (setup → steps → validation → teardown → cleanup).
// When session.settings.recoverOnCaseFailure is true, a failed case triggers
// one kill → relaunch → recoverSteps → beforeEach cycle and a single retry
// before the failure is recorded. Otherwise case-level retries apply.
func (r *Runner) runCase(ctx context.Context, tc *session.TestCase) result.Case {
	cr := r.runCaseOnce(ctx, tc)
	if cr.Status == result.StatusPassed {
		return cr
	}
	if r.recoverOnCaseFailure() && !r.opts.NoAppLaunch && ctx.Err() == nil {
		r.logf("warn", "case %s failed (%s) — kill → relaunch → recover → retry once", tc.ID, cr.Status)
		if err := r.restartAndRecover(ctx, tc.ID); err != nil {
			r.logf("error", "session recovery failed: %v", err)
			return cr
		}
		return r.runCaseOnce(ctx, tc)
	}
	for attempt := 0; attempt < tc.Retries; attempt++ {
		r.logf("warn", "retrying case %s (attempt %d/%d)", tc.ID, attempt+2, tc.Retries+1)
		cr = r.runCaseOnce(ctx, tc)
		if cr.Status == result.StatusPassed {
			break
		}
	}
	return cr
}

// runRecoverSteps executes session.recoverSteps strictly after a relaunch.
// Failures abort recovery so the original case failure is kept.
func (r *Runner) runRecoverSteps(ctx context.Context, caseID string) error {
	steps := r.sess.Session.RecoverSteps
	if len(steps) == 0 {
		return nil
	}
	r.logf("info", "running session recoverSteps (%d step(s))", len(steps))
	cr := &result.Case{Status: result.StatusPassed}
	r.runPhase(ctx, caseID, "recover", steps, cr)
	if cr.Status == result.StatusPassed {
		return nil
	}
	return fmt.Errorf("recoverSteps finished with status %s", cr.Status)
}

func (r *Runner) runCaseOnce(ctx context.Context, tc *session.TestCase) (cr result.Case) {
	start := time.Now()

	// A panic inside a case (driver, capture, AI client, …) is recorded as an
	// errored case instead of crashing the whole runner, so the run continues
	// and every prior result still reaches the report.
	defer func() {
		if rec := recover(); rec != nil {
			r.logf("error", "case %s panicked: %v\n%s", tc.ID, rec, debug.Stack())
			cr.ID = tc.ID
			cr.Name = tc.Name
			cr.Status = result.StatusError
			cr.Error = fmt.Sprintf("internal error (panic): %v", rec)
			cr.DurationMs = time.Since(start).Milliseconds()
			r.bus.Publish(event.Event{
				Type: event.CaseFinished, CaseID: tc.ID, Status: string(cr.Status), DurationMs: cr.DurationMs,
			})
		}
	}()

	r.bag.Set("case.id", tc.ID)
	r.bus.Publish(event.Event{Type: event.CaseStarted, CaseID: tc.ID, CaseName: tc.Name})
	r.logf("info", "case %s: %s", tc.ID, tc.Name)

	cr = result.Case{
		ID:          tc.ID,
		Name:        tc.Name,
		Description: tc.Description,
		Tags:        tc.Tags,
		Status:      result.StatusPassed,
	}

	// Session-level bootstrap before every case (best-effort; never changes the
	// verdict). Typically pins the main window's geometry so window-relative
	// coordinates stay valid; no-ops cleanly when the target window is absent.
	r.runSessionHook(ctx, tc.ID, "beforeEach", r.sess.Session.BeforeEach)
	defer r.runSessionHook(ctx, tc.ID, "afterEach", r.sess.Session.AfterEach)

	// Inform the debugger about the case's own steps before any phase runs so
	// breakpoints can be set on them even when the runner is paused at setup.
	if r.opts.OnCaseSteps != nil {
		r.opts.OnCaseSteps(tc.ID, tc.Steps)
	}

	// Setup + act phases.
	cr.Setup = r.runPhase(ctx, tc.ID, "setup", tc.Setup, &cr)
	actOK := cr.Status == result.StatusPassed
	if actOK {
		cr.Steps = r.runPhase(ctx, tc.ID, "steps", tc.Steps, &cr)
	} else {
		cr.Steps = skippedSteps(tc.Steps, "steps")
	}

	// Check phase (validation) — only if acting succeeded.
	if cr.Status == result.StatusPassed {
		cr.Validation = r.runValidation(ctx, tc)
		if cr.Validation.Status != result.StatusPassed {
			cr.Status = cr.Validation.Status
		}
	} else {
		cr.Validation = result.Validation{Human: tc.Validation.Human, Status: result.StatusSkipped}
		for i := range tc.Validation.Assert {
			cr.Validation.Assert = append(cr.Validation.Assert, r.skippedAssert(&tc.Validation.Assert[i], i))
		}
	}

	// Teardown and cleanup always run; their failures don't change the case verdict.
	cr.Teardown = r.runPhase(ctx, tc.ID, "teardown", tc.Teardown, nil)
	cr.Cleanup = r.runPhase(ctx, tc.ID, "cleanup", tc.Cleanup, nil)

	cr.DurationMs = time.Since(start).Milliseconds()
	r.bus.Publish(event.Event{
		Type: event.CaseFinished, CaseID: tc.ID, Status: string(cr.Status), DurationMs: cr.DurationMs,
	})
	r.logf("info", "case %s finished: %s (%dms)", tc.ID, cr.Status, cr.DurationMs)
	return cr
}

// runSessionHook executes session-level lifecycle steps (setup / beforeEach /
// afterEach) on a BEST-EFFORT basis: every step is attempted and failures are
// logged as warnings but never abort the hook or change any case verdict. These
// steps bootstrap shared state (e.g. pinning the main window's geometry so
// window-relative coordinates stay valid) and may legitimately no-op — for
// example before login, when the main shell window does not exist yet.
func (r *Runner) runSessionHook(ctx context.Context, caseID, name string, steps []session.Step) {
	if len(steps) == 0 {
		return
	}
	phase := "session-" + name
	for i := range steps {
		select {
		case <-ctx.Done():
			return
		default:
		}
		sr := r.runStepOnce(ctx, caseID, phase, i, &steps[i])
		if sr.Status == result.StatusFailed || sr.Status == result.StatusError {
			r.logf("warn", "  %s step[%d] %q did not apply (best-effort): %s",
				name, i, steps[i].Human, sr.Error)
		}
	}
}

// runPhase executes a list of steps. If cr is non-nil, a failed/errored step
// updates the case status (teardown passes nil so it can't change the verdict).
func (r *Runner) runPhase(ctx context.Context, caseID, phase string, steps []session.Step, cr *result.Case) []result.Step {
	var out []result.Step
	for i := range steps {
		sr := r.runStep(ctx, caseID, phase, i, &steps[i])
		out = append(out, sr)
		if sr.Status == result.StatusFailed || sr.Status == result.StatusError {
			// Optional steps (e.g. "log in if still at Please Login") use
			// continueOnFailure: their failure must not poison the case verdict.
			if cr != nil && !steps[i].ContinueOnFailure {
				cr.Status = sr.Status
			}
			if !steps[i].ContinueOnFailure {
				// Mark the rest of this phase skipped.
				for j := i + 1; j < len(steps); j++ {
					out = append(out, skippedStep(&steps[j], phase, j))
				}
				break
			}
		}
	}
	return out
}

// runStep executes one step's machine commands with step-level retries.
func (r *Runner) runStep(ctx context.Context, caseID, phase string, index int, step *session.Step) result.Step {
	r.bag.Set("step.index", itoa(index))

	if r.opts.BeforeEachStep != nil {
		ev := StepHookEvent{CaseID: caseID, Phase: phase, StepIndex: index, Human: step.Human, Machine: step.Machine}
		if r.opts.BeforeEachStep(ctx, ev) == VerdictSkip {
			return skippedStep(step, phase, index)
		}
	}

	attempts := step.Retries + 1
	var sr result.Step
	for attempt := 0; attempt < attempts; attempt++ {
		// Inner loop: re-run the same step when a debug backward-jump requests it.
		// StepRestartRequested is a consumed one-shot flag set by the jump handler.
		for {
			sr = r.runStepOnce(ctx, caseID, phase, index, step)
			if r.opts.StepRestartRequested == nil {
				break
			}
			if !r.opts.StepRestartRequested(caseID, index) {
				break
			}
		}
		if sr.Status == result.StatusPassed {
			break
		}
		if attempt < attempts-1 {
			r.logf("warn", "retrying step %s[%d] (attempt %d/%d)", phase, index, attempt+2, attempts)
		}
	}
	if (sr.Status == result.StatusFailed || sr.Status == result.StatusError) && step.ContinueOnFailure {
		r.logf("warn", "step %s[%d] failed but continueOnFailure is set", phase, index)
	}
	return sr
}

func (r *Runner) runStepOnce(ctx context.Context, caseID, phase string, index int, step *session.Step) result.Step {
	start := time.Now()
	sr := result.Step{Index: index, Phase: phase, Human: step.Human, Status: result.StatusPassed}
	r.bus.Publish(event.Event{
		Type: event.StepStarted, CaseID: caseID, StepIndex: index, Phase: phase,
		Human: step.Human, MachineDesc: describeMachine(step.Machine),
	})
	r.logf("info", "  step[%d] %s", index, step.Human)

	for i := range step.Machine {
		cmd := &step.Machine[i]

		if r.opts.BeforeEachCommand != nil {
			ev := CommandHookEvent{
				CaseID: caseID, Phase: phase, StepIndex: index,
				CmdIndex: i, TotalCmds: len(step.Machine),
				Human: step.Human, Cmd: *cmd, AllCmds: step.Machine,
			}
			if r.opts.BeforeEachCommand(ctx, ev) == VerdictSkip {
				sr.Machine = append(sr.Machine, result.Machine{
					Action:  cmd.Action,
					Summary: describeCommand(cmd),
					Status:  result.StatusSkipped,
				})
				continue
			}
		}

		mStart := time.Now()
		mr := result.Machine{Action: cmd.Action, Summary: describeCommand(cmd)}
		attempts, diag, err := r.runCommand(ctx, cmd, &sr, caseID)
		mr.DurationMs = time.Since(mStart).Milliseconds()
		mr.Attempts = attempts
		mr.Diagnosis = diag
		if err != nil {
			mr.Status = result.StatusError
			mr.Error = err.Error()
			sr.Machine = append(sr.Machine, mr)
			sr.Status = result.StatusError
			sr.Error = err.Error()
			r.logf("error", "  step[%d] command %s failed: %v", index, cmd.Action, err)
			r.maybeShotOnFailure(caseID, &sr)
			break
		}
		mr.Status = result.StatusPassed
		if attempts > 1 {
			r.logf("info", "  step[%d] %s succeeded on attempt %d", index, cmd.Action, attempts)
		}
		sr.Machine = append(sr.Machine, mr)
	}

	sr.DurationMs = time.Since(start).Milliseconds()
	r.bus.Publish(event.Event{
		Type: event.StepFinished, CaseID: caseID, StepIndex: index, Phase: phase,
		Status: string(sr.Status), DurationMs: sr.DurationMs,
	})
	return sr
}

func (r *Runner) maybeShotOnFailure(caseID string, sr *result.Step) {
	if !r.sess.Session.Settings.ScreenshotOnFailure {
		return
	}
	name := caseID + "_step-" + itoa(sr.Index) + "_failure.png"
	if rel, err := r.captureAndSave(nil, name); err == nil {
		sr.Screenshots = append(sr.Screenshots, rel)
		r.bus.Publish(event.Event{Type: event.ScreenshotCaptured, CaseID: caseID, Path: rel, Which: "step"})
	}
}

func skippedSteps(steps []session.Step, phase string) []result.Step {
	var out []result.Step
	for i := range steps {
		out = append(out, skippedStep(&steps[i], phase, i))
	}
	return out
}

func skippedStep(step *session.Step, phase string, index int) result.Step {
	return result.Step{Index: index, Phase: phase, Human: step.Human, Status: result.StatusSkipped}
}
