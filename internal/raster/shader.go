package raster

import (
	"math"
	"sort"
)

// Shader produces premultiplied RGBA8 source pixels for device coordinates.
type Shader interface {
	// ShadeRow fills dst (len >= 4n) for pixels x..x+n-1 on row y.
	ShadeRow(x, y, n int, dst []uint8)
}

// SolidShader is a constant premultiplied color.
type solidShader struct{ c [4]uint8 }

func (s solidShader) ShadeRow(x, y, n int, dst []uint8) {
	for i := 0; i < n; i++ {
		dst[i*4], dst[i*4+1], dst[i*4+2], dst[i*4+3] = s.c[0], s.c[1], s.c[2], s.c[3]
	}
}

// Solid returns a constant color shader.
func Solid(c Color) Shader {
	r, g, b, a := c.Premul8()
	return solidShader{[4]uint8{r, g, b, a}}
}

// GradKind selects the gradient geometry.
type GradKind int

const (
	LinearGrad GradKind = iota
	RadialGrad          // two-point conical
	ConicGrad
)

// GradStop is a color stop at Pos.
type GradStop struct {
	Pos   float64
	Color Color
}

// GradientDef describes a gradient in its own coordinate space.
type GradientDef struct {
	Kind   GradKind
	X0, Y0 float64 // linear start / radial start center / conic center
	R0     float64 // radial start radius / conic start angle (radians)
	X1, Y1 float64 // linear end / radial end center
	R1     float64
	Stops  []GradStop
	Repeat bool // repeating-*-gradient: t wraps
	Premul bool // interpolate premultiplied (CSS) instead of straight (canvas)
}

type gradShader struct {
	def   GradientDef
	inv   Matrix
	alpha float64
	pos   []float64
	col   [][4]float32 // interpolation space components (premul or straight) + alpha as [3]
	// precomputed
	dx, dy, invLen2 float64
	a, dr           float64
	concentric      bool
	valid           bool
}

// NewGradientShader builds a shader; m maps gradient space to device space.
func NewGradientShader(d GradientDef, m Matrix, alpha float64) Shader {
	inv, ok := m.Invert()
	g := &gradShader{def: d, inv: inv, alpha: alpha}
	if !ok || len(d.Stops) == 0 {
		return solidShader{}
	}
	stops := append([]GradStop(nil), d.Stops...)
	sort.SliceStable(stops, func(i, j int) bool { return stops[i].Pos < stops[j].Pos })
	g.pos = make([]float64, len(stops))
	g.col = make([][4]float32, len(stops))
	for i, s := range stops {
		g.pos[i] = s.Pos
		c := s.Color
		if d.Premul {
			g.col[i] = [4]float32{float32(c.R * c.A), float32(c.G * c.A), float32(c.B * c.A), float32(c.A)}
		} else {
			g.col[i] = [4]float32{float32(c.R), float32(c.G), float32(c.B), float32(c.A)}
		}
	}
	switch d.Kind {
	case LinearGrad:
		g.dx, g.dy = d.X1-d.X0, d.Y1-d.Y0
		l2 := g.dx*g.dx + g.dy*g.dy
		if l2 == 0 {
			if d.Repeat {
				return solidShader{}
			}
			// degenerate: canvas paints nothing, CSS uses last color
			return solidShader{}
		}
		g.invLen2 = 1 / l2
	case RadialGrad:
		g.dx, g.dy = d.X1-d.X0, d.Y1-d.Y0
		g.dr = d.R1 - d.R0
		g.a = g.dx*g.dx + g.dy*g.dy - g.dr*g.dr
		g.concentric = g.dx == 0 && g.dy == 0
		if g.concentric && g.dr == 0 {
			return solidShader{}
		}
	}
	return g
}

