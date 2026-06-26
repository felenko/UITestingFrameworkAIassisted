//go:build windows

package platform

import "golang.org/x/sys/windows"

// Lazily-loaded system DLLs and the procedures we call. Centralised so every
// Windows file shares one handle per proc.
var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	shcore   = windows.NewLazySystemDLL("shcore.dll")

	// Input.
	procSendInput    = user32.NewProc("SendInput")
	procSetCursorPos = user32.NewProc("SetCursorPos")
	procGetCursorPos = user32.NewProc("GetCursorPos")
	procVkKeyScanW   = user32.NewProc("VkKeyScanW")

	// Keyboard state / character mapping (used by the input recorder).
	procGetKeyState      = user32.NewProc("GetKeyState")
	procGetKeyboardState = user32.NewProc("GetKeyboardState")
	procToUnicode        = user32.NewProc("ToUnicode")

	// Low-level input hooks (user-intervention detection).
	procSetWindowsHookExW   = user32.NewProc("SetWindowsHookExW")
	procUnhookWindowsHookEx = user32.NewProc("UnhookWindowsHookEx")
	procCallNextHookEx      = user32.NewProc("CallNextHookEx")
	procGetMessageW         = user32.NewProc("GetMessageW")
	procPostThreadMessageW  = user32.NewProc("PostThreadMessageW")
	procGetCurrentThreadId  = kernel32.NewProc("GetCurrentThreadId")

	// Metrics / DPI.
	procGetSystemMetrics            = user32.NewProc("GetSystemMetrics")
	procSetProcessDPIAware          = user32.NewProc("SetProcessDPIAware")
	procSetProcessDpiAwareCtx       = user32.NewProc("SetProcessDpiAwarenessContext")
	procGetDpiForSystem             = user32.NewProc("GetDpiForSystem")

	// Windows enumeration & control.
	procEnumWindows             = user32.NewProc("EnumWindows")
	procGetWindowTextW          = user32.NewProc("GetWindowTextW")
	procGetWindowTextLengthW    = user32.NewProc("GetWindowTextLengthW")
	procGetClassNameW           = user32.NewProc("GetClassNameW")
	procIsWindowVisible         = user32.NewProc("IsWindowVisible")
	procGetWindowThreadProcessId = user32.NewProc("GetWindowThreadProcessId")
	procGetWindowRect           = user32.NewProc("GetWindowRect")
	procSetForegroundWindow     = user32.NewProc("SetForegroundWindow")
	procShowWindow              = user32.NewProc("ShowWindow")
	procBringWindowToTop        = user32.NewProc("BringWindowToTop")
	procIsIconic                = user32.NewProc("IsIconic")
	procIsZoomed                = user32.NewProc("IsZoomed")
	procPostMessageW            = user32.NewProc("PostMessageW")
	procSetWindowPos            = user32.NewProc("SetWindowPos")
	procAttachThreadInput       = user32.NewProc("AttachThreadInput")
	procGetForegroundWindow     = user32.NewProc("GetForegroundWindow")
	procIsChild                 = user32.NewProc("IsChild")
	procSystemParametersInfo    = user32.NewProc("SystemParametersInfoW")
	procGetWindowLongW          = user32.NewProc("GetWindowLongW")

	// Device context / capture.
	procGetDC               = user32.NewProc("GetDC")
	procReleaseDC           = user32.NewProc("ReleaseDC")
	procCreateCompatibleDC  = gdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBmp = gdi32.NewProc("CreateCompatibleBitmap")
	procSelectObject        = gdi32.NewProc("SelectObject")
	procBitBlt              = gdi32.NewProc("BitBlt")
	procDeleteObject        = gdi32.NewProc("DeleteObject")
	procDeleteDC            = gdi32.NewProc("DeleteDC")
	procGetDIBits           = gdi32.NewProc("GetDIBits")
	procGetDeviceCaps       = gdi32.NewProc("GetDeviceCaps")
	procPrintWindow         = user32.NewProc("PrintWindow")

	// Process query.
	procOpenProcess               = kernel32.NewProc("OpenProcess")
	procCloseHandle               = kernel32.NewProc("CloseHandle")
	procQueryFullProcessImageName = kernel32.NewProc("QueryFullProcessImageNameW")

	// Process enumeration (for process_running condition).
	procCreateToolhelp32Snapshot = kernel32.NewProc("CreateToolhelp32Snapshot")
	procProcess32FirstW          = kernel32.NewProc("Process32FirstW")
	procProcess32NextW           = kernel32.NewProc("Process32NextW")
)

// Win32 constants used across the driver.
const (
	inputMouse    = 0
	inputKeyboard = 1

	mouseeventfMove       = 0x0001
	mouseeventfLeftDown   = 0x0002
	mouseeventfLeftUp     = 0x0004
	mouseeventfRightDown  = 0x0008
	mouseeventfRightUp    = 0x0010
	mouseeventfMiddleDown = 0x0020
	mouseeventfMiddleUp   = 0x0040
	mouseeventfWheel      = 0x0800
	mouseeventfHWheel     = 0x01000
	wheelDelta            = 120

	keyeventfExtended = 0x0001
	keyeventfKeyUp    = 0x0002
	keyeventfUnicode  = 0x0004
	keyeventfScancode = 0x0008

	smCXScreen        = 0
	smCYScreen        = 1
	smXVirtualScreen  = 76
	smYVirtualScreen  = 77
	smCXVirtualScreen = 78
	smCYVirtualScreen = 79

	swRestore = 9
	swShow    = 5

	vkMenu                      = 0x12 // ALT
	spiSetForegroundLockTimeout = 0x2001
	spifSendChange              = 0x2

	swpNoZOrder    = 0x0004
	swpNoSize      = 0x0001
	swpNoMove      = 0x0002
	swpNoActivate  = 0x0010

	// Z-order: SetWindowPos hWndInsertAfter sentinels and the topmost ex-style.
	hwndTopmost   = ^uintptr(0)  // (HWND)-1
	hwndNoTopmost = ^uintptr(1)  // (HWND)-2
	gwlExStyle    = ^uintptr(19) // GWL_EXSTYLE = -20, as a uintptr bit pattern
	wsExTopmost   = 0x00000008

	wmClose = 0x0010

	srccopy            = 0x00CC0020
	pwRenderFullContent = 0x00000002
	biRGB              = 0
	dibRGBColors = 0
	logpixelsx   = 88

	processQueryLimitedInformation = 0x1000

	// Low-level hooks.
	whKeyboardLL  = 13
	whMouseLL     = 14
	hcAction      = 0
	llkhfInjected = 0x10 // KBDLLHOOKSTRUCT.flags: synthetic key event
	llmhfInjected = 0x01 // MSLLHOOKSTRUCT.flags: synthetic mouse event
	wmQuit        = 0x0012
	wmMouseMove   = 0x0200
	wmKeyDown     = 0x0100
	wmKeyUp       = 0x0101
	wmSysKeyDown  = 0x0104
	wmSysKeyUp    = 0x0105

	// Mouse button WM_ messages (for the input recorder).
	wmLButtonDown   = 0x0201
	wmLButtonDblClk = 0x0203
	wmRButtonDown   = 0x0204
	wmRButtonDblClk = 0x0206
	wmMButtonDown   = 0x0207
	wmMButtonDblClk = 0x0209

	// SetProcessDpiAwarenessContext value: PER_MONITOR_AWARE_V2 = -4.
	dpiAwarenessContextPerMonitorV2 = ^uintptr(3) // (HANDLE)-4
)

// rect mirrors the Win32 RECT.
type rect struct {
	Left, Top, Right, Bottom int32
}
