package raster

import "math"

// A port of Skia's dash path effect: SkDashPathEffect / SkDashPath::
// InternalFilter (src/utils/SkDashPath.cpp) on top of SkContourMeasure
// (src/core/SkContourMeasure.cpp). It is what Chromium runs for
// stroke-dasharray on SVG text: SkScalerContext applies the effect to the
// glyph outline (in the user space of the text size) before the stroker, so
// the dashes have to be reproduced in single precision with the same segment
// tolerances, t-value quantisation and zero-length / closed-contour rules to
// get the same dash ends.

// dpt is an SkPoint (float32).
type dpt struct{ x, y float32 }

func (a dpt) sub(b dpt) dpt { return dpt{a.x - b.x, a.y - b.y} }
func (a dpt) isZero() bool  { return a.x == 0 && a.y == 0 }

func dashDist(a, b dpt) float32 {
	dx, dy := a.x-b.x, a.y-b.y
	mag2 := dx*dx + dy*dy
	if !math.IsInf(float64(mag2), 0) && mag2 == mag2 {
		return float32(math.Sqrt(float64(mag2)))
	}
	return float32(math.Sqrt(float64(dx)*float64(dx) + float64(dy)*float64(dy)))
}

func dashFinite(v float32) bool { return !(math.IsInf(float64(v), 0) || v != v) }

// dashLerp is SkScalarInterp.
func dashLerp(a, b, t float32) float32 { return a + (b-a)*t }

// dashInterp is skvx interp(v0, v1, t) = v0 + (v1 - v0) * t.
func dashInterp(a, b dpt, t float32) dpt {
	return dpt{a.x + (b.x-a.x)*t, a.y + (b.y-a.y)*t}
}

// dashMix is unchecked_mix(a, b, t) = (b - a) * t + a.
func dashMix(a, b dpt, t float32) dpt {
	return dpt{(b.x-a.x)*t + a.x, (b.y-a.y)*t + a.y}
}

// dashChopQuadAt is SkChopQuadAt.
func dashChopQuadAt(src []dpt, t float32) [5]dpt {
	p01 := dashInterp(src[0], src[1], t)
	p12 := dashInterp(src[1], src[2], t)
	return [5]dpt{src[0], p01, dashInterp(p01, p12, t), p12, src[2]}
}

// dashChopCubicAt is SkChopCubicAt (single t).
func dashChopCubicAt(src []dpt, t float32) [7]dpt {
	if t == 1 {
		return [7]dpt{src[0], src[1], src[2], src[3], src[3], src[3], src[3]}
	}
	ab := dashMix(src[0], src[1], t)
	bc := dashMix(src[1], src[2], t)
	cd := dashMix(src[2], src[3], t)
	abc := dashMix(ab, bc, t)
	bcd := dashMix(bc, cd, t)
	abcd := dashMix(abc, bcd, t)
	return [7]dpt{src[0], ab, abc, abcd, bcd, cd, src[3]}
}

// dashEvalQuad is SkEvalQuadAt (position).
func dashEvalQuad(p []dpt, t float32) dpt {
	ax := p[2].x - 2*p[1].x + p[0].x
	ay := p[2].y - 2*p[1].y + p[0].y
	bx := 2 * (p[1].x - p[0].x)
	by := 2 * (p[1].y - p[0].y)
	return dpt{(ax*t+bx)*t + p[0].x, (ay*t+by)*t + p[0].y}
}

// dashEvalCubic is SkEvalCubicAt (position).
func dashEvalCubic(p []dpt, t float32) dpt {
	ax := p[3].x + 3*(p[1].x-p[2].x) - p[0].x
	ay := p[3].y + 3*(p[1].y-p[2].y) - p[0].y
	bx := 3 * (p[2].x - 2*p[1].x + p[0].x)
	by := 3 * (p[2].y - 2*p[1].y + p[0].y)
	cx := 3 * (p[1].x - p[0].x)
	cy := 3 * (p[1].y - p[0].y)
	return dpt{((ax*t+bx)*t+cx)*t + p[0].x, ((ay*t+by)*t+cy)*t + p[0].y}
}

