package keymap

import (
	"testing"
	"unsafe"
)

func TestKeysymToRune(t *testing.T) {
	cases := []struct {
		ks   uint32
		want rune
		ok   bool
	}{
		{'a', 'a', true}, {'~', '~', true}, {0xe9, 'é', true}, {0x20, ' ', true},
		{0x1000416, 'Ж', true}, // direct Unicode keysym
		{0x6c1, 'а', true},     // cyrillic_a
		{0x7e1, 'α', true},     // greek_alpha
		{0xffb5, '5', true},    // KP_5
		{0xffaa, '*', true},    // KP_Multiply
		{0xff0d, 0, false},     // Return
		{0xffe1, 0, false},     // Shift_L
		{0xfe51, 0, false},     // dead_acute
		{0x20ac, '€', true},    // EuroSign
	}
	for _, c := range cases {
		r, ok := KeysymToRune(c.ks)
		if ok != c.ok || r != c.want {
			t.Errorf("KeysymToRune(%#x) = %q,%v want %q,%v", c.ks, r, ok, c.want, c.ok)
		}
	}
}

func TestKeysymKey(t *testing.T) {
	cases := map[uint32]string{
		0xff0d: "Enter", 0xff08: "Backspace", 0xff09: "Tab", 0xfe20: "Tab", 0xff1b: "Escape",
		0xff51: "ArrowLeft", 0xff54: "ArrowDown", 0xffff: "Delete", 0xffbe: "F1", 0xffc9: "F12",
		0xffe1: "Shift", 0xffe3: "Control", 0xffe9: "Alt", 0xffeb: "Meta", 0xfe03: "AltGraph",
		0xff96: "ArrowLeft", 0xff8d: "Enter", 0xffe5: "CapsLock", 0x1008ff13: "AudioVolumeUp",
	}
	for ks, want := range cases {
		if got, ok := KeysymKey(ks); !ok || got != want {
			t.Errorf("KeysymKey(%#x) = %q,%v want %q", ks, got, ok, want)
		}
	}
	if _, ok := KeysymKey('a'); ok {
		t.Error("'a' is a character, not a named key")
	}
	if !IsModifierKeysym(0xffe1) || IsModifierKeysym(0xff0d) {
		t.Error("IsModifierKeysym")
	}
}

func TestDeadKeys(t *testing.T) {
	comb, sp, ok := DeadKeysym(0xfe51)
	if !ok || comb != 0x301 || sp != 0xb4 {
		t.Fatal(comb, sp, ok)
	}
	for _, c := range []struct {
		comb, base rune
		want       string
	}{
		{0x301, 'e', "é"}, {0x300, 'A', "À"}, {0x303, 'n', "ñ"}, {0x308, 'u', "ü"},
		{0x327, 'c', "ç"}, {0x302, 'o', "ô"}, {0x301, 'x', ""}, {0x30a, 'a', "å"},
	} {
		if got := ComposeDead(c.comb, c.base); got != c.want {
			t.Errorf("ComposeDead(%U,%q) = %q want %q", c.comb, c.base, got, c.want)
		}
	}
}

func TestEvdevAndX11Codes(t *testing.T) {
	cases := map[uint8]string{
		9: "Escape", 10: "Digit1", 38: "KeyA", 36: "Enter", 50: "ShiftLeft", 62: "ShiftRight",
		37: "ControlLeft", 105: "ControlRight", 64: "AltLeft", 108: "AltRight", 65: "Space",
		113: "ArrowLeft", 114: "ArrowRight", 111: "ArrowUp", 116: "ArrowDown", 22: "Backspace",
		23: "Tab", 67: "F1", 95: "F11", 96: "F12", 133: "MetaLeft", 134: "MetaRight",
		110: "Home", 115: "End", 112: "PageUp", 117: "PageDown", 118: "Insert", 119: "Delete",
		90: "Numpad0", 104: "NumpadEnter", 106: "NumpadDivide", 63: "NumpadMultiply",
		20: "Minus", 21: "Equal", 51: "Backslash", 94: "IntlBackslash", 49: "Backquote",
		135: "ContextMenu", 66: "CapsLock", 77: "NumLock", 78: "ScrollLock", 127: "Pause",
	}
	for kc, want := range cases {
		if got := X11Code(kc); got != want {
			t.Errorf("X11Code(%d) = %q want %q", kc, got, want)
		}
	}
	if X11Code(3) != "" || X11Code(255) != "" {
		t.Error("unknown codes must be empty")
	}
	// Letters: the QWERTY rows are contiguous in evdev.
	for i, c := range "QWERTYUIOP" {
		if got := EvdevCode(16 + i); got != "Key"+string(c) {
			t.Errorf("row1 %d = %q", i, got)
		}
	}
	for i, c := range "ASDFGHJKL" {
		if got := EvdevCode(30 + i); got != "Key"+string(c) {
			t.Errorf("row2 %d = %q", i, got)
		}
	}
	for i, c := range "ZXCVBNM" {
		if got := EvdevCode(44 + i); got != "Key"+string(c) {
			t.Errorf("row3 %d = %q", i, got)
		}
	}
}

