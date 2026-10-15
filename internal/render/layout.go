package render

import (
	"image"
	"math"
	"sort"
	"strings"

	"golang.org/x/net/html"

	"github.com/sweefbrowser/sweef/internal/style"
)

type nilEnv struct{}

func (nilEnv) Image(string) image.Image                         { return nil }
func (nilEnv) Bitmap(*html.Node) image.Image                    { return nil }
func (nilEnv) IntrinsicSize(*html.Node) (w, h float64, ok bool) { return 0, 0, false }
func (nilEnv) Focused() *html.Node                              { return nil }

type ctx struct {
	cv          *cvState
	cvDepth     int // > 0 while building inside a skipped content-visibility subtree
	opts        Options
	sess        *Session  // cross-pass caches; nil: none
	svgU        *svgUnion // ids of all the page's inline svg elements (this pass)
	svgUDone    bool
	svgNoSess   bool // the session's scene cache is off for this pass (duplicate ids)
	env         Env
	scrollEnv   ScrollEnv // env's scroll offsets, when it keeps any
	eng         *style.Engine
	vw, vh      float64
	sbViewport  float64
	sbViewportH float64

	styles      map[*html.Node]*style.Computed
	byNode      map[*html.Node][]*lbox
	pseudoBoxes map[*html.Node][]*lbox
	counters    map[string][]int
	quoteDepth  int
	specs       map[string]*fontSpec
	specPtr     map[*style.Computed]*fontSpec

	rootBox *lbox
	bodyBox *lbox
	cbStack []*lbox
	inFlow  []*lbox
	noCache bool
	docEl   *html.Node
	quirks  bool
	res     *Result

	painter    *painter
	vcache     map[vkey]*style.Computed
	smallCaps  map[*style.Computed]*style.Computed
	vIntrinsic bool
	curFL      *lbox // IFC root whose first line is being finished
	all        []*lbox
	fo         map[*lbox][]*lbox                   // foreignObject wrappers per inline <svg> box
	foClip     map[*lbox][]func(x, y float64) bool // clip-path hit tests of the SVG ancestors of a foreignObject wrapper
	// CSS filter: url(#id) support (svgfilterref.go)
	urlFilterHint int
	filterEls     map[*html.Node]*style.Computed
}

// Layout lays out the document rooted at root (a document or html element).
//
// Documents that use @container rules are laid out repeatedly: each pass
// evaluates the queries against the container sizes measured in the previous
// pass until the sizes stop changing.
func Layout(root *html.Node, o Options) *Result {
	cv := &cvState{open: map[*html.Node]bool{}}
	run := func() *Result {
		res, obs, has := layoutPass(root, o, nil, cv)
		if has {
			var used map[*html.Node]style.ContSize
			for pass := 0; pass < 5 && !sameContSizes(used, obs); pass++ {
				used = obs
				res, obs, _ = layoutPass(root, o, used, cv)
			}
		}
		return res
	}
	res := run()
	if len(cv.cands) == 0 {
		return res
	}
	// content-visibility:auto: every auto subtree starts skipped; the ones whose
	// box reaches the viewport (plus margin) are opened and the page is laid out
	// again, until nothing new opens (see contentvis.go). The sizing rounds do
	// not build skipped contents; the last pass does.
	cv.fast = true
	for round := 0; round < 200; round++ {
		res = run()
		if !cv.openRelevant(res, o) {
			break
		}
	}
	cv.fast = false
	return run()
}

func sameContSizes(a, b map[*html.Node]style.ContSize) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		w, ok := b[k]
		if !ok || math.Abs(v.W-w.W) > 0.01 || math.Abs(v.H-w.H) > 0.01 {
			return false
		}
	}
	return true
}

