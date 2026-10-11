package gfx

// replay is the baseline display list rasteriser: rectangles, solid
// borders, text, images, clips and transforms. Layers are composited with
// opacity only. The full implementation (blend modes, filters, shadows,
// styled borders) replaces this file's internals.
func replay(d *DisplayList, w, h int, sx, sy, dpr float64) *Surface {
	s := NewSurface(w, h)
	if !d.Background.IsTransparent() {
		s.Clear(d.Background)
	}
	base := Scale(dpr, dpr).Mul(Translate(-sx, -sy))
	mstack := []Matrix{base}
	cur := func() Matrix { return mstack[len(mstack)-1] }
	type layer struct {
		parent *Surface
		item   *LayerItem
	}
	var layers []layer
	for _, it := range d.Items {
		m := cur()
		switch it.Kind {
		case KindRect:
			var p Path
			p.RoundRect(it.Rect.Rect, it.Rect.Radii)
			s.FillPath(&p, m, NonZero, it.Rect.Paint, DefaultOpts)
		case KindBorder:
			b := it.Border
			var outer, inner Path
			outer.RoundRect(b.Rect, b.Radii)
			in := b.Rect.Inset(b.Widths[0], b.Widths[1], b.Widths[2], b.Widths[3])
			inner.RoundRect(in, InnerRadii(b.Radii, b.Widths))
			ring := &Path{}
			ring.Append(&outer, Identity)
			ring.Append(&inner, Identity)
			c := b.Colors[0]
			for i := 0; i < 4; i++ {
				if b.Widths[i] > 0 && b.Styles[i] != BorderNone && b.Styles[i] != BorderHidden {
					c = b.Colors[i]
					break
				}
			}
			s.FillPath(ring, m, EvenOdd, SolidPaint(c), DefaultOpts)
		case KindText:
			p := SolidPaint(it.Text.Color)
			if it.Text.Paint != nil {
				p = *it.Text.Paint
			}
			s.DrawGlyphs(it.Text.Run, m, p, DefaultOpts)
		case KindImage:
			im := it.Image
			src := im.Src
			if src.Empty() && im.Image != nil {
				b := im.Image.Bounds()
				src = Rect{float64(b.Min.X), float64(b.Min.Y), float64(b.Dx()), float64(b.Dy())}
			}
			o := DefaultOpts
			o.Smoothing = im.Rendering != RenderPixelated
			s.DrawImage(im.Image, src, im.Dst, m, o)
		case KindPath:
			if it.Path.Stroke != nil {
				s.StrokePath(it.Path.Path, m, *it.Path.Stroke, it.Path.Paint, DefaultOpts)
			} else {
				s.FillPath(it.Path.Path, m, it.Path.Rule, it.Path.Paint, DefaultOpts)
			}
		case KindPushClip:
			c := it.Clip
			if c.Path != nil {
				s.PushClip(c.Path, m, c.Rule)
			} else {
				var p Path
				p.RoundRect(c.Rect, c.Radii)
				s.PushClip(&p, m, NonZero)
			}
		case KindPopClip:
			s.PopClip()
		case KindPushTransform:
			mstack = append(mstack, m.Mul(it.Xform.Matrix))
		case KindPopTransform:
			if len(mstack) > 1 {
				mstack = mstack[:len(mstack)-1]
			}
		case KindPushLayer:
			ns := NewSurface(w, h)
			ns.clip = s.clip
			layers = append(layers, layer{s, it.Layer})
			s = ns
		case KindPopLayer:
			if n := len(layers); n > 0 {
				l := layers[n-1]
				layers = layers[:n-1]
				src := s
				s = l.parent
				o := DefaultOpts
				o.Alpha = l.item.Opacity
				o.Op = l.item.Blend
				s.DrawImage(src.Img, Rect{0, 0, float64(w), float64(h)}, Rect{0, 0, float64(w), float64(h)}, Identity, o)
			}
		}
	}
	return s
}

// InnerRadii derives padding-box radii from outer radii and border widths.
func InnerRadii(r Radii, w [4]float64) Radii {
	sub := func(v, d float64) float64 {
		if v-d < 0 {
			return 0
		}
		return v - d
	}
	return Radii{
		{sub(r[0].X, w[3]), sub(r[0].Y, w[0])},
		{sub(r[1].X, w[1]), sub(r[1].Y, w[0])},
		{sub(r[2].X, w[1]), sub(r[2].Y, w[2])},
		{sub(r[3].X, w[3]), sub(r[3].Y, w[2])},
	}
}
