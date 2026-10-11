package render

import (
	"image"
	"math"
	"sort"
	"strings"

	"golang.org/x/net/html"

	"github.com/sweefbrowser/sweef/internal/style"
)

type paintInfo struct{}

type clipRec struct {
	r     Rect
	radii Radii
	shape *ClipSub // clip-path shape (hit testing); r is its bounding box
}

type hitRec struct {
	n     *html.Node
	rects []Rect
	clips []clipRec
	m     [6]float64
	hasM  bool
	noPE  bool
	ext   []func(x, y float64) bool
}

type painter struct {
	c     *ctx
	ops   []Op
	clips []clipRec
	m     [6]float64
	hasM  bool
	// latX, latY is the origin of the pixel lattice rectangles snap to (the
	// box origin of the nearest lattice-opening ancestor), see paint_snap.go
	latX, latY float64
	// hitExt are extra hit tests (user-space points) attached to hits recorded
	// while painting a foreignObject
	hitExt  []func(x, y float64) bool
	hits    []hitRec
	outl    []*lbox
	active  map[*lbox]bool
	svgdocs map[string]*SVGDoc
	// fragShift is non-zero while painting a continuing fragment of a sliced box
	fragShift float64
}

var identity = [6]float64{1, 0, 0, 1, 0, 0}

func (p *painter) emit(o Op) { p.ops = append(p.ops, o) }

func (c *ctx) paintAll(rb *lbox) {
	p := &painter{c: c, m: identity, active: map[*lbox]bool{}}
	c.canvasBackground(p, rb)
	p.paintSC(rb, true)
	p.paintViewportScrollbars()
	c.res.List = p.ops
	c.painter = p
}

func (b *lbox) visible() bool { return b.st.Visibility == "visible" }

// createsSC reports whether b forms a stacking context (or is painted atomically as one).
func (b *lbox) createsSC() bool {
	st := b.st
	if b.parent == nil && b.n != nil {
		return true
	}
	switch st.Position {
	case "fixed", "sticky":
		return true
	case "absolute", "relative":
		return true
	}
	if st.OpacitySet && st.Opacity < 1 || b.hasTransform() || len(st.Filter) > 0 || len(st.BackdropFilter) > 0 || st.MixBlend != "normal" && st.MixBlend != "" ||
		st.Isolation == "isolate" || st.ClipPath || st.MaskImage || st.Perspective != 0 {
		return true
	}
	if st.WillChange != "" && (contains(st.WillChange, "transform") || contains(st.WillChange, "opacity") || contains(st.WillChange, "filter")) {
		return true
	}
	if st.Contain != "" && (contains(st.Contain, "layout") || contains(st.Contain, "paint") || contains(st.Contain, "strict") || contains(st.Contain, "content")) {
		return true
	}
	if !st.ZAuto && b.parent != nil && (b.parent.inner == iFlex || b.parent.inner == iGrid) {
		return true
	}
	return false
}

func (b *lbox) zOrder() int {
	if b.st.ZAuto {
		return 0
	}
	return b.st.ZIndex
}

func rectOf(b *lbox) Rect { return Rect{b.ax, b.ay, b.w, b.h} }

func (p *painter) pushClip(r Rect, radii Radii, owner *lbox) {
	p.emit(PushClip{Rect: r, Radii: radii})
	p.clips = append(append([]clipRec(nil), p.clips...), clipRec{r: r, radii: radii})
	if owner != nil {
		p.active[owner] = true
	}
}

func (p *painter) popClip(owner *lbox) {
	p.emit(PopClip{})
	p.clips = p.clips[:len(p.clips)-1]
	if owner != nil {
		delete(p.active, owner)
	}
}

// overflowClip returns the clip rectangle for a box with overflow != visible.
func (p *painter) overflowClip(b *lbox) (Rect, Radii) {
	r := Rect{b.ax + b.bor[3], b.ay + b.bor[0], b.w - b.bor[1] - b.bor[3], b.h - b.bor[0] - b.bor[2]}
	rad := p.radii(b)
	// inner radii
	var in Radii
	for i := 0; i < 4; i++ {
		bw := [4][2]float64{{b.bor[3], b.bor[0]}, {b.bor[1], b.bor[0]}, {b.bor[1], b.bor[2]}, {b.bor[3], b.bor[2]}}[i]
		in[i][0] = math.Max(0, rad[i][0]-bw[0])
		in[i][1] = math.Max(0, rad[i][1]-bw[1])
	}
	if b.st.OverflowClipMargin != 0 {
		_ = 0
	}
	return r, in
}

func (p *painter) radii(b *lbox) Radii { return p.radiiWH(b, b.w, b.h) }

