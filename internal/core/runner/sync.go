package runner

import (
	"context"
	"fmt"
	"image"
	"strings"
	"time"

	"github.com/felenko/uitest/internal/core/ai"
	"github.com/felenko/uitest/internal/core/platform"
	"github.com/felenko/uitest/internal/core/result"
	"github.com/felenko/uitest/internal/core/session"
)

// Pixel-diff tolerances (fraction of sampled pixels that must differ).
const (
	changedTol = 0.005 // >0.5% of pixels differ => "something happened"
	stableTol  = 0.002 // <0.2% differ between frames => "settled"
	pxDelta    = 24    // per-channel-sum delta counted as a changed pixel
)

// isActuation reports whether an action physically drives the app (and thus
// benefits from settle / verify / retry). Observation and wait commands run
// directly.
func isActuation(action string) bool {
	switch action {
	case "mouse_move", "mouse_click", "mouse_down", "mouse_up", "mouse_drag", "mouse_scroll",
		"type_text", "key_press", "key_down", "key_up",
		"focus_window", "close_window", "move_window", "resize_window":
		return true
	}
	return false
}

func (r *Runner) autoSettle() bool {
	s := r.sess.Session.Settings.AutoSettle
	return s != nil && *s
}

func (r *Runner) aiEscalationOn() bool {
	s := r.sess.Session.Settings.AIEscalation
	return s != nil && *s
}

func (r *Runner) actionRetries(cmd *session.Command) int {
	if cmd.ActionRetries != nil {
		return *cmd.ActionRetries
	}
	return r.sess.Session.Settings.DefaultActionRetries
}

func (r *Runner) settleTimeout() time.Duration {
	return r.scale(r.sess.Session.Settings.SettleTimeout.Or(session.DefaultSettleTimeout))
}

func (r *Runner) settleInterval() time.Duration {
	return r.scale(r.sess.Session.Settings.SettleInterval.Or(session.DefaultSettleInterval))
}

// runCommand wraps a machine command with the cost-ordered closed loop:
// waitBefore (ready) -> locate -> act -> verify (+retry) -> AI diagnosis on
// exhaustion. Non-actuation commands run directly. It returns the number of
// action attempts and any AI diagnosis for the result record.
func (r *Runner) runCommand(ctx context.Context, cmd *session.Command, sr *result.Step, caseID string) (attempts int, diagnosis string, err error) {
	if !isActuation(cmd.Action) {
		return 1, "", r.executeCommand(ctx, cmd, sr, caseID)
	}
	// Phase 1: cheaper locators only. UIA/find are accepted by the schema but
	// not executable yet.
	if cmd.UIA != nil && !cmd.UIA.IsZero() {
		return 0, "", fmt.Errorf("uia targeting is not yet available (Phase 2)")
	}
	if cmd.Find != "" {
		return 0, "", fmt.Errorf("find (AI element location) is not yet available (Phase 3)")
	}

	settle := r.autoSettle()

	// 1) Precondition: wait until the target is ready.
	if cmd.WaitBefore != nil {
		to := r.scale(cmd.WaitBefore.Timeout.Or(r.settleTimeout()))
		ok, werr := r.pollCondition(ctx, cmd.WaitBefore, cmd.Target, nil, time.Now().Add(to))
		if werr != nil {
			return 0, "", werr
		}
		if !ok {
			return 0, "", fmt.Errorf("waitBefore not satisfied: target never became ready")
		}
	} else if settle {
		r.waitStable(ctx, cmd.Target, r.settleTimeout())
	}

	// 2) Decide the verification (explicit, or an inferred cheap default).
	verify := cmd.Verify
	if verify == nil && settle {
		verify = defaultVerify(cmd)
	}

	retries := 0
	if verify != nil {
		retries = r.actionRetries(cmd)
	}

	// 3) Act, then verify; retry just this action when verification fails.
	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			r.logf("warn", "  action %s did not take; re-attempt %d/%d", cmd.Action, attempt, retries)
			r.sleep(ctx, backoff(attempt))
		}
		attempts++

		var before image.Image
		if verify != nil && verify.Changed {
			before, _ = r.captureContext(condTarget(verify, cmd.Target))
		}

		if aerr := r.executeCommand(ctx, cmd, sr, caseID); aerr != nil {
			lastErr = aerr
			continue
		}

		if verify == nil {
			if settle {
				r.waitStable(ctx, cmd.Target, r.settleTimeout())
			}
			return attempts, "", nil
		}

		to := r.scale(verify.Timeout.Or(r.settleTimeout()))
		ok, verr := r.pollCondition(ctx, verify, cmd.Target, before, time.Now().Add(to))
		if verr != nil {
			lastErr = verr
			continue
		}
		if ok {
			return attempts, "", nil
		}
		lastErr = fmt.Errorf("verify not satisfied after %s", cmd.Action)
	}

	// 4) Exhausted cheap retries -> escalate to the AI to diagnose.
	if r.aiEscalationOn() {
		diagnosis = r.aiDiagnose(ctx, cmd.Target)
		if diagnosis != "" {
			r.logf("warn", "  AI diagnosis: %s", diagnosis)
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("action %s did not take effect", cmd.Action)
	}
	return attempts, diagnosis, lastErr
}

