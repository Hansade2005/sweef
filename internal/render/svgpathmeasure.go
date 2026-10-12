package render

import "math"

// Text on a path measures the path the way Blink does: SVG path data is
// normalized to lines, quadratic and cubic Béziers (arcs become cubics with
// Blink's SVGPathNormalizer decomposition) and measured by Skia's
// SkContourMeasure: curves are subdivided at their half until the control
// points are within 1/2 px of the chord, each piece contributes the length of
// its chord, and a position at a distance interpolates the curve parameter
// linearly inside the piece that holds the distance. The lengths therefore
// are those of the subdivision, not the true arc lengths.

type skSegType int

const (
	skLine skSegType = iota
	skQuad
	skCubic
	skConic
)

const skMaxT = 0x3FFFFFFF

type skSeg struct {
	dist  float64
	pt    int // index of the first point of the curve in the contour's points
	typ   skSegType
	tval  int
	scalT float64
	w     float64 // conic weight
}

type skContour struct {
	pts    [][2]float64
	segs   []skSeg
	length float64
}

type skBuilder struct {
	pts  [][2]float64
	segs []skSeg
	dist float64
	tol  float64
}

func (b *skBuilder) line(a, c [2]float64, pt int) {
	d := math.Hypot(c[0]-a[0], c[1]-a[1])
	prev := b.dist
	b.dist += d
	if b.dist > prev {
		b.segs = append(b.segs, skSeg{dist: b.dist, pt: pt, typ: skLine, tval: skMaxT, scalT: 1})
	}
}

func lerp2(a, c [2]float64, t float64) [2]float64 {
	return [2]float64{a[0] + (c[0]-a[0])*t, a[1] + (c[1]-a[1])*t}
}

func quadTooCurvy(p [3][2]float64, tol float64) bool {
	dx := p[1][0]/2 - (p[0][0]+p[2][0])/4
	dy := p[1][1]/2 - (p[0][1]+p[2][1])/4
	return math.Max(math.Abs(dx), math.Abs(dy)) > tol
}

func cubicTooCurvy(p [4][2]float64, tol float64) bool {
	ex := func(pt [2]float64, x, y float64) bool {
		return math.Max(math.Abs(x-pt[0]), math.Abs(y-pt[1])) > tol
	}
	return ex(p[1], p[0][0]+(p[3][0]-p[0][0])/3, p[0][1]+(p[3][1]-p[0][1])/3) ||
		ex(p[2], p[0][0]+(p[3][0]-p[0][0])*2/3, p[0][1]+(p[3][1]-p[0][1])*2/3)
}

func (b *skBuilder) quad(p [3][2]float64, mint, maxt, pt int) {
	if (maxt-mint)>>10 != 0 && quadTooCurvy(p, b.tol) {
		half := (mint + maxt) >> 1
		ab, bc := lerp2(p[0], p[1], .5), lerp2(p[1], p[2], .5)
		m := lerp2(ab, bc, .5)
		b.quad([3][2]float64{p[0], ab, m}, mint, half, pt)
		b.quad([3][2]float64{m, bc, p[2]}, half, maxt, pt)
		return
	}
	d := math.Hypot(p[2][0]-p[0][0], p[2][1]-p[0][1])
	prev := b.dist
	b.dist += d
	if b.dist > prev {
		b.segs = append(b.segs, skSeg{dist: b.dist, pt: pt, typ: skQuad, tval: maxt, scalT: float64(maxt) / skMaxT})
	}
}

func conicEval(p [3][2]float64, w, t float64) [2]float64 {
	mt := 1 - t
	a, bb, c := mt*mt, 2*mt*t*w, t*t
	d := a + bb + c
	return [2]float64{(a*p[0][0] + bb*p[1][0] + c*p[2][0]) / d, (a*p[0][1] + bb*p[1][1] + c*p[2][1]) / d}
}

