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

func (r *Runner) focusGuard() bool {
	s := r.sess.Session.Settings.FocusGuard
	return s != nil && *s
}

func (r *Runner) forceTopmost() bool {
	s := r.sess.Session.Settings.ForceTopmost
	return s != nil && *s
}

func (r *Runner) forcePrimary() bool {
	s := r.sess.Session.Settings.ForcePrimaryDisplay
	return s != nil && *s
}

// ensureOnPrimary pulls the bound window back onto the primary monitor when the
// app has launched on (or drifted to) a secondary display. Window-relative
// coordinates and the primary-origin (0,0) calibration the sessions rely on
// assume the app lives on the primary monitor, so a drifted window makes input
// land on the wrong display. Idempotent; safe to call on every bind/activation.
func (r *Runner) ensureOnPrimary() {
	if !r.forcePrimary() || r.currentWindow == nil {
		return
	}
	moved, err := r.drv.EnsureOnPrimary(r.currentWindow)
	if err != nil {
		r.logf("debug", "force primary: %v", err)
		return
	}
	if moved {
		r.logf("info", "moved %q onto the primary monitor", r.currentWindow.Title())
	}
}

func (r *Runner) recoverOnCaseFailure() bool {
	s := r.sess.Session.Settings.RecoverOnCaseFailure
	return s != nil && *s
}

// forcedWindow remembers a window we pinned topmost and whether it was already
// topmost before we touched it.
type forcedWindow struct {
	w          platform.Window
	wasTopmost bool
}

// ensureTopmost pins the bound window above non-topmost windows so a stray
// normal window can't occlude it during interaction. The window's original
// topmost state is recorded once per handle so restoreTopmost can put it back.
// Idempotent and safe to call on every bind/activation.
func (r *Runner) ensureTopmost() {
	if !r.forceTopmost() || r.currentWindow == nil {
		return
	}
	h := r.currentWindow.Handle()
	if r.topmostForced == nil {
		r.topmostForced = make(map[uintptr]forcedWindow)
	}
	if _, seen := r.topmostForced[h]; !seen {
		r.topmostForced[h] = forcedWindow{w: r.currentWindow, wasTopmost: r.drv.IsTopmost(r.currentWindow)}
	}
	if err := r.drv.SetTopmost(r.currentWindow, true); err != nil {
		r.logf("warn", "force topmost: %v", err)
	}
}

// restoreTopmost releases the topmost pin on every window we forced that was not
// already topmost, so an app left open (or a foreign window) isn't stuck on top.
func (r *Runner) restoreTopmost() {
	for _, fw := range r.topmostForced {
		if fw.wasTopmost {
			continue // it was topmost before us; leave it as the app intended
		}
		if err := r.drv.SetTopmost(fw.w, false); err != nil {
			r.logf("debug", "restore topmost: %v", err)
		}
	}
	r.topmostForced = nil
}

// needsForeground reports whether an action delivers input to the focused/
// foreground window and therefore benefits from a focus guard. Window-management
// actions (focus/close/move/resize) drive the window directly and are excluded.
func needsForeground(action string) bool {
	switch action {
	case "mouse_click", "mouse_down", "mouse_up", "mouse_drag", "mouse_scroll",
		"type_text", "key_press", "key_down", "key_up":
		return true
	}
	return false
}

// isKeyboard reports whether an action delivers keystrokes. Re-sending these on
// a contaminated attempt would duplicate input, so they are warned about rather
// than auto-retried.
func isKeyboard(action string) bool {
	switch action {
	case "type_text", "key_press", "key_down", "key_up":
		return true
	}
	return false
}

// ensureForeground makes the bound window the foreground window before input is
// sent. A no-op when it is already foreground; otherwise it re-activates it.
func (r *Runner) ensureForeground() error {
	if r.currentWindow == nil {
		return nil
	}
	r.ensureOnPrimary()
	if r.drv.ForegroundActive(r.currentWindow) {
		return nil
	}
	if err := r.drv.FocusWindow(r.currentWindow); err != nil {
		return fmt.Errorf("could not activate target window before action: %w", err)
	}
	return nil
}