// ---- contour measure ----------------------------------------------------------------------

const (
	dashSegLine = iota
	dashSegQuad
	dashSegCubic
)

const dashMaxT = 0x3FFFFFFF

func dashT2Scalar(t int32) float32 {
	// 1/kMaxTValue is not representable; as a float it is exactly 2^-30
	return float32(t) * (float32(1) / float32(dashMaxT))
}

type dashSeg struct {
	dist    float32
	ptIndex int
	typ     int
	tval    int32
}

// dashContour is an SkContourMeasure: one contour as monotone segments.
type dashContour struct {
	segs   []dashSeg
	pts    []dpt
	length float32
	closed bool
}

type dashOp struct {
	v byte // 'M' 'L' 'Q' 'C' 'Z'
	p [3]dpt
}

func dashOpsOf(p *Path) []dashOp {
	var ops []dashOp
	i := 0
	f := func(k int) dpt { return dpt{float32(p.Pts[i+2*k]), float32(p.Pts[i+2*k+1])} }
	for _, c := range p.Cmds {
		switch c {
		case 'M', 'L':
			ops = append(ops, dashOp{v: c, p: [3]dpt{f(0)}})
			i += 2
		case 'Q':
			ops = append(ops, dashOp{v: c, p: [3]dpt{f(0), f(1)}})
			i += 4
		case 'C':
			ops = append(ops, dashOp{v: c, p: [3]dpt{f(0), f(1), f(2)}})
			i += 6
		case 'Z':
			ops = append(ops, dashOp{v: 'Z'})
		}
	}
	return ops
}

// dashMeasure is SkPathMeasure / SkContourMeasureIter.
type dashMeasure struct {
	ops         []dashOp
	pos         int
	cur         dpt // current point while walking
	tol         float32
	forceClosed bool
	c           *dashContour
}

func newDashMeasure(p *Path, forceClosed bool, resScale float32) *dashMeasure {
	m := &dashMeasure{ops: dashOpsOf(p), forceClosed: forceClosed}
	m.tol = 0.5 * (float32(1) / resScale)
	for _, v := range p.Pts {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			m.ops = nil // a non-finite path has no contours
			break
		}
	}
	m.c = m.nextContour()
	return m
}

func (m *dashMeasure) length() float32 {
	if m.c == nil {
		return 0
	}
	return m.c.length
}

func (m *dashMeasure) isClosed() bool { return m.c != nil && m.c.closed }

func (m *dashMeasure) next() bool {
	m.c = m.nextContour()
	return m.c != nil
}

func (m *dashMeasure) nextContour() *dashContour {
	for m.pos < len(m.ops) {
		if c := m.buildSegments(); c != nil {
			return c
		}
	}
	return nil
}

func dashTSpanBigEnough(t int32) bool { return t>>10 != 0 }

func dashQuadTooCurvy(p []dpt, tol float32) bool {
	dx := (p[1].x / 2) - (((p[0].x + p[2].x) / 2) / 2)
	dy := (p[1].y / 2) - (((p[0].y + p[2].y) / 2) / 2)
	d := float32(math.Max(math.Abs(float64(dx)), math.Abs(float64(dy))))
	return d > tol
}

func dashCheapExceeds(pt dpt, x, y, tol float32) bool {
	d := float32(math.Max(math.Abs(float64(x-pt.x)), math.Abs(float64(y-pt.y))))
	return d > tol
}

func dashCubicTooCurvy(p []dpt, tol float32) bool {
	return dashCheapExceeds(p[1], dashLerp(p[0].x, p[3].x, 1.0/3), dashLerp(p[0].y, p[3].y, 1.0/3), tol) ||
		dashCheapExceeds(p[2], dashLerp(p[0].x, p[3].x, 2.0/3), dashLerp(p[0].y, p[3].y, 2.0/3), tol)
}