func (b *skBuilder) conic(p [3][2]float64, w float64, mint int, minPt [2]float64, maxt int, maxPt [2]float64, pt int) {
	half := (mint + maxt) >> 1
	halfPt := conicEval(p, w, float64(half)/skMaxT)
	mid := [2]float64{(minPt[0] + maxPt[0]) / 2, (minPt[1] + maxPt[1]) / 2}
	if (maxt-mint)>>10 != 0 && math.Max(math.Abs(halfPt[0]-mid[0]), math.Abs(halfPt[1]-mid[1])) > b.tol {
		b.conic(p, w, mint, minPt, half, halfPt, pt)
		b.conic(p, w, half, halfPt, maxt, maxPt, pt)
		return
	}
	d := math.Hypot(maxPt[0]-minPt[0], maxPt[1]-minPt[1])
	prev := b.dist
	b.dist += d
	if b.dist > prev {
		b.segs = append(b.segs, skSeg{dist: b.dist, pt: pt, typ: skConic, tval: maxt, scalT: float64(maxt) / skMaxT, w: w})
	}
}

func (b *skBuilder) cubic(p [4][2]float64, mint, maxt, pt int) {
	if (maxt-mint)>>10 != 0 && cubicTooCurvy(p, b.tol) {
		half := (mint + maxt) >> 1
		ab, bc, cd := lerp2(p[0], p[1], .5), lerp2(p[1], p[2], .5), lerp2(p[2], p[3], .5)
		abc, bcd := lerp2(ab, bc, .5), lerp2(bc, cd, .5)
		m := lerp2(abc, bcd, .5)
		b.cubic([4][2]float64{p[0], ab, abc, m}, mint, half, pt)
		b.cubic([4][2]float64{m, bcd, cd, p[3]}, half, maxt, pt)
		return
	}
	d := math.Hypot(p[3][0]-p[0][0], p[3][1]-p[0][1])
	prev := b.dist
	b.dist += d
	if b.dist > prev {
		b.segs = append(b.segs, skSeg{dist: b.dist, pt: pt, typ: skCubic, tval: maxt, scalT: float64(maxt) / skMaxT})
	}
}

// pathCmd is one normalized path command in absolute coordinates.
type pathCmd struct {
	op byte // 'M', 'L', 'Q', 'C', 'O' (conic: p[0] control, p[1] end, w), 'Z'
	p  [3][2]float64
	w  float64
}

