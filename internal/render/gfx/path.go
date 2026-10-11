package gfx

import "math"

// PathOp is a path verb.
type PathOp uint8

const (
	OpMove PathOp = iota
	OpLine
	OpQuad
	OpCubic
	OpClose
)

// PathCmd is one verb and its points (Move/Line: 1, Quad: 2, Cubic: 3).
type PathCmd struct {
	Op  PathOp
	Pts [3]Point
}

// Path is a sequence of subpaths. Arcs and ellipses are converted to cubic
// Béziers when added, so every consumer only sees the five verbs above.
type Path struct {
	Cmds []PathCmd
	// current point and subpath start, for arcTo/close bookkeeping
	cur, start Point
	hasCur     bool
}

func (p *Path) MoveTo(x, y float64) {
	p.Cmds = append(p.Cmds, PathCmd{Op: OpMove, Pts: [3]Point{{x, y}}})
	p.cur, p.start, p.hasCur = Point{x, y}, Point{x, y}, true
}

// ensure starts a subpath at (x, y) when there is none (canvas semantics).
func (p *Path) ensure(x, y float64) {
	if !p.hasCur {
		p.MoveTo(x, y)
	}
}

func (p *Path) LineTo(x, y float64) {
	if !p.hasCur {
		p.MoveTo(x, y)
		return
	}
	p.Cmds = append(p.Cmds, PathCmd{Op: OpLine, Pts: [3]Point{{x, y}}})
	p.cur = Point{x, y}
}

func (p *Path) QuadTo(cx, cy, x, y float64) {
	p.ensure(cx, cy)
	p.Cmds = append(p.Cmds, PathCmd{Op: OpQuad, Pts: [3]Point{{cx, cy}, {x, y}}})
	p.cur = Point{x, y}
}

func (p *Path) CubicTo(c1x, c1y, c2x, c2y, x, y float64) {
	p.ensure(c1x, c1y)
	p.Cmds = append(p.Cmds, PathCmd{Op: OpCubic, Pts: [3]Point{{c1x, c1y}, {c2x, c2y}, {x, y}}})
	p.cur = Point{x, y}
}

func (p *Path) Close() {
	if !p.hasCur {
		return
	}
	p.Cmds = append(p.Cmds, PathCmd{Op: OpClose})
	p.cur = p.start
	// After closePath the next segment starts at the subpath's start point.
	p.Cmds = append(p.Cmds, PathCmd{Op: OpMove, Pts: [3]Point{p.start}})
}

// Current returns the current point and whether there is one.
func (p *Path) Current() (Point, bool) { return p.cur, p.hasCur }

// Empty reports a path with no drawing verbs.
func (p *Path) Empty() bool {
	for _, c := range p.Cmds {
		if c.Op != OpMove {
			return false
		}
	}
	return true
}

// Rect adds a closed rectangle subpath.
func (p *Path) Rect(x, y, w, h float64) {
	p.MoveTo(x, y)
	p.LineTo(x+w, y)
	p.LineTo(x+w, y+h)
	p.LineTo(x, y+h)
	p.Close()
}

// RoundRect adds a closed rectangle with elliptical corners, radii clamped
// per CSS Backgrounds §5.5 (scale all radii down when adjacent ones overlap).
func (p *Path) RoundRect(r Rect, rad Radii) {
	if rad.IsZero() {
		p.Rect(r.X, r.Y, r.W, r.H)
		return
	}
	rad = ClampRadii(r, rad)
	const k = 0.5522847498307936 // 4/3*(sqrt2-1)
	x0, y0, x1, y1 := r.X, r.Y, r.Right(), r.Bottom()
	tl, tr, br, bl := rad[0], rad[1], rad[2], rad[3]
	p.MoveTo(x0+tl.X, y0)
	p.LineTo(x1-tr.X, y0)
	if tr.X > 0 || tr.Y > 0 {
		p.CubicTo(x1-tr.X+tr.X*k, y0, x1, y0+tr.Y-tr.Y*k, x1, y0+tr.Y)
	}
	p.LineTo(x1, y1-br.Y)
	if br.X > 0 || br.Y > 0 {
		p.CubicTo(x1, y1-br.Y+br.Y*k, x1-br.X+br.X*k, y1, x1-br.X, y1)
	}
	p.LineTo(x0+bl.X, y1)
	if bl.X > 0 || bl.Y > 0 {
		p.CubicTo(x0+bl.X-bl.X*k, y1, x0, y1-bl.Y+bl.Y*k, x0, y1-bl.Y)
	}
	p.LineTo(x0, y0+tl.Y)
	if tl.X > 0 || tl.Y > 0 {
		p.CubicTo(x0, y0+tl.Y-tl.Y*k, x0+tl.X-tl.X*k, y0, x0+tl.X, y0)
	}
	p.Close()
}

// ClampRadii scales radii so adjacent corners never overlap (CSS rule).
func ClampRadii(r Rect, rad Radii) Radii {
	f := 1.0
	chk := func(sum, side float64) {
		if sum > 0 && side/sum < f {
			f = side / sum
		}
	}
	chk(rad[0].X+rad[1].X, r.W)
	chk(rad[3].X+rad[2].X, r.W)
	chk(rad[0].Y+rad[3].Y, r.H)
	chk(rad[1].Y+rad[2].Y, r.H)
	if f < 1 {
		for i := range rad {
			rad[i].X *= f
			rad[i].Y *= f
		}
	}
	return rad
}

