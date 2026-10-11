package gl

import (
	"math"

	"github.com/sweefbrowser/sweef/internal/gl/glsl"
)

// Texture is a GL texture object.
type Texture struct {
	ID        uint32
	Target    uint32
	Faces     [6][]*Image
	MinF      uint32
	MagF      uint32
	WrapS     uint32
	WrapT     uint32
	WrapR     uint32
	MinLod    float32
	MaxLod    float32
	BaseLevel int
	MaxLevel  int
	CmpMode   uint32
	CmpFunc   uint32
	Aniso     float32
	Immutable bool
	ImmLevels int
	deleted   bool
}

func newTexture(id uint32) *Texture {
	return &Texture{ID: id, MinF: GL_NEAREST_MIPMAP_LINEAR, MagF: GL_LINEAR, WrapS: GL_REPEAT, WrapT: GL_REPEAT, WrapR: GL_REPEAT,
		MinLod: -1000, MaxLod: 1000, MaxLevel: 1000, CmpMode: GL_NONE, CmpFunc: GL_LEQUAL, Aniso: 1}
}

// SamplerParams holds sampler-object state (WebGL2).
type SamplerParams struct {
	MinF, MagF, WrapS, WrapT, WrapR uint32
	MinLod, MaxLod                  float32
	CmpMode, CmpFunc                uint32
}

func (t *Texture) levelImg(face, level int) *Image {
	if level < 0 || level >= len(t.Faces[face]) {
		return nil
	}
	return t.Faces[face][level]
}

func (t *Texture) setLevel(face, level int, im *Image) {
	for len(t.Faces[face]) <= level {
		t.Faces[face] = append(t.Faces[face], nil)
	}
	t.Faces[face][level] = im
}

func isPOT(n int) bool { return n > 0 && n&(n-1) == 0 }

func mipCount(w, h, d int) int {
	n := 1
	for w > 1 || h > 1 || d > 1 {
		w, h, d = max(1, w/2), max(1, h/2), max(1, d/2)
		n++
	}
	return n
}

// texView is the sampling-time view of a texture + sampler state
// implementing glsl.Sampler.
type texView struct {
	tex      *Texture
	dim      int // 2, 3, 6, 22
	levels   [6][]*Image
	base     int
	last     int // last mip level available
	minF     uint32
	magF     uint32
	wrap     [3]uint32
	minLod   float32
	maxLod   float32
	cmp      bool
	cmpFunc  uint32
	complete bool
	es3      bool
	isInt    bool
	depthRGB bool // WebGL1: depth texture replicates into RGB
	mips     bool
	bias     float32
}

var defaultTexel = [4]float32{0, 0, 0, 1}