// layoutPass is one full layout. It returns the sizes of the document's query
// containers as laid out, and whether the style engine saw any @container
// rule (a re-run is only needed then).
func layoutPass(root *html.Node, o Options, cq map[*html.Node]style.ContSize, cv *cvState) (*Result, map[*html.Node]style.ContSize, bool) {
	if o.Width <= 0 {
		o.Width = 1280
	}
	if o.Height <= 0 {
		o.Height = 800
	}
	if o.DPR <= 0 {
		o.DPR = 1
	}
	cv.cands = cv.cands[:0]
	c := &ctx{cv: cv, opts: o, env: o.Env, vw: o.Width, vh: o.Height,
		styles: map[*html.Node]*style.Computed{}, byNode: map[*html.Node][]*lbox{}, pseudoBoxes: map[*html.Node][]*lbox{},
		counters: map[string][]int{}, specs: map[string]*fontSpec{}, specPtr: map[*style.Computed]*fontSpec{}}
	if c.env == nil {
		c.env = nilEnv{}
	}
	c.scrollEnv, _ = c.env.(ScrollEnv)
	c.docEl = findDocEl(root)
	res := &Result{Boxes: map[*html.Node]*Box{}}
	c.res = res
	scheme := o.ColorScheme
	if scheme == "" {
		scheme = "light"
	}
	env := &style.Env{Width: o.Width, Height: o.Height, DPR: o.DPR, ColorScheme: scheme, Print: o.Print, ReducedMotion: o.ReducedMotion}
	var sheets []style.SheetIn
	for _, s := range o.Sheets {
		if s.UserAgent {
			continue
		}
		sheets = append(sheets, style.SheetIn{Text: s.Text, Media: s.Media})
	}
	c.quirks = isQuirks(root)
	if o.Session != nil {
		c.sess = o.Session
		o.Session.Style.SetWebFonts(o.Fonts)
		c.eng = o.Session.Style.Acquire(env, sheets, c.quirks)
		defer func() { o.Session.Style.EndPass(); o.Session.svg.sweep() }()
	} else {
		c.eng = style.NewEngineQ(env, sheets, c.quirks)
	}
	c.eng.SysFont = "arial"
	c.eng.SetWebFonts(o.Fonts)
	if cq != nil {
		c.eng.SetContainerSizes(cq)
	}
	registerFonts(c.eng.Fonts)
	if c.docEl == nil {
		res.Width, res.Height = o.Width, o.Height
		res.Viewport = Rect{0, 0, o.Width, o.Height}
		res.Hit = func(x, y float64) *html.Node { return nil }
		return res, nil, false
	}
	var top []*lbox
	tk := newStageClock()
	c.buildElement(c.docEl, nil, &top)
	tk.lap("build")
	if len(top) == 0 {
		res.Width, res.Height = o.Width, o.Height
		res.Viewport = Rect{0, 0, o.Width, o.Height}
		res.Hit = func(x, y float64) *html.Node { return nil }
		return res, nil, false
	}
	rb := top[0]
	rb.kind = kBlock
	c.rootBox = rb
	c.findBody(rb)
	c.layoutRoot(rb)
	tk.lap("layout")
	c.finishResult(rb)
	tk.lap("finish")
	if !c.eng.HasContainerQueries() {
		return res, nil, false
	}
	obs := map[*html.Node]style.ContSize{}
	for _, n := range c.eng.ContainerNodes() {
		for _, b := range c.byNode[n] {
			if b.pseudo != "" {
				continue
			}
			obs[n] = style.ContSize{W: b.w - b.bor[1] - b.bor[3] - b.pad[1] - b.pad[3] - b.sbW, H: b.h - b.bor[0] - b.bor[2] - b.pad[0] - b.pad[2] - b.sbH}
			break
		}
	}
	return res, obs, true
}

func findDocEl(n *html.Node) *html.Node {
	if n == nil {
		return nil
	}
	if n.Type == html.ElementNode && n.Data == "html" {
		return n
	}
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		if r := findDocEl(ch); r != nil {
			return r
		}
	}
	if n.Type == html.ElementNode {
		return n
	}
	return nil
}

func (c *ctx) findBody(rb *lbox) {
	for _, k := range rb.kids {
		if k.n != nil && k.n.Data == "body" && k.pseudo == "" {
			c.bodyBox = k
		}
	}
	// overflow propagation to the viewport
	if rb.st.OverflowX == "visible" && rb.st.OverflowY == "visible" && c.bodyBox != nil {
		c.bodyBox.ovProp = true
	} else {
		rb.ovProp = true
	}
}

