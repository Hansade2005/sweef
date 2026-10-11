package raster

import (
	"image"
	"math"

	"golang.org/x/net/html"

	"github.com/sweefbrowser/sweef/internal/render"
)

// PaintEnv supplies resources to Paint.
type PaintEnv struct {
	// Bitmap returns live pixels for a render.Bitmap op's node (may be nil).
	Bitmap func(n *html.Node) image.Image
	// Background is painted first; the zero value is transparent. Browsers
	// use opaque white for the page canvas.
	Background Color
}

// Paint executes a display list into a new RGBA image of
// round(width*dpr) x round(height*dpr) device pixels (width/height are CSS
// px; coordinates in ops are CSS px scaled by dpr). The result is
// premultiplied RGBA; with an opaque Background it is also opaque.
func Paint(list []render.Op, width, height int, dpr float64, env PaintEnv) *image.RGBA {
	if dpr <= 0 {
		dpr = 1
	}
	w, h := int(math.Round(float64(width)*dpr)), int(math.Round(float64(height)*dpr))
	p := NewPainter(w, h, dpr, env)
	p.Run(list)
	return p.Surface().Image()
}

// Painter executes display lists incrementally.
type Painter struct {
	c   *Context
	env PaintEnv
	dpr float64
}

// NewPainter creates a painter over a new w x h device pixel surface.
func NewPainter(w, h int, dpr float64, env PaintEnv) *Painter {
	c := NewContext(w, h)
	if env.Background.A > 0 {
		c.SetFill(Brush{Color: env.Background})
		c.FillRect(0, 0, float64(w), float64(h))
	}
	c.SetTransform(Scale(dpr, dpr))
	c.SetSmoothing(true, QualityLow)
	return &Painter{c: c, env: env, dpr: dpr}
}

// Surface returns the painter's target.
func (p *Painter) Surface() *Surface { return p.c.Surface() }

// Run executes ops.
func (p *Painter) Run(list []render.Op) {
	for _, op := range list {
		p.op(op)
	}
	for p.c.LayerDepth() > 0 {
		p.c.EndLayer(1, SourceOver)
	}
}

func col(c render.Color) Color { return RGBA8(c.R, c.G, c.B, c.A) }

func (p *Painter) simple() bool {
	m := p.c.Transform()
	return m[1] == 0 && m[2] == 0 && m[0] > 0 && m[3] > 0
}

// snap rounds a CSS rect to device pixel edges when the transform allows.
func (p *Painter) snap(r render.Rect) render.Rect {
	if !p.simple() {
		return r
	}
	m := p.c.Transform()
	x0 := math.Round(r.X*m[0]+m[4]) - m[4]
	x1 := math.Round((r.X+r.W)*m[0]+m[4]) - m[4]
	y0 := math.Round(r.Y*m[3]+m[5]) - m[5]
	y1 := math.Round((r.Y+r.H)*m[3]+m[5]) - m[5]
	return render.Rect{X: x0 / m[0], Y: y0 / m[3], W: (x1 - x0) / m[0], H: (y1 - y0) / m[3]}
}

func rrPath(r render.Rect, rad render.Radii) *Path {
	pa := NewPath()
	if rad.IsZero() {
		pa.Rect(r.X, r.Y, r.W, r.H)
	} else {
		pa.RoundRect(Rect{r.X, r.Y, r.W, r.H}, [4][2]float64(rad))
	}
	return pa
}