// buildView prepares a view; es3 selects WebGL2 semantics.
func buildView(t *Texture, sp *SamplerParams, es3 bool, floatLinear, halfLinear bool) *texView {
	v := &texView{tex: t, es3: es3}
	switch t.Target {
	case GL_TEXTURE_2D:
		v.dim = 2
	case GL_TEXTURE_3D:
		v.dim = 3
	case GL_TEXTURE_CUBE_MAP:
		v.dim = 6
	case GL_TEXTURE_2D_ARRAY:
		v.dim = 22
	}
	minF, magF := t.MinF, t.MagF
	ws, wt, wr := t.WrapS, t.WrapT, t.WrapR
	v.minLod, v.maxLod = t.MinLod, t.MaxLod
	v.cmp, v.cmpFunc = t.CmpMode == GL_COMPARE_REF_TO_TEXTURE, t.CmpFunc
	if sp != nil {
		minF, magF, ws, wt, wr = sp.MinF, sp.MagF, sp.WrapS, sp.WrapT, sp.WrapR
		v.minLod, v.maxLod = sp.MinLod, sp.MaxLod
		v.cmp, v.cmpFunc = sp.CmpMode == GL_COMPARE_REF_TO_TEXTURE, sp.CmpFunc
	}
	v.minF, v.magF = minF, magF
	v.wrap = [3]uint32{ws, wt, wr}
	nf := 1
	if v.dim == 6 {
		nf = 6
	}
	v.base = t.BaseLevel
	if !es3 {
		v.base = 0
	}
	img0 := t.levelImg(0, v.base)
	if img0 == nil || img0.W == 0 || img0.H == 0 {
		return v
	}
	f := img0.Fmt
	v.isInt = f.Integer
	v.depthRGB = !es3 && f.IsDepth()
	v.mips = minF != GL_NEAREST && minF != GL_LINEAR
	// determine available levels
	q := mipCount(img0.W, img0.H, img0.D)
	if v.dim != 3 {
		q = mipCount(img0.W, img0.H, 1)
	}
	last := v.base + q - 1
	if es3 {
		last = min(last, t.MaxLevel)
	}
	if !v.mips {
		last = v.base
	}
	// completeness of mip chain
	w, h, d := img0.W, img0.H, img0.D
	for face := 0; face < nf; face++ {
		fi := t.levelImg(face, v.base)
		if fi == nil || fi.W != img0.W || fi.H != img0.H || fi.Fmt != f {
			return v
		}
	}
	if v.dim == 6 && img0.W != img0.H {
		return v
	}
	for l := v.base; l <= last; l++ {
		for face := 0; face < nf; face++ {
			im := t.levelImg(face, l)
			if im == nil || im.W != w || im.H != h || (v.dim != 2 && v.dim != 6 && im.D != d && (v.dim == 3 || v.dim == 22 && false)) || im.Fmt != f {
				if v.mips {
					return v
				}
			}
			v.levels[face] = append(v.levels[face], im)
		}
		w, h = max(1, w/2), max(1, h/2)
		if v.dim == 3 {
			d = max(1, d/2)
		}
	}
	v.last = len(v.levels[0]) - 1
	// WebGL1 NPOT restrictions
	if !es3 && (!isPOT(img0.W) || !isPOT(img0.H)) {
		if v.mips || ws != GL_CLAMP_TO_EDGE || wt != GL_CLAMP_TO_EDGE {
			return v
		}
	}
	// float textures need linear support for LINEAR filters
	if f.Class == ClsF32 && f.Float && !f.IsDepth() {
		lin := minF != GL_NEAREST && minF != GL_NEAREST_MIPMAP_NEAREST || magF != GL_NEAREST
		if lin {
			if f.Half && !halfLinear && !es3 || !f.Half && !floatLinear {
				return v
			}
		}
	}
	if v.isInt && (magF != GL_NEAREST || minF != GL_NEAREST && minF != GL_NEAREST_MIPMAP_NEAREST) {
		return v
	}
	v.complete = len(v.levels[0]) > 0
	return v
}

func (v *texView) Size(lod int) [3]int {
	l := min(max(lod, 0), len(v.levels[0])-1)
	if len(v.levels[0]) == 0 {
		return [3]int{}
	}
	im := v.levels[0][l]
	return [3]int{im.W, im.H, im.D}
}

func wrapCoord(i, n int, mode uint32) int {
	switch mode {
	case GL_CLAMP_TO_EDGE:
		if i < 0 {
			return 0
		}
		if i >= n {
			return n - 1
		}
		return i
	case GL_MIRRORED_REPEAT:
		p := 2 * n
		i %= p
		if i < 0 {
			i += p
		}
		if i >= n {
			i = p - 1 - i
		}
		return i
	}
	i %= n
	if i < 0 {
		i += n
	}
	return i
}

func (v *texView) texel(im *Image, x, y, z int) [4]float32 {
	c := im.Get(x, y, z)
	if im.Fmt.IsDepth() {
		if v.depthRGB {
			return [4]float32{c[0], c[0], c[0], 1}
		}
		return [4]float32{c[0], 0, 0, 1}
	}
	return c
}

func (v *texView) Texel(x, y, z, lod int) [4]uint32 {
	if !v.complete || len(v.levels[0]) == 0 {
		return [4]uint32{0, 0, 0, 1}
	}
	l := min(max(lod, 0), len(v.levels[0])-1)
	face := 0
	im := v.levels[face][l]
	if x < 0 || y < 0 || x >= im.W || y >= im.H {
		return [4]uint32{}
	}
	if v.dim == 3 || v.dim == 22 {
		if z < 0 || z >= im.D {
			return [4]uint32{}
		}
	} else {
		z = 0
	}
	if im.Fmt.Class == ClsInt {
		return im.Raw(x, y, z)
	}
	c := v.texel(im, x, y, z)
	return [4]uint32{math.Float32bits(c[0]), math.Float32bits(c[1]), math.Float32bits(c[2]), math.Float32bits(c[3])}
}

