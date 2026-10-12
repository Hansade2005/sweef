package keymap

import (
	"sort"
	"strconv"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// KeysymToRune converts an X11 keysym to the Unicode character it types.
// ok is false for keysyms that are not characters (function keys, modifiers).
func KeysymToRune(ks uint32) (r rune, ok bool) {
	switch {
	case ks >= 0x20 && ks <= 0x7e, ks >= 0xa0 && ks <= 0xff:
		return rune(ks), true
	case ks >= 0x01000100 && ks <= 0x0110ffff:
		return rune(ks - 0x01000000), true
	case ks >= 0xffb0 && ks <= 0xffb9: // KP_0..KP_9
		return rune('0' + ks - 0xffb0), true
	}
	switch ks {
	case 0xff80, 0xa0: // KP_Space
		return ' ', true
	case 0xffaa:
		return '*', true
	case 0xffab:
		return '+', true
	case 0xffac:
		return ',', true
	case 0xffad:
		return '-', true
	case 0xffae:
		return '.', true
	case 0xffaf:
		return '/', true
	case 0xffbd:
		return '=', true
	}
	i := sort.Search(len(keysymUnicode), func(i int) bool { return keysymUnicode[i][0] >= ks })
	if i < len(keysymUnicode) && keysymUnicode[i][0] == ks {
		return rune(keysymUnicode[i][1]), true
	}
	return 0, false
}

var keysymNames = map[uint32]string{
	0xff08: "Backspace", 0xff09: "Tab", 0xfe20: "Tab", 0xff0a: "Enter", 0xff0b: "Clear",
	0xff0d: "Enter", 0xff13: "Pause", 0xff14: "ScrollLock", 0xff15: "PrintScreen",
	0xff1b: "Escape", 0xffff: "Delete", 0xff50: "Home", 0xff51: "ArrowLeft",
	0xff52: "ArrowUp", 0xff53: "ArrowRight", 0xff54: "ArrowDown", 0xff55: "PageUp",
	0xff56: "PageDown", 0xff57: "End", 0xff58: "Home", 0xff60: "Select",
	0xff61: "PrintScreen", 0xff62: "Execute", 0xff63: "Insert", 0xff65: "Undo",
	0xff66: "Redo", 0xff67: "ContextMenu", 0xff68: "Find", 0xff69: "Cancel",
	0xff6a: "Help", 0xff6b: "Pause", 0xff7e: "AltGraph", 0xff7f: "NumLock",
	0xfe03: "AltGraph", 0xfe11: "AltGraph",
	0xff89: "Tab", 0xff8d: "Enter",
	0xff91: "F1", 0xff92: "F2", 0xff93: "F3", 0xff94: "F4",
	0xff95: "Home", 0xff96: "ArrowLeft", 0xff97: "ArrowUp", 0xff98: "ArrowRight",
	0xff99: "ArrowDown", 0xff9a: "PageUp", 0xff9b: "PageDown", 0xff9c: "End",
	0xff9d: "Clear", 0xff9e: "Insert", 0xff9f: "Delete",
	0xffe1: "Shift", 0xffe2: "Shift", 0xffe3: "Control", 0xffe4: "Control",
	0xffe5: "CapsLock", 0xffe6: "CapsLock", 0xffe7: "Meta", 0xffe8: "Meta",
	0xffe9: "Alt", 0xffea: "Alt", 0xffeb: "Meta", 0xffec: "Meta",
	0xffed: "Hyper", 0xffee: "Hyper",
	0xff2f: "Eisu", 0xff31: "HangulMode", 0xff34: "HanjaMode", 0xff21: "Convert",
	0xff22: "NonConvert", 0xff27: "Hankaku", 0xff28: "Zenkaku", 0xff2d: "KanaMode",
	0x1008ff11: "AudioVolumeDown", 0x1008ff12: "AudioVolumeMute", 0x1008ff13: "AudioVolumeUp",
	0x1008ff14: "MediaPlayPause", 0x1008ff15: "MediaStop", 0x1008ff16: "MediaTrackPrevious",
	0x1008ff17: "MediaTrackNext", 0x1008ff26: "BrowserBack", 0x1008ff27: "BrowserForward",
	0x1008ff29: "BrowserRefresh", 0x1008ff2a: "Power", 0x1008ff18: "BrowserHome",
	0x1008ff1b: "BrowserSearch", 0x1008ff30: "BrowserFavorites",
}

// KeysymKey returns the DOM KeyboardEvent.key name for non-character keysyms
// ("Enter", "ArrowLeft", "F5", "Shift", ...).
func KeysymKey(ks uint32) (string, bool) {
	if ks >= 0xffbe && ks <= 0xffe0 { // F1..F35
		return "F" + strconv.Itoa(int(ks-0xffbe)+1), true
	}
	s, ok := keysymNames[ks]
	return s, ok
}

// IsModifierKeysym reports whether ks is a pure modifier key.
func IsModifierKeysym(ks uint32) bool {
	switch ks {
	case 0xffe1, 0xffe2, 0xffe3, 0xffe4, 0xffe5, 0xffe6, 0xffe7, 0xffe8, 0xffe9, 0xffea,
		0xffeb, 0xffec, 0xffed, 0xffee, 0xff7e, 0xfe03, 0xff7f, 0xff14:
		return true
	}
	return false
}

type dead struct{ comb, space rune }

var deadKeys = map[uint32]dead{
	0xfe50: {0x300, '`'}, 0xfe51: {0x301, 0xb4}, 0xfe52: {0x302, '^'}, 0xfe53: {0x303, '~'},
	0xfe54: {0x304, 0xaf}, 0xfe55: {0x306, 0x2d8}, 0xfe56: {0x307, 0x2d9},
	0xfe57: {0x308, 0xa8}, 0xfe58: {0x30a, 0x2da}, 0xfe59: {0x30b, 0x2dd},
	0xfe5a: {0x30c, 0x2c7}, 0xfe5b: {0x327, 0xb8}, 0xfe5c: {0x328, 0x2db},
	0xfe60: {0x323, '.'}, 0xfe61: {0x309, '?'}, 0xfe62: {0x31b, '\''},
}

// DeadKeysym reports whether ks is a dead (accent) keysym and returns its
// combining mark and the spacing accent typed when followed by space.
func DeadKeysym(ks uint32) (comb, spacing rune, ok bool) {
	d, ok := deadKeys[ks]
	return d.comb, d.space, ok
}

// ComposeDead combines a dead key's combining mark with a base character.
// It returns the precomposed string, or "" when no single-rune composition
// exists (the caller then emits the spacing accent followed by base).
func ComposeDead(comb, base rune) string {
	s := norm.NFC.String(string([]rune{base, comb}))
	if r := []rune(s); len(r) == 1 {
		return s
	}
	return ""
}

// Printable reports whether r should be delivered as text input.
func Printable(r rune) bool {
	return r >= 0x20 && r != 0x7f && !unicode.IsControl(r)
}
