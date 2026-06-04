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
			perChar = 10 * time.Millisecond
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
		return r.drv.FocusWindow(w)
	case "close_window":
		w, err := r.findWindow(cmd.Target)
		if err != nil {
			return err
		}
		return r.drv.CloseWindow(w)
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

	// --- wait ---
	case "wait":
		if cmd.ForAI != nil {
			deadline := time.Now().Add(r.scale(cmd.ForAI.Timeout.Or(30 * time.Second)))
			ok, err := r.pollAI(ctx, cmd.ForAI, deadline)
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

	default:
		return fmt.Errorf("unsupported action %q", cmd.Action)
	}
}

// pollAI repeatedly asks an AI question until it answers affirmatively or the
// deadline passes (wait.forAI / readyWhen.forAI).
func (r *Runner) pollAI(ctx context.Context, f *session.ForAI, deadline time.Time) (bool, error) {
	poll := f.PollEvery.Or(time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		default:
		}
		img, err := r.capture(f.Target)
		if err != nil {
			return false, err
		}
		name := fmt.Sprintf("poll-%d.png", time.Now().UnixNano())
		rel, err := r.savePNG(img, name)
		if err != nil {
			return false, err
		}
		out := r.engine.AssertAI(ctx, ai.Request{
			Question: r.bag.Expand(f.Question), ImagePath: r.absPath(rel), Expect: "yes",
		})
		if out.Err == nil && out.Verdict {
			return true, nil
		}
		time.Sleep(r.scale(poll))
	}
	return false, nil
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
