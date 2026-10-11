package raster

import "math"

// Path is a sequence of subpaths made of lines, quadratic and cubic curves.
// Coordinates are in whatever space the caller chose.
type Path struct {
	Cmds []byte    // 'M','L','Q','C','Z'
	Pts  []float64 // 2 per M/L, 4 per Q, 6 per C
	cur  Pt
	st   Pt
	has  bool
}

// NewPath returns an empty path.
func NewPath() *Path { return &Path{} }

// Empty reports whether the path has no segments.
func (p *Path) Empty() bool { return len(p.Cmds) == 0 }

// Clone copies the path.
func (p *Path) Clone() *Path {
	q := *p
	q.Cmds = append([]byte(nil), p.Cmds...)
	q.Pts = append([]float64(nil), p.Pts...)
	return &q
}

// MoveTo starts a new subpath.
func (p *Path) MoveTo(x, y float64) {
	if n := len(p.Cmds); n > 0 && p.Cmds[n-1] == 'M' { // collapse consecutive moves
		p.Pts[len(p.Pts)-2], p.Pts[len(p.Pts)-1] = x, y
	} else {
		p.Cmds = append(p.Cmds, 'M')
		p.Pts = append(p.Pts, x, y)
	}
	p.cur, p.st, p.has = Pt{x, y}, Pt{x, y}, true
}

func (p *Path) ensure(x, y float64) {
	if !p.has {
		p.MoveTo(x, y)
	} else if n := len(p.Cmds); n > 0 && p.Cmds[n-1] == 'Z' {
		p.MoveTo(p.st.X, p.st.Y)
	}
}

// LineTo adds a line.
func (p *Path) LineTo(x, y float64) {
	p.ensure(x, y)
	p.Cmds = append(p.Cmds, 'L')
	p.Pts = append(p.Pts, x, y)
	p.cur = Pt{x, y}
}

// QuadTo adds a quadratic curve.
func (p *Path) QuadTo(cx, cy, x, y float64) {
	p.ensure(cx, cy)
	p.Cmds = append(p.Cmds, 'Q')
	p.Pts = append(p.Pts, cx, cy, x, y)
	p.cur = Pt{x, y}
}

// CubicTo adds a cubic curve.
func (p *Path) CubicTo(c1x, c1y, c2x, c2y, x, y float64) {
	p.ensure(c1x, c1y)
	p.Cmds = append(p.Cmds, 'C')
	p.Pts = append(p.Pts, c1x, c1y, c2x, c2y, x, y)
	p.cur = Pt{x, y}
}

// Close closes the current subpath.
func (p *Path) Close() {
	if !p.has {
		return
	}
	if n := len(p.Cmds); n > 0 && p.Cmds[n-1] != 'Z' {
		p.Cmds = append(p.Cmds, 'Z')
	}
	p.cur = p.st
}

// Current returns the current point.
func (p *Path) Current() (Pt, bool) { return p.cur, p.has }

// Rect adds a closed rectangle.
func (p *Path) Rect(x, y, w, h float64) {
	p.MoveTo(x, y)
	p.LineTo(x+w, y)
	p.LineTo(x+w, y+h)
	p.LineTo(x, y+h)
	p.Close()
}

const kappa = 0.5522847498307936

// Ellipse adds a full ellipse as four cubics.
func (p *Path) Ellipse(cx, cy, rx, ry float64) {
	kx, ky := rx*kappa, ry*kappa
	p.MoveTo(cx+rx, cy)
	p.CubicTo(cx+rx, cy+ky, cx+kx, cy+ry, cx, cy+ry)
	p.CubicTo(cx-kx, cy+ry, cx-rx, cy+ky, cx-rx, cy)
	p.CubicTo(cx-rx, cy-ky, cx-kx, cy-ry, cx, cy-ry)
	p.CubicTo(cx+kx, cy-ry, cx+rx, cy-ky, cx+rx, cy)
	p.Close()
}

