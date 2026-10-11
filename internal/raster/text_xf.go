package raster

import (
	"math"
	"sync"

	"github.com/sweefbrowser/sweef/internal/font"
)

// Text under a general (rotated, skewed, non-uniformly scaled) transform.
//
// Skia hands every glyph mask to FreeType in the same way, whatever the
// device matrix: the 2x2 matrix is split by a QR decomposition into two
// scales, which become FreeType's 26.6 pixel sizes, and a remaining matrix
// that is applied afterwards, in single precision, to the path built from the
// scaled outline (so the points of a rotated glyph are not on FreeType's 26.6
// grid, and which side of a quarter-pixel snap a vertex falls on is decided by
// float arithmetic). The mask is the usual snapped analytic scan conversion of
// that path translated by a subpixel offset that is quantized to a quarter
// pixel along both axes, except along the axis the baseline stays parallel to
// (then that axis rounds to a whole pixel). Every entry of the matrix is first
// rounded to 1/1024 (sk_relax), so a rotation by 90 degrees is exact.

// xfRelax rounds a matrix entry to 1/1024 like Skia's sk_relax.
func xfRelax(v float64) float64 { return math.Round(v*1024) / 1024 }

func relax32(v float32) float32 { return float32(math.Round(float64(v)*1024)) / 1024 }

// xfAxis classifies how the text baseline maps onto the device axes (Skia's
// computeAxisAlignmentForHText): 1 when the x axis stays horizontal (subpixel
// positioning along x only), 2 when it becomes vertical (subpixel along y
// only), 0 otherwise (both axes).
func xfAxis(a, b, c, d float64) int {
	if xfRelax(b) == 0 {
		return 1
	}
	if xfRelax(a) == 0 {
		return 2
	}
	return 0
}

// xfPlace maps a device origin to the integer glyph origin and the
// quarter-pixel subpixel indices: positions gain 1/8 and truncate to 1/4;
// the axis without subpixel positioning rounds to a whole pixel.
func xfPlace(e, f float64, axis int) (ix, iy, sx, sy int) {
	rx, ry := e+0.125, f+0.125
	switch axis {
	case 1:
		ry = math.Floor(f + 0.5)
	case 2:
		rx = math.Floor(e + 0.5)
	}
	fx, fy := math.Floor(rx), math.Floor(ry)
	ix, iy = int(fx), int(fy)
	sx = int(math.Floor((rx - fx) * 4))
	sy = int(math.Floor((ry - fy) * 4))
	return
}

// xfScales computes FreeType's two scales and the remaining matrix r (device
// = r * outline scaled by the two scales) for the text-to-device matrix
// a b c d (canvas convention x' = a*x + c*y, y' = b*x + d*y; y down) with an
// optional synthetic-italic skew sk (x' += sk*y before the matrix).
func xfScales(a, b, c, d, sk float64) (sx, sy float32, r [4]float32, ok bool) {
	A00, A10, A01, A11 := relax32(float32(a)), relax32(float32(b)), relax32(float32(c)), relax32(float32(d))
	if sk != 0 {
		A01 = A00*float32(sk) + A01
		A11 = A10*float32(sk) + A11
	}
	gs00, gs11 := A00, A11
	if A10 != 0 || A01 != 0 || A00 < 0 || A11 < 0 {
		h := float32(math.Sqrt(float64(A00*A00 + A10*A10)))
		if h == 0 {
			return
		}
		cs, sn := A00/h, A10/h
		gs00 = cs*A00 + sn*A10
		gs11 = -sn*A01 + cs*A11
	}
	sx, sy = gs00, gs11
	if sx < 0 {
		sx = -sx
	}
	if sy < 0 {
		sy = -sy
	}
	if sx == 0 || sy == 0 {
		return
	}
	r = [4]float32{A00 / sx, A01 / sy, A10 / sx, A11 / sy}
	return sx, sy, r, true
}

// xfScaledPath is the outline of g as FreeType loads it at the given 26.6
// scales: integer points, implied on-curve points at integer midpoints,
// device orientation (y down), in pixels.
func xfScaledPath(g Glyph, scaleX, scaleY int) *Path {
	f := g.Face
	if len(glyphUnits(g)) == 0 {
		return nil
	}
	gid := g.GID
	if gid == 0 {
		gid = f.GID(g.R)
	}
	if p, ok := fxGlyphPathTTM(f, gid, scaleX, scaleY, 0, fxMat{}, 0, 0); ok {
		return p
	}
	return fxGlyphPathOutline(g, scaleX, scaleY, 0, fxMat{}, 0, 0)
}

