//go:build linux

// Package x11 is the Linux native window shell: a pure-Go X11 client that
// speaks the wire protocol directly (no Xlib, no CGO). See ../README.md.
package x11

import (
	"errors"
	"fmt"
	"image"
	"math"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/sweefbrowser/sweef/internal/shell"
	"github.com/sweefbrowser/sweef/internal/shell/evqueue"
	"github.com/sweefbrowser/sweef/internal/shell/keymap"
)

// wheelNotch is the logical-pixel distance one mouse-wheel notch scrolls
// (Chromium's value on Linux).
const wheelNotch = 53

var atomNames = []string{
	"WM_PROTOCOLS", "WM_DELETE_WINDOW", "_NET_WM_NAME", "UTF8_STRING", "_NET_WM_STATE",
	"_NET_WM_STATE_FULLSCREEN", "_NET_WM_ICON", "_NET_WM_PID", "_NET_WM_PING",
	"CLIPBOARD", "TARGETS", "INCR", "TEXT", "text/plain;charset=utf-8", "text/plain",
	"_SWEEF_CLIP", "_SWEEF_DND", "XdndAware", "XdndEnter", "XdndPosition", "XdndStatus",
	"XdndLeave", "XdndDrop", "XdndFinished", "XdndSelection", "XdndActionCopy",
	"XdndTypeList", "text/uri-list", "_GTK_THEME_VARIANT", "_NET_SUPPORTING_WM_CHECK",
	"WM_CLIENT_MACHINE", "_NET_WM_WINDOW_TYPE", "_NET_WM_WINDOW_TYPE_NORMAL",
}

// Window is an X11 top-level window implementing shell.Window.
type Window struct {
	c     *conn
	q     *evqueue.Queue
	win   uint32
	gc    uint32
	root  uint32
	atoms map[string]uint32

	// geometry (device pixels) and scale
	gmu              sync.Mutex
	devW, devH       int
	scale            float64
	scaleLocked      bool // SWEEF_SCALE / GDK_SCALE override Xft.dpi
	fullscreen       bool
	savedGeo         [4]int
	screenW, screenH int

	closed atomic.Bool
	kb     atomic.Pointer[kbdMap]

	// input state, touched only by the reader goroutine
	pressed map[uint8]bool
	tr      keyTranslator
	focused bool

	// presentation
	pmu       sync.Mutex
	rgbx      bool
	shadow    []byte
	capW      int
	capH      int
	useSHM    bool
	shm       shmState
	putBuf    []byte
	exposeCh  chan image.Rectangle
	exposeAcc image.Rectangle
	emu       sync.Mutex

	// clipboard / selection
	clipMu     sync.Mutex // serialises Clipboard() requests
	ownMu      sync.Mutex
	clipText   string
	owning     bool
	incr       map[incrKey]*incrSend
	selNotify  chan selNotifyEvent
	propNotify chan struct{}

	// cursors
	curMu      sync.Mutex
	cursorFont uint32
	cursors    map[shell.Cursor]uint32

	// XDND
	dnd dndState

	errMu   sync.Mutex
	lastErr *Error
}

// Open opens a window on $DISPLAY.
func Open(cfg shell.Config) (shell.Window, error) {
	w, err := OpenDisplay("", cfg)
	if err != nil {
		return nil, err
	}
	return w, nil
}

// OpenDisplay opens a window on the named display ("" = $DISPLAY).
func OpenDisplay(display string, cfg shell.Config) (*Window, error) {
	c, err := dialDisplay(display)
	if err != nil {
		return nil, err
	}
	w, err := newWindow(c, cfg)
	if err != nil {
		c.close()
		return nil, err
	}
	return w, nil
}

