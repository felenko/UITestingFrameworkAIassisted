//go:build windows

package platform

import (
	"fmt"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// UI Automation targeting (docs/02 "Phase 2"). We talk to the OS UI Automation
// COM API (UIAutomationCore) directly via vtable calls — no cgo, no extra
// dependency, matching the rest of the pure-syscall driver. This resolves a
// control by AutomationId / Name / ControlType to its on-screen bounding
// rectangle (physical pixels), which the runner turns into a real mouse click.
// Because it reads the accessibility tree rather than the screen image, it is
// immune to window position, DPI scaling, and layout/accordion drift.

var (
	ole32    = windows.NewLazySystemDLL("ole32.dll")
	oleaut32 = windows.NewLazySystemDLL("oleaut32.dll")

	procCoInitializeEx   = ole32.NewProc("CoInitializeEx")
	procCoUninitialize   = ole32.NewProc("CoUninitialize")
	procCoCreateInstance = ole32.NewProc("CoCreateInstance")
	procSysAllocString   = oleaut32.NewProc("SysAllocString")
	procSysFreeString    = oleaut32.NewProc("SysFreeString")
)

// CLSID_CUIAutomation and IID_IUIAutomation identify the automation root object.
var (
	clsidCUIAutomation = windows.GUID{
		Data1: 0xff48dba4, Data2: 0x60ef, Data3: 0x4201,
		Data4: [8]byte{0xaa, 0x87, 0x54, 0x10, 0x3e, 0xef, 0x59, 0x4e},
	}
	iidIUIAutomation = windows.GUID{
		Data1: 0x30cbe57d, Data2: 0xd9d0, Data3: 0x452a,
		Data4: [8]byte{0xab, 0x13, 0x7a, 0xc5, 0xac, 0x48, 0x25, 0xee},
	}
)

const (
	coinitMultithreaded = 0x0
	rpcEChangedMode     = 0x80010106
	clsctxInprocServer  = 0x1

	treeScopeSubtree = 0x7 // Element | Children | Descendants

	uiaControlTypePropertyId           = 30003
	uiaNamePropertyId                  = 30005
	uiaIsEnabledPropertyId             = 30010
	uiaAutomationIdPropertyId          = 30011
	uiaValueValuePropertyId            = 30045
	uiaSelectionItemIsSelectedPropertyId = 30079
	uiaToggleStatePropertyId           = 30086

	vtI4   = 3
	vtBSTR = 8
	vtBool = 11

	// IUIAutomation vtable slots (after the 3 IUnknown entries).
	idxElementFromHandle        = 6
	idxElementFromPoint         = 7
	idxGetControlViewWalker     = 14 // get_ControlViewWalker property getter
	idxCreatePropertyCondition  = 23
	idxCreateAndCondition       = 25
	// IUIAutomationElement vtable slots.
	idxFindFirst                = 5
	idxGetCurrentPropertyValue  = 10
	idxGetCurrentBoundingRect   = 43
	// IUIAutomationTreeWalker vtable slots.
	idxWalkerGetParent          = 3
)

// controlTypeIDs maps the friendly control-type names exposed to YAML authors
// (and returned by the Windows MCP inspector) to UIA ControlType IDs.
var controlTypeIDs = map[string]int32{
	"button": 50000, "calendar": 50001, "checkbox": 50002, "combobox": 50003,
	"edit": 50004, "hyperlink": 50005, "image": 50006, "listitem": 50007,
	"list": 50008, "menu": 50009, "menubar": 50010, "menuitem": 50011,
	"progressbar": 50012, "radiobutton": 50013, "scrollbar": 50014,
	"slider": 50015, "spinner": 50016, "statusbar": 50017, "tab": 50018,
	"tabitem": 50019, "text": 50020, "toolbar": 50021, "tooltip": 50022,
	"tree": 50023, "treeitem": 50024, "custom": 50025, "group": 50026,
	"thumb": 50027, "datagrid": 50028, "dataitem": 50029, "document": 50030,
	"splitbutton": 50031, "window": 50032, "pane": 50033, "header": 50034,
	"headeritem": 50035, "table": 50036, "titlebar": 50037, "separator": 50038,
}

// controlTypeNames is the reverse of controlTypeIDs, for rendering a hit-tested
// element's control type back into the friendly name YAML authors use.
var controlTypeNames = func() map[int32]string {
	m := make(map[int32]string, len(controlTypeIDs))
	for name, id := range controlTypeIDs {
		m[id] = name
	}
	return m
}()

// variant mirrors the Win32 VARIANT (24 bytes on x64). We only ever build VT_I4
// (control-type ids) and VT_BSTR (string property values).
type variant struct {
	vt   uint16
	_    [3]uint16
	val  uint64 // first union slot: the BSTR pointer or the I4 value
	_    uint64 // remaining union bytes (pad to 24)
}

// comCall invokes the COM method at vtable slot idx on `this`. The first
// argument passed to the method is always the interface pointer itself.
func comCall(this uintptr, idx int, args ...uintptr) uintptr {
	vtbl := *(*uintptr)(unsafe.Pointer(this))
	fn := *(*uintptr)(unsafe.Pointer(vtbl + uintptr(idx)*unsafe.Sizeof(uintptr(0))))
	ret, _, _ := syscall.SyscallN(fn, append([]uintptr{this}, args...)...)
	return ret
}

func comRelease(this uintptr) {
	if this != 0 {
		comCall(this, 2) // IUnknown::Release
	}
}

func failed(hr uintptr) bool { return int32(hr) < 0 }

// withElement resolves the first UIA element matching q inside window w and
// invokes fn with its COM pointer (0 when nothing matched, so callers can treat
// "absent" as a non-error outcome). All COM init, root resolution, condition
// building, and cleanup are handled here; fn must not retain `found`.
func (d *winDriver) withElement(w Window, q UIAQuery, fn func(found uintptr) error) error {
	if w == nil {
		return fmt.Errorf("no window")
	}
	if q.IsZero() {
		return fmt.Errorf("empty query")
	}

	// COM apartment + element resolution must stay on one OS thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	hr, _, _ := procCoInitializeEx.Call(0, coinitMultithreaded)
	// S_OK (0) and S_FALSE (1) both mean "initialized on this thread"; balance
	// them with CoUninitialize. RPC_E_CHANGED_MODE means COM was already up in a
	// different mode — we did not initialize it, so we must not uninitialize.
	if uintptr(hr) != rpcEChangedMode {
		defer procCoUninitialize.Call()
	}

	var uia uintptr
	hr, _, _ = procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidCUIAutomation)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidIUIAutomation)), uintptr(unsafe.Pointer(&uia)))
	if failed(hr) || uia == 0 {
		return fmt.Errorf("CoCreateInstance(CUIAutomation) failed (hr=0x%08x)", uint32(hr))
	}
	defer comRelease(uia)

	var root uintptr
	if hr := comCall(uia, idxElementFromHandle, w.Handle(), uintptr(unsafe.Pointer(&root))); failed(hr) || root == 0 {
		return fmt.Errorf("ElementFromHandle failed (hr=0x%08x)", uint32(hr))
	}
	defer comRelease(root)

	cond, free, err := d.buildCondition(uia, q)
	if err != nil {
		return err
	}
	defer free()

	var found uintptr
	if hr := comCall(root, idxFindFirst, treeScopeSubtree, cond, uintptr(unsafe.Pointer(&found))); failed(hr) {
		return fmt.Errorf("FindFirst failed (hr=0x%08x)", uint32(hr))
	}
	if found != 0 {
		defer comRelease(found)
	}
	return fn(found)
}

