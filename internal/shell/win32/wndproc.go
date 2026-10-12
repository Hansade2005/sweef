//go:build windows

package win32

import (
	"unsafe"

	"github.com/sweefbrowser/sweef/internal/shell"
	"github.com/sweefbrowser/sweef/internal/shell/keymap"
)

func keyDown(vk uintptr) bool {
	r, _, _ := pGetKeyState.Call(vk)
	return int16(r) < 0 // high bit of the SHORT result
}

func currentMods() shell.Mods {
	var m shell.Mods
	if keyDown(vkShift) {
		m |= shell.ModShift
	}
	if keyDown(vkControl) {
		m |= shell.ModCtrl
	}
	if keyDown(vkMenu) {
		m |= shell.ModAlt
	}
	if keyDown(vkLWin) || keyDown(vkRWin) {
		m |= shell.ModMeta
	}
	return m
}

func (w *Window) curScale() float64 {
	w.gmu.Lock()
	defer w.gmu.Unlock()
	return w.scale
}

func (w *Window) mousePos(lparam uintptr) (float64, float64) {
	s := w.curScale()
	return float64(getX(lparam)) / s, float64(getY(lparam)) / s
}

// handle processes one window message on the UI thread. handled=false means
// "pass to DefWindowProc".
func (w *Window) handle(hwnd uintptr, m uint32, wp, lp uintptr) (uintptr, bool) {
	switch m {
	case wmEraseBkgnd:
		return 1, true // we paint every pixel from the DIB

	case wmPaint:
		var ps paintStruct
		hdc, _, _ := pBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		if hdc != 0 {
			w.paint(hdc, ps.Paint)
		}
		pEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		return 0, true

	case wmSize:
		if wp == sizeMinimized {
			return 0, true
		}
		cw, ch := int(loword(lp)), int(hiword(lp))
		w.gmu.Lock()
		w.clientW, w.clientH = cw, ch
		s := w.scale
		w.gmu.Unlock()
		lw, lh := keymap.LogicalSize(cw, ch, s)
		w.q.Push(shell.Event{Kind: shell.EvResize, W: lw, H: lh, Scale: s})
		return 0, true

	case wmDPIChanged:
		dpi := float64(loword(wp))
		s := keymap.ScaleFromDPI(dpi)
		w.gmu.Lock()
		w.scale = s
		w.gmu.Unlock()
		if r := (*rect)(ptrFrom(lp)); r != nil { // suggested window rectangle
			pSetWindowPos.Call(hwnd, 0, uintptr(r.Left), uintptr(r.Top),
				uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top), swpNoZOrder|swpNoActivate)
		}
		cw, ch := w.devSize()
		lw, lh := keymap.LogicalSize(cw, ch, s)
		w.q.Push(shell.Event{Kind: shell.EvScale, Scale: s, W: lw, H: lh})
		return 0, true

	case wmGetMinMaxInfo:
		return 0, false

	case wmClose:
		w.q.Push(shell.Event{Kind: shell.EvClose})
		return 0, true

	case wmDestroy:
		pPostQuitMessage.Call(0)
		return 0, true

	case wmApp:
		w.runPosted()
		return 0, true

	case wmSetFocus:
		w.focused = true
		w.q.Push(shell.Event{Kind: shell.EvFocus, Focus: true})
		return 0, true
	case wmKillFocus:
		w.focused = false
		if w.buttons != 0 {
			w.buttons = 0
			pReleaseCapture.Call()
		}
		w.q.Push(shell.Event{Kind: shell.EvFocus, Focus: false})
		return 0, true

	case wmSetCursor:
		if loword(lp) == htClient && w.cursor != 0 {
			pSetCursor.Call(w.cursor)
			return 1, true
		}
		return 0, false

	case wmKeyDown, wmSysKeyDown, wmKeyUp, wmSysKeyUp:
		return w.onKey(m, wp, lp)

	case wmChar:
		w.onChar(uint16(wp))
		return 0, true
	case wmSysChar, wmDeadChar:
		return 0, true // Alt+key / accent prefix: the composed WM_CHAR follows
	case wmUniChar:
		if wp == 0xFFFF { // UNICODE_NOCHAR: tell senders we understand WM_UNICHAR
			return 1, true
		}
		if r := rune(wp); keymap.Printable(r) {
			w.q.Push(shell.Event{Kind: shell.EvText, Text: string(r), Mods: currentMods()})
		}
		return 0, true

	case wmMouseMove:
		if !w.tracking {
			tme := trackMouseEvent{Flags: tmeLeave, HwndTrack: hwnd}
			tme.CbSize = uint32(unsafe.Sizeof(tme))
			pTrackMouseEvent.Call(uintptr(unsafe.Pointer(&tme)))
			w.tracking = true
		}
		x, y := w.mousePos(lp)
		w.q.Push(shell.Event{Kind: shell.EvMouseMove, X: x, Y: y, Mods: currentMods()})
		return 0, true
	case wmMouseLeave:
		w.tracking = false
		var p point
		pGetCursorPos.Call(uintptr(unsafe.Pointer(&p)))
		pScreenToClient.Call(hwnd, uintptr(unsafe.Pointer(&p)))
		s := w.curScale()
		w.q.Push(shell.Event{Kind: shell.EvMouseMove, X: float64(p.X) / s, Y: float64(p.Y) / s, Mods: currentMods()})
		return 0, true

	case wmLButtonDown, wmRButtonDown, wmMButtonDown, wmXButtonDown,
		wmLButtonUp, wmRButtonUp, wmMButtonUp, wmXButtonUp:
		return w.onButton(hwnd, m, wp, lp)

	case wmMouseWheel, wmMouseHWheel:
		delta := int16(hiword(wp))
		p := point{int32(getX(lp)), int32(getY(lp))} // screen coordinates
		pScreenToClient.Call(hwnd, uintptr(unsafe.Pointer(&p)))
		s := w.curScale()
		ev := shell.Event{Kind: shell.EvWheel, X: float64(p.X) / s, Y: float64(p.Y) / s,
			Mods: currentMods(), Precise: delta%120 != 0}
		if m == wmMouseWheel {
			ev.DY = keymap.WinWheelPixels(delta, w.wheelLines, false)
		} else {
			ev.DX = keymap.WinWheelPixels(delta, w.wheelLines, true)
		}
		w.q.Push(ev)
		return 0, true

	case wmDropFiles:
		w.onDrop(wp)
		return 0, true
	}
	return 0, false
}

