package render

import (
	"math"
	"sort"
)

// Painting of CSS 3D transforms: layers, 3D rendering contexts and their depth
// sorting, backface culling and the projective hit-test maps.
//
// Model (as in Chromium's property trees): every element with a 3D transform,
// with transform-style: preserve-3d, or with backface-visibility: hidden inside
// a preserve-3d parent is a layer. The matrix of a layer is its own transform
// with the perspective of its container applied on top (Persp * Own). A layer
// whose container preserves 3D accumulates the container's matrix and joins the
// container's rendering context; every other layer is flattened into its
// container's plane (a homography of the z=0 plane). A 3D rendering context is
// drawn by sorting its layers back to front in the post-divide space.

type frame3 struct {
	owner *lbox
	ctx   *ctx3 // nil for a plain flat layer
	acc   Mat4
}

type ctx3 struct {
	root  *lbox
	parts []*part3
	base  [9]float64 // projective map of the context target to the page
}

type part3 struct {
	ops  []Op
	hits []hitRec
	acc  Mat4
	rect Rect
	seq  int
}

type plan3 struct {
	L      Mat4
	shift  [2]float64
	partic bool
	root   bool
	layer  bool
	cull   bool
	acc    Mat4
}

func transOnly(m Mat4) bool {
	const eps = 1e-9
	return m[3][0] == 0 && m[3][1] == 0 && m[3][3] == 1 && math.Abs(m[0][0]-1) < eps && math.Abs(m[1][1]-1) < eps &&
		math.Abs(m[0][1]) < eps && math.Abs(m[1][0]) < eps
}

// plan3 decides how b is painted: nil for the ordinary 2D path.
func (p *painter) plan3(b *lbox) *plan3 {
	st := b.st
	c := b.xfContainer()
	cpersp := false
	if c != nil && c != b {
		_, cpersp = c.persp4()
	}
	partic := p.fr != nil && p.fr.ctx != nil && c != nil && p.fr.owner == c && c.preserves3D() &&
		(b.hasTransform() || b.preserves3D() || st.BackfaceHidden)
	root := !partic && b.preserves3D()
	if !partic && !root && !b.has3DOp() && !cpersp {
		return nil
	}
	dpr := p.c.opts.DPR
	if dpr <= 0 {
		dpr = 1
	}
	rx := p.latX + math.Round((b.ax-p.latX)*dpr)/dpr
	ry := p.latY + math.Round((b.ay-p.latY)*dpr)/dpr
	shift := [2]float64{rx - b.ax, ry - b.ay}
	L, _ := b.local4(shift[0], shift[1])
	pl := &plan3{L: L, shift: shift, partic: partic, root: root}
	pl.acc = L
	if partic {
		pl.acc = p.fr.acc.mul(L)
	}
	own2D := !b.hasTransform() || b.own4(0, 0).is2D()
	pl.layer = partic || root || !own2D && !transOnly(L)
	if st.BackfaceHidden && !L.is2D() || partic && st.BackfaceHidden {
		if pl.acc.backFaceVisible() {
			pl.cull = true
		}
	}
	if !pl.layer && !pl.cull {
		return nil
	}
	return pl
}

func m3of(m [6]float64) [9]float64 {
	return [9]float64{m[0], m[2], m[4], m[1], m[3], m[5], 0, 0, 1}
}

func mul3(a, b [9]float64) (o [9]float64) {
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			o[i*3+j] = a[i*3]*b[j] + a[i*3+1]*b[3+j] + a[i*3+2]*b[6+j]
		}
	}
	return
}

func invert3(a [9]float64) ([9]float64, bool) {
	c00 := a[4]*a[8] - a[5]*a[7]
	c01 := a[5]*a[6] - a[3]*a[8]
	c02 := a[3]*a[7] - a[4]*a[6]
	d := a[0]*c00 + a[1]*c01 + a[2]*c02
	if math.Abs(d) < 1e-18 {
		return a, false
	}
	return [9]float64{
		c00 / d, (a[2]*a[7] - a[1]*a[8]) / d, (a[1]*a[5] - a[2]*a[4]) / d,
		c01 / d, (a[0]*a[8] - a[2]*a[6]) / d, (a[2]*a[3] - a[0]*a[5]) / d,
		c02 / d, (a[1]*a[6] - a[0]*a[7]) / d, (a[0]*a[4] - a[1]*a[3]) / d,
	}, true
}

// layerBounds is the plane area b's layer can paint into.
func (p *painter) layerBounds3(b *lbox) Rect {
	r := rectOf(b)
	pad := 0.0
	for _, sh := range b.st.BoxShadow {
		if sh.Inset {
			continue
		}
		e := math.Abs(sh.DX) + math.Abs(sh.DY) + sh.Blur*2 + math.Max(sh.Spread, 0)
		pad = math.Max(pad, e)
	}
	if b.st.OutlineWidth > 0 && b.st.OutlineStyle != "none" {
		pad = math.Max(pad, b.st.OutlineWidth+b.st.OutlineOffset)
	}
	r = Rect{r.X - pad, r.Y - pad, r.W + 2*pad, r.H + 2*pad}
	if !b.preserves3D() {
		ov := Rect{b.ovLeft, b.ovTop, b.ovRight - b.ovLeft, b.ovBottom - b.ovTop}
		if ov.W > 0 && ov.H > 0 {
			r = r.union(ov)
		}
	}
	return r
}