// elementBounds reads an element's screen bounding rectangle (physical pixels).
func elementBounds(found uintptr) (Bounds, error) {
	var r rect // tagRECT: left, top, right, bottom (LONG)
	if hr := comCall(found, idxGetCurrentBoundingRect, uintptr(unsafe.Pointer(&r))); failed(hr) {
		return Bounds{}, fmt.Errorf("get_CurrentBoundingRectangle failed (hr=0x%08x)", uint32(hr))
	}
	return Bounds{X: int(r.Left), Y: int(r.Top), Width: int(r.Right - r.Left), Height: int(r.Bottom - r.Top)}, nil
}

// FindElement resolves a UIA control inside window w to its screen bounding
// rectangle (physical pixels).
func (d *winDriver) FindElement(w Window, q UIAQuery) (Bounds, error) {
	var b Bounds
	err := d.withElement(w, q, func(found uintptr) error {
		if found == 0 {
			return fmt.Errorf("no UIA element matched %s", describeUIAQuery(q))
		}
		bb, err := elementBounds(found)
		if err != nil {
			return err
		}
		if bb.Width <= 0 || bb.Height <= 0 {
			return fmt.Errorf("UIA element %s has no on-screen rectangle (offscreen/collapsed)", describeUIAQuery(q))
		}
		b = bb
		return nil
	})
	return b, err
}