const dashMaxRecursion = 8

func (c *dashContour) addSeg(dist float32, ptIndex, typ int, tval int32) {
	c.segs = append(c.segs, dashSeg{dist, ptIndex, typ, tval})
}

func (m *dashMeasure) quadSegs(c *dashContour, pts []dpt, distance float32, mint, maxt int32, ptIndex, depth int) float32 {
	if depth < dashMaxRecursion && dashTSpanBigEnough(maxt-mint) && dashQuadTooCurvy(pts, m.tol) {
		halft := (mint + maxt) >> 1
		tmp := dashChopQuadAt(pts, 0.5)
		distance = m.quadSegs(c, tmp[0:3], distance, mint, halft, ptIndex, depth+1)
		distance = m.quadSegs(c, tmp[2:5], distance, halft, maxt, ptIndex, depth+1)
	} else {
		d := dashDist(pts[0], pts[2])
		prev := distance
		distance += d
		if distance > prev {
			c.addSeg(distance, ptIndex, dashSegQuad, maxt)
		}
	}
	return distance
}

func (m *dashMeasure) cubicSegs(c *dashContour, pts []dpt, distance float32, mint, maxt int32, ptIndex, depth int) float32 {
	if depth < dashMaxRecursion && dashTSpanBigEnough(maxt-mint) && dashCubicTooCurvy(pts, m.tol) {
		halft := (mint + maxt) >> 1
		tmp := dashChopCubicAt(pts, 0.5)
		distance = m.cubicSegs(c, tmp[0:4], distance, mint, halft, ptIndex, depth+1)
		distance = m.cubicSegs(c, tmp[3:7], distance, halft, maxt, ptIndex, depth+1)
	} else {
		d := dashDist(pts[0], pts[3])
		prev := distance
		distance += d
		if distance > prev {
			c.addSeg(distance, ptIndex, dashSegCubic, maxt)
		}
	}
	return distance
}

func (m *dashMeasure) lineSeg(c *dashContour, p0, p1 dpt, distance float32, ptIndex int) float32 {
	d := dashDist(p0, p1)
	prev := distance
	distance += d
	if distance > prev {
		c.addSeg(distance, ptIndex, dashSegLine, dashMaxT)
	}
	return distance
}

func (m *dashMeasure) buildSegments() *dashContour {
	c := &dashContour{}
	ptIndex := -1
	var distance float32
	haveClose := m.forceClosed
	haveMove := false
	var cur dpt
loop:
	for ; m.pos < len(m.ops); m.pos++ {
		op := m.ops[m.pos]
		if haveMove && op.v == 'M' {
			break loop
		}
		switch op.v {
		case 'M':
			ptIndex++
			c.pts = append(c.pts, op.p[0])
			cur = op.p[0]
			haveMove = true
		case 'L':
			if !haveMove {
				continue
			}
			prev := distance
			distance = m.lineSeg(c, cur, op.p[0], distance, ptIndex)
			if distance > prev {
				c.pts = append(c.pts, op.p[0])
				ptIndex++
			}
			cur = op.p[0]
		case 'Q':
			if !haveMove {
				continue
			}
			q := []dpt{cur, op.p[0], op.p[1]}
			prev := distance
			distance = m.quadSegs(c, q, distance, 0, dashMaxT, ptIndex, 0)
			if distance > prev {
				c.pts = append(c.pts, op.p[0], op.p[1])
				ptIndex += 2
			}
			cur = op.p[1]
		case 'C':
			if !haveMove {
				continue
			}
			q := []dpt{cur, op.p[0], op.p[1], op.p[2]}
			prev := distance
			distance = m.cubicSegs(c, q, distance, 0, dashMaxT, ptIndex, 0)
			if distance > prev {
				c.pts = append(c.pts, op.p[0], op.p[1], op.p[2])
				ptIndex += 3
			}
			cur = op.p[2]
		case 'Z':
			haveClose = true
		}
	}
	if !dashFinite(distance) || len(c.segs) == 0 {
		return nil
	}
	if haveClose {
		prev := distance
		first := c.pts[0]
		distance = m.lineSeg(c, c.pts[ptIndex], first, distance, ptIndex)
		if distance > prev {
			c.pts = append(c.pts, first)
		}
	}
	c.length = distance
	c.closed = haveClose
	return c
}

