package raster

import "math"

// Fixed-point edge setup after Skia's SkAnalyticEdge / SkEdge (SkFixed 16.16,
// FDot6 26.6, forward differencing for quads and cubics), which Chromium uses
// to scan convert glyph outlines.
//
// SkScan_AAAPath then walks the edges and adds up the exact trapezoid area of
// every row segment, so what decides the pixels is where the segment end
// points land; the coverage itself is accumulated here with the exact-area
// rasterizer. Measured against Chromium 1194 (canvas paths, text_fx_*_test.go)
// the vertex rules below are what matter:
//
//   - input coordinates are truncated to 1/256 pixel (relative to the mask
//     origin, which is an integer pixel);
//   - the start and end y of every curve and both y of every line are rounded
//     to a quarter pixel (SnapY); the vertices in between are the forward
//     differences added to the snapped start, again rounded to a quarter pixel
//     with x left where it was;
//   - when one forward-difference step is two or more pixels tall, the vertex
//     is rounded to a whole pixel instead and slides along the chord;
//   - slopes come from FDot6 (1/64 pixel) differences.

const fxOne = 1 << 16

func fxSnapY(y int) int {
	return int((uint32(y)+(fxOne>>3))>>14) << 14
}

func fxToFDot6x4(v float32) int { return int(v * 256) }

func fxAbs(a int) int {
	if a < 0 {
		return -a
	}
	return a
}

