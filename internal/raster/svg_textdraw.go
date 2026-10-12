package raster

import (
	"math"
	"strings"
)

// Painting of SVG text in Blink's scaled space (see svg_textlayout.go): the
// canvas matrix is the screen matrix times 1/k and glyph origins are the
// scaled-space positions, so the device matrix of a glyph and its origin are
// computed in single precision exactly as the product of those matrices.

// svgPG is one glyph ready for rasterization: its device 2x2 matrix, device
// origin (without the sampling offset, for glyphs drawn as paths) and the
// integer pixel and quarter-pixel subpixel cell Skia puts it in.
type svgPG struct {
	g          Glyph
	a, b, c, d float64
	dx, dy     float64
	ix, iy     int
	sux, suy   int
}

// svgTextXf maps the scaled text space to the device.
type svgTextXf struct {
	m          Matrix
	k          float64
	a, b, c, d float32 // device matrix times 1/k, single precision
}

func newSvgTextXf(m Matrix, k float64) svgTextXf {
	t := svgTextXf{m: m, k: k}
	t.a, t.b, t.c, t.d = float32(m[0]), float32(m[1]), float32(m[2]), float32(m[3])
	if k != 1 {
		ik := float32(1 / float32(k))
		t.a, t.b, t.c, t.d = t.a*ik, t.b*ik, t.c*ik, t.d*ik
	}
	return t
}

// fragXf is the matrix of one painted fragment (SkMatrix of the canvas
// pre-translated to the fragment origin and post-translated by Skia's
// sampling offset) together with the glyph-space matrix.
type fragXf struct {
	a, b, c, d float32 // glyph-space matrix (rotation and scale included)
	px, py     float32 // origin mapped to the device, no sampling offset
	tx, ty     float32 // the same plus the sampling offset
	axis       int
}

// frag builds the matrix of a fragment whose origin is (x, y) in scaled
// space and that is painted rotated by angle and scaled by sx.
func (t svgTextXf) frag(x, y, angle, sx, pivX, pivY float64) fragXf {
	a, b, c, d := t.a, t.b, t.c, t.d
	tx0, ty0 := float32(t.m[4]), float32(t.m[5])
	// the fragment origin is a LayoutUnit: floored to 1/64
	X, Y := lu64(x), lu64(y)
	if angle != 0 || sx != 1 {
		// the canvas is concatenated with translate(P) rotate scale translate(-P)
		// about the unquantized origin P (float matrix product)
		// Blink keeps the angle as a float in degrees
		rad := float64(float32(angle*180/math.Pi)) * math.Pi / 180
		s, cs := math.Sincos(rad)
		if q := math.Round(float64(float32(angle*180/math.Pi)) / 90); math.Abs(float64(float32(angle*180/math.Pi))-q*90) < 1e-9 {
			// quarter turns are exact
			s, cs = [4]float64{0, 1, 0, -1}[int(q)&3], [4]float64{1, 0, -1, 0}[int(q)&3]
		}
		Px, Py := float64(float32(pivX)), float64(float32(pivY))
		ra, rb, rc, rd := cs*sx, s*sx, -s, cs
		e := Px - (ra*Px + rc*Py)
		f := Py - (rb*Px + rd*Py)
		fa, fb, fc, fd, fe, ff := float32(ra), float32(rb), float32(rc), float32(rd), float32(e), float32(f)
		na, nb := a*fa+c*fb, b*fa+d*fb
		nc, nd := a*fc+c*fd, b*fc+d*fd
		tx0, ty0 = (a*fe+c*ff)+tx0, (b*fe+d*ff)+ty0
		a, b, c, d = na, nb, nc, nd
	}
	px := tx0 + (a*X + c*Y)
	py := ty0 + (b*X + d*Y)
	axis := xfAxis(float64(a), float64(b), float64(c), float64(d))
	hx, hy := float32(0.125), float32(0.125)
	switch axis {
	case 1:
		hy = 0.5
	case 2:
		hx = 0.5
	}
	return fragXf{a: a, b: b, c: c, d: d, px: px, py: py, tx: px + hx, ty: py + hy, axis: axis}
}

