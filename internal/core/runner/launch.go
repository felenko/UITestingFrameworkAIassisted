package runner

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/felenko/uitest/internal/core/ai"
	"github.com/felenko/uitest/internal/core/platform"
	"github.com/felenko/uitest/internal/core/session"
)

type appProcess struct {
	cmd *exec.Cmd
}

// launchApp starts the configured process and waits for readiness.
func (r *Runner) launchApp(ctx context.Context) error {
	app := r.sess.Session.Application
	path := r.bag.Expand(app.Path)
	args := make([]string, 0, len(app.Args))
	for _, a := range app.Args {
		args = append(args, r.bag.Expand(a))
	}

	r.logf("info", "launching %s %v", path, args)
	cmd := exec.Command(path, args...)
	if app.WorkingDir != "" {
		cmd.Dir = r.bag.Expand(app.WorkingDir)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting %q: %w", path, err)
	}
	r.app = &appProcess{cmd: cmd}
	if cmd.Process != nil {
		r.appPID = uint32(cmd.Process.Pid)
	}

	if err := r.waitForReady(ctx, app); err != nil {
		return err
	}
	r.attachMainWindow()
	return nil
}

func (r *Runner) waitForReady(ctx context.Context, app session.Application) error {
	timeout := r.scale(app.StartupTimeout.Or(session.DefaultStartupTimeout))
	deadline := time.Now().Add(timeout)
	rw := app.ReadyWhen

	if rw == nil {
		// No condition: a short settle delay.
		time.Sleep(r.scale(500 * time.Millisecond))
		return nil
	}
	if rw.Delay.Duration > 0 {
		time.Sleep(r.scale(rw.Delay.Duration))
		return nil
	}

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if rw.Window != nil {
			q := r.readyWindowQuery(rw.Window)
			if w, err := r.drv.FindWindow(q); err == nil {
				r.currentWindow = w
				r.logf("info", "app ready: window %q found", w.Title())
				return nil
			}
		}
		if rw.ForAI != nil {
			ok, _ := r.pollCondition(ctx, rw.ForAI, rw.ForAI.Target, nil, deadline)
			if ok {
				return nil
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("app not ready within %v (readyWhen unmet)", timeout)
}

// attachMainWindow tries to locate the app's primary window when none is set.
func (r *Runner) attachMainWindow() {
	if r.currentWindow != nil {
		return
	}
	rw := r.sess.Session.Application.ReadyWhen
	if rw != nil && rw.Window != nil {
		if w, err := r.drv.FindWindow(r.readyWindowQuery(rw.Window)); err == nil {
			r.currentWindow = w
		}
	}
}

// readyWindowQuery builds a window query from a readyWhen window match.
func (r *Runner) readyWindowQuery(wm *session.WindowMatch) platform.WindowQuery {
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
	return q
}

// shutdownApp tears the app down per session.application.shutdown.
func (r *Runner) shutdownApp() {
	if r.app == nil {
		return
	}
	mode := r.sess.Session.Application.Shutdown
	switch mode {
	case "leaveOpen":
		r.logf("info", "leaving app open (shutdown: leaveOpen)")
		return
	case "force":
		r.killApp()
	default: // graceful
		if r.currentWindow != nil {
			r.logf("info", "closing app window gracefully")
			_ = r.drv.CloseWindow(r.currentWindow)
			if r.waitExit(3 * time.Second) {
				return
			}
		}
		r.killApp()
	}
}

func (r *Runner) killApp() {
	ourPID := 0
	if r.app != nil && r.app.cmd.Process != nil {
		ourPID = r.app.cmd.Process.Pid
	}
	// SAFETY: only force-kill the window's process when it is the very process we
	// launched. Single-instance apps (e.g. Win11 tabbed Notepad) merge our launch
	// into a pre-existing user process; force-killing that would destroy the
	// user's other windows/unsaved work. In that case we never kill the foreign
	// process — we only close our own.
	if r.currentWindow != nil && ourPID != 0 {
		if pid := r.drv.WindowPID(r.currentWindow); pid != 0 && int(pid) == ourPID {
			r.logf("info", "terminating app process (pid %d)", pid)
			_ = exec.Command("taskkill", "/PID", strconv.Itoa(int(pid)), "/T", "/F").Run()
		} else if pid != 0 && int(pid) != ourPID {
			r.logf("warn", "window is owned by a different process (pid %d, not our %d) — not force-killing; leaving it open", pid, ourPID)
		}
	}
	if ourPID != 0 {
		_ = r.app.cmd.Process.Kill()
		_, _ = r.app.cmd.Process.Wait()
	}
}

func (r *Runner) waitExit(d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		_, _ = r.app.cmd.Process.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// doctor verifies the AI provider CLI and screen capture work (docs/02 §2).
func (r *Runner) doctor() (int, error) {
	if r.opts.DryRun {
		return exitOK, nil
	}
	if _, err := r.drv.CaptureScreen(); err != nil {
		return exitSetupC, fmt.Errorf("screen capture not available: %w", err)
	}
	r.logf("debug", "doctor: screen capture OK")

	provider := r.sess.Session.AI.Provider
	if r.opts.Provider != "" {
		provider = r.opts.Provider
	}
	if provider == "" {
		provider = "claude"
	}
	adapter, ok := ai.NewAdapter(provider)
	if !ok {
		return exitSetupC, fmt.Errorf("unknown AI provider %q", provider)
	}
	if !adapter.Available() {
		hint := ""
		if provider == "cursor" {
			hint = fmt.Sprintf(" (looked for %q)", ai.ResolveCursorAgent())
		}
		return exitSetupC, fmt.Errorf("AI provider %q not found%s — assert_ai will fail", provider, hint)
	}
	r.logf("debug", "doctor: AI provider %q OK", provider)
	return exitOK, nil
}

func baselineRel(sessionDir, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(sessionDir, p)
}
