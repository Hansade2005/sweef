package raster

import (
	"math"

	"github.com/sweefbrowser/sweef/internal/render"
)

// Projective layers (render.PushTransform3D).
//
// The subtree is rasterized into an offscreen surface in the layer's own plane
// at a raster scale chosen like the compositor does (from the 2D scale
// components of the layer-to-screen transform, or the device scale when the
// transform has a perspective part), then the surface is drawn as a textured
// quad through the homography of the plane, with bilinear sampling and an
// anti-aliased quad outline.

// Mat3 is a homography acting on (x, y, 1), row-major.
type Mat3 [9]float64

func (a Mat3) mul(b Mat3) (o Mat3) {
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			o[i*3+j] = a[i*3]*b[j] + a[i*3+1]*b[3+j] + a[i*3+2]*b[6+j]
		}
	}
	return
}

func (a Mat3) invert() (Mat3, bool) {
	c00 := a[4]*a[8] - a[5]*a[7]
	c01 := a[5]*a[6] - a[3]*a[8]
	c02 := a[3]*a[7] - a[4]*a[6]
	d := a[0]*c00 + a[1]*c01 + a[2]*c02
	if math.Abs(d) < 1e-30 || math.IsNaN(d) {
		return a, false
	}
	return Mat3{
		c00 / d, (a[2]*a[7] - a[1]*a[8]) / d, (a[1]*a[5] - a[2]*a[4]) / d,
		c01 / d, (a[0]*a[8] - a[2]*a[6]) / d, (a[2]*a[3] - a[0]*a[5]) / d,
		c02 / d, (a[1]*a[6] - a[0]*a[7]) / d, (a[0]*a[4] - a[1]*a[3]) / d,
	}, true
}

func (a Mat3) apply(x, y float64) (X, Y, W float64) {
	return a[0]*x + a[1]*y + a[2], a[3]*x + a[4]*y + a[5], a[6]*x + a[7]*y + a[8]
}

func mat3From(m Matrix) Mat3 { return Mat3{m[0], m[2], m[4], m[1], m[3], m[5], 0, 0, 1} }

// Layer pixel limits: a layer larger than this is rasterized at a lower scale.
const maxLayerPixels = 24 << 20

type projRec struct {
	clipPoly [][2]float64
	h        Mat3 // layer plane -> parent frame (doc coordinates)
	ox, oy   float64
	rs       float64
	parentCT Matrix
	saveDPR  float64
	hasLat   bool
}

// rasterScale picks the scale the layer is rasterized at. total maps the plane
// to device pixels.
func rasterScale(total Mat3, persp bool, base float64, b render.Rect) float64 {
	if persp || math.Abs(total[6]) > 1e-9 || math.Abs(total[7]) > 1e-9 {
		return base * perspMagnification(total, b, base)
	}
	w := total[8]
	if w == 0 {
		return base
	}
	xs := math.Hypot(total[0]/w, total[3]/w)
	ys := math.Hypot(total[1]/w, total[4]/w)
	s := math.Max(xs, ys)
	if s <= 0 || math.IsNaN(s) || math.IsInf(s, 0) {
		return base
	}
	return s
}

// perspMagnification is the integer factor a perspective layer is rasterized
// above the base scale: 1 unless the projected layer grows by more than about
// 1.27 in either direction, then the next whole number (measured against
// Chromium 1194: ratios 1.1 and 1.25 raster at 1x, 1.3 to 2 at 2x, 2.2 at 3x).
func perspMagnification(total Mat3, b render.Rect, base float64) float64 {
	if b.W <= 0 || b.H <= 0 {
		return 1
	}
	x0, y0, x1, y1 := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, c := range [][2]float64{{b.X, b.Y}, {b.Right(), b.Y}, {b.Right(), b.Bottom()}, {b.X, b.Bottom()}} {
		X, Y, W := total.apply(c[0], c[1])
		if W <= 1e-6 {
			return 1
		}
		X, Y = X/W, Y/W
		x0, x1 = math.Min(x0, X), math.Max(x1, X)
		y0, y1 = math.Min(y0, Y), math.Max(y1, Y)
	}
	r := math.Max((x1-x0)/(b.W*base), (y1-y0)/(b.H*base))
	if r <= 1.27 || math.IsNaN(r) {
		return 1
	}
	return math.Min(math.Ceil(r-1e-9), 8)
}

