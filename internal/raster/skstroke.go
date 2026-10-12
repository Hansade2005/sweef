package raster

import "math"

// A single-precision port of Skia's SkStroke / SkPathStroker
// (src/core/SkStroke.cpp, SkStrokerPriv.cpp) for lines, quads, conics and
// cubics. Skia strokes with float arithmetic and decides from threshold tests
// whether a quad is close enough to the offset curve or must be split, so the
// outline only matches Chromium's when every operation is done in float32 in
// the same order. Conic arcs (round joins and caps) are kept as conics until
// they are mapped to the device, where Skia's edge builder turns them into
// quads with a 1/4 pixel tolerance (skConicsToQuads).

const skNearlyZero = float32(1.0 / (1 << 12))
const skRoot2Over2 = float32(0.707106781)

func skNearlyZeroF(v float32) bool { return v <= skNearlyZero && v >= -skNearlyZero }

func (a fpt) add(b fpt) fpt { return fpt{a.x + b.x, a.y + b.y} }
func (a fpt) sub(b fpt) fpt { return fpt{a.x - b.x, a.y - b.y} }
func (a fpt) neg() fpt      { return fpt{-a.x, -a.y} }
func (a fpt) dot(b fpt) float32 {
	return a.x*b.x + a.y*b.y
}
func (a fpt) cross(b fpt) float32 { return a.x*b.y - a.y*b.x }
func (a fpt) isZero() bool        { return a.x == 0 && a.y == 0 }
func (a fpt) finite() bool {
	return !(math.IsInf(float64(a.x), 0) || math.IsInf(float64(a.y), 0) || a.x != a.x || a.y != a.y)
}
func skDistSqd(a, b fpt) float32 {
	dx, dy := a.x-b.x, a.y-b.y
	return dx*dx + dy*dy
}
func skLenSqd(a fpt) float32 { return a.x*a.x + a.y*a.y }

// skSetLength is SkPoint::setLength (computed in double, rounded to float).
func skSetLength(p *fpt, length float32) bool {
	xx, yy := float64(p.x), float64(p.y)
	dmag := math.Sqrt(xx*xx + yy*yy)
	dscale := float64(length) / dmag
	x := float32(float64(p.x) * dscale)
	y := float32(float64(p.y) * dscale)
	if math.IsInf(float64(x), 0) || math.IsInf(float64(y), 0) || x != x || y != y || (x == 0 && y == 0) {
		*p = fpt{}
		return false
	}
	*p = fpt{x, y}
	return true
}

// skNormalize is SkPoint::setNormalize on (x, y).
func skNormalize(x, y float32) (fpt, bool) {
	p := fpt{x, y}
	ok := skSetLength(&p, 1)
	return p, ok
}

func skCanNormalize(dx, dy float32) bool {
	return fpt{dx, dy}.finite() && (dx != 0 || dy != 0)
}

func skDegenerate(v fpt) bool { return !skCanNormalize(v.x, v.y) }

func skRotateCCW(p fpt) fpt { return fpt{p.y, -p.x} }
func skRotateCW(p fpt) fpt  { return fpt{-p.y, p.x} }

// ---- path builder -------------------------------------------------------------------------

// skPath is the subset of SkPathBuilder the stroker writes: verbs M L Q K
// (conic, weight in w) C Z.
type skPath struct {
	v []byte
	p []fpt
	w []float32
}

func (s *skPath) empty() bool { return len(s.v) == 0 }
func (s *skPath) reset()      { s.v, s.p, s.w = s.v[:0], s.p[:0], s.w[:0] }
func (s *skPath) ensure() {
	if len(s.v) == 0 {
		s.moveTo(fpt{})
	}
}
func (s *skPath) moveTo(q fpt) {
	s.v = append(s.v, 'M')
	s.p = append(s.p, q)
}
func (s *skPath) lineTo(q fpt) {
	s.ensure()
	s.v = append(s.v, 'L')
	s.p = append(s.p, q)
}
func (s *skPath) quadTo(c, q fpt) {
	s.ensure()
	s.v = append(s.v, 'Q')
	s.p = append(s.p, c, q)
}
func (s *skPath) conicTo(c, q fpt, w float32) {
	s.ensure()
	s.v = append(s.v, 'K')
	s.p = append(s.p, c, q)
	s.w = append(s.w, w)
}
func (s *skPath) cubicTo(c1, c2, q fpt) {
	s.ensure()
	s.v = append(s.v, 'C')
	s.p = append(s.p, c1, c2, q)
}
func (s *skPath) close() {
	if len(s.v) > 0 && s.v[len(s.v)-1] != 'Z' {
		s.v = append(s.v, 'Z')
	}
}
func (s *skPath) lastPt() (fpt, bool) {
	if len(s.p) == 0 {
		return fpt{}, false
	}
	return s.p[len(s.p)-1], true
}
func (s *skPath) setLastPoint(q fpt) {
	if len(s.p) == 0 {
		s.moveTo(q)
		return
	}
	s.p[len(s.p)-1] = q
}

// reversePathTo is SkPathBuilder::privateReversePathTo: the last contour of o
// reversed, without the moveTo.
func (s *skPath) reversePathTo(o *skPath) {
	pi := len(o.p)
	wi := len(o.w)
	for vi := len(o.v) - 1; vi >= 0; vi-- {
		switch o.v[vi] {
		case 'M':
			return
		case 'L':
			pi--
			s.lineTo(o.p[pi-1])
		case 'Q':
			pi -= 2
			s.quadTo(o.p[pi], o.p[pi-1])
		case 'K':
			pi -= 2
			wi--
			s.conicTo(o.p[pi], o.p[pi-1], o.w[wi])
		case 'C':
			pi -= 3
			s.cubicTo(o.p[pi+1], o.p[pi], o.p[pi-1])
		}
	}
}

func (s *skPath) addPath(o *skPath) {
	s.v = append(s.v, o.v...)
	s.p = append(s.p, o.p...)
	s.w = append(s.w, o.w...)
}

func (s *skPath) addCircle(cx, cy, r float32) {
	s.moveTo(fpt{cx + r, cy})
	s.conicTo(fpt{cx + r, cy + r}, fpt{cx, cy + r}, skRoot2Over2)
	s.conicTo(fpt{cx - r, cy + r}, fpt{cx - r, cy}, skRoot2Over2)
	s.conicTo(fpt{cx - r, cy - r}, fpt{cx, cy - r}, skRoot2Over2)
	s.conicTo(fpt{cx + r, cy - r}, fpt{cx + r, cy}, skRoot2Over2)
	s.close()
}

// ---- conics -------------------------------------------------------------------------------

type skConic struct {
	p [3]fpt
	w float32
}

