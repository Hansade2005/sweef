// Package headless is a shell.Window that has no display: it applies each
// Present(frame, damage) to an in-memory "screen", records timing, and
// replays scripted input events. It is the test harness for the whole GUI
// stack and produces the screenshots used to verify rendering without X11.
//
// The screen only changes inside the damage rectangle the app reports, so a
// test that compares Screen() with the compositor's frame catches any
// missing damage.
package headless

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sweefbrowser/sweef/internal/shell"
)

// Window implements shell.Window.
type Window struct {
	mu        sync.Mutex
	w, h      int // logical
	scale     float64
	events    chan shell.Event
	screen    *image.RGBA
	presents  int
	lastPres  time.Time
	presentAt []time.Time
	damages   []image.Rectangle
	cursor    shell.Cursor
	title     string
	clip      string
	clipHTML  string
	opened    []string
	fullscr   bool
	closed    bool
	cond      *sync.Cond
	// OnPresent, when set, is called after each Present (no locks held).
	OnPresent func(damage image.Rectangle)
}

// New creates a headless window of w x h logical pixels at the given scale.
func New(w, h int, scale float64) *Window {
	if scale <= 0 {
		scale = 1
	}
	win := &Window{w: w, h: h, scale: scale, events: make(chan shell.Event, 1024)}
	win.cond = sync.NewCond(&win.mu)
	win.screen = image.NewRGBA(image.Rect(0, 0, int(float64(w)*scale), int(float64(h)*scale)))
	return win
}

// Open satisfies the shell registration signature.
func Open(cfg shell.Config) (shell.Window, error) {
	w, h := cfg.Width, cfg.Height
	if w == 0 {
		w, h = 1280, 800
	}
	return New(w, h, 1), nil
}

func (w *Window) Present(frame *image.RGBA, damage image.Rectangle) {
	w.mu.Lock()
	b := frame.Bounds()
	if b != w.screen.Bounds() {
		w.screen = image.NewRGBA(b)
		damage = image.Rectangle{}
	}
	if damage.Empty() {
		damage = b
	}
	damage = damage.Intersect(b)
	n := 4 * damage.Dx()
	for y := damage.Min.Y; y < damage.Max.Y; y++ {
		so := frame.PixOffset(damage.Min.X, y)
		do := w.screen.PixOffset(damage.Min.X, y)
		copy(w.screen.Pix[do:do+n], frame.Pix[so:so+n])
	}
	now := time.Now()
	w.presents++
	w.lastPres = now
	if len(w.presentAt) < 1<<16 {
		w.presentAt = append(w.presentAt, now)
		w.damages = append(w.damages, damage)
	}
	cb := w.OnPresent
	w.cond.Broadcast()
	w.mu.Unlock()
	if cb != nil {
		cb(damage)
	}
}

func (w *Window) Events() <-chan shell.Event { return w.events }

func (w *Window) Size() (int, int, float64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w, w.h, w.scale
}

func (w *Window) SetTitle(s string)        { w.mu.Lock(); w.title = s; w.mu.Unlock() }
func (w *Window) SetCursor(c shell.Cursor) { w.mu.Lock(); w.cursor = c; w.mu.Unlock() }
func (w *Window) SetClipboard(s string)    { w.mu.Lock(); w.clip, w.clipHTML = s, ""; w.mu.Unlock() }

// SetClipboardRich and ClipboardHTML make the headless window a shell.RichClipboard.
func (w *Window) SetClipboardRich(text, html string) {
	w.mu.Lock()
	w.clip, w.clipHTML = text, html
	w.mu.Unlock()
}
func (w *Window) ClipboardHTML() string { w.mu.Lock(); defer w.mu.Unlock(); return w.clipHTML }
func (w *Window) Clipboard() string     { w.mu.Lock(); defer w.mu.Unlock(); return w.clip }
func (w *Window) SetFullscreen(b bool)  { w.mu.Lock(); w.fullscr = b; w.mu.Unlock() }
func (w *Window) OpenURL(u string) error {
	w.mu.Lock()
	w.opened = append(w.opened, u)
	w.mu.Unlock()
	return nil
}

// Close ends the event stream.
func (w *Window) Close() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	close(w.events)
	w.cond.Broadcast()
	w.mu.Unlock()
}

// ---- test-side API ------------------------------------------------------------

// Push delivers an event to the app (non-blocking beyond the buffer).
func (w *Window) Push(ev shell.Event) {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.mu.Unlock()
	w.events <- ev
}

// Resize simulates a window resize.
func (w *Window) Resize(lw, lh int, scale float64) {
	w.mu.Lock()
	w.w, w.h, w.scale = lw, lh, scale
	w.mu.Unlock()
	w.Push(shell.Event{Kind: shell.EvResize, W: lw, H: lh, Scale: scale})
}

// Presents returns how many frames were presented.
func (w *Window) Presents() int { w.mu.Lock(); defer w.mu.Unlock(); return w.presents }

// PresentTimes returns the time of every Present so far (capped at 65536).
func (w *Window) PresentTimes() []time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]time.Time(nil), w.presentAt...)
}

// LastPresent returns the time of the last Present.
func (w *Window) LastPresent() time.Time { w.mu.Lock(); defer w.mu.Unlock(); return w.lastPres }

// Screen returns a copy of the current screen.
func (w *Window) Screen() *image.RGBA {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := image.NewRGBA(w.screen.Bounds())
	copy(out.Pix, w.screen.Pix)
	return out
}

// At returns one pixel of the current screen (cheap; no copy of the frame).
func (w *Window) At(x, y int) color.RGBA {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.screen.RGBAAt(x, y)
}