// ElementState resolves a UIA control and reads its state for deterministic
// assertions. A missing element is reported as UIAElement{Found:false}, nil —
// only COM failures return an error.
func (d *winDriver) ElementState(w Window, q UIAQuery) (UIAElement, error) {
	var el UIAElement
	err := d.withElement(w, q, func(found uintptr) error {
		if found == 0 {
			return nil // Found stays false; absence is not an error
		}
		el.Found = true
		el.Name = elemStringProp(found, uiaNamePropertyId)
		el.Value = elemStringProp(found, uiaValueValuePropertyId)
		el.Enabled = elemBoolProp(found, uiaIsEnabledPropertyId)
		el.Selected = elemBoolProp(found, uiaSelectionItemIsSelectedPropertyId)
		el.Toggle = elemToggleProp(found, uiaToggleStatePropertyId)
		if b, err := elementBounds(found); err == nil {
			el.Bounds = b
		}
		return nil
	})
	return el, err
}

// elemPropVariant reads one current property value of an element into a VARIANT.
func elemPropVariant(found uintptr, propID int) (variant, bool) {
	var v variant
	hr := comCall(found, idxGetCurrentPropertyValue, uintptr(propID), uintptr(unsafe.Pointer(&v)))
	if failed(hr) {
		return variant{}, false
	}
	return v, true
}

// elemStringProp reads a BSTR property as a Go string (empty if unsupported).
func elemStringProp(found uintptr, propID int) string {
	v, ok := elemPropVariant(found, propID)
	if !ok || v.vt != vtBSTR || v.val == 0 {
		return ""
	}
	s := windows.UTF16PtrToString((*uint16)(unsafe.Pointer(uintptr(v.val))))
	procSysFreeString.Call(uintptr(v.val))
	return s
}

// elemBoolProp reads a VT_BOOL/VT_I4 property as a bool.
func elemBoolProp(found uintptr, propID int) bool {
	v, ok := elemPropVariant(found, propID)
	if !ok {
		return false
	}
	switch v.vt {
	case vtBool:
		return int16(uint16(v.val)) != 0 // VARIANT_BOOL: 0xFFFF = true
	case vtI4:
		return int32(uint32(v.val)) != 0
	default:
		return false
	}
}

// elemToggleProp reads ToggleState (VT_I4 0/1/2); ToggleNone if not a toggle.
func elemToggleProp(found uintptr, propID int) ToggleState {
	v, ok := elemPropVariant(found, propID)
	if !ok || v.vt != vtI4 {
		return ToggleNone
	}
	switch int32(uint32(v.val)) {
	case 0:
		return ToggleOff
	case 1:
		return ToggleOn
	case 2:
		return ToggleIndeterminate
	default:
		return ToggleNone
	}
}

// buildCondition assembles the IUIAutomationCondition for q. It returns the
// condition pointer plus a cleanup func that releases every COM object and
// frees every BSTR it allocated.
func (d *winDriver) buildCondition(uia uintptr, q UIAQuery) (uintptr, func(), error) {
	var propConds []uintptr // property conditions to AND together
	var release []uintptr   // every condition object (props + ANDs) to release
	var bstrs []uintptr
	cleanup := func() {
		for _, c := range release {
			comRelease(c)
		}
		for _, s := range bstrs {
			procSysFreeString.Call(s)
		}
	}

	propCond := func(propID int, v variant) (uintptr, error) {
		var c uintptr
		hr := comCall(uia, idxCreatePropertyCondition, uintptr(propID),
			uintptr(unsafe.Pointer(&v)), uintptr(unsafe.Pointer(&c)))
		if failed(hr) || c == 0 {
			return 0, fmt.Errorf("CreatePropertyCondition(%d) failed (hr=0x%08x)", propID, uint32(hr))
		}
		release = append(release, c)
		return c, nil
	}

	strVariant := func(s string) (variant, error) {
		p, err := windows.UTF16PtrFromString(s)
		if err != nil {
			return variant{}, err
		}
		bstr, _, _ := procSysAllocString.Call(uintptr(unsafe.Pointer(p)))
		if bstr == 0 {
			return variant{}, fmt.Errorf("SysAllocString failed")
		}
		bstrs = append(bstrs, bstr)
		return variant{vt: vtBSTR, val: uint64(bstr)}, nil
	}

	if q.AutomationID != "" {
		v, err := strVariant(q.AutomationID)
		if err != nil {
			cleanup()
			return 0, nil, err
		}
		c, err := propCond(uiaAutomationIdPropertyId, v)
		if err != nil {
			cleanup()
			return 0, nil, err
		}
		propConds = append(propConds, c)
	}
	if q.Name != "" {
		v, err := strVariant(q.Name)
		if err != nil {
			cleanup()
			return 0, nil, err
		}
		c, err := propCond(uiaNamePropertyId, v)
		if err != nil {
			cleanup()
			return 0, nil, err
		}
		propConds = append(propConds, c)
	}
	if q.ControlType != "" {
		id, ok := controlTypeIDs[strings.ToLower(strings.TrimSpace(q.ControlType))]
		if !ok {
			cleanup()
			return 0, nil, fmt.Errorf("unknown UIA controlType %q", q.ControlType)
		}
		c, err := propCond(uiaControlTypePropertyId, variant{vt: vtI4, val: uint64(uint32(id))})
		if err != nil {
			cleanup()
			return 0, nil, err
		}
		propConds = append(propConds, c)
	}

	// Fold the property conditions into a single AND condition. Iterate the
	// fixed propConds slice; AND results are tracked separately in `release`.
	combined := propConds[0]
	for i := 1; i < len(propConds); i++ {
		var and uintptr
		hr := comCall(uia, idxCreateAndCondition, combined, propConds[i], uintptr(unsafe.Pointer(&and)))
		if failed(hr) || and == 0 {
			cleanup()
			return 0, nil, fmt.Errorf("CreateAndCondition failed (hr=0x%08x)", uint32(hr))
		}
		release = append(release, and)
		combined = and
	}
	return combined, cleanup, nil
}