func (c *ctx) viewportOverflow() (string, string) {
	if c.bodyBox != nil && c.bodyBox.ovProp {
		return c.bodyBox.st.OverflowX, c.bodyBox.st.OverflowY
	}
	return c.rootBox.st.OverflowX, c.rootBox.st.OverflowY
}

func (c *ctx) layoutRoot(rb *lbox) {
	_, oy := c.viewportOverflow()
	c.sbViewport = 0
	if oy == "scroll" {
		c.sbViewport = sbSize
	}
	for pass := 0; pass < 4; pass++ {
		c.resetKids(rb)
		cw := c.vw - c.sbViewport
		in := layoutIn{cbw: cw, cbh: c.vh - c.sbViewportH, fixW: -1, fixH: -1, bfc: &bfcState{}}
		c.cbStack = nil
		c.layoutBox(rb, in)
		rb.x, rb.y = rb.mar[3], rb.mar[0]
		c.place(rb, rb.x, rb.y)
		docH := math.Max(rb.ovBottom, rb.y+rb.h+rb.mar[2])
		docW := math.Max(rb.ovRight, rb.x+rb.w+rb.mar[1])
		ox, _ := c.viewportOverflow()
		if c.sbViewport == 0 && (oy == "auto" || oy == "visible") && docH > c.vh-c.sbViewportH+0.01 {
			c.sbViewport = sbSize
			continue
		}
		if c.sbViewportH == 0 && (ox == "auto" || ox == "visible" || ox == "scroll") && docW > c.vw-c.sbViewport+0.01 {
			c.sbViewportH = sbSize
			continue
		}
		break
	}
}

func registerFonts(ff []style.FontFace) {}

func (c *ctx) finishResult(rb *lbox) {
	res := c.res
	vw := c.vw - c.sbViewport
	res.Viewport = Rect{0, 0, vw, c.vh - c.sbViewportH}
	res.Width = math.Max(vw, math.Max(rb.ovRight, rb.ax+rb.w+rb.mar[1]))
	res.Height = math.Max(c.vh, math.Max(rb.ovBottom, rb.ay+rb.h+rb.mar[2]))
	tk := newStageClock()
	c.layoutForeignObjects()
	c.finishFilterRefs()
	tk.lap("finish:fo")
	c.buildBoxes()
	tk.lap("finish:boxes")
	c.prefetchPaintImages()
	c.paintAll(rb)
	tk.lap("finish:paint")
	c.installHit(rb)
	tk.lap("finish:hit")
}

// ---- placement: absolute coordinates ---------------------------------------------------

func (b *lbox) overflowX() string {
	if b.ovProp {
		return "visible"
	}
	return b.st.OverflowX
}
func (b *lbox) overflowY() string {
	if b.ovProp {
		return "visible"
	}
	return b.st.OverflowY
}

func (b *lbox) clips() bool {
	ox, oy := b.overflowX(), b.overflowY()
	return ox != "visible" || oy != "visible"
}

