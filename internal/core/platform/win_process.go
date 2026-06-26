//go:build windows

package platform

import (
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	th32csSnapProcess = 0x00000002
	maxPathChars      = 260 // MAX_PATH
)

// processEntry32W mirrors the Win32 PROCESSENTRY32W struct.
type processEntry32W struct {
	dwSize              uint32
	cntUsage            uint32
	th32ProcessID       uint32
	th32DefaultHeapID   uintptr
	th32ModuleID        uint32
	cntThreads          uint32
	th32ParentProcessID uint32
	pcPriClassBase      int32
	dwFlags             uint32
	szExeFile           [maxPathChars]uint16
}

// ProcessRunning reports whether any running process has an executable name
// equal to name (case-insensitive, e.g. "AdvancedInstaller.exe").
func (d *winDriver) ProcessRunning(name string) bool {
	snap, _, _ := procCreateToolhelp32Snapshot.Call(th32csSnapProcess, 0)
	if snap == uintptr(windows.InvalidHandle) {
		return false
	}
	defer procCloseHandle.Call(snap)

	var pe processEntry32W
	pe.dwSize = uint32(unsafe.Sizeof(pe))
	ret, _, _ := procProcess32FirstW.Call(snap, uintptr(unsafe.Pointer(&pe)))
	for ret != 0 {
		if strings.EqualFold(windows.UTF16ToString(pe.szExeFile[:]), name) {
			return true
		}
		ret, _, _ = procProcess32NextW.Call(snap, uintptr(unsafe.Pointer(&pe)))
	}
	return false
}