func (v *texView) SampleRaw(r *glsl.SampleReq) [4]uint32 {
	if !v.complete {
		return [4]uint32{0, 0, 0, 1}
	}
	lvl := v.base
	var face int
	s, t, p := float64(r.Coord[0]), float64(r.Coord[1]), float64(r.Coord[2])
	var layer int
	if v.dim == 6 {
		face, s, t = cubeFace(r.Coord[0], r.Coord[1], r.Coord[2])
	}
	if v.dim == 22 {
		layer = int(math.Floor(p + 0.5))
	}
	_ = lvl
	im := v.levels[face][0]
	x := wrapCoord(int(math.Floor(s*float64(im.W)))+int(r.Offset[0]), im.W, v.wrap[0])
	y := wrapCoord(int(math.Floor(t*float64(im.H)))+int(r.Offset[1]), im.H, v.wrap[1])
	z := 0
	if v.dim == 3 {
		z = wrapCoord(int(math.Floor(p*float64(im.D)))+int(r.Offset[2]), im.D, v.wrap[2])
	}
	if v.dim == 22 {
		z = min(max(layer, 0), im.D-1)
	}
	return im.Raw(x, y, z)
}

// cubeFace maps a direction to (face, s, t) in [0,1].
func cubeFace(x, y, z float32) (int, float64, float64) {
	ax, ay, az := math.Abs(float64(x)), math.Abs(float64(y)), math.Abs(float64(z))
	var face int
	var sc, tc, ma float64
	switch {
	case ax >= ay && ax >= az:
		ma = ax
		if x >= 0 {
			face, sc, tc = 0, -float64(z), -float64(y)
		} else {
			face, sc, tc = 1, float64(z), -float64(y)
		}
	case ay >= az:
		ma = ay
		if y >= 0 {
			face, sc, tc = 2, float64(x), float64(z)
		} else {
			face, sc, tc = 3, float64(x), -float64(z)
		}
	default:
		ma = az
		if z >= 0 {
			face, sc, tc = 4, float64(x), -float64(y)
		} else {
			face, sc, tc = 5, -float64(x), -float64(y)
		}
	}
	if ma == 0 {
		return 0, 0.5, 0.5
	}
	return face, 0.5 * (sc/ma + 1), 0.5 * (tc/ma + 1)
}

func (v *texView) Sample(r *glsl.SampleReq) [4]float32 {
	if !v.complete {
		return defaultTexel
	}
	nl := len(v.levels[0])
	im0 := v.levels[0][0]
	// level of detail
	var lod float32
	switch r.Mode {
	case glsl.SampleLod:
		lod = r.Lod + r.Bias
	default:
		var rho float64
		if r.Mode == glsl.SampleGrad || r.DX != [3]float32{} || r.DY != [3]float32{} {
			rho = v.rho(r, im0)
		}
		if rho > 0 {
			lod = float32(math.Log2(rho))
		} else {
			lod = -1000
		}
		lod += r.Bias
	}
	if v.es3 {
		// texture lod bias not modelled
	}
	if lod < v.minLod {
		lod = v.minLod
	}
	if lod > v.maxLod {
		lod = v.maxLod
	}
	// magnification vs minification
	filter := v.magF
	mag := lod <= 0
	if !mag {
		filter = v.minF
	}
	lastL := float32(nl - 1)
	if !v.mips {
		lastL = 0
	}
	if lod > lastL {
		lod = lastL
	}
	if lod < 0 {
		lod = 0
	}
	mipLinear := !mag && (filter == GL_NEAREST_MIPMAP_LINEAR || filter == GL_LINEAR_MIPMAP_LINEAR)
	linear := filter == GL_LINEAR || filter == GL_LINEAR_MIPMAP_NEAREST || filter == GL_LINEAR_MIPMAP_LINEAR
	l0 := 0
	var frac float32
	switch {
	case mag || !v.mips:
		l0 = 0
	case mipLinear:
		l0 = int(lod)
		frac = lod - float32(l0)
	default:
		l0 = int(lod + 0.5)
		if l0 >= nl {
			l0 = nl - 1
		}
	}
	c0 := v.sampleLevel(r, l0, linear)
	if mipLinear && frac > 0 && l0+1 < nl {
		c1 := v.sampleLevel(r, l0+1, linear)
		for k := 0; k < 4; k++ {
			c0[k] = c0[k]*(1-frac) + c1[k]*frac
		}
	}
	return c0
}

