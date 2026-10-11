package raster

import "math"

// Fixed-point edge setup after Skia's SkAnalyticEdge / SkEdge (SkFixed 16.16,
// FDot6 26.6, forward differencing for quads and cubics). The coverage walk
// of SkScan_AAAPath computes the exact trapezoid area of every segment, so
// the segments produced here are accumulated with the exact-area rasterizer;
// what matters for matching Chromium is where the segment end points land.

// tunables for experiments (see text_fx_lab_test.go)
var (
	fxEvenRound = false // SK_RASTERIZE_EVEN_ROUNDING: round instead of truncate to 1/256
	fxBigSnap   = true  // snap curve vertices to whole pixels when a step is >= 2px tall
)

const fxOne = 1 << 16

var fxBigThresh = fxOne * 2
var fxBigVariant = 0
var fxSmallRound = 0
var fxMidOv = map[[2]int]float64{}
var fxExactSet = map[int]bool{}
var fxShiftOv func(idx int) int
var fxSmallSlide = false
var fxVTrace func(idx, count int, nx, ny, sx, sy float64)
var fxQuadIdx, fxQuadOverride int
var fxQInfo func(idx, shift, y0, y2, wind, qy int)
var fxStartMode = 0
var fxRMode = 0
var fxShiftMap []int
var fxQOffMode = 0
var fxQSnap = true
var fxQNoSnapMid = false
var fxDS = 5
var fxQuadOffset = false
var fxTrace = false
var fxShiftBias = 0

func fxSnapY(y int) int {
	return int((uint32(y)+(fxOne>>3))>>14) << 14
}

func fxToFDot6x4(v float32) int {
	if fxEvenRound {
		return int(math.RoundToEven(float64(v) * 256))
	}
	return int(v * 256)
}

func fxAbs(a int) int {
	if a < 0 {
		return -a
	}
	return a
}

// fxQuickDiv is the 16.16 quotient of two FDot6 values.
func fxQuickDiv(a, b int) int { return int((int64(a) << 16) / int64(b)) }

func fxMul(a, b int) int { return int((int64(a) * int64(b)) >> 16) }