func newWindow(c *conn, cfg shell.Config) (*Window, error) {
	scr := c.screen
	vis, ok := scr.visuals[scr.rootVisual]
	if !ok {
		return nil, errors.New("x11: root visual not found")
	}
	if bpp := c.bitsPerPixel[scr.rootDepth]; bpp != 32 {
		return nil, fmt.Errorf("x11: unsupported root depth %d (%d bpp); need 32 bits per pixel", scr.rootDepth, bpp)
	}
	w := &Window{
		c: c, q: evqueue.New(), root: scr.root, pressed: map[uint8]bool{},
		exposeCh:   make(chan image.Rectangle, 1),
		selNotify:  make(chan selNotifyEvent, 4),
		propNotify: make(chan struct{}, 64),
		incr:       map[incrKey]*incrSend{},
		cursors:    map[shell.Cursor]uint32{},
		screenW:    scr.width, screenH: scr.height,
	}
	switch {
	case vis.redMask == 0xff0000 && vis.blu == 0xff:
		w.rgbx = false
	case vis.redMask == 0xff && vis.blu == 0xff0000:
		w.rgbx = true
	default:
		return nil, fmt.Errorf("x11: unsupported visual masks r=%#x b=%#x", vis.redMask, vis.blu)
	}
	c.handler = w.onEvent
	c.onError = func(e *Error) {
		w.errMu.Lock()
		w.lastErr = e
		w.errMu.Unlock()
		if os.Getenv("SWEEF_X11_DEBUG") != "" {
			fmt.Fprintln(os.Stderr, e)
		}
	}

	atoms, err := c.internAll(atomNames)
	if err != nil {
		return nil, err
	}
	w.atoms = atoms

	w.scale = w.detectScale()
	cw, ch := cfg.Width, cfg.Height
	if cw <= 0 {
		cw = 1280
	}
	if ch <= 0 {
		ch = 800
	}
	w.devW, w.devH = keymap.ToDevice(cw, w.scale), keymap.ToDevice(ch, w.scale)
	if w.devW > 16000 || w.devH > 16000 {
		return nil, errors.New("x11: window too large")
	}

	w.win = c.allocID()
	x, y := (scr.width-w.devW)/2, (scr.height-w.devH)/2
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	b := request(opCreateWindow, 0, 32+8)
	le.PutUint32(b[4:], w.win)
	le.PutUint32(b[8:], scr.root)
	le.PutUint16(b[12:], uint16(int16(x)))
	le.PutUint16(b[14:], uint16(int16(y)))
	le.PutUint16(b[16:], uint16(w.devW))
	le.PutUint16(b[18:], uint16(w.devH))
	le.PutUint16(b[22:], 1) // InputOutput
	le.PutUint32(b[28:], 0x1|0x800)
	le.PutUint32(b[32:], 0) // background pixmap None: we repaint from the shadow
	le.PutUint32(b[36:], maskKeyPress|maskKeyRelease|maskButtonPress|maskButtonRelease|
		maskEnterWindow|maskLeaveWindow|maskPointerMotion|maskExposure|maskStructure|
		maskFocusChange|maskPropertyChange)
	if _, err := c.send(b, false); err != nil {
		return nil, err
	}

	w.gc = c.allocID()
	g := request(opCreateGC, 0, 20)
	le.PutUint32(g[4:], w.gc)
	le.PutUint32(g[8:], w.win)
	le.PutUint32(g[12:], 1<<16) // GraphicsExposures
	le.PutUint32(g[16:], 0)
	c.send(g, false)

	w.setWMProperties(cfg)
	w.setupInput()
	w.setupSHM()
	w.dndInit()
	// RESOURCE_MANAGER changes on the root window (live DPI changes).
	c.changeWindowAttrs(scr.root, 0x800, maskPropertyChange)
	c.mapWindow(w.win)
	if err := c.sync(); err != nil {
		return nil, err
	}

	go w.exposeLoop()
	go func() {
		<-c.done
		if !w.closed.Load() {
			w.q.Push(shell.Event{Kind: shell.EvClose})
		}
		w.shmWake()
	}()
	if cfg.Icon != nil {
		w.SetIcon(cfg.Icon)
	}
	lw, lh := keymap.LogicalSize(w.devW, w.devH, w.scale)
	w.q.Push(shell.Event{Kind: shell.EvResize, W: lw, H: lh, Scale: w.scale})
	return w, nil
}

func (w *Window) detectScale() float64 {
	for _, env := range []string{"SWEEF_SCALE", "GDK_SCALE"} {
		if s, ok := keymap.ParseScaleEnv(os.Getenv(env)); ok {
			w.scaleLocked = true
			return s
		}
	}
	return w.xftScale()
}

func (w *Window) xftScale() float64 {
	pr, err := w.c.getProperty(w.root, atomResourceManager, atomString, false, 0, 1<<16)
	if err == nil && pr != nil {
		if dpi, ok := keymap.ParseXftDPI(string(pr.value)); ok {
			return keymap.ScaleFromDPI(dpi)
		}
	}
	return 1
}