// glyph places a glyph at offset (gx, gy) from the fragment origin.
func (f fragXf) glyph(g Glyph, gx, gy float32) svgPG {
	x := (gx*f.a + gy*f.c) + f.tx
	y := (gy*f.d + gx*f.b) + f.ty
	fx, fy := math.Floor(float64(x)), math.Floor(float64(y))
	pg := svgPG{g: g, a: float64(f.a), b: float64(f.b), c: float64(f.c), d: float64(f.d),
		dx: float64((gx*f.a + gy*f.c) + f.px), dy: float64((gy*f.d + gx*f.b) + f.py),
		ix: int(fx), iy: int(fy)}
	// quarter-pixel subpixel indices on the axes that have them
	if f.axis != 2 {
		pg.sux = int(math.Floor(float64(int32((float64(x)-fx)*65536)) / 16384))
	}
	if f.axis != 1 {
		pg.suy = int(math.Floor(float64(int32((float64(y)-fy)*65536)) / 16384))
	}
	return pg
}

// svgGlyphMask rasterizes glyphs of one size the way Skia does: glyphs whose
// text matrix has a column longer than 256 px are paths, all others are
// cached masks placed on the quarter-pixel grid and added up through the
// preblend table. With st.w > 0 the glyphs are stroked (user-space stroke).
func (c *Context) svgGlyphMask(gs []svgPG, size float64, fake, skew bool, lum uint8, st xfStroke) *Mask {
	clip := c.drawBounds()
	type placed struct {
		m      *Mask
		ox, oy int
	}
	var ps []placed
	bb := IRect{}
	var pathOut *Mask
	for _, g := range gs {
		if math.IsNaN(g.a+g.b+g.c+g.d+g.dx+g.dy) || math.IsInf(g.dx+g.dy, 0) {
			continue
		}
		if math.Hypot(g.a*size, g.b*size) > 256 || math.Hypot(g.c*size, g.d*size) > 256 {
			if st.w > 0 {
				continue
			}
			var mk *Mask
			if fake {
				p := xfPathGlyph(g.g, size, Matrix{g.a, g.b, g.c, g.d, 0, 0}, skew, 0, 0)
				if p == nil {
					continue
				}
				fill, sp, flip := xfBoldPaths(p, size, g.a, g.b, g.c, g.d)
				if fill == nil {
					continue
				}
				t := Translate(g.dx, g.dy)
				mk = fxFillUnion(fill.Transform(t), sp.Transform(t), flip, clip)
			} else {
				p := xfPathGlyph(g.g, size, Matrix{g.a, g.b, g.c, g.d, g.dx, g.dy}, skew, 0, 0)
				if p == nil {
					continue
				}
				mk = fxFillPath(p, clip)
			}
			pathOut = unionMasks(pathOut, mk)
			continue
		}
		var gm *Mask
		if st.w > 0 {
			gm = xfGlyphMaskS(g.g, size, g.a, g.b, g.c, g.d, false, skew, g.sux, g.suy, st)
		} else {
			gm = xfGlyphMask(g.g, size, g.a, g.b, g.c, g.d, fake, skew, g.sux, g.suy)
		}
		if gm == nil {
			continue
		}
		r := IRect{gm.B.X0 + g.ix, gm.B.Y0 + g.iy, gm.B.X1 + g.ix, gm.B.Y1 + g.iy}
		if r.Intersect(clip).Empty() {
			continue
		}
		ps = append(ps, placed{gm, g.ix, g.iy})
		bb = bb.Union(r)
	}
	bb = bb.Intersect(clip)
	var out *Mask
	if len(ps) > 0 && !bb.Empty() {
		out = newMask(bb)
		lut := lutForLum(lum)
		for _, p := range ps {
			for yy := max(p.m.B.Y0+p.oy, bb.Y0); yy < min(p.m.B.Y1+p.oy, bb.Y1); yy++ {
				src := p.m.Row(yy - p.oy)
				x0 := max(p.m.B.X0+p.ox, bb.X0)
				x1 := min(p.m.B.X1+p.ox, bb.X1)
				if x1 <= x0 {
					continue
				}
				srow := src[x0-p.ox-p.m.B.X0 : x1-p.ox-p.m.B.X0]
				drow := out.Row(yy)[x0-bb.X0 : x1-bb.X0]
				for i, v := range srow {
					if v == 0 {
						continue
					}
					v = lut[v]
					s := uint32(drow[i]) + uint32(v)
					if s > 255 {
						s = 255
					}
					drow[i] = uint8(s)
				}
			}
		}
	}
	return unionMasks(out, pathOut)
}

// fragment is a group of glyphs painted together (same style, plain).
type fragment struct {
	gl []textGlyph
}