func (p *Painter) op(op render.Op) {
	c := p.c
	switch o := op.(type) {
	case render.FillRect:
		if o.Color.A == 0 || o.Rect.W <= 0 || o.Rect.H <= 0 {
			return
		}
		r := p.snap(o.Rect)
		c.SetFill(Brush{Color: col(o.Color)})
		if o.Radii.IsZero() {
			c.FillRect(r.X, r.Y, r.W, r.H)
		} else {
			c.FillPath(rrPath(r, o.Radii), NonZero)
		}
	case render.Border:
		p.border(o)
	case render.TextRun:
		p.text(o)
	case render.Image:
		p.image(o)
	case render.Gradient:
		p.gradient(o)
	case render.BoxShadow:
		p.boxShadow(o)
	case render.PushClip:
		c.Save()
		r := p.snap(o.Rect)
		if o.Radii.IsZero() {
			c.ClipRect(r.X, r.Y, r.W, r.H)
		} else {
			c.ClipPath(rrPath(r, o.Radii), NonZero)
		}
	case render.PopClip:
		c.Restore()
	case render.PushLayer:
		c.Save()
		b := c.ClipBounds()
		if o.Bounds.W > 0 && o.Bounds.H > 0 {
			m := c.Transform()
			pa := NewPath()
			pa.Rect(o.Bounds.X, o.Bounds.Y, o.Bounds.W, o.Bounds.H)
			bb, _ := pa.Transform(m).Bounds()
			b = bb.OutsetBounds().Intersect(b)
		}
		fl := make([]Filter, len(o.Filter))
		dpr := c.Transform()[0]
		for i, f := range o.Filter {
			fl[i] = Filter{Name: f.Name, Args: append([]float64(nil), f.Args...), Color: col(f.Color)}
			switch f.Name {
			case "blur":
				fl[i].Args[0] *= dpr
			case "drop-shadow":
				for k := range fl[i].Args {
					fl[i].Args[k] *= dpr
				}
			}
		}
		c.SetTransformStash()
		c.BeginLayer(b, fl)
		c.layerMeta = append(c.layerMeta, layerMeta{opacity: o.Opacity, blend: o.Blend})
	case render.PopLayer:
		if n := len(c.layerMeta); n > 0 && c.LayerDepth() > 0 {
			lm := c.layerMeta[n-1]
			c.layerMeta = c.layerMeta[:n-1]
			op, _ := ParseOp(lm.blend)
			if lm.blend == "" {
				op = SourceOver
			}
			a := lm.opacity
			c.EndLayer(a, op)
			c.Restore()
		}
	case render.PushTransform:
		c.Save()
		c.Concat(Matrix(o.M))
	case render.PopTransform:
		c.Restore()
	case render.Bitmap:
		p.bitmap(o)
	case render.SVGDraw:
		p.svgDraw(o)
	case render.Scrollbar:
		p.scrollbar(o)
	case render.ScrollCorner:
		p.scrollCorner(o)
	case render.BackdropFilter:
		p.backdrop(o)
	}
}

func insetRRect(r render.Rect, rad render.Radii, l, t, rt, b float64) (render.Rect, render.Radii) {
	n := render.Rect{X: r.X + l, Y: r.Y + t, W: math.Max(0, r.W-l-rt), H: math.Max(0, r.H-t-b)}
	var out render.Radii
	ins := [4][2]float64{{l, t}, {rt, t}, {rt, b}, {l, b}}
	for i := 0; i < 4; i++ {
		out[i][0] = math.Max(0, rad[i][0]-ins[i][0])
		out[i][1] = math.Max(0, rad[i][1]-ins[i][1])
		if out[i][0] == 0 || out[i][1] == 0 {
			out[i] = [2]float64{}
		}
	}
	return n, out
}

// scaleRadii applies the CSS rule that overlapping radii shrink uniformly.
func fitRadii(r render.Rect, rad render.Radii) render.Radii {
	f := 1.0
	chk := func(sum, avail float64) {
		if sum > avail && sum > 0 {
			f = math.Min(f, avail/sum)
		}
	}
	chk(rad[0][0]+rad[1][0], r.W)
	chk(rad[3][0]+rad[2][0], r.W)
	chk(rad[0][1]+rad[3][1], r.H)
	chk(rad[1][1]+rad[2][1], r.H)
	if f < 1 {
		for i := range rad {
			rad[i][0] *= f
			rad[i][1] *= f
		}
	}
	return rad
}

func (p *Painter) maskOf(pa *Path, rule FillRule) *Mask {
	return FillMask(pa, p.c.Transform(), rule, p.c.drawBounds())
}

func maskMul(a, b *Mask) *Mask {
	if a == nil || b == nil {
		return nil
	}
	r := a.B.Intersect(b.B)
	if r.Empty() {
		return nil
	}
	o := newMask(r)
	for y := r.Y0; y < r.Y1; y++ {
		ar := a.Row(y)[r.X0-a.B.X0 : r.X1-a.B.X0]
		br := b.Row(y)[r.X0-b.B.X0 : r.X1-b.B.X0]
		or := o.Row(y)
		for i := range or {
			or[i] = uint8(mul255(uint32(ar[i]), uint32(br[i])))
		}
	}
	return o
}

func maskSub(a, b *Mask) *Mask { // a * (1-b)
	if a == nil || b == nil {
		return a
	}
	o := &Mask{B: a.B, Stride: a.Stride, A: make([]uint8, len(a.A))}
	for y := a.B.Y0; y < a.B.Y1; y++ {
		ar := a.Row(y)
		or := o.Row(y)
		copy(or, ar)
		if y < b.B.Y0 || y >= b.B.Y1 {
			continue
		}
		x0, x1 := max(a.B.X0, b.B.X0), min(a.B.X1, b.B.X1)
		if x1 <= x0 {
			continue
		}
		br := b.Row(y)[x0-b.B.X0 : x1-b.B.X0]
		for i, v := range br {
			or[x0-a.B.X0+i] = uint8(mul255(uint32(ar[x0-a.B.X0+i]), uint32(255-v)))
		}
	}
	return o
}

