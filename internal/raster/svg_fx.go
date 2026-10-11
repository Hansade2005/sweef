package raster

import (
	"math"
	"strings"
)

// ---- clip-path -------------------------------------------------------------------------

// clipChildMask accumulates the coverage of clipPath child n into acc (max).
func (r *svgR) clipChildMask(n *svgNode, st sStyle, m Matrix, bounds IRect, acc **Mask, depth int) {
	if depth > 8 || n.Tag == "#text" || n.A["display"] == "none" {
		return
	}
	st = r.inherit(st, n.A)
	m = m.Mul(r.nodeTransform(n, &st))
	if !st.visible {
		return
	}
	var p *Path
	switch n.Tag {
	case "rect", "circle", "ellipse", "line", "polyline", "polygon", "path":
		p = r.shapePath(n, &st)
	case "use":
		id := strings.TrimPrefix(strings.TrimSpace(n.A["href"]), "#")
		t := r.doc.ByID[id]
		if t == nil {
			return
		}
		x, y := r.attrLen(n, "x", 'x', &st, 0), r.attrLen(n, "y", 'y', &st, 0)
		r.clipChildMask(t, st, m.Mul(Translate(x, y)), bounds, acc, depth+1)
		return
	case "text":
		return
	}
	if p == nil {
		return
	}
	mk := FillMask(p, m, st.clipRule, bounds)
	if mk == nil {
		return
	}
	if cp := strings.TrimSpace(n.A["clip-path"]); cp != "" && cp != "none" {
		if id, ok := urlRef(cp); ok {
			if cn := r.doc.ByID[id]; cn != nil && cn.Tag == "clippath" && !r.active[cn] {
				if cm := r.clipMask(cn, n, &st, m, bounds); cm != nil {
					mk = maskMul(mk, cm)
				} else {
					return
				}
			}
		}
	}
	if *acc == nil {
		*acc = mk
	} else {
		*acc = maskMax(*acc, mk)
	}
}

func maskMax(a, b *Mask) *Mask {
	r := a.B.Union(b.B)
	out := newMask(r)
	for _, src := range []*Mask{a, b} {
		for y := src.B.Y0; y < src.B.Y1; y++ {
			sr := src.Row(y)
			dr := out.Row(y)[src.B.X0-r.X0:]
			for i, v := range sr {
				if v > dr[i] {
					dr[i] = v
				}
			}
		}
	}
	return out
}

func (r *svgR) clipMask(cn, target *svgNode, st *sStyle, m Matrix, bounds IRect) *Mask {
	r.active[cn] = true
	defer delete(r.active, cn)
	cm := m
	if cn.A["clippathunits"] == "objectBoundingBox" {
		bb, ok := r.bbox(target, st)
		if !ok || bb.W <= 0 || bb.H <= 0 {
			return nil
		}
		cm = cm.Mul(Matrix{bb.W, 0, 0, bb.H, bb.X, bb.Y})
	}
	if t := cn.A["transform"]; t != "" {
		cm = cm.Mul(svgTransform(t))
	}
	cst := r.inherit(r.root, cn.A)
	var acc *Mask
	for _, k := range cn.Kids {
		r.clipChildMask(k, cst, cm, bounds, &acc, 0)
	}
	return acc
}

// applyClip intersects the current clip with clipPath cn; false = nothing visible.
func (r *svgR) applyClip(cn, target *svgNode, st *sStyle, m Matrix) bool {
	if r.active[cn] {
		return true
	}
	mk := r.clipMask(cn, target, st, m, r.c.ClipBounds())
	if mk == nil {
		r.c.clipWithMask(nil)
		return false
	}
	if cp := strings.TrimSpace(cn.A["clip-path"]); cp != "" {
		if id, ok := urlRef(cp); ok {
			if c2 := r.doc.ByID[id]; c2 != nil && c2.Tag == "clippath" && c2 != cn {
				return r.applyClipMask(mk, c2, target, st, m)
			}
		}
	}
	r.c.clipWithMask(mk)
	return true
}

