package session

import (
	"bytes"
	"encoding/json"
	"sort"

	"github.com/invopop/jsonschema"
	orderedmap "github.com/pb33f/ordered-map/v2"
)

// GenerateSchema builds a JSON Schema (draft 2020-12) for TestSession.yaml.
//
// It is reflected from the Go types in this package, so the schema can never
// drift from what the runner actually accepts. Editors (VS Code's YAML
// extension and any other yaml-language-server client) consume it to provide
// autocomplete, hover descriptions, and inline validation. Per-action required
// fields, enums, and the polymorphic shapes (target, duration, string-or-list)
// are layered on via the JSONSchema/JSONSchemaExtend/GetFieldDocString hooks
// below.
func GenerateSchema() ([]byte, error) {
	r := &jsonschema.Reflector{
		// Property names come from yaml tags, not json.
		FieldNameTag: "yaml",
		// yaml tags carry no `omitempty`, so the default (require every
		// untagged field) would be wrong. Flip it: nothing is required unless a
		// struct's JSONSchemaExtend says so.
		RequiredFromJSONSchemaTags: true,
	}
	s := r.Reflect(&Session{})
	if s.Version == "" {
		s.Version = "https://json-schema.org/draft/2020-12/schema"
	}
	s.Title = "TestSession"
	s.Description = "AI-assisted UI test session (TestSession.yaml)."

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// --- small helpers -----------------------------------------------------------

func anyStrings(ss ...string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// oneKeyProps builds a single-property ordered map (used inside if-conditions).
func oneKeyProps(key string, schema *jsonschema.Schema) *orderedmap.OrderedMap[string, *jsonschema.Schema] {
	m := orderedmap.New[string, *jsonschema.Schema]()
	m.Set(key, schema)
	return m
}

func setEnum(s *jsonschema.Schema, prop string, values ...string) {
	if p, ok := s.Properties.Get(prop); ok {
		p.Enum = anyStrings(values...)
	}
}

// setExpect makes an `expect` property accept yes/no as strings or booleans.
// YAML 1.1 tooling coerces yes/no to booleans; YAML 1.2 keeps them as strings.
// The runner accepts both, so the schema does too.
func setExpect(s *jsonschema.Schema) {
	if p, ok := s.Properties.Get("expect"); ok {
		desc := p.Description
		p.Type = ""
		p.Enum = []any{"yes", "no", true, false}
		p.Description = desc
	}
}

// requireWhenAction returns an if/then asserting that when `action` is one of
// the given values, every field in `required` must be present.
func requireWhenAction(actions []string, required ...string) *jsonschema.Schema {
	return &jsonschema.Schema{
		If: &jsonschema.Schema{
			Required:   []string{"action"},
			Properties: oneKeyProps("action", &jsonschema.Schema{Enum: anyStrings(actions...)}),
		},
		Then: &jsonschema.Schema{Required: required},
	}
}

// actionEnum is the command catalog, derived from knownActions so the schema
// can never list an action the runner doesn't accept (and vice versa).
func actionEnum() []any {
	names := make([]string, 0, len(knownActions))
	for a := range knownActions {
		names = append(names, a)
	}
	sort.Strings(names)
	return anyStrings(names...)
}

// --- polymorphic scalar types ------------------------------------------------

// JSONSchema renders a Duration as either a number of milliseconds or a
// unit-bearing string ("500ms", "2s", "1m").
func (Duration) JSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		OneOf: []*jsonschema.Schema{
			{Type: "number", Description: "Milliseconds."},
			{Type: "string", Description: `Duration with a unit, e.g. "500ms", "2s", "1m", "1h30m".`},
		},
		Description: `A duration: a bare number (milliseconds) or a string with a unit ("500ms", "2s", "1m").`,
	}
}

// JSONSchema renders a StringList as a single string or an array of strings.
func (StringList) JSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		OneOf: []*jsonschema.Schema{
			{Type: "string"},
			{Type: "array", Items: &jsonschema.Schema{Type: "string"}},
		},
		Description: "A single string or a list of strings.",
	}
}

