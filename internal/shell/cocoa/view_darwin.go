//go:build darwin

package cocoa

import (
	"fmt"
	"unicode/utf16"

	"github.com/ebitengine/purego/objc"

	"github.com/sweefbrowser/sweef/internal/shell"
	"github.com/sweefbrowser/sweef/internal/shell/keymap"
)

func modsFromFlags(f uint) shell.Mods {
	var m shell.Mods
	if f&flagShift != 0 {
		m |= shell.ModShift
	}
	if f&flagControl != 0 {
		m |= shell.ModCtrl
	}
	if f&flagOption != 0 {
		m |= shell.ModAlt
	}
	if f&flagCommand != 0 {
		m |= shell.ModMeta
	}
	return m
}

// Device-dependent modifier bits (IOKit NX_DEVICE*KEYMASK), used to tell
// left and right modifier keys apart in flagsChanged:.
var deviceModBit = map[uint16]uint{
	56: 0x2, 60: 0x4, // shift L/R
	59: 0x1, 62: 0x2000, // control L/R
	58: 0x20, 61: 0x40, // option L/R
	55: 0x8, 54: 0x10, // command L/R
}

// keyName resolves KeyboardEvent.key for an NSEvent.
func keyName(ev objc.ID, code string, flags uint) string {
	if name, ok := keymap.KeyFromCode(code); ok {
		return name
	}
	chars := goString(send(ev, "characters"))
	if flags&flagControl != 0 || chars == "" || []rune(chars)[0] < 0x20 {
		if c2 := goString(send(ev, "charactersIgnoringModifiers")); c2 != "" {
			chars = c2
		}
	}
	rs := []rune(chars)
	if len(rs) == 0 {
		return "Dead"
	}
	if name, ok := keymap.MacFunctionKey(rs[0]); ok {
		return name
	}
	if len(rs) == 1 {
		return chars
	}
	return string(rs[0])
}

func (w *Window) eventPoint(ev objc.ID) (float64, float64) {
	p := objc.Send[NSPoint](ev, sel("locationInWindow"))
	v := objc.Send[NSPoint](w.view, sel("convertPoint:fromView:"), p, objc.ID(0))
	return v.X, v.Y // the view is flipped: origin top-left, units are logical px
}

func (w *Window) mouseButton(ev objc.ID, kind shell.Kind, button int) {
	x, y := w.eventPoint(ev)
	w.q.Push(shell.Event{Kind: kind, X: x, Y: y, Button: button, Mods: modsFromFlags(sendUint(ev, "modifierFlags"))})
}

func (w *Window) otherButton(ev objc.ID) int {
	switch n := objc.Send[int](ev, sel("buttonNumber")); n {
	case 2:
		return 1 // middle
	case 3:
		return 3 // back
	case 4:
		return 4 // forward
	}
	return -1
}

func (w *Window) mouseMove(ev objc.ID) {
	x, y := w.eventPoint(ev)
	w.q.Push(shell.Event{Kind: shell.EvMouseMove, X: x, Y: y, Mods: modsFromFlags(sendUint(ev, "modifierFlags"))})
}

func (w *Window) onKeyDown(self, ev objc.ID) {
	flags := sendUint(ev, "modifierFlags")
	kc := uint16(sendUint(ev, "keyCode"))
	code := keymap.MacCode(int(kc))
	key := keyName(ev, code, flags)
	if w.marked != "" {
		key = "Process" // IME composition is consuming the key
	}
	w.q.Push(shell.Event{Kind: shell.EvKeyDown, Key: key, Code: code, Mods: modsFromFlags(flags), Repeat: sendBool(ev, "isARepeat")})
	// Shortcuts (Cmd/Ctrl) are not text. Everything else goes through the
	// input system so dead keys, Option characters and IMEs produce text via
	// insertText:replacementRange:.
	if flags&(flagCommand|flagControl) == 0 {
		send(self, "interpretKeyEvents:", send(class("NSArray"), "arrayWithObject:", ev))
	}
}

func (w *Window) onKeyUp(ev objc.ID) {
	flags := sendUint(ev, "modifierFlags")
	code := keymap.MacCode(int(uint16(sendUint(ev, "keyCode"))))
	w.q.Push(shell.Event{Kind: shell.EvKeyUp, Key: keyName(ev, code, flags), Code: code, Mods: modsFromFlags(flags)})
}