func (w *Window) setWMProperties(cfg shell.Config) {
	c := w.c
	a := w.atoms
	title := cfg.Title
	if title == "" {
		title = "Sweef"
	}
	w.SetTitle(title)
	c.changeProperty(0, w.win, atomWMClass, atomString, 8, []byte("sweef\x00Sweef\x00"))
	c.changeProperty(0, w.win, a["WM_PROTOCOLS"], atomAtom, 32,
		u32s(a["WM_DELETE_WINDOW"], a["_NET_WM_PING"]))
	c.changeProperty(0, w.win, a["_NET_WM_PID"], atomCardinal, 32, u32s(uint32(os.Getpid())))
	if h, err := os.Hostname(); err == nil {
		c.changeProperty(0, w.win, a["WM_CLIENT_MACHINE"], atomString, 8, latin1(h))
	}
	c.changeProperty(0, w.win, a["_NET_WM_WINDOW_TYPE"], atomAtom, 32, u32s(a["_NET_WM_WINDOW_TYPE_NORMAL"]))
	if cfg.Dark {
		c.changeProperty(0, w.win, a["_GTK_THEME_VARIANT"], a["UTF8_STRING"], 8, []byte("dark"))
	}
	// WM_NORMAL_HINTS: PMinSize (1<<4): keep the window usable.
	hints := make([]byte, 72)
	le.PutUint32(hints[0:], 1<<4)
	le.PutUint32(hints[20:], 200)
	le.PutUint32(hints[24:], 120)
	c.changeProperty(0, w.win, atomWMNormalHints, atomWMNormalHints, 32, hints)
}

// SetTitle sets WM_NAME and _NET_WM_NAME.
func (w *Window) SetTitle(t string) {
	if w.closed.Load() {
		return
	}
	t = strings.ReplaceAll(t, "\x00", "")
	w.c.changeProperty(0, w.win, atomWMName, atomString, 8, latin1(t))
	w.c.changeProperty(0, w.win, w.atoms["_NET_WM_NAME"], w.atoms["UTF8_STRING"], 8, []byte(t))
}

// SetIcon sets _NET_WM_ICON from a premultiplied RGBA image (downscaled to
// at most 256x256 with nearest-neighbour sampling if larger).
func (w *Window) SetIcon(icon *image.RGBA) {
	if icon == nil || w.closed.Load() {
		return
	}
	bw, bh := icon.Rect.Dx(), icon.Rect.Dy()
	if bw <= 0 || bh <= 0 {
		return
	}
	ow, oh := bw, bh
	for ow > 256 || oh > 256 {
		ow, oh = (ow+1)/2, (oh+1)/2
	}
	data := make([]byte, 8+ow*oh*4)
	le.PutUint32(data[0:], uint32(ow))
	le.PutUint32(data[4:], uint32(oh))
	for y := 0; y < oh; y++ {
		for x := 0; x < ow; x++ {
			sx, sy := icon.Rect.Min.X+x*bw/ow, icon.Rect.Min.Y+y*bh/oh
			i := icon.PixOffset(sx, sy)
			r, g, b, al := uint32(icon.Pix[i]), uint32(icon.Pix[i+1]), uint32(icon.Pix[i+2]), uint32(icon.Pix[i+3])
			if al != 0 && al != 255 { // EWMH wants non-premultiplied ARGB
				r, g, b = min32(r*255/al), min32(g*255/al), min32(b*255/al)
			}
			le.PutUint32(data[8+(y*ow+x)*4:], al<<24|r<<16|g<<8|b)
		}
	}
	w.c.changeProperty(0, w.win, w.atoms["_NET_WM_ICON"], atomCardinal, 32, data)
}

func min32(v uint32) uint32 {
	if v > 255 {
		return 255
	}
	return v
}

// Events implements shell.Window.
func (w *Window) Events() <-chan shell.Event { return w.q.Events() }

// Size implements shell.Window.
func (w *Window) Size() (int, int, float64) {
	w.gmu.Lock()
	defer w.gmu.Unlock()
	lw, lh := keymap.LogicalSize(w.devW, w.devH, w.scale)
	return lw, lh, w.scale
}

func (w *Window) devSize() (int, int) {
	w.gmu.Lock()
	defer w.gmu.Unlock()
	return w.devW, w.devH
}

func (w *Window) curScale() float64 {
	w.gmu.Lock()
	defer w.gmu.Unlock()
	return w.scale
}

