package raster

import "math"

// A port of Skia's SkPathStroker (SkStroke.cpp) for the verbs that appear in
// glyph outlines: lines and quadratics, with miter, bevel and round joins.
// Skia approximates the offset of every quadratic with another quadratic whose
// midpoint may be up to a quarter pixel off (fInvResScale), so stroked glyphs
// only match Chromium when that approximation is reproduced rather than the
// exact offset curve. Cubics are first approximated by quadratics.

type spath struct {
	v []byte // 'M' 'L' 'Q' 'Z'
	p []Pt
}

func (s *spath) moveTo(x, y float64) { s.v = append(s.v, 'M'); s.p = append(s.p, Pt{x, y}) }
func (s *spath) lineTo(x, y float64) { s.v = append(s.v, 'L'); s.p = append(s.p, Pt{x, y}) }
func (s *spath) quadTo(x1, y1, x2, y2 float64) {
	s.v = append(s.v, 'Q')
	s.p = append(s.p, Pt{x1, y1}, Pt{x2, y2})
}
func (s *spath) close() { s.v = append(s.v, 'Z') }
func (s *spath) lastPt() Pt {
	return s.p[len(s.p)-1]
}
func (s *spath) setLastPt(x, y float64) {
	if len(s.p) == 0 {
		s.moveTo(x, y)
		return
	}
	s.p[len(s.p)-1] = Pt{x, y}
}
func (s *spath) empty() bool { return len(s.v) == 0 }
func (s *spath) rewind()     { s.v, s.p = s.v[:0], s.p[:0] }

// conicTo appends a conic as quadratics (SkAutoConicToQuads, tolerance 0.25).
func (s *spath) conicTo(x1, y1, x2, y2, w float64) {
	p0 := s.lastPt()
	c := [3]Pt{p0, {x1, y1}, {x2, y2}}
	// number of subdivisions (SkConic::computeQuadPOW2)
	a := w - 1
	k := a / (4 * (2 + a))
	ex := k * (c[0].X - 2*c[1].X + c[2].X)
	ey := k * (c[0].Y - 2*c[1].Y + c[2].Y)
	err := math.Sqrt(ex*ex + ey*ey)
	pow2 := 0
	for ; pow2 < 5; pow2++ {
		if err <= 0.25 {
			break
		}
		err *= 0.25
	}
	var rec func(c [3]Pt, w float64, depth int)
	rec = func(c [3]Pt, w float64, depth int) {
		if depth == 0 {
			s.quadTo(c[1].X, c[1].Y, c[2].X, c[2].Y)
			return
		}
		// chop the conic at its midpoint
		scale := 1 / (1 + w)
		newW := math.Sqrt(0.5 + w*0.5)
		wp1 := Pt{w * c[1].X, w * c[1].Y}
		m := Pt{(c[0].X + 2*wp1.X + c[2].X) * scale * 0.5, (c[0].Y + 2*wp1.Y + c[2].Y) * scale * 0.5}
		l1 := Pt{(c[0].X + wp1.X) * scale, (c[0].Y + wp1.Y) * scale}
		r1 := Pt{(wp1.X + c[2].X) * scale, (wp1.Y + c[2].Y) * scale}
		rec([3]Pt{c[0], l1, m}, newW, depth-1)
		rec([3]Pt{m, r1, c[2]}, newW, depth-1)
	}
	rec(c, w, pow2)
}

// reversePathTo appends the last contour of o in reverse, without a moveTo.
func (s *spath) reversePathTo(o *spath) {
	pi := len(o.p)
	for vi := len(o.v) - 1; vi >= 0; vi-- {
		switch o.v[vi] {
		case 'M':
			return
		case 'L':
			pi--
			s.lineTo(o.p[pi-1].X, o.p[pi-1].Y)
		case 'Q':
			pi -= 2
			s.quadTo(o.p[pi].X, o.p[pi].Y, o.p[pi-1].X, o.p[pi-1].Y)
		}
	}
}

func (s *spath) toPath() *Path {
	p := NewPath()
	i := 0
	for _, v := range s.v {
		switch v {
		case 'M':
			p.MoveTo(s.p[i].X, s.p[i].Y)
			i++
		case 'L':
			p.LineTo(s.p[i].X, s.p[i].Y)
			i++
		case 'Q':
			p.QuadTo(s.p[i].X, s.p[i].Y, s.p[i+1].X, s.p[i+1].Y)
			i += 2
		case 'Z':
			p.Close()
		}
	}
	return p
}