// xfGlyphPath builds the device-space outline of g (origin at 0,0) for the
// matrix a b c d, the text size, and the quarter-pixel subpixel offsets.
func xfGlyphPath(g Glyph, size, a, b, c, d float64, italic bool, subX, subY int) *Path {
	sk := 0.0
	if italic {
		sk = -0.25
	}
	sx, sy, r, ok := xfScales(a, b, c, d, sk)
	if !ok {
		return nil
	}
	fs := float32(size)
	upm := g.Face.UnitsPerEm()
	p := xfScaledPath(g, ftDivFix(int(sx*fs*64), upm), ftDivFix(int(sy*fs*64), upm))
	if p == nil {
		return nil
	}
	ox, oy := float32(subX)/4, float32(subY)/4
	for i := 0; i < len(p.Pts); i += 2 {
		x, y := float32(p.Pts[i]), float32(p.Pts[i+1])
		p.Pts[i] = float64(r[0]*x + r[1]*y + ox)
		p.Pts[i+1] = float64(r[2]*x + r[3]*y + oy)
	}
	return p
}

// xfBoldPaths returns the glyph path and the outline of its stroke for the
// synthetic bold of the glyph. Skia strokes in "user" space (the device path
// mapped back through the inverse of the matrix, so the stroke follows the
// text size, not the device scale) and maps the result forward again.
func xfBoldPaths(p *Path, size, a, b, c, d float64) (fill, stroke *Path, flip bool) {
	A00, A10, A01, A11 := relax32(float32(a)), relax32(float32(b)), relax32(float32(c)), relax32(float32(d))
	det := A00*A11 - A01*A10
	if det == 0 {
		return nil, nil, false
	}
	inv := [4]float32{A11 / det, -A01 / det, -A10 / det, A00 / det}
	mapPath := func(src *Path, m [4]float32) *Path {
		q := &Path{Cmds: append([]byte(nil), src.Cmds...), Pts: make([]float64, len(src.Pts))}
		for i := 0; i < len(src.Pts); i += 2 {
			x, y := float32(src.Pts[i]), float32(src.Pts[i+1])
			q.Pts[i] = float64(m[0]*x + m[1]*y)
			q.Pts[i+1] = float64(m[2]*x + m[3]*y)
		}
		return q
	}
	local := mapPath(p, inv)
	st := fxStrokePath(local, fxFakeBoldExtra(size), JoinMiter, CapButt, 4, 1)
	flip = fxLargestArea(local)*fxLargestArea(st) < 0
	fwd := [4]float32{A00, A01, A10, A11}
	return mapPath(local, fwd), mapPath(st, fwd), flip
}

// xfStroke describes a stroked-text frame (user-space stroke of the glyph).
type xfStroke struct {
	w, miter float32
	join     LineJoin
	cap      LineCap
}

type xfKey struct {
	st         xfStroke
	f          *font.Face
	r          rune
	gid        int
	size       float32
	a, b, c, d int32 // relaxed matrix, in 1/1024
	subX, subY uint8
	bold, ital bool
}

var (
	xfMu    sync.Mutex
	xfCache = map[xfKey]*Mask{}
)

func xfQ(v float64) int32 { return int32(math.Round(xfRelax(v) * 1024)) }

// xfGlyphMask rasterizes one glyph for the device matrix a b c d (without
// translation) at the given text size and subpixel indices; masks are cached.
// The mask is positioned relative to the integer glyph origin.
func xfGlyphMask(g Glyph, size, a, b, c, d float64, bold, italic bool, subX, subY int) *Mask {
	return xfGlyphMaskS(g, size, a, b, c, d, bold, italic, subX, subY, xfStroke{})
}

// xfGlyphMaskS is xfGlyphMask for stroked text when st.w > 0: the glyph
// outline is stroked in user space (Skia's frame), then mapped forward.
func xfGlyphMaskS(g Glyph, size, a, b, c, d float64, bold, italic bool, subX, subY int, st xfStroke) *Mask {
	k := xfKey{st, g.Face, g.R, g.GID, float32(size), xfQ(a), xfQ(b), xfQ(c), xfQ(d), uint8(subX), uint8(subY), bold, italic}
	xfMu.Lock()
	if m, ok := xfCache[k]; ok {
		xfMu.Unlock()
		return m
	}
	xfMu.Unlock()
	var m *Mask
	if p := xfGlyphPath(g, size, a, b, c, d, italic, subX, subY); p != nil {
		if st.w > 0 {
			if o := xfStrokeOutline(p, a, b, c, d, st); o != nil {
				m = pathMaskTrim(o)
			}
		} else if bold {
			if fill, sp, flip := xfBoldPaths(p, size, a, b, c, d); fill != nil {
				m = fxUnionMask(fill, sp, flip)
			}
		} else {
			m = pathMaskTrim(p)
		}
	}
	xfMu.Lock()
	if len(xfCache) > 20000 {
		xfCache = map[xfKey]*Mask{}
	}
	xfCache[k] = m
	xfMu.Unlock()
	return m
}