// RoundRect adds a rounded rectangle with per-corner elliptical radii
// (top-left, top-right, bottom-right, bottom-left; each [rx, ry]).
func (p *Path) RoundRect(r Rect, rad [4][2]float64) {
	for i := range rad {
		rad[i][0] = math.Max(rad[i][0], 0)
		rad[i][1] = math.Max(rad[i][1], 0)
	}
	// scale radii down so adjacent corners fit
	f := 1.0
	fit := func(sum, avail float64) {
		if sum > avail && sum > 0 {
			f = math.Min(f, avail/sum)
		}
	}
	fit(rad[0][0]+rad[1][0], r.W)
	fit(rad[3][0]+rad[2][0], r.W)
	fit(rad[0][1]+rad[3][1], r.H)
	fit(rad[1][1]+rad[2][1], r.H)
	if f < 1 {
		for i := range rad {
			rad[i][0] *= f
			rad[i][1] *= f
		}
	}
	x0, y0, x1, y1 := r.X, r.Y, r.X+r.W, r.Y+r.H
	arc := func(cx, cy, rx, ry float64, q int) {
		if rx <= 0 || ry <= 0 {
			return
		}
		kx, ky := rx*kappa, ry*kappa
		switch q {
		case 0: // top-left: from (cx-rx, cy) to (cx, cy-ry)
			p.CubicTo(cx-rx, cy-ky, cx-kx, cy-ry, cx, cy-ry)
		case 1: // top-right
			p.CubicTo(cx+kx, cy-ry, cx+rx, cy-ky, cx+rx, cy)
		case 2: // bottom-right
			p.CubicTo(cx+rx, cy+ky, cx+kx, cy+ry, cx, cy+ry)
		case 3: // bottom-left
			p.CubicTo(cx-kx, cy+ry, cx-rx, cy+ky, cx-rx, cy)
		}
	}
	p.MoveTo(x0, y0+rad[0][1])
	arc(x0+rad[0][0], y0+rad[0][1], rad[0][0], rad[0][1], 0)
	p.LineTo(x1-rad[1][0], y0)
	arc(x1-rad[1][0], y0+rad[1][1], rad[1][0], rad[1][1], 1)
	p.LineTo(x1, y1-rad[2][1])
	arc(x1-rad[2][0], y1-rad[2][1], rad[2][0], rad[2][1], 2)
	p.LineTo(x0+rad[3][0], y1)
	arc(x0+rad[3][0], y1-rad[3][1], rad[3][0], rad[3][1], 3)
	p.Close()
}

// Arc adds an elliptical arc (center, radii, rotation, angles in radians),
// preceded by a connecting line when the path has a current point.
func (p *Path) Arc(cx, cy, rx, ry, rot, a0, a1 float64, ccw bool) {
	// normalise sweep
	const twoPi = 2 * math.Pi
	var sweep float64
	if !ccw {
		sweep = a1 - a0
		if sweep >= twoPi {
			sweep = twoPi
		} else if sweep < 0 {
			sweep = math.Mod(sweep, twoPi)
			if sweep < 0 {
				sweep += twoPi
			}
		}
	} else {
		sweep = a1 - a0
		if sweep <= -twoPi {
			sweep = -twoPi
		} else if sweep > 0 {
			sweep = math.Mod(sweep, twoPi)
			if sweep > 0 {
				sweep -= twoPi
			}
		}
	}
	sr, cr := math.Sincos(rot)
	pt := func(a float64) (float64, float64) {
		s, c := math.Sincos(a)
		x, y := rx*c, ry*s
		return cx + x*cr - y*sr, cy + x*sr + y*cr
	}
	sx, sy := pt(a0)
	if p.has {
		p.LineTo(sx, sy)
	} else {
		p.MoveTo(sx, sy)
	}
	n := int(math.Ceil(math.Abs(sweep) / (math.Pi / 2)))
	if n == 0 {
		return
	}
	step := sweep / float64(n)
	t := 4.0 / 3.0 * math.Tan(step/4)
	a := a0
	for i := 0; i < n; i++ {
		b := a + step
		sa, ca := math.Sincos(a)
		sb, cb := math.Sincos(b)
		// derivative-based control points on the unit circle then scale/rotate
		x1, y1 := ca-t*sa, sa+t*ca
		x2, y2 := cb+t*sb, sb-t*cb
		tr := func(x, y float64) (float64, float64) {
			x, y = x*rx, y*ry
			return cx + x*cr - y*sr, cy + x*sr + y*cr
		}
		c1x, c1y := tr(x1, y1)
		c2x, c2y := tr(x2, y2)
		ex, ey := tr(cb, sb)
		p.CubicTo(c1x, c1y, c2x, c2y, ex, ey)
		a = b
	}
}

// Transform returns the path with every point mapped through m.
func (p *Path) Transform(m Matrix) *Path {
	q := &Path{Cmds: append([]byte(nil), p.Cmds...), Pts: make([]float64, len(p.Pts)), has: p.has}
	for i := 0; i < len(p.Pts); i += 2 {
		q.Pts[i], q.Pts[i+1] = m.Apply(p.Pts[i], p.Pts[i+1])
	}
	q.cur = m.ApplyPt(p.cur)
	q.st = m.ApplyPt(p.st)
	return q
}