const (
	fxNearlyZero = 1.0 / (1 << 12)
	fxInvSqrt2   = 0.707106781
)

func vsub(a, b Pt) Pt      { return Pt{a.X - b.X, a.Y - b.Y} }
func vadd(a, b Pt) Pt      { return Pt{a.X + b.X, a.Y + b.Y} }
func vdot(a, b Pt) float64 { return a.X*b.X + a.Y*b.Y }
func vcross(a, b Pt) float64 {
	return a.X*b.Y - a.Y*b.X
}
func vlen2(a Pt) float64 { return a.X*a.X + a.Y*a.Y }

// setLength scales v to length l; false when v is too small.
func vsetLength(v *Pt, l float64) bool {
	d := math.Hypot(v.X, v.Y)
	if !(d > 1e-12) || math.IsInf(d, 0) {
		return false
	}
	v.X, v.Y = v.X*l/d, v.Y*l/d
	return true
}

func ptToLine(pt, a, b Pt) float64 {
	dxy := vsub(b, a)
	ab0 := vsub(pt, a)
	numer, denom := vdot(dxy, ab0), vdot(dxy, dxy)
	t := numer / denom
	if t >= 0 && t <= 1 {
		hit := Pt{a.X*(1-t) + b.X*t, a.Y*(1-t) + b.Y*t}
		return vlen2(vsub(hit, pt))
	}
	return vlen2(vsub(pt, a))
}

func evalQuad(q [3]Pt, t float64) Pt {
	a := Pt{q[2].X - 2*q[1].X + q[0].X, q[2].Y - 2*q[1].Y + q[0].Y}
	b := Pt{2 * (q[1].X - q[0].X), 2 * (q[1].Y - q[0].Y)}
	return Pt{(a.X*t+b.X)*t + q[0].X, (a.Y*t+b.Y)*t + q[0].Y}
}

func evalQuadTangent(q [3]Pt, t float64) Pt {
	if (t == 0 && q[0] == q[1]) || (t == 1 && q[1] == q[2]) {
		return vsub(q[2], q[0])
	}
	b := vsub(q[1], q[0])
	a := Pt{q[2].X - q[1].X - b.X, q[2].Y - q[1].Y - b.Y}
	tt := Pt{a.X*t + b.X, a.Y*t + b.Y}
	return Pt{2 * tt.X, 2 * tt.Y}
}

type fxStroker struct {
	radius, invMiterLimit, invResScale, invResScaleSq float64
	join                                              LineJoin
	cap                                               LineCap
	degenerate                                        bool // a zero-length contour needs caps this port does not draw
	outer, inner                                      spath
	out                                               spath // finished contours

	firstNormal, prevNormal, firstUnitNormal, prevUnitNormal Pt
	firstPt, prevPt, firstOuterPt                            Pt
	segmentCount                                             int
	prevIsLine, joinCompleted                                bool
	strokeOuter                                              bool
	recursion                                                int
}

func newFxStroker(width float64, join LineJoin, cap LineCap, miterLimit, resScale float64) *fxStroker {
	s := &fxStroker{radius: width / 2, join: join, cap: cap, segmentCount: -1}
	if miterLimit <= 1 {
		s.invMiterLimit = 1
	} else {
		s.invMiterLimit = 1 / miterLimit
	}
	if join != JoinMiter {
		s.invMiterLimit = 0
	}
	s.invResScale = 1 / (resScale * 4)
	s.invResScaleSq = s.invResScale * s.invResScale
	return s
}

func fxSetNormal(before, after Pt, resScale, radius float64, normal, unit *Pt) bool {
	u := Pt{(after.X - before.X) * resScale, (after.Y - before.Y) * resScale}
	if !vsetLength(&u, 1) {
		return false
	}
	u = Pt{u.Y, -u.X} // rotate CCW
	*unit = u
	*normal = Pt{u.X * radius, u.Y * radius}
	return true
}

type fxAngle int

const (
	fxNearly180 fxAngle = iota
	fxSharp
	fxShallow
	fxNearlyLine
)

func fxDot2Angle(dot float64) fxAngle {
	if dot >= 0 {
		if math.Abs(1-dot) <= fxNearlyZero {
			return fxNearlyLine
		}
		return fxShallow
	}
	if math.Abs(1+dot) <= fxNearlyZero {
		return fxNearly180
	}
	return fxSharp
}

