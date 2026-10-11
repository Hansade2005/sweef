package raster

import (
	"math"
)

// Brush is a fill or stroke source.
type Brush struct {
	Color    Color
	Gradient *GradientDef
	Pattern  *Pattern
}

// Pattern is a repeating image source; M maps pattern space to user space.
type Pattern struct {
	Img        *Surface
	RepX, RepY bool
	M          Matrix
}

// Quality is imageSmoothingQuality.
type Quality int

const (
	QualityLow Quality = iota
	QualityMedium
	QualityHigh
)

type clipState struct {
	R IRect
	M *Mask // optional AA mask covering R
}

type state struct {
	ctm          Matrix
	fill, stroke Brush
	ss           StrokeStyle
	alpha        float64
	op           CompositeOp
	smooth       bool
	quality      Quality
	shadowColor  Color
	shadowBlur   float64
	shadowDX     float64
	shadowDY     float64
	filters      []Filter
	clip         *clipState
	text         textState
}

type layerRec struct {
	parent  *Surface
	pclip   *clipState
	filters []Filter
}

// Context is a 2D drawing context over a Surface.
type layerMeta struct {
	opacity float64
	blend   string
	mask    *maskJob    // set for layers opened by PushMask
	steps   []layerStep // CSS filter list with SVG filter references
}

type Context struct {
	layerMeta []layerMeta
	surf      *Surface
	st        state
	stack     []state
	layers    []layerRec
}

// NewContext creates a context drawing onto a new transparent w x h surface.
func NewContext(w, h int) *Context { return NewContextSurface(NewSurface(w, h)) }

// NewContextSurface draws onto an existing surface.
func NewContextSurface(s *Surface) *Context {
	c := &Context{surf: s}
	c.ResetState()
	return c
}

// Surface returns the current target surface.
func (c *Context) Surface() *Surface { return c.surf }

// ResetState restores all drawing state to defaults (surface untouched).
func (c *Context) ResetState() {
	c.stack = c.stack[:0]
	c.st = state{
		ctm:    Identity,
		fill:   Brush{Color: Black},
		stroke: Brush{Color: Black},
		ss:     StrokeStyle{Width: 1, Cap: CapButt, Join: JoinMiter, MiterLimit: 10},
		alpha:  1,
		op:     SourceOver,
		smooth: true,
		clip:   &clipState{R: c.surf.R},
	}
	c.st.text = defaultTextState()
}

// Save pushes the drawing state.
func (c *Context) Save() { c.stack = append(c.stack, c.st) }

// Restore pops the drawing state.
func (c *Context) Restore() {
	if n := len(c.stack); n > 0 {
		c.st = c.stack[n-1]
		c.stack = c.stack[:n-1]
	}
}

// SaveDepth is the number of saved states.
func (c *Context) SaveDepth() int { return len(c.stack) }

func (c *Context) SetTransform(m Matrix)           { c.st.ctm = m }
func (c *Context) Transform() Matrix               { return c.st.ctm }
func (c *Context) Concat(m Matrix)                 { c.st.ctm = c.st.ctm.Mul(m) }
func (c *Context) Translate(x, y float64)          { c.Concat(Translate(x, y)) }
func (c *Context) Scale(x, y float64)              { c.Concat(Scale(x, y)) }
func (c *Context) Rotate(a float64)                { c.Concat(Rotate(a)) }
func (c *Context) SetFill(p Brush)                 { c.st.fill = p }
func (c *Context) SetStroke(p Brush)               { c.st.stroke = p }
func (c *Context) FillPaint() Brush                { return c.st.fill }
func (c *Context) StrokePaint() Brush              { return c.st.stroke }
func (c *Context) SetStrokeStyle(s StrokeStyle)    { c.st.ss = s }
func (c *Context) StrokeStyle() StrokeStyle        { return c.st.ss }
func (c *Context) SetGlobalAlpha(a float64)        { c.st.alpha = a }
func (c *Context) GlobalAlpha() float64            { return c.st.alpha }
func (c *Context) SetOp(op CompositeOp)            { c.st.op = op }
func (c *Context) Op() CompositeOp                 { return c.st.op }
func (c *Context) SetSmoothing(on bool, q Quality) { c.st.smooth, c.st.quality = on, q }
func (c *Context) SetFilters(f []Filter)           { c.st.filters = f }