// place assigns absolute coordinates to b (whose layout origin is ox, oy) and
// its descendants and accumulates scrollable overflow.
func (c *ctx) place(b *lbox, ox, oy float64) {
	c.all = append(c.all, b)
	b.relX, b.relY = c.relativeOffset(b)
	b.ax, b.ay = ox+b.relX, oy+b.relY
	// A scroll container places its content as if the box sat at its scrolled position, so every box, line and
	// hit record of the content comes out where it appears on screen; the box itself is put back below.
	scx, scy := c.scrollOf(b)
	b.scx, b.scy = scx, scy
	b.ax, b.ay = b.ax-scx, b.ay-scy
	b.ovRight, b.ovBottom = b.ax+b.w, b.ay+b.h
	b.ovLeft, b.ovTop = b.ax, b.ay
	cx, cy := b.ax+b.bor[3]+b.pad[3], b.ay+b.bor[0]+b.pad[0]
	_ = cx
	_ = cy
	acc := func(k *lbox) {
		if k.hidden() {
			return
		}
		r, bt, l, t := k.ovRight, k.ovBottom, k.ovLeft, k.ovTop
		if k.hasTransform() {
			x0, y0, x1, y1 := k.transformedBounds(k.ovLeft, k.ovTop, k.ovRight, k.ovBottom)
			l, t, r, bt = x0, y0, x1, y1
		}
		if r > b.ovRight {
			b.ovRight = r
		}
		if bt > b.ovBottom {
			b.ovBottom = bt
		}
		if l < b.ovLeft {
			b.ovLeft = l
		}
		if t < b.ovTop {
			b.ovTop = t
		}
	}
	// kids in flow
	for _, k := range b.kids {
		if k.oof || k.kind == kText || k.kind == kInline || k.kind == kBreak || k.kind == kMarker {
			continue
		}
		if b.ifc {
			continue
		}
		c.place(k, b.ax+k.x, b.ay+k.y)
		if !k.hiddenFlow {
			acc(k)
		}
	}
	// inline content
	if b.ifc {
		c.placeInline(b)
		for _, k := range b.inlineKids {
			c.place(k, b.ax+k.x, b.ay+k.y)
			acc(k)
		}
		for _, ln := range b.lines {
			r := b.ax + b.bor[3] + b.pad[3] + ln.x + ln.contentW
			bt := b.ay + b.bor[0] + b.pad[0] + ln.y + ln.h
			if r > b.ovRight {
				b.ovRight = r
			}
			if bt > b.ovBottom {
				b.ovBottom = bt
			}
		}
		for _, fb := range b.inlineFrags() {
			if fb.R+0 > b.ovRight {
				b.ovRight = fb.R
			}
		}
	}
	c.placeSpecial(b, acc)
	// positioned descendants
	for _, k := range b.oofKids {
		if k.cbBox != b {
			continue
		}
		var px, py float64
		if b == c.rootBox && !b.isContainingBlockForAbs() && k.st.Position != "fixed" {
			px, py = 0, 0
		} else if k.st.Position == "fixed" && b == c.rootBox {
			px, py = 0, 0
		} else {
			px, py = b.ax, b.ay
		}
		c.place(k, px+k.x, py+k.y)
		if k.st.Position != "fixed" {
			acc(k)
		}
	}
	// scroll sizes
	b.scrollW = math.Max(b.ovRight, b.ax+b.w) - b.ax
	b.scrollH = math.Max(b.ovBottom, b.ay+b.h) - b.ay
	// the box goes back to where it is; what its content overflows by is measured from there
	b.ax, b.ay = b.ax+scx, b.ay+scy
	b.ovRight, b.ovBottom, b.ovLeft, b.ovTop = b.ovRight+scx, b.ovBottom+scy, b.ovLeft+scx, b.ovTop+scy
	// sticky positioning at scroll offset 0
	if b.st.Position == "sticky" {
		c.applySticky(b)
	}
	if b.clips() {
		// content overflow of a scroll container does not propagate
		myR, myB := b.ax+b.w, b.ay+b.h
		b.scrollR, b.scrollB = b.ovRight, b.ovBottom
		b.ovRight, b.ovBottom = myR, myB
		b.ovLeft, b.ovTop = b.ax, b.ay
	}
}

// scrollOf is the scroll offset of b when it is a scroll container with a scrolled position (the document's
// scroll offsets are kept by the embedder, see ScrollEnv).
func (c *ctx) scrollOf(b *lbox) (float64, float64) {
	if c.scrollEnv == nil || b.n == nil || b.pseudo != "" || b.rep != nil || b == c.rootBox || (b.kind != kBlock && b.kind != kAtomic) || !b.clips() {
		return 0, 0
	}
	x, y := c.scrollEnv.ScrollOffset(b.n)
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	return math.Round(x), math.Round(y)
}

func (b *lbox) hidden() bool { return false }