func darkColor(c Color) Color {
	if c.R == 1 && c.G == 1 && c.B == 1 {
		return Color{0xAB / 255.0, 0xAB / 255.0, 0xAB / 255.0, c.A}
	}
	v := math.Max(c.R, math.Max(c.G, c.B))
	if v == 0 {
		return Color{0, 0, 0, c.A}
	}
	m := math.Max(0, (v-0.33)/v)
	return Color{c.R * m, c.G * m, c.B * m, c.A}
}

func lightColor(c Color) Color {
	if c.R == 0 && c.G == 0 && c.B == 0 {
		return Color{0x54 / 255.0, 0x54 / 255.0, 0x54 / 255.0, c.A}
	}
	v := math.Max(c.R, math.Max(c.G, c.B))
	m := math.Min(1, v+0.33) / v
	return Color{math.Min(1, c.R*m), math.Min(1, c.G*m), math.Min(1, c.B*m), c.A}
}

func (p *Painter) border(o render.Border) {
	c := p.c
	m := c.Transform()
	r := p.snap(o.Rect)
	if r.W <= 0 || r.H <= 0 {
		return
	}
	scale := m[0]
	var w [4]float64
	for i := 0; i < 4; i++ {
		st := o.Styles[i]
		if st == "none" || st == "hidden" || st == "" || o.Widths[i] <= 0 || o.Colors[i].A == 0 && false {
			continue
		}
		dw := math.Floor(o.Widths[i] * scale)
		if dw < 1 {
			dw = 1
		}
		w[i] = dw / scale
	}
	if w == [4]float64{} {
		return
	}
	rad := fitRadii(r, o.Radii)
	// side quads (miter lines through the corners)
	x0, y0, x1, y1 := r.X, r.Y, r.X+r.W, r.Y+r.H
	ix0, iy0, ix1, iy1 := x0+w[3], y0+w[0], x1-w[1], y1-w[2]
	quads := [4][]Pt{
		{{x0, y0}, {x1, y0}, {ix1, iy0}, {ix0, iy0}},
		{{x1, y0}, {x1, y1}, {ix1, iy1}, {ix1, iy0}},
		{{x1, y1}, {x0, y1}, {ix0, iy1}, {ix1, iy1}},
		{{x0, y1}, {x0, y0}, {ix0, iy0}, {ix0, iy1}},
	}
	if !rad.IsZero() {
		// extend the miter lines inward so the corner arcs fall inside the
		// adjacent sides' regions
		mr, mw := 0.0, 0.0
		for k := 0; k < 4; k++ {
			mr = math.Max(mr, math.Max(rad[k][0], rad[k][1]))
			mw = math.Max(mw, w[k])
		}
		ext := func(ox, oy, dx, dy float64) Pt {
			m := math.Max(math.Abs(dx), math.Abs(dy))
			if m == 0 {
				return Pt{ox, oy}
			}
			t := (mr + mw) / m
			return Pt{ox + dx*t, oy + dy*t}
		}
		tl := ext(x0, y0, w[3], w[0])
		tr := ext(x1, y0, -w[1], w[0])
		br := ext(x1, y1, -w[1], -w[2])
		bl := ext(x0, y1, w[3], -w[2])
		quads = [4][]Pt{
			{{x0, y0}, {x1, y0}, tr, tl},
			{{x1, y0}, {x1, y1}, br, tr},
			{{x1, y1}, {x0, y1}, bl, br},
			{{x0, y1}, {x0, y0}, tl, bl},
		}
	}
	// uniform solid fast path
	uniform := true
	for i := 1; i < 4; i++ {
		if o.Styles[i] != o.Styles[0] || o.Colors[i] != o.Colors[0] || w[i] != w[0] {
			uniform = false
		}
	}
	if uniform && o.Styles[0] == "solid" {
		ring := p.ring(r, rad, 0, 0, 0, 0, w[3], w[0], w[1], w[2])
		c.draw(p.maskOf(ring, EvenOdd), Solid(col(o.Colors[0])))
		return
	}
	if uniform && !rad.IsZero() && (o.Styles[0] == "dashed" || o.Styles[0] == "dotted") {
		p.dashedRounded(r, rad, w, 0, nil, col(o.Colors[0]), o.Styles[0] == "dotted")
		return
	}
	for i := 0; i < 4; i++ {
		if w[i] == 0 {
			continue
		}
		color := col(o.Colors[i])
		style := o.Styles[i]
		sideMask := func(ring *Path) *Mask {
			rm := p.maskOf(ring, EvenOdd)
			qp := NewPath()
			qp.MoveTo(quads[i][0].X, quads[i][0].Y)
			for _, q := range quads[i][1:] {
				qp.LineTo(q.X, q.Y)
			}
			qp.Close()
			return maskMul(rm, p.maskOf(qp, NonZero))
		}
		// per-side inset amounts for band [a,b] from the outside
		band := func(a, b float64) *Path {
			var oa, ob [4]float64
			for k := 0; k < 4; k++ {
				oa[k], ob[k] = w[k]*a, w[k]*b
			}
			return p.ring(r, rad, oa[3], oa[0], oa[1], oa[2], ob[3], ob[0], ob[1], ob[2])
		}
		fill := func(mk *Mask, cl Color) { c.draw(mk, Solid(cl)) }
		topLeft := i == 0 || i == 3
		switch style {
		case "solid":
			fill(sideMask(band(0, 1)), color)
		case "double":
			if w[i] < 3/scale {
				fill(sideMask(band(0, 1)), color)
			} else {
				fill(sideMask(band(0, 1.0/3)), color)
				fill(sideMask(band(2.0/3, 1)), color)
			}
		case "inset", "outset":
			cl := color
			dark := (style == "inset") == topLeft
			if dark {
				cl = darkColor(color)
			}
			fill(sideMask(band(0, 1)), cl)
		case "groove", "ridge":
			d, l := darkColor(color), color
			if (style == "groove") != topLeft {
				d, l = l, d
			}
			fill(sideMask(band(0, 0.5)), d)
			fill(sideMask(band(0.5, 1)), l)
		case "dashed", "dotted":
			if !rad.IsZero() {
				p.dashedRounded(r, rad, w, i, quads[i], color, style == "dotted")
			} else {
				p.dashedSide(o, r, rad, w, i, quads[i], color, style == "dotted")
			}
		default:
			fill(sideMask(band(0, 1)), color)
		}
	}
}