func skBuildUnitArc(uStart, uStop fpt, ccw bool, m [6]float32, dst *[5]skConic) int {
	x := uStart.dot(uStop)
	y := uStart.cross(uStop)
	absY := y
	if absY < 0 {
		absY = -absY
	}
	if absY <= skNearlyZero && x > 0 && ((y >= 0 && !ccw) || (y <= 0 && ccw)) {
		return 0
	}
	if ccw {
		y = -y
	}
	quadrant := 0
	if y == 0 {
		quadrant = 2
	} else if x == 0 {
		if y > 0 {
			quadrant = 1
		} else {
			quadrant = 3
		}
	} else {
		if y < 0 {
			quadrant += 2
		}
		if (x < 0) != (y < 0) {
			quadrant++
		}
	}
	qp := [8]fpt{{1, 0}, {1, 1}, {0, 1}, {-1, 1}, {-1, 0}, {-1, -1}, {0, -1}, {1, -1}}
	n := quadrant
	for i := 0; i < n; i++ {
		dst[i] = skConic{[3]fpt{qp[i*2], qp[i*2+1], qp[(i*2+2)%8]}, skRoot2Over2}
	}
	finalP := fpt{x, y}
	lastQ := qp[quadrant*2]
	dot := lastQ.dot(finalP)
	if dot != dot {
		return 0
	}
	if dot < 1 {
		off := fpt{lastQ.x + x, lastQ.y + y}
		cosHalf := float32(math.Sqrt(float64((1 + dot) / 2)))
		skSetLength(&off, 1/cosHalf)
		if !(skAbs32(lastQ.x-off.x) <= skNearlyZero && skAbs32(lastQ.y-off.y) <= skNearlyZero) {
			dst[n] = skConic{[3]fpt{lastQ, off, finalP}, cosHalf}
			n++
		}
	}
	// matrix: setSinCos(uStart.y, uStart.x), pre-scale y by -1 for ccw, then the user matrix
	sn, cs := uStart.y, uStart.x
	a, b, c, d := cs, sn, -sn, cs // x' = a*x + c*y, y' = b*x + d*y
	if ccw {
		c, d = -c, -d
	}
	// post-concat with the user matrix [ua uc ue; ub ud uf]
	ua, ub, uc, ud, ue, uf := m[0], m[1], m[2], m[3], m[4], m[5]
	na, nb := ua*a+uc*b, ub*a+ud*b
	nc, nd := ua*c+uc*d, ub*c+ud*d
	for i := 0; i < n; i++ {
		for j := 0; j < 3; j++ {
			p := dst[i].p[j]
			dst[i].p[j] = fpt{na*p.x + nc*p.y + ue, nb*p.x + nd*p.y + uf}
		}
	}
	return n
}

func skAbs32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

func skBetween(a, b, c float32) bool { return (a-b)*(c-b) <= 0 }

func (c skConic) chop() (l, r skConic) {
	scale := float32(1) / (1 + c.w)
	t0 := fpt{c.p[0].x * scale, c.p[0].y * scale}
	ws := c.w * scale
	t1 := fpt{c.p[1].x * ws, c.p[1].y * ws}
	t2 := fpt{c.p[2].x * scale, c.p[2].y * scale}
	p1 := fpt{t0.x + t1.x, t0.y + t1.y}
	p3 := fpt{t1.x + t2.x, t1.y + t2.y}
	p2 := fpt{0.5*t0.x + t1.x + 0.5*t2.x, 0.5*t0.y + t1.y + 0.5*t2.y}
	nw := float32(math.Sqrt(float64(0.5 + c.w*0.5)))
	l = skConic{[3]fpt{c.p[0], p1, p2}, nw}
	r = skConic{[3]fpt{p2, p3, c.p[2]}, nw}
	return
}

func skConicSubdivide(src skConic, out []fpt, level int) []fpt {
	if level == 0 {
		return append(out, src.p[1], src.p[2])
	}
	d0, d1 := src.chop()
	startY, endY := src.p[0].y, src.p[2].y
	if skBetween(startY, src.p[1].y, endY) {
		midY := d0.p[2].y
		if !skBetween(startY, midY, endY) {
			closer := endY
			if skAbs32(midY-startY) < skAbs32(midY-endY) {
				closer = startY
			}
			d0.p[2].y, d1.p[0].y = closer, closer
		}
		if !skBetween(startY, d0.p[1].y, d0.p[2].y) {
			d0.p[1].y = startY
		}
		if !skBetween(d1.p[0].y, d1.p[1].y, endY) {
			d1.p[1].y = endY
		}
	}
	out = skConicSubdivide(d0, out, level-1)
	return skConicSubdivide(d1, out, level-1)
}

// skConicQuads is SkAutoConicToQuads::computeQuads: the quad points
// (2n+1 of them) for a conic within tol.
func skConicQuads(c skConic, tol float32) []fpt {
	pow2 := 0
	if tol >= 0 && c.p[0].finite() && c.p[1].finite() && c.p[2].finite() && c.w >= 0 && !math.IsInf(float64(c.w), 0) && c.w == c.w {
		a := c.w - 1
		k := a / (4 * (2 + a))
		x := k * (c.p[0].x - 2*c.p[1].x + c.p[2].x)
		y := k * (c.p[0].y - 2*c.p[1].y + c.p[2].y)
		err := float32(math.Sqrt(float64(x*x + y*y)))
		for pow2 = 0; pow2 < 5; pow2++ {
			if err <= tol {
				break
			}
			err *= 0.25
		}
	} else {
		pow2 = 0
	}
	if !(c.w >= 0) || math.IsInf(float64(c.w), 0) {
		pow2 = 0
	}
	pts := []fpt{c.p[0]}
	if pow2 == 5 {
		d0, d1 := c.chop()
		if skAbs32(d0.p[1].x-d0.p[2].x) <= skNearlyZero && skAbs32(d0.p[1].y-d0.p[2].y) <= skNearlyZero &&
			skAbs32(d1.p[0].x-d1.p[1].x) <= skNearlyZero && skAbs32(d1.p[0].y-d1.p[1].y) <= skNearlyZero {
			return []fpt{c.p[0], d0.p[1], d0.p[1], d0.p[1], d1.p[2]}
		}
	}
	pts = skConicSubdivide(c, pts, pow2)
	for _, p := range pts {
		if !p.finite() {
			for i := 1; i < len(pts)-1; i++ {
				pts[i] = c.p[1]
			}
			break
		}
	}
	return pts
}

// toDevice maps the outline through the 2x2 matrix m (float32, x' = m0*x +
// m1*y, y' = m2*x + m3*y: the layout of xfStrokeOutline) and returns it as a
// Path with every conic turned into quads at device scale.
func (s *skPath) toDevice(m [4]float32) *Path {
	return s.toDeviceT(m, 0, 0)
}

// toDeviceT is toDevice with a translation (x' = m0*x + m1*y + tx).
func (s *skPath) toDeviceT(m [4]float32, tx, ty float32) *Path {
	mp := func(q fpt) fpt { return fpt{m[0]*q.x + m[1]*q.y + tx, m[2]*q.x + m[3]*q.y + ty} }
	out := NewPath()
	pi, wi := 0, 0
	for _, v := range s.v {
		switch v {
		case 'M':
			q := mp(s.p[pi])
			out.MoveTo(float64(q.x), float64(q.y))
			pi++
		case 'L':
			q := mp(s.p[pi])
			out.LineTo(float64(q.x), float64(q.y))
			pi++
		case 'Q':
			c, q := mp(s.p[pi]), mp(s.p[pi+1])
			out.QuadTo(float64(c.x), float64(c.y), float64(q.x), float64(q.y))
			pi += 2
		case 'C':
			c1, c2, q := mp(s.p[pi]), mp(s.p[pi+1]), mp(s.p[pi+2])
			out.CubicTo(float64(c1.x), float64(c1.y), float64(c2.x), float64(c2.y), float64(q.x), float64(q.y))
			pi += 3
		case 'K':
			// the conic starts at the last point
			var start fpt
			if len(out.Pts) >= 2 {
				start = fpt{float32(out.Pts[len(out.Pts)-2]), float32(out.Pts[len(out.Pts)-1])}
			}
			cc := skConic{[3]fpt{start, mp(s.p[pi]), mp(s.p[pi+1])}, s.w[wi]}
			qs := skConicQuads(cc, 0.25)
			for i := 1; i+1 < len(qs); i += 2 {
				out.QuadTo(float64(qs[i].x), float64(qs[i].y), float64(qs[i+1].x), float64(qs[i+1].y))
			}
			pi += 2
			wi++
		case 'Z':
			out.Close()
		}
	}
	return out
}

// ---- joins and caps -----------------------------------------------------------------------

type skAngle int

const (
	skNearly180 skAngle = iota
	skSharp
	skShallow
	skNearlyLine
)

func skDot2Angle(dot float32) skAngle {
	if dot >= 0 {
		if skNearlyZeroF(1 - dot) {
			return skNearlyLine
		}
		return skShallow
	}
	if skNearlyZeroF(1 + dot) {
		return skNearly180
	}
	return skSharp
}

