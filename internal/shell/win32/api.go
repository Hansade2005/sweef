//go:build windows

package win32

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// Raw Win32 bindings. Everything is loaded lazily from the system directory
// (NewLazySystemDLL), so a missing optional export (newer-Windows APIs) only
// fails when it is first used; callers check with proc.Find() where the API
// is optional.

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	shcore   = windows.NewLazySystemDLL("shcore.dll")
	dwmapi   = windows.NewLazySystemDLL("dwmapi.dll")

	pRegisterClassExW              = user32.NewProc("RegisterClassExW")
	pCreateWindowExW               = user32.NewProc("CreateWindowExW")
	pDestroyWindow                 = user32.NewProc("DestroyWindow")
	pDefWindowProcW                = user32.NewProc("DefWindowProcW")
	pGetMessageW                   = user32.NewProc("GetMessageW")
	pTranslateMessage              = user32.NewProc("TranslateMessage")
	pDispatchMessageW              = user32.NewProc("DispatchMessageW")
	pPostMessageW                  = user32.NewProc("PostMessageW")
	pPostQuitMessage               = user32.NewProc("PostQuitMessage")
	pSendMessageW                  = user32.NewProc("SendMessageW")
	pShowWindow                    = user32.NewProc("ShowWindow")
	pSetWindowTextW                = user32.NewProc("SetWindowTextW")
	pGetClientRect                 = user32.NewProc("GetClientRect")
	pSetWindowPos                  = user32.NewProc("SetWindowPos")
	pGetWindowRect                 = user32.NewProc("GetWindowRect")
	pAdjustWindowRectEx            = user32.NewProc("AdjustWindowRectEx")
	pAdjustWindowRectExFD          = user32.NewProc("AdjustWindowRectExForDpi")
	pGetDC                         = user32.NewProc("GetDC")
	pReleaseDC                     = user32.NewProc("ReleaseDC")
	pBeginPaint                    = user32.NewProc("BeginPaint")
	pEndPaint                      = user32.NewProc("EndPaint")
	pValidateRect                  = user32.NewProc("ValidateRect")
	pLoadCursorW                   = user32.NewProc("LoadCursorW")
	pSetCursor                     = user32.NewProc("SetCursor")
	pSetCapture                    = user32.NewProc("SetCapture")
	pReleaseCapture                = user32.NewProc("ReleaseCapture")
	pTrackMouseEvent               = user32.NewProc("TrackMouseEvent")
	pGetKeyState                   = user32.NewProc("GetKeyState")
	pGetKeyboardState              = user32.NewProc("GetKeyboardState")
	pGetKeyboardLayout             = user32.NewProc("GetKeyboardLayout")
	pToUnicodeEx                   = user32.NewProc("ToUnicodeEx")
	pMapVirtualKeyW                = user32.NewProc("MapVirtualKeyW")
	pScreenToClient                = user32.NewProc("ScreenToClient")
	pGetCursorPos                  = user32.NewProc("GetCursorPos")
	pOpenClipboard                 = user32.NewProc("OpenClipboard")
	pCloseClipboard                = user32.NewProc("CloseClipboard")
	pEmptyClipboard                = user32.NewProc("EmptyClipboard")
	pSetClipboardData              = user32.NewProc("SetClipboardData")
	pGetClipboardData              = user32.NewProc("GetClipboardData")
	pIsClipboardFormatAvail        = user32.NewProc("IsClipboardFormatAvailable")
	pRegisterClipboardFormatW      = user32.NewProc("RegisterClipboardFormatW")
	pSystemParametersInfoW         = user32.NewProc("SystemParametersInfoW")
	pMonitorFromWindow             = user32.NewProc("MonitorFromWindow")
	pGetMonitorInfoW               = user32.NewProc("GetMonitorInfoW")
	pGetWindowPlacement            = user32.NewProc("GetWindowPlacement")
	pSetWindowPlacement            = user32.NewProc("SetWindowPlacement")
	pGetWindowLongW                = user32.NewProc("GetWindowLongW")
	pSetWindowLongW                = user32.NewProc("SetWindowLongW")
	pGetWindowLongPtrW             = user32.NewProc("GetWindowLongPtrW")
	pSetWindowLongPtrW             = user32.NewProc("SetWindowLongPtrW")
	pSetProcessDPIAware            = user32.NewProc("SetProcessDPIAware")
	pSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	pGetDpiForWindow               = user32.NewProc("GetDpiForWindow")
	pGetDpiForSystem               = user32.NewProc("GetDpiForSystem")
	pCreateIconIndirect            = user32.NewProc("CreateIconIndirect")
	pDestroyIcon                   = user32.NewProc("DestroyIcon")
	pInvalidateRect                = user32.NewProc("InvalidateRect")
	pGetSystemMetrics              = user32.NewProc("GetSystemMetrics")
	pSetForegroundWindow           = user32.NewProc("SetForegroundWindow")

	pCreateCompatibleDC = gdi32.NewProc("CreateCompatibleDC")
	pDeleteDC           = gdi32.NewProc("DeleteDC")
	pCreateDIBSection   = gdi32.NewProc("CreateDIBSection")
	pCreateBitmap       = gdi32.NewProc("CreateBitmap")
	pSelectObject       = gdi32.NewProc("SelectObject")
	pDeleteObject       = gdi32.NewProc("DeleteObject")
	pBitBlt             = gdi32.NewProc("BitBlt")
	pGdiFlush           = gdi32.NewProc("GdiFlush")
	pGetDeviceCaps      = gdi32.NewProc("GetDeviceCaps")

	pGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")
	pGlobalAlloc      = kernel32.NewProc("GlobalAlloc")
	pGlobalFree       = kernel32.NewProc("GlobalFree")
	pGlobalLock       = kernel32.NewProc("GlobalLock")
	pGlobalUnlock     = kernel32.NewProc("GlobalUnlock")
	pGlobalSize       = kernel32.NewProc("GlobalSize")

	pShellExecuteW   = shell32.NewProc("ShellExecuteW")
	pDragAcceptFiles = shell32.NewProc("DragAcceptFiles")
	pDragQueryFileW  = shell32.NewProc("DragQueryFileW")
	pDragQueryPoint  = shell32.NewProc("DragQueryPoint")
	pDragFinish      = shell32.NewProc("DragFinish")

	pSetProcessDpiAwareness = shcore.NewProc("SetProcessDpiAwareness")

	pDwmSetWindowAttribute = dwmapi.NewProc("DwmSetWindowAttribute")
)