func (c *ctx) relativeOffset(b *lbox) (float64, float64) {
	st := b.st
	if st.Position != "relative" && st.Position != "sticky" {
		return 0, 0
	}
	if st.Position == "sticky" {
		return 0, 0
	}
	cbw, cbh := 0.0, -1.0
	if b.lp != nil {
		cbw = b.lp.w - b.lp.hExtra() - b.lp.sbW
		if hh := b.lp.contentH; b.lp.st.Height.K != style.LAuto {
			cbh = hh
		}
	}
	var dx, dy float64
	l, r, t, bt := st.Inset[3], st.Inset[1], st.Inset[0], st.Inset[2]
	switch {
	case l.K != style.LAuto:
		dx = rl(l, cbw)
	case r.K != style.LAuto:
		dx = -rl(r, cbw)
	}
	switch {
	case t.K != style.LAuto:
		if !(t.HasPct() && cbh < 0) {
			dy = rl(t, cbh)
		}
	case bt.K != style.LAuto:
		if !(bt.HasPct() && cbh < 0) {
			dy = -rl(bt, cbh)
		}
	}
	return dx, dy
}

func (c *ctx) applySticky(b *lbox) {
	st := b.st
	// scrollport: viewport (nearest scroll container approximated by the viewport)
	var sp Rect = Rect{0, 0, c.vw - c.sbViewport, c.vh - c.sbViewportH}
	for p := b.lp; p != nil; p = p.lp {
		if p.clips() && p != c.rootBox {
			sp = Rect{p.ax + p.bor[3], p.ay + p.bor[0], p.w - p.bor[1] - p.bor[3], p.h - p.bor[0] - p.bor[2]}
			break
		}
		if p.lp == nil {
			break
		}
	}
	var dx, dy float64
	t, bt := st.Inset[0], st.Inset[2]
	l, r := st.Inset[3], st.Inset[1]
	if t.K != style.LAuto {
		top := sp.Y + rl(t, sp.H)
		if b.ay < top {
			dy = top - b.ay
		}
	} else if bt.K != style.LAuto {
		bot := sp.Y + sp.H - rl(bt, sp.H)
		if b.ay+b.h > bot {
			dy = bot - (b.ay + b.h)
		}
	}
	if l.K != style.LAuto {
		left := sp.X + rl(l, sp.W)
		if b.ax < left {
			dx = left - b.ax
		}
	} else if r.K != style.LAuto {
		rt := sp.X + sp.W - rl(r, sp.W)
		if b.ax+b.w > rt {
			dx = rt - (b.ax + b.w)
		}
	}
	// limited by the containing block
	if b.lp != nil {
		cb := b.lp
		if dy > 0 {
			maxDy := cb.ay + cb.h - cb.bor[2] - cb.pad[2] - (b.ay + b.h) - b.mar[2]
			if dy > maxDy {
				dy = math.Max(0, maxDy)
			}
		}
		if dy < 0 {
			minDy := cb.ay + cb.bor[0] + cb.pad[0] - b.ay + b.mar[0]
			if dy < minDy {
				dy = math.Min(0, minDy)
			}
		}
	}
	if dx != 0 || dy != 0 {
		c.shiftTree(b, dx, dy)
	}
}

func (c *ctx) shiftTree(b *lbox, dx, dy float64) {
	b.ax += dx
	b.ay += dy
	b.relX += dx
	b.relY += dy
	b.ovRight += dx
	b.ovBottom += dy
	b.ovLeft += dx
	b.ovTop += dy
	for _, k := range b.kids {
		if k.kind == kBlock || k.kind == kAtomic {
			c.shiftTree(k, dx, dy)
		}
	}
	for _, k := range b.inlineKids {
		c.shiftTree(k, dx, dy)
	}
	for _, k := range b.oofKids {
		if k.cbBox == b {
			c.shiftTree(k, dx, dy)
		}
	}
	for _, fr := range b.fragsAbs {
		_ = fr
	}
	b.shiftFrags(dx, dy)
}

// ---- transforms ------------------------------------------------------------------------------

func (b *lbox) hasTransform() bool {
	st := b.st
	return len(st.Transform) > 0 || len(st.Translate) > 0 || len(st.Rotate) > 0 || len(st.Scale) > 0
}

