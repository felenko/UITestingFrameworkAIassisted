package runner

import (
	"context"
	"fmt"
	"image"
	"strings"
	"time"

	"github.com/felenko/uitest/internal/core/ai"
	"github.com/felenko/uitest/internal/core/locator"
	"github.com/felenko/uitest/internal/core/platform"
	"github.com/felenko/uitest/internal/core/session"
)

// resolveFindTarget resolves a `find:` description to a concrete screen point,
// in cost order (docs/02 Phase 3 — self-healing locators):
//
//  1. cached selector — a previously harvested UIA query from the locator
//     store resolves deterministically (no AI, no pixels);
//  2. AI vision — on a cache miss or a stale selector, capture the search
//     context and ask the AI engine for the element's pixel coordinates;
//  3. harvest — hit-test the UIA tree at the located point, round-trip verify
//     the harvested query, and cache it as an unapproved candidate so the next
//     run takes path 1.
//
// cmd.Target is rewritten to the resolved screen point; the caller restores it
// after the action so re-runs re-resolve from scratch.
func (r *Runner) resolveFindTarget(ctx context.Context, cmd *session.Command) error {
	key := strings.TrimSpace(cmd.Find)
	desc := r.bag.Expand(key)

	// 1) Deterministic: a previously harvested selector.
	if r.locators != nil {
		if e := r.locators.Get(key); e != nil && !e.UIA.IsZero() {
			if err := r.resolveCachedLocator(cmd, e); err == nil {
				if !e.Approved {
					r.unapprovedFinds++
				}
				return nil
			} else {
				r.logf("warn", "  find %q: cached locator (%s) is stale: %v — re-locating via AI", key, e.UIA, err)
			}
		}
	}

	// 2) AI vision: capture the search context and ask for coordinates.
	w, werr := r.uiaWindow(cmd.Target)
	if werr != nil {
		w = nil // fall back to a whole-screen locate
	}
	img, origin, err := r.captureForFind(w)
	if err != nil {
		return fmt.Errorf("find %q: capture failed: %w", key, err)
	}
	rel, err := r.savePNG(img, fmt.Sprintf("find-%d.png", time.Now().UnixNano()))
	if err != nil {
		return err
	}
	req := ai.Request{
		Question:  desc,
		ImagePath: r.absPath(rel),
		Provider:  cmd.Provider,
		Timeout:   r.scale(cmd.Timeout.Duration),
	}
	if cmd.Retries != nil {
		req.Retries = *cmd.Retries
	}
	res := r.engine.LocatePoint(ctx, req, img.Bounds().Dx(), img.Bounds().Dy())
	if res.Err != nil {
		return fmt.Errorf("find %q: AI locate failed: %w", key, res.Err)
	}
	if !res.Found {
		return fmt.Errorf("find %q: AI reports the element is not visible", key)
	}
	pt := platform.Point{X: origin.X + res.X, Y: origin.Y + res.Y}
	r.logf("info", "  find %q -> AI point(%d,%d)", key, pt.X, pt.Y)

	// 3) Harvest a durable selector at the point; cache it for the next run.
	if q, b, ok := r.harvestSelector(w, pt); ok {
		r.cacheLocator(key, cmd, q)
		r.currentWindow = w
		cx, cy := b.X+b.Width/2, b.Y+b.Height/2
		cmd.Target = &session.Target{X: &cx, Y: &cy, RelativeTo: "screen"}
		return nil
	}

	// 4) Fallback: act on the raw AI point. Nothing is cached, so the next run
	// pays the AI again — the log calls it out for the author.
	r.logf("warn", "  find %q: no durable selector harvested at (%d,%d); using the AI point directly (not cached)", key, pt.X, pt.Y)
	if w != nil {
		r.currentWindow = w
	}
	x, y := pt.X, pt.Y
	cmd.Target = &session.Target{X: &x, Y: &y, RelativeTo: "screen"}
	return nil
}

// resolveCachedLocator resolves a stored locator entry via UI Automation and
// rewrites cmd.Target to the element's center, exactly like resolveUIATarget.
func (r *Runner) resolveCachedLocator(cmd *session.Command, e *locator.Entry) error {
	t := cmd.Target
	if (t == nil || (t.Window == "" && t.Process == "" && t.Class == "")) && e.Window != "" {
		t = &session.Target{Window: e.Window}
	}
	w, err := r.uiaWindow(t)
	if err != nil {
		return err
	}
	q := platform.UIAQuery{AutomationID: e.UIA.AutomationID, Name: e.UIA.Name, ControlType: e.UIA.ControlType}
	b, err := r.drv.FindElement(w, q)
	if err != nil {
		return err
	}
	r.currentWindow = w
	cx, cy := b.X+b.Width/2, b.Y+b.Height/2
	cmd.Target = &session.Target{X: &cx, Y: &cy, RelativeTo: "screen"}
	approved := "candidate"
	if e.Approved {
		approved = "approved"
	}
	r.logf("info", "  find %q -> cached %s -> point(%d,%d) in %q (%s)", e.Find, e.UIA, cx, cy, w.Title(), approved)
	return nil
}