// radiiWH resolves the border radii for a box of the given border-box size.
func (p *painter) radiiWH(b *lbox, bw, bh float64) Radii {
	st := b.st
	var r Radii
	any := false
	for i := 0; i < 4; i++ {
		h := rl(st.Radius[i][0], bw)
		v := rl(st.Radius[i][1], bh)
		if h > 0 && v > 0 {
			any = true
			r[i] = [2]float64{h, v}
		}
	}
	if !any {
		return Radii{}
	}
	f := 1.0
	chk := func(sum, lim float64) {
		if sum > 0 && lim/sum < f {
			f = lim / sum
		}
	}
	chk(r[0][0]+r[1][0], bw)
	chk(r[3][0]+r[2][0], bw)
	chk(r[0][1]+r[3][1], bh)
	chk(r[1][1]+r[2][1], bh)
	if f < 1 {
		for i := range r {
			r[i][0] *= f
			r[i][1] *= f
		}
	}
	return r
}

func (p *painter) addHit(b *lbox, rects ...Rect) {
	if b.n == nil {
		return
	}
	p.hits = append(p.hits, hitRec{n: b.n, rects: rects, clips: p.clips, m: p.m, hasM: p.hasM, noPE: b.st.PointerEvents == "none", ext: p.hitExt})
}

// paintSC paints a stacking context rooted at b.
func (p *painter) paintSC(b *lbox, isRoot bool) {
	st := b.st
	// clips from the containing-block chain
	var chain []*lbox
	if !isRoot {
		chain = p.clipChain(b)
	}
	for _, cb := range chain {
		r, rad := p.overflowClip(cb)
		p.pushClip(r, rad, nil)
	}
	endFx := p.beginEffects(b)
	var deferred []*lbox
	if !isRoot || true {
		p.paintDecor(b, isRoot)
	}
	clipped := false
	if b.clips() && !isRoot {
		r, rad := p.overflowClip(b)
		p.pushClip(r, rad, b)
		clipped = true
	}
	// negative z-index children are painted before in-flow content: collect first
	p.collectDeferred(b, &deferred)
	sort.SliceStable(deferred, func(i, j int) bool { return deferred[i].zOrder() < deferred[j].zOrder() })
	idx := 0
	for idx < len(deferred) && deferred[idx].zOrder() < 0 {
		p.paintSC(deferred[idx], false)
		idx++
	}
	p.phaseBlocks(b)
	p.phaseFloats(b)
	p.phaseInline(b)
	for ; idx < len(deferred); idx++ {
		p.paintSC(deferred[idx], false)
	}
	if clipped {
		p.paintScrollbars(b)
		p.popClip(b)
	}
	endFx()
	for range chain {
		p.popClip(nil)
	}
	_ = st
}

// beginEffects opens the transform and layer (opacity, filter, blend) of b and
// returns the function that closes them.
func (p *painter) beginEffects(b *lbox) func() {
	st := b.st
	layer := st.OpacitySet && st.Opacity < 1 || len(st.Filter) > 0 || st.MixBlend != "normal" && st.MixBlend != ""
	drop := b.dropsFraction()
	tx := b.hasTransform() || drop
	saveM, saveHas := p.m, p.hasM
	saveLX, saveLY := p.latX, p.latY
	if tx {
		pm, lx, ly := p.frameMatrix(b, drop)
		p.emit(PushTransform{M: pm, Lattice: true, LatX: lx, LatY: ly})
		p.latX, p.latY = lx, ly
		if b.hasTransform() {
			m := b.fullMatrix()
			p.m = mulM(p.m, m)
			p.hasM = true
		}
	}
	if len(st.BackdropFilter) > 0 && st.Visibility != "hidden" {
		bf := BackdropFilter{Rect: rectOf(b), Radii: p.radii(b)}
		for _, f := range st.BackdropFilter {
			bf.Filter = append(bf.Filter, Filter{Name: f.Name, Args: f.Args, Color: f.Color})
		}
		p.emit(bf)
	}
	// clip-path and mask apply after the filter and before opacity/blending:
	// [opacity layer [clip/mask [filter layer ...]]]
	cm := st.ClipPath || st.MaskImage
	outer, inner := layer, false
	if cm {
		outer = st.OpacitySet && st.Opacity < 1 || st.MixBlend != "normal" && st.MixBlend != ""
		inner = len(st.Filter) > 0
	}
	layerBounds := Rect{b.ovLeft, b.ovTop, b.ovRight - b.ovLeft, b.ovBottom - b.ovTop}
	filters := func() (out []Filter) {
		for _, f := range st.Filter {
			out = append(out, Filter{Name: f.Name, Args: f.Args, Color: f.Color})
		}
		return
	}
	if outer {
		pl := PushLayer{Opacity: 1, Blend: st.MixBlend, Bounds: layerBounds}
		if st.OpacitySet {
			pl.Opacity = st.Opacity
		}
		if !cm {
			pl.Filter = filters()
		}
		p.emit(pl)
	}
	var endCM func()
	if cm {
		endCM = p.clipMaskBegin(b)
		if inner {
			p.emit(PushLayer{Opacity: 1, Filter: filters(), Bounds: layerBounds})
		}
	}
	return func() {
		if inner {
			p.emit(PopLayer{})
		}
		if endCM != nil {
			endCM()
		}
		if outer {
			p.emit(PopLayer{})
		}
		if tx {
			p.emit(PopTransform{})
		}
		p.m, p.hasM = saveM, saveHas
		p.latX, p.latY = saveLX, saveLY
	}
}

