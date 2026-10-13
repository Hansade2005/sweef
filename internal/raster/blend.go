package raster

import (
	"encoding/binary"
	"math"
	"strings"
)

// CompositeOp is a Porter-Duff operator or blend mode.
type CompositeOp int

const (
	SourceOver CompositeOp = iota
	SourceIn
	SourceOut
	SourceAtop
	DestinationOver
	DestinationIn
	DestinationOut
	DestinationAtop
	Lighter
	Copy
	Xor
	Clear
	Multiply
	Screen
	Overlay
	Darken
	Lighten
	ColorDodge
	ColorBurn
	HardLight
	SoftLight
	Difference
	Exclusion
	Hue
	Saturation
	ColorBlend
	Luminosity
	Plus // alias of Lighter for CSS plus-lighter
)

var opNames = map[string]CompositeOp{
	"source-over": SourceOver, "source-in": SourceIn, "source-out": SourceOut, "source-atop": SourceAtop,
	"destination-over": DestinationOver, "destination-in": DestinationIn, "destination-out": DestinationOut,
	"destination-atop": DestinationAtop, "lighter": Lighter, "copy": Copy, "xor": Xor,
	"multiply": Multiply, "screen": Screen, "overlay": Overlay, "darken": Darken, "lighten": Lighten,
	"color-dodge": ColorDodge, "color-burn": ColorBurn, "hard-light": HardLight, "soft-light": SoftLight,
	"difference": Difference, "exclusion": Exclusion, "hue": Hue, "saturation": Saturation,
	"color": ColorBlend, "luminosity": Luminosity,
	"normal": SourceOver, "plus-lighter": Lighter,
}

// ParseOp maps a canvas globalCompositeOperation / CSS mix-blend-mode name.
func ParseOp(s string) (CompositeOp, bool) {
	op, ok := opNames[strings.ToLower(s)]
	return op, ok
}

// OpName is the canonical canvas name of op.
func OpName(op CompositeOp) string {
	if op == Plus {
		op = Lighter
	}
	for k, v := range opNames {
		if v == op && k != "normal" && k != "plus-lighter" {
			return k
		}
	}
	return "source-over"
}

// unbounded operators change destination pixels where the source is empty.
func (op CompositeOp) unbounded() bool {
	switch op {
	case SourceIn, SourceOut, DestinationIn, DestinationAtop, Copy, Clear:
		return true
	}
	return false
}

func lum(r, g, b float32) float32 { return 0.3*r + 0.59*g + 0.11*b }

func clipColor(r, g, b float32) (float32, float32, float32) {
	l := lum(r, g, b)
	n := min(r, g, b)
	x := max(r, g, b)
	if n < 0 {
		d := l - n
		r, g, b = l+(r-l)*l/d, l+(g-l)*l/d, l+(b-l)*l/d
	}
	if x > 1 {
		d := x - l
		r, g, b = l+(r-l)*(1-l)/d, l+(g-l)*(1-l)/d, l+(b-l)*(1-l)/d
	}
	return r, g, b
}

func setLum(r, g, b, l float32) (float32, float32, float32) {
	d := l - lum(r, g, b)
	return clipColor(r+d, g+d, b+d)
}

func sat(r, g, b float32) float32 { return max(r, g, b) - min(r, g, b) }

func setSat(r, g, b, s float32) (float32, float32, float32) {
	c := [3]float32{r, g, b}
	// indices of min, mid, max
	mx, md, mn := 0, 1, 2
	if c[mx] < c[md] {
		mx, md = md, mx
	}
	if c[md] < c[mn] {
		md, mn = mn, md
	}
	if c[mx] < c[md] {
		mx, md = md, mx
	}
	if c[mx] > c[mn] {
		c[md] = (c[md] - c[mn]) * s / (c[mx] - c[mn])
		c[mx] = s
	} else {
		c[md], c[mx] = 0, 0
	}
	c[mn] = 0
	return c[0], c[1], c[2]
}

