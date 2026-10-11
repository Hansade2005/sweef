package raster

import (
	"math"
	"strings"
)

// Filter effects run on the element's layer (device pixels, premultiplied
// RGBA). Every primitive result keeps its color space (linearRGB or sRGB,
// per color-interpolation-filters) and its primitive subregion.

type fxState struct {
	r       *svgR
	f       *svgFilterSpec
	n       *svgNode
	st      *sStyle
	m, inv  Matrix
	hasInv  bool
	layer   *Surface
	region  IRect
	scx     float64
	scy     float64
	pux     float64
	puy     float64
	results map[string]*Surface
	subs    map[*Surface]IRect
	lin     map[*Surface]bool
	srcS    *Surface // SourceGraphic in sRGB
	srcL    *Surface // SourceGraphic in linearRGB (lazy)
	last    *Surface
	// noise is anchored at (noiseX, noiseY) in user space (the border box
	// origin for CSS filter references)
	noiseX, noiseY float64
}

func (fx *fxState) isLinear(p *svgNode) bool {
	if v := strings.ToLower(strings.TrimSpace(p.A["color-interpolation-filters"])); v != "" && v != "inherit" {
		return v != "srgb"
	}
	return fx.f.linear
}

// conv returns s converted from its own space to linear (to == true) or sRGB.
func (fx *fxState) conv(s *Surface, to bool) *Surface {
	from, ok := fx.lin[s]
	if !ok {
		from = fx.f.linear
	}
	if from == to {
		return s
	}
	c := cloneSurface(s)
	if to {
		convertSurface(c, &s2lin)
	} else {
		convertSurface(c, &lin2s)
	}
	fx.lin[c] = to
	fx.subs[c] = fx.subOf(s)
	return c
}

func (fx *fxState) source(lin bool) *Surface {
	if !lin {
		return fx.srcS
	}
	if fx.srcL == nil {
		fx.srcL = cloneSurface(fx.srcS)
		convertSurface(fx.srcL, &s2lin)
		fx.lin[fx.srcL] = true
		fx.subs[fx.srcL] = fx.region
	}
	return fx.srcL
}

func (fx *fxState) subOf(s *Surface) IRect {
	if r, ok := fx.subs[s]; ok {
		return r
	}
	return fx.region
}

// input resolves the in / in2 attribute of p to a surface in p's color space.
func (fx *fxState) input(p *svgNode, key string, lin bool) *Surface {
	name := strings.TrimSpace(p.A[key])
	var s *Surface
	switch name {
	case "SourceGraphic":
		s = fx.source(lin)
	case "SourceAlpha":
		s = cloneSurface(fx.srcS)
		for i := 0; i+3 < len(s.Pix); i += 4 {
			s.Pix[i], s.Pix[i+1], s.Pix[i+2] = 0, 0, 0
		}
		fx.lin[s] = lin
		fx.subs[s] = fx.region
		return s
	case "BackgroundImage", "BackgroundAlpha", "FillPaint", "StrokePaint":
		s = NewSurfaceRect(fx.layer.R)
		fx.lin[s] = lin
		fx.subs[s] = fx.region
		return s
	case "":
		if fx.last != nil {
			s = fx.last
		} else {
			s = fx.source(lin)
		}
	default:
		if v, ok := fx.results[name]; ok {
			s = v
		} else if fx.last != nil {
			s = fx.last
		} else {
			s = fx.source(lin)
		}
	}
	return fx.conv(s, lin)
}

// inSub is the subregion of the input named by attribute key of p.
func (fx *fxState) inSub(p *svgNode, key string) IRect {
	switch name := strings.TrimSpace(p.A[key]); name {
	case "SourceGraphic", "SourceAlpha", "BackgroundImage", "BackgroundAlpha", "FillPaint", "StrokePaint":
		return fx.region
	case "":
		if fx.last != nil {
			return fx.subOf(fx.last)
		}
		return fx.region
	default:
		if v, ok := fx.results[name]; ok {
			return fx.subOf(v)
		}
		if fx.last != nil {
			return fx.subOf(fx.last)
		}
		return fx.region
	}
}

func (fx *fxState) devRect(x, y, w, h float64) IRect {
	dr, _ := transformRect(Rect{x, y, w, h}, fx.m)
	return IRect{int(math.Floor(dr.X + 1e-6)), int(math.Floor(dr.Y + 1e-6)), int(math.Ceil(dr.X + dr.W - 1e-6)), int(math.Ceil(dr.Y + dr.H - 1e-6))}
}

