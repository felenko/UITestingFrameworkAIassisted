package runner

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"

	"github.com/felenko/uitest/internal/core/platform"
	"github.com/felenko/uitest/internal/core/session"
)

// queryPID returns the PID used to disambiguate window matches. Many real apps
// launch through a shim/launcher whose own PID owns no window, so once a real
// app window has been bound we prefer its owning process for all later matching.
func (r *Runner) queryPID() uint32 {
	if r.uiPID != 0 {
		return r.uiPID
	}
	return r.appPID
}

// windowQuery builds a platform query from a target + session default strategy.
func (r *Runner) windowQuery(t *session.Target) platform.WindowQuery {
	strategy := r.sess.Session.Settings.WindowMatch
	q := platform.WindowQuery{
		Title:    r.bag.Expand(t.Window),
		Process:  r.bag.Expand(t.Process),
		Class:    r.bag.Expand(t.Class),
		Strategy: strategy,
		PID:      r.queryPID(),
	}
	// If the target explicitly names process/class, prefer that strategy.
	switch {
	case t.Process != "":
		q.Strategy = "process"
	case t.Class != "":
		q.Strategy = "class"
	case t.Window != "":
		q.Strategy = "title"
	}
	return q
}

// findWindow locates the window a target refers to. If an explicit query can't
// be resolved, it tries the launched app's primary window, then a still-valid
// current window — never a destroyed handle left over from a closed dialog.
func (r *Runner) findWindow(t *session.Target) (platform.Window, error) {
	if t == nil || (t.Window == "" && t.Process == "" && t.Class == "") {
		if r.currentWindow != nil {
			return r.currentWindow, nil
		}
		return nil, fmt.Errorf("no window specified and no current window")
	}
	pid := r.queryPID()
	w, err := r.drv.FindWindow(r.windowQuery(t))
	if err == nil {
		// Reject a match owned by a foreign process (a title/class collision,
		// e.g. a File Explorer window whose title merely contains the app name).
		// When the target is `exact`, never fall back so the caller no-ops; else
		// prefer the app's own primary window.
		if pid != 0 && r.drv.WindowPID(w) != pid {
			if t.Exact {
				return nil, fmt.Errorf("window %s matched only a foreign process", t.Describe())
			}
			if aw, aerr := r.drv.FindWindowByPID(pid); aerr == nil {
				r.logf("warn", "window %s matched a foreign process; using app window %q", t.Describe(), aw.Title())
				return aw, nil
			}
		}
		return w, nil
	}
	if t.Exact {
		return nil, err
	}
	if pid != 0 {
		if aw, aerr := r.drv.FindWindowByPID(pid); aerr == nil {
			r.logf("warn", "window %s not found; using app window %q", t.Describe(), aw.Title())
			return aw, nil
		}
	}
	if r.currentWindow != nil {
		if _, berr := r.currentWindow.Bounds(); berr == nil {
			r.logf("warn", "window %s not found; reusing current window %q", t.Describe(), r.currentWindow.Title())
			return r.currentWindow, nil
		}
		r.currentWindow = nil
	}
	return nil, err
}

// originFor returns the screen-absolute origin that a relative coordinate is
// measured from, honoring relativeTo / raw and the session default space.
func (r *Runner) originFor(relativeTo string, raw bool) (platform.Point, error) {
	if raw {
		return platform.Point{X: 0, Y: 0}, nil
	}
	space := relativeTo
	if space == "" {
		space = r.sess.Session.Settings.CoordinateSpace
	}
	if space == "screen" {
		return platform.Point{X: 0, Y: 0}, nil
	}
	// window-relative: offset by the current window's top-left.
	if r.currentWindow != nil {
		b, err := r.currentWindow.Bounds()
		if err == nil {
			return platform.Point{X: b.X, Y: b.Y}, nil
		}
	}
	// No current window: treat as screen-absolute.
	return platform.Point{X: 0, Y: 0}, nil
}

// resolvePoint converts a point target to a screen-absolute coordinate.
func (r *Runner) resolvePoint(t *session.Target) (platform.Point, error) {
	if !t.IsPoint() {
		return platform.Point{}, fmt.Errorf("expected a point target { x, y }")
	}
	origin, err := r.originFor(t.RelativeTo, t.Raw)
	if err != nil {
		return platform.Point{}, err
	}
	return platform.Point{X: origin.X + *t.X, Y: origin.Y + *t.Y}, nil
}

