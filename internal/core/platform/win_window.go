//go:build windows

package platform

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

func errno(op string, err error) error {
	if err == nil {
		return fmt.Errorf("%s failed", op)
	}
	return fmt.Errorf("%s: %w", op, err)
}

// winWindow implements Window.
type winWindow struct {
	hwnd  uintptr
	title string
}

func (w *winWindow) Title() string   { return w.title }
func (w *winWindow) Handle() uintptr { return w.hwnd }

func (w *winWindow) Bounds() (Bounds, error) {
	var r rect
	res, _, err := procGetWindowRect.Call(w.hwnd, uintptr(unsafe.Pointer(&r)))
	if res == 0 {
		return Bounds{}, errno("GetWindowRect", err)
	}
	return Bounds{
		X:      int(r.Left),
		Y:      int(r.Top),
		Width:  int(r.Right - r.Left),
		Height: int(r.Bottom - r.Top),
	}, nil
}

func getWindowText(hwnd uintptr) string {
	n, _, _ := procGetWindowTextLengthW.Call(hwnd)
	if n == 0 {
		return ""
	}
	buf := make([]uint16, n+1)
	procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return syscall.UTF16ToString(buf)
}

func getClassName(hwnd uintptr) string {
	buf := make([]uint16, 256)
	procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return syscall.UTF16ToString(buf)
}

func windowPID(hwnd uintptr) uint32 {
	var pid uint32
	procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	return pid
}

