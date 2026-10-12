//go:build darwin

package cocoa

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/png"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/ebitengine/purego/objc"

	"github.com/sweefbrowser/sweef/internal/shell"
	"github.com/sweefbrowser/sweef/internal/shell/evqueue"
	"github.com/sweefbrowser/sweef/internal/shell/keymap"
	"github.com/sweefbrowser/sweef/internal/shell/mainthread"
)

// NSEvent modifier flags and NSWindow style masks.
const (
	flagCapsLock = 1 << 16
	flagShift    = 1 << 17
	flagControl  = 1 << 18
	flagOption   = 1 << 19
	flagCommand  = 1 << 20

	styleTitled          = 1 << 0
	styleClosable        = 1 << 1
	styleMiniaturizable  = 1 << 2
	styleResizable       = 1 << 3
	styleFullSizeContent = 1 << 15
	styleFullScreen      = 1 << 14

	cgImageAlphaPremultipliedLast = 1
)

var (
	regMu        sync.Mutex
	reg          = map[objc.ID]*Window{} // view, delegate and window ids -> window
	windowsMu    sync.Mutex
	windowsByWin = map[objc.ID]*Window{}
	classOnce    sync.Once
	classErr     error
	viewClass    objc.Class
	delegateCls  objc.Class
)

// Window implements shell.Window on Cocoa.
type Window struct {
	q        *evqueue.Queue
	win      objc.ID
	view     objc.ID
	delegate objc.ID
	closed   atomic.Bool

	gmu        sync.Mutex
	logW, logH float64
	scale      float64

	// touched only on the main thread
	cursor  objc.ID
	marked  string
	modDown map[uint16]bool
	imeRect NSRect

	// pixel store the view draws from (malloc'ed so CoreGraphics may keep it)
	bmu   sync.Mutex
	buf   uintptr
	capW  int
	capH  int
	imgW  int
	imgH  int
	space uintptr

	dmu     sync.Mutex
	dmg     image.Rectangle
	flushed bool
}

func lookup(id objc.ID) *Window {
	regMu.Lock()
	defer regMu.Unlock()
	return reg[id]
}

// Open creates a window. The program must run under mainthread.Run (Cocoa
// requires the main thread); Open blocks until the window exists.
func Open(cfg shell.Config) (shell.Window, error) {
	if err := loadFrameworks(); err != nil {
		return nil, err
	}
	var w *Window
	var cerr error
	if err := mainthread.Do(func() { w, cerr = create(cfg) }); err != nil {
		return nil, fmt.Errorf("cocoa: %w (wrap main() in mainthread.Run)", err)
	}
	if cerr != nil {
		return nil, cerr
	}
	return w, nil
}