func (p *Painter) push3D(o render.PushTransform3D) {
	c := p.c
	m := o.M
	hp := Mat3{m[0], m[1], m[3], m[4], m[5], m[7], m[12], m[13], m[15]}
	ctm := c.Transform()
	total := mat3From(ctm).mul(hp)
	base := math.Hypot(ctm[0], ctm[1])
	rs := rasterScale(total, m[12] != 0 || m[13] != 0 || m[14] != 0, base, o.Bounds)
	ox, oy := 0.0, 0.0
	if o.Lattice {
		ox, oy = o.LatX, o.LatY
	}
	b := o.Bounds
	var r IRect
	for {
		r = IRect{int(math.Floor((b.X - ox) * rs)), int(math.Floor((b.Y - oy) * rs)), int(math.Ceil((b.Right() - ox) * rs)), int(math.Ceil((b.Bottom() - oy) * rs))}
		if r.W()*r.H() <= maxLayerPixels || rs < 0.05 {
			break
		}
		rs *= 0.7
	}
	if r.Empty() {
		r = IRect{0, 0, 1, 1}
	}
	rec := projRec{clipPoly: o.Clip, h: hp, ox: ox, oy: oy, rs: rs, parentCT: ctm, saveDPR: p.dpr, hasLat: o.Lattice}
	p.proj = append(p.proj, rec)
	c.Save()
	c.SetTransform(Matrix{rs, 0, 0, rs, -ox * rs, -oy * rs})
	c.layers = append(c.layers, layerRec{parent: c.surf, pclip: c.st.clip})
	c.surf = NewSurfaceRect(r)
	c.st.clip = &clipState{R: r}
	p.dpr = rs
	p.lat = append(p.lat, latRec{o.Lattice, o.LatX, o.LatY})
}

func (p *Painter) pop3D() {
	n := len(p.proj)
	if n == 0 {
		return
	}
	c := p.c
	rec := p.proj[n-1]
	p.proj = p.proj[:n-1]
	if len(p.lat) > 0 {
		p.lat = p.lat[:len(p.lat)-1]
	}
	p.dpr = rec.saveDPR
	if len(c.layers) == 0 {
		c.Restore()
		return
	}
	lr := c.layers[len(c.layers)-1]
	c.layers = c.layers[:len(c.layers)-1]
	layer := c.surf
	c.surf = lr.parent
	c.st.clip = lr.pclip
	c.Restore()
	drawProjected(c.surf, layer, rec, lr.pclip)
}

// drawProjected composites the layer surface (pixel grid scaled by rec.rs and
// anchored at (rec.ox, rec.oy)) onto dst through the plane's homography.
func drawProjected(dst, layer *Surface, rec projRec, cl *clipState) {
	// layer pixel (u, v) -> plane (x, y) -> parent frame -> device
	t := Mat3{1 / rec.rs, 0, rec.ox, 0, 1 / rec.rs, rec.oy, 0, 0, 1}
	q := mat3From(rec.parentCT).mul(rec.h).mul(t)
	inv, ok := q.invert()
	if !ok {
		return
	}
	rect := [][2]float64{{float64(layer.R.X0), float64(layer.R.Y0)}, {float64(layer.R.X1), float64(layer.R.Y0)}, {float64(layer.R.X1), float64(layer.R.Y1)}, {float64(layer.R.X0), float64(layer.R.Y1)}}
	area := cl.R.Intersect(dst.R)
	pts := devicePoly(q, rect)
	if len(pts) < 3 {
		return
	}
	mask := RasterizePolys([]Subpath{{P: pts, Closed: true}}, NonZero, area)
	if mask == nil {
		return
	}
	if len(rec.clipPoly) >= 3 {
		// The two halves of a layer cut by a plane share an edge. Each piece
		// is drawn with that edge pushed out by half a pixel so no seam of
		// uncovered pixels remains (the compositor draws split quads without
		// edge anti-aliasing on the cut).
		cp := make([][2]float64, len(rec.clipPoly))
		for i, v := range rec.clipPoly {
			cp[i] = [2]float64{(v[0] - rec.ox) * rec.rs, (v[1] - rec.oy) * rec.rs}
		}
		cpts := devicePoly(q, cp)
		if len(cpts) < 3 {
			return
		}
		cpts = offsetConvex(cpts, 0.6)
		cm := RasterizePolys([]Subpath{{P: cpts, Closed: true}}, NonZero, area)
		mask = maskMul(mask, cm)
		if mask == nil {
			return
		}
	}
	sh := &projShader{s: layer, inv: inv}
	composite(dst, mask, sh, SourceOver, cl)
}