func fxIsClockwise(before, after Pt) bool { return before.X*after.Y > before.Y*after.X }

func fxHandleInnerJoin(inner *spath, pivot, after Pt) {
	inner.lineTo(pivot.X, pivot.Y)
	inner.lineTo(pivot.X-after.X, pivot.Y-after.Y)
}

func (k *fxStroker) joiner(outer, inner *spath, beforeUnit, pivot, afterUnit Pt, prevIsLine, currIsLine bool) {
	switch k.join {
	case JoinRound:
		k.roundJoin(outer, inner, beforeUnit, pivot, afterUnit)
	case JoinBevel:
		k.bevelJoin(outer, inner, beforeUnit, pivot, afterUnit)
	default:
		k.miterJoin(outer, inner, beforeUnit, pivot, afterUnit, prevIsLine, currIsLine)
	}
}

func (k *fxStroker) bevelJoin(outer, inner *spath, beforeUnit, pivot, afterUnit Pt) {
	// Skia's BluntJoiner has no early-out for nearly straight joins: it
	// always adds the outer point and goes through the pivot on the inside.
	before, after := beforeUnit, afterUnit
	r := k.radius
	if !fxIsClockwise(before, after) {
		outer, inner = inner, outer
		before = Pt{-before.X, -before.Y}
		after = Pt{-after.X, -after.Y}
	}
	after = Pt{after.X * r, after.Y * r}
	outer.lineTo(pivot.X+after.X, pivot.Y+after.Y)
	fxHandleInnerJoin(inner, pivot, after)
}

func (k *fxStroker) miterJoin(outer, inner *spath, beforeUnit, pivot, afterUnit Pt, prevIsLine, currIsLine bool) {
	dot := vdot(beforeUnit, afterUnit)
	at := fxDot2Angle(dot)
	before, after := beforeUnit, afterUnit
	r := k.radius
	var mid Pt
	blunt := func() {
		after = Pt{after.X * r, after.Y * r}
		if !currIsLine {
			outer.lineTo(pivot.X+after.X, pivot.Y+after.Y)
		}
		fxHandleInnerJoin(inner, pivot, after)
	}
	if at == fxNearlyLine {
		return
	}
	if at == fxNearly180 {
		currIsLine = false
		blunt()
		return
	}
	ccw := !fxIsClockwise(before, after)
	if ccw {
		outer, inner = inner, outer
		before = Pt{-before.X, -before.Y}
		after = Pt{-after.X, -after.Y}
	}
	miter := func() {
		if prevIsLine {
			outer.setLastPt(pivot.X+mid.X, pivot.Y+mid.Y)
		} else {
			outer.lineTo(pivot.X+mid.X, pivot.Y+mid.Y)
		}
		blunt()
	}
	if dot == 0 && k.invMiterLimit <= fxInvSqrt2 {
		mid = Pt{(before.X + after.X) * r, (before.Y + after.Y) * r}
		miter()
		return
	}
	sinHalf := math.Sqrt((1 + dot) / 2)
	if sinHalf < k.invMiterLimit {
		currIsLine = false
		blunt()
		return
	}
	if at == fxSharp {
		mid = Pt{after.Y - before.Y, before.X - after.X}
		if ccw {
			mid = Pt{-mid.X, -mid.Y}
		}
	} else {
		mid = Pt{before.X + after.X, before.Y + after.Y}
	}
	vsetLength(&mid, r/sinHalf)
	miter()
}

func (k *fxStroker) roundJoin(outer, inner *spath, beforeUnit, pivot, afterUnit Pt) {
	dot := vdot(beforeUnit, afterUnit)
	if fxDot2Angle(dot) == fxNearlyLine {
		return
	}
	before, after := beforeUnit, afterUnit
	ccw := false
	if !fxIsClockwise(before, after) {
		outer, inner = inner, outer
		before = Pt{-before.X, -before.Y}
		after = Pt{-after.X, -after.Y}
		ccw = true
	}
	// arc from before to after around the pivot as quadratics
	a0 := math.Atan2(before.Y, before.X)
	a1 := math.Atan2(after.Y, after.X)
	d := a1 - a0
	if ccw {
		for d > 0 {
			d -= 2 * math.Pi
		}
	} else {
		for d < 0 {
			d += 2 * math.Pi
		}
	}
	n := int(math.Ceil(math.Abs(d) / (math.Pi / 2)))
	if n < 1 {
		n = 1
	}
	step := d / float64(n)
	r := k.radius
	for i := 0; i < n; i++ {
		s0 := a0 + step*float64(i)
		s1 := s0 + step
		w := math.Cos(step / 2)
		mx, my := math.Cos((s0+s1)/2)/w, math.Sin((s0+s1)/2)/w
		outer.conicTo(pivot.X+mx*r, pivot.Y+my*r, pivot.X+math.Cos(s1)*r, pivot.Y+math.Sin(s1)*r, w)
	}
	after = Pt{after.X * r, after.Y * r}
	fxHandleInnerJoin(inner, pivot, after)
}