// capture grabs the image for an observation target (screen | window | rect).
func (r *Runner) capture(t *session.Target) (image.Image, error) {
	if t == nil || t.IsZero() || t.Screen {
		return r.drv.CaptureScreen()
	}
	if t.Rect != nil {
		b, err := r.rectBounds(t)
		if err != nil {
			return nil, err
		}
		return r.drv.CaptureBounds(b)
	}
	if t.Window != "" || t.Process != "" || t.Class != "" {
		w, err := r.findWindow(t)
		if err != nil {
			return nil, err
		}
		// PrintWindow-based capture gets the window's own pixels even if it is
		// occluded or not focused.
		return r.drv.CaptureWindow(w)
	}
	// Point target isn't capturable; fall back to whole screen.
	return r.drv.CaptureScreen()
}

// rectBounds converts a rectangle target to screen-absolute bounds.
func (r *Runner) rectBounds(t *session.Target) (platform.Bounds, error) {
	origin, err := r.originFor(t.RelativeTo, t.Raw)
	if err != nil {
		return platform.Bounds{}, err
	}
	// A rect relativeTo window measures from that window's origin.
	if (t.RelativeTo == "" && r.sess.Session.Settings.CoordinateSpace == "window") || t.RelativeTo == "window" {
		if r.currentWindow != nil {
			if b, berr := r.currentWindow.Bounds(); berr == nil {
				origin = platform.Point{X: b.X, Y: b.Y}
			}
		}
	}
	return platform.Bounds{
		X:      origin.X + t.Rect.X,
		Y:      origin.Y + t.Rect.Y,
		Width:  t.Rect.Width,
		Height: t.Rect.Height,
	}, nil
}

// savePNG writes an image to the screenshots dir and returns its outDir-relative path.
func (r *Runner) savePNG(img image.Image, name string) (string, error) {
	full := filepath.Join(r.shotsDir, name)
	f, err := os.Create(full)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		return "", err
	}
	rel, err := filepath.Rel(r.outDir, full)
	if err != nil {
		rel = filepath.Join("screenshots", name)
	}
	return filepath.ToSlash(rel), nil
}

// ensureInputTarget binds and activates the window input should go to before a
// click or keystroke. Unlike focusGuard (user-intervention detection), this
// always runs so the runner never types into the wrong HWND when a modal dialog
// or an explicit target window is in play.
func (r *Runner) ensureInputTarget(cmd *session.Command) error {
	if !needsForeground(cmd.Action) {
		return nil
	}
	// Command names a window: bind and foreground it.
	if cmd.Target != nil && (cmd.Target.Window != "" || cmd.Target.Process != "" || cmd.Target.Class != "") {
		w, err := r.findWindow(cmd.Target)
		if err != nil {
			return fmt.Errorf("input target window: %w", err)
		}
		r.currentWindow = w
		if err := r.drv.FocusWindow(w); err != nil {
			return fmt.Errorf("could not activate input target window: %w", err)
		}
		r.ensureTopmost()
		return nil
	}
	// Prefer the app's current foreground window (e.g. an error message box).
	if pid := r.queryPID(); pid != 0 {
		if fg, err := r.drv.ForegroundWindow(); err == nil && r.drv.WindowPID(fg) == pid {
			r.currentWindow = fg
			if !r.drv.ForegroundActive(fg) {
				if err := r.drv.FocusWindow(fg); err != nil {
					return fmt.Errorf("could not activate foreground app window: %w", err)
				}
			}
			r.ensureTopmost()
			return nil
		}
	}
	// Fall back to the bound window or the app's primary window.
	if r.currentWindow != nil {
		if err := r.ensureForeground(); err != nil {
			return err
		}
		r.ensureTopmost()
		return nil
	}
	if pid := r.queryPID(); pid != 0 {
		if w, err := r.drv.FindWindowByPID(pid); err == nil {
			r.currentWindow = w
			if err := r.drv.FocusWindow(w); err != nil {
				return fmt.Errorf("could not activate app window: %w", err)
			}
			r.ensureTopmost()
		}
	}
	return nil
}

// captureAndSave captures a target and writes it under the given file name.
func (r *Runner) captureAndSave(t *session.Target, name string) (string, error) {
	img, err := r.capture(t)
	if err != nil {
		return "", err
	}
	return r.savePNG(img, name)
}