// ring is the area between the outer inset a and inner inset b of the border box.
func (p *Painter) ring(r render.Rect, rad render.Radii, al, at, ar, ab, bl, bt, br, bb float64) *Path {
	pa := NewPath()
	or, orad := insetRRect(r, rad, al, at, ar, ab)
	pa.AddPath(rrPath(or, orad), Identity)
	ir, irad := insetRRect(r, rad, bl, bt, br, bb)
	if ir.W > 0 && ir.H > 0 {
		pa.AddPath(rrPath(ir, irad), Identity)
	}
	return pa
}

func (p *Painter) dashedSide(o render.Border, r render.Rect, rad render.Radii, w [4]float64, i int, quad []Pt, color Color, dotted bool) {
	c := p.c
	bw := w[i]
	// centreline of the side (straight part between corner radii)
	x0, y0, x1, y1 := r.X, r.Y, r.X+r.W, r.Y+r.H
	var a, b Pt
	switch i {
	case 0:
		a, b = Pt{x0 + rad[0][0], y0 + bw/2}, Pt{x1 - rad[1][0], y0 + bw/2}
	case 1:
		a, b = Pt{x1 - bw/2, y0 + rad[1][1]}, Pt{x1 - bw/2, y1 - rad[2][1]}
	case 2:
		a, b = Pt{x1 - rad[2][0], y1 - bw/2}, Pt{x0 + rad[3][0], y1 - bw/2}
	default:
		a, b = Pt{x0 + bw/2, y1 - rad[3][1]}, Pt{x0 + bw/2, y0 + rad[0][1]}
	}
	// extend to the corners when there is no radius so adjacent sides meet
	if rad[i][0] == 0 && rad[i][1] == 0 {
		switch i {
		case 0:
			a.X = x0
		case 1:
			a.Y = y0
		case 2:
			a.X = x1
		default:
			a.Y = y1
		}
	}
	if rad[(i+1)%4][0] == 0 && rad[(i+1)%4][1] == 0 {
		switch i {
		case 0:
			b.X = x1
		case 1:
			b.Y = y1
		case 2:
			b.X = x0
		default:
			b.Y = y0
		}
	}
	length := math.Hypot(b.X-a.X, b.Y-a.Y)
	if length <= 0 {
		return
	}
	if dotted {
		n := math.Max(1, math.Round((length+bw)/(2*bw)))
		dp := NewPath()
		for i := 0.0; i < n; i++ {
			t := 0.0
			if n > 1 {
				t = (bw/2 + i*(length-bw)/(n-1)) / length
			} else {
				t = 0.5
			}
			dp.Ellipse(a.X+(b.X-a.X)*t, a.Y+(b.Y-a.Y)*t, bw/2, bw/2)
		}
		c.draw(p.maskOf(dp, NonZero), Solid(color))
		return
	}
	dash := 2 * bw
	if bw < 3 {
		dash = 3 * bw
	}
	gap := bw
	n := math.Max(1, math.Round((length+gap)/(dash+gap)))
	if n > 1 {
		gap = (length - n*dash) / (n - 1)
	}
	pa := NewPath()
	pa.MoveTo(a.X, a.Y)
	pa.LineTo(b.X, b.Y)
	ss := StrokeStyle{Width: bw, Cap: CapButt, Join: JoinMiter, MiterLimit: 10, Dash: []float64{dash, gap}}
	mk := StrokeMask(pa, c.Transform(), ss, c.drawBounds())
	// clip to the side's region of the ring
	ring := p.ring(r, rad, 0, 0, 0, 0, w[3], w[0], w[1], w[2])
	rm := p.maskOf(ring, EvenOdd)
	qp := NewPath()
	qp.MoveTo(quad[0].X, quad[0].Y)
	for _, q := range quad[1:] {
		qp.LineTo(q.X, q.Y)
	}
	qp.Close()
	mk = maskMul(mk, maskMul(rm, p.maskOf(qp, NonZero)))
	c.draw(mk, Solid(color))
}