func (r *svgR) applyClipMask(mk *Mask, c2, target *svgNode, st *sStyle, m Matrix) bool {
	if m2 := r.clipMask(c2, target, st, m, r.c.ClipBounds()); m2 != nil {
		r.c.clipWithMask(maskMul(mk, m2))
		return true
	}
	r.c.clipWithMask(nil)
	return false
}

// ---- mask --------------------------------------------------------------------------------

func (r *svgR) applyMask(mn, target *svgNode, st *sStyle, m Matrix) {
	if r.active[mn] {
		return
	}
	r.active[mn] = true
	defer delete(r.active, mn)
	c := r.c
	layer := c.surf
	bb, hasBB := r.bbox(target, st)
	units := mn.A["maskunits"]
	obb := units != "userSpaceOnUse"
	if obb && (!hasBB || bb.W <= 0 || bb.H <= 0) {
		layer.Clear()
		return
	}
	dim := func(k string, dflt float64, axis byte) float64 {
		v, ok := mn.A[k]
		if !ok {
			if obb {
				return dflt
			}
			return dflt * r.refFor(axis)
		}
		if obb {
			v = strings.TrimSpace(v)
			if strings.HasSuffix(v, "%") {
				f, _ := svgNumber(v[:len(v)-1])
				return f / 100
			}
			f, _ := svgNumber(v)
			return f
		}
		return r.length(v, r.refFor(axis), st.fontSize, dflt*r.refFor(axis))
	}
	x, y, w, h := dim("x", -0.1, 'x'), dim("y", -0.1, 'y'), dim("width", 1.2, 'x'), dim("height", 1.2, 'y')
	if obb {
		x, y, w, h = bb.X+x*bb.W, bb.Y+y*bb.H, w*bb.W, h*bb.H
	}
	tmp := NewSurfaceRect(layer.R)
	tc := NewContextSurface(tmp)
	tc.SetSmoothing(true, QualityLow)
	tc.SetTransform(m)
	tc.ClipRect(x, y, w, h)
	sr := r.sub(tc)
	cm := m
	if mn.A["maskcontentunits"] == "objectBoundingBox" && hasBB {
		cm = cm.Mul(Matrix{bb.W, 0, 0, bb.H, bb.X, bb.Y})
	}
	mst := r.inherit(r.root, mn.A)
	sr.drawChildren(mn, mst, cm)
	alphaType := mn.A["mask-type"] == "alpha"
	w4 := layer.R.W()
	for yy := 0; yy < layer.R.H(); yy++ {
		lp := layer.Pix[yy*layer.Stride : yy*layer.Stride+w4*4]
		mp := tmp.Pix[yy*tmp.Stride : yy*tmp.Stride+w4*4]
		for xx := 0; xx < w4; xx++ {
			var k float64
			if alphaType {
				k = float64(mp[xx*4+3])
			} else {
				k = 0.2126*float64(mp[xx*4]) + 0.7152*float64(mp[xx*4+1]) + 0.0722*float64(mp[xx*4+2])
			}
			f := uint32(k + 0.5)
			if f > 255 {
				f = 255
			}
			if f == 255 {
				continue
			}
			for ch := 0; ch < 4; ch++ {
				lp[xx*4+ch] = uint8(mul255(uint32(lp[xx*4+ch]), f))
			}
		}
	}
}

// ---- filter ------------------------------------------------------------------------------

type svgFilterSpec struct {
	node    *svgNode
	prims   []*svgNode
	region  IRect
	user    Rect
	primObb bool
	bb      Rect
	hasBB   bool
	linear  bool
}