// planeAffine6 is the z=0 plane of m as a CSS matrix(); a projective part is
// ignored (the 2D approximation used where only an affine map is needed).
func planeAffine6(m Mat4) [6]float64 {
	w := m[3][3]
	if w == 0 || m[3][0] != 0 || m[3][1] != 0 {
		w = 1
	}
	return [6]float64{m[0][0] / w, m[1][0] / w, m[0][1] / w, m[1][1] / w, m[0][3] / w, m[1][3] / w}
}

func (b *lbox) matrixFor(w, h float64) [6]float64 {
	st := b.st
	if b.use4() {
		_, _, rw, rh := b.refBox()
		return planeAffine6(b.xfList(rw, rh))
	}
	m := [6]float64{1, 0, 0, 1, 0, 0}
	mul := func(a, b [6]float64) [6]float64 {
		return [6]float64{a[0]*b[0] + a[2]*b[1], a[1]*b[0] + a[3]*b[1], a[0]*b[2] + a[2]*b[3], a[1]*b[2] + a[3]*b[3],
			a[0]*b[4] + a[2]*b[5] + a[4], a[1]*b[4] + a[3]*b[5] + a[5]}
	}
	apply := func(ops []style.TOp) {
		for _, op := range ops {
			var t [6]float64 = [6]float64{1, 0, 0, 1, 0, 0}
			lv := func(i int, base float64) float64 { return op.A[i].Resolve(base) }
			num := func(i int, d float64) float64 {
				if i < op.N {
					return op.A[i].V
				}
				return d
			}
			switch op.Fn {
			case "translate":
				t[4] = lv(0, w)
				if op.N > 1 {
					t[5] = lv(1, h)
				}
			case "translatex":
				t[4] = lv(0, w)
			case "translatey":
				t[5] = lv(0, h)
			case "translate3d":
				t[4] = lv(0, w)
				t[5] = lv(1, h)
			case "scale":
				sx := num(0, 1)
				sy := sx
				if op.N > 1 {
					sy = num(1, sx)
				}
				t[0], t[3] = sx, sy
			case "scalex":
				t[0] = num(0, 1)
			case "scaley":
				t[3] = num(0, 1)
			case "scale3d":
				t[0], t[3] = num(0, 1), num(1, 1)
			case "rotate", "rotatez":
				a := num(0, 0) * math.Pi / 180
				t[0], t[1], t[2], t[3] = math.Cos(a), math.Sin(a), -math.Sin(a), math.Cos(a)
			case "rotatex":
				a := num(0, 0) * math.Pi / 180
				t[3] = math.Cos(a)
			case "rotatey":
				a := num(0, 0) * math.Pi / 180
				t[0] = math.Cos(a)
			case "skew":
				ax := num(0, 0) * math.Pi / 180
				ay := num(1, 0) * math.Pi / 180
				t[2], t[1] = math.Tan(ax), math.Tan(ay)
			case "skewx":
				t[2] = math.Tan(num(0, 0) * math.Pi / 180)
			case "skewy":
				t[1] = math.Tan(num(0, 0) * math.Pi / 180)
			case "matrix":
				t = [6]float64{num(0, 1), num(1, 0), num(2, 0), num(3, 1), num(4, 0), num(5, 0)}
			default:
				continue
			}
			m = mul(m, t)
		}
	}
	// order: translate, rotate, scale, then transform
	apply(st.Translate)
	apply(st.Rotate)
	apply(st.Scale)
	apply(st.Transform)
	return m
}

// fullMatrix returns the matrix about the transform origin in absolute coordinates.
func (b *lbox) fullMatrix() [6]float64 {
	if b.use4() {
		return planeAffine6(b.own4(0, 0))
	}
	w, h := b.w, b.h
	m := b.matrixFor(w, h)
	ox := b.st.TransformOrigin[0].Resolve(w)
	oy := b.st.TransformOrigin[1].Resolve(h)
	px, py := b.ax+ox, b.ay+oy
	// translate(p) * m * translate(-p)
	e := m[4] + px - (m[0]*px + m[2]*py)
	f := m[5] + py - (m[1]*px + m[3]*py)
	return [6]float64{m[0], m[1], m[2], m[3], e, f}
}