func (w *Window) onButton(hwnd uintptr, m uint32, wp, lp uintptr) (uintptr, bool) {
	var btn int
	down := false
	ret := uintptr(0)
	switch m {
	case wmLButtonDown:
		btn, down = 0, true
	case wmLButtonUp:
		btn = 0
	case wmMButtonDown:
		btn, down = 1, true
	case wmMButtonUp:
		btn = 1
	case wmRButtonDown:
		btn, down = 2, true
	case wmRButtonUp:
		btn = 2
	case wmXButtonDown, wmXButtonUp:
		down = m == wmXButtonDown
		switch hiword(wp) {
		case 1: // XBUTTON1 = back
			btn = 3
		case 2: // XBUTTON2 = forward
			btn = 4
		default:
			return 1, true
		}
		ret = 1 // XBUTTON messages must return TRUE
	}
	bit := 1 << uint(btn)
	k := shell.EvMouseUp
	if down {
		if w.buttons == 0 {
			pSetCapture.Call(hwnd) // keep receiving moves while dragging outside
		}
		w.buttons |= bit
		k = shell.EvMouseDown
	} else {
		w.buttons &^= bit
		if w.buttons == 0 {
			pReleaseCapture.Call()
		}
	}
	x, y := w.mousePos(lp)
	w.q.Push(shell.Event{Kind: k, X: x, Y: y, Button: btn, Mods: currentMods()})
	return ret, true
}

