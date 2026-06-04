//go:build windows

package main

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	comdlg32             = windows.NewLazySystemDLL("comdlg32.dll")
	procGetOpenFileNameW = comdlg32.NewProc("GetOpenFileNameW")
)

// openFilenameW mirrors Win32 OPENFILENAMEW.
type openFilenameW struct {
	lStructSize       uint32
	hwndOwner         uintptr
	hInstance         uintptr
	lpstrFilter       *uint16
	lpstrCustomFilter *uint16
	nMaxCustFilter    uint32
	nFilterIndex      uint32
	lpstrFile         *uint16
	nMaxFile          uint32
	lpstrFileTitle    *uint16
	nMaxFileTitle     uint32
	lpstrInitialDir   *uint16
	lpstrTitle        *uint16
	flags             uint32
	nFileOffset       uint16
	nFileExtension    uint16
	lpstrDefExt       *uint16
	lCustData         uintptr
	lpfnHook          uintptr
	lpTemplateName    *uint16
	pvReserved        uintptr
	dwReserved        uint32
	flagsEx           uint32
}

const (
	ofnFileMustExist = 0x00001000
	ofnPathMustExist = 0x00000800
	ofnExplorer      = 0x00080000
)

// openFileDialog shows a native "Open" dialog filtered to YAML files.
func openFileDialog() (string, error) {
	buf := make([]uint16, 4096)
	// Filter: "Test sessions\0*.yaml;*.yml\0All files\0*.*\0\0"
	filter := utf16z("Test sessions (*.yaml;*.yml)")
	filter = append(filter, utf16z("*.yaml;*.yml")...)
	filter = append(filter, utf16z("All files (*.*)")...)
	filter = append(filter, utf16z("*.*")...)
	filter = append(filter, 0) // double-null terminator

	title, _ := syscall.UTF16PtrFromString("Open Test Session")

	ofn := openFilenameW{
		lStructSize:  uint32(unsafe.Sizeof(openFilenameW{})),
		lpstrFilter:  &filter[0],
		nFilterIndex: 1,
		lpstrFile:    &buf[0],
		nMaxFile:     uint32(len(buf)),
		lpstrTitle:   title,
		flags:        ofnFileMustExist | ofnPathMustExist | ofnExplorer,
	}

	r, _, _ := procGetOpenFileNameW.Call(uintptr(unsafe.Pointer(&ofn)))
	if r == 0 {
		// User cancelled or error: return empty (not an error).
		return "", nil
	}
	return syscall.UTF16ToString(buf), nil
}

// utf16z encodes a string to UTF-16 with a trailing null (one segment of a
// double-null-terminated filter list).
func utf16z(s string) []uint16 {
	u := utf16FromString(s)
	return append(u, 0)
}

func utf16FromString(s string) []uint16 {
	u, err := syscall.UTF16FromString(s)
	if err != nil {
		return []uint16{0}
	}
	// UTF16FromString already adds a trailing null; strip it so callers control it.
	if len(u) > 0 && u[len(u)-1] == 0 {
		u = u[:len(u)-1]
	}
	return u
}
