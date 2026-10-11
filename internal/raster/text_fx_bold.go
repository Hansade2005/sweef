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