// SetShadow configures the drop shadow (blur is the canvas shadowBlur value).
func (c *Context) SetShadow(col Color, blur, dx, dy float64) {
	c.st.shadowColor, c.st.shadowBlur, c.st.shadowDX, c.st.shadowDY = col, blur, dx, dy
}

// ClipBounds returns the current clip's bounding rect.
func (c *Context) ClipBounds() IRect { return c.st.clip.R.Intersect(c.surf.R) }

// ---- clipping ---------------------------------------------------------------

// ClipMask intersects the clip with a coverage mask (nil = empty).
func (c *Context) clipWithMask(m *Mask) {
	cur := c.st.clip
	if m == nil {
		c.st.clip = &clipState{}
		return
	}
	r := cur.R.Intersect(m.B)
	if r.Empty() {
		c.st.clip = &clipState{}
		return
	}
	// detect fully covered rectangle (no AA needed)
	full := true
	for y := r.Y0; y < r.Y1 && full; y++ {
		row := m.Row(y)[r.X0-m.B.X0 : r.X1-m.B.X0]
		for _, v := range row {
			if v != 255 {
				full = false
				break
			}
		}
	}
	if full && cur.M == nil {
		c.st.clip = &clipState{R: r}
		return
	}
	nm := newMask(r)
	for y := r.Y0; y < r.Y1; y++ {
		src := m.Row(y)[r.X0-m.B.X0 : r.X1-m.B.X0]
		dst := nm.Row(y)
		if cur.M != nil {
			cr := cur.M.Row(y)[r.X0-cur.M.B.X0 : r.X1-cur.M.B.X0]
			for i := range dst {
				dst[i] = uint8(mul255(uint32(src[i]), uint32(cr[i])))
			}
		} else {
			copy(dst, src)
		}
	}
	c.st.clip = &clipState{R: r, M: nm}
}

// ClipPath intersects the clip with p (user space).
func (c *Context) ClipPath(p *Path, rule FillRule) {
	c.clipWithMask(FillMask(p, c.st.ctm, rule, c.ClipBounds()))
}

// ClipRect intersects the clip with a rectangle (user space).
func (c *Context) ClipRect(x, y, w, h float64) {
	p := NewPath()
	p.Rect(x, y, w, h)
	c.ClipPath(p, NonZero)
}

// ClipRoundRect intersects the clip with a rounded rectangle.
func (c *Context) ClipRoundRect(r Rect, rad [4][2]float64) {
	p := NewPath()
	p.RoundRect(r, rad)
	c.ClipPath(p, NonZero)
}

// ---- shaders ----------------------------------------------------------------

func (c *Context) filterMode() FilterMode {
	if !c.st.smooth {
		return FilterNearest
	}
	switch c.st.quality {
	case QualityMedium:
		return FilterMipmap
	case QualityHigh:
		return FilterBicubic
	}
	return FilterBilinear
}

func (c *Context) shaderFor(p Brush, alpha float64) Shader {
	switch {
	case p.Gradient != nil:
		return NewGradientShader(*p.Gradient, c.st.ctm, alpha)
	case p.Pattern != nil:
		return NewImageShader(p.Pattern.Img, c.st.ctm.Mul(p.Pattern.M), p.Pattern.RepX, p.Pattern.RepY, c.filterMode(), alpha)
	}
	col := p.Color
	col.A *= alpha
	return Solid(col)
}

// ---- compositing --------------------------------------------------------------

type surfShader struct {
	s     *Surface
	alpha uint32 // 0..256
}