// AddPath appends o (transformed by m) to p.
func (p *Path) AddPath(o *Path, m Matrix) {
	t := o
	if !m.IsIdentity() {
		t = o.Transform(m)
	}
	p.Cmds = append(p.Cmds, t.Cmds...)
	p.Pts = append(p.Pts, t.Pts...)
	p.cur, p.st, p.has = t.cur, t.st, t.has || p.has
}

// Bounds returns the control-point bounding box.
func (p *Path) Bounds() (Rect, bool) {
	if len(p.Pts) == 0 {
		return Rect{}, false
	}
	x0, y0, x1, y1 := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for i := 0; i < len(p.Pts); i += 2 {
		x, y := p.Pts[i], p.Pts[i+1]
		x0, x1 = math.Min(x0, x), math.Max(x1, x)
		y0, y1 = math.Min(y0, y), math.Max(y1, y)
	}
	return Rect{x0, y0, x1 - x0, y1 - y0}, true
}

// Subpath is a flattened polyline. Smooth[i] marks vertices that lie inside a
// curve (joins there are smooth regardless of the line-join setting).
type Subpath struct {
	P      []Pt
	Smooth []bool
	Closed bool
}

// Flatten converts the path to polylines. tol is the maximum deviation in the
// path's own units.
func (p *Path) Flatten(tol float64) []Subpath {
	var out []Subpath
	var cur *Subpath
	var start Pt
	var last Pt
	flush := func() {
		if cur != nil && len(cur.P) > 0 {
			out = append(out, *cur)
		}
		cur = nil
	}
	add := func(pt Pt, smooth bool) {
		cur.P = append(cur.P, pt)
		cur.Smooth = append(cur.Smooth, smooth)
	}
	i := 0
	for _, c := range p.Cmds {
		switch c {
		case 'M':
			flush()
			start = Pt{p.Pts[i], p.Pts[i+1]}
			i += 2
			cur = &Subpath{}
			add(start, false)
			last = start
		case 'L':
			if cur == nil {
				cur = &Subpath{}
				add(last, false)
			}
			last = Pt{p.Pts[i], p.Pts[i+1]}
			i += 2
			add(last, false)
		case 'Q':
			if cur == nil {
				cur = &Subpath{}
				add(last, false)
			}
			c1 := Pt{p.Pts[i], p.Pts[i+1]}
			e := Pt{p.Pts[i+2], p.Pts[i+3]}
			i += 4
			flattenQuad(last, c1, e, tol, func(pt Pt, smooth bool) { add(pt, smooth) })
			last = e
		case 'C':
			if cur == nil {
				cur = &Subpath{}
				add(last, false)
			}
			c1 := Pt{p.Pts[i], p.Pts[i+1]}
			c2 := Pt{p.Pts[i+2], p.Pts[i+3]}
			e := Pt{p.Pts[i+4], p.Pts[i+5]}
			i += 6
			flattenCubic(last, c1, c2, e, tol, func(pt Pt, smooth bool) { add(pt, smooth) })
			last = e
		case 'Z':
			if cur != nil {
				cur.Closed = true
				flush()
			}
			last = start
		}
	}
	flush()
	return out
}

func flattenQuad(p0, p1, p2 Pt, tol float64, emit func(Pt, bool)) {
	// deviation of quad from chord is |p0 - 2p1 + p2| / 4 at t=.5
	dx, dy := p0.X-2*p1.X+p2.X, p0.Y-2*p1.Y+p2.Y
	dd := math.Hypot(dx, dy)
	n := int(math.Ceil(math.Sqrt(dd / (4 * tol))))
	if n < 1 {
		n = 1
	}
	if n > 500 {
		n = 500
	}
	for i := 1; i <= n; i++ {
		t := float64(i) / float64(n)
		mt := 1 - t
		pt := Pt{mt*mt*p0.X + 2*mt*t*p1.X + t*t*p2.X, mt*mt*p0.Y + 2*mt*t*p1.Y + t*t*p2.Y}
		emit(pt, i < n)
	}
}

