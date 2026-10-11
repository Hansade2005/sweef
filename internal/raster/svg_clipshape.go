package raster

import (
	"math"

	"github.com/sweefbrowser/sweef/internal/render"
)

// strokeBBox is the bounding box of n including the stroke (user space of n,
// before its transform).
func (r *svgR) strokeBBox(n *svgNode, st *sStyle) (Rect, bool) {
	switch n.Tag {
	case "g", "a", "svg", "switch", "symbol":
		var out Rect
		have := false
		for _, k := range n.Kids {
			if k.Tag == "#text" || nonRendered(k.Tag) || k.A["display"] == "none" {
				continue
			}
			ks := r.inherit(*st, k.A)
			b, ok := r.strokeBBox(k, &ks)
			if !ok {
				continue
			}
			b, _ = transformRect(b, r.nodeTransform(k, &ks))
			if !have {
				out, have = b, true
				continue
			}
			x0, y0 := math.Min(out.X, b.X), math.Min(out.Y, b.Y)
			x1, y1 := math.Max(out.X+out.W, b.X+b.W), math.Max(out.Y+out.H, b.Y+b.H)
			out = Rect{x0, y0, x1 - x0, y1 - y0}
		}
		return out, have
	}
	b, ok := r.bbox(n, st)
	if !ok {
		return b, false
	}
	if st.stroke.kind != pNone && st.sw > 0 {
		h := st.sw / 2
		b = Rect{b.X - h, b.Y - h, b.W + 2*h, b.H + 2*h}
	}
	return b, true
}

// applyShapeClip clips to a CSS basic shape given as the clip-path value of
// n; false when nothing of n is visible.
func (r *svgR) applyShapeClip(v string, n *svgNode, st *sStyle) bool {
	cs, ok := render.ParseSVGClip(v)
	if !ok {
		return true // not a basic shape: ignored
	}
	var R Rect
	var have bool
	switch cs.Box {
	case "fill-box", "content-box", "padding-box":
		R, have = r.bbox(n, st)
	case "view-box":
		R, have = Rect{0, 0, r.vw, r.vh}, true
	default: // border-box and margin-box map to the stroke box
		R, have = r.strokeBBox(n, st)
	}
	if !have {
		R = Rect{}
	}
	pa, rule := clipSubPath(cs.Resolve(render.Rect(R)))
	if pa.Empty() {
		r.c.clipWithMask(nil)
		return false
	}
	r.c.ClipPath(pa, rule)
	return true
}