func (k *fxStroker) moveTo(p Pt) {
	if k.segmentCount > 0 {
		k.finishContour(false, false)
	}
	k.segmentCount = 0
	k.firstPt, k.prevPt = p, p
	k.joinCompleted = false
}

func (k *fxStroker) preJoinTo(cur Pt, normal, unit *Pt, currIsLine bool) bool {
	if !fxSetNormal(k.prevPt, cur, 1, k.radius, normal, unit) {
		if k.cap == CapButt {
			return false // zero-length segments vanish
		}
		// square and round caps still draw: upright orientation
		*normal, *unit = Pt{k.radius, 0}, Pt{1, 0}
	}
	if k.segmentCount == 0 {
		k.firstNormal, k.firstUnitNormal = *normal, *unit
		k.firstOuterPt = Pt{k.prevPt.X + normal.X, k.prevPt.Y + normal.Y}
		k.outer.moveTo(k.firstOuterPt.X, k.firstOuterPt.Y)
		k.inner.moveTo(k.prevPt.X-normal.X, k.prevPt.Y-normal.Y)
	} else {
		k.joiner(&k.outer, &k.inner, k.prevUnitNormal, k.prevPt, *unit, k.prevIsLine, currIsLine)
	}
	k.prevIsLine = currIsLine
	return true
}

func (k *fxStroker) postJoinTo(cur, normal, unit Pt) {
	k.joinCompleted = true
	k.prevPt, k.prevUnitNormal, k.prevNormal = cur, unit, normal
	k.segmentCount++
}

func (k *fxStroker) lineTo(cur Pt) {
	if math.Abs(k.prevPt.X-cur.X) <= fxNearlyZero*k.invResScale && math.Abs(k.prevPt.Y-cur.Y) <= fxNearlyZero*k.invResScale {
		if k.cap == CapButt || k.joinCompleted {
			return
		}
	}
	var normal, unit Pt
	if !k.preJoinTo(cur, &normal, &unit, true) {
		return
	}
	k.outer.lineTo(cur.X+normal.X, cur.Y+normal.Y)
	k.inner.lineTo(cur.X-normal.X, cur.Y-normal.Y)
	k.postJoinTo(cur, normal, unit)
}

func fxDegenerate(v Pt) bool { return math.Hypot(v.X, v.Y) < 1e-9 }

func fxQuadInLine(q [3]Pt) bool {
	ptMax := -1.0
	var o1, o2 int
	for i := 0; i < 2; i++ {
		for j := i + 1; j < 3; j++ {
			d := vsub(q[j], q[i])
			m := math.Max(math.Abs(d.X), math.Abs(d.Y))
			if ptMax < m {
				o1, o2, ptMax = i, j, m
			}
		}
	}
	mid := o1 ^ o2 ^ 3
	const slop = 0.000005
	return ptToLine(q[mid], q[o1], q[o2]) <= ptMax*ptMax*slop
}

// quadMaxCurvature mirrors SkFindQuadMaxCurvature.
func fxQuadMaxCurvature(q [3]Pt) float64 {
	ax, ay := q[1].X-q[0].X, q[1].Y-q[0].Y
	bx, by := q[0].X-2*q[1].X+q[2].X, q[0].Y-2*q[1].Y+q[2].Y
	numer := -(ax*bx + ay*by)
	denom := bx*bx + by*by
	if numer <= 0 {
		return 0
	}
	if numer >= denom {
		return 1
	}
	return numer / denom
}

