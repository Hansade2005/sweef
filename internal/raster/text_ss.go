package raster

import (
	"math"
	"sync"

	"github.com/sweefbrowser/sweef/internal/font"
)

// Chromium's glyph masks come from Skia's analytic anti-aliasing scan
// converter: coverage is the exact area of the outline, but every edge
// endpoint (including the points where curves are flattened) is snapped to
// 1/4 pixel vertically and 1/256 pixel horizontally. That quantization is
// visible in every horizontal stem, so it is reproduced here. Outlines are
// unhinted and sit on FreeType's 26.6 grid, translated by the 1/4 pixel
// subpixel position.

// textFxMode selects the glyph scan converter: 0 fixed-point Skia edges,
// 1 float snapped polygons, 2 exact curves (experiments only).
var textFxMode = 0
var textFxYOff = 0

func aaaSnapY(y float64) float64 {
	y = math.Floor(y*256+0.5) / 256
	return math.Floor(y*4+0.5) / 4
}

func aaaSnapX(x float64) float64 { return math.Floor(x*256+0.5) / 256 }

func aaaPt(x, y float64) Pt { return Pt{aaaSnapX(x), aaaSnapY(y)} }

func aaaCheapDist(dx, dy int) int {
	if dx < 0 {
		dx = -dx
	}
	if dy < 0 {
		dy = -dy
	}
	if dx > dy {
		return dx + dy>>1
	}
	return dy + dx>>1
}

func aaaShift(dx, dy int) int {
	dist := aaaCheapDist(dx, dy)
	dist = (dist + (1 << 4)) >> (3 + 2)
	n := 0
	for v := dist; v > 0; v >>= 1 {
		n++
	}
	return n >> 1
}

func aaaCubicDelta(a, b, c, d int) int {
	one := ((a*8 - b*15 + 6*c + d) * 19) >> 9
	two := ((a + 6*b - c*15 + d*8) * 19) >> 9
	if one < 0 {
		one = -one
	}
	if two < 0 {
		two = -two
	}
	if one > two {
		return one
	}
	return two
}

func aaaQuad(dst []Pt, x0, y0, x1, y1, x2, y2 float64) []Pt {
	f := func(v float64) int { return int(v * 4 * 64) }
	dx := ((f(x1) << 1) - f(x0) - f(x2)) >> 2
	dy := ((f(y1) << 1) - f(y0) - f(y2)) >> 2
	sh := aaaShift(dx, dy)
	if sh == 0 {
		sh = 1
	} else if sh > 6 {
		sh = 6
	}
	n := 1 << sh
	// Skia's analytic quad edge walks the curve from its top end: the
	// forward differencing starts at the snapped top y, interior vertices are
	// snapped to 1/4 pixel (never past the snapped bottom y) and the bottom
	// vertex is the snapped end point.
	rev := false
	if y0 > y2 {
		x0, y0, x2, y2 = x2, y2, x0, y0
		rev = true
	}
	top := aaaSnapY(y0)
	off := top - y0
	lastY := aaaSnapY(y2)
	start := len(dst)
	for k := 1; k <= n; k++ {
		t := float64(k) / float64(n)
		mt := 1 - t
		var px, py float64
		if k == n {
			px, py = x2, lastY
		} else {
			px = mt*mt*x0 + 2*mt*t*x1 + t*t*x2
			py = aaaSnapY(mt*mt*y0 + 2*mt*t*y1 + t*t*y2 + off)
			if py > lastY {
				py = lastY
			}
		}
		dst = append(dst, Pt{aaaSnapX(px), py})
	}
	// vertices were generated top to bottom; the caller expects the curve in
	// its original direction, ending at the original end point
	if rev {
		seg := dst[start:]
		// seg holds points after the top start; build the full list including
		// the top vertex, reversed, excluding the original start.
		full := make([]Pt, 0, len(seg)+1)
		full = append(full, Pt{aaaSnapX(x0), top})
		full = append(full, seg...)
		// reverse to the original direction and drop its first element (the
		// original start, already supplied by the caller)
		for a, b := 0, len(full)-1; a < b; a, b = a+1, b-1 {
			full[a], full[b] = full[b], full[a]
		}
		dst = append(dst[:start], full[1:]...)
	}
	return dst
}

func lerpPt(a, b Pt, t float64) Pt { return Pt{a.X + (b.X-a.X)*t, a.Y + (b.Y-a.Y)*t} }