func (b *lbox) transformedBounds(l, t, r, bt float64) (float64, float64, float64, float64) {
	if b.use4() {
		// projective: map the corners through the full matrix
		m4 := b.own4(0, 0)
		x0, y0, x1, y1 := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
		for _, c := range [][2]float64{{l, t}, {r, t}, {l, bt}, {r, bt}} {
			x, y := m4.project(c[0], c[1])
			x0, x1 = math.Min(x0, x), math.Max(x1, x)
			y0, y1 = math.Min(y0, y), math.Max(y1, y)
		}
		return x0, y0, x1, y1
	}
	m := b.fullMatrix()
	xs := []float64{l, r, l, r}
	ys := []float64{t, t, bt, bt}
	x0, y0, x1, y1 := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for i := range xs {
		x := m[0]*xs[i] + m[2]*ys[i] + m[4]
		y := m[1]*xs[i] + m[3]*ys[i] + m[5]
		x0, x1 = math.Min(x0, x), math.Max(x1, x)
		y0, y1 = math.Min(y0, y), math.Max(y1, y)
	}
	return x0, y0, x1, y1
}

// ---- Box output ----------------------------------------------------------------------------------

func (c *ctx) buildBoxes() {
	res := c.res
	// every element that has a style
	for n, st := range c.styles {
		bs := c.byNode[n]
		if st.Display == "none" {
			res.Boxes[n] = &Box{Display: "none", Position: st.Position, Visible: false, Transform: [6]float64{1, 0, 0, 1, 0, 0}, Style: st}
			continue
		}
		if len(bs) == 0 {
			res.Boxes[n] = &Box{Display: st.Display, Position: st.Position, Visible: st.Visibility == "visible", Transform: [6]float64{1, 0, 0, 1, 0, 0}, Style: st}
			continue
		}
		bx := &Box{Display: st.Display, Position: st.Position, Visible: st.Visibility == "visible", Transform: [6]float64{1, 0, 0, 1, 0, 0}, Style: st}
		bx.ZIndex = 0
		if !st.ZAuto {
			bx.ZIndex = st.ZIndex
		}
		var border Rect
		first := true
		for _, b := range bs {
			var r Rect
			if b.kind == kInline || b.kind == kBreak {
				for _, f := range b.absFrags {
					bx.Frags = append(bx.Frags, f)
					r = r.union(f)
				}
				for _, cbk := range b.contBlocks {
					r = r.union(Rect{cbk.ax, cbk.ay, cbk.w, cbk.h})
				}
				if len(b.absFrags) == 0 && len(b.contBlocks) == 0 {
					continue
				}
			} else {
				r = Rect{b.ax, b.ay, b.w, b.h}
				if b.kind == kAtomic || b.kind == kBlock {
					bx.Margin = b.mar
				}
				if b.kind == kBlock && len(b.mcFrags) > 1 {
					// a block split over columns reports one rectangle per column
					bx.Frags = append(bx.Frags, b.colFrags()...)
				}
			}
			if first {
				border = r
				first = false
			} else {
				border = border.union(r)
			}
		}
		main := bs[0]
		for _, b := range bs {
			if b.kind != kInline {
				main = b
				break
			}
		}
		bx.Border = border
		if main.kind != kInline {
			bx.Padding = Rect{border.X + main.bor[3], border.Y + main.bor[0], border.W - main.bor[1] - main.bor[3], border.H - main.bor[0] - main.bor[2]}
			bx.Content = Rect{bx.Padding.X + main.pad[3], bx.Padding.Y + main.pad[0], bx.Padding.W - main.pad[1] - main.pad[3] - main.sbW, bx.Padding.H - main.pad[0] - main.pad[2] - main.sbH}
			bx.ClientW, bx.ClientH = bx.Padding.W-main.sbW, bx.Padding.H-main.sbH
			bx.ScrollW, bx.ScrollH = math.Max(main.scrollW, bx.ClientW), math.Max(main.scrollH, bx.ClientH)
			bx.ScrollLeft, bx.ScrollTop = main.scx, main.scy
			if main.clips() {
				sw := main.scrollR - main.ax - main.bor[3]
				sh := main.scrollB - main.ay - main.bor[0]
				bx.ScrollW, bx.ScrollH = math.Max(sw, bx.ClientW), math.Max(sh, bx.ClientH)
			}
			if main.kind == kBlock || main.kind == kAtomic {
				d := st.Display
				if main.kind == kBlock {
					d = style.Blockify(d)
				}
				bx.Display = d
			}
			if main.hasTransform() {
				bx.HasTransform = true
				bx.Transform = main.fullMatrix()
			}
			bx.Xf3 = main.xf3dOf()
			if main == c.bodyBox || main == c.rootBox {
				// html/body client sizes refer to the viewport for the root
			}
			bx.OffsetParent = c.offsetParent(main)
		} else {
			bx.Padding, bx.Content = border, border
			bx.ClientW, bx.ClientH = border.W, border.H
			bx.ScrollW, bx.ScrollH = border.W, border.H
		}
		res.Boxes[n] = bx
	}
	for _, bs := range c.byNode {
		for _, b := range bs {
			if b.rep != nil && b.rep.kind == "svg" && b.n.Namespace != "" || b.rep != nil && b.rep.kind == "svg" {
				c.svgGeometry(b)
			}
			if b.rep == nil || b.rep.ctrl != "select" {
				continue
			}
			ci := c.controlInfo(b)
			if !ci.list {
				continue
			}
			cr := Rect{b.ax + b.bor[3], b.ay + b.bor[0], b.w - b.bor[1] - b.bor[3], b.h - b.bor[0] - b.bor[2]}
			for i, o := range ci.options {
				res.Boxes[o] = &Box{Border: Rect{cr.X, cr.Y + float64(i)*ci.optH, cr.W - 15, ci.optH}, Display: "block", Visible: true, Transform: [6]float64{1, 0, 0, 1, 0, 0}}
				res.Boxes[o].Padding, res.Boxes[o].Content = res.Boxes[o].Border, res.Boxes[o].Border
			}
		}
	}
	c.zeroNonRenderedSVG()
	if rb := res.Boxes[c.docEl]; rb != nil {
		rb.ClientW, rb.ClientH = c.vw-c.sbViewport, c.vh-c.sbViewportH
		rb.ScrollW, rb.ScrollH = res.Width, res.Height
	}
}

