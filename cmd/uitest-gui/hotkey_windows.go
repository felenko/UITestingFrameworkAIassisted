//go:build windows

package main

import (
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"github.com/felenko/uitest/internal/core/event"
	"github.com/felenko/uitest/internal/core/runner"
	"golang.org/x/sys/windows"
)

// Global keyboard hook for debug-mode hotkeys.
//
//   F10  Step — execute the current command and pause before the next one.
//   F5   Run  — execute commands until the next breakpoint (or end of case).
//   F9   Toggle breakpoint at the command the runner is currently paused on.
//
// The hook goroutine is started once (lazily) and runs for the process lifetime.
// It is transparent when not debugging: F5/F10/F9 pass through unless
// verdictCh is non-nil (i.e. the runner is actively waiting for a verdict).

const (
	dbgVkF5         = uint32(0x74)
	dbgVkF9         = uint32(0x78)
	dbgVkF10        = uint32(0x79)
	dbgWHKeyboardLL = 13
	dbgWMKeyDown    = uintptr(0x0100)
	dbgWMQuit       = uintptr(0x0012)
)

var (
	dbgKernel32              = windows.NewLazySystemDLL("kernel32.dll")
	dbgProcSetHookEx         = user32.NewProc("SetWindowsHookExW")
	dbgProcUnhookHook        = user32.NewProc("UnhookWindowsHookEx")
	dbgProcCallNextHook      = user32.NewProc("CallNextHookEx")
	dbgProcGetMessage        = user32.NewProc("GetMessageW")
	dbgProcGetCurrentThread  = dbgKernel32.NewProc("GetCurrentThreadId")
)

var (
	dbgHkMu   sync.Mutex
	dbgHkCtrl *debugCtrl
	dbgHkHook uintptr
	dbgHkCh   = make(chan uint32, 16)
	dbgHkOnce sync.Once
	dbgHkCbPtr uintptr
)

type dbgKbdStruct struct {
	vkCode      uint32
	scanCode    uint32
	flags       uint32
	time        uint32
	dwExtraInfo uintptr
}

func init() {
	dbgHkCbPtr = syscall.NewCallback(dbgKeyboardHookProc)
}

// setDebugHotkeyCtrl wires the active debug controller into the hook. Call
// with ctrl != nil when a debug run starts, nil when it ends.
func setDebugHotkeyCtrl(ctrl *debugCtrl) {
	dbgHkMu.Lock()
	dbgHkCtrl = ctrl
	dbgHkMu.Unlock()
	if ctrl != nil {
		dbgHkOnce.Do(func() {
			go dbgHotkeyLoop()
			go dbgHotkeyHandler()
		})
	}
}

// dbgKeyboardHookProc is the WH_KEYBOARD_LL callback. Runs in the hook
// goroutine's message loop — must be fast and non-blocking.
func dbgKeyboardHookProc(code, wParam, lParam uintptr) uintptr {
	if code >= 0 && wParam == dbgWMKeyDown {
		ks := (*dbgKbdStruct)(unsafe.Pointer(lParam))
		vk := ks.vkCode
		if vk == dbgVkF5 || vk == dbgVkF9 || vk == dbgVkF10 {
			dbgHkMu.Lock()
			ctrl := dbgHkCtrl
			dbgHkMu.Unlock()
			if ctrl != nil {
				ctrl.mu.Lock()
				paused := ctrl.verdictCh != nil
				ctrl.mu.Unlock()
				if paused {
					select {
					case dbgHkCh <- vk:
					default:
					}
					// Swallow F5 and F10 so they don't reach the tested app.
					if vk == dbgVkF5 || vk == dbgVkF10 {
						return 1
					}
				}
			}
		}
	}
	r, _, _ := dbgProcCallNextHook.Call(dbgHkHook, code, wParam, lParam)
	return r
}

// dbgHotkeyLoop runs the message pump that keeps the LL hook alive.
// Must stay pinned to its OS thread.
func dbgHotkeyLoop() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	h, _, _ := dbgProcSetHookEx.Call(dbgWHKeyboardLL, dbgHkCbPtr, 0, 0)
	if h == 0 {
		return
	}
	dbgHkMu.Lock()
	dbgHkHook = h
	dbgHkMu.Unlock()
	defer dbgProcUnhookHook.Call(h)

	type msg struct {
		hwnd, message, wParam, lParam uintptr
		time                          uint32
		pt                            [2]int32
	}
	var m msg
	for {
		r, _, _ := dbgProcGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if r == 0 || r == ^uintptr(0) || m.message == dbgWMQuit {
			return
		}
	}
}

// dbgHotkeyHandler processes keys from the channel in a normal goroutine where
// it is safe to call arbitrary Go code.
func dbgHotkeyHandler() {
	for vk := range dbgHkCh {
		dbgHkMu.Lock()
		ctrl := dbgHkCtrl
		dbgHkMu.Unlock()
		if ctrl == nil {
			continue
		}
		switch vk {
		case dbgVkF10:
			ctrl.sendVerdictMode(runner.VerdictRun, "step")
		case dbgVkF5:
			ctrl.sendVerdictMode(runner.VerdictRun, "run")
		case dbgVkF9:
			ctrl.mu.Lock()
			caseID := ctrl.curCaseID
			stepIdx := ctrl.curStepIdx
			cmdIdx := ctrl.curCmdIdx
			ctrl.mu.Unlock()
			active := ctrl.toggleBreakpoint(caseID, stepIdx, cmdIdx)
			ctrl.a.pushEvent(event.Event{
				Type:             event.BreakpointToggled,
				CaseID:           caseID,
				StepIndex:        stepIdx,
				CmdIndex:         cmdIdx,
				BreakpointActive: active,
			})
		}
	}
}