// devicePoly maps a polygon in layer pixels to device space, clipping it to the
// half space in front of the camera first.
func devicePoly(q Mat3, poly [][2]float64) []Pt {
	type hv struct{ x, y, w float64 }
	hs := make([]hv, len(poly))
	for i, v := range poly {
		x, y, w := q.apply(v[0], v[1])
		hs[i] = hv{x, y, w}
	}
	const nearW = 1e-6
	var out []hv
	for i := range hs {
		a, b := hs[i], hs[(i+1)%len(hs)]
		ain, bin := a.w > nearW, b.w > nearW
		if ain {
			out = append(out, a)
		}
		if ain != bin {
			t := (nearW - a.w) / (b.w - a.w)
			out = append(out, hv{a.x + (b.x-a.x)*t, a.y + (b.y-a.y)*t, a.w + (b.w-a.w)*t})
		}
	}
	pts := make([]Pt, len(out))
	for i, v := range out {
		pts[i] = Pt{v.x / v.w, v.y / v.w}
	}
	return pts
}

// offsetConvex pushes every edge of a convex polygon outward by d.
func offsetConvex(p []Pt, d float64) []Pt {
	n := len(p)
	var area float64
	for i := 0; i < n; i++ {
		j := (i + 1) % n
		area += p[i].X*p[j].Y - p[j].X*p[i].Y
	}
	sg := 1.0
	if area < 0 {
		sg = -1
	}
	type ln struct{ ax, ay, dx, dy float64 }
	ls := make([]ln, n)
	for i := 0; i < n; i++ {
		j := (i + 1) % n
		dx, dy := p[j].X-p[i].X, p[j].Y-p[i].Y
		l := math.Hypot(dx, dy)
		if l == 0 {
			return p
		}
		// outward normal for a polygon of the given orientation
		nx, ny := sg*dy/l, -sg*dx/l
		ls[i] = ln{p[i].X + nx*d, p[i].Y + ny*d, dx, dy}
	}
	out := make([]Pt, n)
	for i := 0; i < n; i++ {
		a, b := ls[(i+n-1)%n], ls[i]
		den := a.dx*b.dy - a.dy*b.dx
		if math.Abs(den) < 1e-12 {
			out[i] = p[i]
			continue
		}
		t := ((b.ax-a.ax)*b.dy - (b.ay-a.ay)*b.dx) / den
		out[i] = Pt{a.ax + a.dx*t, a.ay + a.dy*t}
	}
	return out
}

// projShader samples a layer surface through the inverse homography with
// bilinear filtering and clamp-to-edge addressing.
type projShader struct {
	s   *Surface
	inv Mat3 // device -> layer pixels
}

func (ps *projShader) ShadeRow(x, y, n int, dst []uint8) {
	s := ps.s
	inv := ps.inv
	w0, h0 := s.R.W(), s.R.H()
	fy := float64(y) + 0.5
	for i := 0; i < n; i++ {
		fx := float64(x+i) + 0.5
		u := inv[0]*fx + inv[1]*fy + inv[2]
		v := inv[3]*fx + inv[4]*fy + inv[5]
		w := inv[6]*fx + inv[7]*fy + inv[8]
		o := dst[i*4 : i*4+4 : i*4+4]
		if w == 0 {
			o[0], o[1], o[2], o[3] = 0, 0, 0, 0
			continue
		}
		u, v = u/w-float64(s.R.X0)-0.5, v/w-float64(s.R.Y0)-0.5
		x0, y0 := int(math.Floor(u)), int(math.Floor(v))
		ax, ay := u-float64(x0), v-float64(y0)
		x1, y1 := x0+1, y0+1
		x0, x1 = clampInt(x0, 0, w0-1), clampInt(x1, 0, w0-1)
		y0, y1 = clampInt(y0, 0, h0-1), clampInt(y1, 0, h0-1)
		p00 := s.Pix[y0*s.Stride+x0*4 : y0*s.Stride+x0*4+4]
		p10 := s.Pix[y0*s.Stride+x1*4 : y0*s.Stride+x1*4+4]
		p01 := s.Pix[y1*s.Stride+x0*4 : y1*s.Stride+x0*4+4]
		p11 := s.Pix[y1*s.Stride+x1*4 : y1*s.Stride+x1*4+4]
		w00, w10, w01, w11 := (1-ax)*(1-ay), ax*(1-ay), (1-ax)*ay, ax*ay
		for k := 0; k < 4; k++ {
			f := w00*float64(p00[k]) + w10*float64(p10[k]) + w01*float64(p01[k]) + w11*float64(p11[k])
			o[k] = uint8(f + 0.5)
		}
	}
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
