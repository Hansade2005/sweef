package gl

import "math"

// Image is one mip level (or renderbuffer / framebuffer storage) of pixels,
// stored as RGBA in the format's class.
type Image struct {
	W, H, D   int
	S         int // samples (1 = single sampled)
	Fmt       *Format
	U8        []uint8
	F         []float32
	I         []int32
	St        []uint8 // stencil plane (W*H*D*S)
	stride    int     // elements per sample: 4 for color, 1 for depth
	clearConv bool    // use the glClear float->unorm conversion
}

// NewImage allocates zeroed storage.
func NewImage(f *Format, w, h, d, samples int) *Image {
	if samples < 1 {
		samples = 1
	}
	im := &Image{W: w, H: h, D: d, S: samples, Fmt: f, stride: 4}
	n := w * h * d * samples * 4
	if f.IsDepth() {
		im.stride = 1
		n = w * h * d * samples
	}
	switch f.Class {
	case ClsU8:
		if f.SBits > 0 && f.DBits == 0 {
			im.St = make([]uint8, w*h*d*samples)
			return im
		}
		im.U8 = make([]uint8, n)
	case ClsF32:
		im.F = make([]float32, n)
	case ClsInt:
		im.I = make([]int32, n)
	}
	if f.SBits > 0 && im.St == nil {
		im.St = make([]uint8, w*h*d*samples)
	}
	return im
}

func (im *Image) idx(x, y, z int) int { return (((z*im.H + y) * im.W) + x) * im.S * im.stride }

// idxS is the element index of sample s of a pixel.
func (im *Image) idxS(x, y, z, s int) int {
	return ((((z*im.H+y)*im.W)+x)*im.S + s) * im.stride
}

// pix is the pixel number (for the stencil plane at sample 0).
func (im *Image) pixS(x, y, z, s int) int { return (((z*im.H+y)*im.W)+x)*im.S + s }

// Bytes returns the approximate storage size.
func (im *Image) Bytes() int { return len(im.U8) + len(im.F)*4 + len(im.I)*4 + len(im.St) }

// Get returns the texel as normalized floats (or raw ints for integer
// classes via math.Float32frombits of the int).
func (im *Image) Get(x, y, z int) [4]float32 {
	i := im.idx(x, y, z)
	if im.stride == 1 {
		return [4]float32{im.F[i], 0, 0, 1}
	}
	switch im.Fmt.Class {
	case ClsU8:
		if im.Fmt.SRGB {
			return [4]float32{srgbToLinear[im.U8[i]], srgbToLinear[im.U8[i+1]], srgbToLinear[im.U8[i+2]], float32(im.U8[i+3]) / 255}
		}
		return [4]float32{float32(im.U8[i]) / 255, float32(im.U8[i+1]) / 255, float32(im.U8[i+2]) / 255, float32(im.U8[i+3]) / 255}
	case ClsF32:
		return [4]float32{im.F[i], im.F[i+1], im.F[i+2], im.F[i+3]}
	}
	return [4]float32{float32(im.I[i]), float32(im.I[i+1]), float32(im.I[i+2]), float32(im.I[i+3])}
}

// Raw returns the texel's raw 32-bit lanes (float bits for float classes).
func (im *Image) Raw(x, y, z int) [4]uint32 {
	i := im.idx(x, y, z)
	if im.stride == 1 {
		return [4]uint32{math.Float32bits(im.F[i]), 0, 0, math.Float32bits(1)}
	}
	switch im.Fmt.Class {
	case ClsU8:
		v := im.Get(x, y, z)
		return [4]uint32{math.Float32bits(v[0]), math.Float32bits(v[1]), math.Float32bits(v[2]), math.Float32bits(v[3])}
	case ClsF32:
		return [4]uint32{math.Float32bits(im.F[i]), math.Float32bits(im.F[i+1]), math.Float32bits(im.F[i+2]), math.Float32bits(im.F[i+3])}
	}
	return [4]uint32{uint32(im.I[i]), uint32(im.I[i+1]), uint32(im.I[i+2]), uint32(im.I[i+3])}
}

