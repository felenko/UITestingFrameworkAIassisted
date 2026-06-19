//go:build windows

package platform

import (
	"fmt"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

// vkNames maps non-printable VK codes to canonical key names for recording.
var vkNames = map[uint32]string{
	0x08: "Backspace", 0x09: "Tab", 0x0D: "Enter", 0x1B: "Escape",
	0x20: "Space", 0x21: "PageUp", 0x22: "PageDown",
	0x23: "End", 0x24: "Home",
	0x25: "Left", 0x26: "Up", 0x27: "Right", 0x28: "Down",
	0x2D: "Insert", 0x2E: "Delete",
	0x2C: "PrintScreen",
}

// modifierVKSet holds VK codes that are modifier keys and should not produce
// standalone key_press actions when held in combination with other keys.
var modifierVKSet = map[uint32]bool{
	0x10: true, 0x11: true, 0x12: true, // Shift, Ctrl, Alt
	0xA0: true, 0xA1: true,             // LShift, RShift
	0xA2: true, 0xA3: true,             // LCtrl, RCtrl
	0xA4: true, 0xA5: true,             // LAlt, RAlt
	0x5B: true, 0x5C: true,             // LWin, RWin
	0x14: true,                         // CapsLock
}

func init() {
	for i := 1; i <= 24; i++ {
		vkNames[uint32(0x70+i-1)] = fmt.Sprintf("F%d", i)
	}
}

// recRawEvent is the minimal event sent from a hook proc to the processing goroutine.
type recRawEvent struct {
	kind      byte    // 'm' = mouse, 'k' = key
	wParam    uintptr // WM_* message
	ptX, ptY  int32
	vkCode    uint32
	char      rune // result of ToUnicode; 0 = non-printable
	ctrlDown  bool
	altDown   bool
	shiftDown bool
}

// winInputRecorder installs global low-level hooks and captures physical user
// interactions as RecordedActions with best-effort UIA enrichment for clicks.
type winInputRecorder struct {
	drv       *winDriver
	rawCh     chan recRawEvent    // hook proc → process goroutine
	out       chan RecordedAction // process goroutine → consumer
	threadID  uintptr
	mouseHook uintptr
	keyHook   uintptr
	stopped   uint32
	pumpDone  chan struct{} // closed when pump exits (rawCh is closed before this)
	procDone  chan struct{} // closed when process goroutine exits (out is closed before this)
}

func (d *winDriver) RecordInput() (InputRecorder, error) {
	rec := &winInputRecorder{
		drv:      d,
		rawCh:    make(chan recRawEvent, 512),
		out:      make(chan RecordedAction, 256),
		pumpDone: make(chan struct{}),
		procDone: make(chan struct{}),
	}
	ready := make(chan error, 1)
	go rec.pump(ready)
	if err := <-ready; err != nil {
		return nil, err
	}
	go rec.process()
	return rec, nil
}

func (rec *winInputRecorder) C() <-chan RecordedAction { return rec.out }

func (rec *winInputRecorder) Stop() {
	if !atomic.CompareAndSwapUint32(&rec.stopped, 0, 1) {
		return
	}
	if rec.threadID != 0 {
		procPostThreadMessageW.Call(rec.threadID, wmQuit, 0, 0)
	}
	<-rec.pumpDone // pump exited; rawCh is closed
	<-rec.procDone // process drained rawCh; out is closed
}

// pump installs the hooks and runs the message loop on a locked OS thread.
func (rec *winInputRecorder) pump(ready chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer func() {
		close(rec.rawCh) // signals process to drain and exit
		close(rec.pumpDone)
	}()

	mouseCB := syscall.NewCallback(rec.mouseProc)
	keyCB := syscall.NewCallback(rec.keyProc)

	rec.mouseHook, _, _ = procSetWindowsHookExW.Call(whMouseLL, mouseCB, 0, 0)
	rec.keyHook, _, _ = procSetWindowsHookExW.Call(whKeyboardLL, keyCB, 0, 0)
	if rec.mouseHook == 0 && rec.keyHook == 0 {
		ready <- fmt.Errorf("SetWindowsHookEx: could not install input hooks for recording")
		return
	}
	rec.threadID, _, _ = procGetCurrentThreadId.Call()
	ready <- nil

	var m msg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
	}
	if rec.mouseHook != 0 {
		procUnhookWindowsHookEx.Call(rec.mouseHook)
	}
	if rec.keyHook != 0 {
		procUnhookWindowsHookEx.Call(rec.keyHook)
	}
}

// mouseProc is the low-level mouse hook callback.
func (rec *winInputRecorder) mouseProc(nCode, wParam, lParam uintptr) uintptr {
	if int(nCode) == hcAction {
		hs := (*msllHookStruct)(unsafe.Pointer(lParam))
		if hs.flags&llmhfInjected == 0 {
			switch wParam {
			case wmLButtonDown, wmLButtonDblClk,
				wmRButtonDown, wmRButtonDblClk,
				wmMButtonDown, wmMButtonDblClk:
				select {
				case rec.rawCh <- recRawEvent{kind: 'm', wParam: wParam,
					ptX: hs.ptX, ptY: hs.ptY}:
				default:
				}
			}
		}
	}
	ret, _, _ := procCallNextHookEx.Call(0, nCode, wParam, lParam)
	return ret
}