func create(cfg shell.Config) (*Window, error) {
	classOnce.Do(func() { classErr = registerClasses() })
	if classErr != nil {
		return nil, classErr
	}
	cw, ch := cfg.Width, cfg.Height
	if cw <= 0 {
		cw = 1280
	}
	if ch <= 0 {
		ch = 800
	}
	w := &Window{q: evqueue.New(), scale: 1, modDown: map[uint16]bool{}, imeRect: rect(0, 0, 1, 20)}

	style := uint(styleTitled | styleClosable | styleMiniaturizable | styleResizable)
	if cfg.UnifiedTitleBar {
		style |= styleFullSizeContent
	}
	win := send(send(class("NSWindow"), "alloc"), "initWithContentRect:styleMask:backing:defer:",
		rect(0, 0, float64(cw), float64(ch)), style, uint(2), false)
	if win == 0 {
		return nil, errors.New("cocoa: NSWindow creation failed")
	}
	send(win, "setReleasedWhenClosed:", false)
	title := cfg.Title
	if title == "" {
		title = "Sweef"
	}
	send(win, "setTitle:", nsString(title))
	if cfg.UnifiedTitleBar {
		send(win, "setTitlebarAppearsTransparent:", true)
		send(win, "setTitleVisibility:", int(1)) // NSWindowTitleHidden
	}
	if cfg.Dark {
		if ap := send(class("NSAppearance"), "appearanceNamed:", nsString("NSAppearanceNameDarkAqua")); ap != 0 {
			send(win, "setAppearance:", ap)
		}
	}
	send(win, "setCollectionBehavior:", uint(1<<7)) // FullScreenPrimary
	send(win, "setContentMinSize:", NSSize{Width: 200, Height: 120})
	send(win, "setAcceptsMouseMovedEvents:", true)

	view := send(send(objc.ID(viewClass), "alloc"), "initWithFrame:", rect(0, 0, float64(cw), float64(ch)))
	// Resize with the window.
	send(view, "setAutoresizingMask:", uint(2|16)) // NSViewWidthSizable|NSViewHeightSizable
	delegate := send(send(objc.ID(delegateCls), "alloc"), "init")
	send(win, "setContentView:", view)
	send(win, "setDelegate:", delegate)
	send(win, "makeFirstResponder:", view)

	// Mouse-exited events and files dropped from Finder.
	area := send(send(class("NSTrackingArea"), "alloc"), "initWithRect:options:owner:userInfo:",
		rect(0, 0, 0, 0), uint(0x01|0x02|0x20|0x200), view, objc.ID(0)) // entered/exited, moved, activeInKeyWindow, inVisibleRect
	send(view, "addTrackingArea:", area)
	send(view, "registerForDraggedTypes:", send(class("NSArray"), "arrayWithObject:", nsString("NSFilenamesPboardType")))

	w.win, w.view, w.delegate = win, view, delegate
	w.space = cgColorSpaceCreateDeviceRGB()
	regMu.Lock()
	reg[view], reg[delegate], reg[win] = w, w, w
	regMu.Unlock()
	windowsMu.Lock()
	windowsByWin[win] = w
	windowsMu.Unlock()

	w.syncGeometry()
	send(win, "center")
	send(win, "makeKeyAndOrderFront:", objc.ID(0))
	send(nsApp, "activateIgnoringOtherApps:", true)
	if cfg.Icon != nil {
		w.SetIcon(cfg.Icon)
	}
	w.pushResize()
	return w, nil
}

// syncGeometry reads the content size and scale (main thread).
func (w *Window) syncGeometry() {
	b := objc.Send[NSRect](w.view, sel("bounds"))
	s := sendFloat(w.win, "backingScaleFactor")
	if s <= 0 {
		s = 1
	}
	w.gmu.Lock()
	w.logW, w.logH, w.scale = b.Size.Width, b.Size.Height, s
	w.gmu.Unlock()
}

func (w *Window) pushResize() {
	w.gmu.Lock()
	lw, lh, s := w.logW, w.logH, w.scale
	w.gmu.Unlock()
	w.q.Push(shell.Event{Kind: shell.EvResize, W: int(lw + 0.5), H: int(lh + 0.5), Scale: s})
}

func (w *Window) curScale() float64 {
	w.gmu.Lock()
	defer w.gmu.Unlock()
	return w.scale
}

// ---- shell.Window ----------------------------------------------------------

func (w *Window) Events() <-chan shell.Event { return w.q.Events() }

func (w *Window) Size() (int, int, float64) {
	w.gmu.Lock()
	defer w.gmu.Unlock()
	return int(w.logW + 0.5), int(w.logH + 0.5), w.scale
}

func (w *Window) SetTitle(t string) {
	if w.closed.Load() {
		return
	}
	mainthread.Post(func() { send(w.win, "setTitle:", nsString(t)) })
}

var cursorSelectors = map[shell.Cursor]string{
	shell.CursorDefault: "arrowCursor", shell.CursorPointer: "pointingHandCursor",
	shell.CursorText: "IBeamCursor", shell.CursorWait: "arrowCursor", // macOS has no public busy cursor
	shell.CursorCrosshair: "crosshairCursor", shell.CursorResizeEW: "resizeLeftRightCursor",
	shell.CursorResizeNS: "resizeUpDownCursor", shell.CursorNotAllowed: "operationNotAllowedCursor",
	shell.CursorGrab: "openHandCursor",
}

func (w *Window) SetCursor(c shell.Cursor) {
	if w.closed.Load() {
		return
	}
	name, ok := cursorSelectors[c]
	if !ok {
		name = "arrowCursor"
	}
	mainthread.Post(func() {
		w.cursor = send(class("NSCursor"), name)
		send(w.win, "invalidateCursorRectsForView:", w.view)
		send(w.cursor, "set")
	})
}

