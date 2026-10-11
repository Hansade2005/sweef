package gl

import "math"

// Class is the storage class of an image.
type Class uint8

const (
	ClsU8  Class = iota // 4 bytes per texel (RGBA, normalized)
	ClsF32              // 4 float32 per texel
	ClsInt              // 4 int32 per texel (signed or unsigned bit patterns)
)

// Format describes a sized internal format.
type Format struct {
	Internal   uint32
	Base       uint32 // RGBA RGB RG RED ALPHA LUMINANCE LUMINANCE_ALPHA DEPTH_COMPONENT DEPTH_STENCIL (+_INTEGER)
	Class      Class
	Chan       int // number of color channels present
	Bits       [4]int
	DBits      int
	SBits      int
	SRGB       bool
	Half       bool // quantize to binary16 on store
	Snorm      bool
	Unsigned   bool // integer formats: unsigned
	Integer    bool
	Float      bool
	Filterable bool
	ColorRend  bool
	DepthRend  bool
	Luminance  bool // L replicated into RGB
	AlphaOnly  bool
	BytesPerPx int // bytes of an equivalent client-side pixel (readPixels etc. not used)
	Es3Only    bool
}

func (f *Format) IsDepth() bool   { return f.DBits > 0 }
func (f *Format) IsStencil() bool { return f.SBits > 0 }
func (f *Format) IsColor() bool   { return f.DBits == 0 && f.SBits == 0 }

var formats = map[uint32]*Format{}

func addFmt(f Format) {
	ff := f
	formats[f.Internal] = &ff
}