// storeColor writes a color given as floats (already in the format's value
// domain: linear for sRGB).
func (im *Image) setFloat(i int, c [4]float32) {
	f := im.Fmt
	switch f.Class {
	case ClsU8:
		if f.SRGB {
			c[0], c[1], c[2] = linearToSRGB(c[0]), linearToSRGB(c[1]), linearToSRGB(c[2])
		}
		for k := 0; k < 4; k++ {
			v := c[k]
			if b := f.Bits[k]; b > 0 && b < 8 {
				m := float32(int(1)<<uint(b) - 1)
				v = float32(math.Floor(float64(clampf(v, 0, 1)*m+0.5))) / m
			}
			if im.clearConv {
				im.U8[i+k] = unormToByteClear(v)
			} else {
				im.U8[i+k] = unormToByte(v)
			}
		}
	case ClsF32:
		for k := 0; k < 4; k++ {
			v := c[k]
			if f.Half {
				v = quantHalf(v)
			}
			if f.Snorm {
				v = clampf(v, -1, 1)
			}
			im.F[i+k] = v
		}
	}
}

// quantDepth rounds a depth value to the precision of the format.
func quantDepth(f *Format, z float32) float32 {
	if z < 0 {
		z = 0
	} else if z > 1 {
		z = 1
	}
	switch f.DBits {
	case 16:
		return float32(math.Floor(float64(z)*65535+0.5)) / 65535
	case 24:
		return float32(math.Floor(float64(z)*16777215+0.5)) / 16777215
	}
	return z
}

// Clear fills the image (all samples) with the cleared color.
func (im *Image) fillColor(c [4]float32, mask [4]bool, ci [4]int32) {
	n := len(im.U8) / 4
	switch im.Fmt.Class {
	case ClsU8:
		var tmp Image
		tmp = Image{Fmt: im.Fmt, U8: make([]uint8, 4)}
		tmp.setFloat(0, c)
		for p := 0; p < n; p++ {
			for k := 0; k < 4; k++ {
				if mask[k] {
					im.U8[p*4+k] = tmp.U8[k]
				}
			}
		}
	case ClsF32:
		var v [4]float32
		t := Image{Fmt: im.Fmt, F: make([]float32, 4)}
		t.setFloat(0, c)
		copy(v[:], t.F)
		for p := 0; p < len(im.F)/4; p++ {
			for k := 0; k < 4; k++ {
				if mask[k] {
					im.F[p*4+k] = v[k]
				}
			}
		}
	case ClsInt:
		for p := 0; p < len(im.I)/4; p++ {
			for k := 0; k < 4; k++ {
				if mask[k] {
					im.I[p*4+k] = ci[k]
				}
			}
		}
	}
}

// ---- client pixel layout ------------------------------------------------------

func formatComponents(format uint32) int {
	switch format {
	case GL_RGBA, GL_RGBA_INTEGER:
		return 4
	case GL_RGB, GL_RGB_INTEGER:
		return 3
	case GL_RG, GL_RG_INTEGER, GL_LUMINANCE_ALPHA:
		return 2
	case GL_RED, GL_RED_INTEGER, GL_LUMINANCE, GL_ALPHA, GL_DEPTH_COMPONENT, GL_DEPTH_STENCIL:
		return 1
	}
	return 0
}

// pixelBytes returns the bytes per client pixel, or 0 when the combination is
// invalid.
func pixelBytes(format, typ uint32) int {
	comps := formatComponents(format)
	if comps == 0 {
		return 0
	}
	switch typ {
	case GL_UNSIGNED_BYTE, GL_BYTE:
		return comps
	case GL_SHORT, GL_UNSIGNED_SHORT, GL_HALF_FLOAT, GL_HALF_FLOAT_OES:
		return comps * 2
	case GL_INT, GL_UNSIGNED_INT, GL_FLOAT:
		return comps * 4
	case GL_UNSIGNED_SHORT_4_4_4_4, GL_UNSIGNED_SHORT_5_5_5_1, GL_UNSIGNED_SHORT_5_6_5:
		return 2
	case GL_UNSIGNED_INT_2_10_10_10_REV, GL_UNSIGNED_INT_24_8, GL_UNSIGNED_INT_10F_11F_11F_REV, GL_UNSIGNED_INT_5_9_9_9_REV:
		return 4
	case GL_FLOAT_32_UNSIGNED_INT_24_8_REV:
		return 8
	}
	return 0
}