func skIsClockwise(before, after fpt) bool { return before.x*after.y > before.y*after.x }

func skHandleInnerJoin(inner *skPath, pivot, after fpt) {
	inner.lineTo(pivot)
	inner.lineTo(fpt{pivot.x - after.x, pivot.y - after.y})
}

type skStroker struct {
	radius, invMiterLimit, resScale, invResScale, invResScaleSq float32
	join                                                        LineJoin
	cap                                                         LineCap

	firstNormal, prevNormal, firstUnitNormal, prevUnitNormal fpt
	firstPt, prevPt, firstOuterPt                            fpt
	firstOuterPtIndex                                        int
	segmentCount                                             int
	prevIsLine, joinCompleted                                bool

	outer, inner, cusper skPath
	strokeOuter          bool
	recursion            int
	foundTangents        bool
	canIgnoreCenter      bool
}

func newSkStroker(radius, miterLimit float32, cap LineCap, join LineJoin, resScale float32) *skStroker {
	s := &skStroker{radius: radius, resScale: resScale, cap: cap, join: join, segmentCount: -1}
	if join == JoinMiter {
		if miterLimit <= 1 {
			s.join = JoinBevel
		} else {
			s.invMiterLimit = 1 / miterLimit
		}
	}
	s.invResScale = 1 / (resScale * 4)
	s.invResScaleSq = s.invResScale * s.invResScale
	return s
}

func (k *skStroker) joiner(outer, inner *skPath, beforeUnit, pivot, afterUnit fpt, prevIsLine, currIsLine bool) {
	switch k.join {
	case JoinRound:
		k.roundJoin(outer, inner, beforeUnit, pivot, afterUnit)
	case JoinBevel:
		k.bluntJoin(outer, inner, beforeUnit, pivot, afterUnit)
	default:
		k.miterJoin(outer, inner, beforeUnit, pivot, afterUnit, prevIsLine, currIsLine)
	}
}

func (k *skStroker) bluntJoin(outer, inner *skPath, beforeUnit, pivot, afterUnit fpt) {
	after := fpt{afterUnit.x * k.radius, afterUnit.y * k.radius}
	if !skIsClockwise(beforeUnit, afterUnit) {
		outer, inner = inner, outer
		after = after.neg()
	}
	outer.lineTo(fpt{pivot.x + after.x, pivot.y + after.y})
	skHandleInnerJoin(inner, pivot, after)
}

func (k *skStroker) roundJoin(outer, inner *skPath, beforeUnit, pivot, afterUnit fpt) {
	dot := beforeUnit.dot(afterUnit)
	if skDot2Angle(dot) == skNearlyLine {
		return
	}
	before, after := beforeUnit, afterUnit
	ccw := false
	if !skIsClockwise(before, after) {
		outer, inner = inner, outer
		before, after = before.neg(), after.neg()
		ccw = true
	}
	var conics [5]skConic
	m := [6]float32{k.radius, 0, 0, k.radius, pivot.x, pivot.y}
	n := skBuildUnitArc(before, after, ccw, m, &conics)
	if n > 0 {
		for i := 0; i < n; i++ {
			outer.conicTo(conics[i].p[1], conics[i].p[2], conics[i].w)
		}
		after = fpt{after.x * k.radius, after.y * k.radius}
		skHandleInnerJoin(inner, pivot, after)
	}
}

func (k *skStroker) miterJoin(outer, inner *skPath, beforeUnit, pivot, afterUnit fpt, prevIsLine, currIsLine bool) {
	dot := beforeUnit.dot(afterUnit)
	at := skDot2Angle(dot)
	before, after := beforeUnit, afterUnit
	var mid fpt
	blunt := func() {
		after = fpt{after.x * k.radius, after.y * k.radius}
		if !currIsLine {
			outer.lineTo(fpt{pivot.x + after.x, pivot.y + after.y})
		}
		skHandleInnerJoin(inner, pivot, after)
	}
	miter := func() {
		if prevIsLine {
			outer.setLastPoint(fpt{pivot.x + mid.x, pivot.y + mid.y})
		} else {
			outer.lineTo(fpt{pivot.x + mid.x, pivot.y + mid.y})
		}
		blunt()
	}
	if at == skNearlyLine {
		return
	}
	if at == skNearly180 {
		currIsLine = false
		blunt()
		return
	}
	ccw := !skIsClockwise(before, after)
	if ccw {
		outer, inner = inner, outer
		before, after = before.neg(), after.neg()
	}
	if dot == 0 && k.invMiterLimit <= 0.707106781 {
		mid = fpt{(before.x + after.x) * k.radius, (before.y + after.y) * k.radius}
		miter()
		return
	}
	sinHalf := float32(math.Sqrt(float64((1 + dot) / 2)))
	if sinHalf < k.invMiterLimit {
		currIsLine = false
		blunt()
		return
	}
	if at == skSharp {
		mid = fpt{after.y - before.y, before.x - after.x}
		if ccw {
			mid = mid.neg()
		}
	} else {
		mid = fpt{before.x + after.x, before.y + after.y}
	}
	skSetLength(&mid, k.radius/sinHalf)
	miter()
}

func (k *skStroker) capper(path *skPath, pivot, normal, stop fpt, extendLast bool) {
	switch k.cap {
	case CapRound:
		parallel := skRotateCW(normal)
		pc := fpt{pivot.x + parallel.x, pivot.y + parallel.y}
		path.conicTo(fpt{pc.x + normal.x, pc.y + normal.y}, pc, skRoot2Over2)
		path.conicTo(fpt{pc.x - normal.x, pc.y - normal.y}, stop, skRoot2Over2)
	case CapSquare:
		parallel := skRotateCW(normal)
		if extendLast {
			path.setLastPoint(fpt{pivot.x + normal.x + parallel.x, pivot.y + normal.y + parallel.y})
			path.lineTo(fpt{pivot.x - normal.x + parallel.x, pivot.y - normal.y + parallel.y})
		} else {
			path.lineTo(fpt{pivot.x + normal.x + parallel.x, pivot.y + normal.y + parallel.y})
			path.lineTo(fpt{pivot.x - normal.x + parallel.x, pivot.y - normal.y + parallel.y})
			path.lineTo(stop)
		}
	default:
		path.lineTo(stop)
	}
}

// ---- contour handling -----------------------------------------------------------------------

func skSetNormal(before, after fpt, scale, radius float32, normal, unit *fpt) bool {
	u, ok := skNormalize((after.x-before.x)*scale, (after.y-before.y)*scale)
	if !ok {
		return false
	}
	u = skRotateCCW(u)
	*unit = u
	*normal = fpt{u.x * radius, u.y * radius}
	return true
}

func skSetNormalVec(v fpt, radius float32, normal, unit *fpt) bool {
	u, ok := skNormalize(v.x, v.y)
	if !ok {
		return false
	}
	u = skRotateCCW(u)
	*unit = u
	*normal = fpt{u.x * radius, u.y * radius}
	return true
}

func (k *skStroker) preJoinTo(cur fpt, normal, unit *fpt, currIsLine bool) bool {
	if !skSetNormal(k.prevPt, cur, k.resScale, k.radius, normal, unit) {
		if k.cap == CapButt {
			return false
		}
		*normal, *unit = fpt{k.radius, 0}, fpt{1, 0}
	}
	if k.segmentCount == 0 {
		k.firstNormal, k.firstUnitNormal = *normal, *unit
		k.firstOuterPt = fpt{k.prevPt.x + normal.x, k.prevPt.y + normal.y}
		k.outer.moveTo(k.firstOuterPt)
		k.inner.moveTo(fpt{k.prevPt.x - normal.x, k.prevPt.y - normal.y})
	} else {
		k.joiner(&k.outer, &k.inner, k.prevUnitNormal, k.prevPt, *unit, k.prevIsLine, currIsLine)
	}
	k.prevIsLine = currIsLine
	return true
}