func (k *fxStroker) quadTo(p1, p2 Pt) {
	quad := [3]Pt{k.prevPt, p1, p2}
	dAB, dBC := fxDegenerate(vsub(quad[1], quad[0])), fxDegenerate(vsub(quad[2], quad[1]))
	if dAB && dBC {
		k.lineTo(p2)
		return
	}
	if dAB || dBC {
		k.lineTo(p2)
		return
	}
	if fxQuadInLine(quad) {
		t := fxQuadMaxCurvature(quad)
		if t == 0 || t == 1 {
			k.lineTo(p2)
			return
		}
		red := evalQuad(quad, t)
		k.lineTo(red)
		save := k.join
		k.join = JoinRound
		k.lineTo(p2)
		k.join = save
		return
	}
	var normalAB, unitAB, normalBC, unitBC Pt
	if !k.preJoinTo(p1, &normalAB, &unitAB, false) {
		k.lineTo(p2)
		return
	}
	var qc fxQuadConstruct
	k.strokeOuter = true
	qc.init(0, 1)
	k.recursion = 0
	k.quadStroke(quad, &qc)
	k.strokeOuter = false
	qc = fxQuadConstruct{}
	qc.init(0, 1)
	k.recursion = 0
	k.quadStroke(quad, &qc)
	if !fxSetNormal(quad[1], quad[2], 1, k.radius, &normalBC, &unitBC) {
		normalBC, unitBC = normalAB, unitAB
	}
	k.postJoinTo(p2, normalBC, unitBC)
}

type fxQuadConstruct struct {
	quad                       [3]Pt
	tangentStart, tangentEnd   Pt
	startT, midT, endT         float64
	startSet, endSet, opposite bool
}

func (q *fxQuadConstruct) init(start, end float64) bool {
	q.startT, q.midT, q.endT = start, (start+end)/2, end
	q.startSet, q.endSet = false, false
	return q.startT < q.midT && q.midT < q.endT
}

func (q *fxQuadConstruct) initWithStart(parent *fxQuadConstruct) bool {
	if !q.init(parent.startT, parent.midT) {
		return false
	}
	q.quad[0] = parent.quad[0]
	q.tangentStart = parent.tangentStart
	q.startSet = true
	return true
}

func (q *fxQuadConstruct) initWithEnd(parent *fxQuadConstruct) bool {
	if !q.init(parent.midT, parent.endT) {
		return false
	}
	q.quad[2] = parent.quad[2]
	q.tangentEnd = parent.tangentEnd
	q.endSet = true
	return true
}

func (k *fxStroker) setRayPts(tPt, dxy Pt, onPt, tangent *Pt) {
	if !vsetLength(&dxy, k.radius) {
		dxy = Pt{k.radius, 0}
	}
	flip := 1.0
	if !k.strokeOuter {
		flip = -1
	}
	// outer is kOuter_StrokeType = 1, inner = -1
	*onPt = Pt{tPt.X + flip*dxy.Y, tPt.Y - flip*dxy.X}
	if tangent != nil {
		*tangent = Pt{onPt.X + dxy.X, onPt.Y + dxy.Y}
	}
}

func (k *fxStroker) quadPerpRay(quad [3]Pt, t float64, tPt, onPt, tangent *Pt) {
	*tPt = evalQuad(quad, t)
	dxy := evalQuadTangent(quad, t)
	if dxy.X == 0 && dxy.Y == 0 {
		dxy = vsub(quad[2], quad[0])
	}
	k.setRayPts(*tPt, dxy, onPt, tangent)
}

type fxResult int

const (
	fxSplit fxResult = iota
	fxDegen
	fxQuadRes
)

func (k *fxStroker) intersectRay(q *fxQuadConstruct) fxResult {
	start, end := q.quad[0], q.quad[2]
	aLen := vsub(q.tangentStart, start)
	bLen := vsub(q.tangentEnd, end)
	denom := vcross(aLen, bLen)
	if denom == 0 || math.IsInf(denom, 0) || math.IsNaN(denom) {
		q.opposite = vdot(aLen, bLen) < 0
		return fxDegen
	}
	q.opposite = false
	ab0 := vsub(start, end)
	numerA := vcross(bLen, ab0)
	numerB := vcross(aLen, ab0)
	if (numerA >= 0) == (numerB >= 0) {
		d1 := ptToLine(start, end, q.tangentEnd)
		d2 := ptToLine(end, start, q.tangentStart)
		if math.Max(d1, d2) <= k.invResScaleSq {
			return fxDegen
		}
		return fxSplit
	}
	numerA /= denom
	if numerA > numerA-1 {
		q.quad[1] = Pt{start.X*(1-numerA) + q.tangentStart.X*numerA, start.Y*(1-numerA) + q.tangentStart.Y*numerA}
		return fxQuadRes
	}
	q.opposite = vdot(aLen, bLen) < 0
	return fxDegen
}