// svgNormalizePath parses SVG path data into absolute M/L/Q/C/Z commands.
func svgNormalizePath(d string) []pathCmd {
	toks := tokenizePath(d)
	var out []pathCmd
	var cx, cy, sx, sy float64
	var lcx, lcy float64 // last control point
	var prevCmd byte
	var cmd byte
	i := 0
	num := func() (float64, bool) {
		if i < len(toks) && toks[i].isNum {
			v := toks[i].v
			i++
			return v, true
		}
		return 0, false
	}
	flag := num
	started := false
	for i < len(toks) {
		if !toks[i].isNum {
			cmd = toks[i].c
			i++
			if cmd == 'Z' || cmd == 'z' {
				if started {
					out = append(out, pathCmd{op: 'Z'})
					cx, cy = sx, sy
				}
				prevCmd = 'Z'
				continue
			}
		} else if cmd == 0 || cmd == 'Z' || cmd == 'z' {
			break
		}
		if !started && cmd != 'M' && cmd != 'm' {
			break
		}
		rel := cmd >= 'a' && cmd <= 'z'
		up := cmd
		if rel {
			up = cmd - 32
		}
		ox, oy := 0.0, 0.0
		if rel {
			ox, oy = cx, cy
		}
		get := func(n int) ([]float64, bool) {
			v := make([]float64, n)
			for k := range v {
				x, ok := num()
				if !ok {
					return nil, false
				}
				v[k] = x
			}
			return v, true
		}
		switch up {
		case 'M':
			v, ok := get(2)
			if !ok {
				return out
			}
			cx, cy = ox+v[0], oy+v[1]
			sx, sy = cx, cy
			out = append(out, pathCmd{op: 'M', p: [3][2]float64{{cx, cy}}})
			started = true
			if rel {
				cmd = 'l'
			} else {
				cmd = 'L'
			}
			lcx, lcy = cx, cy
		case 'L', 'H', 'V':
			var nx, ny = cx, cy
			switch up {
			case 'L':
				v, ok := get(2)
				if !ok {
					return out
				}
				nx, ny = ox+v[0], oy+v[1]
			case 'H':
				x, ok := num()
				if !ok {
					return out
				}
				nx = ox + x
			case 'V':
				y, ok := num()
				if !ok {
					return out
				}
				ny = oy + y
			}
			out = append(out, pathCmd{op: 'L', p: [3][2]float64{{nx, ny}}})
			cx, cy = nx, ny
			lcx, lcy = cx, cy
		case 'C', 'S':
			var c1, c2, e [2]float64
			if up == 'C' {
				v, ok := get(6)
				if !ok {
					return out
				}
				c1 = [2]float64{ox + v[0], oy + v[1]}
				c2 = [2]float64{ox + v[2], oy + v[3]}
				e = [2]float64{ox + v[4], oy + v[5]}
			} else {
				v, ok := get(4)
				if !ok {
					return out
				}
				c1 = [2]float64{cx, cy}
				if prevCmd == 'C' || prevCmd == 'S' {
					c1 = [2]float64{2*cx - lcx, 2*cy - lcy}
				}
				c2 = [2]float64{ox + v[0], oy + v[1]}
				e = [2]float64{ox + v[2], oy + v[3]}
			}
			out = append(out, pathCmd{op: 'C', p: [3][2]float64{c1, c2, e}})
			lcx, lcy = c2[0], c2[1]
			cx, cy = e[0], e[1]
		case 'Q', 'T':
			var c1, e [2]float64
			if up == 'Q' {
				v, ok := get(4)
				if !ok {
					return out
				}
				c1 = [2]float64{ox + v[0], oy + v[1]}
				e = [2]float64{ox + v[2], oy + v[3]}
			} else {
				v, ok := get(2)
				if !ok {
					return out
				}
				c1 = [2]float64{cx, cy}
				if prevCmd == 'Q' || prevCmd == 'T' {
					c1 = [2]float64{2*cx - lcx, 2*cy - lcy}
				}
				e = [2]float64{ox + v[0], oy + v[1]}
			}
			out = append(out, pathCmd{op: 'Q', p: [3][2]float64{c1, e}})
			lcx, lcy = c1[0], c1[1]
			cx, cy = e[0], e[1]
		case 'A':
			rx, ok1 := num()
			ry, ok2 := num()
			ang, ok3 := num()
			large, ok4 := flag()
			sweep, ok5 := flag()
			ex, ok6 := num()
			ey, ok7 := num()
			if !(ok1 && ok2 && ok3 && ok4 && ok5 && ok6 && ok7) {
				return out
			}
			e := [2]float64{ox + ex, oy + ey}
			out = appendArc(out, [2]float64{cx, cy}, e, rx, ry, ang, large != 0, sweep != 0)
			cx, cy = e[0], e[1]
			lcx, lcy = cx, cy
		default:
			i++
			continue
		}
		prevCmd = up
	}
	return out
}