func (p *Painter) text(o render.TextRun) {
	c := p.c
	sh := &Shaped{Size: o.Size, Width: o.Width, Fake: o.Bold, Skew: o.Italic, Lum: textLum(col(o.Color))}
	sh.Glyphs = make([]Glyph, 0, len(o.Glyphs))
	for _, g := range o.Glyphs {
		if g.Face == nil {
			continue
		}
		sh.Glyphs = append(sh.Glyphs, Glyph{R: g.R, X: g.X, Face: g.Face})
	}
	c.Save()
	defer c.Restore()
	if len(sh.Glyphs) > 0 {
		for i := len(o.Shadows) - 1; i >= 0; i-- {
			s := o.Shadows[i]
			if s.Color.A == 0 {
				continue
			}
			mk := c.textMask(sh, o.X+s.DX, o.Y+s.DY, 1)
			if mk == nil {
				continue
			}
			sig := s.Blur / 2 * c.Transform()[0]
			if sig > 0 {
				mk = BlurMask(mk, sig, sig, 0, 0, c.ClipBounds())
			}
			c.draw(mk, Solid(col(s.Color)))
		}
		if o.Color.A > 0 {
			c.draw(c.textMask(sh, o.X, o.Y, 1), Solid(col(o.Color)))
		}
		if o.Stroke > 0 {
			if tp := c.textPath(sh, o.X, o.Y, 1); tp != nil {
				c.SetStroke(Brush{Color: col(o.StrokeColor)})
				c.SetStrokeStyle(StrokeStyle{Width: o.Stroke, Join: JoinMiter, MiterLimit: 4})
				c.StrokePath(tp)
			}
		}
	}
	if !(o.Underline || o.Overline || o.LineThrough) || o.Width <= 0 {
		return
	}
	dc := col(o.DecoColor)
	if o.DecoColor.A == 0 {
		dc = col(o.Color)
	}
	// Geometry measured from Chromium (Linux, Liberation/DejaVu fonts):
	// thickness floor(size/10) (min 1); underline top = baseline +
	// max(ceil(size/20), ceil(thickness/2)); overline sits on the ascent;
	// line-through centre follows a size-dependent table.
	th := math.Max(1, math.Floor(o.Size/10))
	autoTh := th
	if o.DecoThick > 0 {
		th = math.Max(1, math.Round(o.DecoThick))
	}
	asc := math.Round(o.Size * 0.9)
	for _, g := range o.Glyphs {
		if g.Face != nil {
			asc = math.Round(g.Face.Metrics(o.Size).Ascent)
			break
		}
	}
	line := func(y float64, dir float64) {
		switch o.DecoStyle {
		case "double":
			p.decoRect(o.X, y, o.Width, th, dc)
			p.decoRect(o.X, y+dir*(th+1), o.Width, th, dc)
		case "dotted", "dashed":
			dash, gap := th, th
			if o.DecoStyle == "dashed" {
				dash, gap = 2*th, th
				if th < 3 {
					dash, gap = 3*th, 2*th
				}
			}
			if o.Width <= dash*2 {
				p.decoRect(o.X, y, o.Width, th, dc)
				return
			}
			gap = selectBestDashGap(o.Width, dash, gap, false)
			for x := 0.0; x < o.Width-1e-6; x += dash + gap {
				p.decoRect(o.X+x, y, math.Min(dash, o.Width-x), th, dc)
			}
		case "wavy":
			amp := (th + 3) / 2
			per := 4*th + 3
			cy := y + amp + th/2
			pa := NewPath()
			step := 0.5
			for x := 0.0; x <= o.Width+step; x += step {
				xx := math.Min(x, o.Width)
				yy := cy + amp*math.Sin(2*math.Pi*xx/per)
				if x == 0 {
					pa.MoveTo(o.X+xx, yy)
				} else {
					pa.LineTo(o.X+xx, yy)
				}
			}
			c.SetStroke(Brush{Color: dc})
			c.SetStrokeStyle(StrokeStyle{Width: th, Join: JoinRound})
			c.Save()
			c.ClipRect(o.X, y-1, o.Width, 2*amp+th+2)
			c.StrokePath(pa)
			c.Restore()
		default:
			p.decoRect(o.X, y, o.Width, th, dc)
		}
	}
	if o.Underline {
		off := math.Max(math.Ceil(o.Size/20), math.Ceil(th/2))
		if o.UnderOffset != 0 {
			off = o.UnderOffset
		}
		line(o.Y+off, 1)
	}
	if o.Overline {
		line(o.Y-asc-th, -1)
	}
	if o.LineThrough {
		// centre above the baseline (measured table, linearly interpolated)
		ctr := lineThroughCenter(o.Size, autoTh)
		line(o.Y-ctr-th/2, 1)
	}
}

