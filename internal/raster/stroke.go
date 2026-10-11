package raster

import "math"

// Cap and join styles.
type (
	LineCap  int
	LineJoin int
)

const (
	CapButt LineCap = iota
	CapRound
	CapSquare
)

const (
	JoinMiter LineJoin = iota
	JoinRound
	JoinBevel
)

// StrokeStyle describes a stroke in the path's own coordinate space.
type StrokeStyle struct {
	Width      float64
	Cap        LineCap
	Join       LineJoin
	MiterLimit float64
	Dash       []float64
	DashOffset float64
}

func orient(poly []Pt) []Pt {
	var a float64
	n := len(poly)
	for i := 0; i < n; i++ {
		j := (i + 1) % n
		a += poly[i].X*poly[j].Y - poly[j].X*poly[i].Y
	}
	if a < 0 {
		for i, j := 0, n-1; i < j; i, j = i+1, j-1 {
			poly[i], poly[j] = poly[j], poly[i]
		}
	}
	return poly
}

func validDash(d []float64) []float64 {
	if len(d) == 0 {
		return nil
	}
	sum := 0.0
	for _, v := range d {
		if v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
			return nil
		}
		sum += v
	}
	if sum <= 0 {
		return nil
	}
	if len(d)%2 == 1 {
		d = append(append([]float64(nil), d...), d...)
	}
	return d
}

// ApplyDash splits polylines into dashes.
func ApplyDash(sp []Subpath, dash []float64, offset float64) []Subpath {
	dash = validDash(dash)
	if dash == nil {
		return sp
	}
	total := 0.0
	for _, v := range dash {
		total += v
	}
	var out []Subpath
	for _, s := range sp {
		pts := s.P
		sm := s.Smooth
		if s.Closed && len(pts) > 1 {
			pts = append(append([]Pt(nil), pts...), pts[0])
			sm = append(append([]bool(nil), sm...), false)
		}
		if len(pts) < 2 {
			continue
		}
		// position within pattern
		off := math.Mod(offset, total)
		if off < 0 {
			off += total
		}
		idx := 0
		for off >= dash[idx] {
			off -= dash[idx]
			idx = (idx + 1) % len(dash)
			if dash[idx] == 0 && off == 0 {
				break
			}
		}
		remain := dash[idx] - off
		on := idx%2 == 0
		var cur *Subpath
		startedOn := on
		var first, last *Subpath
		var firstIdx int
		emit := func() {
			if cur != nil {
				out = append(out, *cur)
				if first == nil {
					first = cur
					firstIdx = len(out) - 1
				}
				last = cur
				_ = last
				cur = nil
			}
		}
		if on {
			cur = &Subpath{P: []Pt{pts[0]}, Smooth: []bool{false}}
		}
		endedOn := false
		for i := 0; i+1 < len(pts); i++ {
			a, b := pts[i], pts[i+1]
			segLen := math.Hypot(b.X-a.X, b.Y-a.Y)
			if segLen == 0 {
				continue
			}
			pos := 0.0
			guard := 0
			for segLen-pos > remain && guard < 1<<20 {
				guard++
				pos += remain
				t := pos / segLen
				pt := Pt{a.X + (b.X-a.X)*t, a.Y + (b.Y-a.Y)*t}
				if on {
					cur.P = append(cur.P, pt)
					cur.Smooth = append(cur.Smooth, false)
					emit()
				} else {
					cur = &Subpath{P: []Pt{pt}, Smooth: []bool{false}}
				}
				on = !on
				idx = (idx + 1) % len(dash)
				remain = dash[idx]
			}
			remain -= segLen - pos
			if on {
				cur.P = append(cur.P, b)
				cur.Smooth = append(cur.Smooth, sm[i+1])
			}
		}
		endedOn = on
		if cur != nil {
			if len(cur.P) >= 1 {
				out = append(out, *cur)
				if first == nil {
					first = cur
					firstIdx = len(out) - 1
				}
				last = &out[len(out)-1]
			}
			cur = nil
		}
		// merge last dash with first for closed paths that start and end "on"
		if s.Closed && startedOn && endedOn && first != nil && last != nil && len(out) >= 2 && firstIdx != len(out)-1 {
			l := out[len(out)-1]
			f := out[firstIdx]
			l.P = append(l.P, f.P[1:]...)
			l.Smooth = append(l.Smooth, f.Smooth[1:]...)
			out[firstIdx] = l
			out = out[:len(out)-1]
		}
	}
	return out
}