func (k *skStroker) postJoinTo(cur, normal, unit fpt) {
	k.joinCompleted = true
	k.prevPt, k.prevUnitNormal, k.prevNormal = cur, unit, normal
	k.segmentCount++
}

func (k *skStroker) finishContour(closed, currIsLine bool) {
	if k.segmentCount > 0 {
		if closed {
			k.joiner(&k.outer, &k.inner, k.prevUnitNormal, k.prevPt, k.firstUnitNormal, k.prevIsLine, currIsLine)
			k.outer.close()
			if pt, ok := k.inner.lastPt(); ok {
				k.outer.moveTo(pt)
				k.outer.reversePathTo(&k.inner)
				k.outer.close()
			}
		} else {
			if pt, ok := k.inner.lastPt(); ok {
				k.capper(&k.outer, k.prevPt, k.prevNormal, pt, currIsLine)
				k.outer.reversePathTo(&k.inner)
				k.capper(&k.outer, k.firstPt, k.firstNormal.neg(), k.firstOuterPt, k.prevIsLine)
				k.outer.close()
			}
		}
		if !k.cusper.empty() {
			k.outer.addPath(&k.cusper)
			k.cusper.reset()
		}
	}
	k.inner.reset()
	k.segmentCount = -1
	k.firstOuterPtIndex = len(k.outer.p)
}

func (k *skStroker) moveTo(p fpt) {
	if k.segmentCount > 0 {
		k.finishContour(false, false)
	}
	k.segmentCount = 0
	k.firstPt, k.prevPt = p, p
	k.joinCompleted = false
}

func (k *skStroker) lineTo(cur fpt, hasValidTangent func() bool) {
	tol := skNearlyZero * k.invResScale
	teeny := skAbs32(k.prevPt.x-cur.x) <= tol && skAbs32(k.prevPt.y-cur.y) <= tol
	if k.cap == CapButt && teeny {
		return
	}
	if teeny && (k.joinCompleted || (hasValidTangent != nil && hasValidTangent())) {
		return
	}
	var normal, unit fpt
	if !k.preJoinTo(cur, &normal, &unit, true) {
		return
	}
	k.outer.lineTo(fpt{cur.x + normal.x, cur.y + normal.y})
	k.inner.lineTo(fpt{cur.x - normal.x, cur.y - normal.y})
	k.postJoinTo(cur, normal, unit)
}

// ---- quads ----------------------------------------------------------------------------------

func skPtToLine(pt, a, b fpt) float32 {
	dxy := b.sub(a)
	ab0 := pt.sub(a)
	numer, denom := dxy.dot(ab0), dxy.dot(dxy)
	t := numer / denom
	if t >= 0 && t <= 1 {
		hit := fpt{a.x*(1-t) + b.x*t, a.y*(1-t) + b.y*t}
		return skDistSqd(hit, pt)
	}
	return skDistSqd(pt, a)
}

func skPtToTangentLine(pt, start, tangent fpt) float32 {
	ab0 := pt.sub(start)
	numer, denom := tangent.dot(ab0), tangent.dot(tangent)
	t := numer / denom
	if t >= 0 && t <= 1 {
		hit := fpt{start.x + tangent.x*t, start.y + tangent.y*t}
		return skDistSqd(hit, pt)
	}
	return skDistSqd(pt, start)
}

func skQuadInLine(q [3]fpt) bool {
	ptMax := float32(-1)
	var o1, o2 int
	for i := 0; i < 2; i++ {
		for j := i + 1; j < 3; j++ {
			d := q[j].sub(q[i])
			m := skMax32(skAbs32(d.x), skAbs32(d.y))
			if ptMax < m {
				o1, o2, ptMax = i, j, m
			}
		}
	}
	mid := o1 ^ o2 ^ 3
	const slop = float32(0.000005)
	return skPtToLine(q[mid], q[o1], q[o2]) <= ptMax*ptMax*slop
}

func skMax32(a, b float32) float32 {
	if a < b {
		return b
	}
	return a
}
func skMin32(a, b float32) float32 {
	if b < a {
		return b
	}
	return a
}

func skCubicInLine(c [4]fpt) bool {
	ptMax := float32(-1)
	var o1, o2 int
	for i := 0; i < 3; i++ {
		for j := i + 1; j < 4; j++ {
			d := c[j].sub(c[i])
			m := skMax32(skAbs32(d.x), skAbs32(d.y))
			if ptMax < m {
				o1, o2, ptMax = i, j, m
			}
		}
	}
	mid1 := (1 + (2 >> o2)) >> o1
	mid2 := o1 ^ o2 ^ mid1
	slop := ptMax * ptMax * 0.00001
	return skPtToLine(c[mid1], c[o1], c[o2]) <= slop && skPtToLine(c[mid2], c[o1], c[o2]) <= slop
}

func skEvalQuad(q []fpt, t float32) fpt {
	ax := q[2].x - 2*q[1].x + q[0].x
	ay := q[2].y - 2*q[1].y + q[0].y
	bx := 2 * (q[1].x - q[0].x)
	by := 2 * (q[1].y - q[0].y)
	return fpt{(ax*t+bx)*t + q[0].x, (ay*t+by)*t + q[0].y}
}

func skEvalQuadTangent(q [3]fpt, t float32) fpt {
	if (t == 0 && q[0] == q[1]) || (t == 1 && q[1] == q[2]) {
		return q[2].sub(q[0])
	}
	b := q[1].sub(q[0])
	a := fpt{q[2].x - q[1].x - b.x, q[2].y - q[1].y - b.y}
	tt := fpt{a.x*t + b.x, a.y*t + b.y}
	return fpt{tt.x + tt.x, tt.y + tt.y}
}

func skQuadMaxCurvature(q [3]fpt) float32 {
	ax, ay := q[1].x-q[0].x, q[1].y-q[0].y
	bx := q[0].x - q[1].x - q[1].x + q[2].x
	by := q[0].y - q[1].y - q[1].y + q[2].y
	numer := -(ax*bx + ay*by)
	denom := bx*bx + by*by
	if denom < 0 {
		numer, denom = -numer, -denom
	}
	if numer <= 0 {
		return 0
	}
	if numer >= denom {
		return 1
	}
	return numer / denom
}

type skRed int

const (
	skPointRed skRed = iota
	skLineRed
	skQuadRed
	skDegenRed
	skDegen2Red
	skDegen3Red
)

func skCheckQuadLinear(q [3]fpt) (skRed, fpt) {
	dAB, dBC := skDegenerate(q[1].sub(q[0])), skDegenerate(q[2].sub(q[1]))
	if dAB && dBC {
		return skPointRed, fpt{}
	}
	if dAB || dBC {
		return skLineRed, fpt{}
	}
	if !skQuadInLine(q) {
		return skQuadRed, fpt{}
	}
	t := skQuadMaxCurvature(q)
	if t == 0 || t == 1 {
		return skLineRed, fpt{}
	}
	return skDegenRed, skEvalQuad(q[:], t)
}

func (k *skStroker) lineToSimple(p fpt) { k.lineTo(p, nil) }

func (k *skStroker) degenerateLines(pts ...fpt) {
	k.lineTo(pts[0], nil)
	save := k.join
	k.join = JoinRound
	for _, p := range pts[1:] {
		k.lineTo(p, nil)
	}
	k.join = save
}

type skQuadConstruct struct {
	quad                     [3]fpt
	tangentStart, tangentEnd fpt
	startT, midT, endT       float32
	startSet, endSet         bool
	opposite                 bool
}

func (q *skQuadConstruct) init(start, end float32) bool {
	q.startT, q.midT, q.endT = start, (start+end)*0.5, end
	q.startSet, q.endSet = false, false
	return q.startT < q.midT && q.midT < q.endT
}
func (q *skQuadConstruct) initWithStart(parent *skQuadConstruct) bool {
	if !q.init(parent.startT, parent.midT) {
		return false
	}
	q.quad[0] = parent.quad[0]
	q.tangentStart = parent.tangentStart
	q.startSet = true
	return true
}
func (q *skQuadConstruct) initWithEnd(parent *skQuadConstruct) bool {
	if !q.init(parent.midT, parent.endT) {
		return false
	}
	q.quad[2] = parent.quad[2]
	q.tangentEnd = parent.tangentEnd
	q.endSet = true
	return true
}