func (w *Window) SetFullscreen(on bool) {
	if w.closed.Load() {
		return
	}
	mainthread.Post(func() {
		is := sendUint(w.win, "styleMask")&styleFullScreen != 0
		if is != on {
			send(w.win, "toggleFullScreen:", objc.ID(0))
		}
	})
}

// SetIMERect tells the input method where the text caret is (logical px,
// top-left origin) so candidate windows appear next to it.
func (w *Window) SetIMERect(r image.Rectangle) {
	mainthread.Post(func() {
		w.imeRect = rect(float64(r.Min.X), float64(r.Min.Y), float64(max(r.Dx(), 1)), float64(max(r.Dy(), 1)))
	})
}

// SetIcon sets the Dock/app icon.
func (w *Window) SetIcon(icon *image.RGBA) {
	if icon == nil || w.closed.Load() {
		return
	}
	var b bytes.Buffer
	if png.Encode(&b, icon) != nil {
		return
	}
	data := b.Bytes()
	mainthread.Post(func() {
		d := send(class("NSData"), "dataWithBytes:length:", uintptr(unsafe.Pointer(&data[0])), uint(len(data)))
		img := send(send(class("NSImage"), "alloc"), "initWithData:", d)
		if img != 0 {
			send(nsApp, "setApplicationIconImage:", img)
		}
	})
}

const pasteboardString = "public.utf8-plain-text"

func (w *Window) SetClipboard(s string) {
	if w.closed.Load() {
		return
	}
	mainthread.Do(func() {
		pb := send(class("NSPasteboard"), "generalPasteboard")
		send(pb, "clearContents")
		send(pb, "setString:forType:", nsString(s), nsString(pasteboardString))
	})
}

func (w *Window) Clipboard() string {
	if w.closed.Load() {
		return ""
	}
	var out string
	mainthread.Do(func() {
		pb := send(class("NSPasteboard"), "generalPasteboard")
		out = goString(send(pb, "stringForType:", nsString(pasteboardString)))
	})
	return out
}

func (w *Window) OpenURL(raw string) error {
	if _, err := keymap.CheckOpenURL(raw); err != nil {
		return fmt.Errorf("cocoa: %w", err)
	}
	var ok bool
	if err := mainthread.Do(func() {
		u := send(class("NSURL"), "URLWithString:", nsString(raw))
		if u == 0 {
			return
		}
		ws := send(class("NSWorkspace"), "sharedWorkspace")
		ok = sendBool(ws, "openURL:", u)
	}); err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("cocoa: NSWorkspace could not open %q", raw)
	}
	return nil
}

func (w *Window) Close() {
	if !w.closed.CompareAndSwap(false, true) {
		return
	}
	mainthread.Do(func() {
		send(w.win, "setDelegate:", objc.ID(0))
		send(w.win, "close")
		regMu.Lock()
		delete(reg, w.view)
		delete(reg, w.delegate)
		delete(reg, w.win)
		regMu.Unlock()
		windowsMu.Lock()
		delete(windowsByWin, w.win)
		windowsMu.Unlock()
		w.bmu.Lock()
		if w.buf != 0 {
			libcFree(w.buf)
			w.buf = 0
		}
		if w.space != 0 {
			cgColorSpaceRelease(w.space)
			w.space = 0
		}
		w.bmu.Unlock()
	})
	w.q.Close()
}

// ---- presentation ------------------------------------------------------------

