package session

import (
	"path/filepath"
	"testing"
	"time"
)

func TestParseExample(t *testing.T) {
	path := filepath.Join("..", "..", "..", "examples", "TestSessionCases.yaml")
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.Version != 1 {
		t.Errorf("version = %d, want 1", s.Version)
	}
	if len(s.TestCases) != 3 {
		t.Fatalf("got %d cases, want 3", len(s.TestCases))
	}

	// Defaults applied.
	if s.Session.AI.Provider != "claude" {
		t.Errorf("provider = %q, want claude", s.Session.AI.Provider)
	}

	tc1 := s.TestCases[0]
	// Single-command machine sections parse to a one-element slice.
	if got := len(tc1.Steps[0].Machine); got != 1 {
		t.Errorf("TC-001 step0 machine len = %d, want 1", got)
	}
	if tc1.Steps[0].Machine[0].Action != "focus_window" {
		t.Errorf("unexpected action %q", tc1.Steps[0].Machine[0].Action)
	}
	// Variable interpolation source preserved (expanded at run time).
	if tc1.Steps[1].Machine[0].Text != "${greeting}" {
		t.Errorf("text = %q", tc1.Steps[1].Machine[0].Text)
	}

	// Validation: first assert expect yes, second expect no, target screen.
	asserts := tc1.Validation.Assert
	if len(asserts) != 2 {
		t.Fatalf("TC-001 asserts = %d, want 2", len(asserts))
	}
	if asserts[0].Expect != "yes" {
		t.Errorf("assert0 expect = %q, want yes", asserts[0].Expect)
	}
	if asserts[1].Expect != "no" {
		t.Errorf("assert1 expect = %q, want no", asserts[1].Expect)
	}
	if asserts[1].Target == nil || !asserts[1].Target.Screen {
		t.Errorf("assert1 target should be screen")
	}

	// TC-002 has a multi-command machine list.
	tc2 := s.TestCases[1]
	if got := len(tc2.Steps[0].Machine); got != 3 {
		t.Errorf("TC-002 step0 machine len = %d, want 3", got)
	}
}

func TestValidateErrors(t *testing.T) {
	s := &Session{
		Version: 1,
		Session: SessionInfo{Application: Application{Path: ""}},
		TestCases: []TestCase{
			{ID: "A", Name: "x", Steps: []Step{{Human: "h", Machine: []Command{{Action: "bogus"}}}},
				Validation: Validation{Human: "ok", Assert: []Command{{Action: "assert_ai"}}}},
			{ID: "A"}, // duplicate id, missing name/steps
		},
	}
	err := Validate(s)
	if err == nil {
		t.Fatal("expected validation errors")
	}
	ve, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("error type = %T", err)
	}
	// Expect: missing app path, unknown action, assert_ai missing question,
	// duplicate id, missing name, missing steps, missing validation.
	if len(ve.Problems) < 5 {
		t.Errorf("got %d problems, want several:\n%v", len(ve.Problems), ve.Problems)
	}
}

func TestParseDuration(t *testing.T) {
	cases := map[string]time.Duration{
		"500ms": 500 * time.Millisecond,
		"2s":    2 * time.Second,
		"1m":    time.Minute,
		"2000":  2 * time.Second, // bare number = ms
	}
	for in, want := range cases {
		got, err := ParseDuration(in)
		if err != nil {
			t.Errorf("ParseDuration(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseDuration(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestTargetScreenScalar(t *testing.T) {
	s, err := Load(filepath.Join("..", "..", "..", "examples", "TestSessionCases.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// TC-001 assert[1] uses `target: screen` (a scalar).
	tgt := s.TestCases[0].Validation.Assert[1].Target
	if tgt == nil || !tgt.Screen {
		t.Errorf("expected screen target, got %+v", tgt)
	}
}