func (r *svgR) prepareFilter(n *svgNode, st *sStyle, f string, m Matrix) *svgFilterSpec {
	id, ok := urlRef(f)
	if !ok {
		return nil
	}
	fn := r.doc.ByID[id]
	if fn == nil || fn.Tag != "filter" {
		return nil
	}
	spec := &svgFilterSpec{node: fn}
	// primitives (follow href if the filter has none)
	src := fn
	for i := 0; i < 8 && src != nil; i++ {
		for _, k := range src.Kids {
			if strings.HasPrefix(k.Tag, "fe") {
				spec.prims = append(spec.prims, k)
			}
		}
		if len(spec.prims) > 0 {
			break
		}
		src = r.doc.ByID[strings.TrimPrefix(strings.TrimSpace(src.A["href"]), "#")]
	}
	spec.bb, spec.hasBB = r.bbox(n, st)
	fa := func(k string) (string, bool) { return r.chainAttr(fn, k) }
	units, _ := fa("filterunits")
	obb := units != "userSpaceOnUse"
	pu, _ := fa("primitiveunits")
	spec.primObb = pu == "objectBoundingBox"
	if obb && (!spec.hasBB || spec.bb.W <= 0 || spec.bb.H <= 0) {
		return &svgFilterSpec{node: fn, region: IRect{}}
	}
	dim := func(k string, dflt float64, axis byte) float64 {
		v, ok := fa(k)
		if !ok {
			if obb {
				return dflt
			}
			return dflt * r.refFor(axis)
		}
		if obb {
			v = strings.TrimSpace(v)
			if strings.HasSuffix(v, "%") {
				f, _ := svgNumber(v[:len(v)-1])
				return f / 100
			}
			f, _ := svgNumber(v)
			return f
		}
		return r.length(v, r.refFor(axis), st.fontSize, dflt*r.refFor(axis))
	}
	x, y, w, h := dim("x", -0.1, 'x'), dim("y", -0.1, 'y'), dim("width", 1.2, 'x'), dim("height", 1.2, 'y')
	if obb {
		x, y, w, h = spec.bb.X+x*spec.bb.W, spec.bb.Y+y*spec.bb.H, w*spec.bb.W, h*spec.bb.H
	}
	spec.user = Rect{x, y, w, h}
	dr, _ := transformRect(spec.user, m)
	spec.region = IRect{int(math.Floor(dr.X + 1e-6)), int(math.Floor(dr.Y + 1e-6)), int(math.Ceil(dr.X + dr.W - 1e-6)), int(math.Ceil(dr.Y + dr.H - 1e-6))}
	spec.linear = st.cif != "srgb"
	if v := fn.A["color-interpolation-filters"]; v != "" {
		spec.linear = strings.ToLower(v) != "srgb"
	}
	return spec
}

var lin2s, s2lin [256]uint8

func init() {
	for i := 0; i < 256; i++ {
		f := float64(i) / 255
		s2lin[i] = clampU8(srgbToLinear(f) * 255)
		lin2s[i] = clampU8(linearToSRGB(f) * 255)
	}
}

func convertSurface(s *Surface, lut *[256]uint8) {
	for i := 0; i+3 < len(s.Pix); i += 4 {
		a := uint32(s.Pix[i+3])
		if a == 0 {
			continue
		}
		for k := 0; k < 3; k++ {
			v := uint32(s.Pix[i+k])
			if a != 255 {
				v = (v*255 + a/2) / a
				if v > 255 {
					v = 255
				}
			}
			v = uint32(lut[v])
			s.Pix[i+k] = uint8(mul255(v, a))
		}
	}
}

func cloneSurface(s *Surface) *Surface { return s.Clone() }

func clearOutside(s *Surface, keep IRect) {
	keep = keep.Intersect(s.R)
	w := s.R.W()
	for y := s.R.Y0; y < s.R.Y1; y++ {
		row := s.Pix[(y-s.R.Y0)*s.Stride : (y-s.R.Y0)*s.Stride+w*4]
		if y < keep.Y0 || y >= keep.Y1 || keep.Empty() {
			clear(row)
			continue
		}
		for x := s.R.X0; x < keep.X0; x++ {
			clear(row[(x-s.R.X0)*4 : (x-s.R.X0)*4+4])
		}
		for x := keep.X1; x < s.R.X1; x++ {
			clear(row[(x-s.R.X0)*4 : (x-s.R.X0)*4+4])
		}
	}
}