type skResult int

const (
	skSplit skResult = iota
	skDegenRes
	skQuadRes
)

func (k *skStroker) setRayPts(tPt fpt, dxy *fpt, onPt, tangent *fpt) {
	if !skSetLength(dxy, k.radius) {
		*dxy = fpt{k.radius, 0}
	}
	flip := float32(-1)
	if k.strokeOuter {
		flip = 1
	}
	*onPt = fpt{tPt.x + flip*dxy.y, tPt.y - flip*dxy.x}
	if tangent != nil {
		*tangent = *dxy
	}
}

func (k *skStroker) quadPerpRay(quad [3]fpt, t float32, tPt, onPt, tangent *fpt) {
	*tPt = skEvalQuad(quad[:], t)
	dxy := skEvalQuadTangent(quad, t)
	if dxy.isZero() {
		dxy = quad[2].sub(quad[0])
	}
	k.setRayPts(*tPt, &dxy, onPt, tangent)
}

func skEvalCubic(c []fpt, t float32) fpt {
	ax := c[3].x + 3*(c[1].x-c[2].x) - c[0].x
	ay := c[3].y + 3*(c[1].y-c[2].y) - c[0].y
	bx := 3 * (c[2].x - 2*c[1].x + c[0].x)
	by := 3 * (c[2].y - 2*c[1].y + c[0].y)
	cx := 3 * (c[1].x - c[0].x)
	cy := 3 * (c[1].y - c[0].y)
	return fpt{((ax*t+bx)*t+cx)*t + c[0].x, ((ay*t+by)*t+cy)*t + c[0].y}
}

func skCubicDeriv(c []fpt, t float32) fpt {
	ax := c[3].x + 3*(c[1].x-c[2].x) - c[0].x
	ay := c[3].y + 3*(c[1].y-c[2].y) - c[0].y
	bx := 2 * (c[2].x - 2*c[1].x + c[0].x)
	by := 2 * (c[2].y - 2*c[1].y + c[0].y)
	cx := c[1].x - c[0].x
	cy := c[1].y - c[0].y
	return fpt{(ax*t+bx)*t + cx, (ay*t+by)*t + cy}
}

func (k *skStroker) cubicPerpRay(cubic [4]fpt, t float32, tPt, onPt, tangent *fpt) {
	*tPt = skEvalCubic(cubic[:], t)
	var dxy fpt
	if (t == 0 && cubic[0] == cubic[1]) || (t == 1 && cubic[2] == cubic[3]) {
		if t == 0 {
			dxy = cubic[2].sub(cubic[0])
		} else {
			dxy = cubic[3].sub(cubic[1])
		}
		if dxy.isZero() {
			dxy = cubic[3].sub(cubic[0])
		}
	} else {
		dxy = skCubicDeriv(cubic[:], t)
	}
	if dxy.isZero() {
		cPts := cubic
		if skNearlyZeroF(t) {
			dxy = cubic[2].sub(cubic[0])
		} else if skNearlyZeroF(1 - t) {
			dxy = cubic[3].sub(cubic[1])
		} else {
			ch := dashChopCubicAtF(cubic, t)
			dxy = ch[3].sub(ch[2])
			if dxy.isZero() {
				dxy = ch[3].sub(ch[1])
				cPts = [4]fpt{ch[0], ch[1], ch[2], ch[3]}
			}
		}
		if dxy.isZero() {
			dxy = cPts[3].sub(cPts[0])
		}
	}
	k.setRayPts(*tPt, &dxy, onPt, tangent)
}

// dashChopCubicAtF is SkChopCubicAt on fpt.
func dashChopCubicAtF(c [4]fpt, t float32) [7]fpt {
	d := dashChopCubicAt([]dpt{{c[0].x, c[0].y}, {c[1].x, c[1].y}, {c[2].x, c[2].y}, {c[3].x, c[3].y}}, t)
	var o [7]fpt
	for i := range o {
		o[i] = fpt{d[i].x, d[i].y}
	}
	return o
}

func (k *skStroker) intersectRay(q *skQuadConstruct, ctrl bool) skResult {
	start, end := q.quad[0], q.quad[2]
	aLen, bLen := q.tangentStart, q.tangentEnd
	denom := aLen.cross(bLen)
	if denom == 0 || math.IsInf(float64(denom), 0) || denom != denom {
		q.opposite = aLen.dot(bLen) < 0
		return skDegenRes
	}
	q.opposite = false
	ab0 := start.sub(end)
	numerA := bLen.cross(ab0)
	numerB := aLen.cross(ab0)
	if (numerA >= 0) == (numerB >= 0) {
		d1 := skPtToTangentLine(start, end, q.tangentEnd)
		d2 := skPtToTangentLine(end, start, q.tangentStart)
		if skMax32(d1, d2) <= k.invResScaleSq {
			return skDegenRes
		}
		return skSplit
	}
	numerA /= denom
	if numerA > numerA-1 {
		if ctrl {
			q.quad[1] = fpt{start.x + q.tangentStart.x*numerA, start.y + q.tangentStart.y*numerA}
		}
		return skQuadRes
	}
	q.opposite = aLen.dot(bLen) < 0
	return skDegenRes
}

func skSharpAngle(q [3]fpt) bool {
	smaller, larger := q[1].sub(q[0]), q[1].sub(q[2])
	sl, ll := skLenSqd(smaller), skLenSqd(larger)
	if sl > ll {
		smaller, larger = larger, smaller
		ll = sl
	}
	if !skSetLength(&smaller, ll) {
		return false
	}
	return smaller.dot(larger) > 0
}

func skWithin(a, b fpt, limit float32) bool { return skDistSqd(a, b) <= limit*limit }

func (k *skStroker) ptInQuadBounds(q [3]fpt, pt fpt) bool {
	inv := k.invResScale
	xMin := skMin32(skMin32(q[0].x, q[1].x), q[2].x)
	if pt.x+inv < xMin {
		return false
	}
	xMax := skMax32(skMax32(q[0].x, q[1].x), q[2].x)
	if pt.x-inv > xMax {
		return false
	}
	yMin := skMin32(skMin32(q[0].y, q[1].y), q[2].y)
	if pt.y+inv < yMin {
		return false
	}
	yMax := skMax32(skMax32(q[0].y, q[1].y), q[2].y)
	return !(pt.y-inv > yMax)
}

func skIntersectQuadRay(line [2]fpt, quad [3]fpt) []float32 {
	vec := line[1].sub(line[0])
	var r [3]float32
	for n := 0; n < 3; n++ {
		r[n] = vec.cross(quad[n].sub(line[0]))
	}
	A, B, C := r[2], r[1], r[0]
	A += C - 2*B
	B -= C
	return fxFindUnitQuadRoots(A, 2*B, C)
}

func (k *skStroker) strokeCloseEnough(stroke [3]fpt, ray [2]fpt, q *skQuadConstruct) skResult {
	strokeMid := skEvalQuad(stroke[:], 0.5)
	if skWithin(ray[0], strokeMid, k.invResScale) {
		if skSharpAngle(q.quad) {
			return skSplit
		}
		return skQuadRes
	}
	if !k.ptInQuadBounds(stroke, ray[0]) {
		return skSplit
	}
	roots := skIntersectQuadRay(ray, stroke)
	if len(roots) != 1 {
		return skSplit
	}
	quadPt := skEvalQuad(stroke[:], roots[0])
	err := k.invResScale * (1 - skAbs32(roots[0]-0.5)*2)
	if skWithin(ray[0], quadPt, err) {
		if skSharpAngle(q.quad) {
			return skSplit
		}
		return skQuadRes
	}
	return skSplit
}