func init() {
	u8 := func(in, base uint32, ch int, bits [4]int, rend bool) Format {
		return Format{Internal: in, Base: base, Class: ClsU8, Chan: ch, Bits: bits, Filterable: true, ColorRend: rend}
	}
	addFmt(u8(GL_RGBA8, GL_RGBA, 4, [4]int{8, 8, 8, 8}, true))
	addFmt(u8(GL_RGB8, GL_RGB, 3, [4]int{8, 8, 8, 0}, true))
	addFmt(u8(GL_RG8, GL_RG, 2, [4]int{8, 8, 0, 0}, true))
	addFmt(u8(GL_R8, GL_RED, 1, [4]int{8, 0, 0, 0}, true))
	addFmt(u8(GL_RGBA4, GL_RGBA, 4, [4]int{4, 4, 4, 4}, true))
	addFmt(u8(GL_RGB5_A1, GL_RGBA, 4, [4]int{5, 5, 5, 1}, true))
	addFmt(u8(GL_RGB565, GL_RGB, 3, [4]int{5, 6, 5, 0}, true))
	addFmt(u8(GL_RGB10_A2, GL_RGBA, 4, [4]int{10, 10, 10, 2}, true))
	f := u8(GL_SRGB8_ALPHA8, GL_RGBA, 4, [4]int{8, 8, 8, 8}, true)
	f.SRGB = true
	addFmt(f)
	f = u8(GL_SRGB8, GL_RGB, 3, [4]int{8, 8, 8, 0}, false)
	f.SRGB = true
	addFmt(f)
	for _, s := range []struct {
		in uint32
		b  uint32
		ch int
	}{{GL_R8_SNORM, GL_RED, 1}, {GL_RG8_SNORM, GL_RG, 2}, {GL_RGB8_SNORM, GL_RGB, 3}, {GL_RGBA8_SNORM, GL_RGBA, 4}} {
		f := u8(s.in, s.b, s.ch, [4]int{8, 8, 8, 8}, false)
		f.Class = ClsF32
		f.Snorm = true
		addFmt(f)
	}
	// unsized legacy bases, stored as 8 bit
	l := u8(GL_LUMINANCE, GL_LUMINANCE, 1, [4]int{8, 0, 0, 0}, false)
	l.Luminance = true
	addFmt(l)
	la := u8(GL_LUMINANCE_ALPHA, GL_LUMINANCE_ALPHA, 2, [4]int{8, 0, 0, 8}, false)
	la.Luminance = true
	addFmt(la)
	a := u8(GL_ALPHA, GL_ALPHA, 1, [4]int{0, 0, 0, 8}, false)
	a.AlphaOnly = true
	addFmt(a)
	// float formats
	fl := func(in, base uint32, ch int, half bool, rend bool) Format {
		b := 32
		if half {
			b = 16
		}
		return Format{Internal: in, Base: base, Class: ClsF32, Chan: ch, Bits: [4]int{b, b, b, b}, Half: half, Float: true, Filterable: half, ColorRend: rend}
	}
	addFmt(fl(GL_RGBA16F, GL_RGBA, 4, true, true))
	addFmt(fl(GL_RGB16F, GL_RGB, 3, true, false))
	addFmt(fl(GL_RG16F, GL_RG, 2, true, true))
	addFmt(fl(GL_R16F, GL_RED, 1, true, true))
	addFmt(fl(GL_RGBA32F, GL_RGBA, 4, false, true))
	addFmt(fl(GL_RGB32F, GL_RGB, 3, false, false))
	addFmt(fl(GL_RG32F, GL_RG, 2, false, true))
	addFmt(fl(GL_R32F, GL_RED, 1, false, true))
	r := fl(GL_R11F_G11F_B10F, GL_RGB, 3, true, true)
	addFmt(r)
	addFmt(fl(GL_RGB9_E5, GL_RGB, 3, true, false))
	// integer formats
	in := func(i, base uint32, ch, bits int, uns bool) {
		addFmt(Format{Internal: i, Base: base, Class: ClsInt, Chan: ch, Bits: [4]int{bits, bits, bits, bits}, Integer: true, Unsigned: uns, ColorRend: true})
	}
	in(GL_R8I, GL_RED_INTEGER, 1, 8, false)
	in(GL_R8UI, GL_RED_INTEGER, 1, 8, true)
	in(GL_R16I, GL_RED_INTEGER, 1, 16, false)
	in(GL_R16UI, GL_RED_INTEGER, 1, 16, true)
	in(GL_R32I, GL_RED_INTEGER, 1, 32, false)
	in(GL_R32UI, GL_RED_INTEGER, 1, 32, true)
	in(GL_RG8I, GL_RG_INTEGER, 2, 8, false)
	in(GL_RG8UI, GL_RG_INTEGER, 2, 8, true)
	in(GL_RG16I, GL_RG_INTEGER, 2, 16, false)
	in(GL_RG16UI, GL_RG_INTEGER, 2, 16, true)
	in(GL_RG32I, GL_RG_INTEGER, 2, 32, false)
	in(GL_RG32UI, GL_RG_INTEGER, 2, 32, true)
	in(GL_RGB8I, GL_RGB_INTEGER, 3, 8, false)
	in(GL_RGB8UI, GL_RGB_INTEGER, 3, 8, true)
	in(GL_RGB16I, GL_RGB_INTEGER, 3, 16, false)
	in(GL_RGB16UI, GL_RGB_INTEGER, 3, 16, true)
	in(GL_RGB32I, GL_RGB_INTEGER, 3, 32, false)
	in(GL_RGB32UI, GL_RGB_INTEGER, 3, 32, true)
	in(GL_RGBA8I, GL_RGBA_INTEGER, 4, 8, false)
	in(GL_RGBA8UI, GL_RGBA_INTEGER, 4, 8, true)
	in(GL_RGBA16I, GL_RGBA_INTEGER, 4, 16, false)
	in(GL_RGBA16UI, GL_RGBA_INTEGER, 4, 16, true)
	in(GL_RGBA32I, GL_RGBA_INTEGER, 4, 32, false)
	in(GL_RGBA32UI, GL_RGBA_INTEGER, 4, 32, true)
	in(GL_RGB10_A2UI, GL_RGBA_INTEGER, 4, 10, true)
	formats[GL_RGB8I].ColorRend, formats[GL_RGB8UI].ColorRend = false, false
	formats[GL_RGB16I].ColorRend, formats[GL_RGB16UI].ColorRend = false, false
	formats[GL_RGB32I].ColorRend, formats[GL_RGB32UI].ColorRend = false, false
	// depth / stencil
	addFmt(Format{Internal: GL_DEPTH_COMPONENT16, Base: GL_DEPTH_COMPONENT, Class: ClsF32, DBits: 16, Filterable: true, DepthRend: true})
	addFmt(Format{Internal: GL_DEPTH_COMPONENT24, Base: GL_DEPTH_COMPONENT, Class: ClsF32, DBits: 24, Filterable: true, DepthRend: true})
	addFmt(Format{Internal: GL_DEPTH_COMPONENT32F, Base: GL_DEPTH_COMPONENT, Class: ClsF32, DBits: 32, Float: true, Filterable: true, DepthRend: true})
	addFmt(Format{Internal: GL_DEPTH24_STENCIL8, Base: GL_DEPTH_STENCIL, Class: ClsF32, DBits: 24, SBits: 8, Filterable: true, DepthRend: true})
	addFmt(Format{Internal: GL_DEPTH32F_STENCIL8, Base: GL_DEPTH_STENCIL, Class: ClsF32, DBits: 32, SBits: 8, Float: true, Filterable: true, DepthRend: true})
	addFmt(Format{Internal: GL_STENCIL_INDEX8, Base: GL_DEPTH_STENCIL, Class: ClsU8, SBits: 8, DepthRend: true})
	for _, f := range formats {
		f.Es3Only = f.Internal != GL_RGBA4 && f.Internal != GL_RGB5_A1 && f.Internal != GL_RGB565 && f.Internal != GL_DEPTH_COMPONENT16 &&
			f.Internal != GL_LUMINANCE && f.Internal != GL_LUMINANCE_ALPHA && f.Internal != GL_ALPHA && f.Internal != GL_STENCIL_INDEX8
	}
}