func blendSep(op CompositeOp, cb, cs float32) float32 {
	switch op {
	case Multiply:
		return cb * cs
	case Screen:
		return cb + cs - cb*cs
	case Overlay:
		return blendSep(HardLight, cs, cb)
	case Darken:
		return min(cb, cs)
	case Lighten:
		return max(cb, cs)
	case ColorDodge:
		if cb == 0 {
			return 0
		}
		if cs >= 1 {
			return 1
		}
		return min(1, cb/(1-cs))
	case ColorBurn:
		if cb >= 1 {
			return 1
		}
		if cs <= 0 {
			return 0
		}
		return 1 - min(1, (1-cb)/cs)
	case HardLight:
		if cs <= 0.5 {
			return cb * 2 * cs
		}
		t := 2*cs - 1
		return cb + t - cb*t
	case SoftLight:
		if cs <= 0.5 {
			return cb - (1-2*cs)*cb*(1-cb)
		}
		var d float32
		if cb <= 0.25 {
			d = ((16*cb-12)*cb + 4) * cb
		} else {
			d = float32(math.Sqrt(float64(cb)))
		}
		return cb + (2*cs-1)*(d-cb)
	case Difference:
		if cb > cs {
			return cb - cs
		}
		return cs - cb
	case Exclusion:
		return cb + cs - 2*cb*cs
	}
	return cs
}

// compositePx combines premultiplied float source and destination.
func compositePx(op CompositeOp, sr, sg, sb, sa, dr, dg, db, da float32) (float32, float32, float32, float32) {
	var fa, fb float32
	switch op {
	case SourceOver:
		fa, fb = 1, 1-sa
	case SourceIn:
		fa, fb = da, 0
	case SourceOut:
		fa, fb = 1-da, 0
	case SourceAtop:
		fa, fb = da, 1-sa
	case DestinationOver:
		fa, fb = 1-da, 1
	case DestinationIn:
		fa, fb = 0, sa
	case DestinationOut:
		fa, fb = 0, 1-sa
	case DestinationAtop:
		fa, fb = 1-da, sa
	case Lighter, Plus:
		return min(1, sr+dr), min(1, sg+dg), min(1, sb+db), min(1, sa+da)
	case Copy:
		return sr, sg, sb, sa
	case Xor:
		fa, fb = 1-da, 1-sa
	case Clear:
		return 0, 0, 0, 0
	default:
		// blend modes
		ar := sa + da - sa*da
		if sa == 0 {
			return dr, dg, db, da
		}
		if da == 0 {
			return sr, sg, sb, sa
		}
		cs := [3]float32{sr / sa, sg / sa, sb / sa}
		cb := [3]float32{dr / da, dg / da, db / da}
		var bl [3]float32
		switch op {
		case Hue:
			r, g, b := setSat(cs[0], cs[1], cs[2], sat(cb[0], cb[1], cb[2]))
			bl[0], bl[1], bl[2] = setLum(r, g, b, lum(cb[0], cb[1], cb[2]))
		case Saturation:
			r, g, b := setSat(cb[0], cb[1], cb[2], sat(cs[0], cs[1], cs[2]))
			bl[0], bl[1], bl[2] = setLum(r, g, b, lum(cb[0], cb[1], cb[2]))
		case ColorBlend:
			bl[0], bl[1], bl[2] = setLum(cs[0], cs[1], cs[2], lum(cb[0], cb[1], cb[2]))
		case Luminosity:
			bl[0], bl[1], bl[2] = setLum(cb[0], cb[1], cb[2], lum(cs[0], cs[1], cs[2]))
		default:
			for i := 0; i < 3; i++ {
				bl[i] = blendSep(op, cb[i], cs[i])
			}
		}
		k := sa * da
		return (1-da)*sr + (1-sa)*dr + k*bl[0], (1-da)*sg + (1-sa)*dg + k*bl[1], (1-da)*sb + (1-sa)*db + k*bl[2], ar
	}
	return sr*fa + dr*fb, sg*fa + dg*fb, sb*fa + db*fb, sa*fa + da*fb
}

// mul255 computes round(a*b/255).
func mul255(a, b uint32) uint32 {
	t := a*b + 128
	return (t + t>>8) >> 8
}

