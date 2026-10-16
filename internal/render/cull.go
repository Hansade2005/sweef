package render

import "math"

// CullOps returns the part of a display list that can put ink in the document rows [y0, y1) (CSS px). It exists
// so a viewport-sized slice of a very tall page can be rasterised without walking every op of the page: the GUI
// paints the page in bands, and a band at the bottom of a 100,000 px document must not cost more than one at the
// top.
//
// The cull is conservative. Leaf ops whose bounds (grown by their blur, shadow and glyph extents) lie outside
// the rows are dropped; a clip, layer, or mask group whose whole area lies outside is dropped with everything
// inside it. Anything inside a transform, a 3D transform, a clip path or a mask is kept as is (those move or
// reshape their content in ways that are not worth bounding here), and so are ops this function does not know.
// The ops that are returned are in their original order, so the result paints exactly the same pixels inside
// the rows as the full list.
func CullOps(list []Op, y0, y1 float64) []Op {
	const pad = 4
	y0 -= pad
	y1 += pad
	out := make([]Op, 0, 256)
	// unsafe counts the open groups inside which positions are not document positions
	unsafe := 0
	// stack of what each open group needs when it closes: 1 = counted in unsafe, 0 = plain
	type grp struct {
		unsafe bool
	}
	stack := make([]grp, 0, 16)
	outside := func(top, bot float64) bool { return bot < y0 || top > y1 }
	for i := 0; i < len(list); i++ {
		op := list[i]
		switch o := op.(type) {
		case PushClip:
			if unsafe == 0 && o.Rect.W >= 0 && o.Rect.H >= 0 && outside(o.Rect.Y, o.Rect.Bottom()) {
				i = skipGroup(list, i)
				continue
			}
			stack = append(stack, grp{})
			out = append(out, op)
		case PushLayer:
			if unsafe == 0 && len(o.Filter) == 0 && o.Bounds.H > 0 && outside(o.Bounds.Y, o.Bounds.Bottom()) {
				i = skipGroup(list, i)
				continue
			}
			stack = append(stack, grp{unsafe: true})
			unsafe++
			out = append(out, op)
		case PushTransform, PushTransform3D, PushClipPath, PushMask:
			stack = append(stack, grp{unsafe: true})
			unsafe++
			out = append(out, op)
		case PopClip:
			if n := len(stack); n > 0 {
				if stack[n-1].unsafe {
					unsafe--
				}
				stack = stack[:n-1]
			}
			out = append(out, op)
		case PopLayer, PopTransform, PopTransform3D, PopClipPath, PopMask:
			if n := len(stack); n > 0 {
				if stack[n-1].unsafe {
					unsafe--
				}
				stack = stack[:n-1]
			}
			out = append(out, op)
		default:
			if unsafe == 0 {
				if top, bot, ok := leafRows(op); ok && outside(top, bot) {
					continue
				}
			}
			out = append(out, op)
		}
	}
	return out
}

// skipGroup returns the index of the Pop op that closes the group opened at list[i].
func skipGroup(list []Op, i int) int {
	depth := 0
	for j := i; j < len(list); j++ {
		switch list[j].(type) {
		case PushClip, PushLayer, PushTransform, PushTransform3D, PushClipPath, PushMask:
			depth++
		case PopClip, PopLayer, PopTransform, PopTransform3D, PopClipPath, PopMask:
			depth--
			if depth == 0 {
				return j
			}
		}
	}
	return len(list) - 1
}

// leafRows is the vertical extent of a drawing op, or ok=false for ops it does not bound.
func leafRows(op Op) (top, bot float64, ok bool) {
	switch o := op.(type) {
	case FillRect:
		return o.Rect.Y, o.Rect.Bottom(), true
	case Border:
		return o.Rect.Y, o.Rect.Bottom(), true
	case Image:
		return o.Dst.Y, o.Dst.Bottom(), true
	case Gradient:
		return o.Rect.Y, o.Rect.Bottom(), true
	case Bitmap:
		return o.Dst.Y, o.Dst.Bottom(), true
	case SVGDraw:
		return o.Dst.Y, o.Dst.Bottom(), true
	case Scrollbar:
		return o.Rect.Y, o.Rect.Bottom(), true
	case ScrollCorner:
		return o.Rect.Y, o.Rect.Bottom(), true
	case BoxShadow:
		e := o.Blur*2 + math.Abs(o.Spread) + math.Abs(o.DY)
		return o.Rect.Y - e, o.Rect.Bottom() + e, true
	case BackdropFilter:
		return o.Rect.Y - 200, o.Rect.Bottom() + 200, true
	case TextRun:
		e := 0.0
		for _, s := range o.Shadows {
			e = math.Max(e, math.Abs(s.DY)+s.Blur*2)
		}
		// glyphs reach about 1.3 em above the baseline (tall accents) and 0.5 em below; lines and their offsets
		// stay within 0.3 em more
		return o.Y - 1.6*o.Size - e - o.Stroke, o.Y + 0.9*o.Size + e + o.Stroke + o.UnderOffset + o.DecoThick, true
	}
	return 0, 0, false
}