// channelDefaults returns the value a missing channel reads as.
func (f *Format) missing(c int) float32 {
	if c == 3 {
		return 1
	}
	return 0
}

// ---- half float helpers -------------------------------------------------------

func f32ToHalf(f float32) uint16 {
	b := math.Float32bits(f)
	sign := uint16(b>>16) & 0x8000
	exp := int((b >> 23) & 0xFF)
	man := b & 0x7FFFFF
	if exp == 255 {
		if man != 0 {
			return sign | 0x7E00
		}
		return sign | 0x7C00
	}
	e := exp - 127 + 15
	if e >= 31 {
		return sign | 0x7C00
	}
	if e <= 0 {
		if e < -10 {
			return sign
		}
		man |= 0x800000
		shift := uint(14 - e)
		half := uint16(man >> shift)
		rem := man & (1<<shift - 1)
		mid := uint32(1) << (shift - 1)
		if rem > mid || rem == mid && half&1 == 1 {
			half++
		}
		return sign | half
	}
	half := uint16(e<<10) | uint16(man>>13)
	rem := man & 0x1FFF
	if rem > 0x1000 || rem == 0x1000 && half&1 == 1 {
		half++
	}
	return sign | half
}

func halfToF32(h uint16) float32 {
	sign := uint32(h&0x8000) << 16
	exp := uint32(h>>10) & 0x1F
	man := uint32(h & 0x3FF)
	switch {
	case exp == 0:
		if man == 0 {
			return math.Float32frombits(sign)
		}
		e := uint32(127 - 15 + 1)
		for man&0x400 == 0 {
			man <<= 1
			e--
		}
		man &= 0x3FF
		return math.Float32frombits(sign | e<<23 | man<<13)
	case exp == 31:
		return math.Float32frombits(sign | 0x7F800000 | man<<13)
	}
	return math.Float32frombits(sign | (exp+127-15)<<23 | man<<13)
}

func quantHalf(f float32) float32 { return halfToF32(f32ToHalf(f)) }

// sRGB conversion tables.
var srgbToLinear [256]float32

func init() {
	for i := range srgbToLinear {
		c := float64(i) / 255
		if c <= 0.04045 {
			srgbToLinear[i] = float32(c / 12.92)
		} else {
			srgbToLinear[i] = float32(math.Pow((c+0.055)/1.055, 2.4))
		}
	}
}

func linearToSRGB(l float32) float32 {
	if l <= 0 {
		return 0
	}
	if l >= 1 {
		return 1
	}
	if l <= 0.0031308 {
		return l * 12.92
	}
	return float32(1.055*math.Pow(float64(l), 1/2.4) - 0.055)
}

func clampf(x, lo, hi float32) float32 {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}

func unormToByte(f float32) uint8 {
	if !(f > 0) {
		return 0
	}
	if f >= 1 {
		return 255
	}
	return uint8(f*255 + 0.5)
}