func (w *Window) onFlagsChanged(ev objc.ID) {
	flags := sendUint(ev, "modifierFlags")
	kc := uint16(sendUint(ev, "keyCode"))
	code := keymap.MacCode(int(kc))
	key, ok := keymap.KeyFromCode(code)
	if !ok {
		return
	}
	var down bool
	switch {
	case kc == 57: // Caps Lock reports state, not press/release
		down = flags&flagCapsLock != 0
	case deviceModBit[kc] != 0:
		down = flags&deviceModBit[kc] != 0
	case kc == 63:
		down = flags&(1<<23) != 0
	default:
		down = !w.modDown[kc]
	}
	w.modDown[kc] = down
	kind := shell.EvKeyUp
	if down {
		kind = shell.EvKeyDown
	}
	w.q.Push(shell.Event{Kind: kind, Key: key, Code: code, Mods: modsFromFlags(flags)})
}

func (w *Window) onScroll(ev objc.ID) {
	dx, dy := sendFloat(ev, "scrollingDeltaX"), sendFloat(ev, "scrollingDeltaY")
	if dx == 0 && dy == 0 {
		return
	}
	precise := sendBool(ev, "hasPreciseScrollingDeltas")
	x, y := w.eventPoint(ev)
	w.q.Push(shell.Event{Kind: shell.EvWheel, X: x, Y: y,
		DX: keymap.MacScrollPixels(dx, precise), DY: keymap.MacScrollPixels(dy, precise),
		Mods: modsFromFlags(sendUint(ev, "modifierFlags")), Precise: precise})
}

// text extracts a Go string from the id passed to insertText:/setMarkedText:
// (an NSString or NSAttributedString).
func text(v objc.ID) string {
	if v == 0 {
		return ""
	}
	if sendBool(v, "isKindOfClass:", class("NSAttributedString")) {
		v = send(v, "string")
	}
	return goString(v)
}

func utf16Len(s string) uint { return uint(len(utf16.Encode([]rune(s)))) }

