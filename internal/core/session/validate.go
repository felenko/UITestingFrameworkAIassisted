package session

import (
	"fmt"
	"strings"
)

// knownActions is the command catalog (docs/02 §3).
var knownActions = map[string]bool{
	// mouse
	"mouse_move": true, "mouse_click": true, "mouse_down": true,
	"mouse_up": true, "mouse_drag": true, "mouse_scroll": true,
	// keyboard
	"type_text": true, "key_press": true, "key_down": true, "key_up": true,
	// app & window
	"launch_app": true, "focus_window": true, "close_window": true,
	"close_popup": true, "move_window": true, "resize_window": true, "wait": true,
	// observation
	"screenshot": true, "assert_ai": true, "read_text_ai": true,
	// deterministic assertions (no AI)
	"assert_window": true, "assert_element": true, "assert_dialog": true,
}

// elementStates are the accepted values for assert_element's `state` field.
var elementStates = map[string]bool{
	"enabled": true, "disabled": true, "selected": true,
	"checked": true, "unchecked": true,
}

var knownProviders = map[string]bool{"claude": true, "codex": true, "cursor": true}

// ValidationError aggregates all problems found in a session file so the user
// sees everything at once.
type ValidationError struct {
	Problems []string
}

func (e *ValidationError) Error() string {
	if len(e.Problems) == 1 {
		return e.Problems[0]
	}
	return fmt.Sprintf("%d validation errors:\n  - %s",
		len(e.Problems), strings.Join(e.Problems, "\n  - "))
}

// Validate checks a session against the rules in docs/03 §9.
func Validate(s *Session) error {
	v := &validator{}

	if s.Version == 0 {
		v.add("version is required")
	} else if s.Version != SupportedVersion {
		v.add("unsupported version %d (this runner supports version %d)", s.Version, SupportedVersion)
	}

	if strings.TrimSpace(s.Session.Application.Path) == "" {
		v.add("session.application.path is required")
	}

	if ai := s.Session.AI; ai.Provider != "" && !knownProviders[ai.Provider] {
		v.add("session.ai.provider %q is not a known provider (claude|codex|cursor)", ai.Provider)
	}

	if len(s.TestCases) == 0 {
		v.add("at least one testCase is required")
	}

	// Session-level lifecycle hooks (best-effort bootstrap steps).
	v.checkSteps("session.setup", s.Session.Setup)
	v.checkSteps("session.beforeEach", s.Session.BeforeEach)
	v.checkSteps("session.afterEach", s.Session.AfterEach)
	v.checkSteps("session.recoverSteps", s.Session.RecoverSteps)

	seen := map[string]bool{}
	for i := range s.TestCases {
		tc := &s.TestCases[i]
		where := fmt.Sprintf("testCase[%d]", i)
		if tc.ID != "" {
			where = tc.ID
		}

		if strings.TrimSpace(tc.ID) == "" {
			v.add("%s: id is required", where)
		} else if seen[tc.ID] {
			v.add("duplicate testCase id %q", tc.ID)
		}
		seen[tc.ID] = true

		if strings.TrimSpace(tc.Name) == "" {
			v.add("%s: name is required", where)
		}
		if len(tc.Steps) == 0 {
			v.add("%s: steps is required and must be non-empty", where)
		}

		v.checkSteps(where+".setup", tc.Setup)
		v.checkSteps(where+".steps", tc.Steps)
		v.checkSteps(where+".teardown", tc.Teardown)
		v.checkSteps(where+".cleanup", tc.Cleanup)
		v.checkValidation(where, &tc.Validation)
	}

	if len(v.problems) > 0 {
		return &ValidationError{Problems: v.problems}
	}
	return nil
}

type validator struct{ problems []string }

func (v *validator) add(format string, args ...any) {
	v.problems = append(v.problems, fmt.Sprintf(format, args...))
}

func (v *validator) checkSteps(where string, steps []Step) {
	for i := range steps {
		st := &steps[i]
		sw := fmt.Sprintf("%s[%d]", where, i)
		if strings.TrimSpace(st.Human) == "" {
			v.add("%s: human is required", sw)
		}
		if len(st.Machine) == 0 {
			v.add("%s: machine is required (a command or list of commands)", sw)
		}
		for j := range st.Machine {
			v.checkCommand(fmt.Sprintf("%s.machine[%d]", sw, j), &st.Machine[j])
		}
	}
}

func (v *validator) checkValidation(where string, val *Validation) {
	if strings.TrimSpace(val.Human) == "" {
		v.add("%s.validation: human (acceptance criteria) is required", where)
	}
	if len(val.Assert) == 0 {
		v.add("%s.validation: assert must contain at least one entry", where)
		return
	}
	for j := range val.Assert {
		v.checkCommand(fmt.Sprintf("%s.validation.assert[%d]", where, j), &val.Assert[j])
	}
}