func (k *skStroker) compareQuadQuad(quad [3]fpt, q *skQuadConstruct) skResult {
	if !q.startSet {
		var p fpt
		k.quadPerpRay(quad, q.startT, &p, &q.quad[0], &q.tangentStart)
		q.startSet = true
	}
	if !q.endSet {
		var p fpt
		k.quadPerpRay(quad, q.endT, &p, &q.quad[2], &q.tangentEnd)
		q.endSet = true
	}
	if r := k.intersectRay(q, true); r != skQuadRes {
		return r
	}
	var ray [2]fpt
	k.quadPerpRay(quad, q.midT, &ray[1], &ray[0], nil)
	return k.strokeCloseEnough(q.quad, ray, q)
}

func (k *skStroker) sink() *skPath {
	if k.strokeOuter {
		return &k.outer
	}
	return &k.inner
}

func (k *skStroker) addDegenerateLine(q *skQuadConstruct) { k.sink().lineTo(q.quad[2]) }

const (
	skRecQuad  = 16
	skRecCubic = 24
	skRecTan   = 5 * 3
)

func (k *skStroker) quadStroke(quad [3]fpt, q *skQuadConstruct) bool {
	switch k.compareQuadQuad(quad, q) {
	case skQuadRes:
		k.sink().quadTo(q.quad[1], q.quad[2])
		return true
	case skDegenRes:
		k.addDegenerateLine(q)
		return true
	}
	if !q.quad[2].finite() {
		return false
	}
	k.recursion++
	if k.recursion > skRecQuad {
		k.addDegenerateLine(q)
		k.recursion--
		return true
	}
	var half skQuadConstruct
	if !half.initWithStart(q) {
		k.addDegenerateLine(q)
		k.recursion--
		return true
	}
	if !k.quadStroke(quad, &half) {
		return false
	}
	if !half.initWithEnd(q) {
		k.addDegenerateLine(q)
		k.recursion--
		return true
	}
	if !k.quadStroke(quad, &half) {
		return false
	}
	k.recursion--
	return true
}

func (k *skStroker) quadTo(p1, p2 fpt) {
	quad := [3]fpt{k.prevPt, p1, p2}
	red, reduction := skCheckQuadLinear(quad)
	switch red {
	case skPointRed, skLineRed:
		k.lineTo(p2, nil)
		return
	case skDegenRed:
		k.degenerateLines(reduction, p2)
		return
	}
	var normalAB, unitAB, normalBC, unitBC fpt
	if !k.preJoinTo(p1, &normalAB, &unitAB, false) {
		k.lineTo(p2, nil)
		return
	}
	var qc skQuadConstruct
	for _, outer := range []bool{true, false} {
		k.strokeOuter = outer
		k.foundTangents = false
		k.recursion = 0
		qc = skQuadConstruct{}
		qc.init(0, 1)
		k.quadStroke(quad, &qc)
	}
	if !skSetNormal(quad[1], quad[2], k.resScale, k.radius, &normalBC, &unitBC) {
		normalBC, unitBC = normalAB, unitAB
	}
	k.postJoinTo(p2, normalBC, unitBC)
}

// ---- cubics ---------------------------------------------------------------------------------

func skSolveCubicPoly(c [4]float32) []float32 {
	if skNearlyZeroF(c[0]) {
		return fxFindUnitQuadRoots(c[1], c[2], c[3])
	}
	inva := 1 / c[0]
	a, b, cc := c[1]*inva, c[2]*inva, c[3]*inva
	Q := (a*a - b*3) / 9
	R := (2*a*a*a - 9*a*b + 27*cc) / 54
	Q3 := Q * Q * Q
	R2Q3 := R*R - Q3
	adiv3 := a / 3
	pin := func(v float32) float32 {
		if v < 0 {
			return 0
		}
		if v > 1 {
			return 1
		}
		return v
	}
	if R2Q3 < 0 {
		rq := R / float32(math.Sqrt(float64(Q3)))
		if rq < -1 {
			rq = -1
		} else if rq > 1 {
			rq = 1
		}
		theta := float32(math.Acos(float64(rq)))
		neg2RootQ := -2 * float32(math.Sqrt(float64(Q)))
		const pi = float32(math.Pi)
		t := [3]float32{
			pin(neg2RootQ*float32(math.Cos(float64(theta/3))) - adiv3),
			pin(neg2RootQ*float32(math.Cos(float64((theta+2*pi)/3))) - adiv3),
			pin(neg2RootQ*float32(math.Cos(float64((theta-2*pi)/3))) - adiv3),
		}
		// bubble sort, then collapse duplicates
		for i := 0; i < 3; i++ {
			for j := i + 1; j < 3; j++ {
				if t[j] < t[i] {
					t[i], t[j] = t[j], t[i]
				}
			}
		}
		out := []float32{t[0]}
		for i := 1; i < 3; i++ {
			if t[i] != out[len(out)-1] {
				out = append(out, t[i])
			}
		}
		return out
	}
	A := skAbs32(R) + float32(math.Sqrt(float64(R2Q3)))
	A = float32(math.Pow(float64(A), float64(float32(0.3333333))))
	if R > 0 {
		A = -A
	}
	if A != 0 {
		A += Q / A
	}
	return []float32{pin(A - adiv3)}
}

func skCubicMaxCurvature(c [4]fpt) []float32 {
	f := func(a0, a1, a2, a3 float32) [4]float32 {
		a := a1 - a0
		b := a2 - 2*a1 + a0
		cc := a3 + 3*(a1-a2) - a0
		return [4]float32{cc * cc, 3 * b * cc, 2*b*b + cc*a, a * b}
	}
	cx := f(c[0].x, c[1].x, c[2].x, c[3].x)
	cy := f(c[0].y, c[1].y, c[2].y, c[3].y)
	for i := range cx {
		cx[i] += cy[i]
	}
	return skSolveCubicPoly(cx)
}

func skCheckCubicLinear(c [4]fpt) (skRed, [3]fpt, int) {
	dAB, dBC, dCD := skDegenerate(c[1].sub(c[0])), skDegenerate(c[2].sub(c[1])), skDegenerate(c[3].sub(c[2]))
	if dAB && dBC && dCD {
		return skPointRed, [3]fpt{}, 0
	}
	n := 0
	for _, b := range []bool{dAB, dBC, dCD} {
		if b {
			n++
		}
	}
	if n == 2 {
		return skLineRed, [3]fpt{}, 0
	}
	if !skCubicInLine(c) {
		tp := 1
		if dAB {
			tp = 2
		}
		return skQuadRed, [3]fpt{}, tp
	}
	var red [3]fpt
	rc := 0
	for _, t := range skCubicMaxCurvature(c) {
		if 0 >= t || t >= 1 {
			continue
		}
		p := skEvalCubic(c[:], t)
		if p != c[0] && p != c[3] {
			red[rc] = p
			rc++
		}
	}
	if rc == 0 {
		return skLineRed, red, 0
	}
	return skRed(int(skQuadRed) + rc), red, 0
}

func skFindCubicInflections(c [4]fpt) []float32 {
	Ax, Ay := c[1].x-c[0].x, c[1].y-c[0].y
	Bx, By := c[2].x-2*c[1].x+c[0].x, c[2].y-2*c[1].y+c[0].y
	Cx, Cy := c[3].x+3*(c[1].x-c[2].x)-c[0].x, c[3].y+3*(c[1].y-c[2].y)-c[0].y
	return fxFindUnitQuadRoots(Bx*Cy-By*Cx, Ax*Cy-Ay*Cx, Ax*By-Ay*Bx)
}

func skOnSameSide(c [4]fpt, testIdx, lineIdx int) bool {
	origin := c[lineIdx]
	line := c[lineIdx+1].sub(origin)
	var cr [2]float32
	for i := 0; i < 2; i++ {
		cr[i] = line.cross(c[testIdx+i].sub(origin))
	}
	return cr[0]*cr[1] >= 0
}

