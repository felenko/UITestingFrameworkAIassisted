// Package session defines the TestSession.yaml data model (docs/03), plus
// loading, default-resolution, and validation of session files.
package session

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// Session is the root of a TestSession.yaml file.
type Session struct {
	Version   int               `yaml:"version"`
	Session   SessionInfo       `yaml:"session"`
	Variables map[string]string `yaml:"variables"`
	TestCases []TestCase        `yaml:"testCases"`

	// SourcePath is the file the session was loaded from (set by Load).
	SourcePath string `yaml:"-"`
}

// SessionInfo holds the app-under-test and global settings.
type SessionInfo struct {
	Name        string      `yaml:"name"`
	Description string      `yaml:"description"`
	Application Application `yaml:"application"`
	AI          AI          `yaml:"ai"`
	Settings    Settings    `yaml:"settings"`
}

// Application describes the program under test.
type Application struct {
	Path           string     `yaml:"path"`
	Args           StringList `yaml:"args"`
	WorkingDir     string     `yaml:"workingDir"`
	StartupTimeout Duration   `yaml:"startupTimeout"`
	ReadyWhen      *ReadyWhen `yaml:"readyWhen"`
	Shutdown       string     `yaml:"shutdown"` // graceful | force | leaveOpen
}

// AI holds the assertion-engine provider configuration.
type AI struct {
	Provider string   `yaml:"provider"` // claude | codex | cursor
	Model    string   `yaml:"model"`
	Timeout  Duration `yaml:"timeout"`
	Retries  int      `yaml:"retries"`
	Samples  int      `yaml:"samples"`
}

// Settings holds global runner settings.
type Settings struct {
	OutDir              string   `yaml:"outDir"`
	DefaultStepTimeout  Duration `yaml:"defaultStepTimeout"`
	FailFast            bool     `yaml:"failFast"`
	ScreenshotOnFailure bool     `yaml:"screenshotOnFailure"`
	WindowMatch         string   `yaml:"windowMatch"`     // title | process | class
	CoordinateSpace     string   `yaml:"coordinateSpace"` // window | screen
	DPIAware            bool     `yaml:"dpiAware"`

	// --- self-correcting actuation defaults ---
	AutoSettle           *bool    `yaml:"autoSettle"`           // settle + auto-verify each action (default true)
	SettleTimeout        Duration `yaml:"settleTimeout"`        // max wait for readiness/verify (default 5s)
	SettleInterval       Duration `yaml:"settleInterval"`       // poll / stability interval (default 250ms)
	DefaultActionRetries int      `yaml:"defaultActionRetries"` // action re-attempts when verify fails (default 2)
	AIEscalation         *bool    `yaml:"aiEscalation"`         // AI diagnosis when cheap retries exhaust (default true)
}

// TestCase is a named scenario: ordered steps plus a final validation.
type TestCase struct {
	ID          string     `yaml:"id"`
	Name        string     `yaml:"name"`
	Description string     `yaml:"description"`
	Folder      string     `yaml:"folder"` // optional "/"-separated group path for organizing cases
	Tags        StringList `yaml:"tags"`
	FailFast    *bool      `yaml:"failFast"`
	Retries     int        `yaml:"retries"`

	Setup      []Step     `yaml:"setup"`
	Steps      []Step     `yaml:"steps"`
	Validation Validation `yaml:"validation"`
	Teardown   []Step     `yaml:"teardown"`
}

// Step is the two-faced unit: a `human` intent plus one or more `machine`
// commands.
type Step struct {
	Human   string    `yaml:"human"`
	Machine []Command `yaml:"machine"`

	Timeout           Duration `yaml:"timeout"`
	Retries           int      `yaml:"retries"`
	ContinueOnFailure bool     `yaml:"continueOnFailure"`
}