// --- Target: "screen" | point | window | rect --------------------------------

func (Target) JSONSchemaExtend(s *jsonschema.Schema) {
	// The `Screen bool` field has no yaml tag; the "screen" form is the string
	// branch below, so drop the leaked capitalized property.
	s.Properties.Delete("Screen")
	setEnum(s, "relativeTo", "window", "screen")
	obj := *s // the reflected mapping form (x/y/window/process/class/rect/...)
	obj.Description = "A point {x,y}, a window {window|process|class}, or a rectangle {rect}."
	*s = jsonschema.Schema{
		Description: "Where the action applies or what to capture.",
		OneOf: []*jsonschema.Schema{
			{Type: "string", Enum: anyStrings("screen"), Description: "The whole screen."},
			&obj,
		},
	}
}

func (Target) GetFieldDocString(field string) string {
	return map[string]string{
		"X":          "X coordinate of a point target.",
		"Y":          "Y coordinate of a point target.",
		"Window":     "Match a window by title (substring or regex).",
		"Process":    "Match a window by owning process name, e.g. \"notepad.exe\".",
		"Class":      "Match a window by its Win32 class name.",
		"Rect":       "A rectangular region {x, y, width, height}.",
		"RelativeTo": "Coordinate space for x/y: window (default) or screen.",
		"Raw":        "Treat coordinates as raw device pixels (skip DPI scaling).",
	}[field]
}

// --- Command -----------------------------------------------------------------

func (Command) JSONSchemaExtend(s *jsonschema.Schema) {
	s.Required = []string{"action"}
	if p, ok := s.Properties.Get("action"); ok {
		p.Enum = actionEnum()
		p.Description = "The command to perform."
	}
	setEnum(s, "provider", "claude", "codex", "cursor")
	setEnum(s, "button", "left", "right", "middle")
	setExpect(s)

	// Per-action required fields (mirror of the runner's validation rules).
	pointActions := []string{"mouse_move", "mouse_click", "mouse_down", "mouse_up", "mouse_scroll"}
	windowActions := []string{"focus_window", "close_window", "move_window", "resize_window"}
	s.AllOf = append(s.AllOf,
		requireWhenAction(pointActions, "target"),
		requireWhenAction(windowActions, "target"),
		requireWhenAction([]string{"mouse_drag"}, "from", "to"),
		requireWhenAction([]string{"type_text"}, "text"),
		requireWhenAction([]string{"key_press"}, "keys"),
		requireWhenAction([]string{"key_down", "key_up"}, "key"),
		requireWhenAction([]string{"launch_app"}, "path"),
		requireWhenAction([]string{"assert_ai"}, "question"),
		requireWhenAction([]string{"read_text_ai"}, "question", "store"),
	)
}

func (Command) GetFieldDocString(field string) string {
	return map[string]string{
		"Action":         "The command to perform (see the action enum).",
		"Target":         "Where the action applies (point, window, rect, or \"screen\").",
		"From":           "mouse_drag: the start point {x, y}.",
		"To":             "mouse_drag: the end point {x, y}.",
		"Button":         "Mouse button: left (default), right, or middle.",
		"Count":          "Click count (e.g. 2 for a double-click).",
		"DX":             "mouse_scroll: horizontal scroll amount.",
		"DY":             "mouse_scroll: vertical scroll amount (positive scrolls down).",
		"Text":           "type_text: the text to type.",
		"PerCharDelayMs": "type_text: delay between characters in ms (paces fast apps).",
		"Keys":           "key_press: a chord like \"Ctrl+S\" or a list of chords.",
		"Key":            "key_down/key_up: the single key to press or release.",
		"MS":             "wait: how long to wait (e.g. \"500ms\", \"2s\").",
		"ForAI":          "wait: wait until this condition holds instead of a fixed delay.",
		"Path":           "launch_app: path to the executable to launch.",
		"Args":           "launch_app: command-line arguments.",
		"WorkingDir":     "launch_app: working directory for the launched process.",
		"ReadyWhen":      "launch_app: how to know the app is ready.",
		"X":              "move_window: target X position.",
		"Y":              "move_window: target Y position.",
		"Width":          "resize_window: target width.",
		"Height":         "resize_window: target height.",
		"Save":           "screenshot: file name to save the capture as.",
		"Question":       "assert_ai/read_text_ai: the question for the AI engine.",
		"Expect":         "assert_ai: expected answer, yes or no.",
		"Store":          "read_text_ai: variable name to store the extracted text in.",
		"Provider":       "Override the AI provider for this command.",
		"Timeout":        "Override the AI timeout for this command.",
		"UIA":            "Locate the target control via UI Automation.",
		"Find":           "Locate the target via AI element search (Phase 3).",
		"WaitBefore":     "Precondition: wait until the target is ready before acting.",
		"Verify":         "Postcondition: confirm the action had its intended effect.",
		"ActionRetries":  "How many times to re-attempt the action if Verify fails.",
	}[field]
}