// defaultVerify infers a cheap, safe post-condition for actions where retrying
// is sound. Only the canonical "missed click / drag" cases auto-retry; typing
// and key chords are NOT auto-retried (re-sending would duplicate input).
func defaultVerify(cmd *session.Command) *session.Condition {
	switch cmd.Action {
	case "mouse_click", "mouse_drag", "mouse_scroll":
		return &session.Condition{Changed: true}
	default:
		return nil
	}
}

// condTarget returns the condition's own target or the action's target.
func condTarget(cond *session.Condition, fallback *session.Target) *session.Target {
	if cond != nil && cond.Target != nil && !cond.Target.IsZero() {
		return cond.Target
	}
	return fallback
}

// pollCondition waits until every present rung of a condition holds, or the
// deadline passes. Stability is handled first (it spans time); the remaining
// point-in-time rungs (window/changed/uia/question) are then polled.
func (r *Runner) pollCondition(ctx context.Context, cond *session.Condition, target *session.Target, before image.Image, deadline time.Time) (bool, error) {
	if cond == nil || cond.IsZero() {
		return true, nil
	}
	if cond.Stable {
		r.waitStable(ctx, condTarget(cond, target), time.Until(deadline))
	}
	poll := r.scale(cond.PollEvery.Or(r.settleInterval()))
	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		default:
		}
		ok, err := r.pointRungs(ctx, cond, target, before)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
		if !time.Now().Before(deadline) {
			return false, nil
		}
		r.sleep(ctx, poll)
	}
}

