// Package platform abstracts OS-level input actuation, window management, and
// screen capture (docs/02 §3.1–§3.4). The runner uses the Driver interface;
// the concrete implementation is selected at build time (Windows here).
package platform

import (
	"image"
	"time"
)

// Point is a screen-absolute coordinate in physical pixels.
type Point struct{ X, Y int }

// Bounds is a screen-absolute rectangle in physical pixels.
type Bounds struct{ X, Y, Width, Height int }

// WindowQuery describes how to locate a window.
type WindowQuery struct {
	Title    string // substring/regex match on window title
	Process  string // executable name (e.g. "notepad.exe")
	Class    string // window class name
	Strategy string // title | process | class (default title)
	PID      uint32 // if >0, prefer a window owned by this process
}

// UIAQuery locates a control inside a window via the UI Automation tree. Any
// non-empty field narrows the match; AutomationID is exact, Name is exact,
// ControlType is a friendly control-type name (e.g. "Button", "Edit"). The
// driver returns the first element (subtree order) that matches every set
// field — immune to pixel/DPI/layout drift because it reads the accessibility
// tree, not the screen.
type UIAQuery struct {
	AutomationID string
	Name         string
	ControlType  string
}

// IsZero reports whether no selector was provided.
func (q UIAQuery) IsZero() bool {
	return q.AutomationID == "" && q.Name == "" && q.ControlType == ""
}

// UIANode is an element identity harvested from the UI Automation tree (e.g. by
// hit-testing a screen point). It carries just enough to rebuild a durable
// UIAQuery for the element later: the identifying properties plus the bounds it
// had when harvested.
type UIANode struct {
	AutomationID string
	Name         string
	ControlType  string // friendly control-type name (see UIAQuery); "" if unknown
	Bounds       Bounds
}

// ToggleState mirrors the UIA ToggleState enum for checkbox/toggle controls.
type ToggleState int

const (
	ToggleNone          ToggleState = -1 // element is not a toggle control
	ToggleOff           ToggleState = 0
	ToggleOn            ToggleState = 1
	ToggleIndeterminate ToggleState = 2
)

// UIAElement is a resolved control's state, read from the UI Automation tree for
// deterministic assertions (no screen-image or AI dependency).
type UIAElement struct {
	Found    bool
	Name     string      // UIA Name property
	Value    string      // ValuePattern value (edit / combo box text)
	Enabled  bool        // IsEnabled
	Selected bool        // SelectionItem.IsSelected
	Toggle   ToggleState // checkbox / toggle state (ToggleNone if not a toggle)
	Bounds   Bounds
}

// Window is a located top-level window.
type Window interface {
	Title() string
	Bounds() (Bounds, error)
	Handle() uintptr
}

// InputWatcher observes physical (non-injected) user input for the duration of
// a run, so the runner can tell whether a person touched the real mouse or
// keyboard while an action was being driven. Implementations ignore the
// synthetic input the framework itself injects.
type InputWatcher interface {
	// UserEvents returns a monotonically increasing count of real user input
	// events (physical mouse buttons/wheel and key presses) seen so far. The
	// runner snapshots it around an action and treats any delta as contamination.
	UserEvents() uint64
	// Stop uninstalls the watcher and releases its resources.
	Stop()
}

// RecordedAction is a single captured user input event from an InputRecorder.
type RecordedAction struct {
	At     time.Time
	Action string // "mouse_click" | "type_text" | "key_press"

	// mouse_click fields.
	X, Y   int
	Button string // "left" | "right" | "middle"
	Count  int    // 1 = single click; 2 = double-click

	// type_text field.
	Text string

	// key_press field.
	Keys string

	// UIA enrichment (best-effort; empty when hit-test found nothing).
	UIAID      string // AutomationId at click point
	UIAName    string // Name property at click point
	WindowTitle string // title of the top-level window that owns the click point
}

// Describe returns a short human-readable summary for the recording feed.
func (a RecordedAction) Describe() string {
	switch a.Action {
	case "mouse_click":
		if a.UIAID != "" {
			s := "mouse_click  " + a.UIAID + " (UIA)  @ (" + itoa(a.X) + ", " + itoa(a.Y) + ")"
			if a.WindowTitle != "" {
				s += "  [" + a.WindowTitle + "]"
			}
			return s
		}
		return "mouse_click  @ (" + itoa(a.X) + ", " + itoa(a.Y) + ")"
	case "type_text":
		s := a.Text
		if len(s) > 50 {
			s = s[:50] + "…"
		}
		return "type_text  \"" + s + "\""
	case "key_press":
		return "key_press  " + a.Keys
	}
	return a.Action
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// InputRecorder captures raw physical mouse clicks and keystrokes while active.
type InputRecorder interface {
	// C delivers RecordedActions as they are captured (buffered channel, cap 256).
	// The channel is closed when Stop() completes.
	C() <-chan RecordedAction
	// Stop ends recording, flushes any buffered input, and closes C().
	Stop()
}

// Driver is the OS abstraction the runner drives.
type Driver interface {
	// Lifecycle / capabilities.
	SetDPIAware() error
	ScreenBounds() Bounds
	DPIScale() float64

	// Mouse.
	MouseMove(p Point) error
	MouseClick(p Point, button string, count int) error
	MouseDown(p Point, button string) error
	MouseUp(p Point, button string) error
	MouseDrag(from, to Point, button string) error
	MouseScroll(p Point, dx, dy int) error

	// Keyboard.
	TypeText(text string, perChar time.Duration) error
	KeyPress(chord string) error
	KeyDown(key string) error
	KeyUp(key string) error

	// Windows.
	FindWindow(q WindowQuery) (Window, error)
	FindWindowByPID(pid uint32) (Window, error) // largest visible window for a process
	FindElement(w Window, q UIAQuery) (Bounds, error) // locate a control via UI Automation
	ElementState(w Window, q UIAQuery) (UIAElement, error) // read a control's state (existence/value/toggle/enabled)
	ElementAtPoint(p Point) (UIANode, error) // hit-test the UIA tree at a screen point; walks up to the nearest identifiable ancestor
	AppWindows(pid uint32) ([]Window, error) // all visible top-level windows owned by pid (for dialog detection)
	FocusWindow(w Window) error
	ForegroundWindow() (Window, error) // the HWND that currently has keyboard focus
	ForegroundActive(w Window) bool    // true if w is the current foreground window
	IsTopmost(w Window) bool         // true if w has the WS_EX_TOPMOST style
	SetTopmost(w Window, on bool) error // pin/unpin w above non-topmost windows
	CloseWindow(w Window) error
	MoveWindow(w Window, x, y int) error
	ResizeWindow(w Window, width, height int) error
	EnsureOnPrimary(w Window) (bool, error) // move w onto the primary monitor if its center is off it; reports whether it moved
	WindowPID(w Window) uint32 // process that owns the window (may differ from a launcher PID)

	// Process query.
	ProcessRunning(name string) bool // true if any process whose exe matches name (case-insensitive) is alive

	// Input integrity.
	WatchInput() (InputWatcher, error) // start observing real user input during a run

	// Debug / recording.
	RecordInput() (InputRecorder, error) // start capturing physical mouse clicks and keystrokes

	// Capture.
	CaptureScreen() (image.Image, error) // primary monitor; use CaptureWindow/CaptureBounds for narrower scope
	CaptureBounds(b Bounds) (image.Image, error)
	CaptureWindow(w Window) (image.Image, error) // window's own pixels (occlusion-safe)
}