// aaaCubicChopped splits a cubic at its y extrema (as Skia's edge builder
// does) and flattens every monotonic piece.
func aaaCubicChopped(dst []Pt, c [4]Pt) []Pt {
	// dy/dt = 3(a t^2 + b t + c)
	a := -c[0].Y + 3*c[1].Y - 3*c[2].Y + c[3].Y
	b := 2 * (c[0].Y - 2*c[1].Y + c[2].Y)
	k := c[1].Y - c[0].Y
	var ts []float64
	if math.Abs(a) < 1e-12 {
		if math.Abs(b) > 1e-12 {
			ts = append(ts, -k/b)
		}
	} else if d := b*b - 4*a*k; d >= 0 {
		sq := math.Sqrt(d)
		ts = append(ts, (-b-sq)/(2*a), (-b+sq)/(2*a))
	}
	var cut []float64
	for _, t := range ts {
		if t > 1e-6 && t < 1-1e-6 {
			cut = append(cut, t)
		}
	}
	if len(cut) == 2 && cut[0] > cut[1] {
		cut[0], cut[1] = cut[1], cut[0]
	}
	rest := c
	prev := 0.0
	for _, t := range cut {
		u := (t - prev) / (1 - prev)
		l, r := splitCubic(rest, u)
		dst = aaaCubic(dst, l)
		rest = r
		prev = t
	}
	return aaaCubic(dst, rest)
}

func splitCubic(c [4]Pt, t float64) (l, r [4]Pt) {
	p01, p12, p23 := lerpPt(c[0], c[1], t), lerpPt(c[1], c[2], t), lerpPt(c[2], c[3], t)
	p012, p123 := lerpPt(p01, p12, t), lerpPt(p12, p23, t)
	m := lerpPt(p012, p123, t)
	return [4]Pt{c[0], p01, p012, m}, [4]Pt{m, p123, p23, c[3]}
}

func aaaCubic(dst []Pt, c [4]Pt) []Pt {
	f := func(v float64) int { return int(v * 4 * 64) }
	dx := aaaCubicDelta(f(c[0].X), f(c[1].X), f(c[2].X), f(c[3].X))
	dy := aaaCubicDelta(f(c[0].Y), f(c[1].Y), f(c[2].Y), f(c[3].Y))
	sh := aaaShift(dx, dy) + 1
	if sh > 6 {
		sh = 6
	}
	n := 1 << sh
	rev := false
	if c[0].Y > c[3].Y {
		c[0], c[3] = c[3], c[0]
		c[1], c[2] = c[2], c[1]
		rev = true
	}
	top := aaaSnapY(c[0].Y)
	off := top - c[0].Y
	lastY := aaaSnapY(c[3].Y)
	start := len(dst)
	for k := 1; k <= n; k++ {
		t := float64(k) / float64(n)
		mt := 1 - t
		var px, py float64
		if k == n {
			px, py = c[3].X, lastY
		} else {
			a, b, cc, d := mt*mt*mt, 3*mt*mt*t, 3*mt*t*t, t*t*t
			px = a*c[0].X + b*c[1].X + cc*c[2].X + d*c[3].X
			py = aaaSnapY(a*c[0].Y + b*c[1].Y + cc*c[2].Y + d*c[3].Y + off)
			if py > lastY {
				py = lastY
			}
		}
		dst = append(dst, Pt{aaaSnapX(px), py})
	}
	if rev {
		seg := dst[start:]
		full := make([]Pt, 0, len(seg)+1)
		full = append(full, Pt{aaaSnapX(c[0].X), top})
		full = append(full, seg...)
		for a, b := 0, len(full)-1; a < b; a, b = a+1, b-1 {
			full[a], full[b] = full[b], full[a]
		}
		dst = append(dst[:start], full[1:]...)
	}
	return dst
}

// aaaFlatten converts p (device pixels, y down) to polygons with Skia's
// curve subdivision and edge-endpoint snapping.
func aaaFlatten(p *Path) []Subpath {
	var out []Subpath
	var cur *Subpath
	pts := p.Pts
	i := 0
	var cx, cy float64
	for _, c := range p.Cmds {
		switch c {
		case 'M':
			out = append(out, Subpath{Closed: true})
			cur = &out[len(out)-1]
			cx, cy = pts[i], pts[i+1]
			cur.P = append(cur.P, aaaPt(cx, cy))
			i += 2
		case 'L':
			cx, cy = pts[i], pts[i+1]
			cur.P = append(cur.P, aaaPt(cx, cy))
			i += 2
		case 'Q':
			x1, y1, x2, y2 := pts[i], pts[i+1], pts[i+2], pts[i+3]
			// Skia chops quads at their y extremum before building edges
			den := cy - 2*y1 + y2
			if den != 0 {
				if t := (cy - y1) / den; t > 0 && t < 1 {
					ax, ay := cx+(x1-cx)*t, cy+(y1-cy)*t
					bx, by := x1+(x2-x1)*t, y1+(y2-y1)*t
					mx, my := ax+(bx-ax)*t, ay+(by-ay)*t
					cur.P = aaaQuad(cur.P, cx, cy, ax, ay, mx, my)
					cur.P = aaaQuad(cur.P, mx, my, bx, by, x2, y2)
					cx, cy = x2, y2
					i += 4
					continue
				}
			}
			cur.P = aaaQuad(cur.P, cx, cy, x1, y1, x2, y2)
			cx, cy = x2, y2
			i += 4
		case 'C':
			x1, y1, x2, y2, x3, y3 := pts[i], pts[i+1], pts[i+2], pts[i+3], pts[i+4], pts[i+5]
			cur.P = aaaCubicChopped(cur.P, [4]Pt{{cx, cy}, {x1, y1}, {x2, y2}, {x3, y3}})
			cx, cy = x3, y3
			i += 6
		}
	}
	return out
}

