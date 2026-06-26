package runner

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/felenko/uitest/internal/core/ai"
	"github.com/felenko/uitest/internal/core/event"
	"github.com/felenko/uitest/internal/core/result"
	"github.com/felenko/uitest/internal/core/session"
)

// absPath converts an outDir-relative artifact path to an absolute path for the
// AI provider to read.
func (r *Runner) absPath(rel string) string {
	return filepath.Join(r.outDir, filepath.FromSlash(rel))
}

// aiRequest builds an AI engine request from an observation command.
func (r *Runner) aiRequest(cmd *session.Command, rel string) ai.Request {
	req := ai.Request{
		Question:  r.bag.Expand(cmd.Question),
		ImagePath: r.absPath(rel),
		Expect:    cmd.Expect,
		Provider:  cmd.Provider,
		Timeout:   r.scale(cmd.Timeout.Duration),
	}
	if cmd.Retries != nil {
		req.Retries = *cmd.Retries
	}
	if cmd.Samples != nil {
		req.Samples = *cmd.Samples
	}
	return req
}

// executeCommand performs one machine command. Screenshots captured by the
// command are appended to sr.Screenshots.
func (r *Runner) executeCommand(ctx context.Context, cmd *session.Command, sr *result.Step, caseID string) error {
	switch cmd.Action {

	// --- mouse ---
	case "mouse_move":
		p, err := r.resolvePoint(cmd.Target)
		if err != nil {
			return err
		}
		return r.drv.MouseMove(p)
	case "mouse_click":
		p, err := r.resolvePoint(cmd.Target)
		if err != nil {
			return err
		}
		count := cmd.Count
		if count == 0 {
			count = 1
		}
		return r.drv.MouseClick(p, cmd.Button, count)
	case "mouse_down":
		p, err := r.resolvePoint(cmd.Target)
		if err != nil {
			return err
		}
		return r.drv.MouseDown(p, cmd.Button)
	case "mouse_up":
		p, err := r.resolvePoint(cmd.Target)
		if err != nil {
			return err
		}
		return r.drv.MouseUp(p, cmd.Button)
	case "mouse_drag":
		from, err := r.resolvePoint(cmd.From)
		if err != nil {
			return fmt.Errorf("from: %w", err)
		}
		to, err := r.resolvePoint(cmd.To)
		if err != nil {
			return fmt.Errorf("to: %w", err)
		}
		return r.drv.MouseDrag(from, to, cmd.Button)
	case "mouse_scroll":
		p, err := r.resolvePoint(cmd.Target)
		if err != nil {
			return err
		}
		return r.drv.MouseScroll(p, cmd.DX, cmd.DY)

	// --- keyboard ---
	case "type_text":
		// SendInput delivers keystrokes asynchronously; pace them a little so
		// fast apps don't drop characters, and let the queue drain afterwards.
		perChar := time.Duration(cmd.PerCharDelayMs) * time.Millisecond
		if perChar == 0 {
			perChar = 25 * time.Millisecond // pace input so fast apps don't drop characters
		}
		if err := r.drv.TypeText(r.bag.Expand(cmd.Text), perChar); err != nil {
			return err
		}
		time.Sleep(r.scale(150 * time.Millisecond))
		return nil
	case "key_press":
		for _, chord := range cmd.Keys {
			if err := r.drv.KeyPress(r.bag.Expand(chord)); err != nil {
				return err
			}
		}
		return nil
	case "key_down":
		return r.drv.KeyDown(cmd.Key)
	case "key_up":
		return r.drv.KeyUp(cmd.Key)

	// --- window ---
	case "focus_window":
		w, err := r.findWindow(cmd.Target)
		if err != nil {
			return err
		}
		r.currentWindow = w
		if err := r.drv.FocusWindow(w); err != nil {
			return err
		}
		r.ensureTopmost()
		return nil
	case "close_window":
		w, err := r.findWindow(cmd.Target)
		if err != nil {
			return err
		}
		return r.drv.CloseWindow(w)
	case "close_popup":
		// Close every visible app window that is not r.currentWindow (the
		// runner's tracked main window). Use-case: a child form (patron record,
		// dialog) opened during a step needs to be closed in cleanup without
		// knowing its title. Harmless if no popup is open.
		pid := r.uiPID
		if pid == 0 {
			pid = r.appPID
		}
		if pid == 0 {
			return nil
		}
		wins, err := r.drv.AppWindows(pid)
		if err != nil {
			return nil // best-effort
		}
		var mainHandle uintptr
		if r.currentWindow != nil {
			mainHandle = r.currentWindow.Handle()
		}
		for _, w := range wins {
			if mainHandle != 0 && w.Handle() == mainHandle {
				continue
			}
			_ = r.drv.CloseWindow(w)
		}
		return nil
	case "move_window":
		w, err := r.findWindow(cmd.Target)
		if err != nil {
			return err
		}
		return r.drv.MoveWindow(w, cmd.X, cmd.Y)
	case "resize_window":
		w, err := r.findWindow(cmd.Target)
		if err != nil {
			return err
		}
		return r.drv.ResizeWindow(w, cmd.Width, cmd.Height)
	case "launch_app":
		return r.launchMidTest(ctx, cmd)

	// --- control flow ---
	case "wait_for":
		cond := cmd.WaitCondition
		if cond == nil || cond.IsZero() {
			return fmt.Errorf("wait_for: condition is required")
		}
		timeout := r.scale(cmd.Timeout.Or(30 * time.Second))
		// Shallow-copy the condition so we can override PollEvery without
		// mutating the parsed YAML struct.
		effective := *cond
		if cmd.Interval.Duration > 0 {
			effective.PollEvery = cmd.Interval
		}
		ok, err := r.pollCondition(ctx, &effective, cmd.Target, nil, time.Now().Add(timeout))
		if err != nil {
			return err
		}
		if !ok && !cmd.Optional {
			return fmt.Errorf("wait_for: condition not met after %s", timeout)
		}
		return nil

	case "repeat":
		return r.executeRepeat(ctx, cmd, sr, caseID)

	// --- wait ---
	case "wait":
		if cmd.ForAI != nil {
			deadline := time.Now().Add(r.scale(cmd.ForAI.Timeout.Or(30 * time.Second)))
			ok, err := r.pollCondition(ctx, cmd.ForAI, cmd.ForAI.Target, nil, deadline)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("wait.forAI condition not met before timeout")
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(r.scale(cmd.MS.Duration)):
		}
		return nil

	// --- observation ---
	case "screenshot":
		name := r.bag.Expand(cmd.Save)
		if name == "" {
			name = fmt.Sprintf("%s_step-%02d.png", caseID, sr.Index)
		}
		rel, err := r.captureAndSave(cmd.Target, name)
		if err != nil {
			return err
		}
		sr.Screenshots = append(sr.Screenshots, rel)
		r.bus.Publish(event.Event{
			Type: event.ScreenshotCaptured, CaseID: caseID, Path: rel,
			Target: cmd.Target.Describe(), Which: "step",
		})
		return nil
	case "assert_ai":
		// Mid-scenario assertion inside a step: fail the step if it doesn't pass.
		name := fmt.Sprintf("%s_step-%02d_assert.png", caseID, sr.Index)
		rel, err := r.captureAndSave(cmd.Target, name)
		if err != nil {
			return err
		}
		sr.Screenshots = append(sr.Screenshots, rel)
		out := r.engine.AssertAI(ctx, r.aiRequest(cmd, rel))
		if out.Err != nil {
			return out.Err
		}
		if !out.Pass {
			return fmt.Errorf("assert_ai failed: %q -> %v (expected %s)", cmd.Question, out.Verdict, cmd.Expect)
		}
		return nil
	case "read_text_ai":
		name := fmt.Sprintf("%s_step-%02d_read.png", caseID, sr.Index)
		rel, err := r.captureAndSave(cmd.Target, name)
		if err != nil {
			return err
		}
		sr.Screenshots = append(sr.Screenshots, rel)
		value, _, err := r.engine.ReadText(ctx, r.aiRequest(cmd, rel))
		if err != nil {
			return err
		}
		r.bag.Set(cmd.Store, value)
		r.logf("info", "read_text_ai stored %s=%q", cmd.Store, value)
		return nil

	case "assert_window", "assert_element", "assert_dialog":
		// Mid-scenario deterministic gate: fail the step if the check doesn't hold.
		name := fmt.Sprintf("%s_step-%02d_assert.png", caseID, sr.Index)
		if rel, err := r.captureAndSave(nil, name); err == nil {
			sr.Screenshots = append(sr.Screenshots, rel)
		}
		verdict, question, observed, err := r.deterministicCheck(cmd)
		if err != nil {
			return err
		}
		want := normalizeExpect(cmd.Expect) != "no"
		if verdict != want {
			return fmt.Errorf("%s failed: %s — %s (expected %v)", cmd.Action, question, observed, want)
		}
		return nil

	default:
		return fmt.Errorf("unsupported action %q", cmd.Action)
	}
}