func (ss surfShader) ShadeRow(x, y, n int, dst []uint8) {
	s := ss.s
	if y < s.R.Y0 || y >= s.R.Y1 {
		clear(dst[:n*4])
		return
	}
	// copy overlap
	x0, x1 := max(x, s.R.X0), min(x+n, s.R.X1)
	if x0 > x {
		clear(dst[:(x0-x)*4])
	}
	if x1 < x+n {
		clear(dst[max(x1-x, 0)*4 : n*4])
	}
	if x1 <= x0 {
		return
	}
	src := s.Row(y, x0, x1)
	out := dst[(x0-x)*4 : (x1-x)*4]
	if ss.alpha == 256 {
		copy(out, src)
		return
	}
	for i := range src {
		out[i] = uint8((uint32(src[i]) * ss.alpha) >> 8)
	}
}

// composite blends shader pixels through coverage mask m (nil = everywhere in
// the clip) onto dst using op and the clip.
func composite(dst *Surface, m *Mask, sh Shader, op CompositeOp, cl *clipState) {
	r := cl.R.Intersect(dst.R)
	if m != nil && !op.unbounded() && op != Copy {
		r = r.Intersect(m.B)
	}
	if r.Empty() {
		return
	}
	if op == Copy {
		// clear the clip area, then draw normally
		clearOp := solidShader{[4]uint8{0, 0, 0, 255}}
		composite(dst, nil, clearOp, DestinationOut, cl)
		composite(dst, m, sh, SourceOver, cl)
		return
	}
	ss, isSolid := sh.(solidShader)
	unb := op.unbounded()
	w := r.W()
	parallelFor(r.H(), max(1, 65536/w), func(lo, hi int) {
		src := make([]uint8, w*4)
		cov := make([]uint8, w)
		var clipRow, orig []uint8
		if cl.M != nil {
			clipRow = make([]uint8, w)
			if unb {
				orig = make([]uint8, w*4)
			}
		}
		if isSolid {
			for i := 0; i < w; i++ {
				src[i*4], src[i*4+1], src[i*4+2], src[i*4+3] = ss.c[0], ss.c[1], ss.c[2], ss.c[3]
			}
		}
		for y := r.Y0 + lo; y < r.Y0+hi; y++ {
			// coverage
			a, b := 0, w
			if m != nil {
				if y < m.B.Y0 || y >= m.B.Y1 {
					if !unb {
						continue
					}
					clear(cov)
					a, b = 0, w
				} else {
					mx0, mx1 := max(r.X0, m.B.X0), min(r.X1, m.B.X1)
					clear(cov)
					if mx1 > mx0 {
						copy(cov[mx0-r.X0:], m.Row(y)[mx0-m.B.X0:mx1-m.B.X0])
					}
					a, b = mx0-r.X0, mx1-r.X0
					if unb {
						a, b = 0, w
					}
				}
			} else {
				for i := range cov {
					cov[i] = 255
				}
			}
			if cl.M != nil {
				clear(clipRow)
				cx0, cx1 := max(r.X0, cl.M.B.X0), min(r.X1, cl.M.B.X1)
				if cl.M.B.Y0 <= y && y < cl.M.B.Y1 && cx1 > cx0 {
					copy(clipRow[cx0-r.X0:], cl.M.Row(y)[cx0-cl.M.B.X0:cx1-cl.M.B.X0])
				}
				if !unb {
					for i := a; i < b; i++ {
						cov[i] = uint8(mul255(uint32(cov[i]), uint32(clipRow[i])))
					}
				}
			}
			if !unb {
				for a < b && cov[a] == 0 {
					a++
				}
				for b > a && cov[b-1] == 0 {
					b--
				}
				if a >= b {
					continue
				}
			}
			if !isSolid {
				sh.ShadeRow(r.X0+a, y, b-a, src[a*4:])
			}
			drow := dst.Row(y, r.X0+a, r.X0+b)
			if unb && cl.M != nil {
				copy(orig[a*4:b*4], drow)
			}
			blendRow(drow, src[a*4:b*4], cov[a:b], op, b-a)
			if unb && cl.M != nil {
				lerpRow(drow, orig[a*4:b*4], clipRow[a:b], b-a)
			}
		}
	})
}

// ---- drawing -----------------------------------------------------------------

