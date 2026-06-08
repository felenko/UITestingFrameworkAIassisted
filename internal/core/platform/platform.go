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
	FocusWindow(w Window) error
	ForegroundWindow() (Window, error) // the HWND that currently has keyboard focus
	ForegroundActive(w Window) bool    // true if w is the current foreground window
	IsTopmost(w Window) bool         // true if w has the WS_EX_TOPMOST style
	SetTopmost(w Window, on bool) error // pin/unpin w above non-topmost windows
	CloseWindow(w Window) error
	MoveWindow(w Window, x, y int) error
	ResizeWindow(w Window, width, height int) error
	WindowPID(w Window) uint32 // process that owns the window (may differ from a launcher PID)

	// Input integrity.
	WatchInput() (InputWatcher, error) // start observing real user input during a run

	// Capture.
	CaptureScreen() (image.Image, error)
	CaptureBounds(b Bounds) (image.Image, error)
	CaptureWindow(w Window) (image.Image, error) // window's own pixels (occlusion-safe)
}