func (c *ctx) offsetParent(b *lbox) *html.Node {
	for p := b.parent; p != nil; p = p.parent {
		if p.n == nil {
			continue
		}
		if p.st.Position != "static" || p.n.Data == "body" || p.n.Data == "td" || p.n.Data == "th" || p.n.Data == "table" {
			return p.n
		}
	}
	return nil
}

func sortedNodes(m map[*html.Node]*Box) []*html.Node {
	var out []*html.Node
	for n := range m {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return false })
	return out
}

// IsQuirks reports whether layout treats the document as quirks mode (document.compatMode).
func IsQuirks(root *html.Node) bool { return isQuirks(root) }

// isQuirks reports whether the document lacks a standards-mode doctype.
func isQuirks(root *html.Node) bool {
	if root == nil || root.Type != html.DocumentNode {
		return false
	}
	for ch := root.FirstChild; ch != nil; ch = ch.NextSibling {
		if ch.Type == html.DoctypeNode {
			if strings.EqualFold(ch.Data, "html") {
				for _, a := range ch.Attr {
					if a.Key == "public" && a.Val != "" {
						l := strings.ToLower(a.Val)
						if strings.Contains(l, "html 4.01 transitional") || strings.Contains(l, "html 4.01 frameset") || strings.Contains(l, "html 3") || strings.Contains(l, "html 2") {
							for _, b := range ch.Attr {
								if b.Key == "system" && b.Val != "" {
									return false
								}
							}
							return true
						}
					}
				}
				return false
			}
			return true
		}
		if ch.Type == html.ElementNode {
			break
		}
	}
	return true
}