func skFindCubicCusp(c [4]fpt) float32 {
	if c[0] == c[1] || c[2] == c[3] {
		return -1
	}
	if skOnSameSide(c, 0, 2) || skOnSameSide(c, 2, 0) {
		return -1
	}
	for _, t := range skCubicMaxCurvature(c) {
		if 0 >= t || t >= 1 {
			continue
		}
		dPt := skCubicDeriv(c[:], t)
		mag := skLenSqd(dPt)
		prec := (skDistSqd(c[1], c[0]) + skDistSqd(c[2], c[1]) + skDistSqd(c[3], c[2])) * 1e-8
		if mag < prec {
			return t
		}
	}
	return -1
}

func (k *skStroker) cubicQuadEnds(c [4]fpt, q *skQuadConstruct) {
	if !q.startSet {
		var p fpt
		k.cubicPerpRay(c, q.startT, &p, &q.quad[0], &q.tangentStart)
		q.startSet = true
	}
	if !q.endSet {
		var p fpt
		k.cubicPerpRay(c, q.endT, &p, &q.quad[2], &q.tangentEnd)
		q.endSet = true
	}
}

func (k *skStroker) compareQuadCubic(c [4]fpt, q *skQuadConstruct) skResult {
	k.cubicQuadEnds(c, q)
	if r := k.intersectRay(q, true); r != skQuadRes {
		return r
	}
	var ray [2]fpt
	k.cubicPerpRay(c, q.midT, &ray[1], &ray[0], nil)
	return k.strokeCloseEnough(q.quad, ray, q)
}

func (k *skStroker) cubicMidOnLine(c [4]fpt, q *skQuadConstruct) bool {
	var p, mid fpt
	k.cubicPerpRay(c, q.midT, &p, &mid, nil)
	return skPtToLine(mid, q.quad[0], q.quad[2]) < k.invResScaleSq
}

func (k *skStroker) cubicStroke(c [4]fpt, q *skQuadConstruct) bool {
	if !k.foundTangents {
		k.cubicQuadEnds(c, q)
		res := k.intersectRay(q, false)
		if res != skQuadRes {
			if (res == skDegenRes || skWithin(q.quad[0], q.quad[2], k.invResScale)) && k.cubicMidOnLine(c, q) {
				k.addDegenerateLine(q)
				return true
			}
		} else {
			k.foundTangents = true
		}
	}
	if k.foundTangents {
		res := k.compareQuadCubic(c, q)
		if res == skQuadRes {
			k.sink().quadTo(q.quad[1], q.quad[2])
			return true
		}
		if res == skDegenRes && !q.opposite {
			k.addDegenerateLine(q)
			return true
		}
	}
	if !q.quad[2].finite() {
		return false
	}
	lim := skRecTan
	if k.foundTangents {
		lim = skRecCubic
	}
	k.recursion++
	if k.recursion > lim {
		k.addDegenerateLine(q)
		k.recursion--
		return true
	}
	var half skQuadConstruct
	if !half.initWithStart(q) {
		k.addDegenerateLine(q)
		k.recursion--
		return true
	}
	if !k.cubicStroke(c, &half) {
		return false
	}
	if !half.initWithEnd(q) {
		k.addDegenerateLine(q)
		k.recursion--
		return true
	}
	if !k.cubicStroke(c, &half) {
		return false
	}
	k.recursion--
	return true
}

func (k *skStroker) cubicTo(p1, p2, p3 fpt) {
	cubic := [4]fpt{k.prevPt, p1, p2, p3}
	red, reduction, tp := skCheckCubicLinear(cubic)
	switch {
	case red == skPointRed || red == skLineRed:
		k.lineTo(p3, nil)
		return
	case red >= skDegenRed && red <= skDegen3Red:
		save := k.join
		k.lineTo(reduction[0], nil)
		k.join = JoinRound
		if red >= skDegen2Red {
			k.lineTo(reduction[1], nil)
		}
		if red == skDegen3Red {
			k.lineTo(reduction[2], nil)
		}
		k.lineTo(p3, nil)
		k.join = save
		return
	}
	tangentPt := cubic[tp]
	var normalAB, unitAB, normalCD, unitCD fpt
	if !k.preJoinTo(tangentPt, &normalAB, &unitAB, false) {
		k.lineTo(p3, nil)
		return
	}
	ts := skFindCubicInflections(cubic)
	lastT := float32(0)
	for i := 0; i <= len(ts); i++ {
		nextT := float32(1)
		if i < len(ts) {
			nextT = ts[i]
		}
		for _, outer := range []bool{true, false} {
			var q skQuadConstruct
			k.strokeOuter = outer
			k.foundTangents = false
			k.recursion = 0
			q.init(lastT, nextT)
			k.cubicStroke(cubic, &q)
		}
		lastT = nextT
	}
	if cusp := skFindCubicCusp(cubic); cusp > 0 {
		loc := skEvalCubic(cubic[:], cusp)
		k.cusper.addCircle(loc.x, loc.y, k.radius)
	}
	// setCubicEndNormal
	ab, cd := cubic[1].sub(cubic[0]), cubic[3].sub(cubic[2])
	dAB, dCD := skDegenerate(ab), skDegenerate(cd)
	ok := !(dAB && dCD)
	if ok {
		if dAB {
			ab = cubic[2].sub(cubic[0])
			dAB = skDegenerate(ab)
		}
		if dCD {
			cd = cubic[3].sub(cubic[1])
			dCD = skDegenerate(cd)
		}
		ok = !(dAB || dCD)
	}
	if ok {
		skSetNormalVec(cd, k.radius, &normalCD, &unitCD)
	} else {
		normalCD, unitCD = normalAB, unitAB
	}
	k.postJoinTo(p3, normalCD, unitCD)
}

// ---- driver ---------------------------------------------------------------------------------

// skStrokePath is SkStroke::strokePath: it strokes p (float32 values taken
// from p's float64 coordinates) and returns the outline with conics intact.
// closedRect handles SkStroke::strokeRect for a closed axis-aligned rectangle.
func skStrokePath(p *Path, width float32, join LineJoin, cap LineCap, miterLimit, resScale float32) *skPath {
	radius := width / 2
	out := &skPath{}
	if !(radius > 0) {
		return out
	}
	if r, dirCW, ok := skIsClosedRect(p); ok {
		skStrokeRect(r, dirCW, width, join, miterLimit, out)
		return out
	}
	k := newSkStroker(radius, miterLimit, cap, join, resScale)
	type op struct {
		v byte
		p [3]fpt
	}
	var ops []op
	i := 0
	f := func(n int) fpt { return fpt{float32(p.Pts[i+2*n]), float32(p.Pts[i+2*n+1])} }
	var cur, start fpt
	has := false
	needClose := false
	autoClose := func() {
		if has && needClose && cur != start {
			ops = append(ops, op{'L', [3]fpt{start}})
		}
	}
	for _, c := range p.Cmds {
		switch c {
		case 'M':
			ops = append(ops, op{'M', [3]fpt{f(0)}})
			start = f(0)
			cur = start
			has = true
			needClose = false
			i += 2
		case 'L':
			ops = append(ops, op{'L', [3]fpt{f(0)}})
			cur = f(0)
			needClose = true
			i += 2
		case 'Q':
			ops = append(ops, op{'Q', [3]fpt{f(0), f(1)}})
			cur = f(1)
			needClose = true
			i += 4
		case 'C':
			ops = append(ops, op{'C', [3]fpt{f(0), f(1), f(2)}})
			cur = f(2)
			needClose = true
			i += 6
		case 'Z':
			autoClose()
			ops = append(ops, op{'Z', [3]fpt{}})
			cur = start
			needClose = false
		}
	}
	lastSeg := byte('M')
	hasValidTangent := func(from int) func() bool {
		return func() bool {
			for j := from; j < len(ops); j++ {
				o := ops[j]
				switch o.v {
				case 'M', 'Z':
					return false
				case 'L':
					// compare with the point before it
					var prev fpt
					prev = skOpEnd(ops[j-1].v, ops[j-1].p)
					if prev == o.p[0] {
						continue
					}
					return true
				case 'Q':
					prev := skOpEnd(ops[j-1].v, ops[j-1].p)
					if prev == o.p[0] && prev == o.p[1] {
						continue
					}
					return true
				case 'C':
					prev := skOpEnd(ops[j-1].v, ops[j-1].p)
					if prev == o.p[0] && prev == o.p[1] && prev == o.p[2] {
						continue
					}
					return true
				}
			}
			return false
		}
	}
	var moveTo fpt
	for j, o := range ops {
		switch o.v {
		case 'M':
			k.moveTo(o.p[0])
			moveTo = o.p[0]
		case 'L':
			k.lineTo(o.p[0], hasValidTangent(j+1))
			lastSeg = 'L'
		case 'Q':
			k.quadTo(o.p[0], o.p[1])
			lastSeg = 'Q'
		case 'C':
			k.cubicTo(o.p[0], o.p[1], o.p[2])
			lastSeg = 'C'
		case 'Z':
			if cap != CapButt {
				if k.segmentCount == 0 {
					k.lineTo(moveTo, nil)
					lastSeg = 'L'
					continue
				}
				if skContourEmpty(k) {
					lastSeg = 'L'
					continue
				}
			}
			k.finishContour(true, lastSeg == 'L')
		}
	}
	k.finishContour(false, lastSeg == 'L')
	return &k.outer
}