// userRect maps a device rect back to filter user space.
func (fx *fxState) userRect(d IRect) Rect {
	if !fx.hasInv {
		return fx.f.user
	}
	r, _ := transformRect(Rect{float64(d.X0), float64(d.Y0), float64(d.X1 - d.X0), float64(d.Y1 - d.Y0)}, fx.inv)
	return r
}

// subregion computes the primitive subregion of p (device px, within the
// filter region) and its user-space rect. def is the default (union of the
// input subregions).
func (fx *fxState) subregion(p *svgNode, def IRect) (IRect, Rect) {
	f := fx.f
	hasSub := false
	for _, k := range []string{"x", "y", "width", "height"} {
		if _, ok := p.A[k]; ok {
			hasSub = true
		}
	}
	du := fx.userRect(def)
	if !hasSub {
		return def, du
	}
	st := fx.st
	r := fx.r
	get := func(k string, d float64, axis byte) (float64, bool) {
		v, ok := p.A[k]
		if !ok {
			return d, false
		}
		if f.primObb && f.hasBB {
			vv := strings.TrimSpace(v)
			if strings.HasSuffix(vv, "%") {
				fv, _ := svgNumber(vv[:len(vv)-1])
				return fv / 100, true
			}
			fv, _ := svgNumber(vv)
			return fv, true
		}
		return r.length(v, r.refFor(axis), st.fontSize, d), true
	}
	x, xs := get("x", du.X, 'x')
	y, ys := get("y", du.Y, 'y')
	w, ws := get("width", du.W, 'x')
	h, hs := get("height", du.H, 'y')
	if f.primObb && f.hasBB {
		if xs {
			x = f.bb.X + x*f.bb.W
		}
		if ys {
			y = f.bb.Y + y*f.bb.H
		}
		if ws {
			w *= f.bb.W
		}
		if hs {
			h *= f.bb.H
		}
	}
	u := Rect{x, y, w, h}
	return fx.devRect(x, y, w, h).Intersect(fx.region), u
}

// vec maps a user-space offset (dx, dy) to a device-space vector.
func (fx *fxState) vec(dx, dy float64) (float64, float64) {
	dx, dy = dx*fx.pux, dy*fx.puy
	return fx.m[0]*dx + fx.m[2]*dy, fx.m[1]*dx + fx.m[3]*dy
}

func (fx *fxState) num(p *svgNode, k string, d float64) float64 {
	if v, ok := svgNumber(p.A[k]); ok {
		return v
	}
	return d
}

func (fx *fxState) pairs(p *svgNode, k string, d float64) (float64, float64) {
	v := splitNums(p.A[k])
	switch len(v) {
	case 0:
		return d, d
	case 1:
		return v[0], v[0]
	}
	return v[0], v[1]
}

func (fx *fxState) color(p *svgNode, key string, def Color, lin bool) Color {
	col := def
	if v, ok := p.A[key]; ok {
		if cc, ok := ParseColor(v, fx.st.color); ok {
			col = cc
		}
	}
	if lin {
		col.R, col.G, col.B = srgbToLinear(col.R), srgbToLinear(col.G), srgbToLinear(col.B)
	}
	return col
}

func (r *svgR) runFilter(f *svgFilterSpec, n *svgNode, st *sStyle, m Matrix) {
	r.runFilterAt(f, n, st, m, 0, 0)
}

func (r *svgR) runFilterAt(f *svgFilterSpec, n *svgNode, st *sStyle, m Matrix, nx, ny float64) {
	c := r.c
	layer := c.surf
	if f.region.Empty() || len(f.prims) == 0 {
		layer.Clear()
		return
	}
	fx := &fxState{r: r, f: f, n: n, st: st, m: m, layer: layer, region: f.region,
		results: map[string]*Surface{}, subs: map[*Surface]IRect{}, lin: map[*Surface]bool{}, noiseX: nx, noiseY: ny}
	fx.inv, fx.hasInv = m.Invert()
	fx.scx, fx.scy = math.Hypot(m[0], m[1]), math.Hypot(m[2], m[3])
	fx.pux, fx.puy = 1, 1
	if f.primObb && f.hasBB {
		fx.pux, fx.puy = f.bb.W, f.bb.H
	}
	src := cloneSurface(layer)
	clearOutside(src, f.region)
	fx.srcS = src
	fx.lin[src] = false
	fx.subs[src] = f.region
	for _, p := range f.prims {
		lin := fx.isLinear(p)
		out, defSub := fx.primitive(p, lin)
		if out == nil {
			continue
		}
		sub, _ := fx.subregion(p, defSub)
		clearOutside(out, sub)
		fx.lin[out] = lin
		fx.subs[out] = sub
		if name := p.A["result"]; name != "" {
			fx.results[name] = out
		}
		fx.last = out
	}
	if fx.last == nil {
		layer.Clear()
		return
	}
	res := fx.conv(fx.last, false)
	if res == fx.srcS {
		res = cloneSurface(res)
	}
	clearOutside(res, f.region)
	copy(layer.Pix, res.Pix)
}

