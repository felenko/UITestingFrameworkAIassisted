package session

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// StringList accepts either a single scalar string or a YAML sequence of
// strings. Used for `keys` (a chord or a sequence of chords) and `args`.
type StringList []string

func (s *StringList) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		*s = StringList{node.Value}
		return nil
	case yaml.SequenceNode:
		var items []string
		if err := node.Decode(&items); err != nil {
			return err
		}
		*s = items
		return nil
	default:
		return fmt.Errorf("expected string or list of strings, got %v", node.Kind)
	}
}

// Rect is a rectangle in logical pixels.
type Rect struct {
	X      int `yaml:"x"`
	Y      int `yaml:"y"`
	Width  int `yaml:"width"`
	Height int `yaml:"height"`
}

// Target specifies where an action applies or what to capture. In YAML it is
// either the bare string `screen`, or a mapping describing a point, a window,
// or a rectangle (docs/02 §5, docs/03 §6).
type Target struct {
	Screen bool // target: screen

	// Point form: { x, y }
	X *int `yaml:"x"`
	Y *int `yaml:"y"`

	// Window form: { window, process, class }
	Window  string `yaml:"window"`
	Process string `yaml:"process"`
	Class   string `yaml:"class"`

	// Rectangle form: { rect: {x,y,width,height} }
	Rect *Rect `yaml:"rect"`

	// Coordinate space + DPI handling.
	RelativeTo string `yaml:"relativeTo"` // window | screen
	Raw        bool   `yaml:"raw"`

	// Exact requires the matched window to belong to the app under test. When
	// set, a title/class match owned by a foreign process (e.g. a File Explorer
	// whose title merely contains the app name) is rejected and NO app-window
	// fallback is used — so the action cleanly errors instead of acting on the
	// wrong window. Useful for best-effort hooks that must no-op when the target
	// window genuinely does not exist yet.
	Exact bool `yaml:"exact"`
}

// targetAlias avoids infinite recursion in UnmarshalYAML.
type targetAlias Target

func (t *Target) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		if node.Value == "screen" {
			t.Screen = true
			return nil
		}
		return fmt.Errorf("unknown target %q (expected 'screen' or a mapping)", node.Value)
	}
	var a targetAlias
	if err := node.Decode(&a); err != nil {
		return err
	}
	*t = Target(a)
	return nil
}

// IsPoint reports whether the target denotes a single coordinate.
func (t *Target) IsPoint() bool { return t != nil && t.X != nil && t.Y != nil }

// IsZero reports whether no target form was provided.
func (t *Target) IsZero() bool {
	if t == nil {
		return true
	}
	return !t.Screen && t.X == nil && t.Y == nil && t.Window == "" &&
		t.Process == "" && t.Class == "" && t.Rect == nil
}

// Describe returns a short human label for logs/reports.
func (t *Target) Describe() string {
	switch {
	case t == nil || t.IsZero():
		return "screen"
	case t.Screen:
		return "screen"
	case t.IsPoint():
		return fmt.Sprintf("point(%d,%d)", *t.X, *t.Y)
	case t.Rect != nil:
		return fmt.Sprintf("rect(%d,%d %dx%d)", t.Rect.X, t.Rect.Y, t.Rect.Width, t.Rect.Height)
	case t.Window != "":
		return fmt.Sprintf("window %q", t.Window)
	case t.Process != "":
		return fmt.Sprintf("process %q", t.Process)
	case t.Class != "":
		return fmt.Sprintf("class %q", t.Class)
	default:
		return "screen"
	}
}

// WindowMatch describes how to find a window by title/process/class. Used by
// `readyWhen.window` (where the YAML nests `{ title: ... }`).
type WindowMatch struct {
	Title   string `yaml:"title"`
	Process string `yaml:"process"`
	Class   string `yaml:"class"`
	Gone    bool   `yaml:"gone"` // condition is satisfied when the window is ABSENT
}

// UIAQuery locates a control via the UI Automation tree (Phase 2). Used for
// `uia:` targeting and inside a UIA condition.
type UIAQuery struct {
	AutomationID string `yaml:"automationId"`
	Name         string `yaml:"name"`
	ControlType  string `yaml:"controlType"`
}

// IsZero reports whether no UIA selector was provided.
func (q *UIAQuery) IsZero() bool {
	return q == nil || (q.AutomationID == "" && q.Name == "" && q.ControlType == "")
}

// UIACondition checks a UIA element's existence/state (Phase 2).
type UIACondition struct {
	UIAQuery `yaml:",inline"`
	State    string `yaml:"state"` // exists | enabled | selected
	Value    string `yaml:"value"` // element value must equal this
}

// Condition is a cost-ordered, ladder-aware check used by `waitBefore`,
// `verify`, `wait.forAI`, and `readyWhen.forAI`. All present rungs must hold;
// the runner evaluates the cheapest rungs first and only invokes the AI
// (`question`) when it is the declared check or as the escalation rung.
type Condition struct {
	// Cheap rungs (no AI cost).
	Window  *WindowMatch  `yaml:"window"`  // a window must exist (or be gone)
	Changed bool          `yaml:"changed"` // the target region must have changed since acting
	Stable  bool          `yaml:"stable"`  // the target region must be visually settled
	UIA     *UIACondition `yaml:"uia"`     // a UIA element state (Phase 2)

	// Rich rung (AI vision).
	Question string `yaml:"question"`
	Expect   string `yaml:"expect"`

	// Shared.
	Target    *Target  `yaml:"target"`
	PollEvery Duration `yaml:"pollEvery"`
	Timeout   Duration `yaml:"timeout"`
}

// IsZero reports whether the condition declares no rung.
func (c *Condition) IsZero() bool {
	if c == nil {
		return true
	}
	return c.Window == nil && !c.Changed && !c.Stable && c.UIA == nil && c.Question == ""
}

// ForAI is the AI polling/extraction sub-spec used by `wait.forAI` and
// `readyWhen.forAI`. It is an alias of Condition so those keep working while
// gaining the cheaper rungs.
type ForAI = Condition

// ReadyWhen describes how the runner knows the app is ready.
type ReadyWhen struct {
	Window *WindowMatch `yaml:"window"`
	Delay  Duration     `yaml:"delay"`
	ForAI  *ForAI       `yaml:"forAI"`
}
