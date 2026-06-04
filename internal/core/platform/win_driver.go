//go:build windows

package platform

import "unsafe"

// winDriver is the Windows implementation of Driver.
type winDriver struct {
	dpiScale float64
}

// New returns the platform Driver for the current OS (Windows).
func New() Driver {
	return &winDriver{dpiScale: 1.0}
}

// SetDPIAware makes the process DPI-aware so coordinates and captures use
// physical pixels consistently. Prefers per-monitor-v2, falls back to system.
func (d *winDriver) SetDPIAware() error {
	if procSetProcessDpiAwareCtx.Find() == nil {
		r, _, _ := procSetProcessDpiAwareCtx.Call(dpiAwarenessContextPerMonitorV2)
		if r != 0 {
			d.dpiScale = d.detectScale()
			return nil
		}
	}
	procSetProcessDPIAware.Call()
	d.dpiScale = d.detectScale()
	return nil
}

func (d *winDriver) detectScale() float64 {
	if procGetDpiForSystem.Find() == nil {
		r, _, _ := procGetDpiForSystem.Call()
		if r > 0 {
			return float64(r) / 96.0
		}
	}
	hdc, _, _ := procGetDC.Call(0)
	if hdc != 0 {
		defer procReleaseDC.Call(0, hdc)
		r, _, _ := procGetDeviceCaps.Call(hdc, logpixelsx)
		if r > 0 {
			return float64(r) / 96.0
		}
	}
	return 1.0
}

func (d *winDriver) DPIScale() float64 { return d.dpiScale }

func metric(index int) int {
	r, _, _ := procGetSystemMetrics.Call(uintptr(index))
	return int(int32(r))
}

// ScreenBounds returns the primary screen rectangle in physical pixels.
func (d *winDriver) ScreenBounds() Bounds {
	return Bounds{X: 0, Y: 0, Width: metric(smCXScreen), Height: metric(smCYScreen)}
}

// virtualBounds spans all monitors (used for whole-screen capture).
func virtualBounds() Bounds {
	return Bounds{
		X:      metric(smXVirtualScreen),
		Y:      metric(smYVirtualScreen),
		Width:  metric(smCXVirtualScreen),
		Height: metric(smCYVirtualScreen),
	}
}

// input mirrors Win32 INPUT (x64): a 4-byte type, 4-byte pad, then a 32-byte
// union. [4]uint64 guarantees 8-byte alignment so the embedded uintptr in the
// union lands correctly.
type input struct {
	typ   uint32
	_     uint32
	union [4]uint64
}

type mouseInput struct {
	dx          int32
	dy          int32
	mouseData   uint32
	dwFlags     uint32
	time        uint32
	dwExtraInfo uintptr
}

type keybdInput struct {
	wVk         uint16
	wScan       uint16
	dwFlags     uint32
	time        uint32
	dwExtraInfo uintptr
}

func makeMouseInput(mi mouseInput) input {
	in := input{typ: inputMouse}
	*(*mouseInput)(unsafe.Pointer(&in.union[0])) = mi
	return in
}

func makeKeybdInput(ki keybdInput) input {
	in := input{typ: inputKeyboard}
	*(*keybdInput)(unsafe.Pointer(&in.union[0])) = ki
	return in
}

// sendInputs dispatches a batch of synthesized inputs.
func sendInputs(in []input) error {
	if len(in) == 0 {
		return nil
	}
	r, _, err := procSendInput.Call(
		uintptr(len(in)),
		uintptr(unsafe.Pointer(&in[0])),
		unsafe.Sizeof(in[0]),
	)
	if int(r) != len(in) {
		return errno("SendInput", err)
	}
	return nil
}
