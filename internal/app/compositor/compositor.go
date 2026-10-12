// Package compositor stitches the chrome layer, the page texture and an
// optional alpha overlay into one RGBA frame.
//
// Layers (bottom to top):
//
//  1. chrome: window-sized RGBA painted from the chrome page tab. Its
//     viewport rectangle is a hole: only the pixels outside it are shown.
//  2. page: a tall RGBA texture (the whole document, or a capped prefix)
//     painted once per page change. Scrolling just moves a window over it,
//     so scrolling never calls the engine and costs one memcpy.
//  3. overlay: a small premultiplied-alpha surface (menus, popovers) blended
//     over the result with a global opacity for fades.
//
// Damage tracking is exact: SetChrome/SetPage diff the new pixels against
// the old ones and only the bounding box of real changes is recomposed and
// handed to Present. Compose allocates nothing in steady state.
//
// All coordinates are device pixels. Callers convert from logical pixels with
// the window scale.
package compositor

import (
	"image"
	"image/color"
	"sort"
	"sync"
	"time"
)

// Compositor is safe for concurrent use; every method takes one mutex, held
// only for pixel copies.
type Compositor struct {
	mu sync.Mutex

	w, h  int
	scale float64
	frame *image.RGBA

	chrome *image.RGBA
	page   *image.RGBA
	pageH  int // valid rows in page (texture may be taller than needed)
	vp     image.Rectangle
	scroll int   // device px offset of the visible window into page
	docH   int   // full document height in device px (>= pageH); sets the scroll range
	sbA    uint8 // overlay scrollbar opacity (0 = hidden)

	ovl      *image.RGBA
	ovlAt    image.Point
	ovlAlpha uint8 // 0 = hidden

	bg     color.RGBA // page background below the end of the texture
	damage image.Rectangle
	frames int

	composeTimes ring
	paintTimes   map[string]*ring
}

// New returns a compositor for a w x h device-pixel window.
func New(w, h int, scale float64) *Compositor {
	c := &Compositor{bg: color.RGBA{255, 255, 255, 255}, paintTimes: map[string]*ring{}, scale: scale}
	c.resize(w, h)
	return c
}

func (c *Compositor) resize(w, h int) {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	c.w, c.h = w, h
	c.frame = image.NewRGBA(image.Rect(0, 0, w, h))
	c.chrome = image.NewRGBA(image.Rect(0, 0, w, h))
	fillWhite(c.frame)
	c.damage = c.frame.Bounds()
}

// Resize changes the window size (device px) and scale; everything is
// damaged and the chrome must be repainted by the caller.
func (c *Compositor) Resize(w, h int, scale float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.scale = scale
	if w != c.w || h != c.h {
		c.resize(w, h)
	}
	c.damage = c.frame.Bounds()
	c.clampScroll()
}

// Size returns the device-pixel size.
func (c *Compositor) Size() (w, h int, scale float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.w, c.h, c.scale
}

// SetViewport sets the hole rectangle (device px).
func (c *Compositor) SetViewport(r image.Rectangle) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r = r.Intersect(image.Rect(0, 0, c.w, c.h))
	if r == c.vp {
		return
	}
	c.damage = c.damage.Union(c.vp).Union(r)
	c.vp = r
	c.clampScroll()
}

// Viewport returns the current hole.
func (c *Compositor) Viewport() image.Rectangle {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.vp
}

// SetBackground sets the colour shown below the end of a short page.
func (c *Compositor) SetBackground(col color.RGBA) {
	c.mu.Lock()
	c.bg = col
	c.damage = c.damage.Union(c.vp)
	c.mu.Unlock()
}

