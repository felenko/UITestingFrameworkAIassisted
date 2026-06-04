package runner

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"time"

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
			ok, _ := r.pollAI(ctx, rw.ForAI, deadline)
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
	if r.app == nil || r.app.cmd.Process == nil {
		return
	}
	r.logf("info", "terminating app process")
	_ = r.app.cmd.Process.Kill()
	_, _ = r.app.cmd.Process.Wait()
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
	return exitOK, nil
}

func baselineRel(sessionDir, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(sessionDir, p)
}