// lineThroughCenter is the distance of the strike-through centre above the
// baseline for the auto thickness (measured: top offsets for 1..64px).
func lineThroughCenter(size, th float64) float64 {
	tbl := [][2]float64{{10, 3}, {12, 4}, {14, 5}, {16, 5}, {18, 6}, {20, 7}, {24, 9}, {28, 10}, {32, 11}, {40, 14}, {48, 17}, {64, 23}}
	top := 0.0
	switch {
	case size <= tbl[0][0]:
		top = tbl[0][1] * size / tbl[0][0]
	case size >= tbl[len(tbl)-1][0]:
		top = tbl[len(tbl)-1][1] + (size-64)*0.365
	default:
		for k := 1; k < len(tbl); k++ {
			if size <= tbl[k][0] {
				a, b := tbl[k-1], tbl[k]
				top = a[1] + (b[1]-a[1])*(size-a[0])/(b[0]-a[0])
				break
			}
		}
	}
	top = math.Round(top)
	return top - th/2
}

func (p *Painter) decoRect(x, y, w, h float64, cl Color) {
	r := p.snap(render.Rect{X: x, Y: y, W: w, H: h})
	if r.H == 0 {
		r.H = 1 / p.c.Transform()[3]
	}
	p.c.SetFill(Brush{Color: cl})
	p.c.FillRect(r.X, r.Y, r.W, r.H)
}

func (p *Painter) image(o render.Image) {
	if o.Img == nil || o.Dst.W <= 0 || o.Dst.H <= 0 {
		return
	}
	c := p.c
	img := FromImage(o.Img)
	if img.W() == 0 || img.H() == 0 {
		return
	}
	c.Save()
	defer c.Restore()
	if o.Smooth {
		c.SetSmoothing(true, QualityLow)
	} else {
		c.SetSmoothing(false, QualityLow)
	}
	dst := p.snap(o.Dst)
	if !o.Radii.IsZero() {
		c.ClipPath(rrPath(dst, fitRadii(dst, o.Radii)), NonZero)
	} else {
		c.ClipRect(dst.X, dst.Y, dst.W, dst.H)
	}
	if o.Tile != nil {
		t := o.Tile
		sw, sh := t.Size[0], t.Size[1]
		if sw <= 0 || sh <= 0 {
			return
		}
		stepX, stepY := t.Step[0], t.Step[1]
		if stepX <= 0 {
			stepX = sw
		}
		if stepY <= 0 {
			stepY = sh
		}
		xs := tileStarts(t.Origin[0], stepX, dst.X, dst.X+dst.W, t.RepeatX)
		ys := tileStarts(t.Origin[1], stepY, dst.Y, dst.Y+dst.H, t.RepeatY)
		sx0, sy0 := 0.0, 0.0
		sw0, sh0 := float64(img.W()), float64(img.H())
		if o.Src.W > 0 && o.Src.H > 0 {
			sx0, sy0, sw0, sh0 = o.Src.X, o.Src.Y, o.Src.W, o.Src.H
		}
		for _, y := range ys {
			for _, x := range xs {
				// snap tile edges to device pixels like Chromium
				r := p.snap(render.Rect{X: x, Y: y, W: sw, H: sh})
				c.DrawImage(img, sx0, sy0, sw0, sh0, r.X, r.Y, r.W, r.H)
			}
		}
		return
	}
	sx, sy, sw, sh := 0.0, 0.0, float64(img.W()), float64(img.H())
	if o.Src.W > 0 && o.Src.H > 0 {
		sx, sy, sw, sh = o.Src.X, o.Src.Y, o.Src.W, o.Src.H
	}
	c.DrawImage(img, sx, sy, sw, sh, dst.X, dst.Y, dst.W, dst.H)
}

