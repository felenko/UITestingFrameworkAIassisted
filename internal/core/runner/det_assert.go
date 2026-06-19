package runner

import (
	"fmt"
	"strings"

	"github.com/felenko/uitest/internal/core/platform"
	"github.com/felenko/uitest/internal/core/result"
	"github.com/felenko/uitest/internal/core/session"
)

// isDeterministicAssert reports whether an assert action is evaluated against the
// UI Automation tree / window list directly (no screenshot reasoning, no AI).
func isDeterministicAssert(action string) bool {
	switch action {
	case "assert_window", "assert_element", "assert_dialog":
		return true
	}
	return false
}

// evalDeterministic computes a no-AI verdict for a deterministic assert and fills
// the result record. The caller (runAssert) still captures a screenshot so the
// report keeps visual evidence alongside the deterministic verdict.
func (r *Runner) evalDeterministic(ar *result.Assert, a *session.Command) {
	ar.Provider = "deterministic"
	verdict, question, observed, err := r.deterministicCheck(a)
	if ar.Question == "" {
		ar.Question = question
	}
	ar.RawAnswer = observed
	if err != nil {
		ar.Status = result.StatusError
		ar.Error = err.Error()
		return
	}
	ar.Verdict = verdict
	want := normalizeExpect(a.Expect) != "no"
	if verdict == want {
		ar.Status = result.StatusPassed
	} else {
		ar.Status = result.StatusFailed
	}
}

func (r *Runner) deterministicCheck(a *session.Command) (verdict bool, question, observed string, err error) {
	switch a.Action {
	case "assert_window":
		return r.checkWindow(a)
	case "assert_element":
		return r.checkElement(a)
	case "assert_dialog":
		return r.checkDialog(a)
	}
	return false, "", "", fmt.Errorf("not a deterministic assert: %s", a.Action)
}

// checkWindow verifies a window's presence/absence.
func (r *Runner) checkWindow(a *session.Command) (bool, string, string, error) {
	var exists bool
	if a.Target.Exact {
		// Require an app-process-owned match so a foreign window whose title
		// merely contains the app name (e.g. a browser tab) can't satisfy it.
		_, err := r.findWindow(a.Target)
		exists = err == nil
	} else {
		wm := &session.WindowMatch{
			Title:   r.bag.Expand(a.Target.Window),
			Process: r.bag.Expand(a.Target.Process),
			Class:   r.bag.Expand(a.Target.Class),
		}
		exists = r.windowExists(wm)
	}
	observed := "window absent"
	if exists {
		observed = "window present"
	}
	return exists, fmt.Sprintf("%s is open", a.Target.Describe()), observed, nil
}

// checkElement verifies a UIA element's existence and (optionally) its state and
// value via the accessibility tree.
func (r *Runner) checkElement(a *session.Command) (bool, string, string, error) {
	w, err := r.uiaWindow(a.Target)
	if err != nil {
		return false, "", "", fmt.Errorf("uia host window: %w", err)
	}
	q := platform.UIAQuery{
		AutomationID: r.bag.Expand(a.UIA.AutomationID),
		Name:         r.bag.Expand(a.UIA.Name),
		ControlType:  a.UIA.ControlType,
	}
	st, err := r.drv.ElementState(w, q)
	if err != nil {
		return false, "", "", err
	}

	verdict := st.Found
	var checks []string

	if a.State != "" {
		checks = append(checks, a.State)
		if st.Found {
			switch strings.ToLower(a.State) {
			case "enabled":
				verdict = verdict && st.Enabled
			case "disabled":
				verdict = verdict && !st.Enabled
			case "selected":
				verdict = verdict && st.Selected
			case "checked":
				verdict = verdict && st.Toggle == platform.ToggleOn
			case "unchecked":
				verdict = verdict && st.Toggle == platform.ToggleOff
			}
		}
	}

	// Compare against the ValuePattern value, falling back to the Name.
	val := st.Value
	if val == "" {
		val = st.Name
	}
	if a.Equals != "" {
		eq := r.bag.Expand(a.Equals)
		checks = append(checks, fmt.Sprintf("value==%q", eq))
		if st.Found {
			verdict = verdict && val == eq
		}
	}
	if a.Contains != "" {
		sub := r.bag.Expand(a.Contains)
		checks = append(checks, fmt.Sprintf("value contains %q", sub))
		if st.Found {
			verdict = verdict && strings.Contains(val, sub)
		}
	}

	label := describeUIA(q)
	question := fmt.Sprintf("element %s exists", label)
	if len(checks) > 0 {
		question = fmt.Sprintf("element %s [%s]", label, strings.Join(checks, ", "))
	}
	observed := "not found"
	if st.Found {
		observed = fmt.Sprintf("found (value=%q name=%q enabled=%v selected=%v toggle=%d)",
			st.Value, st.Name, st.Enabled, st.Selected, st.Toggle)
	}
	return verdict, question, observed, nil
}

// checkDialog scans the app's own top-level windows for a dialog matching an
// optional title substring and a required set of button names. Specifying
// buttons (e.g. [OK] or [Yes, No]) is what distinguishes a real dialog from the
// main shell window when both share a title.
func (r *Runner) checkDialog(a *session.Command) (bool, string, string, error) {
	pid := r.queryPID()
	if pid == 0 {
		return false, "", "", fmt.Errorf("no app process to scan for dialogs")
	}
	wins, err := r.drv.AppWindows(pid)
	if err != nil {
		return false, "", "", err
	}

	titleWant := ""
	if a.Target != nil {
		titleWant = r.bag.Expand(a.Target.Window)
	}
	var buttons []string
	for _, b := range a.Buttons {
		buttons = append(buttons, r.bag.Expand(b))
	}

	matched := ""
	for _, w := range wins {
		if titleWant != "" && !strings.Contains(strings.ToLower(w.Title()), strings.ToLower(titleWant)) {
			continue
		}
		ok := true
		for _, b := range buttons {
			if _, e := r.drv.FindElement(w, platform.UIAQuery{Name: b, ControlType: "button"}); e != nil {
				ok = false
				break
			}
		}
		if ok {
			matched = w.Title()
			break
		}
	}

	desc := "dialog"
	if titleWant != "" {
		desc += fmt.Sprintf(" titled ~%q", titleWant)
	}
	if len(buttons) > 0 {
		desc += fmt.Sprintf(" with buttons %v", buttons)
	}
	observed := "no matching dialog"
	if matched != "" {
		observed = fmt.Sprintf("found dialog %q", matched)
	}
	return matched != "", desc + " is open", observed, nil
}
