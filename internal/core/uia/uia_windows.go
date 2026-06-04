//go:build windows

// Package uia is a minimal UI Automation (UIAutomationCore) backend. It drives
// the CUIAutomation COM object via go-ole + manual vtable calls (no CGO) to
// locate controls by AutomationId/Name/ControlType and read their state. This
// is far more robust than pixel coordinates for apps that expose a UIA tree.
package uia

import (
	"fmt"
	"strings"
	"syscall"
	"unsafe"

	ole "github.com/go-ole/go-ole"
	"golang.org/x/sys/windows"
)

const (
	clsidCUIAutomation = "{FF48DBA4-60EF-4201-AA87-54103EEF594E}"
	iidIUIAutomation   = "{30CBE57D-D9D0-452A-AB13-7AC5AC4825EE}"
)

// IUIAutomation vtable indices (after IUnknown 0..2).
const (
	mElementFromHandle    = 6
	mGetRootElement       = 5
	mCreateTrueCondition  = 21
)

// IUIAutomationElement vtable indices.
const (
	eFindAll                     = 6
	eGetCurrentControlType       = 21
	eGetCurrentName              = 23
	eGetCurrentIsEnabled         = 28
	eGetCurrentAutomationId      = 29
	eGetCurrentBoundingRectangle = 43
)

// IUIAutomationElementArray vtable indices.
const (
	arrGetLength  = 3
	arrGetElement = 4
)

// TreeScope.
const treeScopeSubtree = 7

var ptrSize = unsafe.Sizeof(uintptr(0))

var (
	oleaut32         = windows.NewLazySystemDLL("oleaut32.dll")
	procSysFreeString = oleaut32.NewProc("SysFreeString")
)

// rect mirrors Win32 RECT (UIA returns left/top/right/bottom in screen pixels).
type rect struct{ Left, Top, Right, Bottom int32 }

// Query selects a control in the UIA tree. Empty fields are ignored; all set
// fields must match (AND).
type Query struct {
	AutomationID string
	Name         string
	ControlType  string
}

// Rect is a control's screen rectangle.
type Rect struct{ X, Y, Width, Height int }

// Center returns the rectangle's midpoint.
func (r Rect) Center() (int, int) { return r.X + r.Width/2, r.Y + r.Height/2 }

// Automation is a live IUIAutomation instance.
type Automation struct {
	ptr uintptr
}

// Element wraps an IUIAutomationElement.
type Element struct{ ptr uintptr }

// hrCall invokes the COM method at vtable index idx on `this`, returning the HRESULT.
func hrCall(this uintptr, idx int, args ...uintptr) uintptr {
	vtbl := *(*uintptr)(unsafe.Pointer(this))
	fn := *(*uintptr)(unsafe.Pointer(vtbl + uintptr(idx)*ptrSize))
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{this}, args...)...)
	return r
}

func succeeded(hr uintptr) bool { return int32(hr) >= 0 }

func release(ptr uintptr) {
	if ptr != 0 {
		hrCall(ptr, 2) // IUnknown::Release
	}
}

// New creates a UI Automation instance (COM initialized multi-threaded).
func New() (*Automation, error) {
	_ = ole.CoInitializeEx(0, ole.COINIT_MULTITHREADED)
	unk, err := ole.CreateInstance(ole.NewGUID(clsidCUIAutomation), ole.NewGUID(iidIUIAutomation))
	if err != nil {
		return nil, fmt.Errorf("CoCreateInstance(CUIAutomation): %w", err)
	}
	if unk == nil {
		return nil, fmt.Errorf("CUIAutomation returned nil")
	}
	return &Automation{ptr: uintptr(unsafe.Pointer(unk))}, nil
}

// Close releases the automation object.
func (a *Automation) Close() { release(a.ptr); a.ptr = 0 }

// elementFromHandle returns the UIA element for a top-level window handle.
func (a *Automation) elementFromHandle(hwnd uintptr) (*Element, error) {
	var out uintptr
	if !succeeded(hrCall(a.ptr, mElementFromHandle, hwnd, uintptr(unsafe.Pointer(&out)))) || out == 0 {
		return nil, fmt.Errorf("ElementFromHandle failed")
	}
	return &Element{ptr: out}, nil
}

func (a *Automation) createTrueCondition() (uintptr, error) {
	var cond uintptr
	if !succeeded(hrCall(a.ptr, mCreateTrueCondition, uintptr(unsafe.Pointer(&cond)))) || cond == 0 {
		return 0, fmt.Errorf("CreateTrueCondition failed")
	}
	return cond, nil
}

