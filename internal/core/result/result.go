// Package result defines the machine-readable results model (results.json,
// docs/05 §5) that the runner produces and the reporter renders. Every assert
// always carries an `expected` and `actual` block (the report invariant).
package result

import "time"

// Status is the outcome of a unit of work.
type Status string

const (
	StatusPending Status = "pending"
	StatusRunning Status = "running"
	StatusPassed  Status = "passed"
	StatusFailed  Status = "failed"
	StatusError   Status = "error"
	StatusSkipped Status = "skipped"
)

// Results is the root document.
type Results struct {
	Session       string      `json:"session"`
	Frontend      string      `json:"frontend"` // cli | gui
	RunnerVersion string      `json:"runnerVersion"`
	Environment   Environment `json:"environment"`
	StartedAt     time.Time   `json:"startedAt"`
	FinishedAt    time.Time   `json:"finishedAt"`
	Provider      string      `json:"provider"`
	Model         string      `json:"model,omitempty"`
	Application   string      `json:"application,omitempty"`
	Summary       Summary     `json:"summary"`
	Cases         []Case      `json:"cases"`

	// UnverifiedBaselines is set when any assert used a candidate expected.
	UnverifiedBaselines bool `json:"unverifiedBaselines"`
}

// Environment captures where the run happened.
type Environment struct {
	OS     string `json:"os"`
	Screen string `json:"screen"`
}

// Summary holds rollup counts.
type Summary struct {
	Total   int `json:"total"`
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Errors  int `json:"errors"`
	Skipped int `json:"skipped"`
}

// Case is one test case result.
type Case struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Tags        []string   `json:"tags,omitempty"`
	Status      Status     `json:"status"`
	DurationMs  int64      `json:"durationMs"`
	Setup       []Step     `json:"setup,omitempty"`
	Steps       []Step     `json:"steps"`
	Validation  Validation `json:"validation"`
	Teardown    []Step     `json:"teardown,omitempty"`
	Error       string     `json:"error,omitempty"`
}

// Step is one step result (act phase).
type Step struct {
	Index       int       `json:"index"`
	Phase       string    `json:"phase"` // setup | steps | teardown
	Human       string    `json:"human"`
	Machine     []Machine `json:"machine"`
	Screenshots []string  `json:"screenshots,omitempty"`
	Status      Status    `json:"status"`
	DurationMs  int64     `json:"durationMs"`
	Error       string    `json:"error,omitempty"`
}

// Machine is one executed command within a step.
type Machine struct {
	Action     string `json:"action"`
	Summary    string `json:"summary,omitempty"`
	Status     Status `json:"status"`
	DurationMs int64  `json:"durationMs"`
	Error      string `json:"error,omitempty"`
	Attempts   int    `json:"attempts,omitempty"`   // action attempts made (1 = first try worked)
	Diagnosis  string `json:"diagnosis,omitempty"`  // AI diagnosis captured after exhausted retries
}

// Validation is the check phase result.
type Validation struct {
	Human  string   `json:"human"`
	Status Status   `json:"status"`
	Assert []Assert `json:"assert"`
}

// Assert is one assertion result. Always carries Expected + Actual.
type Assert struct {
	ID        string   `json:"id"`
	Human     string   `json:"human,omitempty"`
	Action    string   `json:"action"`
	Question  string   `json:"question"`
	Expect    string   `json:"expect"`
	Provider  string   `json:"provider"`
	Model     string   `json:"model,omitempty"`
	RawAnswer string   `json:"rawAnswer"`
	Verdict   bool     `json:"verdict"`
	Status    Status   `json:"status"`
	Error     string   `json:"error,omitempty"`
	Samples   int      `json:"samples,omitempty"`
	Retries   int      `json:"retries,omitempty"`
	Expected  Expected `json:"expected"`
	Actual    Actual   `json:"actual"`
}

// Expected is the "what should be" image and its provenance.
type Expected struct {
	Source string `json:"source"` // declared | approved | candidate
	Image  string `json:"image"`
}

// Actual is the "what actually is" image captured at assertion time.
type Actual struct {
	Image      string    `json:"image"`
	CapturedAt time.Time `json:"capturedAt"`
}