func fragmentsOf(placed []textGlyph) []fragment {
	var frs []fragment
	for _, p := range placed {
		if k := len(frs) - 1; k >= 0 {
			f := &frs[k]
			pv := &f.gl[len(f.gl)-1]
			sn, cs := math.Sincos(pv.angle)
			if !p.fragStart && pv.node == p.node && pv.st == p.st && pv.angle == p.angle && pv.sx == p.sx && pv.size == p.size &&
				math.Abs(pv.x+pv.w*cs-p.x) < 1e-6 && math.Abs(pv.y+pv.w*sn-p.y) < 1e-6 && math.Abs(pv.w-pv.nat) < 1e-6 && math.Abs(p.w-p.nat) < 1e-6 {
				f.gl = append(f.gl, p)
				continue
			}
		}
		frs = append(frs, fragment{gl: []textGlyph{p}})
	}
	return frs
}

func (r *svgR) drawText(n *svgNode, st sStyle, m Matrix) {
	if !st.visible {
		return
	}
	k := svgScreenScale(m)
	if st.geomPrec {
		k = 1
	}
	placed := r.layoutText(n, &st, k)
	if len(placed) == 0 {
		return
	}
	bb, hasBB := glyphsBBox(placed, k)
	xf := newSvgTextXf(m, k)
	c := r.c
	for _, fr := range fragmentsOf(placed) {
		p0 := &fr.gl[0]
		if !p0.st.visible {
			continue
		}
		order := []string{"fill", "stroke"}
		if po := strings.Fields(p0.st.paintOrder); len(po) > 0 && po[0] != "normal" {
			if po[0] == "stroke" {
				order = []string{"stroke", "fill"}
			}
		}
		for _, step := range order {
			if step == "fill" {
				r.fillFragment(c, n, fr, xf, bb, hasBB, m)
			} else {
				r.strokeFragment(c, n, fr, xf, bb, hasBB, m)
			}
		}
		r.decorateFragment(c, fr, k)
	}
}

func fragmentPGs(fr fragment, xf svgTextXf) []svgPG {
	out := make([]svgPG, 0, len(fr.gl))
	if bd := fr.gl[0].bidi; bd != nil {
		// right-to-left text: shape the fragment as a whole
		var rs []rune
		for _, g := range fr.gl {
			rs = append(rs, g.r)
		}
		p := &fr.gl[0]
		fx := xf.frag(p.x, p.y, p.angle, p.sx, p.pivX, p.pivY)
		sh := ShapeDir(bd.spec, normalizeText(string(rs)), bd.ls, bd.ws, true, false)
		for _, g := range sh.Glyphs {
			if g.R != ' ' {
				out = append(out, fx.glyph(Glyph{R: g.R, Face: g.Face, GID: g.GID}, float32(g.X), 0))
			}
		}
		return out
	}
	var fx fragXf
	var off float64 // exact pen offset from the fragment start
	for i := range fr.gl {
		p := &fr.gl[i]
		if i == 0 {
			fx = xf.frag(p.x, p.y, p.angle, p.sx, p.pivX, p.pivY)
			off = 0
		}
		if !(p.skip || strings.TrimSpace(string(p.r)) == "") {
			out = append(out, fx.glyph(p.g, float32(off), 0))
		}
		off += p.ex
	}
	return out
}

func (r *svgR) fillFragment(c *Context, n *svgNode, fr fragment, xf svgTextXf, bb Rect, hasBB bool, m Matrix) {
	p0 := &fr.gl[0]
	if p0.st.fill.kind == pNone || p0.st.fillOp <= 0 {
		return
	}
	sh := r.shaderFor(p0.st.fill, p0.st.fillOp, n, bb, hasBB, p0.st, m)
	if sh == nil {
		return
	}
	var lum uint8
	if p0.st.fill.kind == pColor {
		lum = textLum(Color{p0.st.fill.col.R, p0.st.fill.col.G, p0.st.fill.col.B, 1})
	}
	pgs := fragmentPGs(fr, xf)
	if len(pgs) == 0 {
		return
	}
	if mk := c.svgGlyphMask(pgs, p0.size, p0.fake, p0.skew, lum, xfStroke{}); mk != nil {
		c.draw(mk, sh)
	}
}