// SetChrome installs new chrome pixels. src must have the window's size.
// The compositor takes ownership of src and returns its previous buffer for
// reuse (nil on the first call or a size change). Damage becomes the
// bounding box of the pixels that actually changed (outside the hole).
func (c *Compositor) SetChrome(src *image.RGBA) (spare *image.RGBA) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if src == nil {
		return nil
	}
	sb := src.Bounds()
	if sb.Dx() == c.w && sb.Dy() == c.h {
		d := diff(c.chrome, src, c.chrome.Bounds())
		spare, c.chrome = c.chrome, src
		if !d.Empty() {
			c.damage = c.damage.Union(c.outsideHole(d))
		}
		return spare
	}
	// Partial paint (only the strip of the window the chrome occupies) or a
	// size that no longer matches (resize in flight): diff and copy what fits
	// into the chrome buffer, keeping the rest; the buffer stays ours.
	r := sb.Intersect(c.chrome.Bounds())
	d := diff(c.chrome, src, r)
	if !d.Empty() {
		copyRect(c.chrome, src, d)
		c.damage = c.damage.Union(c.outsideHole(d))
	}
	return src
}

// outsideHole shrinks d when it lies entirely inside the viewport hole.
func (c *Compositor) outsideHole(d image.Rectangle) image.Rectangle {
	if c.vp.Empty() {
		return d
	}
	if d.In(c.vp) {
		return image.Rectangle{}
	}
	return d
}

// SetPage installs new page pixels (the whole document or a prefix). The
// compositor takes ownership and returns the previous texture for reuse.
// Damage is the changed part of the visible window.
func (c *Compositor) SetPage(src *image.RGBA) (spare *image.RGBA) {
	c.mu.Lock()
	defer c.mu.Unlock()
	old := c.page
	c.page = src
	if src == nil {
		c.pageH = 0
		c.damage = c.damage.Union(c.vp)
		return old
	}
	c.pageH = src.Bounds().Dy()
	c.clampScroll()
	if old == nil || old.Bounds() != src.Bounds() {
		c.damage = c.damage.Union(c.vp)
		return old
	}
	// diff only the visible window
	win := image.Rect(0, c.scroll, src.Bounds().Dx(), c.scroll+c.vp.Dy())
	if d := diff(old, src, win); !d.Empty() {
		d = d.Sub(image.Pt(0, c.scroll)).Add(c.vp.Min)
		c.damage = c.damage.Union(d.Intersect(c.vp))
	}
	return old
}

// PageHeight is the texture height in device px (0 without a page).
func (c *Compositor) PageHeight() int { c.mu.Lock(); defer c.mu.Unlock(); return c.pageH }

// SetDocHeight tells the compositor the full document height (device px).
// The page texture may be shorter (it is painted ahead of the scroll
// position); the scroll range follows the document.
func (c *Compositor) SetDocHeight(h int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if h == c.docH {
		return
	}
	c.docH = h
	c.clampScroll()
	if c.sbA > 0 {
		c.damage = c.damage.Union(c.sbStrip())
	}
}

// SetScrollbar sets the overlay scrollbar opacity (0 hides it).
func (c *Compositor) SetScrollbar(a uint8) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if a == c.sbA {
		return
	}
	c.sbA = a
	c.damage = c.damage.Union(c.sbStrip())
}

func (c *Compositor) sbStrip() image.Rectangle {
	w := c.sbWidth()
	return image.Rect(c.vp.Max.X-w-4, c.vp.Min.Y, c.vp.Max.X, c.vp.Max.Y)
}

func (c *Compositor) sbWidth() int {
	w := int(6*c.scale + 0.5)
	if w < 4 {
		w = 4
	}
	return w
}

// sbThumb returns the scrollbar thumb rectangle, empty if nothing scrolls.
func (c *Compositor) sbThumb() image.Rectangle {
	total := c.docH
	if c.pageH > total {
		total = c.pageH
	}
	vh := c.vp.Dy()
	if total <= vh || vh <= 0 {
		return image.Rectangle{}
	}
	th := vh * vh / total
	if th < 24 {
		th = 24
	}
	if th > vh {
		th = vh
	}
	y := c.vp.Min.Y + (vh-th)*c.scroll/(total-vh)
	w := c.sbWidth()
	return image.Rect(c.vp.Max.X-w-2, y, c.vp.Max.X-2, y+th)
}