func (r *Runner) launchMidTest(ctx context.Context, cmd *session.Command) error {
	app := session.Application{
		Path:      cmd.Path,
		Args:      cmd.Args,
		ReadyWhen: cmd.ReadyWhen,
	}
	prev := r.sess.Session.Application
	r.sess.Session.Application = app
	defer func() { r.sess.Session.Application = prev }()
	return r.launchApp(ctx)
}

// executeRepeat implements the `repeat` action. It runs the body (cmd.Steps)
// repeatedly according to the exit form: a fixed count (times), a while
// condition (checked before each iteration), or an until condition (checked
// after each iteration). cmd.Timeout is a hard ceiling; cmd.Grace lets a
// while/until condition wobble for a short period before the loop exits.
func (r *Runner) executeRepeat(ctx context.Context, cmd *session.Command, sr *result.Step, caseID string) error {
	var deadline time.Time
	if cmd.Timeout.Duration > 0 {
		deadline = time.Now().Add(r.scale(cmd.Timeout.Duration))
	}
	interval := r.scale(cmd.Interval.Or(0))
	grace := r.scale(cmd.Grace.Duration)

	evalCond := func(cond *session.Condition) (bool, error) {
		if cond == nil || cond.IsZero() {
			return true, nil
		}
		return r.pointRungs(ctx, cond, nil, nil)
	}

	// graceRecover polls until the condition returns wantVal or grace expires.
	graceRecover := func(cond *session.Condition, wantVal bool) bool {
		if grace <= 0 {
			return false
		}
		poll := r.scale(cmd.Interval.Or(500 * time.Millisecond))
		gd := time.Now().Add(grace)
		for time.Now().Before(gd) {
			r.sleep(ctx, poll)
			if ctx.Err() != nil {
				return false
			}
			ok, _ := evalCond(cond)
			if ok == wantVal {
				return true
			}
		}
		return false
	}

	for iteration := 0; ; iteration++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			break
		}
		if cmd.Times > 0 && iteration >= cmd.Times {
			break
		}

		// While condition: must hold before each body run.
		if cmd.While != nil && !cmd.While.IsZero() {
			ok, err := evalCond(cmd.While)
			if err != nil {
				return err
			}
			if !ok && !graceRecover(cmd.While, true) {
				break
			}
		}

		// Run body; a failing command breaks the current iteration.
		for i := range cmd.Steps {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			_, _, err := r.runCommand(ctx, &cmd.Steps[i], sr, caseID)
			if err != nil {
				r.logf("warn", "repeat[%d]: body[%d] %s: %v", iteration, i, cmd.Steps[i].Action, err)
				if cmd.Strict {
					return fmt.Errorf("repeat body[%d] %s: %w", i, cmd.Steps[i].Action, err)
				}
				break
			}
		}

		// Until condition: exit when it first becomes true.
		if cmd.Until != nil && !cmd.Until.IsZero() {
			ok, err := evalCond(cmd.Until)
			if err != nil {
				return err
			}
			if ok {
				// With grace, require the condition to hold for the full grace period.
				if grace > 0 && !graceRecover(cmd.Until, false) {
					break // stayed true for the grace period — done
				} else if grace == 0 {
					break
				}
			}
		}

		if interval > 0 {
			r.sleep(ctx, interval)
		}
	}
	return nil
}