func (r *svgR) runFilter(f *svgFilterSpec, n *svgNode, st *sStyle, m Matrix) {
	c := r.c
	layer := c.surf
	if f.region.Empty() || len(f.prims) == 0 {
		layer.Clear()
		return
	}
	src := cloneSurface(layer)
	clearOutside(src, f.region)
	if f.linear {
		convertSurface(src, &s2lin)
	}
	results := map[string]*Surface{}
	var last *Surface
	scx, scy := math.Hypot(m[0], m[1]), math.Hypot(m[2], m[3])
	region := f.region
	getIn := func(p *svgNode, key string) *Surface {
		name := strings.TrimSpace(p.A[key])
		switch name {
		case "SourceGraphic":
			return src
		case "SourceAlpha":
			s := cloneSurface(src)
			for i := 0; i+3 < len(s.Pix); i += 4 {
				s.Pix[i], s.Pix[i+1], s.Pix[i+2] = 0, 0, 0
			}
			return s
		case "BackgroundImage", "BackgroundAlpha", "FillPaint", "StrokePaint":
			return NewSurfaceRect(layer.R)
		case "":
			if last != nil {
				return last
			}
			return src
		}
		if s, ok := results[name]; ok {
			return s
		}
		if last != nil {
			return last
		}
		return src
	}
	num := func(p *svgNode, k string, d float64) float64 {
		if v, ok := svgNumber(p.A[k]); ok {
			return v
		}
		return d
	}
	pairs := func(p *svgNode, k string, d float64) (float64, float64) {
		v := splitNums(p.A[k])
		switch len(v) {
		case 0:
			return d, d
		case 1:
			return v[0], v[0]
		}
		return v[0], v[1]
	}
	// primitiveUnits=objectBoundingBox scales numbers by the bbox size
	pux, puy := 1.0, 1.0
	if f.primObb && f.hasBB {
		pux, puy = f.bb.W, f.bb.H
	}
	flood := func(p *svgNode) Color {
		col := RGBA8(0, 0, 0, 255)
		if v, ok := p.A["flood-color"]; ok {
			if cc, ok := ParseColor(v, st.color); ok {
				col = cc
			}
		}
		if v, ok := p.A["flood-opacity"]; ok {
			col.A *= parseOpacity(v, 1)
		}
		if f.linear {
			col.R, col.G, col.B = srgbToLinear(col.R), srgbToLinear(col.G), srgbToLinear(col.B)
		}
		return col
	}
	for _, p := range f.prims {
		var out *Surface
		switch p.Tag {
		case "fegaussianblur":
			sx, sy := pairs(p, "stddeviation", 0)
			in := getIn(p, "in")
			out = cloneSurface(in)
			if sx < 0 || sy < 0 {
				out.Clear()
			} else if sx > 0 || sy > 0 {
				blurPlane(out.Pix, out.R.W(), out.R.H(), out.Stride, 4, sx*pux*scx, sy*puy*scy)
			}
		case "feoffset":
			in := getIn(p, "in")
			dx, dy := num(p, "dx", 0)*pux*scx, num(p, "dy", 0)*puy*scy
			out = offsetSurface(in, dx, dy)
		case "fedropshadow":
			in := getIn(p, "in")
			sx, sy := pairs(p, "stddeviation", 2)
			dx, dy := num(p, "dx", 2)*pux*scx, num(p, "dy", 2)*puy*scy
			sh := cloneSurface(in)
			col := flood(p)
			if _, ok := p.A["flood-color"]; !ok {
				col = RGBA8(0, 0, 0, 255)
				if f.linear {
					col = RGBA8(0, 0, 0, 255)
				}
				if v, ok := p.A["flood-opacity"]; ok {
					col.A *= parseOpacity(v, 1)
				}
			}
			tintAlpha(sh, col)
			if sx > 0 || sy > 0 {
				blurPlane(sh.Pix, sh.R.W(), sh.R.H(), sh.Stride, 4, sx*pux*scx, sy*puy*scy)
			}
			sh = offsetSurface(sh, dx, dy)
			composite(sh, nil, surfShader{in, 256}, SourceOver, &clipState{R: sh.R})
			out = sh
		case "feflood":
			out = NewSurfaceRect(layer.R)
			col := flood(p)
			composite(out, nil, Solid(col), SourceOver, &clipState{R: region.Intersect(out.R)})
		case "fecolormatrix":
			out = cloneSurface(getIn(p, "in"))
			colorMatrixPrim(out, p)
		case "feblend":
			in, in2 := getIn(p, "in"), getIn(p, "in2")
			out = cloneSurface(in2)
			op := SourceOver
			if o, ok := ParseOp(p.A["mode"]); ok && p.A["mode"] != "" {
				op = o
			}
			composite(out, nil, surfShader{in, 256}, op, &clipState{R: out.R})
		case "femerge":
			out = NewSurfaceRect(layer.R)
			for _, k := range p.Kids {
				if k.Tag == "femergenode" {
					composite(out, nil, surfShader{getIn(k, "in"), 256}, SourceOver, &clipState{R: out.R})
				}
			}
		case "fecomposite":
			in, in2 := getIn(p, "in"), getIn(p, "in2")
			op := p.A["operator"]
			if op == "arithmetic" {
				out = arithmetic(in, in2, num(p, "k1", 0), num(p, "k2", 0), num(p, "k3", 0), num(p, "k4", 0))
			} else {
				out = cloneSurface(in2)
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
			}
		case "fecomponenttransfer":
			out = cloneSurface(getIn(p, "in"))
			componentTransfer(out, p)
		case "femorphology":
			rx, ry := pairs(p, "radius", 0)
			out = morphology(getIn(p, "in"), int(math.Round(rx*pux*scx)), int(math.Round(ry*puy*scy)), p.A["operator"] == "dilate")
		case "feimage", "feturbulence", "fetile", "fediffuselighting", "fespecularlighting", "fedisplacementmap", "feconvolvematrix":
			out = cloneSurface(getIn(p, "in"))
			if p.Tag != "fetile" && p.Tag != "feconvolvematrix" && p.Tag != "fedisplacementmap" {
				out = NewSurfaceRect(layer.R)
			}
		default:
			continue
		}
		// primitive subregion
		sub := region
		hasSub := false
		for _, k := range []string{"x", "y", "width", "height"} {
			if _, ok := p.A[k]; ok {
				hasSub = true
			}
		}
		if hasSub {
			u := f.user
			get := func(k string, d float64, axis byte) float64 {
				v, ok := p.A[k]
				if !ok {
					return d
				}
				if f.primObb && f.hasBB {
					vv := strings.TrimSpace(v)
					if strings.HasSuffix(vv, "%") {
						fv, _ := svgNumber(vv[:len(vv)-1])
						fv /= 100
						if axis == 'x' {
							return fv
						}
						return fv
					}
					fv, _ := svgNumber(vv)
					return fv
				}
				return r.length(v, r.refFor(axis), st.fontSize, d)
			}
			x, y, w, h := get("x", u.X, 'x'), get("y", u.Y, 'y'), get("width", u.W, 'x'), get("height", u.H, 'y')
			if f.primObb && f.hasBB {
				if _, ok := p.A["x"]; ok {
					x = f.bb.X + x*f.bb.W
				}
				if _, ok := p.A["y"]; ok {
					y = f.bb.Y + y*f.bb.H
				}
				if _, ok := p.A["width"]; ok {
					w *= f.bb.W
				} else {
					w = u.W
				}
				if _, ok := p.A["height"]; ok {
					h *= f.bb.H
				} else {
					h = u.H
				}
			}
			dr, _ := transformRect(Rect{x, y, w, h}, m)
			sub = IRect{int(math.Floor(dr.X + 1e-6)), int(math.Floor(dr.Y + 1e-6)), int(math.Ceil(dr.X + dr.W - 1e-6)), int(math.Ceil(dr.Y + dr.H - 1e-6))}.Intersect(region)
		}
		clearOutside(out, sub)
		if name := p.A["result"]; name != "" {
			results[name] = out
		}
		last = out
	}
	if last == nil {
		layer.Clear()
		return
	}
	res := last
	if res == src {
		res = cloneSurface(src)
	}
	if f.linear {
		convertSurface(res, &lin2s)
	}
	clearOutside(res, region)
	copy(layer.Pix, res.Pix)
}