func (g *gradShader) colorAt(t float64) (r, gg, b, a float32) {
	n := len(g.pos)
	if g.def.Repeat {
		first, last := g.pos[0], g.pos[n-1]
		span := last - first
		if span > 0 {
			t = (t-first)/span - math.Floor((t-first)/span)
			t = first + t*span
		}
	}
	var c [4]float32
	switch {
	case t <= g.pos[0]:
		c = g.col[0]
	case t >= g.pos[n-1]:
		c = g.col[n-1]
	default:
		i := 1
		for i < n-1 && t >= g.pos[i] {
			i++
		}
		p0, p1 := g.pos[i-1], g.pos[i]
		if p1 <= p0 {
			c = g.col[i]
		} else {
			f := float32((t - p0) / (p1 - p0))
			c0, c1 := g.col[i-1], g.col[i]
			c = [4]float32{c0[0] + (c1[0]-c0[0])*f, c0[1] + (c1[1]-c0[1])*f, c0[2] + (c1[2]-c0[2])*f, c0[3] + (c1[3]-c0[3])*f}
		}
	}
	if g.def.Premul {
		return c[0], c[1], c[2], c[3]
	}
	return c[0] * c[3], c[1] * c[3], c[2] * c[3], c[3]
}

func (g *gradShader) ShadeRow(x, y, n int, dst []uint8) {
	fy := float64(y) + 0.5
	al := float32(g.alpha)
	for i := 0; i < n; i++ {
		fx := float64(x+i) + 0.5
		px := g.inv[0]*fx + g.inv[2]*fy + g.inv[4]
		py := g.inv[1]*fx + g.inv[3]*fy + g.inv[5]
		var t float64
		ok := true
		switch g.def.Kind {
		case LinearGrad:
			t = ((px-g.def.X0)*g.dx + (py-g.def.Y0)*g.dy) * g.invLen2
		case ConicGrad:
			ang := math.Atan2(py-g.def.Y0, px-g.def.X0) - g.def.R0
			t = ang / (2 * math.Pi)
			t -= math.Floor(t)
		case RadialGrad:
			pdx, pdy := px-g.def.X0, py-g.def.Y0
			if g.concentric {
				t = (math.Hypot(pdx, pdy) - g.def.R0) / g.dr
				if g.dr < 0 && false {
					t = 0
				}
			} else {
				b := pdx*g.dx + pdy*g.dy + g.def.R0*g.dr
				c := pdx*pdx + pdy*pdy - g.def.R0*g.def.R0
				ok = false
				if g.a == 0 {
					if b != 0 {
						s := c / (2 * b)
						if g.def.R0+s*g.dr >= 0 {
							t, ok = s, true
						}
					}
				} else {
					disc := b*b - g.a*c
					if disc >= 0 {
						sq := math.Sqrt(disc)
						s1, s2 := (b+sq)/g.a, (b-sq)/g.a
						if s1 < s2 {
							s1, s2 = s2, s1
						}
						if g.def.R0+s1*g.dr >= 0 {
							t, ok = s1, true
						} else if g.def.R0+s2*g.dr >= 0 {
							t, ok = s2, true
						}
					}
				}
			}
		}
		o := dst[i*4 : i*4+4 : i*4+4]
		if !ok || math.IsNaN(t) {
			o[0], o[1], o[2], o[3] = 0, 0, 0, 0
			continue
		}
		r, gg, b, a := g.colorAt(t)
		o[0], o[1], o[2], o[3] = f2u8(r*al), f2u8(gg*al), f2u8(b*al), f2u8(a*al)
	}
}

// FilterMode selects image sampling.
type FilterMode int

const (
	FilterNearest FilterMode = iota
	FilterBilinear
	FilterMipmap // bilinear within the nearest mip level
	FilterBicubic
)

// ImageShader samples a surface through a transform.
type ImageShader struct {
	Img        *Surface
	inv        Matrix // device -> image pixel space
	RepX, RepY bool
	Filter     FilterMode
	Clamp      IRect // sampling window in image pixels (empty = whole image)
	EdgeClamp  bool  // replicate edges instead of transparent outside (drawImage)
	alpha      float64
	mips       []*Surface
	level      int
	scale      float64
}