// Ellipse adds an elliptical arc (canvas ellipse()): centre, radii,
// rotation, start and end angles in radians, direction.
func (p *Path) Ellipse(cx, cy, rx, ry, rot, a0, a1 float64, ccw bool) {
	sweep := a1 - a0
	tau := 2 * math.Pi
	if !ccw {
		if sweep >= tau {
			sweep = tau
		} else {
			sweep = math.Mod(sweep, tau)
			if sweep < 0 {
				sweep += tau
			}
		}
	} else {
		if -sweep >= tau {
			sweep = -tau
		} else {
			sweep = math.Mod(sweep, tau)
			if sweep > 0 {
				sweep -= tau
			}
		}
	}
	sr, cr := math.Sincos(rot)
	pt := func(a float64) Point {
		s, c := math.Sincos(a)
		x, y := rx*c, ry*s
		return Point{cx + x*cr - y*sr, cy + x*sr + y*cr}
	}
	start := pt(a0)
	if p.hasCur {
		p.LineTo(start.X, start.Y)
	} else {
		p.MoveTo(start.X, start.Y)
	}
	n := int(math.Ceil(math.Abs(sweep) / (math.Pi / 2)))
	if n == 0 {
		return
	}
	step := sweep / float64(n)
	k := 4.0 / 3.0 * math.Tan(step/4)
	a := a0
	for i := 0; i < n; i++ {
		b := a + step
		sa, ca := math.Sincos(a)
		sb, cb := math.Sincos(b)
		// control points on the unit circle, then scale/rotate
		p1 := Point{ca - k*sa, sa + k*ca}
		p2 := Point{cb + k*sb, sb - k*cb}
		tf := func(u Point) Point {
			x, y := rx*u.X, ry*u.Y
			return Point{cx + x*cr - y*sr, cy + x*sr + y*cr}
		}
		c1, c2, e := tf(p1), tf(p2), pt(b)
		p.CubicTo(c1.X, c1.Y, c2.X, c2.Y, e.X, e.Y)
		a = b
	}
}

// Arc adds a circular arc (canvas arc()).
func (p *Path) Arc(cx, cy, r, a0, a1 float64, ccw bool) { p.Ellipse(cx, cy, r, r, 0, a0, a1, ccw) }

// ArcTo adds canvas arcTo(): a line toward (x1,y1) then an arc of radius r
// tangent to both lines.
func (p *Path) ArcTo(x1, y1, x2, y2, r float64) {
	p.ensure(x1, y1)
	x0, y0 := p.cur.X, p.cur.Y
	if (x0 == x1 && y0 == y1) || (x1 == x2 && y1 == y2) || r == 0 {
		p.LineTo(x1, y1)
		return
	}
	v1x, v1y := x0-x1, y0-y1
	v2x, v2y := x2-x1, y2-y1
	l1, l2 := math.Hypot(v1x, v1y), math.Hypot(v2x, v2y)
	cross := v1x*v2y - v1y*v2x
	if math.Abs(cross) < 1e-9*l1*l2 { // collinear
		p.LineTo(x1, y1)
		return
	}
	cos := (v1x*v2x + v1y*v2y) / (l1 * l2)
	ang := math.Acos(math.Max(-1, math.Min(1, cos)))
	d := r / math.Tan(ang/2)
	t1 := Point{x1 + v1x/l1*d, y1 + v1y/l1*d}
	t2 := Point{x1 + v2x/l2*d, y1 + v2y/l2*d}
	// centre is along the bisector
	bx, by := v1x/l1+v2x/l2, v1y/l1+v2y/l2
	bl := math.Hypot(bx, by)
	h := r / math.Sin(ang/2)
	c := Point{x1 + bx/bl*h, y1 + by/bl*h}
	a0 := math.Atan2(t1.Y-c.Y, t1.X-c.X)
	a1 := math.Atan2(t2.Y-c.Y, t2.X-c.X)
	p.LineTo(t1.X, t1.Y)
	p.Ellipse(c.X, c.Y, r, r, 0, a0, a1, cross > 0)
}

// Transform returns a copy of the path with every point mapped through m.
func (p *Path) Transform(m Matrix) *Path {
	q := &Path{Cmds: make([]PathCmd, len(p.Cmds)), cur: m.Apply(p.cur), start: m.Apply(p.start), hasCur: p.hasCur}
	for i, c := range p.Cmds {
		q.Cmds[i] = c
		n := c.N()
		for j := 0; j < n; j++ {
			q.Cmds[i].Pts[j] = m.Apply(c.Pts[j])
		}
	}
	return q
}

// Append adds q's commands (optionally transformed) to p.
func (p *Path) Append(q *Path, m Matrix) {
	t := q
	if !m.IsIdentity() {
		t = q.Transform(m)
	}
	p.Cmds = append(p.Cmds, t.Cmds...)
	p.cur, p.start, p.hasCur = t.cur, t.start, t.hasCur
}

// N is the number of points the command carries.
func (c PathCmd) N() int {
	switch c.Op {
	case OpMove, OpLine:
		return 1
	case OpQuad:
		return 2
	case OpCubic:
		return 3
	}
	return 0
}

// Bounds returns the control-point bounding box of the path.
func (p *Path) Bounds() Rect {
	first := true
	var x0, y0, x1, y1 float64
	for _, c := range p.Cmds {
		for j := 0; j < c.N(); j++ {
			pt := c.Pts[j]
			if first {
				x0, y0, x1, y1, first = pt.X, pt.Y, pt.X, pt.Y, false
				continue
			}
			x0, y0 = math.Min(x0, pt.X), math.Min(y0, pt.Y)
			x1, y1 = math.Max(x1, pt.X), math.Max(y1, pt.Y)
		}
	}
	return Rect{x0, y0, x1 - x0, y1 - y0}
}