func processName(hwnd uintptr) string {
	pid := windowPID(hwnd)
	if pid == 0 {
		return ""
	}
	h, _, _ := procOpenProcess.Call(processQueryLimitedInformation, 0, uintptr(pid))
	if h == 0 {
		return ""
	}
	defer procCloseHandle.Call(h)
	buf := make([]uint16, 1024)
	size := uint32(len(buf))
	res, _, _ := procQueryFullProcessImageName.Call(h, 0,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	if res == 0 {
		return ""
	}
	full := syscall.UTF16ToString(buf[:size])
	return filepath.Base(full)
}

type candidate struct {
	hwnd  uintptr
	title string
}

// Windows caps syscall.NewCallback registrations (~2000). enumWindows runs on
// every FindWindow/FindWindowByPID call, so the callback must be created once.
var (
	enumWindowsCBOnce sync.Once
	enumWindowsCB     uintptr
	enumWindowsMu     sync.Mutex
	enumWindowsBuf    []candidate
)

func enumWindowsProc(hwnd, _ uintptr) uintptr {
	if vis, _, _ := procIsWindowVisible.Call(hwnd); vis == 0 {
		return 1
	}
	title := getWindowText(hwnd)
	if title == "" {
		return 1
	}
	enumWindowsBuf = append(enumWindowsBuf, candidate{hwnd: hwnd, title: title})
	return 1
}

// enumWindows returns all visible top-level windows that have a title.
func enumWindows() []candidate {
	enumWindowsCBOnce.Do(func() {
		enumWindowsCB = syscall.NewCallback(enumWindowsProc)
	})
	enumWindowsMu.Lock()
	defer enumWindowsMu.Unlock()
	enumWindowsBuf = enumWindowsBuf[:0]
	procEnumWindows.Call(enumWindowsCB, 0)
	out := make([]candidate, len(enumWindowsBuf))
	copy(out, enumWindowsBuf)
	return out
}

func (d *winDriver) FindWindow(q WindowQuery) (Window, error) {
	strategy := q.Strategy
	if strategy == "" {
		switch {
		case q.Process != "":
			strategy = "process"
		case q.Class != "":
			strategy = "class"
		default:
			strategy = "title"
		}
	}

	var titleRE *regexp.Regexp
	if strategy == "title" && q.Title != "" {
		if re, err := regexp.Compile("(?i)" + q.Title); err == nil {
			titleRE = re
		}
	}

	// Gather all matches, then prefer one owned by the launched process (q.PID)
	// to disambiguate (e.g. "Notepad" also matching a "Notepad++" window).
	var matches []candidate
	for _, c := range enumWindows() {
		switch strategy {
		case "title":
			if matchTitle(c.title, q.Title, titleRE) {
				matches = append(matches, c)
			}
		case "process":
			if strings.EqualFold(processName(c.hwnd), q.Process) {
				matches = append(matches, c)
			}
		case "class":
			if strings.EqualFold(getClassName(c.hwnd), q.Class) {
				matches = append(matches, c)
			}
		}
	}

	if len(matches) == 0 {
		return nil, fmt.Errorf("no window matched %s", describeQuery(q, strategy))
	}
	if q.PID != 0 {
		for _, c := range matches {
			if windowPID(c.hwnd) == q.PID {
				return &winWindow{hwnd: c.hwnd, title: c.title}, nil
			}
		}
		// Title matched but no window belongs to the requested process. Return
		// an error so the caller can fall back to FindWindowByPID rather than
		// receiving a stale match owned by a foreign process.
		return nil, fmt.Errorf("no window matched %s for pid %d", describeQuery(q, strategy), q.PID)
	}
	return &winWindow{hwnd: matches[0].hwnd, title: matches[0].title}, nil
}

// FindWindowByPID returns the largest visible top-level window owned by pid.
func (d *winDriver) FindWindowByPID(pid uint32) (Window, error) {
	if pid == 0 {
		return nil, fmt.Errorf("no process id")
	}
	var best *candidate
	bestArea := 0
	for _, c := range enumWindows() {
		if windowPID(c.hwnd) != pid {
			continue
		}
		var r rect
		if res, _, _ := procGetWindowRect.Call(c.hwnd, uintptr(unsafe.Pointer(&r))); res == 0 {
			continue
		}
		area := int(r.Right-r.Left) * int(r.Bottom-r.Top)
		if area > bestArea {
			best = &c
			bestArea = area
		}
	}
	if best == nil {
		return nil, fmt.Errorf("no visible window for pid %d", pid)
	}
	return &winWindow{hwnd: best.hwnd, title: best.title}, nil
}

// AppWindows returns every visible, titled top-level window owned by pid. Used
// to detect dialogs (message boxes / modal forms are separate top-level windows
// owned by the same process).
func (d *winDriver) AppWindows(pid uint32) ([]Window, error) {
	if pid == 0 {
		return nil, fmt.Errorf("no process id")
	}
	var out []Window
	for _, c := range enumWindows() {
		if windowPID(c.hwnd) == pid {
			out = append(out, &winWindow{hwnd: c.hwnd, title: c.title})
		}
	}
	return out, nil
}

func matchTitle(title, want string, re *regexp.Regexp) bool {
	if strings.Contains(strings.ToLower(title), strings.ToLower(want)) {
		return true
	}
	return re != nil && re.MatchString(title)
}

func describeQuery(q WindowQuery, strategy string) string {
	switch strategy {
	case "process":
		return fmt.Sprintf("process=%q", q.Process)
	case "class":
		return fmt.Sprintf("class=%q", q.Class)
	default:
		return fmt.Sprintf("title~=%q", q.Title)
	}
}

func (d *winDriver) FocusWindow(w Window) error {
	hwnd := w.Handle()
	// Disable the foreground-lock timeout so SetForegroundWindow is allowed.
	procSystemParametersInfo.Call(spiSetForegroundLockTimeout, 0, 0, spifSendChange)

	for attempt := 0; attempt < 5; attempt++ {
		// Only change show state when the window is hidden or minimized. Calling
		// ShowWindow on an already-visible normal/maximized window can resize or
		// flicker some apps (e.g. WPF login/shell dialogs already on screen).
		if iconic, _, _ := procIsIconic.Call(hwnd); iconic != 0 {
			procShowWindow.Call(hwnd, swRestore)
		} else if vis, _, _ := procIsWindowVisible.Call(hwnd); vis == 0 {
			procShowWindow.Call(hwnd, swShow)
		}
		procBringWindowToTop.Call(hwnd)

		// Attach our calling thread to the current foreground thread's input queue
		// so SetForegroundWindow is permitted despite focus-stealing prevention.
		// NOTE: the attached thread must be *our* thread (GetCurrentThreadId), not
		// the target window's thread — that's what grants us the right to call
		// SetForegroundWindow successfully.
		fg, _, _ := procGetForegroundWindow.Call()
		var fgThread, myThread uintptr
		if fg != 0 {
			fgThread, _, _ = procGetWindowThreadProcessId.Call(fg, 0)
		}
		myThread, _, _ = procGetCurrentThreadId.Call()
		attached := false
		if fgThread != 0 && fgThread != myThread {
			if r, _, _ := procAttachThreadInput.Call(fgThread, myThread, 1); r != 0 {
				attached = true
			}
		}
		procSetForegroundWindow.Call(hwnd)
		if attached {
			procAttachThreadInput.Call(fgThread, myThread, 0)
		}

		time.Sleep(60 * time.Millisecond)
		cur, _, _ := procGetForegroundWindow.Call()
		if cur == hwnd {
			return nil // exact match
		}
		// Credential/composite dialogs (e.g. Windows Security) host the visible
		// PIN dialog as a child HWND. GetForegroundWindow returns the child, not
		// the top-level host we found — accept that as success.
		if cur != 0 {
			if child, _, _ := procIsChild.Call(hwnd, cur); child != 0 {
				return nil
			}
		}
	}
	return fmt.Errorf("could not bring window %q to the foreground", w.Title())
}

// ForegroundWindow returns the top-level window that currently owns keyboard focus.
func (d *winDriver) ForegroundWindow() (Window, error) {
	hwnd, _, _ := procGetForegroundWindow.Call()
	if hwnd == 0 {
		return nil, fmt.Errorf("no foreground window")
	}
	return &winWindow{hwnd: hwnd, title: getWindowText(hwnd)}, nil
}

// ForegroundActive reports whether w is the current foreground window. Cheap
// pre-flight check used to guard input actuation against focus drift.
func (d *winDriver) ForegroundActive(w Window) bool {
	cur, _, _ := procGetForegroundWindow.Call()
	return cur != 0 && cur == w.Handle()
}

// IsTopmost reports whether w currently has the WS_EX_TOPMOST style.
func (d *winDriver) IsTopmost(w Window) bool {
	ex, _, _ := procGetWindowLongW.Call(w.Handle(), gwlExStyle)
	return ex&wsExTopmost != 0
}

// SetTopmost pins w above (or releases it from) all non-topmost windows so a
// stray normal window can't occlude the target during interaction. Owned
// dialogs follow their owner into the topmost band. SWP_NOACTIVATE keeps focus
// where it is; foreground is handled separately by FocusWindow.
func (d *winDriver) SetTopmost(w Window, on bool) error {
	after := hwndNoTopmost
	if on {
		after = hwndTopmost
	}
	res, _, err := procSetWindowPos.Call(w.Handle(), after, 0, 0, 0, 0,
		swpNoMove|swpNoSize|swpNoActivate)
	if res == 0 {
		return errno("SetWindowPos(topmost)", err)
	}
	return nil
}

func (d *winDriver) WindowPID(w Window) uint32 {
	return windowPID(w.Handle())
}

func (d *winDriver) CloseWindow(w Window) error {
	res, _, err := procPostMessageW.Call(w.Handle(), wmClose, 0, 0)
	if res == 0 {
		return errno("PostMessage(WM_CLOSE)", err)
	}
	return nil
}

// geomTolerance is how many pixels bounds may differ and still count as "already there".
const geomTolerance = 2

func intClose(a, b int) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= geomTolerance
}

