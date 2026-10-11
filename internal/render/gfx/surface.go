package gfx

import (
	"image"
	"image/color"
	"image/draw"
	"math"

	"github.com/sweefbrowser/sweef/internal/render/font"
	"golang.org/x/image/vector"
)

// Surface is a premultiplied RGBA pixel buffer with a clip stack. All draw
// calls take geometry in user space plus a Matrix to device space.
//
// The clip is a coverage mask (nil = unclipped) intersected with each new
// clip; PushClip/PopClip nest. Draw calls composite with an operator and a
// global alpha.
type Surface struct {
	Img *image.RGBA
	// clip is the current coverage mask in device space; nil = no clip.
	clip  *image.Alpha
	stack []*image.Alpha
}

// NewSurface allocates a transparent w x h surface.
func NewSurface(w, h int) *Surface {
	if w < 0 {
		w = 0
	}
	if h < 0 {
		h = 0
	}
	return &Surface{Img: image.NewRGBA(image.Rect(0, 0, w, h))}
}

// Width and Height of the surface in device px.
func (s *Surface) Width() int  { return s.Img.Bounds().Dx() }
func (s *Surface) Height() int { return s.Img.Bounds().Dy() }

// Clear fills the whole surface (ignoring clip) with c.
func (s *Surface) Clear(c Color) {
	r, g, b, a := c.Premul()
	draw.Draw(s.Img, s.Img.Bounds(), image.NewUniform(color.RGBA64{uint16(r), uint16(g), uint16(b), uint16(a)}), image.Point{}, draw.Src)
}

// DrawOptions are the per-draw compositing parameters.
type DrawOptions struct {
	Op     CompositeOp
	Alpha  float64 // global alpha; 0 is treated as 1 only when Opaque... use 1 for opaque
	Shadow *Shadow
	// Smoothing selects bilinear filtering for images (nearest when false).
	Smoothing bool
}

// DefaultOpts is opaque source-over with smoothing.
var DefaultOpts = DrawOptions{Op: SrcOver, Alpha: 1, Smoothing: true}

// FillPath fills path p (user space) transformed by m with paint.
func (s *Surface) FillPath(p *Path, m Matrix, rule FillRule, paint Paint, o DrawOptions) {
	mask := s.Coverage(p, m, rule)
	if mask == nil {
		return
	}
	s.composite(mask, paint, m, o)
}

// StrokePath strokes p with st, then fills the outline (nonzero).
func (s *Surface) StrokePath(p *Path, m Matrix, st StrokeStyle, paint Paint, o DrawOptions) {
	outline := Stroke(p, st)
	s.FillPath(outline, m, NonZero, paint, o)
}

// FillRect fills an axis-aligned rectangle (user space) via m.
func (s *Surface) FillRect(r Rect, m Matrix, paint Paint, o DrawOptions) {
	var p Path
	p.Rect(r.X, r.Y, r.W, r.H)
	s.FillPath(&p, m, NonZero, paint, o)
}

// Coverage rasterises p through m into an anti-aliased coverage mask the
// size of the surface, already intersected with the clip. Nil when empty.
func (s *Surface) Coverage(p *Path, m Matrix, rule FillRule) *image.Alpha {
	w, h := s.Width(), s.Height()
	if w == 0 || h == 0 || p.Empty() {
		return nil
	}
	mask := rasterize(p, m, rule, w, h)
	if s.clip != nil {
		intersectMask(mask, s.clip)
	}
	return mask
}

// PushClip intersects the clip with path p (through m).
func (s *Surface) PushClip(p *Path, m Matrix, rule FillRule) {
	s.stack = append(s.stack, s.clip)
	w, h := s.Width(), s.Height()
	mask := rasterize(p, m, rule, w, h)
	if s.clip != nil {
		intersectMask(mask, s.clip)
	}
	s.clip = mask
}

// PushClipRect is PushClip for a device-space rectangle (fast path).
func (s *Surface) PushClipRect(r Rect) {
	var p Path
	p.Rect(r.X, r.Y, r.W, r.H)
	s.PushClip(&p, Identity, NonZero)
}

// PopClip restores the clip saved by the matching PushClip.
func (s *Surface) PopClip() {
	if n := len(s.stack); n > 0 {
		s.clip = s.stack[n-1]
		s.stack = s.stack[:n-1]
	}
}

// ClipDepth is the number of pushed clips (canvas save/restore uses it).
func (s *Surface) ClipDepth() int { return len(s.stack) }

// ClipMask returns the current clip mask (nil = unclipped).
func (s *Surface) ClipMask() *image.Alpha { return s.clip }

