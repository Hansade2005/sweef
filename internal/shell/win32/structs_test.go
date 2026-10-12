package win32

import (
	"testing"
	"unsafe"
)

// TestStructLayout pins the sizes and key offsets of the structs we pass to
// Win32 against the documented C layouts (64-bit values / 32-bit values).
// It builds and runs on any OS, so a wrong field type is caught on Linux CI.
func TestStructLayout(t *testing.T) {
	is64 := unsafe.Sizeof(uintptr(0)) == 8
	pick := func(v64, v32 uintptr) uintptr {
		if is64 {
			return v64
		}
		return v32
	}
	check := func(name string, got, want uintptr) {
		t.Helper()
		if got != want {
			t.Errorf("%s = %d, want %d", name, got, want)
		}
	}
	check("sizeof(point)", unsafe.Sizeof(point{}), 8)
	check("sizeof(rect)", unsafe.Sizeof(rect{}), 16)
	check("sizeof(msg)", unsafe.Sizeof(msg{}), pick(48, 28))
	var m msg
	check("msg.WParam", unsafe.Offsetof(m.WParam), pick(16, 8))
	check("msg.Pt", unsafe.Offsetof(m.Pt), pick(36, 20))
	check("sizeof(wndClassEx)", unsafe.Sizeof(wndClassEx{}), pick(80, 48))
	var wc wndClassEx
	check("wndClassEx.WndProc", unsafe.Offsetof(wc.WndProc), pick(8, 8))
	check("wndClassEx.Instance", unsafe.Offsetof(wc.Instance), pick(24, 20))
	check("wndClassEx.ClassName", unsafe.Offsetof(wc.ClassName), pick(64, 40))
	check("sizeof(paintStruct)", unsafe.Sizeof(paintStruct{}), pick(72, 64))
	var ps paintStruct
	check("paintStruct.Paint", unsafe.Offsetof(ps.Paint), pick(12, 8))
	check("sizeof(bitmapInfoHeader)", unsafe.Sizeof(bitmapInfoHeader{}), 40)
	check("sizeof(bitmapInfo)", unsafe.Sizeof(bitmapInfo{}), 44)
	check("sizeof(trackMouseEvent)", unsafe.Sizeof(trackMouseEvent{}), pick(24, 16))
	check("sizeof(windowPlacement)", unsafe.Sizeof(windowPlacement{}), 44)
	check("sizeof(monitorInfo)", unsafe.Sizeof(monitorInfo{}), 40)
	check("sizeof(iconInfo)", unsafe.Sizeof(iconInfo{}), pick(32, 20))
	var ii iconInfo
	check("iconInfo.HbmMask", unsafe.Offsetof(ii.HbmMask), pick(16, 12))
	check("sizeof(minMaxInfo)", unsafe.Sizeof(minMaxInfo{}), 40)
}