func (d *winDriver) MoveWindow(w Window, x, y int) error {
	b, err := w.Bounds()
	if err == nil && intClose(b.X, x) && intClose(b.Y, y) {
		return nil // already at target position — don't touch show state
	}
	// A maximized (or minimized) window can't be repositioned meaningfully —
	// restore it to a normal state first so the move actually takes effect.
	unmaximize(w.Handle())
	res, _, err := procSetWindowPos.Call(w.Handle(), 0,
		uintptr(int32(x)), uintptr(int32(y)), 0, 0, swpNoZOrder|swpNoSize)
	if res == 0 {
		return errno("SetWindowPos(move)", err)
	}
	return nil
}

func (d *winDriver) ResizeWindow(w Window, width, height int) error {
	b, err := w.Bounds()
	if err == nil && intClose(b.Width, width) && intClose(b.Height, height) {
		return nil // already at target size — don't unmaximize or resize
	}
	// A maximized window ignores an explicit size; restore it first so the
	// requested geometry (used to keep window-relative coordinates valid) sticks.
	unmaximize(w.Handle())
	res, _, err := procSetWindowPos.Call(w.Handle(), 0, 0, 0,
		uintptr(int32(width)), uintptr(int32(height)), swpNoZOrder|swpNoMove)
	if res == 0 {
		return errno("SetWindowPos(resize)", err)
	}
	return nil
}

// EnsureOnPrimary moves w onto the primary monitor when its center currently
// lies off it (e.g. the app launched on a secondary display). The runner's
// window-relative coordinates and its primary-origin (0,0) calibration assume
// the app lives on the primary monitor, so a drifted window makes clicks land on
// the wrong display. Returns true when a move was performed.
func (d *winDriver) EnsureOnPrimary(w Window) (bool, error) {
	b, err := w.Bounds()
	if err != nil {
		return false, err
	}
	prim := d.ScreenBounds() // primary monitor, origin (0,0)
	cx, cy := b.X+b.Width/2, b.Y+b.Height/2
	if cx >= prim.X && cx < prim.X+prim.Width && cy >= prim.Y && cy < prim.Y+prim.Height {
		return false, nil // already on the primary monitor
	}
	// Anchor the window at the primary origin — the calibration reference point.
	if err := d.MoveWindow(w, prim.X, prim.Y); err != nil {
		return false, err
	}
	return true, nil
}

// unmaximize restores a maximized or minimized window to its normal state so
// move/resize can apply an exact geometry. A no-op for already-normal windows.
func unmaximize(hwnd uintptr) {
	if zoomed, _, _ := procIsZoomed.Call(hwnd); zoomed != 0 {
		procShowWindow.Call(hwnd, swRestore)
		return
	}
	if iconic, _, _ := procIsIconic.Call(hwnd); iconic != 0 {
		procShowWindow.Call(hwnd, swRestore)
	}
}