// primitive runs one filter primitive and returns its (unclipped) result and
// the default subregion for it.
func (fx *fxState) primitive(p *svgNode, lin bool) (*Surface, IRect) {
	layer := fx.layer
	region := fx.region
	def := region
	// default subregion: union of the subregions of the inputs
	inDef := func(keys ...string) {
		var u IRect
		first := true
		for _, k := range keys {
			if k == "in2" && strings.TrimSpace(p.A[k]) == "" {
				continue
			}
			sr := fx.inSub(p, k)
			if first {
				u, first = sr, false
			} else {
				u = u.Union(sr)
			}
		}
		if !first {
			def = u
		}
	}
	switch p.Tag {
	case "fegaussianblur":
		inDef("in")
		sx, sy := fx.pairs(p, "stddeviation", 0)
		in := fx.input(p, "in", lin)
		out := cloneSurface(in)
		if sx < 0 || sy < 0 || (sx == 0 && sy == 0 && false) {
			out.Clear()
		} else if sx > 0 || sy > 0 {
			if (sx == 0) != (sy == 0) {
				// one zero axis: blur only the other (spec)
			}
			blurPlane(out.Pix, out.R.W(), out.R.H(), out.Stride, 4, sx*fx.pux*fx.scx, sy*fx.puy*fx.scy)
		}
		return out, def
	case "feoffset":
		inDef("in")
		in := fx.input(p, "in", lin)
		dx, dy := fx.vec(fx.num(p, "dx", 0), fx.num(p, "dy", 0))
		return offsetSurface(in, dx, dy), def
	case "fedropshadow":
		inDef("in")
		in := fx.input(p, "in", lin)
		sx, sy := fx.pairs(p, "stddeviation", 2)
		dx, dy := fx.vec(fx.num(p, "dx", 2), fx.num(p, "dy", 2))
		sh := cloneSurface(in)
		col := RGBA8(0, 0, 0, 255)
		if v, ok := p.A["flood-color"]; ok {
			if cc, ok := ParseColor(v, fx.st.color); ok {
				col = cc
				if lin {
					col.R, col.G, col.B = srgbToLinear(col.R), srgbToLinear(col.G), srgbToLinear(col.B)
				}
			}
		}
		if v, ok := p.A["flood-opacity"]; ok {
			col.A *= parseOpacity(v, 1)
		}
		tintAlpha(sh, col)
		if sx > 0 || sy > 0 {
			blurPlane(sh.Pix, sh.R.W(), sh.R.H(), sh.Stride, 4, sx*fx.pux*fx.scx, sy*fx.puy*fx.scy)
		}
		sh = offsetSurface(sh, dx, dy)
		composite(sh, nil, surfShader{in, 256}, SourceOver, &clipState{R: sh.R})
		return sh, def
	case "feflood":
		out := NewSurfaceRect(layer.R)
		col := fx.color(p, "flood-color", RGBA8(0, 0, 0, 255), lin)
		if v, ok := p.A["flood-opacity"]; ok {
			col.A *= parseOpacity(v, 1)
		}
		sub, _ := fx.subregion(p, region)
		composite(out, nil, Solid(col), SourceOver, &clipState{R: sub.Intersect(out.R)})
		return out, region
	case "fecolormatrix":
		inDef("in")
		out := cloneSurface(fx.input(p, "in", lin))
		colorMatrixPrim(out, p)
		return out, def
	case "feblend":
		inDef("in", "in2")
		in, in2 := fx.input(p, "in", lin), fx.input(p, "in2", lin)
		out := cloneSurface(in2)
		op := SourceOver
		if o, ok := ParseOp(p.A["mode"]); ok && p.A["mode"] != "" {
			op = o
		}
		composite(out, nil, surfShader{in, 256}, op, &clipState{R: out.R})
		return out, def
	case "femerge":
		out := NewSurfaceRect(layer.R)
		var u IRect
		first := true
		for _, k := range p.Kids {
			if k.Tag == "femergenode" {
				composite(out, nil, surfShader{fx.input(k, "in", lin), 256}, SourceOver, &clipState{R: out.R})
				if sr := fx.inSub(k, "in"); first {
					u, first = sr, false
				} else {
					u = u.Union(sr)
				}
			}
		}
		if !first {
			def = u
		}
		return out, def
	case "fecomposite":
		inDef("in", "in2")
		in, in2 := fx.input(p, "in", lin), fx.input(p, "in2", lin)
		op := p.A["operator"]
		if op == "arithmetic" {
			return arithmetic(in, in2, fx.num(p, "k1", 0), fx.num(p, "k2", 0), fx.num(p, "k3", 0), fx.num(p, "k4", 0)), def
		}
		out := cloneSurface(in2)
		o := SourceOver
		switch op {
		case "in":
			o = SourceIn
		case "out":
			o = SourceOut
		case "atop":
			o = SourceAtop
		case "xor":
			o = Xor
		}
		composite(out, nil, surfShader{in, 256}, o, &clipState{R: out.R})
		return out, def
	case "fecomponenttransfer":
		inDef("in")
		out := cloneSurface(fx.input(p, "in", lin))
		componentTransfer(out, p)
		return out, def
	case "femorphology":
		inDef("in")
		rx, ry := fx.pairs(p, "radius", 0)
		return morphology(fx.input(p, "in", lin), int(math.Round(rx*fx.pux*fx.scx)), int(math.Round(ry*fx.puy*fx.scy)), p.A["operator"] == "dilate"), def
	case "feturbulence":
		return fx.turbulence(p), region
	case "fedisplacementmap":
		inDef("in", "in2")
		return fx.displacement(p, fx.input(p, "in", lin), fx.input(p, "in2", lin)), def
	case "feconvolvematrix":
		inDef("in")
		return fx.convolve(p, fx.input(p, "in", lin)), def
	case "fetile":
		inDef("in")
		in := fx.input(p, "in", lin)
		sub, _ := fx.subregion(p, region)
		return fx.tile(in, fx.subOf(in), sub), region
	case "feimage":
		return fx.image(p, lin), region
	case "fediffuselighting", "fespecularlighting":
		inDef("in")
		in := fx.input(p, "in", lin)
		sub, _ := fx.subregion(p, def)
		return fx.lighting(p, in, sub, lin), def
	}
	return nil, def
}