func (v *texView) rho(r *glsl.SampleReq, im *Image) float64 {
	w, h, d := float64(im.W), float64(im.H), float64(im.D)
	switch v.dim {
	case 6:
		face, _, _ := cubeFace(r.Coord[0], r.Coord[1], r.Coord[2])
		x, y, z := float64(r.Coord[0]), float64(r.Coord[1]), float64(r.Coord[2])
		comp := func(d [3]float32) (float64, float64) {
			dx, dy, dz := float64(d[0]), float64(d[1]), float64(d[2])
			var sc, tc, ma, dsc, dtc, dma float64
			switch face {
			case 0:
				sc, tc, ma, dsc, dtc, dma = -z, -y, x, -dz, -dy, dx
			case 1:
				sc, tc, ma, dsc, dtc, dma = z, -y, -x, dz, -dy, -dx
			case 2:
				sc, tc, ma, dsc, dtc, dma = x, z, y, dx, dz, dy
			case 3:
				sc, tc, ma, dsc, dtc, dma = x, -z, -y, dx, -dz, -dy
			case 4:
				sc, tc, ma, dsc, dtc, dma = x, -y, z, dx, -dy, dz
			default:
				sc, tc, ma, dsc, dtc, dma = -x, -y, -z, -dx, -dy, -dz
			}
			if ma == 0 {
				return 0, 0
			}
			ds := 0.5 * (dsc*ma - sc*dma) / (ma * ma)
			dt := 0.5 * (dtc*ma - tc*dma) / (ma * ma)
			return ds * w, dt * w
		}
		ax, ay := comp(r.DX)
		bx, by := comp(r.DY)
		return math.Max(math.Hypot(ax, ay), math.Hypot(bx, by))
	case 3:
		dx := math.Sqrt(sq(float64(r.DX[0])*w) + sq(float64(r.DX[1])*h) + sq(float64(r.DX[2])*d))
		dy := math.Sqrt(sq(float64(r.DY[0])*w) + sq(float64(r.DY[1])*h) + sq(float64(r.DY[2])*d))
		return math.Max(dx, dy)
	}
	dx := math.Hypot(float64(r.DX[0])*w, float64(r.DX[1])*h)
	dy := math.Hypot(float64(r.DY[0])*w, float64(r.DY[1])*h)
	return math.Max(dx, dy)
}

func sq(x float64) float64 { return x * x }