func fxDiffToShift(dx, dy int) int {
	dist := aaaCheapDist(dx, dy)
	dist = (dist + (1 << (fxDS - 1))) >> fxDS
	n := 0
	for v := dist; v > 0; v >>= 1 {
		n++
	}
	if fxShiftMap != nil {
		return fxShiftMap[min(n, len(fxShiftMap)-1)]
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
	dx := (x1 - x0) >> 10
	b.edges = append(b.edges, fxEdge{x0, y0, y1, fxQuickDiv(dx, dy), wind})
}

// seg adds one piece of a curve (SkAnalyticEdge::updateLine).
func (b *fxBuilder) seg(x0, y0, x1, y1, slope, wind int) bool {
	if fxQSnap {
		y0, y1 = fxSnapY(y0), fxSnapY(y1)
	}
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
			// flatten_double_quad_extrema
			p01.y, p12.y = m.y, m.y
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

func absf32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

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
	if shift > 0 {
		shift += fxShiftBias
	}
	if fxShiftOv != nil && fxShiftOv(fxQuadIdx+1) != 0 {
		shift += fxShiftOv(fxQuadIdx + 1)
	}
	if shift == 0 {
		shift = 1
	} else if shift > 6 {
		shift = 6
	}
	count := 1 << shift
	ax, ay := (x0-x1-x1+x2)<<9, (y0-y1-y1+y2)<<9
	bx, by := (x1-x0)<<10, (y1-y0)<<10
	qx, qy := (x0<<10)>>2, (y0<<10)>>2
	qdx, qdy := (bx+(ax>>shift))>>2, (by+(ay>>shift))>>2
	qddx, qddy := (ax>>(shift-1))>>2, (ay>>(shift-1))>>2
	lastX, lastY := (x2<<10)>>2, (y2<<10)>>2
	cshift := shift - 1
	fxQuadIdx++
	if fxExactSet[fxQuadIdx] {
		fx0, fy0, fx1, fy1, fx2, fy2 := float64(x0)/256, float64(y0)/256, float64(x1)/256, float64(y1)/256, float64(x2)/256, float64(y2)/256
		px, py := fx0, fy0
		for k := 1; k <= 32; k++ {
			t := float64(k) / 32
			mt := 1 - t
			nx := mt*mt*fx0 + 2*mt*t*fx1 + t*t*fx2
			ny := mt*mt*fy0 + 2*mt*t*fy1 + t*t*fy2
			b.edges = append(b.edges, fxEdge{int(px * fxOne), int(py * fxOne), int(ny * fxOne), int((nx - px) / (ny - py) * fxOne), wind})
			px, py = nx, ny
		}
		return
	}
	oldx, oldy := qx, qy
	if fxQInfo != nil {
		fxQInfo(fxQuadIdx, shift, y0, y2, wind, qy)
	}
	switch fxStartMode {
	case 1:
		oldy = (qy >> 14) << 14 // floor
	case 2:
		oldy = ((qy + (1<<14 - 1)) >> 14) << 14 // ceil
	}
	qy = fxSnapY(qy)
	lastY = fxSnapY(lastY)
	sx, sy := qx, qy
	if fxQuadOverride == fxQuadIdx || fxQuadOffset || (fxQOffMode == 1 && wind > 0) || (fxQOffMode == 2 && wind < 0) {
		oldy = oldy // true start
	} else if fxStartMode == 0 {
		oldy = qy
	}
	for count > 0 {
		var newx, newy, nsx, nsy, slope int
		count--
		if fxTrace {
			println("step", count, qdx>>cshift, qdy>>cshift, oldy)
		}
		if count > 0 {
			newx = oldx + (qdx >> cshift)
			newy = oldy + (qdy >> cshift)
			if fxBigSnap && fxAbs(qdy>>cshift) >= fxBigThresh {
				diffY := (newy - sy) >> 10
				slope = math.MaxInt32
				if diffY != 0 {
					slope = fxQuickDiv((newx-sx)>>10, diffY)
				}
				nsy = fxMin(lastY, (newy+fxOne/2)&^(fxOne-1))
				switch fxBigVariant {
				case 0:
					nsx = newx - fxMul(slope, newy-nsy)
				case 1:
					nsx = newx
				case 2:
					nsx = newx
					if d := (nsy - sy) >> 10; d != 0 {
						slope = fxQuickDiv((newx-sx)>>10, d)
					}
				case 3:
					if d := (nsy - sy) >> 10; d != 0 {
						slope = fxQuickDiv((newx-sx)>>10, d)
					}
					nsx = newx - fxMul(slope, newy-nsy)
				}
			} else {
				nsy = fxMin(lastY, fxSnapY(newy))
				switch fxSmallRound {
				case 1:
					nsy = fxMin(lastY, ((newy+(1<<14-1))>>14)<<14)
				case 2:
					nsy = fxMin(lastY, (newy>>14)<<14)
				case 3:
					nsy = fxMin(lastY, ((newy+(1<<13)-1)>>14)<<14)
				}
				if fxQNoSnapMid {
					nsy = fxMin(lastY, newy)
				}
				nsx = newx
				diffY := (nsy - sy) >> 10
				slope = math.MaxInt32
				if diffY != 0 {
					slope = fxQuickDiv((newx-sx)>>10, diffY)
				}
				if fxSmallSlide {
					if dY := (newy - sy) >> 10; dY != 0 {
						sl := fxQuickDiv((newx-sx)>>10, dY)
						nsx = newx - fxMul(sl, newy-nsy)
						if diffY != 0 {
							slope = fxQuickDiv((nsx-sx)>>10, diffY)
						}
					}
				}
			}
			qdx += qddx
			qdy += qddy
		} else {
			newx, newy = lastX, lastY
			nsx, nsy = newx, newy
			diffY := (newy - sy) >> 10
			slope = math.MaxInt32
			if diffY != 0 {
				slope = fxQuickDiv((newx-sx)>>10, diffY)
			}
		}
		if v, ok := fxMidOv[[2]int{fxQuadIdx, count}]; ok {
			nsy = int(v * fxOne)
		}
		if fxVTrace != nil {
			fxVTrace(fxQuadIdx, count, float64(newx)/65536, float64(newy)/65536, float64(nsx)/65536, float64(nsy)/65536)
		}
		if slope < math.MaxInt32 && b.seg(sx, sy, nsx, nsy, slope, wind) {
			sx, sy = nsx, nsy
		}
		oldx, oldy = newx, newy
		if fxRMode == 1 && count > 0 {
			oldy = nsy
		}
	}
}

func fxMin(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func fxCubicDelta(a, b, c, d int) int {
	one := ((a*8 - b*15 + 6*c + d) * 19) >> 9
	two := ((a + 6*b - c*15 + d*8) * 19) >> 9
	return maxInt(fxAbs(one), fxAbs(two))
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
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
		t2 := (ts[1] - ts[0]) / (1 - ts[0])
		m, rr := fxChopCubic(r, t2)
		m[2].y, rr[1].y = m[3].y, m[3].y
		b.monoCubic(l)
		b.monoCubic(m)
		b.monoCubic(rr)
	}
}

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
	dx := fxCubicDelta(x[0], x[1], x[2], x[3])
	dy := fxCubicDelta(y[0], y[1], y[2], y[3])
	shift := fxDiffToShift(dx, dy) + 1
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
		return
	}
	cx, cdx, cddx, cdddx := coef(x)
	cy, cdy, cddy, cdddy := coef(y)
	cx >>= 2
	cy = fxSnapY(cy >> 2)
	lastX, lastY := (x[3]<<10)>>2, fxSnapY((y[3]<<10)>>2)
	sx, sy := cx, cy
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
		nsy := fxMin(lastY, fxSnapY(newy))
		diffY := (nsy - sy) >> 10
		slope := math.MaxInt32
		if diffY != 0 {
			slope = fxQuickDiv((newx-sx)>>10, diffY)
		}
		if slope < math.MaxInt32 && b.seg(sx, sy, newx, nsy, slope, wind) {
			sx, sy = newx, nsy
		}
		oldx, oldy = newx, newy
	}
}

// fxFillPath rasterizes p (device pixels, y down) like Skia's analytic AA.
// Cubic and quad scaling follow SkEdgeBuilder.
func fxFillPath(p *Path, clip IRect) *Mask {
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for i := 0; i < len(p.Pts); i += 2 {
		x, y := p.Pts[i], p.Pts[i+1]
		if math.IsNaN(x) || math.IsNaN(y) {
			return nil
		}
		minX, maxX = math.Min(minX, x), math.Max(maxX, x)
		minY, maxY = math.Min(minY, y), math.Max(maxY, y)
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
	r := newRasterizer(IRect{b.X0, b.Y0, b.X1, b.Y1})
	defer r.release()
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
	return r.mask(NonZero)
}