// ---- turbulence ------------------------------------------------------------------------

func (fx *fxState) turbulence(p *svgNode) *Surface {
	out := NewSurfaceRect(fx.layer.R)
	bfx, bfy := fx.pairs(p, "basefrequency", 0)
	oct := int(fx.num(p, "numoctaves", 1))
	if bfx < 0 || bfy < 0 || oct <= 0 {
		return out
	}
	seed := int64(math.Floor(fx.num(p, "seed", 0) + 0.5))
	fractal := p.A["type"] == "fractalNoise"
	stitch := p.A["stitchtiles"] == "stitch"
	sub, su := fx.subregion(p, fx.region)
	g := newTurbGen(seed)
	sub = sub.Intersect(out.R)
	if sub.Empty() {
		return out
	}
	w := sub.W()
	parallelFor(sub.H(), 8, func(lo, hi int) {
		for yy := sub.Y0 + lo; yy < sub.Y0+hi; yy++ {
			for xx := sub.X0; xx < sub.X0+w; xx++ {
				ux, uy := float64(xx)+0.5, float64(yy)+0.5
				if fx.hasInv {
					ux, uy = fx.inv.Apply(ux, uy)
				}
				ux += 0.5 - fx.noiseX
				uy += 0.5 - fx.noiseY
				var c [4]float64
				for ch := 0; ch < 4; ch++ {
					s := g.turbulence(ch, ux, uy, bfx, bfy, oct, fractal, stitch, su.X, su.Y, su.W, su.H)
					if fractal {
						s = (s + 1) / 2
					}
					c[ch] = math.Max(0, math.Min(1, s))
				}
				o := (yy-out.R.Y0)*out.Stride + (xx-out.R.X0)*4
				a := c[3]
				out.Pix[o] = uint8(c[0]*a*255 + 0.5)
				out.Pix[o+1] = uint8(c[1]*a*255 + 0.5)
				out.Pix[o+2] = uint8(c[2]*a*255 + 0.5)
				out.Pix[o+3] = uint8(a*255 + 0.5)
			}
		}
	})
	return out
}

// ---- displacement map --------------------------------------------------------------------

func chanIndex(v string) int {
	switch strings.TrimSpace(v) {
	case "R":
		return 0
	case "G":
		return 1
	case "B":
		return 2
	}
	return 3
}

