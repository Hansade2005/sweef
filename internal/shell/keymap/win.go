package keymap

import "strconv"

// Windows virtual-key codes used by the tables below.
const (
	VK_BACK       = 0x08
	VK_TAB        = 0x09
	VK_RETURN     = 0x0D
	VK_SHIFT      = 0x10
	VK_CONTROL    = 0x11
	VK_MENU       = 0x12
	VK_PROCESSKEY = 0xE5
	VK_PACKET     = 0xE7
)

var winVKNames = map[int]string{
	0x08: "Backspace", 0x09: "Tab", 0x0C: "Clear", 0x0D: "Enter", 0x10: "Shift",
	0x11: "Control", 0x12: "Alt", 0x13: "Pause", 0x14: "CapsLock", 0x15: "KanaMode",
	0x17: "JunjaMode", 0x18: "FinalMode", 0x19: "KanjiMode", 0x1B: "Escape",
	0x1C: "Convert", 0x1D: "NonConvert", 0x1E: "Accept", 0x1F: "ModeChange",
	0x21: "PageUp", 0x22: "PageDown", 0x23: "End", 0x24: "Home", 0x25: "ArrowLeft",
	0x26: "ArrowUp", 0x27: "ArrowRight", 0x28: "ArrowDown", 0x29: "Select",
	0x2A: "Print", 0x2B: "Execute", 0x2C: "PrintScreen", 0x2D: "Insert", 0x2E: "Delete",
	0x2F: "Help", 0x5B: "Meta", 0x5C: "Meta", 0x5D: "ContextMenu", 0x5F: "Standby",
	0x90: "NumLock", 0x91: "ScrollLock", 0xA0: "Shift", 0xA1: "Shift", 0xA2: "Control",
	0xA3: "Control", 0xA4: "Alt", 0xA5: "Alt", 0xA6: "BrowserBack", 0xA7: "BrowserForward",
	0xA8: "BrowserRefresh", 0xA9: "BrowserStop", 0xAA: "BrowserSearch",
	0xAB: "BrowserFavorites", 0xAC: "BrowserHome", 0xAD: "AudioVolumeMute",
	0xAE: "AudioVolumeDown", 0xAF: "AudioVolumeUp", 0xB0: "MediaTrackNext",
	0xB1: "MediaTrackPrevious", 0xB2: "MediaStop", 0xB3: "MediaPlayPause",
	0xE5: "Process", 0xF6: "Attn", 0xF7: "CrSel", 0xF8: "ExSel", 0xF9: "EraseEof",
	0xFA: "Play", 0xFB: "ZoomToggle",
}

// WinVKKey returns the DOM key name for virtual keys that are not
// characters ("Enter", "F5", "ArrowLeft"...). ok is false for character keys,
// whose key string must come from ToUnicodeEx (layout and shift aware).
func WinVKKey(vk int) (string, bool) {
	if vk >= 0x70 && vk <= 0x87 {
		return "F" + strconv.Itoa(vk-0x70+1), true
	}
	s, ok := winVKNames[vk]
	return s, ok
}

// winExtCodes lists the scan codes whose extended (0xE0-prefixed) variant is
// a different physical key (navigation cluster, right-hand modifiers...).
var winExtCodes = map[int]string{
	0x1C: "NumpadEnter", 0x1D: "ControlRight", 0x35: "NumpadDivide", 0x37: "PrintScreen",
	0x38: "AltRight", 0x45: "NumLock", 0x47: "Home", 0x48: "ArrowUp", 0x49: "PageUp",
	0x4B: "ArrowLeft", 0x4D: "ArrowRight", 0x4F: "End", 0x50: "ArrowDown",
	0x51: "PageDown", 0x52: "Insert", 0x53: "Delete", 0x5B: "MetaLeft", 0x5C: "MetaRight",
	0x5D: "ContextMenu", 0x5E: "Power", 0x5F: "Sleep", 0x63: "WakeUp",
	0x20: "AudioVolumeMute", 0x2E: "AudioVolumeDown", 0x30: "AudioVolumeUp",
	0x22: "MediaPlayPause", 0x24: "MediaStop", 0x19: "MediaTrackNext", 0x10: "MediaTrackPrevious",
	0x32: "BrowserHome", 0x6A: "BrowserBack", 0x69: "BrowserForward", 0x67: "BrowserRefresh",
	0x65: "BrowserSearch", 0x66: "BrowserFavorites", 0x46: "Pause",
}

// WinCode maps the scan code and extended flag from a WM_KEYDOWN lParam
// (bits 16-23 and bit 24) to a DOM KeyboardEvent.code. vk disambiguates
// the few keys with no usable scan code (Pause).
func WinCode(scan int, extended bool, vk int) string {
	if vk == 0x13 {
		return "Pause"
	}
	scan &= 0xff
	if extended {
		if s, ok := winExtCodes[scan]; ok {
			return s
		}
		return ""
	}
	switch scan {
	case 0x70:
		return "KanaMode"
	case 0x73:
		return "IntlRo"
	case 0x79:
		return "Convert"
	case 0x7B:
		return "NonConvert"
	case 0x7D:
		return "IntlYen"
	case 0x7E:
		return "NumpadComma"
	case 0x57:
		return "F11"
	case 0x58:
		return "F12"
	case 0x64, 0x65, 0x66, 0x67, 0x68, 0x69, 0x6A, 0x6B, 0x6C, 0x6D, 0x6E:
		return "F" + strconv.Itoa(scan-0x64+13) // F13..F23 on enhanced keyboards
	case 0x76:
		return "F24"
	}
	if scan >= 0x59 { // set-1 gaps handled above
		return ""
	}
	return domCodeSet1[scan]
}

// WinWheelPixels converts a WM_MOUSEWHEEL/HWHEEL delta (multiples of 120)
// into logical pixels: one notch scrolls `lines` lines of lineHeight pixels
// (SPI_GETWHEELSCROLLLINES default 3). Windows reports positive delta for
// scrolling away from the user (content moves down) while shell.Event wants
// positive = content moves up, so the sign is flipped. For HWHEEL the
// Windows sign already means "right", which equals shell's convention for
// DX, so the caller passes horizontal=true to keep the sign.
func WinWheelPixels(delta int16, lines int, horizontal bool) float64 {
	if lines <= 0 {
		lines = 3
	}
	px := float64(delta) / 120 * float64(lines) * (100.0 / 3)
	if horizontal {
		return px
	}
	return -px
}