func tintAlpha(s *Surface, col Color) {
	cr, cg, cb := col.R, col.G, col.B
	for i := 0; i+3 < len(s.Pix); i += 4 {
		a := float64(s.Pix[i+3]) / 255 * col.A
		s.Pix[i] = clampU8(cr * a * 255)
		s.Pix[i+1] = clampU8(cg * a * 255)
		s.Pix[i+2] = clampU8(cb * a * 255)
		s.Pix[i+3] = clampU8(a * 255)
	}
}

func offsetSurface(in *Surface, dx, dy float64) *Surface {
	out := NewSurfaceRect(in.R)
	ix, iy := int(math.Round(dx)), int(math.Round(dy))
	w, h := in.R.W(), in.R.H()
	for y := 0; y < h; y++ {
		ty := y - iy
		if ty < 0 || ty >= h {
			continue
		}
		for x := 0; x < w; x++ {
			tx := x - ix
			if tx < 0 || tx >= w {
				continue
			}
			copy(out.Pix[y*out.Stride+x*4:y*out.Stride+x*4+4], in.Pix[ty*in.Stride+tx*4:ty*in.Stride+tx*4+4])
		}
	}
	return out
}

func unpremulPx(p []uint8) (r, g, b, a float32) {
	a = float32(p[3]) / 255
	if p[3] == 0 {
		return 0, 0, 0, 0
	}
	return float32(p[0]) / 255 / a, float32(p[1]) / 255 / a, float32(p[2]) / 255 / a, a
}