// Present copies the damaged part of frame (premultiplied RGBA, which is
// exactly kCGImageAlphaPremultipliedLast) into the view's pixel store and
// schedules a redraw of that area.
func (w *Window) Present(frame *image.RGBA, damage image.Rectangle) {
	if frame == nil || w.closed.Load() {
		return
	}
	fw, fh := frame.Rect.Dx(), frame.Rect.Dy()
	if fw <= 0 || fh <= 0 {
		return
	}
	r := damage
	if r.Empty() {
		r = frame.Rect
	}
	r = r.Intersect(frame.Rect)
	if r.Empty() {
		return
	}
	w.bmu.Lock()
	if w.space == 0 {
		w.bmu.Unlock()
		return
	}
	resized := fw != w.imgW || fh != w.imgH
	if w.buf == 0 || fw > w.capW || fh > w.capH {
		ncw, nch := (max(fw, w.capW)+63)&^63, (max(fh, w.capH)+63)&^63
		nb := libcCalloc(uintptr(ncw*nch), 4)
		if nb == 0 {
			w.bmu.Unlock()
			return
		}
		if w.buf != 0 {
			old := unsafe.Slice((*byte)(ptrFrom(w.buf)), w.capW*w.capH*4)
			nw := unsafe.Slice((*byte)(ptrFrom(nb)), ncw*nch*4)
			for y := 0; y < min(w.capH, nch); y++ {
				copy(nw[y*ncw*4:y*ncw*4+min(w.capW, ncw)*4], old[y*w.capW*4:])
			}
			libcFree(w.buf)
		}
		w.buf, w.capW, w.capH = nb, ncw, nch
		resized = true
	}
	dst := unsafe.Slice((*byte)(ptrFrom(w.buf)), w.capW*w.capH*4)
	n := r.Dx() * 4
	for y := r.Min.Y; y < r.Max.Y; y++ {
		so := frame.PixOffset(r.Min.X, y)
		do := (y-frame.Rect.Min.Y)*w.capW*4 + (r.Min.X-frame.Rect.Min.X)*4
		copy(dst[do:do+n], frame.Pix[so:so+n])
	}
	w.imgW, w.imgH = fw, fh
	w.bmu.Unlock()

	if resized {
		r = image.Rect(0, 0, fw, fh)
	}
	w.invalidate(r.Sub(frame.Rect.Min))
}

// invalidate coalesces damage and asks the main thread to redraw it.
func (w *Window) invalidate(r image.Rectangle) {
	w.dmu.Lock()
	w.dmg = w.dmg.Union(r)
	if w.flushed {
		w.dmu.Unlock()
		return
	}
	w.flushed = true
	w.dmu.Unlock()
	mainthread.Post(func() {
		w.dmu.Lock()
		d := w.dmg
		w.dmg = image.Rectangle{}
		w.flushed = false
		w.dmu.Unlock()
		if w.closed.Load() || d.Empty() {
			return
		}
		s := w.curScale()
		send(w.view, "setNeedsDisplayInRect:", rect(
			float64(d.Min.X)/s-1, float64(d.Min.Y)/s-1,
			float64(d.Dx())/s+2, float64(d.Dy())/s+2))
	})
}

// draw is the body of -drawRect: (main thread).
func (w *Window) draw(cg uintptr, dirty NSRect) {
	w.bmu.Lock()
	defer w.bmu.Unlock()
	if cg == 0 || w.space == 0 {
		return
	}
	s := w.curScale()
	cgContextSaveGState(cg)
	defer cgContextRestoreGState(cg)
	if w.buf == 0 || w.imgW == 0 {
		cgContextSetRGBFillColor(cg, 0, 0, 0, 1)
		cgContextFillRect(cg, dirty)
		return
	}
	iw, ih := float64(w.imgW)/s, float64(w.imgH)/s
	if dirty.Origin.X+dirty.Size.Width > iw || dirty.Origin.Y+dirty.Size.Height > ih {
		cgContextSetRGBFillColor(cg, 0, 0, 0, 1) // area not covered by the frame yet
		cgContextFillRect(cg, dirty)
	}
	provider := cgDataProviderCreateWithData(0, w.buf, uintptr(w.capW*w.capH*4), 0)
	if provider == 0 {
		return
	}
	img := cgImageCreate(uintptr(w.imgW), uintptr(w.imgH), 8, 32, uintptr(w.capW*4), w.space,
		cgImageAlphaPremultipliedLast, provider, 0, false, 0)
	cgDataProviderRelease(provider)
	if img == 0 {
		return
	}
	cgContextSetInterpolation(cg, 1) // kCGInterpolationNone: pixel exact
	cgContextTranslateCTM(cg, 0, ih) // the view is flipped; CGContextDrawImage is not
	cgContextScaleCTM(cg, 1, -1)
	cgContextDrawImage(cg, rect(0, 0, iw, ih), img)
	cgImageRelease(img)
}