type guKey struct {
	f   *font.Face
	r   rune
	gid int
}

var (
	guMu    sync.Mutex
	guCache = map[guKey][]font.Segment{}
)

// glyphUnits returns the outline in font units (y down), cached.
func glyphUnits(g Glyph) []font.Segment {
	f, r := g.Face, g.R
	k := guKey{f, r, g.GID}
	guMu.Lock()
	s, ok := guCache[k]
	guMu.Unlock()
	if ok {
		return s
	}
	if g.GID != 0 {
		s = f.OutlineGID(g.GID, float64(f.UnitsPerEm()))
	} else {
		s = f.Outline(r, float64(f.UnitsPerEm()))
	}
	guMu.Lock()
	guCache[k] = s
	guMu.Unlock()
	return s
}

func ftMulFix(a, b int) int {
	s := 1
	if a < 0 {
		a, s = -a, -s
	}
	if b < 0 {
		b, s = -b, -s
	}
	return s * int((int64(a)*int64(b)+0x8000)>>16)
}

func ftDivFix(a, b int) int {
	s := 1
	if a < 0 {
		a, s = -a, -s
	}
	if b < 0 {
		b, s = -b, -s
	}
	if b == 0 {
		return 0x7FFFFFFF
	}
	return s * int((int64(a)<<16+int64(b)/2)/int64(b))
}

// skiaGlyph rasterizes glyph r of f at size px with the given quarter-pixel
// subpixel offset. It returns the mask positioned relative to the glyph
// origin (baseline-left), or nil for glyphs without outline.
func skiaGlyph(g Glyph, sizeX, sizeY float64, sub int, skew bool) *Mask {
	f := g.Face
	segs := glyphUnits(g)
	if len(segs) == 0 {
		return nil
	}
	upm := f.UnitsPerEm()
	scaleX := ftDivFix(int(sizeX*64), upm)
	scaleY := ftDivFix(int(sizeY*64), upm)
	skewFix := 0
	if skew {
		skewFix = -0x4000 // -0.25 in 16.16: x' = x - 0.25*y(down)
	}
	conv := func(x, y float64) (float64, float64) {
		X := ftMulFix(int(math.Round(x)), scaleX)
		Y := ftMulFix(int(math.Round(y)), scaleY)
		if skew {
			X += ftMulFix(Y, skewFix)
		}
		X += sub * 16
		Y += textFxYOff
		return float64(X) / 64, float64(Y) / 64
	}
	p := NewPath()
	started := false
	for _, s := range segs {
		switch s.Op {
		case 'M':
			if started {
				p.Close()
			}
			x, y := conv(s.X0, s.Y0)
			p.MoveTo(x, y)
			started = true
		case 'L':
			x, y := conv(s.X0, s.Y0)
			p.LineTo(x, y)
		case 'Q':
			x1, y1 := conv(s.X0, s.Y0)
			x2, y2 := conv(s.X1, s.Y1)
			p.QuadTo(x1, y1, x2, y2)
		case 'C':
			x1, y1 := conv(s.X0, s.Y0)
			x2, y2 := conv(s.X1, s.Y1)
			x3, y3 := conv(s.X2, s.Y2)
			p.CubicTo(x1, y1, x2, y2, x3, y3)
		}
	}
	if started {
		p.Close()
	}
	// bounds
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for i := 0; i < len(p.Pts); i += 2 {
		minX = math.Min(minX, p.Pts[i])
		maxX = math.Max(maxX, p.Pts[i])
		minY = math.Min(minY, p.Pts[i+1])
		maxY = math.Max(maxY, p.Pts[i+1])
	}
	if minX > maxX {
		return nil
	}
	ox, oy := int(math.Floor(minX)), int(math.Floor(minY))
	w := int(math.Ceil(maxX)) - ox
	h := int(math.Ceil(maxY)) - oy
	if w <= 0 || h <= 0 || w > 4096 || h > 4096 {
		return nil
	}
	if textFxMode == 3 {
		return saaFillPath(p, IRect{ox, oy, ox + w, oy + h})
	}
	if textFxMode == 0 {
		return fxFillPath(p, IRect{ox, oy, ox + w, oy + h})
	}
	if textFxMode == 2 {
		return RasterizePolys(p.Flatten(0.01), NonZero, IRect{ox, oy, ox + w, oy + h})
	}
	m := RasterizePolys(aaaFlatten(p), NonZero, IRect{ox, oy, ox + w, oy + h})
	return m
}