// draw paints a coverage mask with the shader, honoring shadow and filter.
func (c *Context) draw(m *Mask, sh Shader) {
	if m == nil {
		return
	}
	st := &c.st
	if len(st.filters) > 0 {
		c.drawFiltered(m, sh)
		return
	}
	c.drawShadow(m, sh, c.surf, st.clip)
	composite(c.surf, m, sh, st.op, st.clip)
}

func (c *Context) hasShadow() bool {
	s := &c.st
	return s.shadowColor.A > 0 && (s.shadowBlur != 0 || s.shadowDX != 0 || s.shadowDY != 0)
}

// drawShadow draws just the shadow of (m, sh) onto dst.
func (c *Context) drawShadow(m *Mask, sh Shader, dst *Surface, cl *clipState) {
	if !c.hasShadow() {
		return
	}
	s := &c.st
	// effective alpha of the shape
	am := newMask(m.B)
	ss, isSolid := sh.(solidShader)
	w := m.B.W()
	parallelFor(m.B.H(), max(1, 65536/max(w, 1)), func(lo, hi int) {
		buf := make([]uint8, w*4)
		for y := m.B.Y0 + lo; y < m.B.Y0+hi; y++ {
			cv := m.Row(y)
			out := am.Row(y)
			if isSolid {
				a := uint32(ss.c[3])
				for i := range cv {
					out[i] = uint8(mul255(uint32(cv[i]), a))
				}
				continue
			}
			sh.ShadeRow(m.B.X0, y, w, buf)
			for i := range cv {
				out[i] = uint8(mul255(uint32(cv[i]), uint32(buf[i*4+3])))
			}
		}
	})
	sigma := s.shadowBlur / 2
	dx, dy := int(math.Round(s.shadowDX)), int(math.Round(s.shadowDY))
	var bm *Mask
	if sigma > 0 {
		bm = BlurMask(am, sigma, sigma, dx, dy, cl.R.Intersect(dst.R))
	} else {
		bm = &Mask{B: IRect{am.B.X0 + dx, am.B.Y0 + dy, am.B.X1 + dx, am.B.Y1 + dy}, Stride: am.Stride, A: am.A}
	}
	if bm == nil {
		return
	}
	col := s.shadowColor
	composite(dst, bm, Solid(col), s.op, cl)
}

// FillPathShader fills p (user space) with an explicit shader.
func (c *Context) FillPathShader(p *Path, rule FillRule, sh Shader) {
	c.draw(FillMask(p, c.st.ctm, rule, c.drawBounds()), sh)
}

// drawBounds is the area that can be affected by a draw (clip, or a bit more
// for shadows/filters which are clipped later anyway).
func (c *Context) drawBounds() IRect {
	b := c.ClipBounds()
	if c.hasShadow() || len(c.st.filters) > 0 {
		// shapes outside the clip may cast shadows into it
		e := int(math.Ceil(math.Abs(c.st.shadowBlur)*1.5)) + int(math.Ceil(math.Max(math.Abs(c.st.shadowDX), math.Abs(c.st.shadowDY))))
		e += filterPadMax(c.st.filters)
		b = IRect{b.X0 - e, b.Y0 - e, b.X1 + e, b.Y1 + e}
	}
	return b
}

// FillPath fills p with the fill paint.
func (c *Context) FillPath(p *Path, rule FillRule) {
	c.draw(FillMask(p, c.st.ctm, rule, c.drawBounds()), c.shaderFor(c.st.fill, c.st.alpha))
}

// StrokePath strokes p with the stroke paint and style.
func (c *Context) StrokePath(p *Path) {
	if mk, ok := StrokeMaskFx(p, c.st.ctm, c.st.ss, c.drawBounds()); ok {
		c.draw(mk, c.shaderFor(c.st.stroke, c.st.alpha))
		return
	}
	c.draw(StrokeMask(p, c.st.ctm, c.st.ss, c.drawBounds()), c.shaderFor(c.st.stroke, c.st.alpha))
}

// FillRect fills a rectangle in user space.
func (c *Context) FillRect(x, y, w, h float64) {
	if w == 0 || h == 0 {
		return
	}
	c.draw(c.rectMask(x, y, w, h), c.shaderFor(c.st.fill, c.st.alpha))
}