func flattenCubic(p0, p1, p2, p3 Pt, tol float64, emit func(Pt, bool)) {
	d1x, d1y := p0.X-2*p1.X+p2.X, p0.Y-2*p1.Y+p2.Y
	d2x, d2y := p1.X-2*p2.X+p3.X, p1.Y-2*p2.Y+p3.Y
	dd := math.Max(math.Hypot(d1x, d1y), math.Hypot(d2x, d2y))
	n := int(math.Ceil(math.Sqrt(3 * dd / (4 * tol))))
	if n < 1 {
		n = 1
	}
	if n > 500 {
		n = 500
	}
	for i := 1; i <= n; i++ {
		t := float64(i) / float64(n)
		mt := 1 - t
		a, b, c, d := mt*mt*mt, 3*mt*mt*t, 3*mt*t*t, t*t*t
		pt := Pt{a*p0.X + b*p1.X + c*p2.X + d*p3.X, a*p0.Y + b*p1.Y + c*p2.Y + d*p3.Y}
		emit(pt, i < n)
	}
}

// ParseSVGPath parses an SVG path data string into p.
func ParseSVGPath(d string) (*Path, bool) {
	p := NewPath()
	ok := parseSVG(p, d)
	return p, ok
}

func parseSVG(p *Path, s string) bool {
	i := 0
	n := len(s)
	skip := func() {
		for i < n && (s[i] == ' ' || s[i] == ',' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r' || s[i] == '\f') {
			i++
		}
	}
	skipWS := func() {
		for i < n && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r' || s[i] == '\f') {
			i++
		}
	}
	num := func() (float64, bool) {
		skip()
		st := i
		if i < n && (s[i] == '+' || s[i] == '-') {
			i++
		}
		digits := 0
		for i < n && s[i] >= '0' && s[i] <= '9' {
			i++
			digits++
		}
		if i < n && s[i] == '.' {
			i++
			for i < n && s[i] >= '0' && s[i] <= '9' {
				i++
				digits++
			}
		}
		if digits == 0 {
			i = st
			return 0, false
		}
		if i < n && (s[i] == 'e' || s[i] == 'E') {
			j := i + 1
			if j < n && (s[j] == '+' || s[j] == '-') {
				j++
			}
			if j < n && s[j] >= '0' && s[j] <= '9' {
				for j < n && s[j] >= '0' && s[j] <= '9' {
					j++
				}
				i = j
			}
		}
		v, err := parseFloat(s[st:i])
		return v, err == nil
	}
	flag := func() (bool, bool) {
		skip()
		if i < n && (s[i] == '0' || s[i] == '1') {
			v := s[i] == '1'
			i++
			return v, true
		}
		return false, false
	}
	var cur, start, lastC, lastQ Pt
	var prev byte
	skipWS()
	if i >= n {
		return true
	}
	first := true
	for {
		skipWS()
		if i >= n {
			break
		}
		cmd := s[i]
		if !((cmd >= 'a' && cmd <= 'z') || (cmd >= 'A' && cmd <= 'Z')) {
			return false
		}
		if first && cmd != 'M' && cmd != 'm' {
			return false
		}
		first = false
		i++
		rel := cmd >= 'a'
		up := cmd &^ 0x20
		if up == 'Z' {
			p.Close()
			cur = start
			prev = 'Z'
			continue
		}
		var np int
		switch up {
		case 'M', 'L', 'T':
			np = 2
		case 'H', 'V':
			np = 1
		case 'S', 'Q':
			np = 4
		case 'C':
			np = 6
		case 'A':
			np = 7
		default:
			return false
		}
		cnt := 0
		for {
			var a [7]float64
			okk := true
			if up == 'A' {
				for k := 0; k < 3 && okk; k++ {
					a[k], okk = num()
				}
				if okk {
					var f1, f2 bool
					f1, okk = flag()
					if okk {
						f2, okk = flag()
					}
					if okk {
						if f1 {
							a[3] = 1
						}
						if f2 {
							a[4] = 1
						}
						a[5], okk = num()
						if okk {
							a[6], okk = num()
						}
					}
				}
			} else {
				for k := 0; k < np && okk; k++ {
					a[k], okk = num()
				}
			}
			if !okk {
				if cnt == 0 {
					return false
				}
				break
			}
			cnt++
			ox, oy := 0.0, 0.0
			if rel {
				ox, oy = cur.X, cur.Y
			}
			switch up {
			case 'M':
				cur = Pt{a[0] + ox, a[1] + oy}
				if cnt == 1 {
					p.MoveTo(cur.X, cur.Y)
					start = cur
				} else {
					p.LineTo(cur.X, cur.Y)
				}
			case 'L':
				cur = Pt{a[0] + ox, a[1] + oy}
				p.LineTo(cur.X, cur.Y)
			case 'H':
				cur = Pt{a[0] + ox, cur.Y}
				p.LineTo(cur.X, cur.Y)
			case 'V':
				cur = Pt{cur.X, a[0] + oy}
				p.LineTo(cur.X, cur.Y)
			case 'C':
				p.CubicTo(a[0]+ox, a[1]+oy, a[2]+ox, a[3]+oy, a[4]+ox, a[5]+oy)
				lastC = Pt{a[2] + ox, a[3] + oy}
				cur = Pt{a[4] + ox, a[5] + oy}
			case 'S':
				c1 := cur
				if prev == 'C' || prev == 'S' {
					c1 = Pt{2*cur.X - lastC.X, 2*cur.Y - lastC.Y}
				}
				p.CubicTo(c1.X, c1.Y, a[0]+ox, a[1]+oy, a[2]+ox, a[3]+oy)
				lastC = Pt{a[0] + ox, a[1] + oy}
				cur = Pt{a[2] + ox, a[3] + oy}
			case 'Q':
				p.QuadTo(a[0]+ox, a[1]+oy, a[2]+ox, a[3]+oy)
				lastQ = Pt{a[0] + ox, a[1] + oy}
				cur = Pt{a[2] + ox, a[3] + oy}
			case 'T':
				c1 := cur
				if prev == 'Q' || prev == 'T' {
					c1 = Pt{2*cur.X - lastQ.X, 2*cur.Y - lastQ.Y}
				}
				p.QuadTo(c1.X, c1.Y, a[0]+ox, a[1]+oy)
				lastQ = c1
				cur = Pt{a[0] + ox, a[1] + oy}
			case 'A':
				e := Pt{a[5] + ox, a[6] + oy}
				arcTo(p, cur, a[0], a[1], a[2], a[3] != 0, a[4] != 0, e)
				cur = e
			}
			prev = up
			if up == 'M' {
				// subsequent pairs are implicit lineto
				prev = 'L'
				if rel {
					prev = 'L'
				}
			}
			skip()
			if i >= n || !(s[i] == '-' || s[i] == '+' || s[i] == '.' || (s[i] >= '0' && s[i] <= '9')) {
				break
			}
		}
	}
	return true
}