func (fx *fxState) displacement(p *svgNode, in, mp *Surface) *Surface {
	out := NewSurfaceRect(fx.layer.R)
	scale := fx.num(p, "scale", 0)
	sx, sy := scale*fx.pux*fx.scx, scale*fx.puy*fx.scy
	xc, yc := chanIndex(p.A["xchannelselector"]), chanIndex(p.A["ychannelselector"])
	reg := fx.region.Intersect(out.R)
	for y := reg.Y0; y < reg.Y1; y++ {
		for x := reg.X0; x < reg.X1; x++ {
			mo := (y-mp.R.Y0)*mp.Stride + (x-mp.R.X0)*4
			r, g, b, a := unpremulPx(mp.Pix[mo : mo+4])
			ch := [4]float32{r, g, b, a}
			dxv := float64(ch[xc]) * 255
			dyv := float64(ch[yc]) * 255
			dX := sx/255*dxv + 0.5 - sx/2
			dY := sy/255*dyv + 0.5 - sy/2
			srcX, srcY := x+int(dX), y+int(dY)
			if srcX < reg.X0 || srcX >= reg.X1 || srcY < reg.Y0 || srcY >= reg.Y1 {
				continue
			}
			so := (srcY-in.R.Y0)*in.Stride + (srcX-in.R.X0)*4
			oo := (y-out.R.Y0)*out.Stride + (x-out.R.X0)*4
			copy(out.Pix[oo:oo+4], in.Pix[so:so+4])
		}
	}
	return out
}

// ---- convolve matrix ---------------------------------------------------------------------

func (fx *fxState) convolve(p *svgNode, in *Surface) *Surface {
	out := NewSurfaceRect(fx.layer.R)
	ord := splitNums(p.A["order"])
	ox, oy := 3, 3
	switch len(ord) {
	case 1:
		ox, oy = int(ord[0]), int(ord[0])
	case 2:
		ox, oy = int(ord[0]), int(ord[1])
	}
	if _, ok := p.A["order"]; ok && (len(ord) == 0 || len(ord) > 2 || ox <= 0 || oy <= 0 || float64(ox) != ord[0] || (len(ord) == 2 && float64(oy) != ord[1])) {
		return out
	}
	km := splitNums(p.A["kernelmatrix"])
	if ox <= 0 || oy <= 0 || len(km) != ox*oy {
		return out
	}
	div := 0.0
	for _, v := range km {
		div += v
	}
	if v, ok := svgNumber(p.A["divisor"]); ok {
		div = v
	}
	if div == 0 {
		if _, ok := p.A["divisor"]; ok {
			return out
		}
		div = 1
	}
	bias := fx.num(p, "bias", 0)
	tx, ty := ox/2, oy/2
	if v, ok := svgNumber(p.A["targetx"]); ok {
		tx = int(v)
	}
	if v, ok := svgNumber(p.A["targety"]); ok {
		ty = int(v)
	}
	if tx < 0 || tx >= ox || ty < 0 || ty >= oy {
		return out
	}
	edge := strings.TrimSpace(p.A["edgemode"])
	preserve := strings.TrimSpace(p.A["preservealpha"]) == "true"
	reg := fx.region.Intersect(in.R)
	rw, rh := reg.W(), reg.H()
	if rw <= 0 || rh <= 0 {
		return out
	}
	// source samples as float RGBA (premultiplied, or unpremultiplied colors
	// with preserveAlpha)
	src := make([]float32, rw*rh*4)
	for y := 0; y < rh; y++ {
		for x := 0; x < rw; x++ {
			o := (y+reg.Y0-in.R.Y0)*in.Stride + (x+reg.X0-in.R.X0)*4
			px := in.Pix[o : o+4]
			d := (y*rw + x) * 4
			if preserve {
				r, g, b, a := unpremulPx(px)
				src[d], src[d+1], src[d+2], src[d+3] = r, g, b, a
			} else {
				src[d], src[d+1], src[d+2], src[d+3] = float32(px[0])/255, float32(px[1])/255, float32(px[2])/255, float32(px[3])/255
			}
		}
	}
	at := func(x, y int) (float32, float32, float32, float32, bool) {
		if x < 0 || x >= rw || y < 0 || y >= rh {
			switch edge {
			case "wrap":
				x = ((x % rw) + rw) % rw
				y = ((y % rh) + rh) % rh
			case "none":
				return 0, 0, 0, 0, false
			default:
				x = max(0, min(rw-1, x))
				y = max(0, min(rh-1, y))
			}
		}
		d := (y*rw + x) * 4
		return src[d], src[d+1], src[d+2], src[d+3], true
	}
	gain := float32(1 / div)
	fb := float32(bias)
	parallelFor(rh, 8, func(lo, hi int) {
		for y := lo; y < hi; y++ {
			for x := 0; x < rw; x++ {
				var sr, sg, sb, sa float32
				for i := 0; i < oy; i++ {
					for j := 0; j < ox; j++ {
						k := float32(km[(oy-1-i)*ox+(ox-1-j)])
						if k == 0 {
							continue
						}
						r, g, b, a, _ := at(x-tx+j, y-ty+i)
						sr += r * k
						sg += g * k
						sb += b * k
						sa += a * k
					}
				}
				o := (y+reg.Y0-out.R.Y0)*out.Stride + (x+reg.X0-out.R.X0)*4
				if preserve {
					d := (y*rw + x) * 4
					a := src[d+3]
					putPx(out.Pix[o:o+4], clampf(sr*gain+fb), clampf(sg*gain+fb), clampf(sb*gain+fb), a)
				} else {
					a := clampf(sa*gain + fb)
					r := min(clampf(sr*gain+fb), a)
					g := min(clampf(sg*gain+fb), a)
					b := min(clampf(sb*gain+fb), a)
					out.Pix[o] = uint8(r*255 + 0.5)
					out.Pix[o+1] = uint8(g*255 + 0.5)
					out.Pix[o+2] = uint8(b*255 + 0.5)
					out.Pix[o+3] = uint8(a*255 + 0.5)
				}
			}
		}
	})
	return out
}

