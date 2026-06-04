package runner

import (
	"context"
	"time"

	"github.com/felenko/uitest/internal/core/event"
	"github.com/felenko/uitest/internal/core/result"
	"github.com/felenko/uitest/internal/core/session"
)

// runCase executes one test case (setup → steps → validation → teardown),
// honoring case-level retries.
func (r *Runner) runCase(ctx context.Context, tc *session.TestCase) result.Case {
	attempts := tc.Retries + 1
	var cr result.Case
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			r.logf("warn", "retrying case %s (attempt %d/%d)", tc.ID, attempt+1, attempts)
		}
		cr = r.runCaseOnce(ctx, tc)
		if cr.Status == result.StatusPassed {
			break
		}
	}
	return cr
}

func (r *Runner) runCaseOnce(ctx context.Context, tc *session.TestCase) result.Case {
	start := time.Now()
	r.bag.Set("case.id", tc.ID)
	r.bus.Publish(event.Event{Type: event.CaseStarted, CaseID: tc.ID, CaseName: tc.Name})
	r.logf("info", "case %s: %s", tc.ID, tc.Name)

	cr := result.Case{
		ID:          tc.ID,
		Name:        tc.Name,
		Description: tc.Description,
		Tags:        tc.Tags,
		Status:      result.StatusPassed,
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

	// Teardown always runs; its failures don't change the case verdict.
	cr.Teardown = r.runPhase(ctx, tc.ID, "teardown", tc.Teardown, nil)

	cr.DurationMs = time.Since(start).Milliseconds()
	r.bus.Publish(event.Event{
		Type: event.CaseFinished, CaseID: tc.ID, Status: string(cr.Status), DurationMs: cr.DurationMs,
	})
	r.logf("info", "case %s finished: %s (%dms)", tc.ID, cr.Status, cr.DurationMs)
	return cr
}

// runPhase executes a list of steps. If cr is non-nil, a failed/errored step
// updates the case status (teardown passes nil so it can't change the verdict).
func (r *Runner) runPhase(ctx context.Context, caseID, phase string, steps []session.Step, cr *result.Case) []result.Step {
	var out []result.Step
	for i := range steps {
		sr := r.runStep(ctx, caseID, phase, i, &steps[i])
		out = append(out, sr)
		if sr.Status == result.StatusFailed || sr.Status == result.StatusError {
			if cr != nil {
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
	attempts := step.Retries + 1
	var sr result.Step
	for attempt := 0; attempt < attempts; attempt++ {
		sr = r.runStepOnce(ctx, caseID, phase, index, step)
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
