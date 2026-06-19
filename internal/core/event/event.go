// Package event is the runner's progress event bus (docs/01 §4, docs/04 §4).
// The CLI renders events as log lines; the GUI renders them live. The bus is
// a simple synchronous fan-out to registered subscribers.
package event

import "sync"

// Type enumerates the event kinds pushed by the core.
type Type string

const (
	RunStarted         Type = "run.started"
	CaseStarted        Type = "case.started"
	CaseFinished       Type = "case.finished"
	StepStarted        Type = "step.started"
	StepFinished       Type = "step.finished"
	ScreenshotCaptured Type = "screenshot.captured"
	AssertFinished     Type = "assert.finished"
	RunFinished        Type = "run.finished"
	Log                Type = "log"

	// Debug / recording events (GUI debug mode).
	StepPaused       Type = "step.paused"      // step-level debugger waiting (legacy)
	CommandPaused    Type = "command.paused"    // command-level debugger waiting
	RecordingBegan   Type = "recording.began"   // input recording started
	RecordingUpdate  Type = "recording.update"  // a new action was captured
	RecordingStopped Type = "recording.stopped" // recording ended
)

// Event is a single progress message. Fields are populated as relevant to Type.
type Event struct {
	Type Type `json:"type"`

	// Run-level.
	Session string `json:"session,omitempty"`
	Total   int    `json:"total,omitempty"`

	// Case-level.
	CaseID   string `json:"caseId,omitempty"`
	CaseName string `json:"caseName,omitempty"`

	// Step-level.
	StepIndex   int    `json:"stepIndex,omitempty"`
	Phase       string `json:"phase,omitempty"`
	Human       string `json:"human,omitempty"`
	MachineDesc string `json:"machineDesc,omitempty"`

	// Screenshot.
	Path   string `json:"path,omitempty"`
	Target string `json:"target,omitempty"`
	Which  string `json:"which,omitempty"` // step | expected | actual

	// Assert.
	Question     string `json:"question,omitempty"`
	Expect       string `json:"expect,omitempty"`
	RawAnswer    string `json:"rawAnswer,omitempty"`
	Verdict      bool   `json:"verdict,omitempty"`
	ExpectedPath string `json:"expectedPath,omitempty"`
	ActualPath   string `json:"actualPath,omitempty"`

	// Common.
	Status     string `json:"status,omitempty"`
	DurationMs int64  `json:"durationMs,omitempty"`
	ReportPath string `json:"reportPath,omitempty"`

	// Log.
	Level   string `json:"level,omitempty"`
	Message string `json:"message,omitempty"`

	// Debug mode (step.paused / command.paused / recording.*).
	MachineCmds   []string `json:"machineCmds,omitempty"`   // per-command descriptions
	RecordedDesc  string   `json:"recordedDesc,omitempty"`  // latest captured action description
	RecordedCount int      `json:"recordedCount,omitempty"` // total captured so far

	// Command-level debug fields (command.paused).
	CmdIndex  int    `json:"cmdIndex,omitempty"`
	TotalCmds int    `json:"totalCmds,omitempty"`
	CmdDesc   string `json:"cmdDesc,omitempty"`
}

// Handler receives events.
type Handler func(Event)

// Bus fans out events to subscribers.
type Bus struct {
	mu          sync.Mutex
	subscribers []Handler
}

// New returns an empty bus.
func New() *Bus { return &Bus{} }

// Subscribe registers a handler. Handlers run synchronously on Publish.
func (b *Bus) Subscribe(h Handler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subscribers = append(b.subscribers, h)
}

// Publish sends an event to all subscribers.
func (b *Bus) Publish(e Event) {
	b.mu.Lock()
	subs := make([]Handler, len(b.subscribers))
	copy(subs, b.subscribers)
	b.mu.Unlock()
	for _, h := range subs {
		h(e)
	}
}

// Log is a convenience for emitting a log event.
func (b *Bus) Log(level, msg string) {
	b.Publish(Event{Type: Log, Level: level, Message: msg})
}