// DrawImage draws img's src rectangle into dst (user space) through m.
func (s *Surface) DrawImage(img image.Image, src, dst Rect, m Matrix, o DrawOptions) {
	if img == nil || src.Empty() || dst.Empty() {
		return
	}
	// Map dst->device; the paint maps device->image via a pattern matrix.
	sx, sy := dst.W/src.W, dst.H/src.H
	pm := Translate(dst.X, dst.Y).Mul(Scale(sx, sy)).Mul(Translate(-src.X, -src.Y))
	var p Path
	p.Rect(dst.X, dst.Y, dst.W, dst.H)
	b := img.Bounds()
	paint := Paint{Kind: PaintPattern, Image: img, Repeat: NoRepeat, Matrix: pm, Smoothing: o.Smoothing}
	_ = b
	s.FillPath(&p, m, NonZero, paint, o)
}

// GlyphRun is positioned text: glyphs from one shaping call, drawn with the
// pen starting at Origin (user space, on the baseline).
type GlyphRun struct {
	Glyphs []font.Glyph
	Origin Point
}

// DrawGlyphs draws a glyph run with paint. Under a pure translate/scale
// transform glyph masks come from the font cache at subpixel phases; other
// transforms rasterise outlines through the matrix.
func (s *Surface) DrawGlyphs(run GlyphRun, m Matrix, paint Paint, o DrawOptions) {
	w, h := s.Width(), s.Height()
	if w == 0 || h == 0 || len(run.Glyphs) == 0 {
		return
	}
	mask := image.NewAlpha(image.Rect(0, 0, w, h))
	any := false
	if m[1] == 0 && m[2] == 0 && m[0] > 0 && math.Abs(m[0]-m[3]) < 1e-9 {
		sc := m[0]
		for _, g := range run.Glyphs {
			if g.Face == nil || g.ID == 0 && g.Rune != 0 && !g.Face.HasRune(g.Rune) && g.Advance == 0 {
				continue
			}
			px := (run.Origin.X+g.X)*sc + m[4]
			py := run.Origin.Y*sc + m[5]
			ix := math.Floor(px)
			phase := int((px - ix) * font.SubpixelSteps)
			gm := g.Face.GlyphMask(g.ID, phase, sc)
			if gm == nil {
				continue
			}
			iy := int(math.Round(py))
			r := gm.Alpha.Bounds().Add(image.Pt(int(ix)+gm.DX, iy+gm.DY))
			draw.DrawMask(mask, r, image.Opaque, image.Point{}, gm.Alpha, image.Point{}, draw.Over)
			any = true
		}
	} else {
		var p Path
		for _, g := range run.Glyphs {
			if g.Face == nil {
				continue
			}
			ox, oy := run.Origin.X+g.X, run.Origin.Y
			for _, sg := range g.Face.Outline(g.ID) {
				switch sg.Op {
				case font.MoveTo:
					p.MoveTo(ox+sg.Pts[0][0], oy+sg.Pts[0][1])
				case font.LineTo:
					p.LineTo(ox+sg.Pts[0][0], oy+sg.Pts[0][1])
				case font.QuadTo:
					p.QuadTo(ox+sg.Pts[0][0], oy+sg.Pts[0][1], ox+sg.Pts[1][0], oy+sg.Pts[1][1])
				case font.CubeTo:
					p.CubicTo(ox+sg.Pts[0][0], oy+sg.Pts[0][1], ox+sg.Pts[1][0], oy+sg.Pts[1][1], ox+sg.Pts[2][0], oy+sg.Pts[2][1])
				}
			}
		}
		if mk := rasterize(&p, m, NonZero, w, h); mk != nil {
			mask, any = mk, true
		}
	}
	if !any {
		return
	}
	if s.clip != nil {
		intersectMask(mask, s.clip)
	}
	s.composite(mask, paint, m, o)
}

// ---- internals (baseline implementation; see raster.go for the full one)

func rasterize(p *Path, m Matrix, rule FillRule, w, h int) *image.Alpha {
	r := vector.NewRasterizer(w, h)
	tp := p.Transform(m)
	open := false
	for _, c := range tp.Cmds {
		switch c.Op {
		case OpMove:
			if open {
				r.ClosePath()
			}
			r.MoveTo(float32(c.Pts[0].X), float32(c.Pts[0].Y))
			open = true
		case OpLine:
			r.LineTo(float32(c.Pts[0].X), float32(c.Pts[0].Y))
		case OpQuad:
			r.QuadTo(float32(c.Pts[0].X), float32(c.Pts[0].Y), float32(c.Pts[1].X), float32(c.Pts[1].Y))
		case OpCubic:
			r.CubeTo(float32(c.Pts[0].X), float32(c.Pts[0].Y), float32(c.Pts[1].X), float32(c.Pts[1].Y), float32(c.Pts[2].X), float32(c.Pts[2].Y))
		case OpClose:
			r.ClosePath()
			open = false
		}
	}
	if open {
		r.ClosePath()
	}
	mask := image.NewAlpha(image.Rect(0, 0, w, h))
	r.Draw(mask, mask.Bounds(), image.Opaque, image.Point{})
	return mask
}