// FillRectShader fills a rectangle with an explicit shader.
func (c *Context) FillRectShader(x, y, w, h float64, sh Shader) {
	c.draw(c.rectMask(x, y, w, h), sh)
}

func (c *Context) rectMask(x, y, w, h float64) *Mask {
	b := c.drawBounds()
	m := c.st.ctm
	if m[1] == 0 && m[2] == 0 {
		x0, y0 := m.Apply(x, y)
		x1, y1 := m.Apply(x+w, y+h)
		return rectCoverage(x0, y0, x1, y1, b)
	}
	p := NewPath()
	p.Rect(x, y, w, h)
	return FillMask(p, m, NonZero, b)
}

// rectCoverage builds the exact coverage mask of an axis-aligned device rect.
func rectCoverage(x0, y0, x1, y1 float64, clip IRect) *Mask {
	if x0 > x1 {
		x0, x1 = x1, x0
	}
	if y0 > y1 {
		y0, y1 = y1, y0
	}
	if math.IsNaN(x0+y0+x1+y1) || x1 <= x0 || y1 <= y0 {
		return nil
	}
	b := IRect{int(math.Floor(math.Max(x0, -1e9))), int(math.Floor(math.Max(y0, -1e9))), int(math.Ceil(math.Min(x1, 1e9))), int(math.Ceil(math.Min(y1, 1e9)))}.Intersect(clip)
	if b.Empty() {
		return nil
	}
	m := newMask(b)
	cx := make([]float64, b.W())
	for i := range cx {
		px := float64(b.X0 + i)
		cx[i] = math.Max(0, math.Min(px+1, x1)-math.Max(px, x0))
	}
	for y := b.Y0; y < b.Y1; y++ {
		py := float64(y)
		cy := math.Max(0, math.Min(py+1, y1)-math.Max(py, y0))
		row := m.Row(y)
		for i := range row {
			row[i] = uint8(cx[i]*cy*255 + 0.5)
		}
	}
	return m
}

// StrokeRect strokes a rectangle outline.
func (c *Context) StrokeRect(x, y, w, h float64) {
	p := NewPath()
	if w == 0 && h == 0 {
		return
	}
	if w == 0 || h == 0 {
		p.MoveTo(x, y)
		p.LineTo(x+w, y+h)
	} else {
		p.Rect(x, y, w, h)
	}
	c.StrokePath(p)
}

// ClearRect makes a rectangle transparent (ignores shadows and compositing).
func (c *Context) ClearRect(x, y, w, h float64) {
	if w == 0 || h == 0 {
		return
	}
	m := c.rectMask(x, y, w, h)
	if m == nil {
		return
	}
	composite(c.surf, m, solidShader{[4]uint8{0, 0, 0, 255}}, DestinationOut, c.st.clip)
}

// ---- image data ----------------------------------------------------------------

// GetImageData returns straight-alpha RGBA bytes for a device rect.
func (c *Context) GetImageData(x, y, w, h int) []uint8 {
	out := make([]uint8, w*h*4)
	for j := 0; j < h; j++ {
		sy := y + j
		if sy < c.surf.R.Y0 || sy >= c.surf.R.Y1 {
			continue
		}
		for i := 0; i < w; i++ {
			sx := x + i
			if sx < c.surf.R.X0 || sx >= c.surf.R.X1 {
				continue
			}
			o := (sy-c.surf.R.Y0)*c.surf.Stride + (sx-c.surf.R.X0)*4
			a := uint32(c.surf.Pix[o+3])
			d := (j*w + i) * 4
			switch a {
			case 0:
			case 255:
				copy(out[d:d+4], c.surf.Pix[o:o+4])
			default:
				out[d] = unpremul(c.surf.Pix[o], a)
				out[d+1] = unpremul(c.surf.Pix[o+1], a)
				out[d+2] = unpremul(c.surf.Pix[o+2], a)
				out[d+3] = uint8(a)
			}
		}
	}
	return out
}