func pathBounds(ps ...*Path) (minX, minY, maxX, maxY float64) {
	minX, minY, maxX, maxY = math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, p := range ps {
		for i := 0; i < len(p.Pts); i += 2 {
			minX, maxX = math.Min(minX, p.Pts[i]), math.Max(maxX, p.Pts[i])
			minY, maxY = math.Min(minY, p.Pts[i+1]), math.Max(maxY, p.Pts[i+1])
		}
	}
	return
}

// pathMaskTrim rasterizes a glyph-origin-relative outline over its own
// integer bounds.
func pathMaskTrim(p *Path) *Mask {
	minX, minY, maxX, maxY := pathBounds(p)
	if minX > maxX {
		return nil
	}
	ox, oy := int(math.Floor(minX)), int(math.Floor(minY))
	w := int(math.Ceil(maxX)) - ox
	h := int(math.Ceil(maxY)) - oy
	if w <= 0 || h <= 0 || w > 4096 || h > 4096 {
		return nil
	}
	return fxTrim(fxFillPath(p, IRect{ox, oy, ox + w, oy + h}))
}

// fxUnionMask fills a glyph together with its stroke outline.
func fxUnionMask(p, q *Path, flip bool) *Mask {
	minX, minY, maxX, maxY := pathBounds(p, q)
	if minX > maxX {
		return nil
	}
	ox, oy := int(math.Floor(minX)), int(math.Floor(minY))
	w, h := int(math.Ceil(maxX))-ox, int(math.Ceil(maxY))-oy
	if w <= 0 || h <= 0 || w > 4096 || h > 4096 {
		return nil
	}
	return fxTrim(fxFillUnion(p, q, flip, IRect{ox, oy, ox + w, oy + h}))
}

// xfCanonicalSize is the em size, in pixels, at which Skia generates the
// outlines it draws as paths (glyphs too big for the glyph cache).
const xfCanonicalSize = 64

// xfPathGlyph builds the device outline of g drawn as a path: the outline is
// loaded by FreeType at the canonical size (so its points sit on the 26.6 grid
// of a 64 px em), scaled to the text size and moved to the pen position in
// single precision, and then mapped through the full, unrelaxed device matrix
// m (a b c d e f; x' = a*x + c*y + e). pen is in user space.
func xfPathGlyph(g Glyph, size float64, m Matrix, italic bool, penX, penY float64) *Path {
	upm := g.Face.UnitsPerEm()
	p := xfScaledPath(g, ftDivFix(xfCanonicalSize*64, upm), ftDivFix(xfCanonicalSize*64, upm))
	if p == nil {
		return nil
	}
	s := float32(size) / xfCanonicalSize
	sk := float32(0)
	if italic {
		sk = -0.25
	}
	px, py := float32(penX), float32(penY)
	a, b, c, d, e, f := float32(m[0]), float32(m[1]), float32(m[2]), float32(m[3]), float32(m[4]), float32(m[5])
	for i := 0; i < len(p.Pts); i += 2 {
		x, y := float32(p.Pts[i]), float32(p.Pts[i+1])
		x = (x+sk*y)*s + px
		y = y*s + py
		p.Pts[i] = float64(a*x + c*y + e)
		p.Pts[i+1] = float64(b*x + d*y + f)
	}
	return p
}

// xfStrokeOutline strokes a device glyph path in user space and maps the
// outline back to device space.
func xfStrokeOutline(p *Path, a, b, c, d float64, st xfStroke) *Path {
	A00, A10, A01, A11 := relax32(float32(a)), relax32(float32(b)), relax32(float32(c)), relax32(float32(d))
	det := A00*A11 - A01*A10
	if det == 0 {
		return nil
	}
	inv := [4]float32{A11 / det, -A01 / det, -A10 / det, A00 / det}
	fwd := [4]float32{A00, A01, A10, A11}
	mapPath := func(src *Path, m [4]float32) *Path {
		q := &Path{Cmds: append([]byte(nil), src.Cmds...), Pts: make([]float64, len(src.Pts))}
		for i := 0; i < len(src.Pts); i += 2 {
			x, y := float32(src.Pts[i]), float32(src.Pts[i+1])
			q.Pts[i] = float64(m[0]*x + m[1]*y)
			q.Pts[i+1] = float64(m[2]*x + m[3]*y)
		}
		return q
	}
	o := fxStrokePath(mapPath(p, inv), float64(st.w), st.join, st.cap, float64(st.miter), 1)
	return mapPath(o, fwd)
}