func (p *painter) beginEffects3D(b *lbox, pl *plan3) func() {
	saveM, saveHas := p.m, p.hasM
	saveHP, saveHasHP := p.hp, p.hasHP
	saveLX, saveLY := p.latX, p.latY
	saveFr := p.fr
	saveOps, saveHits := p.ops, p.hits
	if pl.cull {
		// a hidden back face paints nothing and cannot be hit: record into
		// slices that are thrown away
		p.ops, p.hits = nil, nil
		return func() {
			p.ops, p.hits = saveOps, saveHits
		}
	}
	var ctx *ctx3
	base := p.hp
	if !p.hasHP {
		base = m3of(p.m)
	}
	switch {
	case pl.partic:
		ctx = p.fr.ctx
	case pl.root:
		ctx = &ctx3{root: b, base: base}
	}
	if pl.partic {
		base = ctx.base
	}
	var part *part3
	if ctx != nil {
		p.seq3++
		part = &part3{acc: pl.acc, rect: rectOf(b), seq: p.seq3}
		p.ops, p.hits = nil, nil
	}
	push := PushTransform3D{M: mat16(pl.acc), Bounds: p.layerBounds3(b), Lattice: true, LatX: b.ax, LatY: b.ay}
	p.emit(push)
	p.latX, p.latY = b.ax, b.ay
	p.hp = mul3(base, pl.acc.plane())
	p.hasHP = true
	p.fr = &frame3{owner: b, ctx: ctx, acc: pl.acc}
	if pl.root || pl.partic {
		p.fr.ctx = ctx
	}
	endFx := p.beginFx(b)
	return func() {
		endFx()
		p.emit(PopTransform3D{})
		if part != nil {
			part.ops, part.hits = p.ops, p.hits
			p.ops, p.hits = saveOps, saveHits
			ctx.parts = append(ctx.parts, part)
			if pl.root {
				p.flushCtx(ctx)
			}
		}
		p.fr = saveFr
		p.m, p.hasM = saveM, saveHas
		p.hp, p.hasHP = saveHP, saveHasHP
		p.latX, p.latY = saveLX, saveLY
	}
}

// ---- depth sorting -------------------------------------------------------------------------

type v3 struct {
	u, v       float64 // position on the layer plane
	X, Y, Z, W float64 // homogeneous position in the context target space
}

func lerpV(a, b v3, t float64) v3 {
	return v3{u: a.u + (b.u-a.u)*t, v: a.v + (b.v-a.v)*t, X: a.X + (b.X-a.X)*t, Y: a.Y + (b.Y-a.Y)*t, Z: a.Z + (b.Z-a.Z)*t, W: a.W + (b.W-a.W)*t}
}

type poly3 struct {
	part  *part3
	vs    []v3
	n     [3]float64
	d     float64
	whole bool // not split: draws the whole layer
}

const nearW = 1e-5

// quadPoly builds the polygon of a layer rectangle in the context target
// space, clipped to the w > 0 half space.
func quadPoly(pt *part3) *poly3 {
	r := pt.rect
	corners := [][2]float64{{r.X, r.Y}, {r.Right(), r.Y}, {r.Right(), r.Bottom()}, {r.X, r.Bottom()}}
	vs := make([]v3, 4)
	for i, c := range corners {
		X, Y, Z, W := pt.acc.apply(c[0], c[1], 0)
		vs[i] = v3{u: c[0], v: c[1], X: X, Y: Y, Z: Z, W: W}
	}
	vs = clipW(vs)
	if len(vs) < 3 {
		return nil
	}
	pl := &poly3{part: pt, vs: vs, whole: len(vs) == 4}
	if !pl.plane() {
		return nil
	}
	return pl
}

func clipW(vs []v3) []v3 {
	var out []v3
	n := len(vs)
	for i := 0; i < n; i++ {
		a, b := vs[i], vs[(i+1)%n]
		ain, bin := a.W > nearW, b.W > nearW
		if ain {
			out = append(out, a)
		}
		if ain != bin {
			t := (nearW - a.W) / (b.W - a.W)
			out = append(out, lerpV(a, b, t))
		}
	}
	return out
}

func (q v3) post() (x, y, z float64) { return q.X / q.W, q.Y / q.W, q.Z / q.W }