// --- Condition (waitBefore / verify / forAI) ---------------------------------

func (Condition) JSONSchemaExtend(s *jsonschema.Schema) {
	setExpect(s)
}

func (Condition) GetFieldDocString(field string) string {
	return map[string]string{
		"Window":    "A window must exist (or be gone, if window.gone is set).",
		"Changed":   "The target region must have changed since acting.",
		"Stable":    "The target region must be visually settled (stopped changing).",
		"UIA":       "A UI Automation element must be in the given state.",
		"Question":  "Ask the AI engine this yes/no question (most expensive rung).",
		"Expect":    "Expected AI answer: yes or no.",
		"Target":    "Region to evaluate; defaults to the action's target.",
		"PollEvery": "How often to re-check the condition.",
		"Timeout":   "Maximum time to wait for the condition to hold.",
	}[field]
}

// --- Step: machine is one command or a list of commands ----------------------

func (Step) JSONSchemaExtend(s *jsonschema.Schema) {
	s.Required = []string{"human", "machine"}
	if p, ok := s.Properties.Get("machine"); ok && p.Items != nil {
		item := p.Items // $ref to Command
		s.Properties.Set("machine", &jsonschema.Schema{
			Description: "A single command or a list of commands.",
			OneOf: []*jsonschema.Schema{
				item,
				{Type: "array", Items: item},
			},
		})
	}
}

func (Step) GetFieldDocString(field string) string {
	return map[string]string{
		"Human":             "Plain-language description of the step's intent.",
		"Machine":           "The command(s) that carry out the step.",
		"Timeout":           "Per-step timeout override.",
		"Retries":           "How many times to retry the whole step on failure.",
		"ContinueOnFailure": "If true, keep going even if this step fails.",
	}[field]
}

// --- Session / SessionInfo / Application / AI / TestCase / Validation --------

func (Session) JSONSchemaExtend(s *jsonschema.Schema) {
	s.Required = []string{"version", "session", "testCases"}
	if p, ok := s.Properties.Get("testCases"); ok {
		one := uint64(1)
		p.MinItems = &one
	}
}

func (SessionInfo) JSONSchemaExtend(s *jsonschema.Schema) {
	s.Required = []string{"application"}
}

func (Application) JSONSchemaExtend(s *jsonschema.Schema) {
	s.Required = []string{"path"}
	setEnum(s, "shutdown", "graceful", "force", "leaveOpen")
}

func (AI) JSONSchemaExtend(s *jsonschema.Schema) {
	setEnum(s, "provider", "claude", "codex", "cursor")
}

func (Settings) JSONSchemaExtend(s *jsonschema.Schema) {
	setEnum(s, "windowMatch", "title", "process", "class")
	setEnum(s, "coordinateSpace", "window", "screen")
}

func (TestCase) JSONSchemaExtend(s *jsonschema.Schema) {
	s.Required = []string{"id", "name", "steps"}
	if p, ok := s.Properties.Get("steps"); ok {
		one := uint64(1)
		p.MinItems = &one
	}
}

func (Validation) JSONSchemaExtend(s *jsonschema.Schema) {
	s.Required = []string{"human", "assert"}
}
