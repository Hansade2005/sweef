package compositor

import (
	"encoding/binary"
	"image"
	"image/color"
)

func fillWhite(m *image.RGBA) {
	for i := range m.Pix {
		m.Pix[i] = 0xff
	}
}

// copyRect copies the pixels of r (in dst coordinates; both images share
// origin 0,0) from src to dst, row by row.
func copyRect(dst, src *image.RGBA, r image.Rectangle) {
	r = r.Intersect(dst.Bounds()).Intersect(src.Bounds())
	if r.Empty() {
		return
	}
	n := 4 * r.Dx()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		so := src.PixOffset(r.Min.X, y)
		do := dst.PixOffset(r.Min.X, y)
		copy(dst.Pix[do:do+n], src.Pix[so:so+n])
	}
}

func fillRow(row []byte, c color.RGBA) {
	if len(row) < 4 {
		return
	}
	row[0], row[1], row[2], row[3] = c.R, c.G, c.B, c.A
	for filled := 4; filled < len(row); filled *= 2 {
		copy(row[filled:], row[:filled])
	}
}

func fillRect(m *image.RGBA, r image.Rectangle, c color.RGBA) {
	r = r.Intersect(m.Bounds())
	for y := r.Min.Y; y < r.Max.Y; y++ {
		o := m.PixOffset(r.Min.X, y)
		fillRow(m.Pix[o:o+4*r.Dx()], c)
	}
}

// diff returns the bounding box (within clip) of pixels that differ between
// a and b, which must have identical bounds. It is allocation free and
// compares whole rows first, so an unchanged frame costs a few memcmp's.
func diff(a, b *image.RGBA, clip image.Rectangle) image.Rectangle {
	clip = clip.Intersect(a.Bounds()).Intersect(b.Bounds())
	if clip.Empty() {
		return image.Rectangle{}
	}
	n := 4 * clip.Dx()
	rowEq := func(y int) bool {
		ao, bo := a.PixOffset(clip.Min.X, y), b.PixOffset(clip.Min.X, y)
		return string(a.Pix[ao:ao+n]) == string(b.Pix[bo:bo+n]) // compiler-optimised, no alloc
	}
	y0 := clip.Min.Y
	for y0 < clip.Max.Y && rowEq(y0) {
		y0++
	}
	if y0 == clip.Max.Y {
		return image.Rectangle{}
	}
	y1 := clip.Max.Y - 1
	for y1 > y0 && rowEq(y1) {
		y1--
	}
	x0, x1 := clip.Max.X, clip.Min.X
	for y := y0; y <= y1; y++ {
		ao, bo := a.PixOffset(clip.Min.X, y), b.PixOffset(clip.Min.X, y)
		ra, rb := a.Pix[ao:ao+n], b.Pix[bo:bo+n]
		if string(ra) == string(rb) {
			continue
		}
		i := 0
		for i+4 <= n && binary.LittleEndian.Uint32(ra[i:]) == binary.LittleEndian.Uint32(rb[i:]) {
			i += 4
		}
		if px := clip.Min.X + i/4; px < x0 {
			x0 = px
		}
		j := n - 4
		for j > i && binary.LittleEndian.Uint32(ra[j:]) == binary.LittleEndian.Uint32(rb[j:]) {
			j -= 4
		}
		if px := clip.Min.X + j/4 + 1; px > x1 {
			x1 = px
		}
	}
	return image.Rect(x0, y0, x1, y1+1)
}

// blend composites the premultiplied src (placed at `at`) over dst inside r
// with a global opacity a (0..255): dst = src*a + dst*(1 - srcA*a).
func blend(dst, src *image.RGBA, at image.Point, r image.Rectangle, a uint8) {
	sb := image.Rectangle{Min: at, Max: at.Add(src.Bounds().Size())}
	r = r.Intersect(sb).Intersect(dst.Bounds())
	if r.Empty() {
		return
	}
	ga := uint32(a)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		do := dst.PixOffset(r.Min.X, y)
		so := src.PixOffset(r.Min.X-at.X+src.Rect.Min.X, y-at.Y+src.Rect.Min.Y)
		d := dst.Pix[do : do+4*r.Dx()]
		s := src.Pix[so : so+4*r.Dx()]
		for i := 0; i+4 <= len(d); i += 4 {
			sa := uint32(s[i+3])
			if sa == 0 {
				continue
			}
			if ga == 255 && sa == 255 {
				d[i], d[i+1], d[i+2], d[i+3] = s[i], s[i+1], s[i+2], 255
				continue
			}
			// scale source by global alpha
			sr, sg, sb, sa2 := mul255(uint32(s[i]), ga), mul255(uint32(s[i+1]), ga), mul255(uint32(s[i+2]), ga), mul255(sa, ga)
			inv := 255 - sa2
			d[i] = uint8(sr + mul255(uint32(d[i]), inv))
			d[i+1] = uint8(sg + mul255(uint32(d[i+1]), inv))
			d[i+2] = uint8(sb + mul255(uint32(d[i+2]), inv))
			d[i+3] = uint8(sa2 + mul255(uint32(d[i+3]), inv))
		}
	}
}

func mul255(a, b uint32) uint32 {
	t := a*b + 128
	return (t + t>>8) >> 8
}

// blendRect composites a solid colour with opacity a over r.
func blendRect(dst *image.RGBA, r image.Rectangle, col color.RGBA, a uint8) {
	r = r.Intersect(dst.Bounds())
	ga := uint32(a)
	inv := 255 - ga
	cr, cg, cb := mul255(uint32(col.R), ga), mul255(uint32(col.G), ga), mul255(uint32(col.B), ga)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		o := dst.PixOffset(r.Min.X, y)
		d := dst.Pix[o : o+4*r.Dx()]
		for i := 0; i+4 <= len(d); i += 4 {
			d[i] = uint8(cr + mul255(uint32(d[i]), inv))
			d[i+1] = uint8(cg + mul255(uint32(d[i+1]), inv))
			d[i+2] = uint8(cb + mul255(uint32(d[i+2]), inv))
			d[i+3] = 255
		}
	}
}
