package raster

import "math"

// Synthetic bold. Chromium on Linux does not use FreeType's embolden: Skia
// turns the glyph into "stroke and fill" with a frame width of
// size * interp(size) (1/24 at 9px down to 1/32 at 36px), a miter join and
// a miter limit of 4, and scan converts the outline of the stroke together
// with the glyph with the same analytic anti-aliasing as plain glyphs.

func fxFakeBoldExtra(size float64) float64 {
	const lo, hi = 1.0 / 24, 1.0 / 32
	switch {
	case size <= 9:
		return size * lo
	case size >= 36:
		return size * hi
	}
	return size * (lo + (size-9)/(36-9)*(hi-lo))
}

// fxLargestArea returns the signed area of the biggest contour of p.
func fxLargestArea(p *Path) float64 {
	var best float64
	for _, sp := range p.Flatten(0.05) {
		if a := fxSignedArea(sp.P); math.Abs(a) > math.Abs(best) {
			best = a
		}
	}
	return best
}

func fxSignedArea(pts []Pt) float64 {
	var a float64
	n := len(pts)
	for i := range pts {
		j := (i + 1) % n
		a += pts[i].X*pts[j].Y - pts[j].X*pts[i].Y
	}
	return a / 2
}

// skiaGlyphBold rasterizes the synthetic bold form of g.
func skiaGlyphBold(g Glyph, sizeX, sizeY float64, sub int, skew bool) *Mask {
	p := fxGlyphPath(g, sizeX, sizeY, sub, skew)
	if p == nil {
		return nil
	}
	stroke := fxStrokePath(p, fxFakeBoldExtra(sizeY), JoinMiter, CapButt, 4, 1)
	// Skia reverses the source when it winds the other way from the stroke
	flip := fxLargestArea(p)*fxLargestArea(stroke) < 0
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, pp := range []*Path{p, stroke} {
		for i := 0; i < len(pp.Pts); i += 2 {
			minX, maxX = math.Min(minX, pp.Pts[i]), math.Max(maxX, pp.Pts[i])
			minY, maxY = math.Min(minY, pp.Pts[i+1]), math.Max(maxY, pp.Pts[i+1])
		}
	}
	if minX > maxX {
		return nil
	}
	ox, oy := int(math.Floor(minX)), int(math.Floor(minY))
	w, h := int(math.Ceil(maxX))-ox, int(math.Ceil(maxY))-oy
	if w <= 0 || h <= 0 || w > 4096 || h > 4096 {
		return nil
	}
	return fxTrim(fxFillUnion(p, stroke, flip, IRect{ox, oy, ox + w, oy + h}))
}