// distanceToSegment finds the segment holding distance and the t value on it.
func (c *dashContour) distanceToSegment(distance float32) (int, float32) {
	// SkTKSearch: lower bound over fDistance
	lo, hi := 0, len(c.segs)-1
	for lo < hi {
		mid := (hi + lo) >> 1
		if c.segs[mid].dist < distance {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	idx := hi
	if c.segs[hi].dist < distance {
		idx = hi + 1 // ~(hi+1) xor-ed back
	}
	if idx >= len(c.segs) {
		idx = len(c.segs) - 1 // Skia asserts; clamp
	}
	var startT, startD float32
	if idx > 0 {
		startD = c.segs[idx-1].dist
		if c.segs[idx-1].ptIndex == c.segs[idx].ptIndex {
			startT = dashT2Scalar(c.segs[idx-1].tval)
		}
	}
	seg := c.segs[idx]
	t := startT + (dashT2Scalar(seg.tval)-startT)*(distance-startD)/(seg.dist-startD)
	return idx, t
}

func dashPosAt(pts []dpt, typ int, t float32) dpt {
	switch typ {
	case dashSegLine:
		return dpt{dashLerp(pts[0].x, pts[1].x, t), dashLerp(pts[0].y, pts[1].y, t)}
	case dashSegQuad:
		return dashEvalQuad(pts, t)
	}
	return dashEvalCubic(pts, t)
}

// dashBuilder is the subset of SkPathBuilder the dasher writes to.
type dashBuilder struct {
	p    *Path
	last dpt
	has  bool
}

func (b *dashBuilder) empty() bool { return len(b.p.Cmds) == 0 }
func (b *dashBuilder) moveTo(q dpt) {
	b.p.Cmds = append(b.p.Cmds, 'M')
	b.p.Pts = append(b.p.Pts, float64(q.x), float64(q.y))
	b.last, b.has = q, true
}
func (b *dashBuilder) lineTo(q dpt) {
	if b.empty() {
		b.moveTo(dpt{})
	}
	b.p.Cmds = append(b.p.Cmds, 'L')
	b.p.Pts = append(b.p.Pts, float64(q.x), float64(q.y))
	b.last = q
}
func (b *dashBuilder) quadTo(c, q dpt) {
	if b.empty() {
		b.moveTo(dpt{})
	}
	b.p.Cmds = append(b.p.Cmds, 'Q')
	b.p.Pts = append(b.p.Pts, float64(c.x), float64(c.y), float64(q.x), float64(q.y))
	b.last = q
}
func (b *dashBuilder) cubicTo(c1, c2, q dpt) {
	if b.empty() {
		b.moveTo(dpt{})
	}
	b.p.Cmds = append(b.p.Cmds, 'C')
	b.p.Pts = append(b.p.Pts, float64(c1.x), float64(c1.y), float64(c2.x), float64(c2.y), float64(q.x), float64(q.y))
	b.last = q
}

// segTo is SkContourMeasure_segTo.
func dashSegTo(pts []dpt, typ int, startT, stopT float32, dst *dashBuilder) {
	if startT == stopT {
		if !dst.empty() {
			// a zero-length dash is a zero-length line; the stroker adds the caps
			dst.lineTo(dst.last)
		}
		return
	}
	switch typ {
	case dashSegLine:
		if stopT == 1 {
			dst.lineTo(pts[1])
		} else {
			dst.lineTo(dpt{dashLerp(pts[0].x, pts[1].x, stopT), dashLerp(pts[0].y, pts[1].y, stopT)})
		}
	case dashSegQuad:
		if startT == 0 {
			if stopT == 1 {
				dst.quadTo(pts[1], pts[2])
			} else {
				t0 := dashChopQuadAt(pts, stopT)
				dst.quadTo(t0[1], t0[2])
			}
		} else {
			t0 := dashChopQuadAt(pts, startT)
			if stopT == 1 {
				dst.quadTo(t0[3], t0[4])
			} else {
				t1 := dashChopQuadAt(t0[2:5], (stopT-startT)/(1-startT))
				dst.quadTo(t1[1], t1[2])
			}
		}
	case dashSegCubic:
		if startT == 0 {
			if stopT == 1 {
				dst.cubicTo(pts[1], pts[2], pts[3])
			} else {
				t0 := dashChopCubicAt(pts, stopT)
				dst.cubicTo(t0[1], t0[2], t0[3])
			}
		} else {
			t0 := dashChopCubicAt(pts, startT)
			if stopT == 1 {
				dst.cubicTo(t0[4], t0[5], t0[6])
			} else {
				t1 := dashChopCubicAt(t0[3:7], (stopT-startT)/(1-startT))
				dst.cubicTo(t1[1], t1[2], t1[3])
			}
		}
	}
}

func segPtCount(typ int) int {
	switch typ {
	case dashSegLine:
		return 2
	case dashSegQuad:
		return 3
	}
	return 4
}

// getSegment is SkContourMeasure::getSegment.
func (m *dashMeasure) getSegment(startD, stopD float32, dst *dashBuilder, startWithMoveTo bool) bool {
	c := m.c
	if c == nil {
		return false
	}
	length := c.length
	if startD < 0 {
		startD = 0
	}
	if stopD > length {
		stopD = length
	}
	if !(startD <= stopD) {
		return false
	}
	if len(c.segs) == 0 {
		return false
	}
	si, startT := c.distanceToSegment(startD)
	if !dashFinite(startT) {
		return false
	}
	ei, stopT := c.distanceToSegment(stopD)
	if !dashFinite(stopT) {
		return false
	}
	seg := c.segs[si]
	stop := c.segs[ei]
	if startWithMoveTo {
		dst.moveTo(dashPosAt(c.pts[seg.ptIndex:], seg.typ, startT))
	}
	if seg.ptIndex == stop.ptIndex {
		dashSegTo(c.pts[seg.ptIndex:seg.ptIndex+segPtCount(seg.typ)], seg.typ, startT, stopT, dst)
		return true
	}
	for {
		dashSegTo(c.pts[seg.ptIndex:seg.ptIndex+segPtCount(seg.typ)], seg.typ, startT, 1, dst)
		// Segment::Next: first segment on different points
		pi := seg.ptIndex
		for si++; c.segs[si].ptIndex == pi; si++ {
		}
		seg = c.segs[si]
		startT = 0
		if !(seg.ptIndex < stop.ptIndex) {
			break
		}
	}
	dashSegTo(c.pts[seg.ptIndex:seg.ptIndex+segPtCount(seg.typ)], seg.typ, 0, stopT, dst)
	return true
}

// ---- dash filter --------------------------------------------------------------------------

// dashMaxCount is SkDashPath::kMaxDashCount.
const dashMaxCount = 1000000

// DashParams are the precomputed values of SkDashImpl: the intervals (an even
// count), the phase reduced into the interval length, the length of the first
// dash and its index.
type DashParams struct {
	Intervals     []float32
	Phase         float32
	InitialLen    float32
	InitialIndex  int
	IntervalTotal float32
}

func dashMod(a, b float32) float32 { return float32(math.Mod(float64(a), float64(b))) }

// ValidDashPath is SkDashPath::ValidDashPath.
func validSkDash(phase float32, intervals []float32) bool {
	if len(intervals) < 2 || len(intervals)%2 != 0 {
		return false
	}
	var length float32
	for _, v := range intervals {
		if v < 0 {
			return false
		}
		length += v
	}
	return length > 0 && dashFinite(phase) && dashFinite(length)
}

// NewDashParams is SkDashImpl's constructor with CalcDashParameters; ok is
// false for intervals Skia rejects (negative, odd count after doubling, zero
// sum, non-finite).
func NewDashParams(intervals []float32, phase float32) (DashParams, bool) {
	if !validSkDash(phase, intervals) {
		return DashParams{}, false
	}
	d := DashParams{Intervals: append([]float32(nil), intervals...)}
	var length float32
	for _, v := range intervals {
		length += v
	}
	d.IntervalTotal = length
	if phase < 0 {
		phase = -phase
		if phase > length {
			phase = dashMod(phase, length)
		}
		phase = length - phase
		if phase == length {
			phase = 0
		}
	} else if phase >= length {
		phase = dashMod(phase, length)
	}
	d.Phase = phase
	// find_first_interval
	d.InitialIndex, d.InitialLen = 0, intervals[0]
	ph := phase
	for i, gap := range intervals {
		if ph > gap || (ph == gap && gap != 0) {
			ph -= gap
		} else {
			d.InitialIndex, d.InitialLen = i, gap-ph
			break
		}
	}
	return d, true
}

// DashPath applies the dash effect to src for a stroke of the given width and
// cap (SkDashImpl::onFilterPath with no cull rect). It returns the dashed
// path and whether the stroke was already applied to it (SkDashPath's
// special-line case, which turns a butt-capped straight line into the dash
// rectangles and leaves the paint to fill them). ok is false when Skia leaves
// the path alone (nothing dashable, or more than a million dashes), in which
// case the caller strokes src as it is.
func (d DashParams) DashPath(src *Path, width float32, cap LineCap, resScale float32) (dst *Path, stroked, ok bool) {
	if len(src.Pts) == 0 {
		return NewPath(), false, true
	}
	if resScale <= 0 {
		resScale = 1
	}
	out := &dashBuilder{p: NewPath()}
	// cull_path without a cull rect: only a zero-length line is adjusted
	srcPtr := src
	if len(src.Cmds) == 2 && src.Cmds[0] == 'M' && src.Cmds[1] == 'L' &&
		src.Pts[0] == src.Pts[2] && src.Pts[1] == src.Pts[3] {
		px, py := float32(src.Pts[2]), float32(src.Pts[3])
		px += float32(math.Max(1.001, float64(px))) * (1.0 / (1 << 12))
		srcPtr = NewPath()
		srcPtr.MoveTo(src.Pts[0], src.Pts[1])
		srcPtr.LineTo(float64(px), float64(py))
	}
	// SpecialLineRec
	var (
		special        bool
		lp0, lp1       dpt
		tangent, norm  dpt
		lineLen        float32
		count          = len(d.Intervals)
		intervals      = d.Intervals
		initialLen     = d.InitialLen
		intervalLength = d.IntervalTotal
	)
	if width != 0 && len(srcPtr.Cmds) == 2 && srcPtr.Cmds[0] == 'M' && srcPtr.Cmds[1] == 'L' && cap == CapButt {
		lp0 = dpt{float32(srcPtr.Pts[0]), float32(srcPtr.Pts[1])}
		lp1 = dpt{float32(srcPtr.Pts[2]), float32(srcPtr.Pts[3])}
		lineLen = dashDist(lp0, lp1)
		tangent = lp1.sub(lp0)
		if !tangent.isZero() {
			inv := float32(1) / lineLen
			tangent = dpt{tangent.x * inv, tangent.y * inv}
			if dashFinite(tangent.x) && dashFinite(tangent.y) {
				norm = dpt{tangent.y, -tangent.x} // RotateCCW
				h := width / 2
				norm = dpt{norm.x * h, norm.y * h}
				ptCount := lineLen * float32(count>>1) / intervalLength
				if ptCount > dashMaxCount {
					ptCount = dashMaxCount
				}
				if ptCount == ptCount {
					special = true
				}
			}
		}
	}
	addSpecial := func(d0, d1 float32) {
		if d1 > lineLen {
			d1 = lineLen
		}
		x0 := lp0.x + tangent.x*d0
		x1 := lp0.x + tangent.x*d1
		y0 := lp0.y + tangent.y*d0
		y1 := lp0.y + tangent.y*d1
		out.moveTo(dpt{x0 + norm.x, y0 + norm.y})
		out.lineTo(dpt{x1 + norm.x, y1 + norm.y})
		out.lineTo(dpt{x1 - norm.x, y1 - norm.y})
		out.lineTo(dpt{x0 - norm.x, y0 - norm.y})
	}

	meas := newDashMeasure(srcPtr, false, resScale)
	var dashCount float32
	for {
		skipFirst := meas.isClosed()
		added := false
		length := meas.length()
		index := d.InitialIndex
		dashCount += length * float32(count>>1) / intervalLength
		if dashCount > dashMaxCount {
			return nil, false, false
		}
		var distance float64
		dlen := float64(initialLen)
		for distance < float64(length) {
			added = false
			if index%2 == 0 && !skipFirst {
				added = true
				if special {
					addSpecial(float32(distance), float32(distance+dlen))
				} else {
					meas.getSegment(float32(distance), float32(distance+dlen), out, true)
				}
			}
			distance += dlen
			skipFirst = false
			index++
			if index == count {
				index = 0
			}
			dlen = float64(intervals[index])
		}
		if meas.isClosed() && d.InitialIndex%2 == 0 && initialLen >= 0 {
			meas.getSegment(0, initialLen, out, !added)
		}
		if !meas.next() {
			break
		}
	}
	return out.p, special, true
}

// dashedStrokeMask is SkDraw::drawPath for a stroked, dashed path: the dash
// effect and then the stroker run on the path in its own (user) space with the
// resolution scale of the matrix, and the outline is filled after mapping it
// through m. ok is false (nil mask) when the dash is not valid.
func dashedStrokeMask(p *Path, m Matrix, st StrokeStyle, clip IRect) *Mask {
	dp, ok := NewDashParams(dashF32(st.Dash), float32(st.DashOffset))
	if !ok || st.Width <= 0 {
		return nil
	}
	rs := float32(1)
	sx := math.Hypot(m[0], m[1])
	sy := math.Hypot(m[2], m[3])
	if s := math.Max(sx, sy); s > 0 && !math.IsInf(s, 0) && !math.IsNaN(s) {
		rs = float32(s)
	}
	if st.Width*float64(rs) < 1 {
		return nil // modulated hairline, not ported
	}
	local := p
	if d, stroked, ok := dp.DashPath(p, float32(st.Width), st.Cap, rs); ok {
		local = d
		if stroked {
			return fxFillPath(local.Transform(m), clip)
		}
	}
	ml := st.MiterLimit
	if ml <= 0 {
		ml = 10
	}
	sp := skStrokePath(local, float32(st.Width), st.Join, st.Cap, float32(ml), rs)
	return fxFillPath(sp.toDeviceT([4]float32{float32(m[0]), float32(m[2]), float32(m[1]), float32(m[3])}, float32(m[4]), float32(m[5])), clip)
}

func dashF32(d []float64) []float32 {
	n := len(d)
	if n%2 == 1 {
		n *= 2
	}
	out := make([]float32, n)
	for i := range out {
		out[i] = float32(d[i%len(d)])
	}
	return out
}
