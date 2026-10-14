package engine

// frames_paint.go: frames in the page's pixels.
//
// Layout emits an ordinary render.Bitmap op at the iframe's content box (render.FrameEnv); at raster time
// Env.Bitmap(iframe) asks the frame for its pixels: the child painted at the box size and the parent's device scale,
// scrolled by the frame's own scroll position, on a transparent canvas (a frame document with no background shows
// what is behind it). So the box clip, border radius, ancestors' transforms, opacity and filters, z-order and
// visibility behave as for a canvas. A frame that changes bumps the parent's Generation and calls its update hook,
// so a window repaints. Input is routed in frames_input.go.

import (
	"fmt"
	"image"
	"math"
	"strings"
	"sync"

	"golang.org/x/net/html"

	"github.com/sweefbrowser/sweef/internal/render"
)

// lazyMargin: a loading="lazy" frame starts to load when it is within this many CSS px of the viewport.
const lazyMargin = 1250

// framePaint caches the pixels of a frame document.
type framePaint struct {
	mu       sync.Mutex
	w, h     int // the box (content size) the frame is laid out in
	by, bh   float64
	img      *image.RGBA
	imgGen   uint64
	imgW     int
	imgH     int
	imgDSF   float64
	imgSY    int
	imgValid bool
	lastGen  uint64
}

// contentChanged is called when something painted by this document changed (a frame of it repainted, an image
// arrived): the window must paint again, and so must the document that embeds this one.
func (t *Tab) contentChanged() {
	t.win.frameGen.Add(1)
	if f := t.onUpdate.Load(); f != nil {
		(*f)()
	}
}

// frameKid returns the child tab of the iframe element with the backend node id.
func (t *Tab) frameKid(elem int64) *Tab {
	t.fr.mu.Lock()
	defer t.fr.mu.Unlock()
	return t.fr.kids[elem]
}

// HasFrame implements render.FrameEnv: the iframe element has a live frame document.
func (e *tabEnv) HasFrame(n *html.Node) bool {
	k := e.t.frameKid(e.doc.IDOf(n))
	return k != nil && !k.closed.Load()
}

// frameTick is called by the frame's event loop after every slice: a changed document repaints the parent.
func (c *Tab) frameTick() {
	g := c.Generation()
	c.win.fp.mu.Lock()
	changed := g != c.win.fp.lastGen
	c.win.fp.lastGen = g
	c.win.fp.mu.Unlock()
	if changed && c.isFrame() {
		c.bc.parent.contentChanged()
	}
}

// ---- layout feedback --------------------------------------------------------------------

// updateFrames runs at the end of every layout pass of the parent: it gives each frame the size of its box and
// releases lazy frames that came near the viewport. It only reads the layout result (the layout lock is held).
func (e *tabEnv) updateFrames(res *render.Result, vh float64) {
	t := e.t
	t.fr.mu.Lock()
	kids := make(map[int64]*Tab, len(t.fr.kids))
	for id, k := range t.fr.kids {
		kids[id] = k
	}
	t.fr.mu.Unlock()
	lazy := t.win.lazyWaiting()
	if len(kids) == 0 && !lazy {
		return
	}
	for id, k := range kids {
		n := e.doc.NodeByID(id)
		if n == nil {
			continue
		}
		b := res.Boxes[n]
		if b == nil || b.Display == "none" {
			continue
		}
		if w, h := int(math.Round(b.Content.W)), int(math.Round(b.Content.H)); w > 0 && h > 0 {
			k.resizeFrame(w, h)
		}
		k.win.fp.mu.Lock()
		k.win.fp.by, k.win.fp.bh = b.Border.Y, b.Border.H
		k.win.fp.mu.Unlock()
	}
	if lazy {
		t.win.lazyMu.Lock()
		for id := range t.win.lazyBox {
			if n := e.doc.NodeByID(id); n != nil {
				if b := res.Boxes[n]; b != nil && b.Display != "none" {
					t.win.lazyBox[id] = [2]float64{b.Border.Y, b.Border.H}
				}
			}
		}
		t.win.lazyMu.Unlock()
		t.releaseLazyFrames(float64(t.ScrollY()), vh)
	}
}