// Window messages and constants (winuser.h).
const (
	wmDestroy        = 0x0002
	wmSize           = 0x0005
	wmSetFocus       = 0x0007
	wmKillFocus      = 0x0008
	wmPaint          = 0x000F
	wmClose          = 0x0010
	wmEraseBkgnd     = 0x0014
	wmSetCursor      = 0x0020
	wmGetMinMaxInfo  = 0x0024
	wmSetIcon        = 0x0080
	wmNCCreate       = 0x0081
	wmKeyDown        = 0x0100
	wmKeyUp          = 0x0101
	wmChar           = 0x0102
	wmDeadChar       = 0x0103
	wmSysKeyDown     = 0x0104
	wmSysKeyUp       = 0x0105
	wmSysChar        = 0x0106
	wmUniChar        = 0x0109
	wmIMEStartComp   = 0x010D
	wmIMEEndComp     = 0x010E
	wmIMEComposition = 0x010F
	wmMouseMove      = 0x0200
	wmLButtonDown    = 0x0201
	wmLButtonUp      = 0x0202
	wmRButtonDown    = 0x0204
	wmRButtonUp      = 0x0205
	wmMButtonDown    = 0x0207
	wmMButtonUp      = 0x0208
	wmMouseWheel     = 0x020A
	wmXButtonDown    = 0x020B
	wmXButtonUp      = 0x020C
	wmMouseHWheel    = 0x020E
	wmMouseLeave     = 0x02A3
	wmDropFiles      = 0x0233
	wmDPIChanged     = 0x02E0
	wmApp            = 0x8000
	wmUser           = 0x0400

	sizeMinimized = 1

	csHRedraw = 0x0002
	csVRedraw = 0x0001

	wsOverlappedWindow = 0x00CF0000
	wsClipChildren     = 0x02000000
	wsClipSiblings     = 0x04000000
	wsVisible          = 0x10000000
	wsExAppWindow      = 0x00040000
	cwUseDefault       = 0x80000000

	swShowNormal = 1
	swShow       = 5

	srccopy = 0x00CC0020

	biRGB        = 0
	dibRGBColors = 0

	swpNoSize        = 0x0001
	swpNoMove        = 0x0002
	swpNoZOrder      = 0x0004
	swpNoActivate    = 0x0010
	swpFrameChanged  = 0x0020
	swpNoOwnerZOrder = 0x0200

	gwlStyle = -16

	monitorDefaultToNearest = 2

	tmeLeave = 0x00000002

	idcArrow   = 32512
	idcIBeam   = 32513
	idcWait    = 32514
	idcCross   = 32515
	idcSizeWE  = 32644
	idcSizeNS  = 32645
	idcSizeAll = 32646
	idcNo      = 32648
	idcHand    = 32649

	htClient = 1

	cfUnicodeText = 13

	gmemMoveable = 0x0002

	spiGetWheelScrollLines = 0x0068

	vkShift   = 0x10
	vkControl = 0x11
	vkMenu    = 0x12
	vkLWin    = 0x5B
	vkRWin    = 0x5C

	dwmwaUseImmersiveDarkModeOld = 19
	dwmwaUseImmersiveDarkMode    = 20

	// DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 is the handle value -4.
	dpiPerMonitorAwareV2 = ^uintptr(3)

	lwaDefault = 0
)

// ptrFrom turns an OS-returned address into an unsafe.Pointer without
// tripping vet's uintptr->Pointer check; the memory is not Go memory.
func ptrFrom(u uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&u)) }

func loword(l uintptr) uint16 { return uint16(l) }
func hiword(l uintptr) uint16 { return uint16(l >> 16) }
func getX(l uintptr) int      { return int(int16(loword(l))) }
func getY(l uintptr) int      { return int(int16(hiword(l))) }

func boolArg(b bool) uintptr {
	if b {
		return 1
	}
	return 0
}

// getWindowLong reads a GWL_* value, using the Ptr variant on 64-bit.
func getWindowLong(hwnd uintptr, idx int32) uintptr {
	if pGetWindowLongPtrW.Find() == nil {
		r, _, _ := pGetWindowLongPtrW.Call(hwnd, uintptr(idx))
		return r
	}
	r, _, _ := pGetWindowLongW.Call(hwnd, uintptr(idx))
	return r
}

func setWindowLong(hwnd uintptr, idx int32, v uintptr) {
	if pSetWindowLongPtrW.Find() == nil {
		pSetWindowLongPtrW.Call(hwnd, uintptr(idx), v)
		return
	}
	pSetWindowLongW.Call(hwnd, uintptr(idx), v)
}