func (v *validator) checkCommand(where string, c *Command) {
	if strings.TrimSpace(c.Action) == "" {
		v.add("%s: action is required", where)
		return
	}
	if !knownActions[c.Action] {
		v.add("%s: unknown action %q", where, c.Action)
		return
	}

	if c.Provider != "" && !knownProviders[c.Provider] {
		v.add("%s: provider %q is not a known provider", where, c.Provider)
	}

	if c.WaitBefore != nil {
		v.checkCondition(where+".waitBefore", c.WaitBefore)
	}
	if c.Verify != nil {
		v.checkCondition(where+".verify", c.Verify)
	}

	switch c.Action {
	case "mouse_move", "mouse_click", "mouse_down", "mouse_up", "mouse_scroll":
		// A uia: selector (Phase 2) or find: (Phase 3) resolves to a screen point
		// at run time, so it stands in for an explicit { x, y } target.
		hasResolver := (c.UIA != nil && !c.UIA.IsZero()) || c.Find != ""
		if !hasResolver && !c.Target.IsPoint() {
			v.add("%s: %s requires a point target { x, y } (or a uia:/find: selector)", where, c.Action)
		}
		if c.Action == "mouse_click" {
			v.checkButton(where, c.Button)
		}
	case "mouse_drag":
		if !c.From.IsPoint() {
			v.add("%s: mouse_drag requires a point `from`", where)
		}
		if !c.To.IsPoint() {
			v.add("%s: mouse_drag requires a point `to`", where)
		}
	case "type_text":
		if c.Text == "" {
			v.add("%s: type_text requires text", where)
		}
	case "key_press":
		if len(c.Keys) == 0 {
			v.add("%s: key_press requires keys", where)
		}
	case "key_down", "key_up":
		if strings.TrimSpace(c.Key) == "" {
			v.add("%s: %s requires key", where, c.Action)
		}
	case "launch_app":
		if strings.TrimSpace(c.Path) == "" {
			v.add("%s: launch_app requires path", where)
		}
	case "focus_window", "close_window", "move_window", "resize_window":
		if c.Target.IsZero() || (c.Target.Window == "" && c.Target.Process == "" && c.Target.Class == "") {
			v.add("%s: %s requires a window target", where, c.Action)
		}
	case "wait":
		if c.MS.Duration == 0 && c.ForAI == nil {
			v.add("%s: wait requires `ms` or `forAI`", where)
		}
		if c.ForAI != nil {
			v.checkCondition(where+".forAI", c.ForAI)
		}
	case "assert_ai":
		if strings.TrimSpace(c.Question) == "" {
			v.add("%s: assert_ai requires a question", where)
		}
		v.checkExpect(where, c.Expect)
	case "read_text_ai":
		if strings.TrimSpace(c.Question) == "" {
			v.add("%s: read_text_ai requires a question", where)
		}
		if strings.TrimSpace(c.Store) == "" {
			v.add("%s: read_text_ai requires `store`", where)
		}
	case "assert_window":
		if c.Target.IsZero() || (c.Target.Window == "" && c.Target.Process == "" && c.Target.Class == "") {
			v.add("%s: assert_window requires a window target { window|process|class }", where)
		}
		v.checkExpect(where, c.Expect)
	case "assert_element":
		if c.UIA == nil || c.UIA.IsZero() {
			v.add("%s: assert_element requires a `uia` selector { automationId|name|controlType }", where)
		}
		if c.State != "" && !elementStates[strings.ToLower(c.State)] {
			v.add("%s: assert_element state %q must be enabled|disabled|selected|checked|unchecked", where, c.State)
		}
		v.checkExpect(where, c.Expect)
	case "assert_dialog":
		hasTitle := c.Target != nil && c.Target.Window != ""
		if !hasTitle && len(c.Buttons) == 0 {
			v.add("%s: assert_dialog requires a dialog title (target.window) and/or `buttons`", where)
		}
		v.checkExpect(where, c.Expect)
	}
}

// checkCondition validates a waitBefore/verify/forAI condition: it must declare
// at least one rung, and any AI rung needs a valid expect.
func (v *validator) checkCondition(where string, c *Condition) {
	if c.IsZero() {
		v.add("%s: a condition needs at least one of window|changed|stable|uia|question", where)
		return
	}
	if c.Question != "" {
		v.checkExpect(where, c.Expect)
	}
}

func (v *validator) checkButton(where, button string) {
	switch button {
	case "", "left", "right", "middle":
	default:
		v.add("%s: button %q must be left|right|middle", where, button)
	}
}

func (v *validator) checkExpect(where, expect string) {
	switch strings.ToLower(expect) {
	case "", "yes", "no", "true", "false":
	default:
		v.add("%s: expect %q must be yes|no", where, expect)
	}
}