func (w *winState) lazyWaiting() bool {
	w.lazyMu.Lock()
	defer w.lazyMu.Unlock()
	return len(w.lazyBox) > 0
}

// holdLazy: ensureFrame calls it for an iframe with loading="lazy" that has not been released. It returns true when
// the frame must wait.
func (t *Tab) holdLazy(elem int64) bool {
	t.win.lazyMu.Lock()
	defer t.win.lazyMu.Unlock()
	if t.win.lazyOK[elem] {
		return false
	}
	if t.win.lazyBox == nil {
		t.win.lazyBox = map[int64][2]float64{}
	}
	if _, ok := t.win.lazyBox[elem]; !ok {
		t.win.lazyBox[elem] = [2]float64{math.Inf(1), 0} // position unknown until layout
	}
	return true
}

// releaseLazyFrames starts the waiting lazy frames whose box is within lazyMargin of [sy, sy+vh].
func (t *Tab) releaseLazyFrames(sy, vh float64) {
	var go_ []int64
	t.win.lazyMu.Lock()
	for id, b := range t.win.lazyBox {
		if b[0] < sy+vh+lazyMargin && b[0]+b[1] > sy-lazyMargin {
			if t.win.lazyOK == nil {
				t.win.lazyOK = map[int64]bool{}
			}
			t.win.lazyOK[id] = true
			delete(t.win.lazyBox, id)
			go_ = append(go_, id)
		}
	}
	t.win.lazyMu.Unlock()
	for _, id := range go_ {
		id := id
		go t.ensureFrame(id)
	}
}

// startLazyFramesNear is the scroll hook (SetScrollY).
func (t *Tab) startLazyFramesNear() {
	if !t.win.lazyWaiting() {
		return
	}
	_, vh, _, _, _ := t.Viewport()
	t.releaseLazyFrames(float64(t.ScrollY()), float64(vh))
}

// resizeFrame gives the frame document a new viewport.
func (c *Tab) resizeFrame(w, h int) {
	c.win.fp.mu.Lock()
	same := c.win.fp.w == w && c.win.fp.h == h
	c.win.fp.w, c.win.fp.h = w, h
	c.win.fp.mu.Unlock()
	if same {
		return
	}
	_, _, dsf, mobile, landscape := c.Viewport()
	c.SetViewport(w, h, dsf, mobile, landscape)
	c.mu.RLock()
	d := c.dom
	c.mu.RUnlock()
	if d != nil {
		d.Touch() // a new viewport size is a new layout
	}
	c.bc.parent.contentChanged()
}

// ---- painting --------------------------------------------------------------------------

func (t *Tab) setPaintDSF(d float64) { t.win.paintDSF.Store(math.Float64bits(d)) }

func (t *Tab) lastPaintDSF() float64 {
	d := math.Float64frombits(t.win.paintDSF.Load())
	if d <= 0 {
		return 1
	}
	return d
}

// framePixels returns the frame's current pixels at the parent's scale, repainting the child only when its content,
// size, scale or scroll position changed.
func (c *Tab) framePixels() image.Image {
	if c.closed.Load() || !c.isFrame() {
		return nil
	}
	dsf := c.bc.parent.lastPaintDSF()
	fp := &c.win.fp
	fp.mu.Lock()
	w, h := fp.w, fp.h
	fp.mu.Unlock()
	if w <= 0 || h <= 0 {
		return nil
	}
	gen := c.Generation()
	sy := c.frameScrollY(h)
	fp.mu.Lock()
	if fp.imgValid && fp.imgGen == gen && fp.imgW == w && fp.imgH == h && fp.imgDSF == dsf && fp.imgSY == sy {
		img := fp.img
		fp.mu.Unlock()
		return img
	}
	fp.mu.Unlock()
	img, ok := c.PaintRGBAOpts(PaintOptions{Width: w, Height: h, DSF: dsf, Transparent: true, ScrollY: sy})
	if !ok || img == nil {
		return nil
	}
	fp.mu.Lock()
	fp.img, fp.imgGen, fp.imgW, fp.imgH, fp.imgDSF, fp.imgSY, fp.imgValid = img, gen, w, h, dsf, sy, true
	fp.mu.Unlock()
	return img
}