func tileStarts(origin, step, lo, hi float64, repeat bool) []float64 {
	if !repeat {
		return []float64{origin}
	}
	n := math.Floor((lo - origin) / step)
	var out []float64
	for x := origin + n*step; x < hi && len(out) < 20000; x += step {
		out = append(out, x)
	}
	return out
}

func (p *Painter) gradient(o render.Gradient) {
	c := p.c
	if len(o.Stops) == 0 || o.Rect.W <= 0 || o.Rect.H <= 0 {
		return
	}
	r := p.snap(o.Rect)
	c.Save()
	defer c.Restore()
	geom := o.Rect
	if o.Tile != nil {
		geom = render.Rect{X: o.Tile.Origin[0], Y: o.Tile.Origin[1], W: o.Tile.Size[0], H: o.Tile.Size[1]}
	}
	def := GradientDef{Premul: true, Repeat: o.Repeating}
	for _, s := range o.Stops {
		def.Stops = append(def.Stops, GradStop{Pos: s.Pos, Color: col(s.Color)})
	}
	m := Identity
	switch o.Kind {
	case "radial":
		def.Kind = RadialGrad
		def.X0, def.Y0 = geom.X+o.CX, geom.Y+o.CY
		def.X1, def.Y1 = def.X0, def.Y0
		def.R0, def.R1 = 0, math.Max(o.RX, 1e-6)
		ry := math.Max(o.RY, 1e-6)
		m = Translate(def.X0, def.Y0).Mul(Scale(1, ry/def.R1)).Mul(Translate(-def.X0, -def.Y0))
	case "conic":
		def.Kind = ConicGrad
		def.X0, def.Y0 = geom.X+o.CX, geom.Y+o.CY
		def.R0 = (o.FromAngle - 90) * math.Pi / 180
	default:
		def.Kind = LinearGrad
		a := o.Angle * math.Pi / 180
		sn, cs := math.Sincos(a)
		l := math.Abs(geom.W*sn) + math.Abs(geom.H*cs)
		cx, cy := geom.X+geom.W/2, geom.Y+geom.H/2
		def.X0, def.Y0 = cx-sn*l/2, cy+cs*l/2
		def.X1, def.Y1 = cx+sn*l/2, cy-cs*l/2
	}
	paintFn := func(target Rect) {}
	_ = paintFn
	ctm := c.Transform().Mul(m)
	sh := NewGradientShader(def, ctm, 1)
	var path *Path
	if o.Radii.IsZero() {
		path = NewPath()
		path.Rect(r.X, r.Y, r.W, r.H)
	} else {
		path = rrPath(r, fitRadii(r, o.Radii))
	}
	if o.Tile != nil {
		// render one tile into a surface and repeat it
		t := o.Tile
		tw, th := int(math.Ceil(t.Size[0]*ctm[0]/m[0])), int(math.Ceil(t.Size[1]*c.Transform()[3]))
		if tw > 0 && th > 0 && tw*th < 1<<24 {
			sc := c.Transform()
			tc := NewContext(tw, th)
			tdef := def
			tdef.X0 -= geom.X
			tdef.Y0 -= geom.Y
			tdef.X1 -= geom.X
			tdef.Y1 -= geom.Y
			tm := Scale(float64(tw)/t.Size[0], float64(th)/t.Size[1])
			tsh := NewGradientShader(tdef, tm.Mul(m), 1)
			tc.FillRectShader(0, 0, float64(tw), float64(th), tsh)
			pat := NewImageShader(tc.Surface(), sc.Mul(Translate(t.Origin[0], t.Origin[1])).Mul(Scale(t.Size[0]/float64(tw), t.Size[1]/float64(th))), o.Tile.RepeatX, o.Tile.RepeatY, FilterBilinear, 1)
			c.FillPathShader(path, NonZero, pat)
			return
		}
	}
	c.FillPathShader(path, NonZero, sh)
}