// appendArc converts an elliptical arc into conics the way SkPath::arcTo
// (SVG flavor) does, in single precision (the result is sensitive to float
// rounding when the radii are just large enough).
func appendArc(out []pathCmd, cur, tgt [2]float64, rx64, ry64, angle64 float64, large, sweep bool) []pathCmd {
	if rx64 == 0 || ry64 == 0 {
		return append(out, pathCmd{op: 'L', p: [3][2]float64{tgt}})
	}
	if cur == tgt {
		return out
	}
	f := func(v float64) float32 { return float32(v) }
	sqrt := func(v float32) float32 { return float32(math.Sqrt(float64(v))) }
	rx, ry := float32(math.Abs(rx64)), float32(math.Abs(ry64))
	ang := f(angle64)
	rad := float64(ang) * math.Pi / 180
	sn, cs := float32(math.Sin(rad)), float32(math.Cos(rad))
	snap := func(v float32) float32 {
		if math.Abs(float64(v)) <= 1.0/(1<<12) {
			return 0
		}
		return v
	}
	sn, cs = snap(sn), snap(cs)
	c0x, c0y, tx, ty := f(cur[0]), f(cur[1]), f(tgt[0]), f(tgt[1])
	mdx, mdy := (c0x-tx)*0.5, (c0y-ty)*0.5
	// rotate(-angle)
	tmx, tmy := cs*mdx+sn*mdy, -sn*mdx+cs*mdy
	rs := tmx*tmx/(rx*rx) + tmy*tmy/(ry*ry)
	if rs > 1 {
		rs = sqrt(rs)
		rx *= rs
		ry *= rs
	}
	// rotate(-angle) then scale(1/rx, 1/ry)
	toUnit := func(x, y float32) (float32, float32) {
		x, y = cs*x+sn*y, -sn*x+cs*y
		return x / rx, y / ry
	}
	u0x, u0y := toUnit(c0x, c0y)
	u1x, u1y := toUnit(tx, ty)
	dx, dy := u1x-u0x, u1y-u0y
	d := dx*dx + dy*dy
	sf2 := 1/d - 0.25
	if sf2 < 0 {
		sf2 = 0
	}
	sf := sqrt(sf2)
	if sweep == large {
		sf = -sf
	}
	dx, dy = dx*sf, dy*sf
	cx, cy := (u0x+u1x)*0.5-dy, (u0y+u1y)*0.5+dx
	th1 := float32(math.Atan2(float64(u0y-cy), float64(u0x-cx)))
	th2 := float32(math.Atan2(float64(u1y-cy), float64(u1x-cx)))
	arc := th2 - th1
	if arc < 0 && sweep {
		arc += float32(2 * math.Pi)
	} else if arc > 0 && !sweep {
		arc -= float32(2 * math.Pi)
	}
	if math.Abs(float64(arc)) <= 1.0/(1<<12) {
		return append(out, pathCmd{op: 'L', p: [3][2]float64{tgt}})
	}
	// unit circle -> ellipse: rotate(angle) * scale(rx, ry)
	mapFwd := func(x, y float32) [2]float64 {
		x, y = x*rx, y*ry
		return [2]float64{float64(cs*x - sn*y), float64(sn*x + cs*y)}
	}
	n := int(math.Ceil(math.Abs(float64(arc / float32(2*math.Pi/3)))))
	if n < 1 {
		n = 1
	}
	tw := arc / float32(n)
	t := float32(math.Tan(float64(0.5 * tw)))
	if math.IsNaN(float64(t)) || math.IsInf(float64(t), 0) {
		return out
	}
	w := sqrt(0.5 + float32(math.Cos(float64(tw)))*0.5)
	st := th1
	for i := 0; i < n; i++ {
		en := st + tw
		se, ce := snap(float32(math.Sin(float64(en)))), snap(float32(math.Cos(float64(en))))
		e1x, e1y := ce+cx, se+cy
		c1x, c1y := e1x+t*se, e1y-t*ce
		out = append(out, pathCmd{op: 'O', p: [3][2]float64{mapFwd(c1x, c1y), mapFwd(e1x, e1y)}, w: float64(w)})
		st = en
	}
	return out
}

// measureContours builds Skia's contour measures of normalized commands,
// scaled by k.
func measureContours(cmds []pathCmd, k float64) []*skContour {
	var res []*skContour
	var b *skBuilder
	var cur, start [2]float64
	var closed bool
	flush := func() {
		if b == nil {
			return
		}
		if closed && len(b.pts) > 0 {
			ptIdx := len(b.pts) - 1
			first := b.pts[0]
			prev := b.dist
			b.line(b.pts[ptIdx], first, ptIdx)
			if b.dist > prev {
				b.pts = append(b.pts, first)
			}
		}
		if b.dist > 0 && len(b.segs) > 0 {
			res = append(res, &skContour{pts: b.pts, segs: b.segs, length: b.dist})
		}
		b = nil
	}
	sc := func(p [2]float64) [2]float64 {
		return [2]float64{float64(float32(p[0] * k)), float64(float32(p[1] * k))}
	}
	for _, c := range cmds {
		switch c.op {
		case 'M':
			flush()
			closed = false
			cur = sc(c.p[0])
			start = cur
			b = &skBuilder{tol: 0.5, pts: [][2]float64{cur}}
		case 'L':
			if b == nil {
				continue
			}
			e := sc(c.p[0])
			pt := len(b.pts) - 1
			b.pts = append(b.pts, e)
			b.line(cur, e, pt)
			cur = e
		case 'Q':
			if b == nil {
				continue
			}
			p1, e := sc(c.p[0]), sc(c.p[1])
			pt := len(b.pts) - 1
			b.pts = append(b.pts, p1, e)
			b.quad([3][2]float64{cur, p1, e}, 0, skMaxT, pt)
			cur = e
		case 'O':
			if b == nil {
				continue
			}
			p1, e := sc(c.p[0]), sc(c.p[1])
			pt := len(b.pts) - 1
			b.pts = append(b.pts, p1, e)
			b.conic([3][2]float64{cur, p1, e}, c.w, 0, cur, skMaxT, e, pt)
			cur = e
		case 'C':
			if b == nil {
				continue
			}
			p1, p2, e := sc(c.p[0]), sc(c.p[1]), sc(c.p[2])
			pt := len(b.pts) - 1
			b.pts = append(b.pts, p1, p2, e)
			b.cubic([4][2]float64{cur, p1, p2, e}, 0, skMaxT, pt)
			cur = e
		case 'Z':
			if b != nil {
				closed = true
				flush()
				// a command after Z continues from the start point
				b = &skBuilder{tol: 0.5, pts: [][2]float64{start}}
				closed = false
				cur = start
			}
		}
	}
	flush()
	return res
}

