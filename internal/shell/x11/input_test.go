//go:build linux

package x11

import (
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/shell"
)

func movePointerInto(t *testing.T, x *xtest, w *Window, lx, ly int) {
	t.Helper()
	ox, oy, _, _ := x.c.geometry(w.win)
	x.motion(ox+lx, oy+ly)
}

func TestMouseAndWheel(t *testing.T) {
	w := openTest(t, shellCfg(400, 300))
	x := newXtest(t)
	movePointerInto(t, x, w, 50, 60)
	e := next(t, w, func(e shell.Event) bool { return e.Kind == shell.EvMouseMove && e.X == 50 && e.Y == 60 })
	_ = e
	x.button(1, true)
	d := next(t, w, ofKind(shell.EvMouseDown))
	if d.Button != 0 || d.X != 50 || d.Y != 60 {
		t.Fatalf("%+v", d)
	}
	x.button(1, false)
	next(t, w, ofKind(shell.EvMouseUp))
	x.button(3, true)
	if d := next(t, w, ofKind(shell.EvMouseDown)); d.Button != 2 {
		t.Fatalf("right button %+v", d)
	}
	x.button(3, false)
	x.button(2, true)
	if d := next(t, w, ofKind(shell.EvMouseDown)); d.Button != 1 {
		t.Fatalf("middle button %+v", d)
	}
	x.button(2, false)
	// wheel: button 5 = scroll down = content moves up = positive DY
	x.button(5, true)
	x.button(5, false)
	wh := next(t, w, ofKind(shell.EvWheel))
	if wh.DY <= 0 || wh.DX != 0 || wh.Precise {
		t.Fatalf("wheel down %+v", wh)
	}
	x.button(4, true)
	x.button(4, false)
	if wh := next(t, w, ofKind(shell.EvWheel)); wh.DY >= 0 {
		t.Fatalf("wheel up %+v", wh)
	}
	x.button(7, true)
	x.button(7, false)
	if wh := next(t, w, ofKind(shell.EvWheel)); wh.DX <= 0 {
		t.Fatalf("wheel right %+v", wh)
	}
}

func TestKeyboard(t *testing.T) {
	w := openTest(t, shellCfg(400, 300))
	x := newXtest(t)
	movePointerInto(t, x, w, 10, 10)
	next(t, w, ofKind(shell.EvMouseMove))
	kc := func(ks uint32) uint8 {
		k := w.keycodeFor(ks)
		if k == 0 {
			t.Skipf("no keycode for keysym %#x in test keymap", ks)
		}
		return k
	}
	a, shift, ctrl, ret, left := kc('a'), kc(0xffe1), kc(0xffe3), kc(0xff0d), kc(0xff51)

	x.key(a, true)
	d := next(t, w, ofKind(shell.EvKeyDown))
	if d.Key != "a" || d.Code != "KeyA" || d.Mods != 0 || d.Repeat {
		t.Fatalf("a down %+v", d)
	}
	if tx := next(t, w, ofKind(shell.EvText)); tx.Text != "a" {
		t.Fatalf("text %+v", tx)
	}
	x.key(a, true) // second press without release = auto-repeat
	if d := next(t, w, ofKind(shell.EvKeyDown)); !d.Repeat {
		t.Fatalf("expected Repeat %+v", d)
	}
	next(t, w, ofKind(shell.EvText))
	x.key(a, false)
	if u := next(t, w, ofKind(shell.EvKeyUp)); u.Key != "a" || u.Code != "KeyA" {
		t.Fatalf("a up %+v", u)
	}

	x.key(shift, true)
	if d := next(t, w, ofKind(shell.EvKeyDown)); d.Key != "Shift" || d.Code != "ShiftLeft" || d.Mods != shell.ModShift {
		t.Fatalf("shift down %+v", d)
	}
	x.key(a, true)
	if d := next(t, w, ofKind(shell.EvKeyDown)); d.Key != "A" || d.Mods != shell.ModShift {
		t.Fatalf("A down %+v", d)
	}
	if tx := next(t, w, ofKind(shell.EvText)); tx.Text != "A" {
		t.Fatalf("text %+v", tx)
	}
	x.key(a, false)
	next(t, w, ofKind(shell.EvKeyUp))
	x.key(shift, false)
	if u := next(t, w, ofKind(shell.EvKeyUp)); u.Key != "Shift" || u.Mods != 0 {
		t.Fatalf("shift up %+v", u)
	}

	x.key(ctrl, true)
	next(t, w, ofKind(shell.EvKeyDown))
	x.key(a, true)
	if d := next(t, w, ofKind(shell.EvKeyDown)); d.Key != "a" || d.Mods != shell.ModCtrl {
		t.Fatalf("ctrl+a %+v", d)
	}
	expectNone(t, w, 100*time.Millisecond, ofKind(shell.EvText)) // no text with Ctrl
	x.key(a, false)
	x.key(ctrl, false)

	x.key(ret, true)
	if d := next(t, w, ofKind(shell.EvKeyDown)); d.Key != "Enter" || d.Code != "Enter" {
		t.Fatalf("enter %+v", d)
	}
	x.key(ret, false)
	x.key(left, true)
	if d := next(t, w, ofKind(shell.EvKeyDown)); d.Key != "ArrowLeft" || d.Code != "ArrowLeft" {
		t.Fatalf("left %+v", d)
	}
	x.key(left, false)
}