func putPx(p []uint8, r, g, b, a float32) {
	a = clampf(a)
	p[3] = uint8(a*255 + 0.5)
	p[0] = uint8(clampf(r)*a*255 + 0.5)
	p[1] = uint8(clampf(g)*a*255 + 0.5)
	p[2] = uint8(clampf(b)*a*255 + 0.5)
}

func colorMatrixPrim(s *Surface, p *svgNode) {
	typ := p.A["type"]
	vals := splitNums(p.A["values"])
	var mx [20]float64
	mx = [20]float64{1, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 1, 0}
	switch typ {
	case "saturate":
		v := 1.0
		if len(vals) > 0 {
			v = vals[0]
		}
		mx = [20]float64{
			0.213 + 0.787*v, 0.715 - 0.715*v, 0.072 - 0.072*v, 0, 0,
			0.213 - 0.213*v, 0.715 + 0.285*v, 0.072 - 0.072*v, 0, 0,
			0.213 - 0.213*v, 0.715 - 0.715*v, 0.072 + 0.928*v, 0, 0,
			0, 0, 0, 1, 0}
	case "huerotate":
		a := 0.0
		if len(vals) > 0 {
			a = vals[0] * math.Pi / 180
		}
		cs, sn := math.Cos(a), math.Sin(a)
		mx = [20]float64{
			0.213 + cs*0.787 - sn*0.213, 0.715 - cs*0.715 - sn*0.715, 0.072 - cs*0.072 + sn*0.928, 0, 0,
			0.213 - cs*0.213 + sn*0.143, 0.715 + cs*0.285 + sn*0.140, 0.072 - cs*0.072 - sn*0.283, 0, 0,
			0.213 - cs*0.213 - sn*0.787, 0.715 - cs*0.715 + sn*0.715, 0.072 + cs*0.928 + sn*0.072, 0, 0,
			0, 0, 0, 1, 0}
	case "luminancetoalpha":
		mx = [20]float64{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0.2125, 0.7154, 0.0721, 0, 0}
	default:
		if len(vals) == 20 {
			copy(mx[:], vals)
		}
	}
	for i := 0; i+3 < len(s.Pix); i += 4 {
		r, g, b, a := unpremulPx(s.Pix[i : i+4])
		R, G, B, A := float64(r), float64(g), float64(b), float64(a)
		nr := mx[0]*R + mx[1]*G + mx[2]*B + mx[3]*A + mx[4]
		ng := mx[5]*R + mx[6]*G + mx[7]*B + mx[8]*A + mx[9]
		nb := mx[10]*R + mx[11]*G + mx[12]*B + mx[13]*A + mx[14]
		na := mx[15]*R + mx[16]*G + mx[17]*B + mx[18]*A + mx[19]
		putPx(s.Pix[i:i+4], float32(nr), float32(ng), float32(nb), float32(na))
	}
}

