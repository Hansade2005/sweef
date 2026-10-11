package raster

import (
	"math"

	"github.com/sweefbrowser/sweef/internal/render"
)

// pathLength is the flattened length of the closed subpaths of p.
func pathLength(p *Path) float64 {
	var l float64
	for _, sp := range p.Flatten(0.05) {
		for i := 1; i < len(sp.P); i++ {
			l += math.Hypot(sp.P[i].X-sp.P[i-1].X, sp.P[i].Y-sp.P[i-1].Y)
		}
		if sp.Closed && len(sp.P) > 1 {
			l += math.Hypot(sp.P[0].X-sp.P[len(sp.P)-1].X, sp.P[0].Y-sp.P[len(sp.P)-1].Y)
		}
	}
	return l
}

// selectBestDashGap mirrors Blink's StrokeData::SelectBestDashGap.
func selectBestDashGap(strokeLen, dashLen, gapLen float64, closed bool) float64 {
	avail := strokeLen
	if !closed {
		avail += gapLen
	}
	minDashes := math.Floor(avail / (dashLen + gapLen))
	maxDashes := minDashes + 1
	minGaps, maxGaps := minDashes-1, maxDashes-1
	if closed {
		minGaps, maxGaps = minDashes, maxDashes
	}
	if minGaps <= 0 {
		return gapLen
	}
	minGap := (strokeLen - minDashes*dashLen) / minGaps
	maxGap := (strokeLen - maxDashes*dashLen) / maxGaps
	if maxGap <= 0 || math.Abs(minGap-gapLen) < math.Abs(maxGap-gapLen) {
		return minGap
	}
	return maxGap
}

// dashedRounded paints one side of a dashed/dotted border whose box has
// rounded corners: the dash pattern runs along the closed centre line of the
// whole border and is clipped to the side's region.
func (p *Painter) dashedRounded(r render.Rect, rad render.Radii, w [4]float64, i int, quad []Pt, color Color, dotted bool) {
	c := p.c
	bw := w[i]
	cr, crad := insetRRect(r, rad, w[3]/2, w[0]/2, w[1]/2, w[2]/2)
	if cr.W <= 0 || cr.H <= 0 {
		return
	}
	centre := rrPathFrom(cr, crad, borderStart)
	length := pathLength(centre)
	if length <= 0 {
		return
	}
	var mk *Mask
	if dotted {
		gap := selectBestDashGap(length, bw, bw, true)
		period := bw + gap
		n := math.Max(1, math.Round(length/period))
		period = length / n
		dp := NewPath()
		walkPath(centre, period, func(x, y float64) { dp.Ellipse(x, y, bw/2, bw/2) })
		mk = p.maskOf(dp, NonZero)
	} else {
		dash, gapR := 2*bw, bw
		if bw < 3 {
			dash, gapR = 3*bw, 2*bw
		}
		if length <= dash*2 {
			mk = StrokeMask(centre, c.Transform(), StrokeStyle{Width: bw, Cap: CapButt, Join: JoinMiter, MiterLimit: 10}, c.drawBounds())
		} else {
			gap := selectBestDashGap(length, dash, gapR, true)
			ss := StrokeStyle{Width: bw, Cap: CapButt, Join: JoinMiter, MiterLimit: 10, Dash: []float64{dash, gap}}
			mk = StrokeMask(centre, c.Transform(), ss, c.drawBounds())
		}
	}
	if mk == nil {
		return
	}
	ring := p.ring(r, rad, 0, 0, 0, 0, w[3], w[0], w[1], w[2])
	rm := p.maskOf(ring, EvenOdd)
	if quad == nil {
		mk = maskMul(mk, rm)
		c.draw(mk, Solid(color))
		return
	}
	qp := NewPath()
	qp.MoveTo(quad[0].X, quad[0].Y)
	for _, q := range quad[1:] {
		qp.LineTo(q.X, q.Y)
	}
	qp.Close()
	mk = maskMul(mk, maskMul(rm, p.maskOf(qp, NonZero)))
	c.draw(mk, Solid(color))
}