// userInputCount snapshots the watcher's physical-input counter (0 if disabled).
func (r *Runner) userInputCount() uint64 {
	if r.inputWatcher == nil {
		return 0
	}
	return r.inputWatcher.UserEvents()
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
	// Phase 2/3: resolve a UIA element (`uia:`) or an AI-located element
	// (`find:`) to a concrete screen point so the rest of the closed loop
	// (focus -> act -> verify) runs unchanged. The original target is restored
	// on return so re-runs re-resolve from scratch. When both are present, the
	// cheap deterministic `uia:` is tried first and `find:` is its self-healing
	// fallback: the AI re-locates the element and the harvested selector is
	// cached in the locator store for the next run.
	if (cmd.UIA != nil && !cmd.UIA.IsZero()) || cmd.Find != "" {
		restore := cmd.Target
		defer func() { cmd.Target = restore }()
		resolved := false
		if cmd.UIA != nil && !cmd.UIA.IsZero() {
			if err := r.resolveUIATarget(cmd); err == nil {
				resolved = true
			} else if cmd.Find == "" {
				return 0, "", err
			} else {
				r.logf("warn", "  uia target failed (%v); healing via find %q", err, cmd.Find)
			}
		}
		if !resolved {
			if err := r.resolveFindTarget(ctx, cmd); err != nil {
				return 0, "", err
			}
		}
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

	// A focus guard re-activates the window and watches for user intervention
	// around input actions. It needs a retry budget even without a verify so a
	// contaminated non-keyboard action can be re-attempted cleanly.
	guard := r.focusGuard() && needsForeground(cmd.Action) && r.currentWindow != nil

	retries := 0
	if verify != nil {
		retries = r.actionRetries(cmd)
	}
	if guard && retries == 0 {
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

		// Layer 1: bind and activate the intended window before any click/type.
		// Always on (independent of focusGuard) so input never goes to the wrong HWND.
		if needsForeground(cmd.Action) {
			if terr := r.ensureInputTarget(cmd); terr != nil {
				lastErr = terr
				r.logf("warn", "  input focus: %v", terr)
				continue
			}
		} else if guard {
			if ferr := r.ensureForeground(); ferr != nil {
				lastErr = ferr
				r.logf("warn", "  focus guard: %v", ferr)
				continue
			}
			r.ensureTopmost()
		}

		var before image.Image
		if verify != nil && verify.Changed {
			before, _ = r.captureContext(condTarget(verify, cmd.Target))
		}

		// Layer 2: snapshot real user input so we can detect intervention.
		inputBefore := r.userInputCount()

		if aerr := r.executeCommand(ctx, cmd, sr, caseID); aerr != nil {
			lastErr = aerr
			continue
		}

		// A person touched the real mouse/keyboard during the action. Keyboard
		// actions can't be re-sent without duplicating input, so they are only
		// flagged; others re-attempt from a re-asserted foreground.
		if guard && r.userInputCount() != inputBefore {
			if isKeyboard(cmd.Action) {
				r.logf("warn", "  user input detected during %s; result may be unreliable", cmd.Action)
			} else {
				lastErr = fmt.Errorf("user input detected during %s; re-attempting", cmd.Action)
				r.logf("warn", "  %v", lastErr)
				continue
			}
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
		w, werr := r.uiaWindow(condTarget(cond, target))
		if werr != nil {
			return false, nil // window not present yet; keep polling
		}
		q := platform.UIAQuery{
			AutomationID: r.bag.Expand(cond.UIA.AutomationID),
			Name:         r.bag.Expand(cond.UIA.Name),
			ControlType:  cond.UIA.ControlType,
		}
		st, serr := r.drv.ElementState(w, q)
		if serr != nil || !st.Found {
			return false, nil // element absent; keep polling
		}
		if cond.UIA.State != "" {
			switch strings.ToLower(cond.UIA.State) {
			case "enabled":
				if !st.Enabled {
					return false, nil
				}
			case "disabled":
				if st.Enabled {
					return false, nil
				}
			case "selected":
				if !st.Selected {
					return false, nil
				}
			}
		}
		if cond.UIA.Value != "" {
			val := st.Value
			if val == "" {
				val = st.Name
			}
			if val != r.bag.Expand(cond.UIA.Value) {
				return false, nil
			}
		}
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
