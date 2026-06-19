//go:build windows

package main

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32                = windows.NewLazySystemDLL("user32.dll")
	procSetWindowLongPtrW = user32.NewProc("SetWindowLongPtrW")
	procCallWindowProcW   = user32.NewProc("CallWindowProcW")
	procMessageBoxW       = user32.NewProc("MessageBoxW")
)

const (
	wmClose = 0x0010

	gwlpWndProc = ^uintptr(3) // GWLP_WNDPROC = -4, as uintptr

	mbYesNo        = 0x0004
	mbIconQuestion = 0x0020
	mbDefButton2   = 0x0100
	idYes          = 6
)

var (
	closeGuardApp *app
	origWndProc   uintptr
)

// installCloseGuard subclasses the webview window so WM_CLOSE (X button,
// Alt-F4, taskbar close, graceful taskkill — and a regression script hitting
// the runner window by mistake) is intercepted:
//   - while a run is active: asks "Cancel the session and close?" (default No);
//     if confirmed, cancels the run and lets the window close.
//   - when idle: asks "Do you really want to close?" (default No).
//
// Must be called on the thread that owns the window (main, before w.Run()).
func installCloseGuard(a *app, hwnd uintptr) {
	if hwnd == 0 {
		logLifecycle("close guard: no window handle — guard not installed")
		return
	}
	closeGuardApp = a
	cb := syscall.NewCallback(guardWndProc)
	prev, _, err := procSetWindowLongPtrW.Call(hwnd, gwlpWndProc, cb)
	if prev == 0 {
		logLifecycle(fmt.Sprintf("close guard: SetWindowLongPtrW failed: %v", err))
		return
	}
	origWndProc = prev
	logLifecycle("close guard installed")
}

// guardWndProc filters WM_CLOSE and forwards everything else (and permitted
// closes) to the webview's original window procedure.
func guardWndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	if msg == wmClose {
		a := closeGuardApp
		if a != nil && a.isRunning() {
			if !confirmCancelAndClose(hwnd) {
				logLifecycle("close blocked: user declined to cancel running session")
				return 0
			}
			logLifecycle("close confirmed by user — cancelling running session")
			_ = a.cancelRun()
			// fall through: let WebView2 process the close normally
		} else if !confirmClose(hwnd) {
			logLifecycle("close cancelled by user")
			return 0
		} else {
			logLifecycle("close confirmed by user")
		}
	}
	ret, _, _ := procCallWindowProcW.Call(origWndProc, hwnd, msg, wparam, lparam)
	return ret
}

// confirmClose shows a Yes/No prompt owned by the runner window. Defaults to
// No so a stray Enter (e.g. simulated input) does not close the runner.
func confirmClose(hwnd uintptr) bool {
	text, _ := syscall.UTF16PtrFromString("Do you really want to close the test runner?")
	title, _ := syscall.UTF16PtrFromString("uitest — confirm close")
	r, _, _ := procMessageBoxW.Call(hwnd,
		uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)),
		mbYesNo|mbIconQuestion|mbDefButton2)
	return r == idYes
}

// confirmCancelAndClose asks whether to cancel the in-progress run and close.
// Defaults to No so a stray Enter from simulated input cannot abort the session.
func confirmCancelAndClose(hwnd uintptr) bool {
	text, _ := syscall.UTF16PtrFromString("A test session is in progress.\n\nCancel the session and close the runner?")
	title, _ := syscall.UTF16PtrFromString("uitest — cancel session?")
	r, _, _ := procMessageBoxW.Call(hwnd,
		uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)),
		mbYesNo|mbIconQuestion|mbDefButton2)
	return r == idYes
}