type stroker struct {
	hw    float64
	st    StrokeStyle
	tol   float64
	out   []Subpath
	round []Pt // unit-circle fan template cache not used
}

func (k *stroker) emit(poly []Pt) {
	if len(poly) >= 3 {
		k.out = append(k.out, Subpath{P: orient(poly), Closed: true})
	}
}

func (k *stroker) arcSteps(angle float64) int {
	step := 2 * math.Acos(math.Max(0, 1-k.tol/math.Max(k.hw, k.tol)))
	if step < 0.05 {
		step = 0.05
	}
	n := int(math.Ceil(math.Abs(angle) / step))
	if n < 1 {
		n = 1
	}
	return n
}

// StrokePolys converts polylines into fill polygons (to be rendered with the
// non-zero rule).
func StrokePolys(sp []Subpath, st StrokeStyle, tol float64) []Subpath {
	if st.Width <= 0 || math.IsNaN(st.Width) {
		return nil
	}
	if st.MiterLimit <= 0 {
		st.MiterLimit = 10
	}
	if d := validDash(st.Dash); d != nil {
		sp = ApplyDash(sp, d, st.DashOffset)
	}
	k := &stroker{hw: st.Width / 2, st: st, tol: tol}
	for _, s := range sp {
		k.subpath(s)
	}
	return k.out
}

func (k *stroker) subpath(s Subpath) {
	// dedupe
	pts := make([]Pt, 0, len(s.P))
	sm := make([]bool, 0, len(s.P))
	for i, p := range s.P {
		if len(pts) > 0 && pts[len(pts)-1] == p {
			continue
		}
		pts = append(pts, p)
		sm = append(sm, s.Smooth[i])
	}
	closed := s.Closed
	if closed && len(pts) > 1 && pts[0] == pts[len(pts)-1] {
		pts = pts[:len(pts)-1]
		sm = sm[:len(sm)-1]
	}
	hw := k.hw
	if len(pts) == 1 {
		p := pts[0]
		switch k.st.Cap {
		case CapRound:
			k.emit(k.disc(p, 0, 2*math.Pi))
		case CapSquare:
			k.emit([]Pt{{p.X - hw, p.Y - hw}, {p.X + hw, p.Y - hw}, {p.X + hw, p.Y + hw}, {p.X - hw, p.Y + hw}})
		}
		return
	}
	if len(pts) < 2 {
		return
	}
	n := len(pts)
	segs := n - 1
	if closed {
		segs = n
		if n == 2 { // degenerate closed: treat as there-and-back
			segs = 2
		}
	}
	dir := make([]Pt, segs)
	for i := 0; i < segs; i++ {
		a, b := pts[i], pts[(i+1)%n]
		d := Pt{b.X - a.X, b.Y - a.Y}
		l := math.Hypot(d.X, d.Y)
		dir[i] = Pt{d.X / l, d.Y / l}
	}
	for i := 0; i < segs; i++ {
		a, b := pts[i], pts[(i+1)%n]
		nx, ny := -dir[i].Y*hw, dir[i].X*hw
		k.emit([]Pt{{a.X + nx, a.Y + ny}, {b.X + nx, b.Y + ny}, {b.X - nx, b.Y - ny}, {a.X - nx, a.Y - ny}})
	}
	// joins
	for i := 1; i < segs; i++ {
		k.join(pts[i], dir[i-1], dir[i], sm[i])
	}
	if closed {
		k.join(pts[0], dir[segs-1], dir[0], sm[0])
	} else {
		// caps
		k.capAt(pts[0], Pt{-dir[0].X, -dir[0].Y})
		k.capAt(pts[n-1], dir[segs-1])
	}
}