func TestFocusAndClose(t *testing.T) {
	w := openTest(t, shellCfg(300, 200))
	tc := testConn(t)
	setFocus := func(win uint32) {
		b := request(42, 1, 12) // SetInputFocus, revert-to PointerRoot
		le.PutUint32(b[4:], win)
		tc.send(b, false)
		tc.sync()
	}
	setFocus(w.win)
	if e := next(t, w, ofKind(shell.EvFocus)); !e.Focus {
		t.Fatalf("focus %+v", e)
	}
	setFocus(1) // PointerRoot
	if e := next(t, w, ofKind(shell.EvFocus)); e.Focus {
		t.Fatalf("blur %+v", e)
	}
	// WM_DELETE_WINDOW
	ev := clientMessage32(w.win, w.atoms["WM_PROTOCOLS"], [5]uint32{w.atoms["WM_DELETE_WINDOW"], 0, 0, 0, 0})
	tc.sendEvent(w.win, false, 0, ev)
	next(t, w, ofKind(shell.EvClose))
}

func TestInitialResizeAndSize(t *testing.T) {
	w := openTest(t, shellCfg(333, 222))
	e := next(t, w, ofKind(shell.EvResize))
	if e.W != 333 || e.H != 222 || e.Scale != 1 {
		t.Fatalf("%+v", e)
	}
	lw, lh, s := w.Size()
	if lw != 333 || lh != 222 || s != 1 {
		t.Fatal(lw, lh, s)
	}
}

func TestScaleFromEnv(t *testing.T) {
	t.Setenv("GDK_SCALE", "2")
	w := openTest(t, shellCfg(300, 200))
	_, _, dw, dh := testConn(t).geometry(w.win)
	if dw != 600 || dh != 400 {
		t.Fatalf("device size %dx%d", dw, dh)
	}
	e := next(t, w, ofKind(shell.EvResize))
	if e.W != 300 || e.H != 200 || e.Scale != 2 {
		t.Fatalf("%+v", e)
	}
	x := newXtest(t)
	ox, oy, _, _ := x.c.geometry(w.win)
	x.motion(ox+100, oy+80) // device px -> 50,40 logical
	m := next(t, w, func(e shell.Event) bool { return e.Kind == shell.EvMouseMove && e.X == 50 })
	if m.Y != 40 {
		t.Fatalf("%+v", m)
	}
}

func TestScaleFromXftDPIAndLiveChange(t *testing.T) {
	tc := testConn(t)
	setRM := func(s string) {
		tc.changeProperty(0, tc.screen.root, atomResourceManager, atomString, 8, []byte(s))
		tc.sync()
	}
	setRM("Xft.antialias:\t1\nXft.dpi:\t144\n")
	defer setRM("")
	w := openTest(t, shellCfg(400, 300))
	if _, _, s := w.Size(); s != 1.5 {
		t.Fatalf("scale %v", s)
	}
	setRM("Xft.dpi:\t192\n")
	e := next(t, w, ofKind(shell.EvScale))
	if e.Scale != 2 {
		t.Fatalf("%+v", e)
	}
}

func TestTitleIconCursorFullscreen(t *testing.T) {
	w := openTest(t, shellCfg(400, 300))
	tc := testConn(t)
	w.SetTitle("Héllo – Sweef")
	tc.sync()
	w.Sync()
	pr, err := tc.getProperty(w.win, w.atoms["_NET_WM_NAME"], w.atoms["UTF8_STRING"], false, 0, 100)
	if err != nil || string(pr.value) != "Héllo – Sweef" {
		t.Fatalf("title %q %v", pr.value, err)
	}
	icon := gradient(64, 64)
	w.SetIcon(icon)
	w.Sync()
	pr, _ = tc.getProperty(w.win, w.atoms["_NET_WM_ICON"], atomCardinal, false, 0, 1<<16)
	if len(pr.value) != 8+64*64*4 || le.Uint32(pr.value) != 64 {
		t.Fatalf("icon property len %d", len(pr.value))
	}
	for _, c := range []shell.Cursor{shell.CursorPointer, shell.CursorText, shell.CursorWait, shell.CursorCrosshair,
		shell.CursorResizeEW, shell.CursorResizeNS, shell.CursorNotAllowed, shell.CursorGrab, shell.CursorDefault} {
		w.SetCursor(c)
	}
	w.Sync()
	if e := w.LastError(); e != nil {
		t.Fatal(e)
	}
	// No window manager under Xvfb: the shell falls back to resizing itself.
	w.SetFullscreen(true)
	e := next(t, w, func(e shell.Event) bool { return e.Kind == shell.EvResize && e.W == 1920 })
	if e.H != 1080 {
		t.Fatalf("%+v", e)
	}
	w.SetFullscreen(false)
	next(t, w, func(e shell.Event) bool { return e.Kind == shell.EvResize && e.W == 400 && e.H == 300 })
}

func TestOpenURLRejectsOptions(t *testing.T) {
	w := openTest(t, shellCfg(100, 100))
	if err := w.OpenURL("--help"); err == nil {
		t.Fatal("expected error for non-URL")
	}
	if err := w.OpenURL("/etc/passwd"); err == nil {
		t.Fatal("expected error for path")
	}
}