// Sync waits until the X server has processed everything sent so far.
func (w *Window) Sync() error { return w.c.sync() }

// LastError returns the most recent asynchronous protocol error, if any.
func (w *Window) LastError() *Error {
	w.errMu.Lock()
	defer w.errMu.Unlock()
	return w.lastErr
}

// UsesSHM reports whether Present goes through MIT-SHM.
func (w *Window) UsesSHM() bool { return w.useSHM }

// WindowID returns the X window id (for tests and tools).
func (w *Window) WindowID() uint32 { return w.win }

// Cursor glyph indices in the standard X "cursor" font.
var cursorGlyph = map[shell.Cursor]uint16{
	shell.CursorDefault: 68, shell.CursorPointer: 60, shell.CursorText: 152,
	shell.CursorWait: 150, shell.CursorCrosshair: 34, shell.CursorResizeEW: 108,
	shell.CursorResizeNS: 116, shell.CursorNotAllowed: 24, shell.CursorGrab: 58,
}

// SetCursor implements shell.Window using core-font glyph cursors, which
// need no theme files and work on every server.
func (w *Window) SetCursor(cur shell.Cursor) {
	if w.closed.Load() {
		return
	}
	w.curMu.Lock()
	defer w.curMu.Unlock()
	id, ok := w.cursors[cur]
	if !ok {
		glyph, known := cursorGlyph[cur]
		if !known {
			glyph = 68
		}
		if w.cursorFont == 0 {
			w.cursorFont = w.c.allocID()
			name := "cursor"
			b := request(opOpenFont, 0, 12+len(name))
			le.PutUint32(b[4:], w.cursorFont)
			le.PutUint16(b[8:], uint16(len(name)))
			copy(b[12:], name)
			w.c.send(b, false)
		}
		id = w.c.allocID()
		b := request(opCreateGlyphCursor, 0, 32)
		le.PutUint32(b[4:], id)
		le.PutUint32(b[8:], w.cursorFont)
		le.PutUint32(b[12:], w.cursorFont)
		le.PutUint16(b[16:], glyph)
		le.PutUint16(b[18:], glyph+1)
		le.PutUint16(b[26:], 0xffff) // background white, foreground black
		le.PutUint16(b[28:], 0xffff)
		le.PutUint16(b[30:], 0xffff)
		w.c.send(b, false)
		w.cursors[cur] = id
	}
	w.c.changeWindowAttrs(w.win, 0x4000, id)
}

// SetFullscreen asks the window manager (EWMH) to toggle fullscreen; without
// a window manager it resizes the window to cover the screen itself.
func (w *Window) SetFullscreen(on bool) {
	if w.closed.Load() {
		return
	}
	w.gmu.Lock()
	if w.fullscreen == on {
		w.gmu.Unlock()
		return
	}
	w.fullscreen = on
	w.gmu.Unlock()
	if w.hasEWMHWM() {
		add := uint32(0)
		if on {
			add = 1
		}
		ev := clientMessage32(w.win, w.atoms["_NET_WM_STATE"], [5]uint32{add, w.atoms["_NET_WM_STATE_FULLSCREEN"], 0, 1, 0})
		w.c.sendEvent(w.root, false, maskSubstructRedir|maskSubstructNotif, ev)
		return
	}
	// No WM: manage geometry ourselves.
	if on {
		w.gmu.Lock()
		w.savedGeo = [4]int{0, 0, w.devW, w.devH}
		w.gmu.Unlock()
		w.c.configureWindow(w.win, 1|2|4|8, 0, 0, uint32(w.screenW), uint32(w.screenH))
	} else {
		g := w.savedGeo
		w.c.configureWindow(w.win, 1|2|4|8, uint32(g[0]), uint32(g[1]), uint32(g[2]), uint32(g[3]))
	}
}

func (w *Window) hasEWMHWM() bool {
	pr, err := w.c.getProperty(w.root, w.atoms["_NET_SUPPORTING_WM_CHECK"], atomWindow, false, 0, 1)
	return err == nil && pr != nil && len(pr.value) == 4
}