// NewImageShader samples img where m maps image pixel space to device space.
func NewImageShader(img *Surface, m Matrix, repX, repY bool, f FilterMode, alpha float64) *ImageShader {
	inv, ok := m.Invert()
	if !ok {
		inv = Matrix{0, 0, 0, 0, -1e9, -1e9}
	}
	s := &ImageShader{Img: img, inv: inv, RepX: repX, RepY: repY, Filter: f, alpha: alpha}
	s.Clamp = IRect{0, 0, img.W(), img.H()}
	if f == FilterMipmap || f == FilterBicubic {
		sc := math.Sqrt(math.Abs(inv.Det()))
		if sc > 1.5 && len(img.Pix) > 0 {
			lv := int(math.Floor(math.Log2(sc)))
			cur := img
			s.mips = append(s.mips, img)
			for i := 0; i < lv && cur.W() > 1 && cur.H() > 1; i++ {
				cur = halve(cur)
				s.mips = append(s.mips, cur)
			}
			s.level = len(s.mips) - 1
			if s.level > 0 {
				s.Img = s.mips[s.level]
				f := float64(int(1) << s.level)
				s.inv = Scale(1/f, 1/f).Mul(s.inv)
				s.Clamp = IRect{0, 0, s.Img.W(), s.Img.H()}
			}
		}
	}
	return s
}

// SetClamp restricts sampling to a window of image pixels (srcRect semantics).
func (s *ImageShader) SetClamp(r IRect) {
	if s.level > 0 {
		f := 1 << s.level
		r = IRect{r.X0 / f, r.Y0 / f, (r.X1 + f - 1) / f, (r.Y1 + f - 1) / f}
	}
	s.Clamp = r.Intersect(IRect{0, 0, s.Img.W(), s.Img.H()})
}

func halve(s *Surface) *Surface {
	w, h := (s.W()+1)/2, (s.H()+1)/2
	o := NewSurface(w, h)
	for y := 0; y < h; y++ {
		y0, y1 := min(2*y, s.H()-1), min(2*y+1, s.H()-1)
		for x := 0; x < w; x++ {
			x0, x1 := min(2*x, s.W()-1), min(2*x+1, s.W()-1)
			for c := 0; c < 4; c++ {
				sum := uint32(s.Pix[y0*s.Stride+x0*4+c]) + uint32(s.Pix[y0*s.Stride+x1*4+c]) + uint32(s.Pix[y1*s.Stride+x0*4+c]) + uint32(s.Pix[y1*s.Stride+x1*4+c])
				o.Pix[y*o.Stride+x*4+c] = uint8((sum + 2) / 4)
			}
		}
	}
	return o
}

func (s *ImageShader) texel(ix, iy int) (uint32, uint32, uint32, uint32, bool) {
	c := s.Clamp
	w, h := c.W(), c.H()
	if w <= 0 || h <= 0 {
		return 0, 0, 0, 0, false
	}
	if s.RepX {
		ix = c.X0 + ((ix-c.X0)%w+w)%w
	} else if ix < c.X0 || ix >= c.X1 {
		return 0, 0, 0, 0, false
	}
	if s.RepY {
		iy = c.Y0 + ((iy-c.Y0)%h+h)%h
	} else if iy < c.Y0 || iy >= c.Y1 {
		return 0, 0, 0, 0, false
	}
	o := iy*s.Img.Stride + ix*4
	p := s.Img.Pix[o : o+4 : o+4]
	return uint32(p[0]), uint32(p[1]), uint32(p[2]), uint32(p[3]), true
}

// clampTap handles the strictly-clamped (non repeating) bilinear tap that
// falls outside the window: edge pixels are replicated when the sample point
// itself is inside.
func (s *ImageShader) tap(ix, iy int, inside bool) (uint32, uint32, uint32, uint32, bool) {
	if !s.RepX && !s.RepY && inside {
		c := s.Clamp
		ix = min(max(ix, c.X0), c.X1-1)
		iy = min(max(iy, c.Y0), c.Y1-1)
	}
	return s.texel(ix, iy)
}