func TestWinTables(t *testing.T) {
	keys := map[int]string{
		0x08: "Backspace", 0x0D: "Enter", 0x1B: "Escape", 0x25: "ArrowLeft", 0x2E: "Delete",
		0x70: "F1", 0x7B: "F12", 0x87: "F24", 0xA0: "Shift", 0xA3: "Control", 0xA5: "Alt",
		0x5B: "Meta", 0x5D: "ContextMenu", 0xAF: "AudioVolumeUp", 0xE5: "Process", 0x14: "CapsLock",
	}
	for vk, want := range keys {
		if got, ok := WinVKKey(vk); !ok || got != want {
			t.Errorf("WinVKKey(%#x) = %q,%v want %q", vk, got, ok, want)
		}
	}
	for _, vk := range []int{'A', '1', 0xBA, 0x60, 0x20} { // characters come from ToUnicodeEx
		if _, ok := WinVKKey(vk); ok {
			t.Errorf("WinVKKey(%#x) should be a character key", vk)
		}
	}
	codes := []struct {
		scan int
		ext  bool
		vk   int
		want string
	}{
		{0x1E, false, 'A', "KeyA"}, {0x02, false, '1', "Digit1"}, {0x39, false, 0x20, "Space"},
		{0x1C, false, 0x0D, "Enter"}, {0x1C, true, 0x0D, "NumpadEnter"},
		{0x2A, false, 0x10, "ShiftLeft"}, {0x36, false, 0x10, "ShiftRight"},
		{0x1D, false, 0x11, "ControlLeft"}, {0x1D, true, 0x11, "ControlRight"},
		{0x38, false, 0x12, "AltLeft"}, {0x38, true, 0x12, "AltRight"},
		{0x4B, true, 0x25, "ArrowLeft"}, {0x4B, false, 0x64, "Numpad4"},
		{0x47, true, 0x24, "Home"}, {0x53, true, 0x2E, "Delete"}, {0x52, true, 0x2D, "Insert"},
		{0x35, true, 0x6F, "NumpadDivide"}, {0x35, false, 0xBF, "Slash"},
		{0x5B, true, 0x5B, "MetaLeft"}, {0x5C, true, 0x5C, "MetaRight"}, {0x5D, true, 0x5D, "ContextMenu"},
		{0x3B, false, 0x70, "F1"}, {0x57, false, 0x7A, "F11"}, {0x58, false, 0x7B, "F12"},
		{0x64, false, 0x7C, "F13"}, {0x76, false, 0x87, "F24"},
		{0x00, false, 0x13, "Pause"}, {0x45, true, 0x90, "NumLock"}, {0x45, false, 0x90, "NumLock"},
		{0x29, false, 0xC0, "Backquote"}, {0x56, false, 0xE2, "IntlBackslash"},
		{0x37, false, 0x6A, "NumpadMultiply"}, {0x37, true, 0x2C, "PrintScreen"},
	}
	for _, c := range codes {
		if got := WinCode(c.scan, c.ext, c.vk); got != c.want {
			t.Errorf("WinCode(%#x,%v,%#x) = %q want %q", c.scan, c.ext, c.vk, got, c.want)
		}
	}
}