// OpenURL opens url with xdg-open. Only URLs with a scheme are accepted so a
// crafted value can never be interpreted as an option.
func (w *Window) OpenURL(raw string) error {
	if _, err := keymap.CheckOpenURL(raw); err != nil {
		return fmt.Errorf("x11: %w", err)
	}
	path, err := exec.LookPath("xdg-open")
	if err != nil {
		return fmt.Errorf("x11: xdg-open not found: %w", err)
	}
	cmd := exec.Command(path, raw)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// Close destroys the window and disconnects.
func (w *Window) Close() {
	if !w.closed.CompareAndSwap(false, true) {
		return
	}
	w.shmWake()
	w.c.destroyWindow(w.win)
	w.c.sync() // best effort: flush before the socket goes away
	w.shmFree()
	w.c.close()
	w.q.Close()
}

// ---- events ---------------------------------------------------------------

func i16(b []byte) int { return int(int16(le.Uint16(b))) }

func (w *Window) pos(x, y int) (float64, float64) {
	s := w.curScale()
	return float64(x) / s, float64(y) / s
}

func (w *Window) onEvent(ev []byte) {
	if w.closed.Load() {
		return
	}
	t := ev[0] & 0x7f
	if w.useSHM && t == w.shm.completionEvent {
		w.shmCompleted()
		return
	}
	switch t {
	case evKeyPress, evKeyRelease:
		w.onKey(ev, t == evKeyPress)
	case evButtonPress, evButtonRelease:
		w.onButton(ev, t == evButtonPress)
	case evMotionNotify, evEnterNotify, evLeaveNotify:
		x, y := w.pos(i16(ev[24:]), i16(ev[26:]))
		w.q.Push(shell.Event{Kind: shell.EvMouseMove, X: x, Y: y, Mods: modsFromState(le.Uint16(ev[28:]))})
	case evFocusIn, evFocusOut:
		detail := ev[1]
		if detail == 2 || detail >= 5 { // Inferior, Pointer, PointerRoot, None
			return
		}
		in := t == evFocusIn
		if in == w.focused {
			return
		}
		w.focused = in
		if !in {
			w.pressed = map[uint8]bool{}
			w.tr = keyTranslator{}
		}
		w.q.Push(shell.Event{Kind: shell.EvFocus, Focus: in})
	case evExpose:
		w.onExpose(ev)
	case evConfigureNotify:
		if le.Uint32(ev[8:]) != w.win {
			return
		}
		nw, nh := int(le.Uint16(ev[20:])), int(le.Uint16(ev[22:]))
		w.gmu.Lock()
		changed := nw != w.devW || nh != w.devH
		w.devW, w.devH = nw, nh
		s := w.scale
		w.gmu.Unlock()
		if changed {
			lw, lh := keymap.LogicalSize(nw, nh, s)
			w.q.Push(shell.Event{Kind: shell.EvResize, W: lw, H: lh, Scale: s})
		}
	case evClientMessage:
		w.onClientMessage(ev)
	case evSelectionRequest:
		w.onSelectionRequest(ev)
	case evSelectionClear:
		w.ownMu.Lock()
		if le.Uint32(ev[12:]) == w.atoms["CLIPBOARD"] {
			w.owning = false
			w.clipText = ""
		}
		w.ownMu.Unlock()
	case evSelectionNotify:
		w.onSelectionNotify(ev)
	case evPropertyNotify:
		w.onPropertyNotify(ev)
	case evMappingNotify:
		if ev[4] == 1 || ev[4] == 2 {
			go func() {
				if km, err := w.c.loadKeyboard(); err == nil {
					w.kb.Store(km)
				}
			}()
		}
	case evDestroyNotify:
		if le.Uint32(ev[8:]) == w.win {
			w.q.Push(shell.Event{Kind: shell.EvClose})
		}
	}
}

func (w *Window) onButton(ev []byte, press bool) {
	btn := int(ev[1])
	x, y := w.pos(i16(ev[24:]), i16(ev[26:]))
	mods := modsFromState(le.Uint16(ev[28:]))
	switch btn {
	case 4, 5, 6, 7:
		if !press {
			return
		}
		e := shell.Event{Kind: shell.EvWheel, X: x, Y: y, Mods: mods}
		switch btn {
		case 4:
			e.DY = -wheelNotch
		case 5:
			e.DY = wheelNotch
		case 6:
			e.DX = -wheelNotch
		case 7:
			e.DX = wheelNotch
		}
		w.q.Push(e)
		return
	}
	var b int
	switch btn {
	case 1:
		b = 0
	case 2:
		b = 1
	case 3:
		b = 2
	case 8: // back
		b = 3
	case 9: // forward
		b = 4
	default:
		return
	}
	k := shell.EvMouseUp
	if press {
		k = shell.EvMouseDown
	}
	w.q.Push(shell.Event{Kind: k, X: x, Y: y, Button: b, Mods: mods})
}

func (w *Window) onKey(ev []byte, press bool) {
	kc := ev[1]
	state := le.Uint16(ev[28:])
	km := w.kb.Load()
	ks := km.lookup(kc, state)
	code := keymap.X11Code(kc)
	mods := modsFromState(state)
	if press {
		repeat := w.pressed[kc]
		w.pressed[kc] = true
		key, text := w.tr.translate(ks, mods)
		mods |= modBitFor(key)
		w.q.Push(shell.Event{Kind: shell.EvKeyDown, Key: key, Code: code, Mods: mods, Repeat: repeat})
		if text != "" {
			w.q.Push(shell.Event{Kind: shell.EvText, Text: text, Mods: mods})
		}
		return
	}
	delete(w.pressed, kc)
	key := "Unidentified"
	if name, ok := keymap.KeysymKey(ks); ok {
		key = name
	} else if keymap.IsModifierKeysym(ks) {
		key = "Unidentified"
	} else if r, ok := keymap.KeysymToRune(ks); ok {
		key = string(r)
	} else if _, _, dead := keymap.DeadKeysym(ks); dead {
		key = "Dead"
	}
	mods &^= modBitFor(key)
	w.q.Push(shell.Event{Kind: shell.EvKeyUp, Key: key, Code: code, Mods: mods})
}

func (w *Window) onClientMessage(ev []byte) {
	typ := le.Uint32(ev[8:])
	a := w.atoms
	switch typ {
	case a["WM_PROTOCOLS"]:
		switch le.Uint32(ev[12:]) {
		case a["WM_DELETE_WINDOW"]:
			w.q.Push(shell.Event{Kind: shell.EvClose})
		case a["_NET_WM_PING"]:
			reply := append([]byte(nil), ev...)
			le.PutUint32(reply[4:], w.root)
			w.c.sendEvent(w.root, false, maskSubstructRedir|maskSubstructNotif, reply)
		}
	default:
		w.onDndMessage(typ, ev)
	}
}

func (w *Window) onPropertyNotify(ev []byte) {
	win, atom, state := le.Uint32(ev[4:]), le.Uint32(ev[8:]), ev[16]
	switch {
	case win == w.root && atom == atomResourceManager:
		go w.rescale()
	case win == w.win && atom == w.atoms["_SWEEF_CLIP"] && state == 0:
		select {
		case w.propNotify <- struct{}{}:
		default:
		}
	}
	if state == 1 { // deleted: continue INCR transfers to other windows
		w.incrDeleted(win, atom)
	}
}

func (w *Window) rescale() {
	if w.scaleLocked || w.closed.Load() {
		return
	}
	s := w.xftScale()
	w.gmu.Lock()
	changed := math.Abs(s-w.scale) > 1e-6
	w.scale = s
	dw, dh := w.devW, w.devH
	w.gmu.Unlock()
	if changed {
		lw, lh := keymap.LogicalSize(dw, dh, s)
		w.q.Push(shell.Event{Kind: shell.EvScale, Scale: s, W: lw, H: lh})
	}
}

func (w *Window) onExpose(ev []byte) {
	r := image.Rect(int(le.Uint16(ev[8:])), int(le.Uint16(ev[10:])), 0, 0)
	r.Max = image.Pt(r.Min.X+int(le.Uint16(ev[12:])), r.Min.Y+int(le.Uint16(ev[14:])))
	w.emu.Lock()
	w.exposeAcc = w.exposeAcc.Union(r)
	count := le.Uint16(ev[16:])
	acc := w.exposeAcc
	if count == 0 {
		w.exposeAcc = image.Rectangle{}
	}
	w.emu.Unlock()
	if count == 0 {
		select {
		case w.exposeCh <- acc:
		default:
			// painter busy: it will pick up a full redraw below
			select {
			case old := <-w.exposeCh:
				w.exposeCh <- old.Union(acc)
			default:
				w.exposeCh <- acc
			}
		}
	}
}

func (w *Window) exposeLoop() {
	for {
		select {
		case r := <-w.exposeCh:
			if w.closed.Load() {
				return
			}
			w.redraw(r)
		case <-w.c.done:
			return
		}
	}
}