func (c *Compositor) maxScroll() int {
	total := c.docH
	if c.pageH > total {
		total = c.pageH
	}
	m := total - c.vp.Dy()
	if m < 0 {
		return 0
	}
	return m
}

func (c *Compositor) clampScroll() {
	if m := c.maxScroll(); c.scroll > m {
		c.scroll = m
	}
	if c.scroll < 0 {
		c.scroll = 0
	}
}

// Scroll returns the scroll offset and its maximum (device px).
func (c *Compositor) Scroll() (pos, max int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.scroll, c.maxScroll()
}

// ScrollTo sets the scroll offset; it reports whether the view moved.
func (c *Compositor) ScrollTo(y int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	old := c.scroll
	c.scroll = y
	c.clampScroll()
	if c.scroll == old {
		return false
	}
	c.damage = c.damage.Union(c.vp)
	return true
}

// SetOverlay shows img at position at with global opacity alpha (0..255).
// img must be premultiplied RGBA. A nil img or alpha 0 hides the overlay.
// The surface is referenced, not copied; call it again after mutating it.
func (c *Compositor) SetOverlay(img *image.RGBA, at image.Point, alpha uint8) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ovl != nil && c.ovlAlpha > 0 {
		c.damage = c.damage.Union(image.Rectangle{Min: c.ovlAt, Max: c.ovlAt.Add(c.ovl.Bounds().Size())})
	}
	c.ovl, c.ovlAt, c.ovlAlpha = img, at, alpha
	if img == nil {
		c.ovlAlpha = 0
		return
	}
	if alpha > 0 {
		c.damage = c.damage.Union(image.Rectangle{Min: at, Max: at.Add(img.Bounds().Size())})
	}
}

// Invalidate marks a region (or everything, when empty) as needing recompose.
func (c *Compositor) Invalidate(r image.Rectangle) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if r.Empty() {
		r = image.Rect(0, 0, c.w, c.h)
	}
	c.damage = c.damage.Union(r.Intersect(image.Rect(0, 0, c.w, c.h)))
}

// Dirty reports whether Compose has work to do.
func (c *Compositor) Dirty() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.damage.Intersect(image.Rect(0, 0, c.w, c.h)).Empty()
}

// Compose redraws the damaged region of the frame and returns the frame and
// the damage rectangle (empty when nothing changed). The frame is only valid
// until the next call that mutates the compositor, so Present must copy it
// (shell.Window.Present is documented not to retain it).
func (c *Compositor) Compose() (*image.RGBA, image.Rectangle) {
	t0 := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	d := c.damage.Intersect(c.frame.Bounds())
	c.damage = image.Rectangle{}
	if d.Empty() {
		return c.frame, d
	}
	// 1. chrome everywhere except the hole
	vp := c.vp
	hole := d.Intersect(vp)
	if hole.Empty() {
		copyRect(c.frame, c.chrome, d)
	} else {
		if top := image.Rect(d.Min.X, d.Min.Y, d.Max.X, hole.Min.Y); !top.Empty() {
			copyRect(c.frame, c.chrome, top)
		}
		if bot := image.Rect(d.Min.X, hole.Max.Y, d.Max.X, d.Max.Y); !bot.Empty() {
			copyRect(c.frame, c.chrome, bot)
		}
		if l := image.Rect(d.Min.X, hole.Min.Y, hole.Min.X, hole.Max.Y); !l.Empty() {
			copyRect(c.frame, c.chrome, l)
		}
		if r := image.Rect(hole.Max.X, hole.Min.Y, d.Max.X, hole.Max.Y); !r.Empty() {
			copyRect(c.frame, c.chrome, r)
		}
		c.blitPage(hole)
		if c.sbA > 0 {
			if th := c.sbThumb(); !th.Intersect(hole).Empty() {
				blendRect(c.frame, th.Intersect(hole), color.RGBA{60, 60, 60, 255}, c.sbA)
			}
		}
	}
	// 3. overlay
	if c.ovl != nil && c.ovlAlpha > 0 {
		or := image.Rectangle{Min: c.ovlAt, Max: c.ovlAt.Add(c.ovl.Bounds().Size())}.Intersect(d)
		if !or.Empty() {
			blend(c.frame, c.ovl, c.ovlAt, or, c.ovlAlpha)
		}
	}
	c.frames++
	c.composeTimes.add(time.Since(t0))
	return c.frame, d
}