// harvestSelector hit-tests the UIA tree at pt and tries to turn the element
// there into a durable query. The query must round-trip: searching the window
// for it has to land back on a rectangle containing pt, otherwise it is
// ambiguous (matches a different element first) and is rejected.
func (r *Runner) harvestSelector(w platform.Window, pt platform.Point) (platform.UIAQuery, platform.Bounds, bool) {
	node, err := r.drv.ElementAtPoint(pt)
	if err != nil {
		r.logf("debug", "  harvest at (%d,%d): %v", pt.X, pt.Y, err)
		return platform.UIAQuery{}, platform.Bounds{}, false
	}
	if node.AutomationID == "" && node.Name == "" {
		r.logf("debug", "  harvest at (%d,%d): element exposes no AutomationId or Name", pt.X, pt.Y)
		return platform.UIAQuery{}, platform.Bounds{}, false
	}
	if w == nil {
		return platform.UIAQuery{}, platform.Bounds{}, false // nowhere to round-trip verify
	}
	for _, q := range candidateQueries(node) {
		b, err := r.drv.FindElement(w, q)
		if err != nil {
			continue
		}
		if containsPoint(b, pt) {
			return q, b, true
		}
	}
	r.logf("debug", "  harvest at (%d,%d): %q/%q did not round-trip uniquely", pt.X, pt.Y, node.AutomationID, node.Name)
	return platform.UIAQuery{}, platform.Bounds{}, false
}

// candidateQueries orders the harvested properties from most to least durable:
// AutomationId is the strongest identity, the visible Name the fallback; the
// control type narrows either when the bare property is ambiguous.
func candidateQueries(n platform.UIANode) []platform.UIAQuery {
	var qs []platform.UIAQuery
	if n.AutomationID != "" {
		qs = append(qs, platform.UIAQuery{AutomationID: n.AutomationID})
		if n.ControlType != "" {
			qs = append(qs, platform.UIAQuery{AutomationID: n.AutomationID, ControlType: n.ControlType})
		}
	}
	if n.Name != "" {
		qs = append(qs, platform.UIAQuery{Name: n.Name})
		if n.ControlType != "" {
			qs = append(qs, platform.UIAQuery{Name: n.Name, ControlType: n.ControlType})
		}
	}
	return qs
}

// cacheLocator records a harvested selector as an unapproved candidate. A
// re-harvest of an existing key (healing a stale selector) keeps a note of
// what it replaced so the reviewer can see the history.
func (r *Runner) cacheLocator(key string, cmd *session.Command, q platform.UIAQuery) {
	if r.locators == nil {
		return
	}
	entry := locator.Entry{
		Find:        key,
		UIA:         locator.Selector{AutomationID: q.AutomationID, Name: q.Name, ControlType: q.ControlType},
		HarvestedAt: time.Now(),
		Provider:    r.provider,
	}
	if t := cmd.Target; t != nil && t.Window != "" {
		entry.Window = r.bag.Expand(t.Window)
	}
	if prev := r.locators.Get(key); prev != nil && !prev.UIA.IsZero() {
		entry.Note = fmt.Sprintf("healed %s; previous selector: %s", time.Now().Format("2006-01-02"), prev.UIA)
	}
	r.locators.Put(entry)
	if err := r.locators.Save(); err != nil {
		r.logf("warn", "  locator store save: %v", err)
	}
	r.unapprovedFinds++
	r.logf("info", "  find %q: harvested %s (candidate — review with `uitest locators`)", key, entry.UIA)
}

// captureForFind captures the search context for an AI locate and returns the
// image plus the screen position of its top-left pixel, so an image coordinate
// maps back to a screen point.
func (r *Runner) captureForFind(w platform.Window) (image.Image, platform.Point, error) {
	if w != nil {
		if b, err := w.Bounds(); err == nil {
			if img, err := r.drv.CaptureWindow(w); err == nil {
				return img, platform.Point{X: b.X, Y: b.Y}, nil
			}
		}
	}
	img, err := r.drv.CaptureScreen()
	return img, platform.Point{}, err
}

func containsPoint(b platform.Bounds, p platform.Point) bool {
	return p.X >= b.X && p.X < b.X+b.Width && p.Y >= b.Y && p.Y < b.Y+b.Height
}

// loadLocators opens the session's locator store; failures are downgraded to a
// warning (find: then always takes the AI path and cannot cache).
func (r *Runner) loadLocators() {
	ls, err := locator.Load(locator.PathFor(r.sess.SourcePath))
	if err != nil {
		r.logf("warn", "locator store: %v (find: targets will re-locate via AI every run)", err)
		return
	}
	r.locators = ls
}

// finishLocators flushes the store and surfaces unapproved-candidate usage so
// a green run still tells the author there is review debt.
func (r *Runner) finishLocators() {
	if r.locators == nil {
		return
	}
	if err := r.locators.Save(); err != nil {
		r.logf("warn", "locator store save: %v", err)
	}
	if r.unapprovedFinds > 0 {
		r.logf("warn", "%d find: resolution(s) used unapproved candidate locators — review %s (uitest locators)", r.unapprovedFinds, r.locators.Path())
	}
}
