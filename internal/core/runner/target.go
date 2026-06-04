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

// windowQuery builds a platform query from a target + session default strategy.
func (r *Runner) windowQuery(t *session.Target) platform.WindowQuery {
	strategy := r.sess.Session.Settings.WindowMatch
	q := platform.WindowQuery{
		Title:    r.bag.Expand(t.Window),
		Process:  r.bag.Expand(t.Process),
		Class:    r.bag.Expand(t.Class),
		Strategy: strategy,
		PID:      r.appPID,
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
// be resolved (e.g. the title drifted after the app was edited), it falls back
// to the currently bound window — the app under test — rather than failing.
func (r *Runner) findWindow(t *session.Target) (platform.Window, error) {
	if t == nil || (t.Window == "" && t.Process == "" && t.Class == "") {
		if r.currentWindow != nil {
			return r.currentWindow, nil
		}
		return nil, fmt.Errorf("no window specified and no current window")
	}
	w, err := r.drv.FindWindow(r.windowQuery(t))
	if err != nil {
		if r.currentWindow != nil {
			r.logf("warn", "window %s not found; reusing current window %q", t.Describe(), r.currentWindow.Title())
			return r.currentWindow, nil
		}
		return nil, err
	}
	return w, nil
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

// captureAndSave captures a target and writes it under the given file name.
func (r *Runner) captureAndSave(t *session.Target, name string) (string, error) {
	img, err := r.capture(t)
	if err != nil {
		return "", err
	}
	return r.savePNG(img, name)
}