// noCopyFast disables shadeRowCopy (tests compare both paths).
var noCopyFast bool

// shadeRowCopy is the pixel-for-pixel case: the shader maps device pixels onto
// image pixels by a whole-pixel translation (no scale, no fraction), so every
// bilinear sample has weight 1 on a single texel and the row is a copy. The
// result is exactly what the general path computes. It reports false when the
// mapping is not of that kind.
func (s *ImageShader) shadeRowCopy(x, y, n int, dst []uint8) bool {
	inv := s.inv
	if inv[0] != 1 || inv[1] != 0 || inv[2] != 0 || inv[3] != 1 || s.EdgeClamp || s.alpha != 1 || s.Filter == FilterBicubic {
		return false
	}
	tx, ty := inv[4], inv[5]
	if tx != math.Trunc(tx) || ty != math.Trunc(ty) || math.Abs(tx) > 1<<30 || math.Abs(ty) > 1<<30 {
		return false
	}
	c := s.Clamp
	w, h := c.W(), c.H()
	if w <= 0 || h <= 0 {
		clear(dst[:n*4])
		return true
	}
	iy := y + int(ty)
	if s.RepY {
		iy = c.Y0 + ((iy-c.Y0)%h+h)%h
	} else if iy < c.Y0 || iy >= c.Y1 {
		clear(dst[:n*4])
		return true
	}
	row := s.Img.Pix[iy*s.Img.Stride:]
	off := x + int(tx)
	if s.RepX {
		for i := 0; i < n; i++ {
			ix := c.X0 + ((off+i-c.X0)%w+w)%w
			copy(dst[i*4:i*4+4], row[ix*4:ix*4+4])
		}
	} else {
		a := max(0, c.X0-off)
		b := min(n, c.X1-off)
		if a >= b {
			clear(dst[:n*4])
			return true
		}
		clear(dst[:a*4])
		copy(dst[a*4:b*4], row[(off+a)*4:(off+b)*4])
		clear(dst[b*4 : n*4])
	}
	for i := 0; i < n; i++ {
		o := dst[i*4 : i*4+4 : i*4+4]
		if o[0] > o[3] {
			o[0] = o[3]
		}
		if o[1] > o[3] {
			o[1] = o[3]
		}
		if o[2] > o[3] {
			o[2] = o[3]
		}
	}
	return true
}