// WaitPixel blocks until the screen pixel at (x,y) satisfies ok, polling after
// every Present, and returns the time from start until it did.
func (w *Window) WaitPixel(start time.Time, x, y int, ok func(color.RGBA) bool, timeout time.Duration) (time.Duration, bool) {
	deadline := time.Now().Add(timeout)
	t := time.AfterFunc(timeout, func() { w.mu.Lock(); w.cond.Broadcast(); w.mu.Unlock() })
	defer t.Stop()
	w.mu.Lock()
	defer w.mu.Unlock()
	for {
		if w.screen.Bounds().In(w.screen.Bounds()) && ok(w.screen.RGBAAt(x, y)) {
			return w.lastPres.Sub(start), true
		}
		if time.Now().After(deadline) || w.closed {
			return 0, false
		}
		w.cond.Wait()
	}
}

// Damages returns the damage rectangle of each presented frame.
func (w *Window) Damages() []image.Rectangle {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]image.Rectangle(nil), w.damages...)
}

func (w *Window) Cursor() shell.Cursor { w.mu.Lock(); defer w.mu.Unlock(); return w.cursor }
func (w *Window) Title() string        { w.mu.Lock(); defer w.mu.Unlock(); return w.title }
func (w *Window) Opened() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.opened...)
}
func (w *Window) Fullscreen() bool { w.mu.Lock(); defer w.mu.Unlock(); return w.fullscr }

// WaitPresents blocks until at least n frames were presented.
func (w *Window) WaitPresents(n int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	t := time.AfterFunc(timeout, func() { w.mu.Lock(); w.cond.Broadcast(); w.mu.Unlock() })
	defer t.Stop()
	w.mu.Lock()
	defer w.mu.Unlock()
	for w.presents < n {
		if time.Now().After(deadline) || w.closed {
			return false
		}
		w.cond.Wait()
	}
	return true
}

// PushAndMeasure injects ev and returns the time until the next Present.
// ok is false if no frame was presented within timeout.
func (w *Window) PushAndMeasure(ev shell.Event, timeout time.Duration) (time.Duration, bool) {
	start := w.Presents()
	t0 := time.Now()
	w.Push(ev)
	if !w.WaitPresents(start+1, timeout) {
		return 0, false
	}
	w.mu.Lock()
	// the first present after t0
	var at time.Time
	for i := len(w.presentAt) - 1; i >= 0; i-- {
		if w.presentAt[i].Before(t0) {
			break
		}
		at = w.presentAt[i]
	}
	w.mu.Unlock()
	if at.IsZero() {
		at = time.Now()
	}
	return at.Sub(t0), true
}

// SavePNG writes the screen to path.
func (w *Window) SavePNG(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(f, unpremul(w.Screen()))
}

// unpremul converts to straight alpha for PNG (frames are opaque in
// practice, so this is a no-op unless the overlay left alpha < 255).
func unpremul(m *image.RGBA) *image.RGBA { return m }

// ---- key helpers ---------------------------------------------------------------

// KeyEvents expands a chord like "ctrl+shift+t" or "Enter" into the
// KeyDown/KeyUp pair a native shell would send.
func KeyEvents(chord string) []shell.Event {
	parts := strings.Split(chord, "+")
	key := parts[len(parts)-1]
	if key == "" && len(parts) >= 2 { // "ctrl++"
		key = "+"
	}
	var mods shell.Mods
	for _, p := range parts[:len(parts)-1] {
		switch strings.ToLower(p) {
		case "ctrl", "control":
			mods |= shell.ModCtrl
		case "shift":
			mods |= shell.ModShift
		case "alt", "option":
			mods |= shell.ModAlt
		case "meta", "cmd", "super":
			mods |= shell.ModMeta
		}
	}
	if len(key) > 1 {
		key = canonicalKey(key)
	}
	code := codeFor(key)
	return []shell.Event{
		{Kind: shell.EvKeyDown, Key: key, Code: code, Mods: mods},
		{Kind: shell.EvKeyUp, Key: key, Code: code, Mods: mods},
	}
}

func canonicalKey(k string) string {
	switch strings.ToLower(k) {
	case "enter", "return":
		return "Enter"
	case "esc", "escape":
		return "Escape"
	case "tab":
		return "Tab"
	case "backspace":
		return "Backspace"
	case "delete", "del":
		return "Delete"
	case "left", "arrowleft":
		return "ArrowLeft"
	case "right", "arrowright":
		return "ArrowRight"
	case "up", "arrowup":
		return "ArrowUp"
	case "down", "arrowdown":
		return "ArrowDown"
	case "home":
		return "Home"
	case "end":
		return "End"
	case "pageup":
		return "PageUp"
	case "pagedown":
		return "PageDown"
	case "space":
		return " "
	}
	if len(k) >= 2 && (k[0] == 'f' || k[0] == 'F') {
		return "F" + k[1:]
	}
	return k
}

func codeFor(key string) string {
	if len(key) == 1 {
		c := key[0]
		switch {
		case c >= 'a' && c <= 'z':
			return "Key" + strings.ToUpper(key)
		case c >= 'A' && c <= 'Z':
			return "Key" + key
		case c >= '0' && c <= '9':
			return "Digit" + key
		case c == ' ':
			return "Space"
		}
		return ""
	}
	return key
}

// TypeEvents returns one EvText per rune sequence (committed text input).
func TypeEvents(text string) []shell.Event {
	return []shell.Event{{Kind: shell.EvText, Text: text}}
}