func (v *texView) sampleLevel(r *glsl.SampleReq, level int, linear bool) [4]float32 {
	face := 0
	s, t, p := float64(r.Coord[0]), float64(r.Coord[1]), float64(r.Coord[2])
	if v.dim == 6 {
		face, s, t = cubeFace(r.Coord[0], r.Coord[1], r.Coord[2])
	}
	if level >= len(v.levels[face]) {
		level = len(v.levels[face]) - 1
	}
	im := v.levels[face][level]
	w, h := im.W, im.H
	shadow := v.cmp && im.Fmt.IsDepth()
	layer := 0
	if v.dim == 22 {
		layer = min(max(int(math.Floor(p+0.5)), 0), im.D-1)
	}
	offx, offy := int(r.Offset[0]), int(r.Offset[1])
	cmp := func(c [4]float32) [4]float32 {
		var pass bool
		d := c[0]
		ref := r.Ref
		switch v.cmpFunc {
		case GL_NEVER:
			pass = false
		case GL_LESS:
			pass = ref < d
		case GL_EQUAL:
			pass = ref == d
		case GL_LEQUAL:
			pass = ref <= d
		case GL_GREATER:
			pass = ref > d
		case GL_NOTEQUAL:
			pass = ref != d
		case GL_GEQUAL:
			pass = ref >= d
		default:
			pass = true
		}
		if pass {
			return [4]float32{1, 1, 1, 1}
		}
		return [4]float32{0, 0, 0, 1}
	}
	fetch := func(x, y, z int) [4]float32 {
		c := v.texel(im, x, y, z)
		if shadow {
			return cmp(c)
		}
		return c
	}
	if v.dim == 3 {
		d := im.D
		fz := p * float64(d)
		if !linear {
			x := wrapCoord(int(math.Floor(s*float64(w)))+offx, w, v.wrap[0])
			y := wrapCoord(int(math.Floor(t*float64(h)))+offy, h, v.wrap[1])
			z := wrapCoord(int(math.Floor(fz))+int(r.Offset[2]), d, v.wrap[2])
			return fetch(x, y, z)
		}
		fx, fy, fzz := s*float64(w)-0.5, t*float64(h)-0.5, fz-0.5
		x0, y0, z0 := int(math.Floor(fx)), int(math.Floor(fy)), int(math.Floor(fzz))
		ax, ay, az := float32(fx-float64(x0)), float32(fy-float64(y0)), float32(fzz-float64(z0))
		var out [4]float32
		for dz := 0; dz < 2; dz++ {
			wz := az
			if dz == 0 {
				wz = 1 - az
			}
			for dy := 0; dy < 2; dy++ {
				wy := ay
				if dy == 0 {
					wy = 1 - ay
				}
				for dx := 0; dx < 2; dx++ {
					wx := ax
					if dx == 0 {
						wx = 1 - ax
					}
					c := fetch(wrapCoord(x0+dx+offx, w, v.wrap[0]), wrapCoord(y0+dy+offy, h, v.wrap[1]), wrapCoord(z0+dz+int(r.Offset[2]), d, v.wrap[2]))
					wt := wx * wy * wz
					for k := 0; k < 4; k++ {
						out[k] += c[k] * wt
					}
				}
			}
		}
		return out
	}
	wrapS, wrapT := v.wrap[0], v.wrap[1]
	if v.dim == 6 {
		wrapS, wrapT = GL_CLAMP_TO_EDGE, GL_CLAMP_TO_EDGE
	}
	if !linear {
		x := wrapCoord(int(math.Floor(s*float64(w)))+offx, w, wrapS)
		y := wrapCoord(int(math.Floor(t*float64(h)))+offy, h, wrapT)
		return fetch(x, y, layer)
	}
	fx, fy := s*float64(w)-0.5, t*float64(h)-0.5
	x0, y0 := int(math.Floor(fx)), int(math.Floor(fy))
	ax, ay := float32(fx-float64(x0)), float32(fy-float64(y0))
	if v.dim == 6 {
		cf := func(i, j int) [4]float32 {
			if i >= 0 && i < w && j >= 0 && j < h {
				return v.texel(im, i, j, 0)
			}
			// seamless: find the neighbouring face texel
			sc := 2*(float64(i)+0.5)/float64(w) - 1
			tc := 2*(float64(j)+0.5)/float64(h) - 1
			dx, dy, dz := faceDir(face, sc, tc)
			f2, s2, t2 := cubeFace(float32(dx), float32(dy), float32(dz))
			if f2 >= len(v.levels) || level >= len(v.levels[f2]) {
				return v.texel(im, min(max(i, 0), w-1), min(max(j, 0), h-1), 0)
			}
			im2 := v.levels[f2][level]
			return v.texel(im2, min(max(int(s2*float64(im2.W)), 0), im2.W-1), min(max(int(t2*float64(im2.H)), 0), im2.H-1), 0)
		}
		c00, c10, c01, c11 := cf(x0, y0), cf(x0+1, y0), cf(x0, y0+1), cf(x0+1, y0+1)
		var out [4]float32
		for k := 0; k < 4; k++ {
			top := c00[k]*(1-ax) + c10[k]*ax
			bot := c01[k]*(1-ax) + c11[k]*ax
			out[k] = top*(1-ay) + bot*ay
		}
		return out
	}
	xa, xb := wrapCoord(x0+offx, w, wrapS), wrapCoord(x0+1+offx, w, wrapS)
	ya, yb := wrapCoord(y0+offy, h, wrapT), wrapCoord(y0+1+offy, h, wrapT)
	c00, c10 := fetch(xa, ya, layer), fetch(xb, ya, layer)
	c01, c11 := fetch(xa, yb, layer), fetch(xb, yb, layer)
	var out [4]float32
	for k := 0; k < 4; k++ {
		top := c00[k]*(1-ax) + c10[k]*ax
		bot := c01[k]*(1-ax) + c11[k]*ax
		out[k] = top*(1-ay) + bot*ay
	}
	return out
}

// faceDir returns a direction for face coordinates sc,tc in [-1,1] (may lie
// outside the face for seamless filtering).
func faceDir(face int, sc, tc float64) (x, y, z float64) {
	switch face {
	case 0:
		return 1, -tc, -sc
	case 1:
		return -1, -tc, sc
	case 2:
		return sc, 1, tc
	case 3:
		return sc, -1, -tc
	case 4:
		return sc, -tc, 1
	}
	return -sc, -tc, -1
}