// blendRow composites premultiplied src (len 4n) onto dst through coverage
// cov (nil = full). Both slices are RGBA8 premultiplied.
func blendRow(dst, src []uint8, cov []uint8, op CompositeOp, n int) {
	if op == SourceOver {
		for i := 0; i < n; i++ {
			s := src[i*4 : i*4+4 : i*4+4]
			sa := uint32(s[3])
			sr, sg, sb := uint32(s[0]), uint32(s[1]), uint32(s[2])
			if cov != nil {
				c := uint32(cov[i])
				if c == 0 {
					continue
				}
				if c != 255 {
					sr, sg, sb, sa = mul255(sr, c), mul255(sg, c), mul255(sb, c), mul255(sa, c)
				}
			}
			if sa == 0 {
				continue
			}
			d := dst[i*4 : i*4+4 : i*4+4]
			if sa == 255 {
				d[0], d[1], d[2], d[3] = uint8(sr), uint8(sg), uint8(sb), 255
				continue
			}
			ia := 255 - sa
			d[0] = uint8(min(255, sr+mul255(uint32(d[0]), ia)))
			d[1] = uint8(min(255, sg+mul255(uint32(d[1]), ia)))
			d[2] = uint8(min(255, sb+mul255(uint32(d[2]), ia)))
			d[3] = uint8(min(255, sa+mul255(uint32(d[3]), ia)))
		}
		return
	}
	const inv = 1.0 / 255
	for i := 0; i < n; i++ {
		var c uint32 = 255
		if cov != nil {
			c = uint32(cov[i])
		}
		if c == 0 && !op.unbounded() {
			continue
		}
		s := src[i*4 : i*4+4 : i*4+4]
		d := dst[i*4 : i*4+4 : i*4+4]
		sr, sg, sb, sa := float32(s[0])*inv, float32(s[1])*inv, float32(s[2])*inv, float32(s[3])*inv
		if c != 255 {
			k := float32(c) * inv
			sr, sg, sb, sa = sr*k, sg*k, sb*k, sa*k
		}
		dr, dg, db, da := float32(d[0])*inv, float32(d[1])*inv, float32(d[2])*inv, float32(d[3])*inv
		r, g, b, a := compositePx(op, sr, sg, sb, sa, dr, dg, db, da)
		d[0], d[1], d[2], d[3] = f2u8(r), f2u8(g), f2u8(b), f2u8(a)
		// keep premultiplied invariant after rounding
		if d[0] > d[3] {
			d[0] = d[3]
		}
		if d[1] > d[3] {
			d[1] = d[3]
		}
		if d[2] > d[3] {
			d[2] = d[3]
		}
	}
}

// blendSolidCov is blendRow(SourceOver) for a solid premultiplied colour: dst
// gets col through the coverage row cov. It must stay in step with blendRow.
func blendSolidCov(dst, cov []uint8, col [4]uint8) {
	cr, cg, cb, ca := uint32(col[0]), uint32(col[1]), uint32(col[2]), uint32(col[3])
	if ca == 0 {
		return
	}
	n := len(cov)
	opaque := ca == 255
	px := binary.LittleEndian.Uint32(col[:])
	i := 0
	for i < n {
		// eight pixels at a time: nothing to do where the coverage is 0, and a
		// plain store where it is full and the colour is opaque (the interior of
		// a fill, the empty area around a mask)
		if i+8 <= n {
			w := binary.LittleEndian.Uint64(cov[i:])
			if w == 0 {
				i += 8
				continue
			}
			if opaque && w == ^uint64(0) {
				d := dst[i*4 : i*4+32 : i*4+32]
				for k := 0; k < 32; k += 4 {
					binary.LittleEndian.PutUint32(d[k:], px)
				}
				i += 8
				continue
			}
		}
		cv := cov[i]
		if cv == 0 {
			i++
			continue
		}
		sr, sg, sb, sa := cr, cg, cb, ca
		if cv != 255 {
			c := uint32(cv)
			sr, sg, sb, sa = mul255(sr, c), mul255(sg, c), mul255(sb, c), mul255(sa, c)
		}
		if sa == 0 {
			i++
			continue
		}
		d := dst[i*4 : i*4+4 : i*4+4]
		if sa == 255 {
			d[0], d[1], d[2], d[3] = uint8(sr), uint8(sg), uint8(sb), 255
			i++
			continue
		}
		ia := 255 - sa
		d[0] = uint8(min(255, sr+mul255(uint32(d[0]), ia)))
		d[1] = uint8(min(255, sg+mul255(uint32(d[1]), ia)))
		d[2] = uint8(min(255, sb+mul255(uint32(d[2]), ia)))
		d[3] = uint8(min(255, sa+mul255(uint32(d[3]), ia)))
		i++
	}
}

func f2u8(v float32) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 1 {
		return 255
	}
	return uint8(v*255 + 0.5)
}

// lerpRow moves dst toward res by k/255 (used for clip masks on unbounded ops).
func lerpRow(dst, orig []uint8, k []uint8, n int) {
	for i := 0; i < n; i++ {
		kk := uint32(k[i])
		if kk == 255 {
			continue
		}
		for j := 0; j < 4; j++ {
			o, r := uint32(orig[i*4+j]), uint32(dst[i*4+j])
			if r >= o {
				dst[i*4+j] = uint8(o + mul255(r-o, kk))
			} else {
				dst[i*4+j] = uint8(o - mul255(o-r, kk))
			}
		}
	}
}