// ---- tile / image ---------------------------------------------------------------------------

func (fx *fxState) tile(in *Surface, t IRect, sub IRect) *Surface {
	out := NewSurfaceRect(fx.layer.R)
	t = t.Intersect(in.R)
	if t.Empty() {
		return out
	}
	tw, th := t.W(), t.H()
	sub = sub.Intersect(out.R)
	for y := sub.Y0; y < sub.Y1; y++ {
		sy := t.Y0 + ((y-t.Y0)%th+th)%th
		for x := sub.X0; x < sub.X1; x++ {
			sx := t.X0 + ((x-t.X0)%tw+tw)%tw
			so := (sy-in.R.Y0)*in.Stride + (sx-in.R.X0)*4
			oo := (y-out.R.Y0)*out.Stride + (x-out.R.X0)*4
			copy(out.Pix[oo:oo+4], in.Pix[so:so+4])
		}
	}
	return out
}

func (fx *fxState) image(p *svgNode, lin bool) *Surface {
	out := NewSurfaceRect(fx.layer.R)
	r := fx.r
	_, su := fx.subregion(p, fx.region)
	tc := NewContextSurface(out)
	tc.SetSmoothing(true, QualityLow)
	tc.SetTransform(fx.m)
	sr := r.sub(tc)
	href := strings.TrimSpace(p.A["href"])
	if strings.HasPrefix(href, "#") {
		if t := r.doc.ByID[strings.TrimPrefix(href, "#")]; t != nil && !r.active[t] {
			sr.active = map[*svgNode]bool{t: true}
			tst := r.inherit(r.root, t.A)
			tm := fx.m
			if _, ok := p.A["x"]; ok {
				tm = tm.Mul(Translate(su.X, 0))
			}
			if _, ok := p.A["y"]; ok {
				tm = tm.Mul(Translate(0, su.Y))
			}
			sr.drawNode(t, tst, tm, false)
		}
	} else if pic := sr.picture(p); pic != nil {
		sr.drawPic(pic, su.X, su.Y, su.W, su.H, p.A["preserveaspectratio"], "")
	}
	if lin {
		convertSurface(out, &s2lin)
	}
	return out
}

// ---- lighting --------------------------------------------------------------------------------