func TestWinWheel(t *testing.T) {
	if got := WinWheelPixels(120, 3, false); got != -100 { // wheel away from user: content moves down
		t.Errorf("wheel up = %v", got)
	}
	if got := WinWheelPixels(-120, 3, false); got != 100 {
		t.Errorf("wheel down = %v", got)
	}
	if got := WinWheelPixels(-120, 1, false); got < 33.3 || got > 33.4 {
		t.Errorf("1 line = %v", got)
	}
	if got := WinWheelPixels(120, 3, true); got != 100 { // HWHEEL right keeps sign
		t.Errorf("hwheel = %v", got)
	}
	if got := WinWheelPixels(-30, 0, false); got <= 0 || got >= 100 { // precision touchpad fraction, default 3 lines
		t.Errorf("fractional = %v", got)
	}
}

func TestMacTables(t *testing.T) {
	cases := map[int]string{
		0: "KeyA", 1: "KeyS", 6: "KeyZ", 12: "KeyQ", 18: "Digit1", 29: "Digit0", 36: "Enter",
		48: "Tab", 49: "Space", 51: "Backspace", 53: "Escape", 55: "MetaLeft", 54: "MetaRight",
		56: "ShiftLeft", 60: "ShiftRight", 58: "AltLeft", 61: "AltRight", 59: "ControlLeft",
		123: "ArrowLeft", 124: "ArrowRight", 125: "ArrowDown", 126: "ArrowUp", 122: "F1",
		111: "F12", 117: "Delete", 115: "Home", 119: "End", 116: "PageUp", 121: "PageDown",
		24: "Equal", 27: "Minus", 50: "Backquote", 82: "Numpad0", 76: "NumpadEnter",
	}
	for kc, want := range cases {
		if got := MacCode(kc); got != want {
			t.Errorf("MacCode(%d) = %q want %q", kc, got, want)
		}
	}
	// Every ANSI letter and digit must be present exactly once.
	seen := map[string]bool{}
	for kc := 0; kc < 128; kc++ {
		if c := MacCode(kc); c != "" {
			if seen[c] {
				t.Errorf("duplicate DOM code %q", c)
			}
			seen[c] = true
		}
	}
	for _, c := range "ABCDEFGHIJKLMNOPQRSTUVWXYZ" {
		if !seen["Key"+string(c)] {
			t.Errorf("missing Key%c", c)
		}
	}
	for c := '0'; c <= '9'; c++ {
		if !seen["Digit"+string(c)] {
			t.Errorf("missing Digit%c", c)
		}
	}
	if k, ok := KeyFromCode("ArrowLeft"); !ok || k != "ArrowLeft" {
		t.Error("KeyFromCode ArrowLeft")
	}
	if k, ok := KeyFromCode("F7"); !ok || k != "F7" {
		t.Error("KeyFromCode F7")
	}
	if k, ok := KeyFromCode("ShiftRight"); !ok || k != "Shift" {
		t.Error("KeyFromCode ShiftRight")
	}
	if _, ok := KeyFromCode("KeyA"); ok {
		t.Error("KeyA is a character key")
	}
	if _, ok := KeyFromCode("Fn5x"); ok {
		t.Error("bogus F code")
	}
	if k, ok := MacFunctionKey(0xF703); !ok || k != "ArrowRight" {
		t.Error("MacFunctionKey")
	}
	if k, ok := MacFunctionKey(0xF704); !ok || k != "F1" {
		t.Error("MacFunctionKey F1")
	}
	if got := MacScrollPixels(3, true); got != -3 {
		t.Errorf("precise scroll = %v", got)
	}
	if got := MacScrollPixels(-2, false); got != 20 {
		t.Errorf("line scroll = %v", got)
	}
}

func TestUTF16(t *testing.T) {
	s := "héllo 😀 世界"
	u := UTF16Z(s)
	if u[len(u)-1] != 0 {
		t.Fatal("not NUL terminated")
	}
	if got := FromUTF16(u[:len(u)-1]); got != s {
		t.Fatalf("round trip %q", got)
	}
	if got := FromUTF16Z(&u[0], 0); got != s {
		t.Fatalf("FromUTF16Z %q", got)
	}
	if got := FromUTF16Z(nil, 0); got != "" {
		t.Fatal("nil")
	}
	// embedded NUL must not truncate the Win32 string silently: it is removed
	if got := FromUTF16Z(&UTF16Z("a\x00b")[0], 0); got != "ab" {
		t.Fatalf("embedded NUL: %q", got)
	}
	if got := FromUTF16([]uint16{0xD800}); got != "\uFFFD" {
		t.Fatalf("lone surrogate %q", got)
	}
	_ = unsafe.Sizeof(0)
}