func fxSharpAngle(q [3]Pt) bool {
	smaller, larger := vsub(q[1], q[0]), vsub(q[1], q[2])
	sl, ll := vlen2(smaller), vlen2(larger)
	if sl > ll {
		smaller, larger = larger, smaller
		ll = sl
	}
	if !vsetLength(&smaller, ll) {
		return false
	}
	return vdot(smaller, larger) > 0
}

func fxWithin(a, b Pt, limit float64) bool { return vlen2(vsub(a, b)) <= limit*limit }

func fxPtInQuadBounds(q [3]Pt, pt Pt, inv float64) bool {
	xMin := math.Min(math.Min(q[0].X, q[1].X), q[2].X)
	if pt.X+inv < xMin {
		return false
	}
	xMax := math.Max(math.Max(q[0].X, q[1].X), q[2].X)
	if pt.X-inv > xMax {
		return false
	}
	yMin := math.Min(math.Min(q[0].Y, q[1].Y), q[2].Y)
	if pt.Y+inv < yMin {
		return false
	}
	yMax := math.Max(math.Max(q[0].Y, q[1].Y), q[2].Y)
	return pt.Y-inv <= yMax
}

func fxIntersectQuadRay(line [2]Pt, quad [3]Pt) []float64 {
	vec := vsub(line[1], line[0])
	var r [3]float64
	for n := 0; n < 3; n++ {
		r[n] = (quad[n].Y-line[0].Y)*vec.X - (quad[n].X-line[0].X)*vec.Y
	}
	A, B, C := r[2], r[1], r[0]
	A += C - 2*B
	B -= C
	rs := fxFindUnitQuadRoots(float32(A), float32(2*B), float32(C))
	out := make([]float64, len(rs))
	for i, v := range rs {
		out[i] = float64(v)
	}
	return out
}

func (k *fxStroker) strokeCloseEnough(stroke [3]Pt, ray [2]Pt, q *fxQuadConstruct) fxResult {
	strokeMid := evalQuad(stroke, 0.5)
	if fxWithin(ray[0], strokeMid, k.invResScale) {
		if fxSharpAngle(q.quad) {
			return fxSplit
		}
		return fxQuadRes
	}
	if !fxPtInQuadBounds(stroke, ray[0], k.invResScale) {
		return fxSplit
	}
	roots := fxIntersectQuadRay(ray, stroke)
	if len(roots) != 1 {
		return fxSplit
	}
	quadPt := evalQuad(stroke, roots[0])
	err := k.invResScale * (1 - math.Abs(roots[0]-0.5)*2)
	if fxWithin(ray[0], quadPt, err) {
		if fxSharpAngle(q.quad) {
			return fxSplit
		}
		return fxQuadRes
	}
	return fxSplit
}

func (k *fxStroker) compareQuadQuad(quad [3]Pt, q *fxQuadConstruct) fxResult {
	if !q.startSet {
		var qs Pt
		k.quadPerpRay(quad, q.startT, &qs, &q.quad[0], &q.tangentStart)
		q.startSet = true
	}
	if !q.endSet {
		var qe Pt
		k.quadPerpRay(quad, q.endT, &qe, &q.quad[2], &q.tangentEnd)
		q.endSet = true
	}
	if r := k.intersectRay(q); r != fxQuadRes {
		return r
	}
	var ray [2]Pt
	k.quadPerpRay(quad, q.midT, &ray[1], &ray[0], nil)
	return k.strokeCloseEnough(q.quad, ray, q)
}

func (k *fxStroker) addDegenerateLine(q *fxQuadConstruct) {
	p := &k.inner
	if k.strokeOuter {
		p = &k.outer
	}
	p.lineTo(q.quad[2].X, q.quad[2].Y)
}