func (fx *fxState) lighting(p *svgNode, in *Surface, sub IRect, lin bool) *Surface {
	out := NewSurfaceRect(fx.layer.R)
	specular := p.Tag == "fespecularlighting"
	ss := fx.num(p, "surfacescale", 1)
	kd := fx.num(p, "diffuseconstant", 1)
	ks := fx.num(p, "specularconstant", 1)
	se := fx.num(p, "specularexponent", 1)
	if specular && (se < 1 || se > 128) || kd < 0 || ks < 0 {
		return out
	}
	col := fx.color(p, "lighting-color", RGBA8(255, 255, 255, 255), lin)
	var ls *svgNode
	for _, k := range p.Kids {
		switch k.Tag {
		case "fedistantlight", "fepointlight", "fespotlight":
			if ls == nil {
				ls = k
			}
		}
	}
	if ls == nil {
		return out
	}
	sub = sub.Intersect(in.R)
	if sub.Empty() {
		return out
	}
	sw, sh := sub.W(), sub.H()
	// alpha plane of the input within the subregion
	alpha := make([]float32, sw*sh)
	for y := 0; y < sh; y++ {
		for x := 0; x < sw; x++ {
			alpha[y*sw+x] = float32(in.Pix[(y+sub.Y0-in.R.Y0)*in.Stride+(x+sub.X0-in.R.X0)*4+3]) / 255
		}
	}
	A := func(x, y int) float64 { return float64(alpha[y*sw+x]) }
	// light geometry in device space
	num := func(k string) float64 { return fx.num(ls, k, 0) }
	pu := func(x, y float64) (float64, float64) {
		if fx.f.primObb && fx.f.hasBB {
			x, y = fx.f.bb.X+x*fx.f.bb.W, fx.f.bb.Y+y*fx.f.bb.H
		}
		return fx.m.Apply(x, y)
	}
	zs := (fx.scx + fx.scy) / 2
	if fx.f.primObb && fx.f.hasBB {
		zs *= math.Sqrt((fx.f.bb.W*fx.f.bb.W + fx.f.bb.H*fx.f.bb.H) / 2)
	}
	var lx, ly, lz, px, py, pz, cone, spotExp float64
	var dir [3]float64
	distant := ls.Tag == "fedistantlight"
	spot := ls.Tag == "fespotlight"
	if distant {
		az, el := num("azimuth")*math.Pi/180, num("elevation")*math.Pi/180
		dir = [3]float64{math.Cos(az) * math.Cos(el), math.Sin(az) * math.Cos(el), math.Sin(el)}
	} else {
		lx, ly = pu(num("x"), num("y"))
		lz = num("z") * zs
	}
	var sdir [3]float64
	hasCone := false
	if spot {
		px, py = pu(num("pointsatx"), num("pointsaty"))
		pz = num("pointsatz") * zs
		sdir = [3]float64{px - lx, py - ly, pz - lz}
		if l := math.Sqrt(sdir[0]*sdir[0] + sdir[1]*sdir[1] + sdir[2]*sdir[2]); l > 0 {
			sdir[0], sdir[1], sdir[2] = sdir[0]/l, sdir[1]/l, sdir[2]/l
		}
		spotExp = fx.num(ls, "specularexponent", 1)
		if v, ok := svgNumber(ls.A["limitingconeangle"]); ok {
			cone = math.Cos(math.Abs(v) * math.Pi / 180)
			hasCone = true
		}
	}
	// the surface normal comes from the Sobel kernels of the spec (with the
	// edge variants); rows / columns missing at the border are dropped
	wts := [3]float64{1, 2, 1}
	normal := func(x, y int) (float64, float64) {
		xl, xr := max(x-1, 0), min(x+1, sw-1)
		yl, yr := max(y-1, 0), min(y+1, sh-1)
		var nx, ny, wsum float64
		for dy := -1; dy <= 1; dy++ {
			yy := y + dy
			if yy < 0 || yy >= sh {
				continue
			}
			w := wts[dy+1]
			wsum += w
			nx += w * (A(xr, yy) - A(xl, yy))
		}
		dxs := float64(xr - xl)
		if dxs > 0 {
			nx = nx / (dxs * wsum / 2)
		} else {
			nx = 0
		}
		wsum = 0
		for dx := -1; dx <= 1; dx++ {
			xx := x + dx
			if xx < 0 || xx >= sw {
				continue
			}
			w := wts[dx+1]
			wsum += w
			ny += w * (A(xx, yr) - A(xx, yl))
		}
		dys := float64(yr - yl)
		if dys > 0 {
			ny = ny / (dys * wsum / 2)
		} else {
			ny = 0
		}
		return -ss * nx, -ss * ny
	}
	parallelFor(sh, 8, func(lo, hi int) {
		for y := lo; y < hi; y++ {
			for x := 0; x < sw; x++ {
				nx, ny := normal(x, y)
				nl := math.Sqrt(nx*nx + ny*ny + 1)
				n := [3]float64{nx / nl, ny / nl, 1 / nl}
				var L [3]float64
				cr, cg, cb := col.R, col.G, col.B
				if distant {
					L = dir
				} else {
					z := ss * A(x, y)
					dx, dy, dz := lx-float64(x+sub.X0), ly-float64(y+sub.Y0), lz-z
					l := math.Sqrt(dx*dx + dy*dy + dz*dz)
					if l == 0 {
						l = 1
					}
					L = [3]float64{dx / l, dy / l, dz / l}
					if spot {
						ms := -(L[0]*sdir[0] + L[1]*sdir[1] + L[2]*sdir[2])
						f := 0.0
						if ms > 0 && (!hasCone || ms >= cone) {
							f = math.Pow(ms, spotExp)
							if hasCone {
								// smooth the cone edge like Skia (about one degree)
								if t := (ms - cone) / 0.016; t < 1 {
									f *= t
								}
							}
						}
						cr, cg, cb = cr*f, cg*f, cb*f
					}
				}
				o := (y+sub.Y0-out.R.Y0)*out.Stride + (x+sub.X0-out.R.X0)*4
				if !specular {
					ndl := n[0]*L[0] + n[1]*L[1] + n[2]*L[2]
					f := math.Max(0, kd*ndl)
					out.Pix[o] = clampU8(math.Min(1, cr*f) * 255)
					out.Pix[o+1] = clampU8(math.Min(1, cg*f) * 255)
					out.Pix[o+2] = clampU8(math.Min(1, cb*f) * 255)
					out.Pix[o+3] = 255
				} else {
					hx, hy, hz := L[0], L[1], L[2]+1
					hl := math.Sqrt(hx*hx + hy*hy + hz*hz)
					f := 0.0
					if hl > 0 {
						nh := (n[0]*hx + n[1]*hy + n[2]*hz) / hl
						if nh > 0 {
							f = ks * math.Pow(nh, se)
						}
					}
					r, g, b := math.Min(1, cr*f), math.Min(1, cg*f), math.Min(1, cb*f)
					a := math.Max(r, math.Max(g, b))
					out.Pix[o] = clampU8(r * 255)
					out.Pix[o+1] = clampU8(g * 255)
					out.Pix[o+2] = clampU8(b * 255)
					out.Pix[o+3] = clampU8(a * 255)
				}
			}
		}
	})
	return out
}

