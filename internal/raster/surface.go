package raster

import (
	"image"
	"image/color"
	"image/draw"
)

// Surface is a premultiplied RGBA8 pixel buffer covering R in device space.
type Surface struct {
	R      IRect
	Stride int // bytes per row
	Pix    []uint8
}

// NewSurface allocates a transparent surface of size w x h at the origin.
func NewSurface(w, h int) *Surface { return NewSurfaceRect(IRect{0, 0, w, h}) }

// NewSurfaceRect allocates a transparent surface covering r.
func NewSurfaceRect(r IRect) *Surface {
	w, h := r.W(), r.H()
	if w < 0 {
		w = 0
	}
	if h < 0 {
		h = 0
	}
	return &Surface{R: r, Stride: w * 4, Pix: make([]uint8, w*h*4)}
}

// W and H are the pixel dimensions.
func (s *Surface) W() int { return s.R.W() }
func (s *Surface) H() int { return s.R.H() }

// Row returns the pixel bytes for absolute coords y, x in [x0,x1).
func (s *Surface) Row(y, x0, x1 int) []uint8 {
	o := (y-s.R.Y0)*s.Stride + (x0-s.R.X0)*4
	return s.Pix[o : o+(x1-x0)*4]
}

// Clone copies the surface.
func (s *Surface) Clone() *Surface {
	c := *s
	c.Pix = append([]uint8(nil), s.Pix...)
	return &c
}

// Clear zeroes the surface.
func (s *Surface) Clear() { clear(s.Pix) }

// Image views the surface as an image.RGBA (premultiplied, bounds at origin 0,0
// when the surface origin is 0,0).
func (s *Surface) Image() *image.RGBA {
	return &image.RGBA{Pix: s.Pix, Stride: s.Stride, Rect: image.Rect(s.R.X0, s.R.Y0, s.R.X1, s.R.Y1)}
}

// FromImage converts any image to a premultiplied surface at the origin.
func FromImage(img image.Image) *Surface {
	b := img.Bounds()
	s := NewSurface(b.Dx(), b.Dy())
	switch m := img.(type) {
	case *image.RGBA:
		for y := 0; y < b.Dy(); y++ {
			copy(s.Pix[y*s.Stride:(y+1)*s.Stride], m.Pix[(y+b.Min.Y-m.Rect.Min.Y)*m.Stride+(b.Min.X-m.Rect.Min.X)*4:])
		}
	case *image.NRGBA:
		for y := 0; y < b.Dy(); y++ {
			si := (y+b.Min.Y-m.Rect.Min.Y)*m.Stride + (b.Min.X-m.Rect.Min.X)*4
			for x := 0; x < b.Dx(); x++ {
				r, g, bl, a := uint32(m.Pix[si]), uint32(m.Pix[si+1]), uint32(m.Pix[si+2]), uint32(m.Pix[si+3])
				o := y*s.Stride + x*4
				if a == 255 {
					s.Pix[o], s.Pix[o+1], s.Pix[o+2], s.Pix[o+3] = uint8(r), uint8(g), uint8(bl), 255
				} else {
					s.Pix[o], s.Pix[o+1], s.Pix[o+2], s.Pix[o+3] = uint8(mul255(r, a)), uint8(mul255(g, a)), uint8(mul255(bl, a)), uint8(a)
				}
				si += 4
			}
		}
	default:
		dst := &image.RGBA{Pix: s.Pix, Stride: s.Stride, Rect: image.Rect(0, 0, b.Dx(), b.Dy())}
		draw.Draw(dst, dst.Rect, img, b.Min, draw.Src)
	}
	return s
}

// ToNRGBA converts to non-premultiplied 8-bit.
func (s *Surface) ToNRGBA() *image.NRGBA {
	w, h := s.W(), s.H()
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		src := s.Pix[y*s.Stride : y*s.Stride+w*4]
		dst := out.Pix[y*out.Stride : y*out.Stride+w*4]
		for x := 0; x < w; x++ {
			a := uint32(src[x*4+3])
			switch a {
			case 0:
			case 255:
				copy(dst[x*4:x*4+4], src[x*4:x*4+4])
			default:
				dst[x*4] = unpremul(src[x*4], a)
				dst[x*4+1] = unpremul(src[x*4+1], a)
				dst[x*4+2] = unpremul(src[x*4+2], a)
				dst[x*4+3] = uint8(a)
			}
		}
	}
	return out
}

func unpremul(c uint8, a uint32) uint8 {
	v := (uint32(c)*255 + a/2) / a
	if v > 255 {
		v = 255
	}
	return uint8(v)
}

// SetNRGBA writes non-premultiplied pixels (src, w x h) at (dx, dy) replacing
// the destination.
func (s *Surface) SetNRGBA(src []uint8, w, h, dx, dy int) {
	for y := 0; y < h; y++ {
		ty := dy + y
		if ty < s.R.Y0 || ty >= s.R.Y1 {
			continue
		}
		for x := 0; x < w; x++ {
			tx := dx + x
			if tx < s.R.X0 || tx >= s.R.X1 {
				continue
			}
			i := (y*w + x) * 4
			a := uint32(src[i+3])
			o := (ty-s.R.Y0)*s.Stride + (tx-s.R.X0)*4
			s.Pix[o], s.Pix[o+1], s.Pix[o+2], s.Pix[o+3] = uint8(mul255(uint32(src[i]), a)), uint8(mul255(uint32(src[i+1]), a)), uint8(mul255(uint32(src[i+2]), a)), uint8(a)
		}
	}
}

// ColorAt returns the straight-alpha color at absolute pixel coordinates.
func (s *Surface) ColorAt(x, y int) color.NRGBA {
	if x < s.R.X0 || x >= s.R.X1 || y < s.R.Y0 || y >= s.R.Y1 {
		return color.NRGBA{}
	}
	o := (y-s.R.Y0)*s.Stride + (x-s.R.X0)*4
	a := uint32(s.Pix[o+3])
	if a == 0 {
		return color.NRGBA{}
	}
	return color.NRGBA{unpremul(s.Pix[o], a), unpremul(s.Pix[o+1], a), unpremul(s.Pix[o+2], a), uint8(a)}
}
