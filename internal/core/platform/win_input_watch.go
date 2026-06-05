//go:build windows

package platform

import (
	"fmt"
	"runtime"
	"sync/atomic"
	"syscall"
	"unsafe"
)

// winInputWatcher installs global low-level mouse + keyboard hooks and counts
// only *physical* user events — events whose injected flag is clear. The
// synthetic input the framework drives via SendInput is flagged injected and is
// ignored, so a non-zero delta over an action means a person intervened.
//
// Low-level hooks must be serviced by a thread that pumps the message queue, so
// the watcher owns one OS-locked goroutine for its whole lifetime.
type winInputWatcher struct {
	count     uint64 // atomic: physical user events observed
	threadID  uintptr
	mouseHook uintptr
	keyHook   uintptr
	stopped   uint32 // atomic guard so Stop is idempotent
	done      chan struct{}
}

// msg mirrors the Win32 MSG structure for GetMessageW.
type msg struct {
	hwnd     uintptr
	message  uint32
	_        uint32 // padding to 8-byte align wParam on amd64
	wParam   uintptr
	lParam   uintptr
	time     uint32
	ptX      int32
	ptY      int32
	lPrivate uint32
}

// msllHookStruct mirrors Win32 MSLLHOOKSTRUCT (low-level mouse hook payload).
type msllHookStruct struct {
	ptX         int32
	ptY         int32
	mouseData   uint32
	flags       uint32
	time        uint32
	dwExtraInfo uintptr
}

// kbdllHookStruct mirrors Win32 KBDLLHOOKSTRUCT (low-level keyboard hook payload).
type kbdllHookStruct struct {
	vkCode      uint32
	scanCode    uint32
	flags       uint32
	time        uint32
	dwExtraInfo uintptr
}

func (d *winDriver) WatchInput() (InputWatcher, error) {
	w := &winInputWatcher{done: make(chan struct{})}
	ready := make(chan error, 1)
	go w.pump(ready)
	if err := <-ready; err != nil {
		return nil, err
	}
	return w, nil
}

// pump installs the hooks and runs the message loop on a single locked thread.
func (w *winInputWatcher) pump(ready chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(w.done)

	mouseCB := syscall.NewCallback(w.mouseProc)
	keyCB := syscall.NewCallback(w.keyProc)

	w.mouseHook, _, _ = procSetWindowsHookExW.Call(whMouseLL, mouseCB, 0, 0)
	w.keyHook, _, _ = procSetWindowsHookExW.Call(whKeyboardLL, keyCB, 0, 0)
	if w.mouseHook == 0 && w.keyHook == 0 {
		ready <- fmt.Errorf("SetWindowsHookEx: could not install input hooks")
		return
	}
	w.threadID, _, _ = procGetCurrentThreadId.Call()
	ready <- nil

	var m msg
	for {
		// GetMessage returns 0 on WM_QUIT (our Stop signal) and -1 on error.
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
	}

	if w.mouseHook != 0 {
		procUnhookWindowsHookEx.Call(w.mouseHook)
	}
	if w.keyHook != 0 {
		procUnhookWindowsHookEx.Call(w.keyHook)
	}
}

// mouseProc counts physical button/wheel events (moves are ignored — our own
// SetCursorPos generates moves that are not reliably flagged injected).
func (w *winInputWatcher) mouseProc(nCode, wParam, lParam uintptr) uintptr {
	if int(nCode) == hcAction && wParam != wmMouseMove {
		// lParam is a live *MSLLHOOKSTRUCT for the callback's duration. `go vet`
		// flags the uintptr->Pointer conversion (it can't prove the param is a
		// pointer); the conversion is safe and standard for low-level hooks.
		hs := (*msllHookStruct)(unsafe.Pointer(lParam))
		if hs.flags&llmhfInjected == 0 {
			atomic.AddUint64(&w.count, 1)
		}
	}
	ret, _, _ := procCallNextHookEx.Call(0, nCode, wParam, lParam)
	return ret
}

// keyProc counts physical key-presses (key-down only, to count one per stroke).
func (w *winInputWatcher) keyProc(nCode, wParam, lParam uintptr) uintptr {
	if int(nCode) == hcAction && (wParam == wmKeyDown || wParam == wmSysKeyDown) {
		// See mouseProc: lParam is a live *KBDLLHOOKSTRUCT; the vet warning on
		// this uintptr->Pointer conversion is a known false positive.
		hs := (*kbdllHookStruct)(unsafe.Pointer(lParam))
		if hs.flags&llkhfInjected == 0 {
			atomic.AddUint64(&w.count, 1)
		}
	}
	ret, _, _ := procCallNextHookEx.Call(0, nCode, wParam, lParam)
	return ret
}

func (w *winInputWatcher) UserEvents() uint64 {
	return atomic.LoadUint64(&w.count)
}

func (w *winInputWatcher) Stop() {
	if !atomic.CompareAndSwapUint32(&w.stopped, 0, 1) {
		return
	}
	if w.threadID != 0 {
		procPostThreadMessageW.Call(w.threadID, wmQuit, 0, 0)
	}
	<-w.done
}