// Find locates the first descendant of the given window matching q.
func (a *Automation) Find(hwnd uintptr, q Query) (*Element, error) {
	root, err := a.elementFromHandle(hwnd)
	if err != nil {
		return nil, err
	}
	defer root.Release()

	cond, err := a.createTrueCondition()
	if err != nil {
		return nil, err
	}
	defer release(cond)

	var arr uintptr
	if !succeeded(hrCall(root.ptr, eFindAll, uintptr(treeScopeSubtree), cond, uintptr(unsafe.Pointer(&arr)))) || arr == 0 {
		return nil, fmt.Errorf("FindAll failed")
	}
	defer release(arr)

	var n int32
	if !succeeded(hrCall(arr, arrGetLength, uintptr(unsafe.Pointer(&n)))) {
		return nil, fmt.Errorf("get_Length failed")
	}

	wantCT := controlTypeID(q.ControlType)
	for i := int32(0); i < n; i++ {
		var ep uintptr
		if !succeeded(hrCall(arr, arrGetElement, uintptr(i), uintptr(unsafe.Pointer(&ep)))) || ep == 0 {
			continue
		}
		el := &Element{ptr: ep}
		if el.matches(q, wantCT) {
			return el, nil
		}
		el.Release()
	}
	return nil, fmt.Errorf("no UIA element matched %s", q.describe())
}

func (e *Element) matches(q Query, wantCT int32) bool {
	if q.AutomationID != "" && !strings.EqualFold(e.AutomationID(), q.AutomationID) {
		return false
	}
	if q.Name != "" && !strings.EqualFold(e.Name(), q.Name) {
		return false
	}
	if wantCT != 0 && e.ControlType() != wantCT {
		return false
	}
	return true
}

// Release frees the element.
func (e *Element) Release() {
	if e != nil {
		release(e.ptr)
		e.ptr = 0
	}
}

func (e *Element) bstrProp(idx int) string {
	var b *uint16
	if !succeeded(hrCall(e.ptr, idx, uintptr(unsafe.Pointer(&b)))) || b == nil {
		return ""
	}
	s := windows.UTF16PtrToString(b)
	procSysFreeString.Call(uintptr(unsafe.Pointer(b)))
	return s
}

// AutomationId returns the control's AutomationId.
func (e *Element) AutomationID() string { return e.bstrProp(eGetCurrentAutomationId) }

// Name returns the control's name.
func (e *Element) Name() string { return e.bstrProp(eGetCurrentName) }

// ControlType returns the UIA control type id.
func (e *Element) ControlType() int32 {
	var ct int32
	hrCall(e.ptr, eGetCurrentControlType, uintptr(unsafe.Pointer(&ct)))
	return ct
}

// IsEnabled reports whether the control is enabled.
func (e *Element) IsEnabled() bool {
	var b int32
	hrCall(e.ptr, eGetCurrentIsEnabled, uintptr(unsafe.Pointer(&b)))
	return b != 0
}

// BoundingRect returns the control's screen rectangle.
func (e *Element) BoundingRect() (Rect, error) {
	var r rect
	if !succeeded(hrCall(e.ptr, eGetCurrentBoundingRectangle, uintptr(unsafe.Pointer(&r)))) {
		return Rect{}, fmt.Errorf("get_CurrentBoundingRectangle failed")
	}
	return Rect{X: int(r.Left), Y: int(r.Top), Width: int(r.Right - r.Left), Height: int(r.Bottom - r.Top)}, nil
}

func (q Query) describe() string {
	var parts []string
	if q.AutomationID != "" {
		parts = append(parts, "automationId="+q.AutomationID)
	}
	if q.Name != "" {
		parts = append(parts, "name="+q.Name)
	}
	if q.ControlType != "" {
		parts = append(parts, "controlType="+q.ControlType)
	}
	return strings.Join(parts, " ")
}

// controlTypeID maps a friendly control-type name to its UIA id (0 = ignore).
func controlTypeID(name string) int32 {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "":
		return 0
	case "button":
		return 50000
	case "checkbox", "check":
		return 50002
	case "combobox", "combo":
		return 50003
	case "edit", "textbox":
		return 50004
	case "hyperlink", "link":
		return 50005
	case "image":
		return 50006
	case "listitem":
		return 50007
	case "list":
		return 50008
	case "menu":
		return 50009
	case "menubar":
		return 50010
	case "menuitem":
		return 50011
	case "radiobutton", "radio":
		return 50013
	case "tab":
		return 50018
	case "tabitem":
		return 50019
	case "text", "label":
		return 50020
	case "toolbar":
		return 50021
	case "tree":
		return 50023
	case "treeitem":
		return 50024
	case "group":
		return 50026
	case "document":
		return 50030
	case "pane":
		return 50033
	case "window":
		return 50032
	case "table":
		return 50036
	default:
		return 0
	}
}