func arithmetic(a, b *Surface, k1, k2, k3, k4 float64) *Surface {
	out := NewSurfaceRect(a.R)
	for i := 0; i+3 < len(out.Pix); i += 4 {
		var v [4]float64
		for k := 0; k < 4; k++ {
			x, y := float64(a.Pix[i+k])/255, float64(b.Pix[i+k])/255
			v[k] = math.Max(0, math.Min(1, k1*x*y+k2*x+k3*y+k4))
		}
		// keep premultiplied invariant
		for k := 0; k < 3; k++ {
			if v[k] > v[3] {
				v[k] = v[3]
			}
		}
		for k := 0; k < 4; k++ {
			out.Pix[i+k] = uint8(v[k]*255 + 0.5)
		}
	}
	return out
}

func componentTransfer(s *Surface, p *svgNode) {
	var fn [4]func(float64) float64
	for _, k := range p.Kids {
		idx := -1
		switch k.Tag {
		case "fefuncr":
			idx = 0
		case "fefuncg":
			idx = 1
		case "fefuncb":
			idx = 2
		case "fefunca":
			idx = 3
		}
		if idx < 0 {
			continue
		}
		tv := splitNums(k.A["tablevalues"])
		get := func(key string, d float64) float64 {
			if v, ok := svgNumber(k.A[key]); ok {
				return v
			}
			return d
		}
		switch k.A["type"] {
		case "table":
			if len(tv) > 0 {
				fn[idx] = func(c float64) float64 {
					n := len(tv) - 1
					if n == 0 {
						return tv[0]
					}
					pos := c * float64(n)
					i := int(math.Floor(pos))
					if i >= n {
						return tv[n]
					}
					return tv[i] + (pos-float64(i))*(tv[i+1]-tv[i])
				}
			}
		case "discrete":
			if len(tv) > 0 {
				fn[idx] = func(c float64) float64 {
					i := int(math.Floor(c * float64(len(tv))))
					if i >= len(tv) {
						i = len(tv) - 1
					}
					return tv[i]
				}
			}
		case "linear":
			sl, ic := get("slope", 1), get("intercept", 0)
			fn[idx] = func(c float64) float64 { return sl*c + ic }
		case "gamma":
			am, ex, of := get("amplitude", 1), get("exponent", 1), get("offset", 0)
			fn[idx] = func(c float64) float64 { return am*math.Pow(c, ex) + of }
		}
	}
	for i := 0; i+3 < len(s.Pix); i += 4 {
		r, g, b, a := unpremulPx(s.Pix[i : i+4])
		v := [4]float64{float64(r), float64(g), float64(b), float64(a)}
		for k := 0; k < 4; k++ {
			if fn[k] != nil {
				v[k] = math.Max(0, math.Min(1, fn[k](v[k])))
			}
		}
		putPx(s.Pix[i:i+4], float32(v[0]), float32(v[1]), float32(v[2]), float32(v[3]))
	}
}

func morphology(in *Surface, rx, ry int, dilate bool) *Surface {
	if rx < 0 || ry < 0 {
		return NewSurfaceRect(in.R)
	}
	w, h := in.R.W(), in.R.H()
	pass := func(src *Surface, rad int, horiz bool) *Surface {
		out := NewSurfaceRect(src.R)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				for ch := 0; ch < 4; ch++ {
					best := -1
					for d := -rad; d <= rad; d++ {
						xx, yy := x, y
						if horiz {
							xx += d
						} else {
							yy += d
						}
						v := 0
						if xx >= 0 && xx < w && yy >= 0 && yy < h {
							v = int(src.Pix[yy*src.Stride+xx*4+ch])
						}
						if best < 0 || (dilate && v > best) || (!dilate && v < best) {
							best = v
						}
					}
					out.Pix[y*out.Stride+x*4+ch] = uint8(best)
				}
			}
		}
		return out
	}
	out := in
	if rx > 0 {
		out = pass(out, rx, true)
	}
	if ry > 0 {
		out = pass(out, ry, false)
	}
	if out == in {
		out = cloneSurface(in)
	}
	return out
}