func (k *fxStroker) quadStroke(quad [3]Pt, q *fxQuadConstruct) bool {
	switch k.compareQuadQuad(quad, q) {
	case fxQuadRes:
		p := &k.inner
		if k.strokeOuter {
			p = &k.outer
		}
		p.quadTo(q.quad[1].X, q.quad[1].Y, q.quad[2].X, q.quad[2].Y)
		return true
	case fxDegen:
		k.addDegenerateLine(q)
		return true
	}
	k.recursion++
	if k.recursion > 33 {
		return false
	}
	var half fxQuadConstruct
	half.initWithStart(q)
	if !k.quadStroke(quad, &half) {
		return false
	}
	half = fxQuadConstruct{}
	half.initWithEnd(q)
	if !k.quadStroke(quad, &half) {
		return false
	}
	k.recursion--
	return true
}

func (k *fxStroker) finishContour(closed, currIsLine bool) {
	if k.segmentCount > 0 {
		if closed {
			k.joiner(&k.outer, &k.inner, k.prevUnitNormal, k.prevPt, k.firstUnitNormal, k.prevIsLine, currIsLine)
			k.outer.close()
			last := k.inner.lastPt()
			k.outer.moveTo(last.X, last.Y)
			k.outer.reversePathTo(&k.inner)
			k.outer.close()
		} else {
			last := k.inner.lastPt()
			k.capper(&k.outer, k.prevPt, k.prevNormal, last, currIsLine)
			k.outer.reversePathTo(&k.inner)
			k.capper(&k.outer, k.firstPt, Pt{-k.firstNormal.X, -k.firstNormal.Y}, k.firstOuterPt, k.prevIsLine)
			k.outer.close()
		}
		k.out.v = append(k.out.v, k.outer.v...)
		k.out.p = append(k.out.p, k.outer.p...)
		k.outer.rewind()
	}
	k.inner.rewind()
	k.segmentCount = -1
}

func (k *fxStroker) capper(path *spath, pivot, normal, stop Pt, other bool) {
	parallel := Pt{-normal.Y, normal.X} // rotate CW
	switch k.cap {
	case CapRound:
		pc := Pt{pivot.X + parallel.X, pivot.Y + parallel.Y}
		path.conicTo(pc.X+normal.X, pc.Y+normal.Y, pc.X, pc.Y, 0.7071067811865476)
		path.conicTo(pc.X-normal.X, pc.Y-normal.Y, stop.X, stop.Y, 0.7071067811865476)
	case CapSquare:
		if other {
			path.setLastPt(pivot.X+normal.X+parallel.X, pivot.Y+normal.Y+parallel.Y)
			path.lineTo(pivot.X-normal.X+parallel.X, pivot.Y-normal.Y+parallel.Y)
		} else {
			path.lineTo(pivot.X+normal.X+parallel.X, pivot.Y+normal.Y+parallel.Y)
			path.lineTo(pivot.X-normal.X+parallel.X, pivot.Y-normal.Y+parallel.Y)
			path.lineTo(stop.X, stop.Y)
		}
	default:
		path.lineTo(stop.X, stop.Y)
	}
}

// fxStrokePath strokes p (device space, curves allowed) and returns the
// outline: for every contour an outer loop and a reversed inner loop.
func fxStrokePath(p *Path, width float64, join LineJoin, cap LineCap, miterLimit, resScale float64) *Path {
	k := newFxStroker(width, join, cap, miterLimit, resScale)
	pts := p.Pts
	i := 0
	var cur, start Pt
	has := false
	lastLine := false
	closeContour := func() {
		if has && cur != start {
			k.lineTo(start)
			lastLine = true
		}
		cur = start
		if has {
			k.finishContour(true, lastLine)
		}
		has = false
	}
	for _, c := range p.Cmds {
		switch c {
		case 'M':
			if has {
				k.finishContour(false, false)
			}
			start = Pt{pts[i], pts[i+1]}
			cur = start
			k.moveTo(cur)
			has = true
			i += 2
		case 'L':
			n := Pt{pts[i], pts[i+1]}
			k.lineTo(n)
			cur = n
			lastLine = true
			i += 2
		case 'Q':
			n := Pt{pts[i+2], pts[i+3]}
			k.quadTo(Pt{pts[i], pts[i+1]}, n)
			cur = n
			lastLine = false
			i += 4
		case 'C':
			// approximate the cubic by quadratics
			c0, c1, c2, c3 := cur, Pt{pts[i], pts[i+1]}, Pt{pts[i+2], pts[i+3]}, Pt{pts[i+4], pts[i+5]}
			const n = 8
			prev := c0
			for s := 1; s <= n; s++ {
				t0, t1 := float64(s-1)/n, float64(s)/n
				pa, pb := evalCubic(c0, c1, c2, c3, t0), evalCubic(c0, c1, c2, c3, t1)
				da, db := cubicDeriv(c0, c1, c2, c3, t0), cubicDeriv(c0, c1, c2, c3, t1)
				// control point: intersection of the end tangents
				h := (t1 - t0) / 3
				ca := Pt{pa.X + da.X*h, pa.Y + da.Y*h}
				cb := Pt{pb.X - db.X*h, pb.Y - db.Y*h}
				ctrl := Pt{(ca.X + cb.X) / 2, (ca.Y + cb.Y) / 2}
				k.quadTo(ctrl, pb)
				prev = pb
			}
			_ = prev
			cur = c3
			lastLine = false
			i += 6
		case 'Z':
			closeContour()
		}
	}
	if has {
		k.finishContour(false, false)
	}
	return k.out.toPath()
}