func mulM(a, b [6]float64) [6]float64 {
	return [6]float64{a[0]*b[0] + a[2]*b[1], a[1]*b[0] + a[3]*b[1], a[0]*b[2] + a[2]*b[3], a[1]*b[2] + a[3]*b[3],
		a[0]*b[4] + a[2]*b[5] + a[4], a[1]*b[4] + a[3]*b[5] + a[5]}
}

// clipChain lists the overflow-clipping ancestors (outermost first) that clip b but
// are not painted as part of an enclosing walk.
func (p *painter) clipChain(b *lbox) []*lbox {
	var chain []*lbox
	cur := b
	for steps := 0; steps < 1000; steps++ {
		var next *lbox
		if cur.oof {
			next = cur.cbBox
		} else {
			next = cur.lp
			if next == nil {
				next = cur.parent
			}
		}
		if next == nil || next == p.c.rootBox {
			break
		}
		if next.clips() && !p.active[next] {
			chain = append(chain, next)
		}
		cur = next
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain
}

// children in paint (tree) order, including inline-level descendants of IFCs.
func (p *painter) flowKids(b *lbox) []*lbox {
	if b.cvSkip {
		return nil // content-visibility: skipped contents do not paint or hit-test
	}
	if !b.ifc {
		hasHidden := false
		for _, k := range b.kids {
			if k.hiddenFlow {
				hasHidden = true
			}
		}
		if !hasHidden {
			return b.kids
		}
		var out []*lbox
		for _, k := range b.kids {
			if !k.hiddenFlow {
				out = append(out, k)
			}
		}
		return out
	}
	var out []*lbox
	var rec func(x *lbox)
	rec = func(x *lbox) {
		for _, k := range x.kids {
			switch k.kind {
			case kInline:
				rec(k)
			case kBlock, kAtomic:
				out = append(out, k)
			}
		}
	}
	rec(b)
	return out
}

func (p *painter) isDeferred(k *lbox) bool {
	if k.kind == kText || k.kind == kMarker || k.kind == kBreak || k.kind == kInline {
		return false
	}
	return k.createsSC() && k.st.Position != "static" || k.createsSC() && !k.floated && !k.inlineLv && k.kind != kAtomic && k.st.Position == "static" && (k.hasTransform() || !k.st.ZAuto || k.st.OpacitySet && k.st.Opacity < 1 || len(k.st.Filter) > 0 || k.st.MixBlend != "normal" && k.st.MixBlend != "" || k.st.ClipPath || k.st.MaskImage) ||
		k.createsSC() && k.floated && k.st.Position == "static" && false
}

func (p *painter) collectDeferred(b *lbox, out *[]*lbox) {
	for _, k := range p.flowKids(b) {
		if k == b.legend && false {
			continue
		}
		if p.isDeferred(k) {
			*out = append(*out, k)
			continue
		}
		// an atomic box that forms a stacking context paints its own positioned
		// descendants (paintAtomic); a plain inline-block leaves them to the
		// enclosing stacking context
		if k.kind == kBlock || k.kind == kAtomic && !k.createsSC() {
			p.collectDeferred(k, out)
		}
	}
	// positioned descendants inside inline elements
	if b.ifc {
		// handled by flowKids recursion
	}
}

func (p *painter) phaseBlocks(b *lbox) {
	for _, k := range p.flowKids(b) {
		if p.isDeferred(k) || k.floated || k.kind == kAtomic || k.oof {
			continue
		}
		if k.kind != kBlock {
			continue
		}
		p.paintDecor(k, false)
		clipped := false
		if k.clips() {
			r, rad := p.overflowClip(k)
			p.pushClip(r, rad, k)
			clipped = true
		}
		p.phaseBlocks(k)
		if clipped {
			p.popClip(k)
		}
	}
}

func (p *painter) phaseFloats(b *lbox) {
	for _, k := range p.flowKids(b) {
		if p.isDeferred(k) || k.oof {
			continue
		}
		if k.floated {
			p.paintAtomic(k)
			continue
		}
		if k.kind != kBlock {
			continue
		}
		clipped := false
		if k.clips() {
			r, rad := p.overflowClip(k)
			p.pushClip(r, rad, k)
			clipped = true
		}
		p.phaseFloats(k)
		if clipped {
			p.popClip(k)
		}
	}
}

func (p *painter) phaseInline(b *lbox) {
	if b.ifc && !b.cvSkip {
		p.paintLines(b)
	}
	for _, k := range p.flowKids(b) {
		if p.isDeferred(k) || k.floated || k.oof || k.kind != kBlock {
			continue
		}
		clipped := false
		if k.clips() {
			r, rad := p.overflowClip(k)
			p.pushClip(r, rad, k)
			clipped = true
		}
		p.phaseInline(k)
		if clipped {
			p.paintScrollbars(k)
			p.popClip(k)
		}
	}
}

// paintAtomic paints a box and its contents as one unit (floats, inline-blocks).
func (p *painter) paintAtomic(a *lbox) {
	if p.isDeferred(a) {
		return
	}
	endFx := p.beginEffects(a)
	p.paintDecor(a, false)
	clipped := false
	if a.clips() {
		r, rad := p.overflowClip(a)
		p.pushClip(r, rad, a)
		clipped = true
	}
	var deferred []*lbox
	if a.createsSC() {
		p.collectDeferred(a, &deferred)
	}
	p.phaseBlocks(a)
	p.phaseFloats(a)
	p.phaseInline(a)
	sort.SliceStable(deferred, func(i, j int) bool { return deferred[i].zOrder() < deferred[j].zOrder() })
	for _, d := range deferred {
		p.paintSC(d, false)
	}
	if clipped {
		p.paintScrollbars(a)
		p.popClip(a)
	}
	endFx()
}

// ---- box decoration -----------------------------------------------------------------------------

func (p *painter) paintDecor(b *lbox, isRoot bool) {
	st := b.st
	if b.n == nil && b.anon {
		return
	}
	r := rectOf(b)
	if r.W <= 0 && r.H <= 0 {
		p.paintSpecial(b)
		return
	}
	vis := st.Visibility == "visible" || st.Visibility == ""
	if len(b.mcFrags) > 1 && b.kind == kBlock && !isRoot && b.inner != iTable {
		p.paintFragDecor(b, vis)
		return
	}
	if vis {
		radii := p.radii(b)
		// outer shadows
		for _, sh := range st.BoxShadow {
			if sh.Inset {
				continue
			}
			p.emit(BoxShadow{Rect: r, Radii: radii, DX: sh.DX, DY: sh.DY, Blur: sh.Blur, Spread: sh.Spread, Color: sh.Color})
		}
		skipBG := false
		if isRoot || b == p.c.bodyBox && p.c.bodyPropagates() {
			skipBG = true
		}
		themed, btn := p.themedControl(b)
		if themed {
			p.paintThemedBox(b, btn)
			skipBG = true
		}
		if !skipBG {
			p.paintBackground(b, r, radii)
		}
		for _, sh := range st.BoxShadow {
			if !sh.Inset {
				continue
			}
			p.emit(BoxShadow{Rect: r, Radii: radii, DX: sh.DX, DY: sh.DY, Blur: sh.Blur, Spread: sh.Spread, Color: sh.Color, Inset: true})
		}
		if !themed && st.BorderStyle != [4]string{} && (b.bor[0] > 0 || b.bor[1] > 0 || b.bor[2] > 0 || b.bor[3] > 0) && !b.hasBorOv {
			p.emit(Border{Rect: r, Widths: b.bor, Colors: st.BorderColor, Styles: st.BorderStyle, Radii: radii})
		}
		if b.inner == iTable && b.cl != nil {
			p.paintCollapsed(b)
		}
		p.paintColumnRules(b)
		if st.OutlineWidth > 0 && st.OutlineStyle != "none" {
			p.outl = append(p.outl, b)
			p.paintOutline(b)
		}
	}
	p.addHit(b, r)
	if vis {
		p.paintSpecial(b)
	}
}

func (c *ctx) bodyPropagates() bool {
	// the body background goes to the canvas when the root element has none
	return c.bodyBox != nil && !hasBackground(c.rootBox.st)
}

func hasBackground(st *style.Computed) bool {
	if st.BgColor.A != 0 {
		return true
	}
	for _, l := range st.BgLayers {
		if !l.Image.IsNone() {
			return true
		}
	}
	return false
}

func (c *ctx) canvasBackground(p *painter, rb *lbox) {
	src := rb
	if !hasBackground(rb.st) && c.bodyBox != nil {
		src = c.bodyBox
	}
	if !hasBackground(src.st) {
		return
	}
	full := Rect{0, 0, math.Max(c.res.Width, c.vw), math.Max(c.res.Height, c.vh)}
	// the positioning area is the root element's box
	p.paintBackgroundArea(src, full, Radii{}, rectOf(rb), true)
}

func (p *painter) paintBackground(b *lbox, r Rect, radii Radii) {
	p.paintBackgroundArea(b, r, radii, r, false)
}

func (p *painter) paintBackgroundArea(b *lbox, border Rect, radii Radii, originBox Rect, canvas bool) {
	st := b.st
	// background color (painted in the clip area of the last layer)
	lastClip := "border-box"
	if n := len(st.BgLayers); n > 0 {
		lastClip = st.BgLayers[n-1].Clip
	}
	boxFor := func(kind string) (Rect, Radii) {
		bb := border
		if canvas {
			return bb, radii
		}
		rad := radii
		switch kind {
		case "padding-box":
			bb = Rect{border.X + b.bor[3], border.Y + b.bor[0], border.W - b.bor[1] - b.bor[3], border.H - b.bor[0] - b.bor[2]}
			rad = shrinkRadii(radii, b.bor)
		case "content-box":
			bb = Rect{border.X + b.bor[3] + b.pad[3], border.Y + b.bor[0] + b.pad[0], border.W - b.hExtra(), border.H - b.vExtra()}
			rad = shrinkRadii(radii, [4]float64{b.bor[0] + b.pad[0], b.bor[1] + b.pad[1], b.bor[2] + b.pad[2], b.bor[3] + b.pad[3]})
		}
		return bb, rad
	}
	blended := false
	for _, ly := range st.BgLayers {
		if ly.Blend != "" && ly.Blend != "normal" && !ly.Image.IsNone() {
			blended = true
		}
	}
	if st.BgColor.A != 0 && lastClip != "text" && !blended {
		cr, crad := boxFor(lastClip)
		p.emit(FillRect{Rect: cr, Color: st.BgColor, Radii: crad})
	}
	// background-blend-mode: blended layers composite against the element's own
	// background only, so the whole background is an isolated group
	if blended {
		p.emit(PushLayer{Opacity: 1, Bounds: border})
		if st.BgColor.A != 0 && lastClip != "text" {
			cr, crad := boxFor(lastClip)
			p.emit(FillRect{Rect: cr, Color: st.BgColor, Radii: crad})
		}
	}
	for i := len(st.BgLayers) - 1; i >= 0; i-- {
		ly := st.BgLayers[i]
		if ly.Image.IsNone() || ly.Clip == "text" {
			continue
		}
		clipBox, clipRad := boxFor(ly.Clip)
		// positioning area
		var pa Rect
		if canvas {
			pa = originBox
		} else {
			pa, _ = boxFor(ly.Origin)
			if p.fragShift != 0 && ly.Origin != "border-box" {
				// continuing fragment of a sliced box: Blink insets the positioning area by
				// the fragment's own (absent) top border and padding
				sh := b.bor[0]
				if ly.Origin == "content-box" {
					sh += b.pad[0]
				}
				pa.Y -= sh
			}
		}
		bl := blended && ly.Blend != "" && ly.Blend != "normal"
		if bl {
			p.emit(PushLayer{Opacity: 1, Blend: ly.Blend, Bounds: border})
		}
		p.paintBgLayer(b, ly, clipBox, clipRad, pa)
		if bl {
			p.emit(PopLayer{})
		}
	}
	if blended {
		p.emit(PopLayer{})
	}
}

func shrinkRadii(r Radii, w [4]float64) Radii {
	var o Radii
	for i := 0; i < 4; i++ {
		ws := [4][2]float64{{w[3], w[0]}, {w[1], w[0]}, {w[1], w[2]}, {w[3], w[2]}}[i]
		o[i][0] = math.Max(0, r[i][0]-ws[0])
		o[i][1] = math.Max(0, r[i][1]-ws[1])
		if o[i][0] == 0 || o[i][1] == 0 {
			o[i] = [2]float64{}
		}
	}
	return o
}

func (p *painter) paintBgLayer(b *lbox, ly style.BgLayer, clip Rect, clipRad Radii, pa Rect) {
	if pa.W <= 0 || pa.H <= 0 || clip.W <= 0 || clip.H <= 0 {
		return
	}
	var iw, ih float64
	var img image.Image
	var svgDoc *SVGDoc
	var grad *style.Gradient
	hasIntrW, hasIntrH := false, false
	var ratio float64
	if ly.Image.Grad != nil {
		grad = ly.Image.Grad
	} else {
		img = p.c.env.Image(ly.Image.URL)
		if img == nil {
			sd := p.c.svgImageDocFor(ly.Image.URL)
			if sd == nil {
				return
			}
			svgDoc = sd
			w, h, hw, hh, rt := svgIntrinsic(sd)
			iw, ih, hasIntrW, hasIntrH, ratio = w, h, hw, hh, rt
			if !hw && !hh {
				// no intrinsic size: ratio-only images fit the positioning area
				switch {
				case rt > 0:
					iw, ih = pa.W, pa.W/rt
					if ih > pa.H {
						iw, ih = pa.H*rt, pa.H
					}
				default:
					iw, ih = pa.W, pa.H
				}
				hasIntrW, hasIntrH = true, true
			}
		} else {
			bnd := img.Bounds()
			iw, ih = float64(bnd.Dx()), float64(bnd.Dy())
			hasIntrW, hasIntrH = true, true
			if ih > 0 {
				ratio = iw / ih
			}
		}
	}
	// image size
	var tw, th float64
	switch ly.SizeKind {
	case "cover", "contain":
		if grad != nil || ratio == 0 {
			tw, th = pa.W, pa.H
		} else {
			sx, sy := pa.W/iw, pa.H/ih
			s := math.Max(sx, sy)
			if ly.SizeKind == "contain" {
				s = math.Min(sx, sy)
			}
			tw, th = iw*s, ih*s
		}
	default:
		wAuto, hAuto := ly.SizeW.K == style.LAuto, ly.SizeH.K == style.LAuto
		if !wAuto {
			tw = rl(ly.SizeW, pa.W)
		}
		if !hAuto {
			th = rl(ly.SizeH, pa.H)
		}
		switch {
		case wAuto && hAuto:
			if grad != nil {
				tw, th = pa.W, pa.H
			} else {
				tw, th = iw, ih
			}
		case wAuto:
			if ratio > 0 {
				tw = th * ratio
			} else if hasIntrW {
				tw = iw
			} else {
				tw = pa.W
			}
		case hAuto:
			if ratio > 0 {
				th = tw / ratio
			} else if hasIntrH {
				th = ih
			} else {
				th = pa.H
			}
		}
	}
	if tw <= 0 || th <= 0 {
		return
	}
	// repeat: round / space adjust
	rx, ry := ly.RepeatX == "repeat" || ly.RepeatX == "round" || ly.RepeatX == "space", ly.RepeatY == "repeat" || ly.RepeatY == "round" || ly.RepeatY == "space"
	stepX, stepY := tw, th
	if ly.RepeatX == "round" && pa.W > 0 {
		n := math.Max(1, math.Round(pa.W/tw))
		tw = pa.W / n
		stepX = tw
	}
	if ly.RepeatY == "round" && pa.H > 0 {
		n := math.Max(1, math.Round(pa.H/th))
		th = pa.H / n
		stepY = th
	}
	// position
	px := ly.PosX.Resolve(pa.W - tw)
	py := ly.PosY.Resolve(pa.H - th)
	if ly.RepeatX == "space" && tw <= pa.W {
		n := math.Floor(pa.W / tw)
		if n >= 2 {
			stepX = tw + (pa.W-n*tw)/(n-1)
			px = 0
		} else {
			rx = false
		}
	}
	if ly.RepeatY == "space" && th <= pa.H {
		n := math.Floor(pa.H / th)
		if n >= 2 {
			stepY = th + (pa.H-n*th)/(n-1)
			py = 0
		} else {
			ry = false
		}
	}
	if ly.Attach == "fixed" {
		// positioned relative to the viewport
		pa = Rect{0, 0, p.c.vw - p.c.sbViewport, p.c.vh}
		px = ly.PosX.Resolve(pa.W - tw)
		py = ly.PosY.Resolve(pa.H - th)
	}
	tile := &Tile{Origin: [2]float64{pa.X + px, pa.Y + py}, Size: [2]float64{tw, th}, Step: [2]float64{stepX, stepY}, RepeatX: rx, RepeatY: ry}
	if grad != nil {
		p.emit(p.gradientOp(b, grad, clip, clipRad, tile))
		return
	}
	if svgDoc != nil {
		p.emitSVGTiles(b, svgDoc, clip, clipRad, tile)
		return
	}
	p.emit(Image{Img: img, Dst: clip, Radii: clipRad, Smooth: b.st.ImageRendering != "pixelated" && b.st.ImageRendering != "crisp-edges", Tile: tile})
}

func (p *painter) gradientOp(b *lbox, g *style.Gradient, rect Rect, rad Radii, tile *Tile) Gradient {
	w, h := tile.Size[0], tile.Size[1]
	out := Gradient{Rect: rect, Kind: g.Kind, Radii: rad, Repeating: g.Repeating, Tile: tile}
	var lineLen float64
	switch g.Kind {
	case "linear":
		ang := g.Angle
		if g.ToCorner != "" {
			f := strings.Fields(g.ToCorner)
			sy, sx := 1.0, 1.0
			if f[0] == "top" {
				sy = -1
			}
			if f[1] == "left" {
				sx = -1
			}
			ang = math.Atan2(sx*h, -sy*w) * 180 / math.Pi
			if ang < 0 {
				ang += 360
			}
		}
		out.Angle = ang
		a := ang * math.Pi / 180
		lineLen = math.Abs(w*math.Sin(a)) + math.Abs(h*math.Cos(a))
	case "radial":
		cx, cy := w/2, h/2
		if g.HasPos {
			cx, cy = g.Pos[0].Resolve(w), g.Pos[1].Resolve(h)
		}
		var rx, ry float64
		if g.HasSize {
			rx, ry = g.RadiusX.Resolve(w), g.RadiusY.Resolve(h)
			if g.Shape == "circle" {
				ry = rx
			}
		} else {
			dl, dr, dt, db := cx, w-cx, cy, h-cy
			switch g.Extent {
			case "closest-side":
				if g.Shape == "circle" {
					rx = math.Min(math.Min(dl, dr), math.Min(dt, db))
					ry = rx
				} else {
					rx, ry = math.Min(dl, dr), math.Min(dt, db)
				}
			case "farthest-side":
				if g.Shape == "circle" {
					rx = math.Max(math.Max(dl, dr), math.Max(dt, db))
					ry = rx
				} else {
					rx, ry = math.Max(dl, dr), math.Max(dt, db)
				}
			case "closest-corner", "farthest-corner", "":
				cdx, cdy := math.Min(dl, dr), math.Min(dt, db)
				if g.Extent != "closest-corner" {
					cdx, cdy = math.Max(dl, dr), math.Max(dt, db)
				}
				if g.Shape == "circle" {
					rx = math.Hypot(cdx, cdy)
					ry = rx
				} else {
					// ellipse with the aspect ratio of the corresponding side distances
					sx, sy := cdx, cdy
					if g.Extent == "closest-corner" {
						sx, sy = math.Min(dl, dr), math.Min(dt, db)
					} else {
						sx, sy = math.Max(dl, dr), math.Max(dt, db)
					}
					if sx == 0 || sy == 0 {
						rx, ry = math.Hypot(cdx, cdy), math.Hypot(cdx, cdy)
					} else {
						k := math.Sqrt2
						rx, ry = sx*k, sy*k
					}
				}
			}
		}
		out.CX, out.CY, out.RX, out.RY = cx, cy, rx, ry
		lineLen = rx
	case "conic":
		cx, cy := w/2, h/2
		if g.HasPos {
			cx, cy = g.Pos[0].Resolve(w), g.Pos[1].Resolve(h)
		}
		out.CX, out.CY, out.FromAngle = cx, cy, g.From
		lineLen = 1
	}
	// stops
	var stops []GradientStop
	type rs struct {
		pos   float64
		has   bool
		color Color
		hint  bool
	}
	var list []rs
	for _, s := range g.Stops {
		x := rs{color: s.Color, hint: s.Hint}
		if s.HasPos {
			if g.Kind == "conic" {
				x.pos, x.has = s.Pos.V/100, true
				if s.Pos.K != style.LPct {
					x.pos = s.Pos.V / 360
				}
			} else if s.Pos.K == style.LPct {
				x.pos, x.has = s.Pos.V/100, true
			} else if lineLen > 0 {
				x.pos, x.has = s.Pos.Resolve(lineLen)/lineLen, true
			}
		}
		list = append(list, x)
	}
	if len(list) > 0 {
		if !list[0].has {
			list[0].pos, list[0].has = 0, true
		}
		if !list[len(list)-1].has {
			list[len(list)-1].pos, list[len(list)-1].has = 1, true
		}
	}
	// monotonic
	maxv := math.Inf(-1)
	for i := range list {
		if list[i].has {
			if list[i].pos < maxv {
				list[i].pos = maxv
			}
			maxv = list[i].pos
		}
	}
	for i := 0; i < len(list); i++ {
		if list[i].has {
			continue
		}
		j := i
		for j < len(list) && !list[j].has {
			j++
		}
		a, bb := list[i-1].pos, list[j].pos
		for k := i; k < j; k++ {
			list[k].pos = a + (bb-a)*float64(k-i+1)/float64(j-i+1)
			list[k].has = true
		}
		i = j
	}
	for _, x := range list {
		if x.hint {
			continue
		}
		stops = append(stops, GradientStop{Pos: x.pos, Color: x.color})
	}
	out.Stops = stops
	return out
}

func (p *painter) paintOutline(b *lbox) { p.paintOutlineRect(b, rectOf(b)) }

// paintOutlineRect draws b's outline around r (one fragment of a split block).
func (p *painter) paintOutlineRect(b *lbox, r Rect) {
	st := b.st
	w := st.OutlineWidth
	off := st.OutlineOffset
	e := w + off
	rr := Rect{r.X - e, r.Y - e, r.W + 2*e, r.H + 2*e}
	styl := st.OutlineStyle
	auto := styl == "auto"
	if auto {
		// Chromium's focus ring: a 2px rounded ring hugging the border box
		styl = "solid"
		w = 2
		e = w + off
		rr = Rect{r.X - e, r.Y - e, r.W + 2*e, r.H + 2*e}
	}
	rad := p.radiiWH(b, r.W, r.H)
	for i := range rad {
		if rad[i][0] > 0 {
			rad[i][0] += e
			rad[i][1] += e
		}
		if auto && rad[i][0] < 4 {
			rad[i] = [2]float64{4, 4}
		}
	}
	p.emit(Border{Rect: rr, Widths: [4]float64{w, w, w, w}, Colors: [4]Color{st.OutlineColor, st.OutlineColor, st.OutlineColor, st.OutlineColor},
		Styles: [4]string{styl, styl, styl, styl}, Radii: rad})
}

// paintCollapsed draws the resolved borders of a collapsed-border table.
func (p *painter) paintCollapsed(b *lbox) {
	m := b.extra.(*tableModel)
	cl := b.cl
	// column x boundaries and row y boundaries from cells
	R, C := len(m.rows), m.ncols
	xs := make([]float64, C+1)
	ys := make([]float64, R+1)
	set := make([]bool, C+1)
	for _, tc := range m.cells {
		cb := tc.b
		if !set[tc.col] {
			xs[tc.col], set[tc.col] = cb.ax, true
		}
		if !set[tc.col+tc.cs] {
			xs[tc.col+tc.cs], set[tc.col+tc.cs] = cb.ax+cb.w, true
		}
	}
	rset := make([]bool, R+1)
	for _, tc := range m.cells {
		cb := tc.b
		if !rset[tc.row] {
			ys[tc.row], rset[tc.row] = cb.ay, true
		}
		if !rset[tc.row+tc.rs] {
			ys[tc.row+tc.rs], rset[tc.row+tc.rs] = cb.ay+cb.h, true
		}
	}
	draw := func(e bedge, r Rect, horizontal bool) {
		if e.w <= 0 || e.style == "none" || e.style == "hidden" || e.style == "" {
			return
		}
		switch e.style {
		case "solid", "double", "inset", "outset", "groove", "ridge":
			p.emit(FillRect{Rect: r, Color: e.color})
		default:
			wd := [4]float64{}
			st := [4]string{}
			col := [4]Color{e.color, e.color, e.color, e.color}
			if horizontal {
				wd = [4]float64{r.H, 0, 0, 0}
				st = [4]string{e.style, "none", "none", "none"}
			} else {
				wd = [4]float64{0, 0, 0, r.W}
				st = [4]string{"none", "none", "none", e.style}
			}
			p.emit(Border{Rect: r, Widths: wd, Colors: col, Styles: st})
		}
	}
	for ri := 0; ri <= R; ri++ {
		for ci := 0; ci < C; ci++ {
			e := cl.h[ri][ci]
			x0, x1 := xs[ci], xs[ci+1]
			y := ys[ri]
			draw(e, Rect{x0 - edgeW(cl.v[minInt(ri, R-1)][ci])/2, y - e.w/2, x1 - x0 + edgeW(cl.v[minInt(ri, R-1)][ci])/2 + edgeW(cl.v[minInt(ri, R-1)][ci+1])/2, e.w}, true)
		}
	}
	for ri := 0; ri < R; ri++ {
		for ci := 0; ci <= C; ci++ {
			e := cl.v[ri][ci]
			y0, y1 := ys[ri], ys[ri+1]
			x := xs[ci]
			draw(e, Rect{x - e.w/2, y0 - edgeW(cl.h[ri][minInt(ci, C-1)])/2, e.w, y1 - y0 + edgeW(cl.h[ri][minInt(ci, C-1)])/2 + edgeW(cl.h[ri+1][minInt(ci, C-1)])/2}, false)
		}
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// themedControl reports whether b is a form control drawn with the native look.
func (p *painter) themedControl(b *lbox) (bool, bool) {
	if b.n == nil {
		return false, false
	}
	if b.isBtn && b.rep == nil {
		return !p.authorStyled(b, true), true
	}
	if b.rep == nil || b.inner != iControl {
		return false, false
	}
	switch b.rep.kind {
	case "input":
		switch b.rep.ctrl {
		case "checkbox", "radio", "range", "color", "file":
			return false, false
		case "button", "submit", "reset":
			return !p.authorStyled(b, true), true
		}
		return !p.authorStyled(b, false), false
	case "textarea", "select":
		return !p.authorStyled(b, false), false
	}
	return false, false
}