// drawFilteredLocal draws n with its SVG filter evaluated in the element's
// own (axis-aligned, scaled) space: blur directions, offsets, morphology
// radii and displacement follow the element's rotation. The filtered layer is
// then mapped to the device with the rotation part of m.
func (r *svgR) drawFilteredLocal(n *svgNode, st sStyle, m Matrix, filterS, maskS string, opacity float64, blend string, isRoot bool) bool {
	sx, sy := math.Hypot(m[0], m[1]), math.Hypot(m[2], m[3])
	if sx <= 1e-9 || sy <= 1e-9 {
		return false
	}
	S := Scale(sx, sy)
	spec := r.prepareFilter(n, &st, filterS, S)
	if spec == nil || spec.region.Empty() {
		return false
	}
	reg := spec.region
	w, h := reg.W(), reg.H()
	if w <= 0 || h <= 0 || w*h > 36_000_000 {
		return false
	}
	ls := Translate(-float64(reg.X0), -float64(reg.Y0)).Mul(S)
	spec = r.prepareFilter(n, &st, filterS, ls)
	if spec == nil || spec.region.Empty() {
		return false
	}
	surf := NewSurface(w, h)
	tc := NewContextSurface(surf)
	tc.SetSmoothing(true, QualityLow)
	tc.SetTransform(ls)
	sr := r.sub(tc)
	sr.drawContent(n, st, ls, isRoot)
	sr.runFilterAt(spec, n, &st, ls, 0, 0)
	if maskS != "" {
		if id, ok := urlRef(maskS); ok {
			if mn := r.doc.ByID[id]; mn != nil && mn.Tag == "mask" {
				sr.applyMask(mn, n, &st, ls)
			}
		}
	}
	r.nodes = sr.nodes
	inv, ok := S.Invert()
	if !ok {
		return false
	}
	// layer pixel -> device: undo the layer origin and the scale, apply m
	P := m.Mul(inv).Mul(Translate(float64(reg.X0), float64(reg.Y0)))
	op := SourceOver
	if blend != "" {
		if o, ok := ParseOp(blend); ok {
			op = o
		}
	}
	c := r.c
	c.Save()
	c.SetTransform(P)
	c.SetGlobalAlpha(opacity)
	c.SetOp(op)
	c.SetSmoothing(true, QualityLow)
	c.DrawImage(surf, 0, 0, float64(w), float64(h), 0, 0, float64(w), float64(h))
	c.Restore()
	return true
}
