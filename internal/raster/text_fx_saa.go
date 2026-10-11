package raster

import "math"

// Supersampling scan converter after SkScan_AntiPath (SHIFT=2): kept for
// comparison with the analytic one; Skia picks between them per path.

type saaEdge struct {
	x, dx    int // 16.16 in supersample columns
	first    int // first supersample row
	last     int // last supersample row (inclusive)
	wind     int
	curveCnt int
}

type saaBuilder struct {
	edges []saaEdge
}

func saaRound6(v int) int { return (v + 32) >> 6 }

func (b *saaBuilder) segment(x0, y0, x1, y1, wind int) bool {
	// x,y in 16.16 supersample units
	fy0, fy1 := y0>>10, y1>>10
	top, bot := saaRound6(fy0), saaRound6(fy1)
	if top == bot {
		return false
	}
	fx0, fx1 := x0>>10, x1>>10
	slope := int((int64(fx1-fx0) << 16) / int64(fy1-fy0))
	dy := ((top << 6) + 32) - fy0
	b.edges = append(b.edges, saaEdge{x: (fx0 + fxMul(slope, dy)) << 10, dx: slope, first: top, last: bot - 1, wind: wind})
	return true
}

func (b *saaBuilder) line(p0, p1 fpt) {
	x0, y0 := int(p0.x*256)<<10, int(p0.y*256)<<10
	x1, y1 := int(p1.x*256)<<10, int(p1.y*256)<<10
	wind := 1
	if y0 > y1 {
		x0, x1 = x1, x0
		y0, y1 = y1, y0
		wind = -1
	}
	b.segment(x0, y0, x1, y1, wind)
}

