//go:build !windows

package platform

import (
	"fmt"
	"image"
	"runtime"
	"time"
)

// stubDriver returns "not supported" for every operation on non-Windows OSes.
// The runner targets desktop Windows; this exists so the module still builds
// (e.g. for `go vet`) on other platforms.
type stubDriver struct{}

// New returns a no-op driver on unsupported platforms.
func New() Driver { return &stubDriver{} }

func unsupported(op string) error {
	return fmt.Errorf("%s: platform %q is not supported (Windows only)", op, runtime.GOOS)
}

func (stubDriver) SetDPIAware() error    { return unsupported("SetDPIAware") }
func (stubDriver) ScreenBounds() Bounds  { return Bounds{} }
func (stubDriver) DPIScale() float64     { return 1.0 }

func (stubDriver) MouseMove(Point) error                 { return unsupported("MouseMove") }
func (stubDriver) MouseClick(Point, string, int) error   { return unsupported("MouseClick") }
func (stubDriver) MouseDown(Point, string) error         { return unsupported("MouseDown") }
func (stubDriver) MouseUp(Point, string) error           { return unsupported("MouseUp") }
func (stubDriver) MouseDrag(Point, Point, string) error  { return unsupported("MouseDrag") }
func (stubDriver) MouseScroll(Point, int, int) error     { return unsupported("MouseScroll") }

func (stubDriver) TypeText(string, time.Duration) error { return unsupported("TypeText") }
func (stubDriver) KeyPress(string) error                { return unsupported("KeyPress") }
func (stubDriver) KeyDown(string) error                 { return unsupported("KeyDown") }
func (stubDriver) KeyUp(string) error                   { return unsupported("KeyUp") }

func (stubDriver) FindWindow(WindowQuery) (Window, error) { return nil, unsupported("FindWindow") }
func (stubDriver) FindWindowByPID(uint32) (Window, error) { return nil, unsupported("FindWindowByPID") }
func (stubDriver) FindElement(Window, UIAQuery) (Bounds, error) {
	return Bounds{}, unsupported("FindElement")
}
func (stubDriver) ElementState(Window, UIAQuery) (UIAElement, error) {
	return UIAElement{}, unsupported("ElementState")
}
func (stubDriver) ElementAtPoint(Point) (UIANode, error) {
	return UIANode{}, unsupported("ElementAtPoint")
}
func (stubDriver) AppWindows(uint32) ([]Window, error) { return nil, unsupported("AppWindows") }
func (stubDriver) FocusWindow(Window) error              { return unsupported("FocusWindow") }
func (stubDriver) ForegroundWindow() (Window, error)     { return nil, unsupported("ForegroundWindow") }
func (stubDriver) ForegroundActive(Window) bool          { return false }
func (stubDriver) IsTopmost(Window) bool                 { return false }
func (stubDriver) SetTopmost(Window, bool) error         { return unsupported("SetTopmost") }
func (stubDriver) CloseWindow(Window) error              { return unsupported("CloseWindow") }
func (stubDriver) MoveWindow(Window, int, int) error     { return unsupported("MoveWindow") }
func (stubDriver) ResizeWindow(Window, int, int) error   { return unsupported("ResizeWindow") }
func (stubDriver) EnsureOnPrimary(Window) (bool, error)  { return false, nil }
func (stubDriver) WindowPID(Window) uint32               { return 0 }

func (stubDriver) CaptureScreen() (image.Image, error)        { return nil, unsupported("CaptureScreen") }
func (stubDriver) CaptureBounds(Bounds) (image.Image, error)  { return nil, unsupported("CaptureBounds") }
func (stubDriver) CaptureWindow(Window) (image.Image, error)  { return nil, unsupported("CaptureWindow") }

// noopWatcher reports no user input; used on unsupported platforms.
type noopWatcher struct{}

func (noopWatcher) UserEvents() uint64 { return 0 }
func (noopWatcher) Stop()              {}

func (stubDriver) WatchInput() (InputWatcher, error)  { return noopWatcher{}, nil }
func (stubDriver) RecordInput() (InputRecorder, error) { return nil, unsupported("RecordInput") }
