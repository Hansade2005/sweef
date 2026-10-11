package font

import (
	"image"
	"math"
	"sync"

	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/vector"
)

// Mask is a rasterised glyph: an 8-bit coverage bitmap and the offset of
// its top-left corner from the pen position on the baseline.
type Mask struct {
	Alpha *image.Alpha
	DX    int
	DY    int
}

// Subpixel positions per pixel. Chromium positions glyphs at 1/4 px
// horizontally on Linux; caching four phases keeps text crisp without
// rasterising each glyph per position.
const SubpixelSteps = 4

type maskKey struct {
	f     *Face
	id    sfnt.GlyphIndex
	phase uint8
	scale float32
}

var (
	maskMu    sync.RWMutex
	maskCache = map[maskKey]*Mask{}
	maskCount int
)

const maskCacheMax = 1 << 15

// GlyphMask rasterises glyph id at a horizontal subpixel phase
// (0..SubpixelSteps-1) and a device scale (device pixel ratio), anti
// aliased, cached. Synthetic bold strokes the coverage by dilating it.
// Returns nil for blank glyphs (spaces).
func (f *Face) GlyphMask(id sfnt.GlyphIndex, phase int, scale float64) *Mask {
	if scale <= 0 {
		scale = 1
	}
	k := maskKey{f, id, uint8(phase), float32(scale)}
	maskMu.RLock()
	m, ok := maskCache[k]
	maskMu.RUnlock()
	if ok {
		return m
	}
	m = f.rasterise(id, float64(phase)/SubpixelSteps, scale)
	maskMu.Lock()
	if maskCount > maskCacheMax {
		maskCache = map[maskKey]*Mask{}
		maskCount = 0
	}
	maskCache[k] = m
	maskCount++
	maskMu.Unlock()
	return m
}

func (f *Face) rasterise(id sfnt.GlyphIndex, sub, scale float64) *Mask {
	segs := f.Outline(id)
	if len(segs) == 0 {
		return nil
	}
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, s := range segs {
		for j := 0; j < s.N(); j++ {
			x, y := s.Pts[j][0]*scale+sub, s.Pts[j][1]*scale
			minX, maxX = math.Min(minX, x), math.Max(maxX, x)
			minY, maxY = math.Min(minY, y), math.Max(maxY, y)
		}
	}
	pad := 1.0
	if f.FakeBold {
		pad = 2
	}
	x0, y0 := math.Floor(minX)-pad, math.Floor(minY)-pad
	w, h := int(math.Ceil(maxX)-x0+pad), int(math.Ceil(maxY)-y0+pad)
	if w <= 0 || h <= 0 || w > 4096 || h > 4096 {
		return nil
	}
	r := vector.NewRasterizer(w, h)
	r.DrawOp = 0 // draw.Over (zero value of draw.Op is Over)
	pt := func(p [2]float64) (float32, float32) {
		return float32(p[0]*scale + sub - x0), float32(p[1]*scale - y0)
	}
	open := false
	for _, s := range segs {
		switch s.Op {
		case MoveTo:
			if open {
				r.ClosePath()
			}
			r.MoveTo(pt(s.Pts[0]))
			open = true
		case LineTo:
			r.LineTo(pt(s.Pts[0]))
		case QuadTo:
			ax, ay := pt(s.Pts[0])
			bx, by := pt(s.Pts[1])
			r.QuadTo(ax, ay, bx, by)
		case CubeTo:
			ax, ay := pt(s.Pts[0])
			bx, by := pt(s.Pts[1])
			cx, cy := pt(s.Pts[2])
			r.CubeTo(ax, ay, bx, by, cx, cy)
		}
	}
	if open {
		r.ClosePath()
	}
	a := image.NewAlpha(image.Rect(0, 0, w, h))
	r.Draw(a, a.Bounds(), image.Opaque, image.Point{})
	if f.FakeBold {
		emboldenX(a, f.Size*scale/24)
	}
	return &Mask{Alpha: a, DX: int(x0), DY: int(y0)}
}

// emboldenX smears coverage rightwards by d px (FreeType-style synthetic
// bold is a 1/24 em outline embolden; a horizontal max is close enough at
// text sizes and keeps the glyph's advance increase consistent).
func emboldenX(a *image.Alpha, d float64) {
	if d <= 0 {
		return
	}
	n := int(math.Ceil(d))
	frac := d - float64(n-1)
	b := a.Bounds()
	row := make([]uint8, b.Dx())
	for y := b.Min.Y; y < b.Max.Y; y++ {
		off := (y - b.Min.Y) * a.Stride
		copy(row, a.Pix[off:off+b.Dx()])
		for x := range row {
			best := float64(row[x])
			for k := 1; k <= n && x-k >= 0; k++ {
				v := float64(row[x-k])
				if k == n {
					v *= frac
				}
				if v > best {
					best = v
				}
			}
			a.Pix[off+x] = uint8(best)
		}
	}
}