// PutImageData writes straight-alpha pixels (w x h) at (dx,dy) restricted to
// the dirty rectangle (relative to the image data), replacing destination
// pixels without compositing, clipping or transforms.
func (c *Context) PutImageData(data []uint8, w, h, dx, dy, dirtyX, dirtyY, dirtyW, dirtyH int) {
	if dirtyW < 0 {
		dirtyX += dirtyW
		dirtyW = -dirtyW
	}
	if dirtyH < 0 {
		dirtyY += dirtyH
		dirtyH = -dirtyH
	}
	x0, y0 := max(dirtyX, 0), max(dirtyY, 0)
	x1, y1 := min(dirtyX+dirtyW, w), min(dirtyY+dirtyH, h)
	for j := y0; j < y1; j++ {
		ty := dy + j
		if ty < c.surf.R.Y0 || ty >= c.surf.R.Y1 {
			continue
		}
		for i := x0; i < x1; i++ {
			tx := dx + i
			if tx < c.surf.R.X0 || tx >= c.surf.R.X1 {
				continue
			}
			s := (j*w + i) * 4
			a := uint32(data[s+3])
			o := (ty-c.surf.R.Y0)*c.surf.Stride + (tx-c.surf.R.X0)*4
			if a == 255 {
				copy(c.surf.Pix[o:o+4], data[s:s+4])
			} else {
				c.surf.Pix[o] = uint8(mul255(uint32(data[s]), a))
				c.surf.Pix[o+1] = uint8(mul255(uint32(data[s+1]), a))
				c.surf.Pix[o+2] = uint8(mul255(uint32(data[s+2]), a))
				c.surf.Pix[o+3] = uint8(a)
			}
		}
	}
}

// ---- hit testing ---------------------------------------------------------------

// windingAt returns the winding number of the device-space flattened path at (x,y).
func windingAt(sp []Subpath, x, y float64) (int, bool) {
	wn := 0
	for _, s := range sp {
		n := len(s.P)
		if n < 2 {
			continue
		}
		for i := 0; i < n; i++ {
			a, b := s.P[i], s.P[(i+1)%n]
			if (a.Y <= y) != (b.Y <= y) {
				t := (y - a.Y) / (b.Y - a.Y)
				xi := a.X + t*(b.X-a.X)
				if xi > x {
					if b.Y > a.Y {
						wn++
					} else {
						wn--
					}
				}
			}
		}
	}
	return wn, true
}

// IsPointInPath reports whether the device-space point (x,y) is inside p
// (p in user space; transformed by the CTM).
func (c *Context) IsPointInPath(p *Path, x, y float64, rule FillRule) bool {
	return PointInPath(p, c.st.ctm, x, y, rule)
}

// PointInPath tests a point against path p mapped through m.
func PointInPath(p *Path, m Matrix, x, y float64, rule FillRule) bool {
	sp := flattenDevice(p, m)
	wn, _ := windingAt(sp, x, y)
	if rule == EvenOdd {
		return wn%2 != 0
	}
	return wn != 0
}

// IsPointInStroke reports whether (x,y) in device space lies on the stroke of p.
func (c *Context) IsPointInStroke(p *Path, x, y float64) bool {
	m := c.st.ctm
	sc := m.MaxScale()
	if sc == 0 {
		return false
	}
	sp := p.Flatten(0.05 / sc)
	polys := StrokePolys(sp, c.st.ss, 0.05/sc)
	for i := range polys {
		for j, q := range polys[i].P {
			polys[i].P[j] = m.ApplyPt(q)
		}
	}
	wn, _ := windingAt(polys, x, y)
	// pieces are consistently oriented; any non-zero winding counts
	return wn != 0
}

// SetTransformStash is a no-op hook kept for symmetry with layers.
func (c *Context) SetTransformStash() {}

// FillPathDevice fills a path already in device space.
func (c *Context) FillPathDevice(p *Path, rule FillRule) {
	c.draw(FillMask(p, Identity, rule, c.drawBounds()), c.shaderFor(c.st.fill, c.st.alpha))
}

// ClipPathDevice intersects the clip with a device-space path.
func (c *Context) ClipPathDevice(p *Path, rule FillRule) {
	c.clipWithMask(FillMask(p, Identity, rule, c.ClipBounds()))
}