// Pack describes client memory layout (UNPACK_* / PACK_* state).
type Pack struct {
	Alignment   int
	RowLength   int
	ImageHeight int
	SkipRows    int
	SkipPixels  int
	SkipImages  int
}

func (p Pack) rowStride(w, pixel int) int {
	rl := p.RowLength
	if rl <= 0 {
		rl = w
	}
	row := rl * pixel
	a := p.Alignment
	if a < 1 {
		a = 1
	}
	return (row + a - 1) / a * a
}

// RequiredBytes is the number of bytes a w*h*d upload reads.
func (p Pack) RequiredBytes(w, h, d, format, typ uint32) int {
	pb := pixelBytes(format, typ)
	if pb == 0 || w == 0 || h == 0 || d == 0 {
		return 0
	}
	rs := p.rowStride(int(w), pb)
	ih := p.ImageHeight
	if ih <= 0 {
		ih = int(h)
	}
	off := p.SkipImages*ih*rs + p.SkipRows*rs + p.SkipPixels*pb
	last := (int(d)-1)*ih*rs + (int(h)-1)*rs + int(w)*pb
	return off + last
}

// decodePx reads one client pixel to RGBA floats (or ints for integer
// formats) according to format/type.
func decodePx(src []byte, off int, format, typ uint32) (c [4]float32, ci [4]int32) {
	le16 := func(o int) uint16 { return uint16(src[o]) | uint16(src[o+1])<<8 }
	le32 := func(o int) uint32 {
		return uint32(src[o]) | uint32(src[o+1])<<8 | uint32(src[o+2])<<16 | uint32(src[o+3])<<24
	}
	switch typ {
	case GL_UNSIGNED_SHORT_4_4_4_4:
		v := le16(off)
		c = [4]float32{float32(v>>12&15) / 15, float32(v>>8&15) / 15, float32(v>>4&15) / 15, float32(v&15) / 15}
		return
	case GL_UNSIGNED_SHORT_5_5_5_1:
		v := le16(off)
		c = [4]float32{float32(v>>11&31) / 31, float32(v>>6&31) / 31, float32(v>>1&31) / 31, float32(v & 1)}
		return
	case GL_UNSIGNED_SHORT_5_6_5:
		v := le16(off)
		c = [4]float32{float32(v>>11&31) / 31, float32(v>>5&63) / 63, float32(v&31) / 31, 1}
		return
	case GL_UNSIGNED_INT_2_10_10_10_REV:
		v := le32(off)
		if format == GL_RGBA_INTEGER {
			ci = [4]int32{int32(v & 1023), int32(v >> 10 & 1023), int32(v >> 20 & 1023), int32(v >> 30)}
			return
		}
		c = [4]float32{float32(v&1023) / 1023, float32(v>>10&1023) / 1023, float32(v>>20&1023) / 1023, float32(v>>30) / 3}
		return
	case GL_UNSIGNED_INT_24_8:
		v := le32(off)
		c[0] = float32(v>>8) / 16777215
		ci[1] = int32(v & 255)
		return
	case GL_FLOAT_32_UNSIGNED_INT_24_8_REV:
		c[0] = math.Float32frombits(le32(off))
		ci[1] = int32(le32(off+4) & 255)
		return
	case GL_UNSIGNED_INT_10F_11F_11F_REV:
		v := le32(off)
		c = [4]float32{unpackF11(v & 0x7FF), unpackF11(v >> 11 & 0x7FF), unpackF10(v >> 22), 1}
		return
	case GL_UNSIGNED_INT_5_9_9_9_REV:
		v := le32(off)
		e := int(v>>27) - 15 - 9
		s := float32(math.Pow(2, float64(e)))
		c = [4]float32{float32(v&511) * s, float32(v>>9&511) * s, float32(v>>18&511) * s, 1}
		return
	}
	n := formatComponents(format)
	if n == 0 {
		n = 1
	}
	integer := format == GL_RED_INTEGER || format == GL_RG_INTEGER || format == GL_RGB_INTEGER || format == GL_RGBA_INTEGER
	var raw [4]float32
	var rawi [4]int32
	for k := 0; k < n; k++ {
		switch typ {
		case GL_UNSIGNED_BYTE:
			b := src[off+k]
			raw[k] = float32(b) / 255
			rawi[k] = int32(b)
		case GL_BYTE:
			b := int8(src[off+k])
			raw[k] = clampf(float32(b)/127, -1, 1)
			rawi[k] = int32(b)
		case GL_UNSIGNED_SHORT:
			v := le16(off + k*2)
			raw[k] = float32(v) / 65535
			rawi[k] = int32(v)
		case GL_SHORT:
			v := int16(le16(off + k*2))
			raw[k] = clampf(float32(v)/32767, -1, 1)
			rawi[k] = int32(v)
		case GL_UNSIGNED_INT:
			v := le32(off + k*4)
			raw[k] = float32(float64(v) / 4294967295)
			rawi[k] = int32(v)
		case GL_INT:
			v := int32(le32(off + k*4))
			raw[k] = float32(v)
			rawi[k] = v
		case GL_FLOAT:
			raw[k] = math.Float32frombits(le32(off + k*4))
		case GL_HALF_FLOAT, GL_HALF_FLOAT_OES:
			raw[k] = halfToF32(le16(off + k*2))
		}
	}
	if integer {
		ci = rawi
		return
	}
	switch format {
	case GL_RGBA:
		c = raw
	case GL_RGB:
		c = [4]float32{raw[0], raw[1], raw[2], 1}
	case GL_RG:
		c = [4]float32{raw[0], raw[1], 0, 1}
	case GL_RED:
		c = [4]float32{raw[0], 0, 0, 1}
	case GL_LUMINANCE:
		c = [4]float32{raw[0], raw[0], raw[0], 1}
	case GL_LUMINANCE_ALPHA:
		c = [4]float32{raw[0], raw[0], raw[0], raw[1]}
	case GL_ALPHA:
		c = [4]float32{0, 0, 0, raw[0]}
	case GL_DEPTH_COMPONENT, GL_DEPTH_STENCIL:
		c = [4]float32{raw[0], 0, 0, 1}
	}
	return
}