// frameScrollY is the frame's scroll offset clamped to its content.
func (t *Tab) frameScrollY(vh int) int {
	sy := t.ScrollY()
	if sy <= 0 {
		return 0
	}
	if max := t.docHeight() - vh; sy > max {
		sy = max
	}
	if sy < 0 {
		sy = 0
	}
	return sy
}

// docHeight is the height of the laid out document in CSS px.
func (t *Tab) docHeight() int {
	t.mu.RLock()
	d := t.dom
	t.mu.RUnlock()
	if d == nil {
		return 0
	}
	w := d.LayoutWidth()
	if w <= 0 {
		w = t.vpW
	}
	l := d.Layout(w)
	if ext, _ := l.Ext.(*renderExt); ext != nil {
		return int(math.Ceil(ext.res.Height))
	}
	return l.Height
}

// SetFramesPaused pauses (true) or resumes the frames' event loops: a background tab should not run its embedded
// players. A paused frame keeps its pixels.
func (t *Tab) SetFramesPaused(p bool) {
	t.win.framesPaused.Store(p)
	for _, k := range t.FrameTabs() {
		k.SetFramesPaused(p)
		if p {
			continue
		}
		k.fr.mu.Lock()
		rt := k.fr.rt
		k.fr.mu.Unlock()
		if rt != nil {
			rt.nudge()
		}
	}
}

// ---- frames in the snapshot ---------------------------------------------------------------

// frameRefs: "f1" is the first frame of the document in tree order, "f1f2" the second frame inside it.

// frameIndex is the 1-based position of the child among its parent's frames.
func (c *Tab) frameIndex() int {
	for i, k := range c.bc.parent.FrameTabs() {
		if k == c {
			return i + 1
		}
	}
	return 0
}

// FrameByRef resolves "f1" or "f1f2" to the nested frame tab.
func (t *Tab) FrameByRef(ref string) (*Tab, bool) {
	cur := t
	for ref != "" {
		if ref[0] != 'f' {
			return nil, false
		}
		i, n := 1, 0
		for i < len(ref) && ref[i] >= '0' && ref[i] <= '9' {
			n = n*10 + int(ref[i]-'0')
			i++
		}
		if i == 1 || n < 1 {
			return nil, false
		}
		kids := cur.FrameTabs()
		if n > len(kids) || !t.eng.frameReadable(kids[n-1].URL()) {
			return nil, false
		}
		cur, ref = kids[n-1], ref[i:]
	}
	return cur, cur != t
}

// splitFrameRef splits a frame-qualified selector "f1e5" / "f1f2e5" / "ref=f1e5" into the frame path and the
// selector inside it ("e5").
func splitFrameRef(sel string) (path, inner string, ok bool) {
	s := strings.TrimPrefix(strings.TrimSpace(sel), "ref=")
	i := 0
	for i < len(s) && s[i] == 'f' {
		j := i + 1
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		if j == i+1 {
			return "", "", false
		}
		i = j
	}
	if i == 0 || i+1 >= len(s) || s[i] != 'e' || s[i+1] < '0' || s[i+1] > '9' {
		return "", "", false
	}
	for k := i + 1; k < len(s); k++ {
		if s[k] < '0' || s[k] > '9' {
			return "", "", false
		}
	}
	return s[:i], s[i:], true
}

// routeFrameSel resolves a frame-qualified ref to the tab that owns it.
func (t *Tab) routeFrameSel(sel string) (*Tab, string, bool) {
	path, inner, ok := splitFrameRef(sel)
	if !ok {
		return nil, "", false
	}
	c, ok := t.FrameByRef(path)
	if !ok {
		return nil, "", false
	}
	return c, inner, true
}

// frameRefUnresolved is the error for a frame-qualified ref ("f1e5") that names no readable frame: it is never a CSS
// selector, so the action fails at once instead of waiting for an element that cannot appear.
func frameRefUnresolved(sel string) error {
	if _, _, ok := splitFrameRef(sel); ok {
		return fmt.Errorf("ref %s names a frame that is not on this page (or may not be read); take a new snapshot", strings.TrimSpace(sel))
	}
	return nil
}