// keyProc is the low-level keyboard hook callback.
func (rec *winInputRecorder) keyProc(nCode, wParam, lParam uintptr) uintptr {
	if int(nCode) == hcAction && (wParam == wmKeyDown || wParam == wmSysKeyDown) {
		hs := (*kbdllHookStruct)(unsafe.Pointer(lParam))
		if hs.flags&llkhfInjected == 0 {
			vk := hs.vkCode
			// Skip standalone modifier key events.
			if !modifierVKSet[vk] {
				ctrlR, _, _ := procGetKeyState.Call(0x11) // VK_CONTROL
				altR, _, _ := procGetKeyState.Call(0x12)  // VK_MENU
				shiftR, _, _ := procGetKeyState.Call(0x10) // VK_SHIFT
				ctrlDown := int16(ctrlR) < 0
				altDown := int16(altR) < 0
				shiftDown := int16(shiftR) < 0

				var char rune
				if !ctrlDown && !altDown {
					// Try to convert VK to printable Unicode char.
					var kbState [256]byte
					procGetKeyboardState.Call(uintptr(unsafe.Pointer(&kbState[0])))
					var buf [4]uint16
					n, _, _ := procToUnicode.Call(
						uintptr(vk), uintptr(hs.scanCode),
						uintptr(unsafe.Pointer(&kbState[0])),
						uintptr(unsafe.Pointer(&buf[0])), 3, 0)
					if int32(n) == 1 && buf[0] >= 0x20 && buf[0] < 0xFFFE {
						char = rune(buf[0])
					}
				}
				select {
				case rec.rawCh <- recRawEvent{kind: 'k', wParam: wParam,
					vkCode: vk, char: char,
					ctrlDown: ctrlDown, altDown: altDown, shiftDown: shiftDown}:
				default:
				}
			}
		}
	}
	ret, _, _ := procCallNextHookEx.Call(0, nCode, wParam, lParam)
	return ret
}

// process reads from rawCh, enriches clicks with UIA, and emits RecordedActions.
func (rec *winInputRecorder) process() {
	defer close(rec.out)
	defer close(rec.procDone)

	var textBuf strings.Builder
	flushText := func() {
		if textBuf.Len() == 0 {
			return
		}
		select {
		case rec.out <- RecordedAction{At: time.Now(), Action: "type_text", Text: textBuf.String()}:
		default:
		}
		textBuf.Reset()
	}

	for ev := range rec.rawCh {
		switch ev.kind {
		case 'm':
			flushText()
			btn := "left"
			switch ev.wParam {
			case wmRButtonDown, wmRButtonDblClk:
				btn = "right"
			case wmMButtonDown, wmMButtonDblClk:
				btn = "middle"
			}
			pt := Point{X: int(ev.ptX), Y: int(ev.ptY)}
			action := RecordedAction{
				At: time.Now(), Action: "mouse_click",
				X: pt.X, Y: pt.Y, Button: btn, Count: 1,
			}
			// Best-effort UIA enrichment; errors are silently ignored.
			if node, err := rec.drv.ElementAtPoint(pt); err == nil && node.AutomationID != "" {
				action.UIAID = node.AutomationID
				action.UIAName = node.Name
			}
			// Best-effort window title capture so recorded steps get target: {window: "..."}.
			if w, err := rec.drv.ForegroundWindow(); err == nil {
				action.WindowTitle = w.Title()
			}
			select {
			case rec.out <- action:
			default:
			}

		case 'k':
			if ev.ctrlDown || ev.altDown {
				flushText()
				chord := buildChord(ev)
				if chord != "" {
					select {
					case rec.out <- RecordedAction{At: time.Now(), Action: "key_press", Keys: chord}:
					default:
					}
				}
			} else if ev.char != 0 {
				textBuf.WriteRune(ev.char)
			} else {
				flushText()
				if name, ok := vkNames[ev.vkCode]; ok {
					select {
					case rec.out <- RecordedAction{At: time.Now(), Action: "key_press", Keys: name}:
					default:
					}
				}
			}
		}
	}
	flushText() // flush any trailing text on Stop
}

// buildChord constructs a "Ctrl+A" style chord string from a raw key event.
func buildChord(ev recRawEvent) string {
	var parts []string
	if ev.ctrlDown {
		parts = append(parts, "Ctrl")
	}
	if ev.altDown {
		parts = append(parts, "Alt")
	}
	if ev.shiftDown {
		parts = append(parts, "Shift")
	}
	// Key name: check special table first, then A-Z / 0-9.
	if name, ok := vkNames[ev.vkCode]; ok {
		parts = append(parts, name)
	} else if ev.vkCode >= 'A' && ev.vkCode <= 'Z' {
		parts = append(parts, string(rune(ev.vkCode)))
	} else if ev.vkCode >= '0' && ev.vkCode <= '9' {
		parts = append(parts, string(rune(ev.vkCode)))
	} else {
		return "" // unknown key; skip
	}
	return strings.Join(parts, "+")
}