func (s *ImageShader) ShadeRow(x, y, n int, dst []uint8) {
	if !noCopyFast && s.shadeRowCopy(x, y, n, dst) {
		return
	}
	fy := float64(y) + 0.5
	al := uint32(s.alpha*256 + 0.5)
	c := s.Clamp
	for i := 0; i < n; i++ {
		fx := float64(x+i) + 0.5
		u := s.inv[0]*fx + s.inv[2]*fy + s.inv[4]
		v := s.inv[1]*fx + s.inv[3]*fy + s.inv[5]
		o := dst[i*4 : i*4+4 : i*4+4]
		inside := (s.RepX || (u >= float64(c.X0) && u < float64(c.X1))) && (s.RepY || (v >= float64(c.Y0) && v < float64(c.Y1)))
		if s.EdgeClamp {
			inside = true
		}
		if !s.RepX && !s.RepY && !inside {
			o[0], o[1], o[2], o[3] = 0, 0, 0, 0
			continue
		}
		var r, g, b, a uint32
		switch s.Filter {
		case FilterNearest:
			ix, iy := int(math.Floor(u)), int(math.Floor(v))
			var ok bool
			r, g, b, a, ok = s.tap(ix, iy, inside)
			if !ok {
				o[0], o[1], o[2], o[3] = 0, 0, 0, 0
				continue
			}
		case FilterBicubic:
			r, g, b, a = s.cubic(u, v, inside)
		default:
			uu, vv := u-0.5, v-0.5
			x0f, y0f := math.Floor(uu), math.Floor(vv)
			fxw, fyw := uint32((uu-x0f)*256+0.5), uint32((vv-y0f)*256+0.5)
			x0, y0 := int(x0f), int(y0f)
			w00, w10, w01, w11 := (256-fxw)*(256-fyw), fxw*(256-fyw), (256-fxw)*fyw, fxw*fyw
			// taps with a zero weight contribute nothing: skip them (the common
			// case of a 1:1 pixel-aligned mapping needs a single tap)
			var r00, g00, b00, a00, r10, g10, b10, a10, r01, g01, b01, a01, r11, g11, b11, a11 uint32
			if w00 != 0 {
				r00, g00, b00, a00, _ = s.tap(x0, y0, inside)
			}
			if w10 != 0 {
				r10, g10, b10, a10, _ = s.tap(x0+1, y0, inside)
			}
			if w01 != 0 {
				r01, g01, b01, a01, _ = s.tap(x0, y0+1, inside)
			}
			if w11 != 0 {
				r11, g11, b11, a11, _ = s.tap(x0+1, y0+1, inside)
			}
			r = (r00*w00 + r10*w10 + r01*w01 + r11*w11 + 32768) >> 16
			g = (g00*w00 + g10*w10 + g01*w01 + g11*w11 + 32768) >> 16
			b = (b00*w00 + b10*w10 + b01*w01 + b11*w11 + 32768) >> 16
			a = (a00*w00 + a10*w10 + a01*w01 + a11*w11 + 32768) >> 16
		}
		if al != 256 {
			r, g, b, a = (r*al)>>8, (g*al)>>8, (b*al)>>8, (a*al)>>8
		}
		o[0], o[1], o[2], o[3] = uint8(min(r, 255)), uint8(min(g, 255)), uint8(min(b, 255)), uint8(min(a, 255))
		if o[0] > o[3] {
			o[0] = o[3]
		}
		if o[1] > o[3] {
			o[1] = o[3]
		}
		if o[2] > o[3] {
			o[2] = o[3]
		}
	}
}

// mitchell weights (B=C=1/3).
func mitchell(t float64) float64 {
	t = math.Abs(t)
	const b, c = 1.0 / 3, 1.0 / 3
	if t < 1 {
		return ((12-9*b-6*c)*t*t*t + (-18+12*b+6*c)*t*t + (6 - 2*b)) / 6
	}
	if t < 2 {
		return ((-b-6*c)*t*t*t + (6*b+30*c)*t*t + (-12*b-48*c)*t + (8*b + 24*c)) / 6
	}
	return 0
}

func (s *ImageShader) cubic(u, v float64, inside bool) (uint32, uint32, uint32, uint32) {
	uu, vv := u-0.5, v-0.5
	x0f, y0f := math.Floor(uu), math.Floor(vv)
	fx, fy := uu-x0f, vv-y0f
	var wx, wy [4]float64
	for k := 0; k < 4; k++ {
		wx[k] = mitchell(fx - float64(k-1))
		wy[k] = mitchell(fy - float64(k-1))
	}
	var r, g, b, a float64
	for j := 0; j < 4; j++ {
		for i := 0; i < 4; i++ {
			w := wx[i] * wy[j]
			tr, tg, tb, ta, _ := s.tap(int(x0f)+i-1, int(y0f)+j-1, inside)
			r += w * float64(tr)
			g += w * float64(tg)
			b += w * float64(tb)
			a += w * float64(ta)
		}
	}
	cl := func(v float64) uint32 {
		if v < 0 {
			return 0
		}
		if v > 255 {
			return 255
		}
		return uint32(v + 0.5)
	}
	a8 := cl(a)
	return min(cl(r), a8), min(cl(g), a8), min(cl(b), a8), a8
}