// plane fits the plane of the polygon in post-divide space; false for a
// degenerate (edge-on or empty) polygon.
func (pl *poly3) plane() bool {
	var nx, ny, nz float64
	n := len(pl.vs)
	for i := 0; i < n; i++ {
		ax, ay, az := pl.vs[i].post()
		bx, by, bz := pl.vs[(i+1)%n].post()
		nx += ay*bz - az*by
		ny += az*bx - ax*bz
		nz += ax*by - ay*bx
	}
	l := math.Sqrt(nx*nx + ny*ny + nz*nz)
	if l < 1e-9 {
		return false
	}
	nx, ny, nz = nx/l, ny/l, nz/l
	if math.Abs(nz) < 1e-6 {
		return false
	}
	x, y, z := pl.vs[0].post()
	pl.n = [3]float64{nx, ny, nz}
	pl.d = nx*x + ny*y + nz*z
	return true
}

func (pl *poly3) dist(q v3) float64 {
	return pl.n[0]*q.X + pl.n[1]*q.Y + pl.n[2]*q.Z - pl.d*q.W
}

type piece3 struct {
	part  *part3
	clip  [][2]float64
	whole bool
}

func (pl *poly3) piece() piece3 {
	pc := piece3{part: pl.part, whole: pl.whole}
	if !pl.whole {
		for _, q := range pl.vs {
			pc.clip = append(pc.clip, [2]float64{q.u, q.v})
		}
	}
	return pc
}

// splitPoly cuts pl by the plane of root into the parts in front and behind.
func splitPoly(pl, root *poly3) (front, back *poly3) {
	var f, b []v3
	n := len(pl.vs)
	const eps = 1e-4
	for i := 0; i < n; i++ {
		a, c := pl.vs[i], pl.vs[(i+1)%n]
		da, dc := root.dist(a)/a.W, root.dist(c)/c.W
		switch {
		case da > eps:
			f = append(f, a)
		case da < -eps:
			b = append(b, a)
		default:
			f = append(f, a)
			b = append(b, a)
		}
		if da > eps && dc < -eps || da < -eps && dc > eps {
			// the edge crosses the plane: interpolate in homogeneous space
			fa, fc := root.dist(a), root.dist(c)
			t := fa / (fa - fc)
			m := lerpV(a, c, t)
			f = append(f, m)
			b = append(b, m)
		}
	}
	mk := func(vs []v3) *poly3 {
		if len(vs) < 3 {
			return nil
		}
		q := &poly3{part: pl.part, vs: vs}
		if !q.plane() {
			return nil
		}
		return q
	}
	return mk(f), mk(b)
}

func bspOrder(list []*poly3, out *[]piece3) {
	if len(list) == 0 {
		return
	}
	root := list[0]
	var front, back, cop []*poly3
	cop = append(cop, root)
	const eps = 1e-4
	for _, q := range list[1:] {
		var pos, neg int
		for _, v := range q.vs {
			d := root.dist(v) / v.W
			if d > eps {
				pos++
			} else if d < -eps {
				neg++
			}
		}
		switch {
		case pos == 0 && neg == 0:
			cop = append(cop, q)
		case neg == 0:
			front = append(front, q)
		case pos == 0:
			back = append(back, q)
		default:
			f, b := splitPoly(q, root)
			if f != nil {
				front = append(front, f)
			}
			if b != nil {
				back = append(back, b)
			}
		}
	}
	sort.SliceStable(cop, func(i, j int) bool { return cop[i].part.seq < cop[j].part.seq })
	emitCop := func() {
		for _, q := range cop {
			*out = append(*out, q.piece())
		}
	}
	if root.n[2] > 0 {
		// the viewer (at +z) is in front of the plane: the back side is farther
		bspOrder(back, out)
		emitCop()
		bspOrder(front, out)
	} else {
		bspOrder(front, out)
		emitCop()
		bspOrder(back, out)
	}
}

// flushCtx sorts the layers of a finished 3D rendering context back to front
// and emits them (and their hit records) into the enclosing op stream.
func (p *painter) flushCtx(ctx *ctx3) {
	parts := ctx.parts
	sort.SliceStable(parts, func(i, j int) bool { return parts[i].seq < parts[j].seq })
	var polys []*poly3
	hitOnly := map[*part3]bool{}
	for _, pt := range parts {
		if len(pt.ops) <= 2 {
			hitOnly[pt] = true
			continue
		}
		if q := quadPoly(pt); q != nil {
			polys = append(polys, q)
		}
	}
	var order []piece3
	bspOrder(polys, &order)
	lastPiece := map[*part3]int{}
	for i, pc := range order {
		lastPiece[pc.part] = i
	}
	for i, pc := range order {
		ops := pc.part.ops
		if pc.clip != nil {
			ops = append([]Op(nil), ops...)
			if pu, ok := ops[0].(PushTransform3D); ok {
				pu.Clip = pc.clip
				ops[0] = pu
			}
		}
		p.ops = append(p.ops, ops...)
		if lastPiece[pc.part] == i {
			p.hits = append(p.hits, pc.part.hits...)
		}
	}
	for _, pt := range parts {
		if _, ok := lastPiece[pt]; !ok {
			// hit records of layers with no visible ink keep their paint order
			p.hits = append(p.hits, pt.hits...)
		}
	}
}