func unpackF11(v uint32) float32 {
	e, m := int(v>>6), v&63
	return unpackSmallFloat(e, m, 6)
}
func unpackF10(v uint32) float32 {
	e, m := int(v>>5), v&31
	return unpackSmallFloat(e, m, 5)
}
func unpackSmallFloat(e int, m uint32, mb uint) float32 {
	switch {
	case e == 0:
		return float32(m) / float32(int(1)<<mb) * float32(math.Pow(2, -14))
	case e == 31:
		if m == 0 {
			return float32(math.Inf(1))
		}
		return float32(math.NaN())
	}
	return float32(math.Pow(2, float64(e-15))) * (1 + float32(m)/float32(int(1)<<mb))
}

// Upload copies a client pixel rectangle into the image at (x,y,z).
// Source rows are read bottom-to-top when flipY is set.
func (im *Image) Upload(x, y, z, w, h, d int, format, typ uint32, src []byte, pk Pack, flipY, premul bool) {
	pb := pixelBytes(format, typ)
	rs := pk.rowStride(w, pb)
	ih := pk.ImageHeight
	if ih <= 0 {
		ih = h
	}
	base := pk.SkipImages*ih*rs + pk.SkipRows*rs + pk.SkipPixels*pb
	f := im.Fmt
	integerDst := f.Class == ClsInt
	for zz := 0; zz < d; zz++ {
		for yy := 0; yy < h; yy++ {
			sy := yy
			if flipY {
				sy = h - 1 - yy
			}
			rowOff := base + zz*ih*rs + sy*rs
			di := im.idx(x, y+yy, z+zz)
			// fast path RGBA8 <- RGBA/UNSIGNED_BYTE
			if f.Class == ClsU8 && !premul && typ == GL_UNSIGNED_BYTE && format == GL_RGBA && f.Bits[0] == 8 && f.Chan >= 3 && f.Base == GL_RGBA {
				copy(im.U8[di:di+4*w], src[rowOff:rowOff+4*w])
				continue
			}
			for xx := 0; xx < w; xx++ {
				c, ci := decodePx(src, rowOff+xx*pb, format, typ)
				i := di + xx*4
				switch {
				case integerDst:
					for k := 0; k < 4; k++ {
						im.I[i+k] = ci[k]
					}
					if f.Chan < 4 {
						im.I[i+3] = 1
					}
					for k := f.Chan; k < 3; k++ {
						im.I[i+k] = 0
					}
				case f.IsDepth():
					im.F[i] = quantDepth(f, c[0])
					if f.SBits > 0 {
						im.St[(((z+zz)*im.H+(y+yy))*im.W)+x+xx] = uint8(ci[1])
					}
				default:
					if premul {
						c[0] *= c[3]
						c[1] *= c[3]
						c[2] *= c[3]
					}
					if f.Snorm {
						// source unsigned -> signed normalized not supported: keep
					}
					im.setFloat(i, c)
					if f.Class == ClsU8 {
						// formats without alpha read alpha 1, without green/blue read 0
						if f.Chan < 4 && !f.AlphaOnly && f.Base != GL_LUMINANCE_ALPHA {
							im.U8[i+3] = 255
						}
					}
				}
			}
		}
	}
}