// blitPage copies the page window into the part of the hole inside r.
func (c *Compositor) blitPage(r image.Rectangle) {
	if c.page == nil {
		fillRect(c.frame, r, c.bg)
		return
	}
	pw := c.page.Bounds().Dx()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		py := y - c.vp.Min.Y + c.scroll
		dst := c.frame.Pix[c.frame.PixOffset(r.Min.X, y):c.frame.PixOffset(r.Max.X, y)]
		if py >= c.pageH || py < 0 {
			fillRow(dst, c.bg)
			continue
		}
		px0 := r.Min.X - c.vp.Min.X
		px1 := r.Max.X - c.vp.Min.X
		n := px1 - px0
		if px1 > pw {
			n = pw - px0
		}
		if n > 0 {
			so := c.page.PixOffset(px0, py)
			copy(dst[:4*n], c.page.Pix[so:so+4*n])
		}
		if n < px1-px0 {
			if n < 0 {
				n = 0
			}
			fillRow(dst[4*n:], c.bg)
		}
	}
}

// Frame returns the last composed frame (for screenshots).
func (c *Compositor) Frame() *image.RGBA { return c.frame }

// CopyFrame returns a copy of the current frame.
func (c *Compositor) CopyFrame() *image.RGBA {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := image.NewRGBA(c.frame.Bounds())
	copy(out.Pix, c.frame.Pix)
	return out
}

// ---- stats ------------------------------------------------------------------

type ring struct {
	buf [256]time.Duration
	n   int
}

func (r *ring) add(d time.Duration) { r.buf[r.n%len(r.buf)] = d; r.n++ }

func (r *ring) snapshot() []time.Duration {
	n := r.n
	if n > len(r.buf) {
		n = len(r.buf)
	}
	return append([]time.Duration(nil), r.buf[:n]...)
}

// Percentiles summarises a set of durations.
type Percentiles struct {
	N             int
	P50, P99, Max time.Duration
}

func percentiles(ds []time.Duration) Percentiles {
	if len(ds) == 0 {
		return Percentiles{}
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	at := func(p float64) time.Duration {
		i := int(p * float64(len(ds)-1))
		return ds[i]
	}
	return Percentiles{N: len(ds), P50: at(0.5), P99: at(0.99), Max: ds[len(ds)-1]}
}

// RecordPaint stores how long an external paint (kind "chrome" or "page")
// took, so Stats can report it next to the compose time.
func (c *Compositor) RecordPaint(kind string, d time.Duration) {
	c.mu.Lock()
	r := c.paintTimes[kind]
	if r == nil {
		r = &ring{}
		c.paintTimes[kind] = r
	}
	r.add(d)
	c.mu.Unlock()
}

// Stats is the frame timing report (`sweef gui --stats`).
type Stats struct {
	Frames  int
	Compose Percentiles
	Paint   map[string]Percentiles
}

// Stats returns timing over the last 256 samples of each series.
func (c *Compositor) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := Stats{Frames: c.frames, Compose: percentiles(c.composeTimes.snapshot()), Paint: map[string]Percentiles{}}
	for k, r := range c.paintTimes {
		s.Paint[k] = percentiles(r.snapshot())
	}
	return s
}