func TestSurrogateJoiner(t *testing.T) {
	var j SurrogateJoiner
	if r, ok := j.Feed('a'); !ok || r != 'a' {
		t.Fatal("ascii")
	}
	hi, lo := UTF16("😀")[0], UTF16("😀")[1]
	if _, ok := j.Feed(hi); ok {
		t.Fatal("high surrogate must wait")
	}
	if r, ok := j.Feed(lo); !ok || r != '😀' {
		t.Fatalf("pair %q %v", r, ok)
	}
	if r, ok := j.Feed(lo); !ok || r != 0xFFFD {
		t.Fatal("orphan low surrogate")
	}
	j.Feed(hi)
	if r, ok := j.Feed('x'); !ok || r != 'x' { // hi dropped, x delivered
		t.Fatal("recovery after orphan high surrogate")
	}
}

func TestDPIMath(t *testing.T) {
	cases := []struct {
		dpi  float64
		want float64
	}{{96, 1}, {120, 1.25}, {144, 1.5}, {192, 2}, {288, 3}, {0, 1}, {-5, 1}, {10, 0.5}, {5000, 8}}
	for _, c := range cases {
		if got := ScaleFromDPI(c.dpi); got != c.want {
			t.Errorf("ScaleFromDPI(%v) = %v want %v", c.dpi, got, c.want)
		}
	}
	if got := ToDevice(1280, 1.5); got != 1920 {
		t.Errorf("ToDevice = %d", got)
	}
	if got := ToDevice(801, 1.25); got != 1001 { // 1001.25
		t.Errorf("ToDevice rounding = %d", got)
	}
	if got := ToDevice(0, 2); got != 1 {
		t.Errorf("ToDevice floor = %d", got)
	}
	if w, h := LogicalSize(1921, 1081, 2); w != 961 || h != 541 {
		t.Errorf("LogicalSize = %d,%d", w, h)
	}
	if w, h := LogicalSize(100, 100, 0); w != 100 || h != 100 {
		t.Errorf("LogicalSize zero scale = %d,%d", w, h)
	}
	if got := ToLogical(300, 1.5); got != 200 {
		t.Errorf("ToLogical = %v", got)
	}
}

func TestParseXftDPI(t *testing.T) {
	res := "Xft.antialias:\t1\nXft.dpi:\t144\nXft.hinting: 1\n"
	if d, ok := ParseXftDPI(res); !ok || d != 144 {
		t.Fatalf("%v %v", d, ok)
	}
	if _, ok := ParseXftDPI("Xft.dpi: abc\n"); ok {
		t.Fatal("garbage accepted")
	}
	if _, ok := ParseXftDPI("*.dpi: 100\n"); ok {
		t.Fatal("wrong key accepted")
	}
	if s, ok := ParseScaleEnv("2"); !ok || s != 2 {
		t.Fatal("env 2")
	}
	if s, ok := ParseScaleEnv(" 1.5 "); !ok || s != 1.5 {
		t.Fatal("env 1.5")
	}
	if _, ok := ParseScaleEnv(""); ok {
		t.Fatal("empty env")
	}
	if s, _ := ParseScaleEnv("100"); s != 8 {
		t.Fatal("clamp")
	}
}

func TestCheckOpenURL(t *testing.T) {
	good := []string{"https://example.com/a?b=c", "http://localhost:8080", "mailto:a@b.c", "ftp://x/y", "HTTPS://EXAMPLE.COM", "steam://run/1"}
	for _, u := range good {
		if _, err := CheckOpenURL(u); err != nil {
			t.Errorf("%q rejected: %v", u, err)
		}
	}
	bad := []string{"", "--help", "-x", "/etc/passwd", "C:\\Windows\\System32\\calc.exe", "c:/x.exe",
		"file:///etc/passwd", "FILE:///x", "javascript:alert(1)", "data:text/html,x", "about:blank",
		"https://a/b\nc", " https://a", "https://a ", "calc.exe", "\\\\server\\share", "https://a\x00b"}
	for _, u := range bad {
		if _, err := CheckOpenURL(u); err == nil {
			t.Errorf("%q accepted", u)
		}
	}
}