func (r *svgR) strokeFragment(c *Context, n *svgNode, fr fragment, xf svgTextXf, bb Rect, hasBB bool, m Matrix) {
	p0 := &fr.gl[0]
	if p0.st.stroke.kind == pNone || p0.st.strokeOp <= 0 || p0.st.sw <= 0 {
		return
	}
	sh := r.shaderFor(p0.st.stroke, p0.st.strokeOp, n, bb, hasBB, p0.st, m)
	if sh == nil {
		return
	}
	ss := r.strokeStyle(p0.st)
	pgs := fragmentPGs(fr, xf)
	if len(pgs) == 0 {
		return
	}
	big := false
	for _, g := range pgs {
		if math.Hypot(g.a*p0.size, g.b*p0.size) > 256 || math.Hypot(g.c*p0.size, g.d*p0.size) > 256 {
			big = true
		}
	}
	if len(ss.Dash) == 0 && !p0.fake && !big {
		var lum uint8
		if col := p0.st.stroke; col.kind == pColor {
			lum = textLum(Color{col.col.R, col.col.G, col.col.B, 1})
		}
		xs := xfStroke{w: float32(ss.Width * xf.k), miter: float32(ss.MiterLimit), join: ss.Join, cap: ss.Cap}
		if mk := c.svgGlyphMask(pgs, p0.size, false, p0.skew, lum, xs); mk != nil {
			c.draw(mk, sh)
		}
		return
	}
	// general case: the outline in scaled space, stroked there
	path := NewPath()
	for i := range fr.gl {
		p := &fr.gl[i]
		if p.skip || strings.TrimSpace(string(p.r)) == "" {
			continue
		}
		s, cs := math.Sincos(p.angle)
		gp := xfPathGlyph(p.g, p.size, Matrix{cs * p.sx, s * p.sx, -s, cs, float64(float32(p.x)), float64(float32(p.y))}, p.skew, 0, 0)
		if gp != nil {
			path.AddPath(gp, Identity)
		}
	}
	if path.Empty() {
		return
	}
	sc := ss
	sc.Width *= xf.k
	if len(sc.Dash) > 0 {
		d := make([]float64, len(sc.Dash))
		for i, v := range sc.Dash {
			d[i] = v * xf.k
		}
		sc.Dash = d
		sc.DashOffset *= xf.k
	}
	dm := m.Mul(Scale(1/xf.k, 1/xf.k))
	if mk := StrokeMask(path, dm, sc, c.drawBounds()); mk != nil {
		c.draw(mk, sh)
	}
}

func (r *svgR) decorateFragment(c *Context, fr fragment, k float64) {
	p0 := &fr.gl[0]
	if p0.st.decoration == "" || p0.st.fill.kind != pColor {
		return
	}
	var w float64
	for _, g := range fr.gl {
		w += g.w
	}
	// geometry of the decoration lines, as measured from Chromium for the
	// (scaled) font: thickness floor(size/10) with a minimum of 1, the
	// underline top below the baseline by max(ceil(size/20), ceil(th/2)), the
	// overline on top of the ascent, the line-through by a size table
	size := p0.size
	th := math.Max(1, size/10)
	col := p0.st.fill.col
	col.A *= p0.st.fillOp
	deco := func(top float64) {
		pa := NewPath()
		pa.Rect(0, top, w, th)
		s, cs := math.Sincos(p0.angle)
		// the line is drawn from the (floored) fragment origin, rotated about the real one
		qx, qy := float64(lu64(p0.x)), float64(lu64(p0.y))
		ox, oy := p0.pivX+cs*(qx-p0.pivX)-s*(qy-p0.pivY), p0.pivY+s*(qx-p0.pivX)+cs*(qy-p0.pivY)
		pa = pa.Transform(Matrix{cs, s, -s, cs, ox, oy}).Transform(Scale(1/k, 1/k))
		// Skia fills the rotated line with its analytic scan converter
		if mk := fxFillPath(pa.Transform(c.st.ctm), c.drawBounds()); mk != nil {
			c.draw(mk, Solid(col))
		}
	}
	if strings.Contains(p0.st.decoration, "underline") {
		deco(math.Max(math.Ceil(size/20), math.Ceil(th/2)))
	}
	if strings.Contains(p0.st.decoration, "line-through") {
		deco(-lineThroughCenter(size, th) - th/2)
	}
	if strings.Contains(p0.st.decoration, "overline") {
		deco(-p0.asc - th)
	}
}

// lu64 floors v to 1/64 (Blink's LayoutUnit) in single precision.
func lu64(v float64) float32 { return float32(math.Floor(float64(float32(v))*64)) / 64 }