// pos returns the point and unit tangent at distance d of the contour.
func (c *skContour) pos(d float64) (x, y, tx, ty float64) {
	if d < 0 {
		d = 0
	} else if d > c.length {
		d = c.length
	}
	// first segment whose distance >= d
	lo, hi := 0, len(c.segs)-1
	for lo < hi {
		mid := (lo + hi) / 2
		if c.segs[mid].dist >= d {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	seg := c.segs[lo]
	startT, startD := 0.0, 0.0
	if lo > 0 {
		startD = c.segs[lo-1].dist
		if c.segs[lo-1].pt == seg.pt {
			startT = c.segs[lo-1].scalT
		}
	}
	t := startT + (seg.scalT-startT)*(d-startD)/(seg.dist-startD)
	p := c.pts[seg.pt:]
	switch seg.typ {
	case skLine:
		x = p[0][0] + (p[1][0]-p[0][0])*t
		y = p[0][1] + (p[1][1]-p[0][1])*t
		tx, ty = p[1][0]-p[0][0], p[1][1]-p[0][1]
	case skQuad:
		a := [2]float64{p[2][0] - 2*p[1][0] + p[0][0], p[2][1] - 2*p[1][1] + p[0][1]}
		b := [2]float64{2 * (p[1][0] - p[0][0]), 2 * (p[1][1] - p[0][1])}
		x = (a[0]*t+b[0])*t + p[0][0]
		y = (a[1]*t+b[1])*t + p[0][1]
		tx = 2 * (a[0]*t + b[0]/2)
		ty = 2 * (a[1]*t + b[1]/2)
		if tx == 0 && ty == 0 {
			tx, ty = p[2][0]-p[0][0], p[2][1]-p[0][1]
		}
	case skConic:
		cp := [3][2]float64{p[0], p[1], p[2]}
		q := conicEval(cp, seg.w, t)
		x, y = q[0], q[1]
		mt := 1 - t
		a, bb, c := mt*mt, 2*mt*t*seg.w, t*t
		da, db, dc := -2*mt, 2*seg.w*(1-2*t), 2*t
		for i := 0; i < 2; i++ {
			N := a*cp[0][i] + bb*cp[1][i] + c*cp[2][i]
			dN := da*cp[0][i] + db*cp[1][i] + dc*cp[2][i]
			D := a + bb + c
			dD := da + db + dc
			v := (dN*D - N*dD) / (D * D)
			if i == 0 {
				tx = v
			} else {
				ty = v
			}
		}
	case skCubic:
		var A, B, C [2]float64
		for i := 0; i < 2; i++ {
			A[i] = p[3][i] + 3*(p[1][i]-p[2][i]) - p[0][i]
			B[i] = 3 * (p[2][i] - 2*p[1][i] + p[0][i])
			C[i] = 3 * (p[1][i] - p[0][i])
		}
		x = ((A[0]*t+B[0])*t+C[0])*t + p[0][0]
		y = ((A[1]*t+B[1])*t+C[1])*t + p[0][1]
		if (t == 0 && p[0] == p[1]) || (t == 1 && p[2] == p[3]) {
			tx, ty = p[2][0]-p[0][0], p[2][1]-p[0][1]
			if t == 1 {
				tx, ty = p[3][0]-p[1][0], p[3][1]-p[1][1]
			}
		} else {
			tx = (3*A[0]*t+2*B[0])*t + C[0]
			ty = (3*A[1]*t+2*B[1])*t + C[1]
		}
	}
	if l := math.Hypot(tx, ty); l > 0 {
		tx, ty = tx/l, ty/l
	}
	return
}