func (k *stroker) disc(c Pt, a0, a1 float64) []Pt {
	n := k.arcSteps(a1 - a0)
	poly := make([]Pt, 0, n+1)
	for i := 0; i < n; i++ {
		s, co := math.Sincos(a0 + (a1-a0)*float64(i)/float64(n))
		poly = append(poly, Pt{c.X + co*k.hw, c.Y + s*k.hw})
	}
	return poly
}

// capAt draws the cap at endpoint p where d is the outward direction.
func (k *stroker) capAt(p, d Pt) {
	hw := k.hw
	switch k.st.Cap {
	case CapSquare:
		nx, ny := -d.Y*hw, d.X*hw
		k.emit([]Pt{{p.X + nx, p.Y + ny}, {p.X + nx + d.X*hw, p.Y + ny + d.Y*hw}, {p.X - nx + d.X*hw, p.Y - ny + d.Y*hw}, {p.X - nx, p.Y - ny}})
	case CapRound:
		base := math.Atan2(d.Y, d.X)
		n := k.arcSteps(math.Pi)
		poly := make([]Pt, 0, n+1)
		for i := 0; i <= n; i++ {
			s, c := math.Sincos(base - math.Pi/2 + math.Pi*float64(i)/float64(n))
			poly = append(poly, Pt{p.X + c*hw, p.Y + s*hw})
		}
		k.emit(poly)
	}
}

func (k *stroker) join(v, d0, d1 Pt, smooth bool) {
	hw := k.hw
	cross := d0.X*d1.Y - d0.Y*d1.X
	dot := d0.X*d1.X + d0.Y*d1.Y
	if math.Abs(cross) < 1e-12 && dot > 0 {
		return
	}
	s := 1.0
	if cross > 0 {
		s = -1
	}
	n0 := Pt{-d0.Y * hw * s, d0.X * hw * s}
	n1 := Pt{-d1.Y * hw * s, d1.X * hw * s}
	a := Pt{v.X + n0.X, v.Y + n0.Y}
	b := Pt{v.X + n1.X, v.Y + n1.Y}
	jn := k.st.Join
	if smooth {
		jn = JoinMiter
	}
	switch jn {
	case JoinRound:
		a0 := math.Atan2(n0.Y, n0.X)
		a1 := math.Atan2(n1.Y, n1.X)
		d := a1 - a0
		for d > math.Pi {
			d -= 2 * math.Pi
		}
		for d < -math.Pi {
			d += 2 * math.Pi
		}
		n := k.arcSteps(d)
		poly := make([]Pt, 0, n+3)
		poly = append(poly, v)
		for i := 0; i <= n; i++ {
			sn, cs := math.Sincos(a0 + d*float64(i)/float64(n))
			poly = append(poly, Pt{v.X + cs*hw, v.Y + sn*hw})
		}
		k.emit(poly)
	case JoinMiter:
		limit := k.st.MiterLimit
		if smooth {
			limit = math.Inf(1)
		}
		den := 1 + dot
		if den > 1e-9 {
			ratio := math.Sqrt(2 / den)
			if ratio <= limit {
				tip := Pt{v.X + (n0.X+n1.X)/den, v.Y + (n0.Y+n1.Y)/den}
				k.emit([]Pt{v, a, tip, b})
				return
			}
		}
		k.emit([]Pt{v, a, b})
	default:
		k.emit([]Pt{v, a, b})
	}
}

// StrokeMask rasterizes the stroke of p (in path space) transformed by m.
func StrokeMask(p *Path, m Matrix, st StrokeStyle, clip IRect) *Mask {
	sc := m.MaxScale()
	if sc == 0 || math.IsNaN(sc) {
		return nil
	}
	tol := 0.05 / sc
	sp := p.Flatten(tol)
	polys := StrokePolys(sp, st, tol)
	if !m.IsIdentity() {
		for i := range polys {
			for j, q := range polys[i].P {
				polys[i].P[j] = m.ApplyPt(q)
			}
		}
	}
	return RasterizePolys(polys, NonZero, clip)
}