// keyName resolves KeyboardEvent.key for a virtual key using the active
// layout and the current modifier state (Ctrl is masked out so Ctrl+A is "a").
func keyName(vk, scan int) string {
	if name, ok := keymap.WinVKKey(vk); ok {
		return name
	}
	if vk == keymap.VK_PACKET {
		return "Unidentified"
	}
	var state [256]byte
	if r, _, _ := pGetKeyboardState.Call(uintptr(unsafe.Pointer(&state[0]))); r == 0 {
		return "Unidentified"
	}
	if state[vkControl]&0x80 != 0 && state[vkMenu]&0x80 == 0 { // Ctrl without Alt (AltGr = Ctrl+Alt stays)
		state[vkControl], state[0xA2], state[0xA3] = 0, 0, 0
	}
	hkl, _, _ := pGetKeyboardLayout.Call(0)
	var buf [8]uint16
	// flags: bit 2 (0x4) = do not modify the keyboard state (Windows 10 1607+;
	// older systems ignore it), so peeking never swallows a pending dead key.
	n, _, _ := pToUnicodeEx.Call(uintptr(vk), uintptr(scan), uintptr(unsafe.Pointer(&state[0])),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 4, hkl)
	switch ni := int32(n); {
	case ni < 0:
		return "Dead"
	case ni == 0:
		return "Unidentified"
	default:
		return keymap.FromUTF16(buf[:ni])
	}
}

func (w *Window) onKey(m uint32, wp, lp uintptr) (uintptr, bool) {
	vk := int(wp)
	scan := int(lp>>16) & 0xff
	ext := lp&(1<<24) != 0
	down := m == wmKeyDown || m == wmSysKeyDown
	ev := shell.Event{
		Key:  keyName(vk, scan),
		Code: keymap.WinCode(scan, ext, vk),
		Mods: currentMods(),
	}
	if down {
		ev.Kind = shell.EvKeyDown
		ev.Repeat = lp&(1<<30) != 0
	} else {
		ev.Kind = shell.EvKeyUp
	}
	w.q.Push(ev)
	if m == wmSysKeyDown || m == wmSysKeyUp {
		// Let the system see Alt+F4 / Alt+Space (system menu); swallow the rest
		// (otherwise tapping Alt would start the menu loop and steal input).
		alt := lp&(1<<29) != 0
		if alt && (vk == 0x73 || vk == 0x20) {
			return 0, false
		}
		return 0, true
	}
	return 0, true
}

func (w *Window) onChar(u uint16) {
	r, ok := w.sj.Feed(u)
	if !ok || !keymap.Printable(r) {
		return
	}
	mods := currentMods()
	if mods&shell.ModCtrl != 0 && mods&shell.ModAlt == 0 {
		return // Ctrl+key is a shortcut, not text (AltGr = Ctrl+Alt is text)
	}
	w.q.Push(shell.Event{Kind: shell.EvText, Text: string(r), Mods: mods})
}

func (w *Window) onDrop(hdrop uintptr) {
	defer pDragFinish.Call(hdrop)
	n, _, _ := pDragQueryFileW.Call(hdrop, 0xFFFFFFFF, 0, 0)
	var files []string
	for i := uintptr(0); i < n; i++ {
		l, _, _ := pDragQueryFileW.Call(hdrop, i, 0, 0)
		if l == 0 || l > 32768 {
			continue
		}
		buf := make([]uint16, l+1)
		pDragQueryFileW.Call(hdrop, i, uintptr(unsafe.Pointer(&buf[0])), l+1)
		files = append(files, keymap.FromUTF16(buf[:l]))
	}
	if len(files) > 0 {
		w.q.Push(shell.Event{Kind: shell.EvDropFiles, Files: files})
	}
}