// pointRungs evaluates the non-temporal rungs (all must hold).
func (r *Runner) pointRungs(ctx context.Context, cond *session.Condition, target *session.Target, before image.Image) (bool, error) {
	if cond.Window != nil {
		found := r.windowExists(cond.Window)
		if found != !cond.Window.Gone {
			return false, nil
		}
	}
	if cond.UIA != nil {
		return false, fmt.Errorf("uia conditions are not yet available (Phase 2)")
	}
	if cond.Changed && before != nil {
		now, err := r.captureContext(condTarget(cond, target))
		if err != nil {
			return false, err
		}
		if !pixelChanged(before, now, changedTol) {
			return false, nil
		}
	}
	if cond.Question != "" {
		ok, err := r.askAI(ctx, cond, target)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

// windowExists reports whether a window matching the condition is present.
func (r *Runner) windowExists(wm *session.WindowMatch) bool {
	q := platform.WindowQuery{
		Title:    r.bag.Expand(wm.Title),
		Process:  r.bag.Expand(wm.Process),
		Class:    r.bag.Expand(wm.Class),
		Strategy: r.sess.Session.Settings.WindowMatch,
		PID:      r.appPID,
	}
	if wm.Process != "" {
		q.Strategy = "process"
	} else if wm.Class != "" {
		q.Strategy = "class"
	}
	_, err := r.drv.FindWindow(q)
	return err == nil
}

// askAI evaluates a condition's AI rung against a fresh capture.
func (r *Runner) askAI(ctx context.Context, cond *session.Condition, target *session.Target) (bool, error) {
	img, err := r.captureContext(condTarget(cond, target))
	if err != nil {
		return false, nil
	}
	rel, err := r.savePNG(img, fmt.Sprintf("cond-%d.png", time.Now().UnixNano()))
	if err != nil {
		return false, nil
	}
	out := r.engine.AssertAI(ctx, ai.Request{
		Question:  r.bag.Expand(cond.Question),
		ImagePath: r.absPath(rel),
		Expect:    cond.Expect,
	})
	if out.Err != nil {
		r.logf("debug", "condition AI check error: %v", out.Err)
		if strings.Contains(out.Err.Error(), "Authentication required") {
			return false, out.Err
		}
		return false, nil
	}
	return out.Pass, nil
}

// aiDiagnose asks the AI to describe what is on screen when an action fails.
func (r *Runner) aiDiagnose(ctx context.Context, target *session.Target) string {
	img, err := r.captureContext(target)
	if err != nil {
		return ""
	}
	rel, err := r.savePNG(img, fmt.Sprintf("diagnose-%d.png", time.Now().UnixNano()))
	if err != nil {
		return ""
	}
	value, _, err := r.engine.ReadText(ctx, ai.Request{
		Question:  "An automated UI action did not produce its expected effect. In one sentence, describe what is currently on screen and whether any unexpected dialog, popup, or error is blocking interaction.",
		ImagePath: r.absPath(rel),
	})
	if err != nil {
		return ""
	}
	return value
}

// waitStable blocks until the target region stops changing (two consecutive
// frames within tolerance) or the timeout elapses. Best-effort: capture errors
// just end the wait.
func (r *Runner) waitStable(ctx context.Context, target *session.Target, timeout time.Duration) {
	interval := r.settleInterval()
	deadline := time.Now().Add(timeout)
	prev, err := r.captureContext(target)
	if err != nil {
		return
	}
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return
		default:
		}
		r.sleep(ctx, interval)
		cur, err := r.captureContext(target)
		if err != nil {
			return
		}
		if !pixelChanged(prev, cur, stableTol) {
			return // settled
		}
		prev = cur
	}
}

// captureContext captures the condition/action target, defaulting to the
// current window (by handle, immune to title drift) and then the whole screen.
func (r *Runner) captureContext(target *session.Target) (image.Image, error) {
	if target != nil && !target.IsZero() {
		return r.capture(target)
	}
	if r.currentWindow != nil {
		if img, err := r.drv.CaptureWindow(r.currentWindow); err == nil {
			return img, nil
		}
	}
	return r.drv.CaptureScreen()
}

// sleep waits d or until the context is cancelled.
func (r *Runner) sleep(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// backoff returns an increasing delay for action re-attempts.
func backoff(attempt int) time.Duration {
	return time.Duration(attempt) * 400 * time.Millisecond
}

// pixelChanged reports whether two images differ in more than `tol` fraction of
// sampled pixels. Differing dimensions count as changed. Sampling keeps it fast.
func pixelChanged(a, b image.Image, tol float64) bool {
	if a == nil || b == nil {
		return true
	}
	ra, rb := a.Bounds(), b.Bounds()
	if ra.Dx() != rb.Dx() || ra.Dy() != rb.Dy() {
		return true
	}
	w, h := ra.Dx(), ra.Dy()
	if w == 0 || h == 0 {
		return true
	}
	step := 4 // sample every 4th pixel in each axis
	var sampled, diff int
	for y := 0; y < h; y += step {
		for x := 0; x < w; x += step {
			ar, ag, ab, _ := a.At(ra.Min.X+x, ra.Min.Y+y).RGBA()
			br, bg, bb, _ := b.At(rb.Min.X+x, rb.Min.Y+y).RGBA()
			// RGBA() returns 16-bit; shift to 8-bit before comparing.
			d := absDiff(ar>>8, br>>8) + absDiff(ag>>8, bg>>8) + absDiff(ab>>8, bb>>8)
			if d > pxDelta {
				diff++
			}
			sampled++
		}
	}
	if sampled == 0 {
		return false
	}
	return float64(diff)/float64(sampled) > tol
}

func absDiff(a, b uint32) uint32 {
	if a > b {
		return a - b
	}
	return b - a
}
