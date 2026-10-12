package keymap

import "strconv"

var macCodes = map[int]string{
	0: "KeyA", 1: "KeyS", 2: "KeyD", 3: "KeyF", 4: "KeyH", 5: "KeyG", 6: "KeyZ", 7: "KeyX",
	8: "KeyC", 9: "KeyV", 10: "IntlBackslash", 11: "KeyB", 12: "KeyQ", 13: "KeyW",
	14: "KeyE", 15: "KeyR", 16: "KeyY", 17: "KeyT", 18: "Digit1", 19: "Digit2",
	20: "Digit3", 21: "Digit4", 22: "Digit6", 23: "Digit5", 24: "Equal", 25: "Digit9",
	26: "Digit7", 27: "Minus", 28: "Digit8", 29: "Digit0", 30: "BracketRight", 31: "KeyO",
	32: "KeyU", 33: "BracketLeft", 34: "KeyI", 35: "KeyP", 36: "Enter", 37: "KeyL",
	38: "KeyJ", 39: "Quote", 40: "KeyK", 41: "Semicolon", 42: "Backslash", 43: "Comma",
	44: "Slash", 45: "KeyN", 46: "KeyM", 47: "Period", 48: "Tab", 49: "Space",
	50: "Backquote", 51: "Backspace", 53: "Escape", 54: "MetaRight", 55: "MetaLeft",
	56: "ShiftLeft", 57: "CapsLock", 58: "AltLeft", 59: "ControlLeft", 60: "ShiftRight",
	61: "AltRight", 62: "ControlRight", 63: "Fn", 64: "F17", 65: "NumpadDecimal",
	67: "NumpadMultiply", 69: "NumpadAdd", 71: "NumLock", 72: "AudioVolumeUp",
	73: "AudioVolumeDown", 74: "AudioVolumeMute", 75: "NumpadDivide", 76: "NumpadEnter",
	78: "NumpadSubtract", 79: "F18", 80: "F19", 81: "NumpadEqual", 82: "Numpad0",
	83: "Numpad1", 84: "Numpad2", 85: "Numpad3", 86: "Numpad4", 87: "Numpad5",
	88: "Numpad6", 89: "Numpad7", 90: "F20", 91: "Numpad8", 92: "Numpad9",
	93: "IntlYen", 94: "IntlRo", 95: "NumpadComma", 96: "F5", 97: "F6", 98: "F7",
	99: "F3", 100: "F8", 101: "F9", 102: "Lang2", 103: "F11", 104: "Lang1", 105: "F13",
	106: "F16", 107: "F14", 109: "F10", 110: "ContextMenu", 111: "F12", 113: "F15",
	114: "Help", 115: "Home", 116: "PageUp", 117: "Delete", 118: "F4", 119: "End",
	120: "F2", 121: "PageDown", 122: "F1", 123: "ArrowLeft", 124: "ArrowRight",
	125: "ArrowDown", 126: "ArrowUp",
}

// MacCode maps an NSEvent keyCode (kVK_* virtual key) to a DOM code.
func MacCode(keyCode int) string { return macCodes[keyCode] }

var codeToKey = map[string]string{
	"Escape": "Escape", "Enter": "Enter", "NumpadEnter": "Enter", "Tab": "Tab",
	"Backspace": "Backspace", "Delete": "Delete", "Insert": "Insert", "Help": "Help",
	"Home": "Home", "End": "End", "PageUp": "PageUp", "PageDown": "PageDown",
	"ArrowLeft": "ArrowLeft", "ArrowRight": "ArrowRight", "ArrowUp": "ArrowUp",
	"ArrowDown": "ArrowDown", "ShiftLeft": "Shift", "ShiftRight": "Shift",
	"ControlLeft": "Control", "ControlRight": "Control", "AltLeft": "Alt",
	"AltRight": "Alt", "MetaLeft": "Meta", "MetaRight": "Meta", "CapsLock": "CapsLock",
	"NumLock": "NumLock", "ContextMenu": "ContextMenu", "Fn": "Fn",
	"AudioVolumeUp": "AudioVolumeUp", "AudioVolumeDown": "AudioVolumeDown",
	"AudioVolumeMute": "AudioVolumeMute", "Lang1": "KanaMode", "Lang2": "Eisu",
}

// KeyFromCode returns the DOM key name for a non-character physical key
// ("Enter", "ArrowLeft", "F7"...). ok is false for keys that type text.
func KeyFromCode(code string) (string, bool) {
	if s, ok := codeToKey[code]; ok {
		return s, true
	}
	if len(code) >= 2 && code[0] == 'F' {
		if _, err := strconv.Atoi(code[1:]); err == nil {
			return code, true
		}
	}
	return "", false
}

// MacFunctionKey maps the NSEvent private-use characters (0xF700 range,
// NSUpArrowFunctionKey etc.) that charactersIgnoringModifiers returns for
// function keys to DOM names, so layout-specific names survive.
func MacFunctionKey(r rune) (string, bool) {
	switch r {
	case 0xF700:
		return "ArrowUp", true
	case 0xF701:
		return "ArrowDown", true
	case 0xF702:
		return "ArrowLeft", true
	case 0xF703:
		return "ArrowRight", true
	case 0xF728:
		return "Delete", true
	case 0xF729:
		return "Home", true
	case 0xF72B:
		return "End", true
	case 0xF72C:
		return "PageUp", true
	case 0xF72D:
		return "PageDown", true
	case 0xF739:
		return "Clear", true
	}
	if r >= 0xF704 && r <= 0xF726 { // NSF1FunctionKey.. NSF35FunctionKey = 0xF704..0xF726
		return "F" + strconv.Itoa(int(r-0xF704)+1), true
	}
	return "", false
}

// MacScrollPixels converts NSEvent scrollingDeltaX/Y into shell wheel units.
// precise is hasPreciseScrollingDeltas: trackpads and Magic Mouse report
// points already; classic wheels report lines, scaled to ~10 px per line.
// NSEvent deltas are positive when content should move down/right (natural
// direction already applied by the OS), shell.Event wants positive = content
// moves up, so the vertical sign flips; horizontal keeps DOM sign
// (positive = content moves left), so it flips as well.
func MacScrollPixels(delta float64, precise bool) float64 {
	if !precise {
		delta *= 10
	}
	return -delta
}