// ElementAtPoint hit-tests the UI Automation tree at a screen point (physical
// pixels) and returns the identifying properties of the element found there.
// When the directly hit element exposes neither an AutomationId nor a Name
// (e.g. the inner text of a button), it walks up the control-view ancestors —
// at most 5 levels — to the nearest identifiable element. This is the harvest
// step of self-healing locators: an AI-located point becomes a durable
// AutomationId/Name selector that later runs resolve without the AI.
func (d *winDriver) ElementAtPoint(p Point) (UIANode, error) {
	var node UIANode

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	hr, _, _ := procCoInitializeEx.Call(0, coinitMultithreaded)
	if uintptr(hr) != rpcEChangedMode {
		defer procCoUninitialize.Call()
	}

	var uia uintptr
	hr, _, _ = procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidCUIAutomation)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidIUIAutomation)), uintptr(unsafe.Pointer(&uia)))
	if failed(hr) || uia == 0 {
		return node, fmt.Errorf("CoCreateInstance(CUIAutomation) failed (hr=0x%08x)", uint32(hr))
	}
	defer comRelease(uia)

	// POINT is 8 bytes and passed by value: pack x (low) and y (high) into one word.
	pt := uintptr(uint64(uint32(int32(p.X))) | uint64(uint32(int32(p.Y)))<<32)
	var el uintptr
	if hr := comCall(uia, idxElementFromPoint, pt, uintptr(unsafe.Pointer(&el))); failed(hr) || el == 0 {
		return node, fmt.Errorf("ElementFromPoint(%d,%d) failed (hr=0x%08x)", p.X, p.Y, uint32(hr))
	}

	// Control-view walker for the ancestor climb (best-effort; 0 disables it).
	var walker uintptr
	if hr := comCall(uia, idxGetControlViewWalker, uintptr(unsafe.Pointer(&walker))); failed(hr) {
		walker = 0
	}
	if walker != 0 {
		defer comRelease(walker)
	}

	cur := el
	node = readUIANode(cur)
	for depth := 0; depth < 5 && node.AutomationID == "" && node.Name == ""; depth++ {
		if walker == 0 {
			break
		}
		var parent uintptr
		if hr := comCall(walker, idxWalkerGetParent, cur, uintptr(unsafe.Pointer(&parent))); failed(hr) || parent == 0 {
			break
		}
		comRelease(cur)
		cur = parent
		node = readUIANode(cur)
	}
	comRelease(cur)

	if node.AutomationID == "" && node.Name == "" && node.ControlType == "" {
		return node, fmt.Errorf("no identifiable UIA element at (%d,%d)", p.X, p.Y)
	}
	return node, nil
}

// readUIANode reads an element's identifying properties.
func readUIANode(found uintptr) UIANode {
	n := UIANode{
		AutomationID: elemStringProp(found, uiaAutomationIdPropertyId),
		Name:         elemStringProp(found, uiaNamePropertyId),
	}
	if v, ok := elemPropVariant(found, uiaControlTypePropertyId); ok && v.vt == vtI4 {
		n.ControlType = controlTypeNames[int32(uint32(v.val))]
	}
	if b, err := elementBounds(found); err == nil {
		n.Bounds = b
	}
	return n
}

func describeUIAQuery(q UIAQuery) string {
	var parts []string
	if q.AutomationID != "" {
		parts = append(parts, fmt.Sprintf("automationId=%q", q.AutomationID))
	}
	if q.Name != "" {
		parts = append(parts, fmt.Sprintf("name=%q", q.Name))
	}
	if q.ControlType != "" {
		parts = append(parts, fmt.Sprintf("controlType=%q", q.ControlType))
	}
	return strings.Join(parts, " ")
}
