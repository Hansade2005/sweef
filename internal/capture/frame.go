package capture

import (
	"image"
	"image/color"
)

// NewFrame allocates a zeroed w x h I420 frame (stride == width, chroma stride == (w+1)/2).
// Odd sizes are allowed; chroma planes are rounded up.
func NewFrame(w, h int) *Frame {
	cw, ch := (w+1)/2, (h+1)/2
	return &Frame{W: w, H: h, YStride: w, UVStride: cw,
		Y: make([]byte, w*h), U: make([]byte, cw*ch), V: make([]byte, cw*ch)}
}

// Clone returns a deep copy.
func (f *Frame) Clone() *Frame {
	c := *f
	c.Y = append([]byte(nil), f.Y...)
	c.U = append([]byte(nil), f.U...)
	c.V = append([]byte(nil), f.V...)
	return &c
}

// Black fills the frame with limited-range black (Y=16, U=V=128).
func (f *Frame) Black() {
	for i := range f.Y {
		f.Y[i] = 16
	}
	for i := range f.U {
		f.U[i] = 128
	}
	for i := range f.V {
		f.V[i] = 128
	}
}

// NewBlack returns a black frame of the given size.
func NewBlack(w, h int) *Frame {
	f := NewFrame(w, h)
	f.Black()
	return f
}

func clamp8(v int) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

// YUVToRGB converts one BT.601 limited-range sample (the range webcams and the VP8/H.264 encoders assume).
func YUVToRGB(y, u, v uint8) (r, g, b uint8) {
	c, d, e := int(y)-16, int(u)-128, int(v)-128
	if c < 0 {
		c = 0
	}
	r = clamp8((298*c + 409*e + 128) >> 8)
	g = clamp8((298*c - 100*d - 208*e + 128) >> 8)
	b = clamp8((298*c + 516*d + 128) >> 8)
	return
}

// RGBToY, RGBToU and RGBToV are the BT.601 limited-range forward transform.
func RGBToY(r, g, b int) uint8 { return uint8(((66*r + 129*g + 25*b + 128) >> 8) + 16) }
func RGBToU(r, g, b int) uint8 { return clamp8(((-38*r - 74*g + 112*b + 128) >> 8) + 128) }
func RGBToV(r, g, b int) uint8 { return clamp8(((112*r - 94*g - 18*b + 128) >> 8) + 128) }

// ToRGBA converts the frame to an opaque RGBA image (previews, PNG output, tests).
func (f *Frame) ToRGBA() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, f.W, f.H))
	for y := 0; y < f.H; y++ {
		yr := f.Y[y*f.YStride:]
		ur := f.U[(y/2)*f.UVStride:]
		vr := f.V[(y/2)*f.UVStride:]
		o := y * img.Stride
		for x := 0; x < f.W; x++ {
			r, g, b := YUVToRGB(yr[x], ur[x/2], vr[x/2])
			img.Pix[o], img.Pix[o+1], img.Pix[o+2], img.Pix[o+3] = r, g, b, 255
			o += 4
		}
	}
	return img
}

// FromRGBA converts src into dst (which must be src's size), averaging each 2x2 block for chroma. Alpha is ignored.
func FromRGBA(dst *Frame, src *image.RGBA) {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if dst.W != w || dst.H != h {
		return
	}
	for y := 0; y < h; y += 2 {
		for x := 0; x < w; x += 2 {
			var sr, sg, sb, n int
			for dy := 0; dy < 2 && y+dy < h; dy++ {
				o := src.PixOffset(b.Min.X+x, b.Min.Y+y+dy)
				for dx := 0; dx < 2 && x+dx < w; dx++ {
					r, g, bl := int(src.Pix[o]), int(src.Pix[o+1]), int(src.Pix[o+2])
					dst.Y[(y+dy)*dst.YStride+x+dx] = RGBToY(r, g, bl)
					sr += r
					sg += g
					sb += bl
					n++
					o += 4
				}
			}
			dst.U[(y/2)*dst.UVStride+x/2] = RGBToU(sr/n, sg/n, sb/n)
			dst.V[(y/2)*dst.UVStride+x/2] = RGBToV(sr/n, sg/n, sb/n)
		}
	}
}

// FromImage converts any image.Image (slow path, used by tests and one-off captures).
func FromImage(img image.Image) *Frame {
	b := img.Bounds()
	if r, ok := img.(*image.RGBA); ok {
		f := NewFrame(b.Dx(), b.Dy())
		FromRGBA(f, r)
		return f
	}
	rgba := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			rgba.Set(x, y, color.RGBAModel.Convert(img.At(b.Min.X+x, b.Min.Y+y)))
		}
	}
	f := NewFrame(b.Dx(), b.Dy())
	FromRGBA(f, rgba)
	return f
}

// Scale writes src into dst (any sizes) with an area-average filter when shrinking and a nearest-neighbour pick when
// growing. The screen sources use it to honour max width/height constraints.
func Scale(dst, src *Frame) {
	scalePlane(dst.Y, dst.YStride, dst.W, dst.H, src.Y, src.YStride, src.W, src.H)
	scalePlane(dst.U, dst.UVStride, (dst.W+1)/2, (dst.H+1)/2, src.U, src.UVStride, (src.W+1)/2, (src.H+1)/2)
	scalePlane(dst.V, dst.UVStride, (dst.W+1)/2, (dst.H+1)/2, src.V, src.UVStride, (src.W+1)/2, (src.H+1)/2)
}

func scalePlane(d []byte, ds, dw, dh int, s []byte, ss, sw, sh int) {
	if dw <= 0 || dh <= 0 || sw <= 0 || sh <= 0 {
		return
	}
	if dw == sw && dh == sh {
		for y := 0; y < dh; y++ {
			copy(d[y*ds:y*ds+dw], s[y*ss:y*ss+sw])
		}
		return
	}
	x0 := make([]int, dw)
	x1 := make([]int, dw)
	for x := 0; x < dw; x++ {
		a := x * sw / dw
		b := ((x+1)*sw + dw - 1) / dw
		if b <= a {
			b = a + 1
		}
		if b > sw {
			b = sw
		}
		x0[x], x1[x] = a, b
	}
	for y := 0; y < dh; y++ {
		ya := y * sh / dh
		yb := ((y+1)*sh + dh - 1) / dh
		if yb <= ya {
			yb = ya + 1
		}
		if yb > sh {
			yb = sh
		}
		row := d[y*ds : y*ds+dw]
		for x := 0; x < dw; x++ {
			sum, n := 0, 0
			for yy := ya; yy < yb; yy++ {
				r := s[yy*ss:]
				for xx := x0[x]; xx < x1[x]; xx++ {
					sum += int(r[xx])
					n++
				}
			}
			row[x] = uint8((sum + n/2) / n)
		}
	}
}