// borderStart is the Skia rrect start index (see rrPathStart) used for the
// dash phase of rounded dashed/dotted borders.
var borderStart = 0

// rrPathFrom builds a clockwise rounded rect path starting at Skia anchor k.
func rrPathFrom(r render.Rect, rad render.Radii, k int) *Path {
	return rrPathStart(r, rad, k)
}

// rrPathStart builds a clockwise rounded rect whose first anchor is Skia's
// rrect point k: 0 (l+rx,t) 1 (r-rx,t) 2 (r,t+ry) 3 (r,b-ry) 4 (r-rx,b)
// 5 (l+rx,b) 6 (l,b-ry) 7 (l,t+ry).
func rrPathStart(r render.Rect, rad render.Radii, k int) *Path {
	x0, y0, x1, y1 := r.X, r.Y, r.X+r.W, r.Y+r.H
	pts := [8]Pt{
		{x0 + rad[0][0], y0}, {x1 - rad[1][0], y0}, {x1, y0 + rad[1][1]}, {x1, y1 - rad[2][1]},
		{x1 - rad[2][0], y1}, {x0 + rad[3][0], y1}, {x0, y1 - rad[3][1]}, {x0, y0 + rad[0][1]},
	}
	arc := func(p *Path, j int) { // arc from pts[j] to pts[j+1]
		switch j {
		case 1:
			cx, cy, rx, ry := x1-rad[1][0], y0+rad[1][1], rad[1][0], rad[1][1]
			if rx > 0 && ry > 0 {
				p.CubicTo(cx+rx*kappa, cy-ry, cx+rx, cy-ry*kappa, cx+rx, cy)
			}
		case 3:
			cx, cy, rx, ry := x1-rad[2][0], y1-rad[2][1], rad[2][0], rad[2][1]
			if rx > 0 && ry > 0 {
				p.CubicTo(cx+rx, cy+ry*kappa, cx+rx*kappa, cy+ry, cx, cy+ry)
			}
		case 5:
			cx, cy, rx, ry := x0+rad[3][0], y1-rad[3][1], rad[3][0], rad[3][1]
			if rx > 0 && ry > 0 {
				p.CubicTo(cx-rx*kappa, cy+ry, cx-rx, cy+ry*kappa, cx-rx, cy)
			}
		case 7:
			cx, cy, rx, ry := x0+rad[0][0], y0+rad[0][1], rad[0][0], rad[0][1]
			if rx > 0 && ry > 0 {
				p.CubicTo(cx-rx, cy-ry*kappa, cx-rx*kappa, cy-ry, cx, cy-ry)
			}
		}
	}
	pa := NewPath()
	pa.MoveTo(pts[k%8].X, pts[k%8].Y)
	for n := 0; n < 8; n++ {
		j := (k + n) % 8
		nx := pts[(j+1)%8]
		if j%2 == 0 {
			pa.LineTo(nx.X, nx.Y)
		} else {
			arc(pa, j)
		}
	}
	pa.Close()
	return pa
}

// walkPath calls fn at every `period` of arc length along p (starting at 0).
func walkPath(p *Path, period float64, fn func(x, y float64)) {
	next := 0.0
	var acc float64
	for _, sp := range p.Flatten(0.05) {
		pts := sp.P
		if sp.Closed && len(pts) > 0 {
			pts = append(append([]Pt(nil), pts...), pts[0])
		}
		for i := 1; i < len(pts); i++ {
			seg := math.Hypot(pts[i].X-pts[i-1].X, pts[i].Y-pts[i-1].Y)
			for next <= acc+seg+1e-9 && seg > 0 {
				t := (next - acc) / seg
				fn(pts[i-1].X+(pts[i].X-pts[i-1].X)*t, pts[i-1].Y+(pts[i].Y-pts[i-1].Y)*t)
				next += period
			}
			acc += seg
		}
	}
}