func intersectMask(dst, clip *image.Alpha) {
	for i := range dst.Pix {
		dst.Pix[i] = uint8((uint32(dst.Pix[i])*uint32(clip.Pix[i]) + 127) / 255)
	}
}

// composite blends paint through coverage mask onto the surface. The
// baseline handles solid colour and patterns with source-over; raster.go's
// full implementation replaces it.
func (s *Surface) composite(mask *image.Alpha, paint Paint, m Matrix, o DrawOptions) {
	alpha := o.Alpha
	if alpha <= 0 {
		return
	}
	if paint.Kind == PaintSolid {
		r, g, b, a := paint.Color.WithAlpha(alpha).Premul()
		src := image.NewUniform(color.RGBA64{uint16(r), uint16(g), uint16(b), uint16(a)})
		draw.DrawMask(s.Img, s.Img.Bounds(), src, image.Point{}, mask, image.Point{}, draw.Over)
		return
	}
	shade := Shader(paint, m)
	if shade == nil {
		return
	}
	b := s.Img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			cov := mask.Pix[(y-b.Min.Y)*mask.Stride+(x-b.Min.X)]
			if cov == 0 {
				continue
			}
			c := shade(float64(x)+0.5, float64(y)+0.5)
			k := float64(cov) / 255 * alpha
			i := s.Img.PixOffset(x, y)
			sa := c.A * k
			da := float64(s.Img.Pix[i+3]) / 255
			for ch, v := range [3]float64{c.R, c.G, c.B} {
				d := float64(s.Img.Pix[i+ch]) / 255
				s.Img.Pix[i+ch] = uint8(math.Round((v*sa + d*(1-sa)) * 255))
			}
			s.Img.Pix[i+3] = uint8(math.Round((sa + da*(1-sa)) * 255))
		}
	}
}

// Shader returns a function giving the straight-alpha colour of paint at a
// device pixel centre, given the draw's user->device matrix.
func Shader(paint Paint, m Matrix) func(x, y float64) Color {
	inv, ok := m.Invert()
	if !ok {
		return nil
	}
	pm := paint.Matrix
	if pm == (Matrix{}) {
		pm = Identity
	}
	pinv, ok := pm.Invert()
	if !ok {
		return nil
	}
	toPaint := pinv.Mul(inv)
	switch paint.Kind {
	case PaintSolid:
		c := paint.Color
		return func(float64, float64) Color { return c }
	case PaintPattern:
		img := paint.Image
		if img == nil {
			return nil
		}
		b := img.Bounds()
		return func(x, y float64) Color {
			p := toPaint.Apply(Point{x, y})
			ix, iy := int(math.Floor(p.X)), int(math.Floor(p.Y))
			w, h := b.Dx(), b.Dy()
			if paint.Repeat == RepeatBoth || paint.Repeat == RepeatX {
				ix = ((ix % w) + w) % w
			}
			if paint.Repeat == RepeatBoth || paint.Repeat == RepeatY {
				iy = ((iy % h) + h) % h
			}
			if ix < 0 || iy < 0 || ix >= w || iy >= h {
				return Transparent
			}
			r, g, bb, a := img.At(b.Min.X+ix, b.Min.Y+iy).RGBA()
			if a == 0 {
				return Transparent
			}
			return Color{float64(r) / float64(a), float64(g) / float64(a), float64(bb) / float64(a), float64(a) / 65535}
		}
	}
	return func(x, y float64) Color {
		p := toPaint.Apply(Point{x, y})
		return gradientAt(paint, p)
	}
}

// gradientAt evaluates linear/radial/conic gradients (baseline version).
func gradientAt(paint Paint, p Point) Color {
	var t float64
	switch paint.Kind {
	case PaintLinear:
		dx, dy := paint.X1-paint.X0, paint.Y1-paint.Y0
		l2 := dx*dx + dy*dy
		if l2 == 0 {
			return Transparent
		}
		t = ((p.X-paint.X0)*dx + (p.Y-paint.Y0)*dy) / l2
	case PaintRadial:
		d := math.Hypot(p.X-paint.X1, p.Y-paint.Y1)
		if paint.R1 == paint.R0 {
			return Transparent
		}
		t = (d - paint.R0) / (paint.R1 - paint.R0)
	case PaintConic:
		a := math.Atan2(p.Y-paint.Y0, p.X-paint.X0) - paint.Angle
		t = math.Mod(a/(2*math.Pi)+2, 1)
	}
	return StopColor(paint.Stops, t, paint.RepeatingGradient)
}