func skOpEnd(v byte, p [3]fpt) fpt {
	switch v {
	case 'Q':
		return p[1]
	case 'C':
		return p[2]
	}
	return p[0]
}

// skContourEmpty is isCurrentContourEmpty: nothing but one point so far.
func skContourEmpty(k *skStroker) bool {
	zero := func(pts []fpt) bool {
		if len(pts) < 2 {
			return true
		}
		for _, q := range pts[1:] {
			if q != pts[0] {
				return false
			}
		}
		return true
	}
	return zero(k.inner.p) && zero(k.outer.p[min(k.firstOuterPtIndex, len(k.outer.p)):])
}

// skIsClosedRect reports whether p is one closed contour of axis-aligned lines
// forming a rectangle (SkPath::isRect with isClosed), and its winding.
func skIsClosedRect(p *Path) (r [4]float32, cw, ok bool) {
	var pts []fpt
	closed := false
	i := 0
	for ci, c := range p.Cmds {
		switch c {
		case 'M':
			if ci != 0 {
				return
			}
			pts = append(pts, fpt{float32(p.Pts[i]), float32(p.Pts[i+1])})
			i += 2
		case 'L':
			if closed {
				return
			}
			pts = append(pts, fpt{float32(p.Pts[i]), float32(p.Pts[i+1])})
			i += 2
		case 'Z':
			if ci != len(p.Cmds)-1 {
				return
			}
			closed = true
		default:
			return
		}
	}
	if !closed || len(pts) < 4 || len(pts) > 5 {
		return
	}
	if len(pts) == 5 && pts[4] != pts[0] {
		return
	}
	pts = pts[:4]
	// edges alternate between horizontal and vertical
	for j := 0; j < 4; j++ {
		a, b := pts[j], pts[(j+1)%4]
		if (a.x != b.x) == (a.y != b.y) {
			return
		}
	}
	h0 := pts[0].y == pts[1].y
	for j := 0; j < 4; j++ {
		a, b := pts[j], pts[(j+1)%4]
		if (a.y == b.y) != (h0 == (j%2 == 0)) {
			return
		}
	}
	x0, x1 := skMin32(pts[0].x, pts[2].x), skMax32(pts[0].x, pts[2].x)
	y0, y1 := skMin32(pts[0].y, pts[2].y), skMax32(pts[0].y, pts[2].y)
	if pts[1].x != x0 && pts[1].x != x1 || pts[1].y != y0 && pts[1].y != y1 {
		return
	}
	// direction of the first two edges (y down: positive cross is clockwise)
	d0 := pts[1].sub(pts[0])
	d1 := pts[2].sub(pts[1])
	cw = d0.cross(d1) > 0
	return [4]float32{x0, y0, x1, y1}, cw, true
}

// skStrokeRect is SkStroke::strokeRect.
func skStrokeRect(r [4]float32, cw bool, width float32, join LineJoin, miterLimit float32, out *skPath) {
	radius := width / 2
	rw, rh := r[2]-r[0], r[3]-r[1]
	o := [4]float32{r[0] - radius, r[1] - radius, r[2] + radius, r[3] + radius}
	if join == JoinMiter && miterLimit < 1.41421356 {
		join = JoinBevel
	}
	poly := func(pts []fpt) {
		if !cw {
			for a, b := 0, len(pts)-1; a < b; a, b = a+1, b-1 {
				pts[a], pts[b] = pts[b], pts[a]
			}
		}
		for i, q := range pts {
			if i == 0 {
				out.moveTo(q)
			} else {
				out.lineTo(q)
			}
		}
		out.close()
	}
	switch join {
	case JoinMiter:
		poly([]fpt{{o[0], o[1]}, {o[2], o[1]}, {o[2], o[3]}, {o[0], o[3]}})
	case JoinBevel:
		poly([]fpt{{r[0], o[1]}, {r[2], o[1]}, {o[2], r[1]}, {o[2], r[3]}, {r[2], o[3]}, {r[0], o[3]}, {o[0], r[3]}, {o[0], r[1]}})
	default:
		// rounded rectangle, quarter arcs as conics
		l, t, rr, b := o[0], o[1], o[2], o[3]
		type seg struct {
			c, e fpt
			line bool
		}
		segs := []seg{
			{e: fpt{rr - radius, t}, line: true},
			{c: fpt{rr, t}, e: fpt{rr, t + radius}},
			{e: fpt{rr, b - radius}, line: true},
			{c: fpt{rr, b}, e: fpt{rr - radius, b}},
			{e: fpt{l + radius, b}, line: true},
			{c: fpt{l, b}, e: fpt{l, b - radius}},
			{e: fpt{l, t + radius}, line: true},
			{c: fpt{l, t}, e: fpt{l + radius, t}},
		}
		if !cw {
			// counter-clockwise: mirror the sequence
			out.moveTo(fpt{l + radius, t})
			out.conicTo(fpt{l, t}, fpt{l, t + radius}, skRoot2Over2)
			out.lineTo(fpt{l, b - radius})
			out.conicTo(fpt{l, b}, fpt{l + radius, b}, skRoot2Over2)
			out.lineTo(fpt{rr - radius, b})
			out.conicTo(fpt{rr, b}, fpt{rr, b - radius}, skRoot2Over2)
			out.lineTo(fpt{rr, t + radius})
			out.conicTo(fpt{rr, t}, fpt{rr - radius, t}, skRoot2Over2)
			out.close()
		} else {
			out.moveTo(fpt{l + radius, t})
			for _, s := range segs {
				if s.line {
					out.lineTo(s.e)
				} else {
					out.conicTo(s.c, s.e, skRoot2Over2)
				}
			}
			out.close()
		}
	}
	if width < skMin32(rw, rh) {
		in := [4]float32{r[0] + radius, r[1] + radius, r[2] - radius, r[3] - radius}
		pts := []fpt{{in[0], in[1]}, {in[2], in[1]}, {in[2], in[3]}, {in[0], in[3]}}
		// the inner rectangle runs the opposite way
		if cw {
			for a, b := 0, len(pts)-1; a < b; a, b = a+1, b-1 {
				pts[a], pts[b] = pts[b], pts[a]
			}
		}
		for i, q := range pts {
			if i == 0 {
				out.moveTo(q)
			} else {
				out.lineTo(q)
			}
		}
		out.close()
	}
}
