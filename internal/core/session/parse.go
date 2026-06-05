package session

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// SupportedVersion is the only schema version this runner understands.
const SupportedVersion = 1

// Defaults applied when the session omits a value (docs/03 §2).
const (
	DefaultProvider       = "claude"
	DefaultAITimeout      = 30 * time.Second
	DefaultAIRetries      = 1
	DefaultAISamples      = 1
	DefaultStartupTimeout = 10 * time.Second
	DefaultStepTimeout    = 15 * time.Second
	DefaultOutDir         = "./test-results"
	DefaultShutdown       = "graceful"
	DefaultWindowMatch    = "title"
	DefaultCoordSpace     = "window"
	DefaultSettleTimeout  = 5 * time.Second
	DefaultSettleInterval = 250 * time.Millisecond
	DefaultActionRetries  = 2
)

func boolPtr(b bool) *bool { return &b }

// Load reads, parses, applies defaults, and validates a session file.
func Load(path string) (*Session, error) {
	s, err := Parse(path)
	if err != nil {
		return nil, err
	}
	if err := Validate(s); err != nil {
		return nil, err
	}
	return s, nil
}

// Parse reads + unmarshals + applies defaults, without validating.
func Parse(path string) (*Session, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading session file: %w", err)
	}
	var s Session
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	s.SourcePath = path
	s.applyDefaults()
	if err := s.mergeCredentials(); err != nil {
		return nil, err
	}
	return &s, nil
}

// applyDefaults fills in unspecified values with their documented defaults.
func (s *Session) applyDefaults() {
	ai := &s.Session.AI
	if ai.Provider == "" {
		ai.Provider = DefaultProvider
	}
	if ai.Timeout.Duration == 0 {
		ai.Timeout = D(DefaultAITimeout)
	}
	if ai.Retries == 0 {
		ai.Retries = DefaultAIRetries
	}
	if ai.Samples == 0 {
		ai.Samples = DefaultAISamples
	}

	app := &s.Session.Application
	if app.StartupTimeout.Duration == 0 {
		app.StartupTimeout = D(DefaultStartupTimeout)
	}
	if app.Shutdown == "" {
		app.Shutdown = DefaultShutdown
	}

	set := &s.Session.Settings
	if set.OutDir == "" {
		set.OutDir = DefaultOutDir
	}
	if set.DefaultStepTimeout.Duration == 0 {
		set.DefaultStepTimeout = D(DefaultStepTimeout)
	}
	if set.WindowMatch == "" {
		set.WindowMatch = DefaultWindowMatch
	}
	if set.CoordinateSpace == "" {
		set.CoordinateSpace = DefaultCoordSpace
	}
	if set.AutoSettle == nil {
		set.AutoSettle = boolPtr(true)
	}
	if set.AIEscalation == nil {
		set.AIEscalation = boolPtr(true)
	}
	if set.SettleTimeout.Duration == 0 {
		set.SettleTimeout = D(DefaultSettleTimeout)
	}
	if set.SettleInterval.Duration == 0 {
		set.SettleInterval = D(DefaultSettleInterval)
	}
	if set.DefaultActionRetries == 0 {
		set.DefaultActionRetries = DefaultActionRetries
	}

	for i := range s.TestCases {
		tc := &s.TestCases[i]
		normalizeSteps(tc.Setup)
		normalizeSteps(tc.Steps)
		normalizeSteps(tc.Teardown)
		for j := range tc.Validation.Assert {
			normalizeCommand(&tc.Validation.Assert[j])
		}
	}
}

func normalizeSteps(steps []Step) {
	for i := range steps {
		for j := range steps[i].Machine {
			normalizeCommand(&steps[i].Machine[j])
		}
	}
}

// normalizeCommand fills per-command defaults that the runner relies on.
func normalizeCommand(c *Command) {
	switch c.Action {
	case "mouse_click":
		if c.Count == 0 {
			c.Count = 1
		}
		if c.Button == "" {
			c.Button = "left"
		}
	case "mouse_down", "mouse_up", "mouse_drag":
		if c.Button == "" {
			c.Button = "left"
		}
	case "assert_ai", "read_text_ai":
		if c.Expect == "" {
			c.Expect = "yes"
		}
	}
}