func (b *saaBuilder) quad(p [3]fpt) {
	a, bb, c := p[0].y, p[1].y, p[2].y
	ab, bc := a-bb, bb-c
	if ab < 0 {
		bc = -bc
	}
	if ab == 0 || bc < 0 {
		if t, ok := fxValidUnitDivide(a-bb, a-bb-bb+c); ok {
			p01, p12 := lerp32(p[0], p[1], t), lerp32(p[1], p[2], t)
			m := lerp32(p01, p12, t)
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

func (b *saaBuilder) monoQuad(q [3]fpt) {
	x0, y0 := int(q[0].x*256), int(q[0].y*256)
	x1, y1 := int(q[1].x*256), int(q[1].y*256)
	x2, y2 := int(q[2].x*256), int(q[2].y*256)
	wind := 1
	if y0 > y2 {
		x0, x2 = x2, x0
		y0, y2 = y2, y0
		wind = -1
	}
	if saaRound6(y0) == saaRound6(y2) {
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
	qx, qy := x0<<10, y0<<10
	qdx, qdy := bx+(ax>>shift), by+(ay>>shift)
	qddx, qddy := ax>>(shift-1), ay>>(shift-1)
	lastX, lastY := x2<<10, y2<<10
	cshift := shift - 1
	oldx, oldy := qx, qy
	for count > 0 {
		var newx, newy int
		count--
		if count > 0 {
			newx = oldx + (qdx >> cshift)
			newy = oldy + (qdy >> cshift)
			qdx += qddx
			qdy += qddy
		} else {
			newx, newy = lastX, lastY
		}
		b.segment(oldx, oldy, newx, newy, wind)
		oldx, oldy = newx, newy
	}
}

// saaFillPath rasterizes with 4x4 supersampling.
func saaFillPath(p *Path, clip IRect) *Mask {
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for i := 0; i < len(p.Pts); i += 2 {
		minX, maxX = math.Min(minX, p.Pts[i]), math.Max(maxX, p.Pts[i])
		minY, maxY = math.Min(minY, p.Pts[i+1]), math.Max(maxY, p.Pts[i+1])
	}
	if minX > maxX {
		return nil
	}
	bounds := IRect{int(math.Floor(minX)), int(math.Floor(minY)), int(math.Ceil(maxX)), int(math.Ceil(maxY))}
	b := bounds.Intersect(clip)
	if b.Empty() {
		return nil
	}
	ox, oy := float64(bounds.X0), float64(bounds.Y0)
	var sb saaBuilder
	pts := p.Pts
	i := 0
	var cur, start fpt
	has := false
	rel := func(x, y float64) fpt { return fpt{float32(x - ox), float32(y - oy)} }
	closeC := func() {
		if has && cur != start {
			sb.line(cur, start)
		}
		cur = start
	}
	for _, c := range p.Cmds {
		switch c {
		case 'M':
			closeC()
			start = rel(pts[i], pts[i+1])
			cur = start
			has = true
			i += 2
		case 'L':
			n := rel(pts[i], pts[i+1])
			sb.line(cur, n)
			cur = n
			i += 2
		case 'Q':
			p1, p2 := rel(pts[i], pts[i+1]), rel(pts[i+2], pts[i+3])
			sb.quad([3]fpt{cur, p1, p2})
			cur = p2
			i += 4
		case 'C':
			// flatten cubics coarsely (comparison tool only)
			p1, p2, p3 := rel(pts[i], pts[i+1]), rel(pts[i+2], pts[i+3]), rel(pts[i+4], pts[i+5])
			prev := cur
			for k := 1; k <= 16; k++ {
				t := float32(k) / 16
				mt := 1 - t
				n := fpt{mt*mt*mt*cur.x + 3*mt*mt*t*p1.x + 3*mt*t*t*p2.x + t*t*t*p3.x, mt*mt*mt*cur.y + 3*mt*mt*t*p1.y + 3*mt*t*t*p2.y + t*t*t*p3.y}
				sb.line(prev, n)
				prev = n
			}
			cur = p3
			i += 6
		case 'Z':
			closeC()
		}
	}
	closeC()
	w, h := bounds.W(), bounds.H()
	acc := make([]int, w*h)
	// sort-free approach: for every supersample row collect crossings
	rows := make([][]struct{ x, w int }, h*4)
	for _, e := range sb.edges {
		x := e.x
		for y := e.first; y <= e.last; y++ {
			if y >= 0 && y < h*4 {
				rows[y] = append(rows[y], struct{ x, w int }{x, e.wind})
			}
			x += e.dx
		}
	}
	for sy, r := range rows {
		if len(r) < 2 {
			continue
		}
		// insertion sort by x
		for i := 1; i < len(r); i++ {
			for j := i; j > 0 && r[j].x < r[j-1].x; j-- {
				r[j], r[j-1] = r[j-1], r[j]
			}
		}
		wnd := 0
		for i := 0; i < len(r)-1; i++ {
			wnd += r[i].w
			if wnd == 0 {
				continue
			}
			L := (r[i].x + 0x8000) >> 16
			R := (r[i+1].x + 0x8000) >> 16
			if R <= L {
				continue
			}
			py := sy >> 2
			maxv := 64
			if sy&3 == 3 {
				maxv = 63
			}
			start, stop := L, R
			if start < 0 {
				start = 0
			}
			if stop > w*4 {
				stop = w * 4
			}
			if stop <= start {
				continue
			}
			fb := start & 3
			fe := stop & 3
			n := (stop >> 2) - (start >> 2) - 1
			px := start >> 2
			if n < 0 {
				acc[py*w+px] += (fe - fb) * 16
				continue
			}
			if fb == 0 {
				n++
			} else {
				acc[py*w+px] += (4 - fb) * 16
				px++
			}
			for k := 0; k < n; k++ {
				acc[py*w+px] += maxv
				px++
			}
			if fe > 0 {
				acc[py*w+px] += fe * 16
			}
		}
	}
	m := newMask(b)
	for y := b.Y0; y < b.Y1; y++ {
		for x := b.X0; x < b.X1; x++ {
			v := acc[(y-bounds.Y0)*w+(x-bounds.X0)]
			if v > 255 {
				v = 255
			}
			m.A[(y-b.Y0)*m.Stride+(x-b.X0)] = uint8(v)
		}
	}
	return m
}