// arcTo appends an SVG endpoint-parameterised elliptical arc.
func arcTo(p *Path, from Pt, rx, ry, xrot float64, large, sweep bool, to Pt) {
	if from == to {
		return
	}
	rx, ry = math.Abs(rx), math.Abs(ry)
	if rx == 0 || ry == 0 {
		p.LineTo(to.X, to.Y)
		return
	}
	phi := xrot * math.Pi / 180
	sp, cp := math.Sincos(phi)
	dx, dy := (from.X-to.X)/2, (from.Y-to.Y)/2
	x1p, y1p := cp*dx+sp*dy, -sp*dx+cp*dy
	lam := x1p*x1p/(rx*rx) + y1p*y1p/(ry*ry)
	if lam > 1 {
		s := math.Sqrt(lam)
		rx *= s
		ry *= s
	}
	num := rx*rx*ry*ry - rx*rx*y1p*y1p - ry*ry*x1p*x1p
	den := rx*rx*y1p*y1p + ry*ry*x1p*x1p
	co := 0.0
	if den != 0 && num > 0 {
		co = math.Sqrt(num / den)
	}
	if large == sweep {
		co = -co
	}
	cxp, cyp := co*rx*y1p/ry, -co*ry*x1p/rx
	cx := cp*cxp - sp*cyp + (from.X+to.X)/2
	cy := sp*cxp + cp*cyp + (from.Y+to.Y)/2
	ang := func(ux, uy, vx, vy float64) float64 {
		return math.Atan2(ux*vy-uy*vx, ux*vx+uy*vy)
	}
	th1 := ang(1, 0, (x1p-cxp)/rx, (y1p-cyp)/ry)
	dth := ang((x1p-cxp)/rx, (y1p-cyp)/ry, (-x1p-cxp)/rx, (-y1p-cyp)/ry)
	if !sweep && dth > 0 {
		dth -= 2 * math.Pi
	} else if sweep && dth < 0 {
		dth += 2 * math.Pi
	}
	p.Arc(cx, cy, rx, ry, phi, th1, th1+dth, dth < 0)
	// snap the final point exactly
	if n := len(p.Pts); n >= 2 {
		p.Pts[n-2], p.Pts[n-1] = to.X, to.Y
		p.cur = to
	}
}