func fxMin(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func fxMax(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// fxQuickDiv is the 16.16 quotient of two FDot6 values.
func fxQuickDiv(a, b int) int { return int((int64(a) << 16) / int64(b)) }

func fxMul(a, b int) int { return int((int64(a) * int64(b)) >> 16) }

func fxCheapDist(dx, dy int) int {
	dx, dy = fxAbs(dx), fxAbs(dy)
	if dx > dy {
		return dx + dy>>1
	}
	return dy + dx>>1
}

func fxDiffToShift(dx, dy int) int {
	dist := (fxCheapDist(dx, dy) + (1 << 4)) >> (3 + 2)
	n := 0
	for v := dist; v > 0; v >>= 1 {
		n++
	}
	return n >> 1
}

// fxEdge is one top-to-bottom line segment in 16.16.
type fxEdge struct {
	x, y0, y1, slope int
	wind             int
}

type fxBuilder struct {
	edges []fxEdge
	// cont is set for paths Skia does not consider convex: its general
	// walker (aaa_walk_edges) calls SkAnalyticCubicEdge::keepContinuous()
	// before every cubic update, which restarts the forward differencing
	// from the x the previous line segment actually ended at.
	cont bool
}

type fpt struct{ x, y float32 }

// line adds a straight edge (SkAnalyticEdge::setLine).
func (b *fxBuilder) line(p0, p1 fpt) {
	x0 := fxToFDot6x4(p0.x) << 8
	y0 := fxSnapY(fxToFDot6x4(p0.y) << 8)
	x1 := fxToFDot6x4(p1.x) << 8
	y1 := fxSnapY(fxToFDot6x4(p1.y) << 8)
	wind := 1
	if y0 > y1 {
		x0, x1 = x1, x0
		y0, y1 = y1, y0
		wind = -1
	}
	dy := (y1 - y0) >> 10
	if dy == 0 {
		return
	}
	b.edges = append(b.edges, fxEdge{x0, y0, y1, fxQuickDiv((x1-x0)>>10, dy), wind})
}

// seg adds one piece of a curve (SkAnalyticEdge::updateLine).
func (b *fxBuilder) seg(x0, y0, y1, slope, wind int) bool {
	y0, y1 = fxSnapY(y0), fxSnapY(y1)
	if (y1-y0)>>10 == 0 {
		return false
	}
	b.edges = append(b.edges, fxEdge{x0, y0, y1, slope, wind})
	return true
}

func fxValidUnitDivide(numer, denom float32) (float32, bool) {
	if numer < 0 {
		numer, denom = -numer, -denom
	}
	if denom == 0 || numer == 0 || numer >= denom {
		return 0, false
	}
	r := numer / denom
	if r != r || r == 0 {
		return 0, false
	}
	return r, true
}

func lerp32(a, b fpt, t float32) fpt {
	return fpt{a.x + (b.x-a.x)*t, a.y + (b.y-a.y)*t}
}

func absf32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

// quad adds a quadratic, chopped at its y extremum as SkEdgeBuilder does.
func (b *fxBuilder) quad(p [3]fpt) {
	a, bb, c := p[0].y, p[1].y, p[2].y
	ab, bc := a-bb, bb-c
	if ab < 0 {
		bc = -bc
	}
	if ab == 0 || bc < 0 {
		if t, ok := fxValidUnitDivide(a-bb, a-bb-bb+c); ok {
			p01, p12 := lerp32(p[0], p[1], t), lerp32(p[1], p[2], t)
			m := lerp32(p01, p12, t)
			p01.y, p12.y = m.y, m.y // flatten_double_quad_extrema
			b.monoQuad([3]fpt{p[0], p01, m})
			b.monoQuad([3]fpt{m, p12, p[2]})
			return
		}
		if absf32(a-bb) < absf32(bb-c) {
			bb = a
		} else {
			bb = c
		}
		p[1].y = bb
	}
	b.monoQuad(p)
}

// monoQuad mirrors SkAnalyticQuadraticEdge::setQuadratic / updateQuadratic.
func (b *fxBuilder) monoQuad(q [3]fpt) {
	x0, y0 := fxToFDot6x4(q[0].x), fxToFDot6x4(q[0].y)
	x1, y1 := fxToFDot6x4(q[1].x), fxToFDot6x4(q[1].y)
	x2, y2 := fxToFDot6x4(q[2].x), fxToFDot6x4(q[2].y)
	wind := 1
	if y0 > y2 {
		x0, x2 = x2, x0
		y0, y2 = y2, y0
		wind = -1
	}
	if (y0+32)>>6 == (y2+32)>>6 {
		return
	}
	dx := ((x1 << 1) - x0 - x2) >> 2
	dy := ((y1 << 1) - y0 - y2) >> 2
	shift := fxDiffToShift(dx, dy)
	if shift == 0 {
		shift = 1
	} else if shift > 6 {
		shift = 6
	}
	count := 1 << shift
	ax, ay := (x0-x1-x1+x2)<<9, (y0-y1-y1+y2)<<9
	bx, by := (x1-x0)<<10, (y1-y0)<<10
	// everything is scaled by 4 (accuracy 2) and brought back to 16.16
	qx, qy := (x0<<10)>>2, (y0<<10)>>2
	qdx, qdy := (bx+(ax>>shift))>>2, (by+(ay>>shift))>>2
	qddx, qddy := (ax>>(shift-1))>>2, (ay>>(shift-1))>>2
	lastX, lastY := (x2<<10)>>2, fxSnapY((y2<<10)>>2)
	qy = fxSnapY(qy)
	cshift := shift - 1
	sx, sy := qx, qy // snapped position of the last emitted vertex
	oldx, oldy := qx, qy
	for count > 0 {
		var newx, newy, nsx, nsy, slope int
		count--
		if count > 0 {
			newx = oldx + (qdx >> cshift)
			newy = oldy + (qdy >> cshift)
			if fxAbs(qdy>>cshift) >= fxOne*2 { // only snap to whole pixels when the step is tall
				diffY := (newy - sy) >> 10
				slope = math.MaxInt32
				if diffY != 0 {
					slope = fxQuickDiv((newx-sx)>>10, diffY)
				}
				nsy = fxMin(lastY, (newy+fxOne/2)&^(fxOne-1))
				nsx = newx - fxMul(slope, newy-nsy)
			} else {
				nsy = fxMin(lastY, fxSnapY(newy))
				nsx = newx
				diffY := (nsy - sy) >> 10
				slope = math.MaxInt32
				if diffY != 0 {
					slope = fxQuickDiv((newx-sx)>>10, diffY)
				}
			}
			qdx += qddx
			qdy += qddy
		} else { // last segment
			newx, newy = lastX, lastY
			nsx, nsy = newx, newy
			diffY := (newy - sy) >> 10
			slope = math.MaxInt32
			if diffY != 0 {
				slope = fxQuickDiv((newx-sx)>>10, diffY)
			}
		}
		if slope < math.MaxInt32 && b.seg(sx, sy, nsy, slope, wind) {
			sx, sy = nsx, nsy
		}
		oldx, oldy = newx, newy
	}
}

func fxCubicDelta(a, b, c, d int) int {
	one := ((a*8 - b*15 + 6*c + d) * 19) >> 9
	two := ((a + 6*b - c*15 + d*8) * 19) >> 9
	return fxMax(fxAbs(one), fxAbs(two))
}

// fxFindUnitQuadRoots mirrors SkFindUnitQuadRoots.
func fxFindUnitQuadRoots(A, B, C float32) []float32 {
	if A == 0 {
		if r, ok := fxValidUnitDivide(-C, B); ok {
			return []float32{r}
		}
		return nil
	}
	dr := float64(B)*float64(B) - 4*float64(A)*float64(C)
	if dr < 0 {
		return nil
	}
	R := float32(math.Sqrt(dr))
	var Q float32
	if B < 0 {
		Q = -(B - R) / 2
	} else {
		Q = -(B + R) / 2
	}
	var rs []float32
	if r, ok := fxValidUnitDivide(Q, A); ok {
		rs = append(rs, r)
	}
	if r, ok := fxValidUnitDivide(C, Q); ok {
		rs = append(rs, r)
	}
	if len(rs) == 2 {
		if rs[0] > rs[1] {
			rs[0], rs[1] = rs[1], rs[0]
		} else if rs[0] == rs[1] {
			rs = rs[:1]
		}
	}
	return rs
}

func fxChopCubic(c [4]fpt, t float32) (l, r [4]fpt) {
	p01, p12, p23 := lerp32(c[0], c[1], t), lerp32(c[1], c[2], t), lerp32(c[2], c[3], t)
	p012, p123 := lerp32(p01, p12, t), lerp32(p12, p23, t)
	m := lerp32(p012, p123, t)
	return [4]fpt{c[0], p01, p012, m}, [4]fpt{m, p123, p23, c[3]}
}

// cubic adds a cubic chopped at its y extrema (SkChopCubicAtYExtrema).
func (b *fxBuilder) cubic(c [4]fpt) {
	A := c[3].y - c[0].y + 3*(c[1].y-c[2].y)
	B := 2 * (c[0].y - c[1].y - c[1].y + c[2].y)
	C := c[1].y - c[0].y
	ts := fxFindUnitQuadRoots(A, B, C)
	switch len(ts) {
	case 0:
		b.monoCubic(c)
	case 1:
		l, r := fxChopCubic(c, ts[0])
		l[2].y, r[1].y = l[3].y, l[3].y
		b.monoCubic(l)
		b.monoCubic(r)
	default:
		l, r := fxChopCubic(c, ts[0])
		l[2].y, r[1].y = l[3].y, l[3].y
		m, rr := fxChopCubic(r, (ts[1]-ts[0])/(1-ts[0]))
		m[2].y, rr[1].y = m[3].y, m[3].y
		b.monoCubic(l)
		b.monoCubic(m)
		b.monoCubic(rr)
	}
}

// monoCubic mirrors SkAnalyticCubicEdge::setCubic / updateCubic.
func (b *fxBuilder) monoCubic(c [4]fpt) {
	var x, y [4]int
	for i := range c {
		x[i], y[i] = fxToFDot6x4(c[i].x), fxToFDot6x4(c[i].y)
	}
	wind := 1
	if y[0] > y[3] {
		x[0], x[3] = x[3], x[0]
		x[1], x[2] = x[2], x[1]
		y[0], y[3] = y[3], y[0]
		y[1], y[2] = y[2], y[1]
		wind = -1
	}
	if (y[0]+32)>>6 == (y[3]+32)>>6 {
		return
	}
	shift := fxDiffToShift(fxCubicDelta(x[0], x[1], x[2], x[3]), fxCubicDelta(y[0], y[1], y[2], y[3])) + 1
	if shift > 6 {
		shift = 6
	}
	upShift := 6
	downShift := shift + upShift - 10
	if downShift < 0 {
		downShift = 0
		upShift = 10 - shift
	}
	coef := func(v [4]int) (cx, cdx, cddx, cdddx int) {
		B := (3 * (v[1] - v[0])) << upShift
		C := (3 * (v[0] - v[1] - v[1] + v[2])) << upShift
		D := (v[3] + 3*(v[1]-v[2]) - v[0]) << upShift
		cx = v[0] << 10
		cdx = B + (C >> shift) + (D >> (2 * shift))
		cddx = 2*C + ((3 * D) >> (shift - 1))
		cdddx = (3 * D) >> (shift - 1)
		return cx >> 2, cdx >> 2, cddx >> 2, cdddx >> 2
	}
	cx, cdx, cddx, cdddx := coef(x)
	cy, cdy, cddy, cdddy := coef(y)
	cy = fxSnapY(cy)
	lastX, lastY := (x[3]<<10)>>2, fxSnapY((y[3]<<10)>>2)
	sy := cy
	oldx, oldy := cx, cy
	count := -(1 << shift)
	for count < 0 {
		var newx, newy int
		count++
		if count < 0 {
			newx = oldx + (cdx >> downShift)
			cdx += cddx >> shift
			cddx += cdddx
			newy = oldy + (cdy >> downShift)
			cdy += cddy >> shift
			cddy += cdddy
		} else {
			newx, newy = lastX, lastY
		}
		if newy < oldy {
			newy = oldy
		}
		nsy := fxSnapY(newy)
		if lastY < nsy {
			nsy = lastY
			count = 0
		}
		diffY := (nsy - sy) >> 10
		slope := math.MaxInt32
		if diffY != 0 {
			slope = fxQuickDiv((newx-oldx)>>10, diffY)
		}
		// the edge starts at the previous curve point's x and the snapped y
		emitted := slope < math.MaxInt32 && b.seg(oldx, sy, nsy, slope, wind)
		if emitted && b.cont {
			// keepContinuous: fCx = fX, the x the edge reaches at its lower y
			newx = fxAdvanceX(oldx, slope, sy, nsy)
		}
		sy = nsy
		oldx, oldy = newx, newy
	}
}

// fxAAA selects the port of Skia's analytic scan converter (text_aaa.go)
// over exact-area accumulation.
var fxAAA = true

// fxFillPath rasterizes p (device pixels, y down) like Skia's analytic AA,
// nonzero winding, restricted to clip. Returns nil when nothing is covered.
func fxFillPath(p *Path, clip IRect) *Mask { return fxFillUnion(p, nil, false, clip) }

// fxPathEdges converts p (device pixels) to analytic edges relative to
// (ox, oy), flipping their winding when asked.
func fxPathEdges(fb *fxBuilder, p *Path, ox, oy float64, flip bool) {
	first := len(fb.edges)
	pts := p.Pts
	i := 0
	var cur, start fpt
	has := false
	rel := func(x, y float64) fpt { return fpt{float32(x - ox), float32(y - oy)} }
	closeContour := func() {
		if has && cur != start {
			fb.line(cur, start)
		}
		cur = start
	}
	for _, c := range p.Cmds {
		switch c {
		case 'M':
			closeContour()
			start = rel(pts[i], pts[i+1])
			cur = start
			has = true
			i += 2
		case 'L':
			n := rel(pts[i], pts[i+1])
			fb.line(cur, n)
			cur = n
			i += 2
		case 'Q':
			p1, p2 := rel(pts[i], pts[i+1]), rel(pts[i+2], pts[i+3])
			fb.quad([3]fpt{cur, p1, p2})
			cur = p2
			i += 4
		case 'C':
			p1, p2, p3 := rel(pts[i], pts[i+1]), rel(pts[i+2], pts[i+3]), rel(pts[i+4], pts[i+5])
			fb.cubic([4]fpt{cur, p1, p2, p3})
			cur = p3
			i += 6
		case 'Z':
			closeContour()
		}
	}
	closeContour()
	if flip {
		for j := first; j < len(fb.edges); j++ {
			fb.edges[j].wind = -fb.edges[j].wind
		}
	}
}

// fxFillUnion fills p, together with the second path q when it is not nil
// (the outline of a stroke), as the nonzero union of both: this is Skia's
// stroke-and-fill. flipP reverses the winding of p so that it agrees with q.
func fxFillUnion(p, q *Path, flipP bool, clip IRect) *Mask {
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, pp := range []*Path{p, q} {
		if pp == nil {
			continue
		}
		for i := 0; i < len(pp.Pts); i += 2 {
			x, y := pp.Pts[i], pp.Pts[i+1]
			if math.IsNaN(x) || math.IsNaN(y) {
				return nil
			}
			minX, maxX = math.Min(minX, x), math.Max(maxX, x)
			minY, maxY = math.Min(minY, y), math.Max(maxY, y)
		}
	}
	if minX > maxX {
		return nil
	}
	const lim = 1 << 20
	bounds := IRect{int(math.Floor(math.Max(minX, -lim))), int(math.Floor(math.Max(minY, -lim))), int(math.Ceil(math.Min(maxX, lim))), int(math.Ceil(math.Min(maxY, lim)))}
	b := bounds.Intersect(clip)
	if b.Empty() {
		return nil
	}
	ox, oy := float64(bounds.X0), float64(bounds.Y0)
	var fb fxBuilder
	fb.cont = !pathIsConvex(p)
	fxPathEdges(&fb, p, ox, oy, flipP)
	if q != nil {
		fb.cont = !pathIsConvex(q)
		fxPathEdges(&fb, q, ox, oy, false)
	}
	if fxAAA {
		npts := 0
		for _, pp := range []*Path{p, q} {
			if pp != nil {
				npts += len(pp.Pts) / 2
			}
		}
		rel := IRect{b.X0 - bounds.X0, b.Y0 - bounds.Y0, b.X1 - bounds.X0, b.Y1 - bounds.Y0}
		m := aaaFill(fb.edges, IRect{0, 0, bounds.W(), bounds.H()}, rel, q == nil && pathIsConvex(p), npts)
		m.B = b
		return m
	}
	r := newRasterizer(b)
	defer r.release()
	if q != nil {
		fxWalk(r, fb.edges, ox, oy)
		return fxMask(r)
	}
	const k = 1.0 / fxOne
	for _, e := range fb.edges {
		xe := e.x + fxMul(e.slope, e.y1-e.y0)
		x0, y0 := float64(e.x)*k+ox, float64(e.y0)*k+oy
		x1, y1 := float64(xe)*k+ox, float64(e.y1)*k+oy
		if e.wind > 0 {
			r.line(x0, y0, x1, y1)
		} else {
			r.line(x1, y1, x0, y0)
		}
	}
	return fxMask(r)
}

// fxMask turns the accumulated signed areas into 8-bit coverage. Skia's
// analytic AA truncates its fixed-point coverage (area * 256, capped at 255).
func fxMask(r *rasterizer) *Mask {
	m := newMask(r.b)
	for y := 0; y < r.h; y++ {
		row := r.acc[y*r.stride : y*r.stride+r.w]
		out := m.A[y*m.Stride : y*m.Stride+r.w]
		var a float32
		for x, d := range row {
			a += d
			v := a
			if v < 0 {
				v = -v
			}
			if v >= 1 {
				out[x] = 255
			} else {
				out[x] = uint8(v * 256)
			}
		}
	}
	return m
}

// fxTrim shrinks a mask to its non-zero bounding box (the exact outline
// bounds are larger than the snapped polygon the scan converter fills).
func fxTrim(m *Mask) *Mask {
	if m == nil {
		return nil
	}
	x0, y0, x1, y1 := m.B.W(), m.B.H(), -1, -1
	for y := 0; y < m.B.H(); y++ {
		row := m.A[y*m.Stride : y*m.Stride+m.B.W()]
		for x, v := range row {
			if v != 0 {
				x0 = min(x0, x)
				x1 = max(x1, x)
				y0 = min(y0, y)
				y1 = max(y1, y)
			}
		}
	}
	if x1 < 0 {
		return nil
	}
	if x0 == 0 && y0 == 0 && x1 == m.B.W()-1 && y1 == m.B.H()-1 {
		return m
	}
	nb := IRect{m.B.X0 + x0, m.B.Y0 + y0, m.B.X0 + x1 + 1, m.B.Y0 + y1 + 1}
	o := newMask(nb)
	for y := y0; y <= y1; y++ {
		copy(o.A[(y-y0)*o.Stride:(y-y0+1)*o.Stride], m.A[y*m.Stride+x0:y*m.Stride+x1+1])
	}
	return o
}
