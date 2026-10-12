package keymap

// domCodeSet1 maps a Linux evdev keycode (which equals the PS/2 set-1 make
// code for 1..88) to the DOM KeyboardEvent.code. X11 keycodes are evdev+8.
var domCodeSet1 = map[int]string{
	1: "Escape", 2: "Digit1", 3: "Digit2", 4: "Digit3", 5: "Digit4", 6: "Digit5",
	7: "Digit6", 8: "Digit7", 9: "Digit8", 10: "Digit9", 11: "Digit0", 12: "Minus",
	13: "Equal", 14: "Backspace", 15: "Tab",
	16: "KeyQ", 17: "KeyW", 18: "KeyE", 19: "KeyR", 20: "KeyT", 21: "KeyY", 22: "KeyU",
	23: "KeyI", 24: "KeyO", 25: "KeyP", 26: "BracketLeft", 27: "BracketRight",
	28: "Enter", 29: "ControlLeft",
	30: "KeyA", 31: "KeyS", 32: "KeyD", 33: "KeyF", 34: "KeyG", 35: "KeyH", 36: "KeyJ",
	37: "KeyK", 38: "KeyL", 39: "Semicolon", 40: "Quote", 41: "Backquote", 42: "ShiftLeft",
	43: "Backslash", 44: "KeyZ", 45: "KeyX", 46: "KeyC", 47: "KeyV", 48: "KeyB", 49: "KeyN",
	50: "KeyM", 51: "Comma", 52: "Period", 53: "Slash", 54: "ShiftRight",
	55: "NumpadMultiply", 56: "AltLeft", 57: "Space", 58: "CapsLock",
	59: "F1", 60: "F2", 61: "F3", 62: "F4", 63: "F5", 64: "F6", 65: "F7", 66: "F8",
	67: "F9", 68: "F10", 69: "NumLock", 70: "ScrollLock",
	71: "Numpad7", 72: "Numpad8", 73: "Numpad9", 74: "NumpadSubtract",
	75: "Numpad4", 76: "Numpad5", 77: "Numpad6", 78: "NumpadAdd",
	79: "Numpad1", 80: "Numpad2", 81: "Numpad3", 82: "Numpad0", 83: "NumpadDecimal",
	86: "IntlBackslash", 87: "F11", 88: "F12",
}

// domCodeEvdevHigh holds evdev codes above the set-1 range.
var domCodeEvdevHigh = map[int]string{
	89: "IntlRo", 90: "Lang4", 91: "Lang3", 92: "Convert", 93: "KanaMode", 94: "NonConvert",
	96: "NumpadEnter", 97: "ControlRight", 98: "NumpadDivide", 99: "PrintScreen",
	100: "AltRight", 102: "Home", 103: "ArrowUp", 104: "PageUp", 105: "ArrowLeft",
	106: "ArrowRight", 107: "End", 108: "ArrowDown", 109: "PageDown", 110: "Insert",
	111: "Delete", 113: "AudioVolumeMute", 114: "AudioVolumeDown", 115: "AudioVolumeUp",
	116: "Power", 117: "NumpadEqual", 119: "Pause", 121: "NumpadComma", 122: "Lang1",
	123: "Lang2", 124: "IntlYen", 125: "MetaLeft", 126: "MetaRight", 127: "ContextMenu",
	163: "MediaTrackNext", 164: "MediaPlayPause", 165: "MediaTrackPrevious", 166: "MediaStop",
	171: "MediaTrackNext", 172: "BrowserHome", 173: "MediaTrackPrevious", 174: "MediaStop",
	150: "BrowserSearch", 158: "BrowserBack", 159: "BrowserForward",
	183: "F13", 184: "F14", 185: "F15", 186: "F16", 187: "F17", 188: "F18", 189: "F19",
	190: "F20", 191: "F21", 192: "F22", 193: "F23", 194: "F24",
}

// EvdevCode returns the DOM code for an evdev keycode ("" if unknown).
func EvdevCode(ev int) string {
	if s, ok := domCodeSet1[ev]; ok {
		return s
	}
	return domCodeEvdevHigh[ev]
}

// X11Code returns the DOM code for an X11 keycode (evdev+8 on Linux).
func X11Code(keycode uint8) string {
	if keycode < 8 {
		return ""
	}
	return EvdevCode(int(keycode) - 8)
}