// StopColor interpolates a stop list at t (premultiplied interpolation, as
// CSS and canvas specify).
func StopColor(stops []GradientStop, t float64, repeating bool) Color {
	if len(stops) == 0 {
		return Transparent
	}
	if repeating {
		a, b := stops[0].Offset, stops[len(stops)-1].Offset
		if b > a {
			t = a + math.Mod(math.Mod(t-a, b-a)+(b-a), b-a)
		}
	}
	if t <= stops[0].Offset {
		return stops[0].Color
	}
	for i := 1; i < len(stops); i++ {
		if t <= stops[i].Offset {
			s0, s1 := stops[i-1], stops[i]
			span := s1.Offset - s0.Offset
			f := 0.0
			if span > 0 {
				f = (t - s0.Offset) / span
			}
			a := s0.Color.A + (s1.Color.A-s0.Color.A)*f
			if a == 0 {
				return Transparent
			}
			mix := func(c0, c1 float64) float64 {
				return (c0*s0.Color.A + (c1*s1.Color.A-c0*s0.Color.A)*f) / a
			}
			return Color{mix(s0.Color.R, s1.Color.R), mix(s0.Color.G, s1.Color.G), mix(s0.Color.B, s1.Color.B), a}
		}
	}
	return stops[len(stops)-1].Color
}

// Stroke converts a path into its stroke outline (filled nonzero). The
// baseline emits per-segment quads with no joins or dashes; stroke.go's
// full implementation replaces it.
func Stroke(p *Path, st StrokeStyle) *Path {
	out := &Path{}
	hw := st.Width / 2
	if hw <= 0 {
		return out
	}
	flat := Flatten(p, 0.25)
	for _, sub := range flat {
		for i := 0; i+1 < len(sub.Pts); i++ {
			a, b := sub.Pts[i], sub.Pts[i+1]
			dx, dy := b.X-a.X, b.Y-a.Y
			l := math.Hypot(dx, dy)
			if l == 0 {
				continue
			}
			nx, ny := -dy/l*hw, dx/l*hw
			out.MoveTo(a.X+nx, a.Y+ny)
			out.LineTo(b.X+nx, b.Y+ny)
			out.LineTo(b.X-nx, b.Y-ny)
			out.LineTo(a.X-nx, a.Y-ny)
			out.Close()
		}
	}
	return out
}

// Polyline is a flattened subpath.
type Polyline struct {
	Pts    []Point
	Closed bool
}

// Flatten converts curves to line segments with the given tolerance (px).
func Flatten(p *Path, tol float64) []Polyline {
	var out []Polyline
	var cur *Polyline
	last := Point{}
	for _, c := range p.Cmds {
		switch c.Op {
		case OpMove:
			out = append(out, Polyline{Pts: []Point{c.Pts[0]}})
			cur = &out[len(out)-1]
			last = c.Pts[0]
		case OpLine:
			if cur == nil {
				out = append(out, Polyline{Pts: []Point{last}})
				cur = &out[len(out)-1]
			}
			cur.Pts = append(cur.Pts, c.Pts[0])
			last = c.Pts[0]
		case OpQuad:
			n := segs(math.Hypot(c.Pts[0].X-last.X, c.Pts[0].Y-last.Y)+math.Hypot(c.Pts[1].X-c.Pts[0].X, c.Pts[1].Y-c.Pts[0].Y), tol)
			for i := 1; i <= n; i++ {
				t := float64(i) / float64(n)
				u := 1 - t
				cur.Pts = append(cur.Pts, Point{u*u*last.X + 2*u*t*c.Pts[0].X + t*t*c.Pts[1].X, u*u*last.Y + 2*u*t*c.Pts[0].Y + t*t*c.Pts[1].Y})
			}
			last = c.Pts[1]
		case OpCubic:
			l := math.Hypot(c.Pts[0].X-last.X, c.Pts[0].Y-last.Y) + math.Hypot(c.Pts[1].X-c.Pts[0].X, c.Pts[1].Y-c.Pts[0].Y) + math.Hypot(c.Pts[2].X-c.Pts[1].X, c.Pts[2].Y-c.Pts[1].Y)
			n := segs(l, tol)
			for i := 1; i <= n; i++ {
				t := float64(i) / float64(n)
				u := 1 - t
				x := u*u*u*last.X + 3*u*u*t*c.Pts[0].X + 3*u*t*t*c.Pts[1].X + t*t*t*c.Pts[2].X
				y := u*u*u*last.Y + 3*u*u*t*c.Pts[0].Y + 3*u*t*t*c.Pts[1].Y + t*t*t*c.Pts[2].Y
				cur.Pts = append(cur.Pts, Point{x, y})
			}
			last = c.Pts[2]
		case OpClose:
			if cur != nil {
				cur.Closed = true
			}
		}
	}
	return out
}

func segs(l, tol float64) int {
	n := int(math.Ceil(math.Sqrt(l / tol)))
	if n < 1 {
		n = 1
	}
	if n > 200 {
		n = 200
	}
	return n
}