// Download reads a rectangle out as client pixels (format/type) with the
// given pack state. Only color images are supported.
func (im *Image) Download(x, y, w, h int, format, typ uint32, dst []byte, pk Pack) {
	pb := pixelBytes(format, typ)
	rs := pk.rowStride(w, pb)
	base := pk.SkipRows*rs + pk.SkipPixels*pb
	f := im.Fmt
	put16 := func(o int, v uint16) { dst[o], dst[o+1] = byte(v), byte(v>>8) }
	put32 := func(o int, v uint32) {
		dst[o], dst[o+1], dst[o+2], dst[o+3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
	}
	n := formatComponents(format)
	for yy := 0; yy < h; yy++ {
		for xx := 0; xx < w; xx++ {
			o := base + yy*rs + xx*pb
			i := im.idx(x+xx, y+yy, 0)
			var c [4]float32
			var ci [4]int32
			switch f.Class {
			case ClsU8:
				if f.SRGB {
					c = [4]float32{float32(im.U8[i]) / 255, float32(im.U8[i+1]) / 255, float32(im.U8[i+2]) / 255, float32(im.U8[i+3]) / 255}
				} else if typ == GL_UNSIGNED_BYTE && n == 4 {
					copy(dst[o:o+4], im.U8[i:i+4])
					continue
				} else {
					for k := 0; k < 4; k++ {
						c[k] = float32(im.U8[i+k]) / 255
					}
				}
			case ClsF32:
				copy(c[:], im.F[i:i+4])
			case ClsInt:
				copy(ci[:], im.I[i:i+4])
				for k := 0; k < 4; k++ {
					c[k] = float32(ci[k])
				}
			}
			vals := [4]float32{c[0], c[1], c[2], c[3]}
			if f.Class != ClsInt && f.Chan < 4 && !f.Luminance && !f.AlphaOnly {
				vals[3] = 1
			}
			switch typ {
			case GL_UNSIGNED_BYTE:
				for k := 0; k < n; k++ {
					if f.Class == ClsInt {
						dst[o+k] = byte(ci[k])
					} else {
						dst[o+k] = unormToByte(vals[k])
					}
				}
			case GL_BYTE:
				for k := 0; k < n; k++ {
					dst[o+k] = byte(int8(clampf(vals[k], -1, 1) * 127))
				}
			case GL_UNSIGNED_SHORT:
				for k := 0; k < n; k++ {
					if f.Class == ClsInt {
						put16(o+k*2, uint16(ci[k]))
					} else {
						put16(o+k*2, uint16(clampf(vals[k], 0, 1)*65535+0.5))
					}
				}
			case GL_SHORT:
				for k := 0; k < n; k++ {
					put16(o+k*2, uint16(int16(ci[k])))
				}
			case GL_UNSIGNED_INT, GL_INT:
				for k := 0; k < n; k++ {
					if f.Class == ClsInt {
						put32(o+k*4, uint32(ci[k]))
					} else {
						put32(o+k*4, uint32(clampf(vals[k], 0, 1)*4294967295))
					}
				}
			case GL_FLOAT:
				for k := 0; k < n; k++ {
					put32(o+k*4, math.Float32bits(vals[k]))
				}
			case GL_HALF_FLOAT, GL_HALF_FLOAT_OES:
				for k := 0; k < n; k++ {
					put16(o+k*2, f32ToHalf(vals[k]))
				}
			case GL_UNSIGNED_SHORT_4_4_4_4:
				q := func(v float32) uint16 { return uint16(clampf(v, 0, 1)*15 + 0.5) }
				put16(o, q(vals[0])<<12|q(vals[1])<<8|q(vals[2])<<4|q(vals[3]))
			case GL_UNSIGNED_SHORT_5_5_5_1:
				q := func(v float32) uint16 { return uint16(clampf(v, 0, 1)*31 + 0.5) }
				put16(o, q(vals[0])<<11|q(vals[1])<<6|q(vals[2])<<1|uint16(clampf(vals[3], 0, 1)+0.5))
			case GL_UNSIGNED_SHORT_5_6_5:
				put16(o, uint16(clampf(vals[0], 0, 1)*31+0.5)<<11|uint16(clampf(vals[1], 0, 1)*63+0.5)<<5|uint16(clampf(vals[2], 0, 1)*31+0.5))
			case GL_UNSIGNED_INT_2_10_10_10_REV:
				q := func(v float32) uint32 { return uint32(clampf(v, 0, 1)*1023 + 0.5) }
				put32(o, q(vals[0])|q(vals[1])<<10|q(vals[2])<<20|uint32(clampf(vals[3], 0, 1)*3+0.5)<<30)
			}
		}
	}
}

// GenerateMipmap builds level dst from level src (box filter).
func downsample(src *Image) *Image {
	w, h, d := max(1, src.W/2), max(1, src.H/2), src.D
	dst := NewImage(src.Fmt, w, h, d, 1)
	f := src.Fmt
	for z := 0; z < d; z++ {
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				x0, x1 := min(2*x, src.W-1), min(2*x+1, src.W-1)
				y0, y1 := min(2*y, src.H-1), min(2*y+1, src.H-1)
				di := dst.idx(x, y, z)
				pts := [4]int{src.idx(x0, y0, z), src.idx(x1, y0, z), src.idx(x0, y1, z), src.idx(x1, y1, z)}
				switch f.Class {
				case ClsU8:
					for k := 0; k < 4; k++ {
						if f.SRGB && k < 3 {
							var s float32
							for _, p := range pts {
								s += srgbToLinear[src.U8[p+k]]
							}
							dst.U8[di+k] = unormToByte(linearToSRGB(s / 4))
						} else {
							s := 0
							for _, p := range pts {
								s += int(src.U8[p+k])
							}
							dst.U8[di+k] = uint8((s + 2) / 4)
						}
					}
				case ClsF32:
					for k := 0; k < 4; k++ {
						var s float32
						for _, p := range pts {
							s += src.F[p+k]
						}
						dst.F[di+k] = s / 4
						if f.Half {
							dst.F[di+k] = quantHalf(dst.F[di+k])
						}
					}
				}
			}
		}
	}
	return dst
}