// registerClasses defines SweefView (NSView subclass) and SweefWindowDelegate.
func registerClasses() error {
	// Every IMP gets the receiver first; unknown receivers (a view of a closed
	// window) are ignored.
	with := func(name string, f func(w *Window)) func(self objc.ID, _ objc.SEL) {
		return func(self objc.ID, _ objc.SEL) {
			defer guard(name)
			if w := lookup(self); w != nil {
				f(w)
			}
		}
	}
	withEvent := func(name string, f func(w *Window, self, ev objc.ID)) func(self objc.ID, _ objc.SEL, ev objc.ID) {
		return func(self objc.ID, _ objc.SEL, ev objc.ID) {
			defer guard(name)
			if w := lookup(self); w != nil {
				f(w, self, ev)
			}
		}
	}
	yes := func(self objc.ID, _ objc.SEL) bool { return true }
	protocols := []*objc.Protocol{}
	if p := objc.GetProtocol("NSTextInputClient"); p != nil {
		protocols = append(protocols, p)
	}
	methods := []objc.MethodDef{
		{Cmd: sel("isFlipped"), Fn: yes},
		{Cmd: sel("isOpaque"), Fn: yes},
		{Cmd: sel("acceptsFirstResponder"), Fn: yes},
		{Cmd: sel("becomeFirstResponder"), Fn: yes},
		{Cmd: sel("acceptsFirstMouse:"), Fn: func(self objc.ID, _ objc.SEL, ev objc.ID) bool { return true }},
		{Cmd: sel("drawRect:"), Fn: func(self objc.ID, _ objc.SEL, dirty NSRect) {
			defer guard("drawRect")
			if w := lookup(self); w != nil {
				ctx := send(class("NSGraphicsContext"), "currentContext")
				w.draw(objc.Send[uintptr](ctx, sel("CGContext")), dirty)
			}
		}},
		{Cmd: sel("resetCursorRects"), Fn: with("resetCursorRects", func(w *Window) {
			c := w.cursor
			if c == 0 {
				c = send(class("NSCursor"), "arrowCursor")
			}
			b := objc.Send[NSRect](w.view, sel("bounds"))
			send(w.view, "addCursorRect:cursor:", b, c)
		})},
		{Cmd: sel("keyDown:"), Fn: withEvent("keyDown", func(w *Window, self, ev objc.ID) { w.onKeyDown(self, ev) })},
		{Cmd: sel("keyUp:"), Fn: withEvent("keyUp", func(w *Window, self, ev objc.ID) { w.onKeyUp(ev) })},
		{Cmd: sel("flagsChanged:"), Fn: withEvent("flagsChanged", func(w *Window, self, ev objc.ID) { w.onFlagsChanged(ev) })},
		{Cmd: sel("mouseDown:"), Fn: withEvent("mouseDown", func(w *Window, self, ev objc.ID) { w.mouseButton(ev, shell.EvMouseDown, 0) })},
		{Cmd: sel("mouseUp:"), Fn: withEvent("mouseUp", func(w *Window, self, ev objc.ID) { w.mouseButton(ev, shell.EvMouseUp, 0) })},
		{Cmd: sel("rightMouseDown:"), Fn: withEvent("rightMouseDown", func(w *Window, self, ev objc.ID) { w.mouseButton(ev, shell.EvMouseDown, 2) })},
		{Cmd: sel("rightMouseUp:"), Fn: withEvent("rightMouseUp", func(w *Window, self, ev objc.ID) { w.mouseButton(ev, shell.EvMouseUp, 2) })},
		{Cmd: sel("otherMouseDown:"), Fn: withEvent("otherMouseDown", func(w *Window, self, ev objc.ID) {
			if b := w.otherButton(ev); b >= 0 {
				w.mouseButton(ev, shell.EvMouseDown, b)
			}
		})},
		{Cmd: sel("otherMouseUp:"), Fn: withEvent("otherMouseUp", func(w *Window, self, ev objc.ID) {
			if b := w.otherButton(ev); b >= 0 {
				w.mouseButton(ev, shell.EvMouseUp, b)
			}
		})},
		{Cmd: sel("mouseMoved:"), Fn: withEvent("mouseMoved", func(w *Window, self, ev objc.ID) { w.mouseMove(ev) })},
		{Cmd: sel("mouseDragged:"), Fn: withEvent("mouseDragged", func(w *Window, self, ev objc.ID) { w.mouseMove(ev) })},
		{Cmd: sel("rightMouseDragged:"), Fn: withEvent("rightMouseDragged", func(w *Window, self, ev objc.ID) { w.mouseMove(ev) })},
		{Cmd: sel("otherMouseDragged:"), Fn: withEvent("otherMouseDragged", func(w *Window, self, ev objc.ID) { w.mouseMove(ev) })},
		{Cmd: sel("mouseExited:"), Fn: withEvent("mouseExited", func(w *Window, self, ev objc.ID) { w.mouseMove(ev) })},
		{Cmd: sel("scrollWheel:"), Fn: withEvent("scrollWheel", func(w *Window, self, ev objc.ID) { w.onScroll(ev) })},

		// NSTextInputClient
		{Cmd: sel("insertText:replacementRange:"), Fn: func(self objc.ID, _ objc.SEL, str objc.ID, rng NSRange) {
			defer guard("insertText")
			if w := lookup(self); w != nil {
				w.marked = ""
				if s := text(str); s != "" {
					w.q.Push(shell.Event{Kind: shell.EvText, Text: s, Mods: modsFromFlags(sendUint(class("NSEvent"), "modifierFlags"))})
				}
			}
		}},
		{Cmd: sel("setMarkedText:selectedRange:replacementRange:"), Fn: func(self objc.ID, _ objc.SEL, str objc.ID, sr, rr NSRange) {
			defer guard("setMarkedText")
			if w := lookup(self); w != nil {
				w.marked = text(str)
			}
		}},
		{Cmd: sel("unmarkText"), Fn: with("unmarkText", func(w *Window) { w.marked = "" })},
		{Cmd: sel("hasMarkedText"), Fn: func(self objc.ID, _ objc.SEL) bool {
			w := lookup(self)
			return w != nil && w.marked != ""
		}},
		{Cmd: sel("markedRange"), Fn: func(self objc.ID, _ objc.SEL) NSRange {
			if w := lookup(self); w != nil && w.marked != "" {
				return NSRange{Location: 0, Length: utf16Len(w.marked)}
			}
			return NSRange{Location: nsNotFound}
		}},
		{Cmd: sel("selectedRange"), Fn: func(self objc.ID, _ objc.SEL) NSRange {
			if w := lookup(self); w != nil && w.marked != "" {
				return NSRange{Location: utf16Len(w.marked)}
			}
			return NSRange{Location: nsNotFound}
		}},
		{Cmd: sel("attributedSubstringForProposedRange:actualRange:"), Fn: func(self objc.ID, _ objc.SEL, r NSRange, actual uintptr) objc.ID { return 0 }},
		{Cmd: sel("validAttributesForMarkedText"), Fn: func(self objc.ID, _ objc.SEL) objc.ID {
			return send(class("NSArray"), "array")
		}},
		{Cmd: sel("firstRectForCharacterRange:actualRange:"), Fn: func(self objc.ID, _ objc.SEL, r NSRange, actual uintptr) NSRect {
			defer guard("firstRect")
			w := lookup(self)
			if w == nil {
				return NSRect{}
			}
			inWin := objc.Send[NSRect](w.view, sel("convertRect:toView:"), w.imeRect, objc.ID(0))
			return objc.Send[NSRect](w.win, sel("convertRectToScreen:"), inWin)
		}},
		{Cmd: sel("characterIndexForPoint:"), Fn: func(self objc.ID, _ objc.SEL, p NSPoint) uint { return nsNotFound }},
		{Cmd: sel("doCommandBySelector:"), Fn: func(self objc.ID, _ objc.SEL, s objc.SEL) {}},

		// Drag and drop of files from Finder.
		{Cmd: sel("draggingEntered:"), Fn: func(self objc.ID, _ objc.SEL, info objc.ID) uint { return 1 }}, // NSDragOperationCopy
		{Cmd: sel("prepareForDragOperation:"), Fn: func(self objc.ID, _ objc.SEL, info objc.ID) bool { return true }},
		{Cmd: sel("performDragOperation:"), Fn: func(self objc.ID, _ objc.SEL, info objc.ID) bool {
			defer guard("performDragOperation")
			w := lookup(self)
			if w == nil {
				return false
			}
			pb := send(info, "draggingPasteboard")
			list := send(pb, "propertyListForType:", nsString("NSFilenamesPboardType"))
			if list == 0 {
				return false
			}
			n := sendUint(list, "count")
			var files []string
			for i := uint(0); i < n; i++ {
				if p := goString(send(list, "objectAtIndex:", i)); p != "" {
					files = append(files, p)
				}
			}
			if len(files) == 0 {
				return false
			}
			w.q.Push(shell.Event{Kind: shell.EvDropFiles, Files: files})
			return true
		}},
	}
	vc, err := objc.RegisterClass("SweefView", objc.GetClass("NSView"), protocols, nil, methods)
	if err != nil {
		return fmt.Errorf("cocoa: registering SweefView: %w", err)
	}
	viewClass = vc

	delegateNote := func(name string, f func(w *Window)) objc.MethodDef {
		return objc.MethodDef{Cmd: sel(name), Fn: func(self objc.ID, _ objc.SEL, note objc.ID) {
			defer guard(name)
			if w := lookup(self); w != nil {
				f(w)
			}
		}}
	}
	dc, err := objc.RegisterClass("SweefWindowDelegate", objc.GetClass("NSObject"), nil, nil, []objc.MethodDef{
		{Cmd: sel("windowShouldClose:"), Fn: func(self objc.ID, _ objc.SEL, sender objc.ID) bool {
			defer guard("windowShouldClose")
			if w := lookup(self); w != nil {
				w.q.Push(shell.Event{Kind: shell.EvClose})
			}
			return false // the app closes the window itself via Close()
		}},
		delegateNote("windowDidResize:", func(w *Window) { w.syncGeometry(); w.pushResize() }),
		delegateNote("windowDidEnterFullScreen:", func(w *Window) { w.syncGeometry(); w.pushResize() }),
		delegateNote("windowDidExitFullScreen:", func(w *Window) { w.syncGeometry(); w.pushResize() }),
		delegateNote("windowDidBecomeKey:", func(w *Window) { w.q.Push(shell.Event{Kind: shell.EvFocus, Focus: true}) }),
		delegateNote("windowDidResignKey:", func(w *Window) {
			w.modDown = map[uint16]bool{}
			w.q.Push(shell.Event{Kind: shell.EvFocus, Focus: false})
		}),
		delegateNote("windowDidChangeBackingProperties:", func(w *Window) {
			w.syncGeometry()
			w.gmu.Lock()
			lw, lh, s := w.logW, w.logH, w.scale
			w.gmu.Unlock()
			w.q.Push(shell.Event{Kind: shell.EvScale, Scale: s, W: int(lw + 0.5), H: int(lh + 0.5)})
		}),
	})
	if err != nil {
		return fmt.Errorf("cocoa: registering SweefWindowDelegate: %w", err)
	}
	delegateCls = dc
	return nil
}