// stepAlias mirrors Step but with a raw machine node so we can accept both a
// single command mapping and a list of commands.
type stepAlias struct {
	Human             string    `yaml:"human"`
	Machine           yaml.Node `yaml:"machine"`
	Timeout           Duration  `yaml:"timeout"`
	Retries           int       `yaml:"retries"`
	ContinueOnFailure bool      `yaml:"continueOnFailure"`
}

func (s *Step) UnmarshalYAML(node *yaml.Node) error {
	var a stepAlias
	if err := node.Decode(&a); err != nil {
		return err
	}
	s.Human = a.Human
	s.Timeout = a.Timeout
	s.Retries = a.Retries
	s.ContinueOnFailure = a.ContinueOnFailure

	cmds, err := decodeCommands(&a.Machine)
	if err != nil {
		return fmt.Errorf("step %q: %w", a.Human, err)
	}
	s.Machine = cmds
	return nil
}

// decodeCommands accepts a single mapping or a sequence of mappings.
func decodeCommands(node *yaml.Node) ([]Command, error) {
	if node == nil || node.Kind == 0 {
		return nil, nil
	}
	switch node.Kind {
	case yaml.MappingNode:
		var c Command
		if err := node.Decode(&c); err != nil {
			return nil, err
		}
		return []Command{c}, nil
	case yaml.SequenceNode:
		var cs []Command
		if err := node.Decode(&cs); err != nil {
			return nil, err
		}
		return cs, nil
	default:
		return nil, fmt.Errorf("machine must be a command or a list of commands")
	}
}

// Validation is the pass/fail decision for a case.
type Validation struct {
	Human  string    `yaml:"human"`
	Assert []Command `yaml:"assert"`
}

// Command is a single action with all possible arguments across the catalog
// (docs/02 §3). Only the fields relevant to `action` are used.
type Command struct {
	Action string `yaml:"action"`

	// Targets / geometry.
	Target *Target `yaml:"target"`
	From   *Target `yaml:"from"`
	To     *Target `yaml:"to"`

	// Mouse.
	Button string `yaml:"button"` // left | right | middle
	Count  int    `yaml:"count"`
	DX     int    `yaml:"dx"`
	DY     int    `yaml:"dy"`

	// Keyboard.
	Text           string     `yaml:"text"`
	PerCharDelayMs int        `yaml:"perCharDelayMs"`
	Keys           StringList `yaml:"keys"`
	Key            string     `yaml:"key"`

	// Wait.
	MS    Duration `yaml:"ms"`
	ForAI *ForAI   `yaml:"forAI"`

	// launch_app / window.
	Path       string     `yaml:"path"`
	Args       StringList `yaml:"args"`
	WorkingDir string     `yaml:"workingDir"`
	ReadyWhen  *ReadyWhen `yaml:"readyWhen"`
	X          int        `yaml:"x"`
	Y          int        `yaml:"y"`
	Width      int        `yaml:"width"`
	Height     int        `yaml:"height"`

	// Observation / assertion.
	Save     string `yaml:"save"`
	Question string `yaml:"question"`
	Expect   string `yaml:"expect"` // yes | no
	Store    string `yaml:"store"`
	Baseline string `yaml:"baseline"`

	// Identity / labelling (used mostly on assert entries).
	ID    string `yaml:"id"`
	Human string `yaml:"human"`

	// Per-command overrides.
	Provider string    `yaml:"provider"`
	Timeout  Duration  `yaml:"timeout"`
	Retries  *int      `yaml:"retries"`
	Samples  *int      `yaml:"samples"`

	// --- self-correcting actuation (cost-ordered escalation) ---
	UIA           *UIAQuery  `yaml:"uia"`           // locate via UI Automation (Phase 2)
	Find          string     `yaml:"find"`          // locate via AI element search (Phase 3)
	WaitBefore    *Condition `yaml:"waitBefore"`    // precondition: target ready before acting
	Verify        *Condition `yaml:"verify"`        // postcondition: the action had its effect
	ActionRetries *int       `yaml:"actionRetries"` // re-attempts of the action when Verify fails
}