func evalCubic(a, b, c, d Pt, t float64) Pt {
	mt := 1 - t
	return Pt{mt*mt*mt*a.X + 3*mt*mt*t*b.X + 3*mt*t*t*c.X + t*t*t*d.X, mt*mt*mt*a.Y + 3*mt*mt*t*b.Y + 3*mt*t*t*c.Y + t*t*t*d.Y}
}

func cubicDeriv(a, b, c, d Pt, t float64) Pt {
	mt := 1 - t
	return Pt{3 * (mt*mt*(b.X-a.X) + 2*mt*t*(c.X-b.X) + t*t*(d.X-c.X)), 3 * (mt*mt*(b.Y-a.Y) + 2*mt*t*(c.Y-b.Y) + t*t*(d.Y-c.Y))}
}

// fxStrokeCubics lets StrokeMaskFx take paths with cubics, which this port
// only approximates by quadratics (Skia's cubic stroker is not ported).
var fxStrokeCubics = false

// skStrokeNew routes StrokeMaskFx through the single-precision stroker.
var skStrokeNew = true

// StrokeMaskFx strokes p (user space) with Skia's stroker and fills the
// outline, transformed by m, with its analytic anti-aliasing. ok is false for
// styles it does not cover (dashes, hairlines thinner than a device pixel),
// where StrokeMask should be used instead.
func StrokeMaskFx(p *Path, m Matrix, st StrokeStyle, clip IRect) (mask *Mask, ok bool) {
	if st.Width <= 0 || math.IsNaN(st.Width) || (validDash(st.Dash) != nil && !skStrokeNew) {
		return nil, false
	}
	sc := m.MaxScale()
	if sc == 0 || math.IsNaN(sc) || math.IsInf(sc, 0) {
		return nil, false
	}
	if st.Width*sc < 1 { // Skia draws these as modulated hairlines
		return nil, false
	}
	for _, v := range p.Pts {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, false
		}
	}
	if !fxStrokeCubics && !skStrokeNew {
		for _, c := range p.Cmds {
			if c == 'C' {
				return nil, false
			}
		}
	}
	ml := st.MiterLimit
	if ml <= 0 {
		ml = 10
	}
	if skStrokeNew {
		// SkDraw::drawPath: stroke in local space with the matrix's resolution
		// scale (the larger column length), then map to the device
		rs := float32(1)
		if s := math.Max(math.Hypot(m[0], m[1]), math.Hypot(m[2], m[3])); s > 0 && !math.IsInf(s, 0) {
			rs = float32(s)
		}
		local := p
		if d := validDash(st.Dash); d != nil {
			dp, ok := NewDashParams(dashF32(d), float32(st.DashOffset))
			if !ok {
				return nil, false
			}
			if dashed, stroked, ok := dp.DashPath(p, float32(st.Width), st.Cap, rs); ok {
				local = dashed
				if stroked {
					return fxFillPath(local.Transform(m), clip), true
				}
			}
		}
		sp := skStrokePath(local, float32(st.Width), st.Join, st.Cap, float32(ml), rs)
		return fxFillPath(sp.toDeviceT([4]float32{float32(m[0]), float32(m[2]), float32(m[1]), float32(m[3])}, float32(m[4]), float32(m[5])), clip), true
	}
	sp := fxStrokePath(p, st.Width, st.Join, st.Cap, ml, sc)
	if !m.IsIdentity() {
		sp = sp.Transform(m)
	}
	return fxFillPath(sp, clip), true
}