func (p *Painter) boxShadow(o render.BoxShadow) {
	c := p.c
	if o.Color.A == 0 {
		return
	}
	scale := c.Transform()[0]
	box := p.snap(o.Rect)
	rad := fitRadii(box, o.Radii)
	sigma := math.Max(0, o.Blur) / 2 * scale
	clipb := c.ClipBounds()
	if !o.Inset {
		sr := render.Rect{X: box.X - o.Spread + o.DX, Y: box.Y - o.Spread + o.DY, W: box.W + 2*o.Spread, H: box.H + 2*o.Spread}
		if sr.W <= 0 || sr.H <= 0 {
			return
		}
		var srad render.Radii
		for i := range rad {
			for k := 0; k < 2; k++ {
				if rad[i][k] > 0 {
					srad[i][k] = math.Max(0, rad[i][k]+o.Spread)
				}
			}
		}
		pad := int(math.Ceil(sigma*3)) + 1
		bb := clipb
		lim := IRect{bb.X0 - pad, bb.Y0 - pad, bb.X1 + pad, bb.Y1 + pad}
		mk := FillMask(rrPath(sr, srad), c.Transform(), NonZero, lim)
		if mk == nil {
			return
		}
		if sigma > 0 {
			mk = BlurMask(mk, sigma, sigma, 0, 0, clipb)
		}
		if mk == nil {
			return
		}
		inner := FillMask(rrPath(box, rad), c.Transform(), NonZero, mk.B)
		mk = maskSub(mk, inner)
		c.draw(mk, Solid(col(o.Color)))
		return
	}
	// inset: shadow of the area outside the (offset, shrunk) hole, clipped to the box
	hole := render.Rect{X: box.X + o.Spread + o.DX, Y: box.Y + o.Spread + o.DY, W: box.W - 2*o.Spread, H: box.H - 2*o.Spread}
	var hrad render.Radii
	for i := range rad {
		for k := 0; k < 2; k++ {
			if rad[i][k] > 0 {
				hrad[i][k] = math.Max(0, rad[i][k]-o.Spread)
			}
		}
	}
	boxm := FillMask(rrPath(box, rad), c.Transform(), NonZero, clipb)
	if boxm == nil {
		return
	}
	pad := int(math.Ceil(sigma*3)) + 1
	area := IRect{boxm.B.X0 - pad, boxm.B.Y0 - pad, boxm.B.X1 + pad, boxm.B.Y1 + pad}
	full := newMask(area)
	for i := range full.A {
		full.A[i] = 255
	}
	if hole.W > 0 && hole.H > 0 {
		hm := FillMask(rrPath(hole, hrad), c.Transform(), NonZero, area)
		full = maskSub(full, hm)
	}
	if sigma > 0 {
		full = BlurMask(full, sigma, sigma, 0, 0, boxm.B)
	}
	if full == nil {
		return
	}
	c.draw(maskMul(full, boxm), Solid(col(o.Color)))
}

func (p *Painter) bitmap(o render.Bitmap) {
	if p.env.Bitmap == nil || o.Node == nil {
		return
	}
	img := p.env.Bitmap(o.Node)
	if img == nil {
		return
	}
	s := FromImage(img)
	if s.W() == 0 || s.H() == 0 {
		return
	}
	c := p.c
	c.Save()
	defer c.Restore()
	dst := p.snap(o.Dst)
	if !o.Radii.IsZero() {
		c.ClipPath(rrPath(dst, fitRadii(dst, o.Radii)), NonZero)
	} else {
		c.ClipRect(dst.X, dst.Y, dst.W, dst.H)
	}
	iw, ih := float64(s.W()), float64(s.H())
	w, h := iw, ih
	switch o.Fit {
	case "contain":
		k := math.Min(dst.W/iw, dst.H/ih)
		w, h = iw*k, ih*k
	case "cover":
		k := math.Max(dst.W/iw, dst.H/ih)
		w, h = iw*k, ih*k
	case "none":
	case "scale-down":
		k := math.Min(1, math.Min(dst.W/iw, dst.H/ih))
		w, h = iw*k, ih*k
	default:
		w, h = dst.W, dst.H
	}
	px, py := 0.5, 0.5
	if o.Pos != [2]float64{} {
		px, py = o.Pos[0], o.Pos[1]
	}
	x := dst.X + (dst.W-w)*px
	y := dst.Y + (dst.H-h)*py
	c.SetSmoothing(true, QualityLow)
	c.DrawImage(s, 0, 0, iw, ih, x, y, w, h)
}
