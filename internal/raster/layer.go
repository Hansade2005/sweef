package raster

import "math"

// drawFiltered renders (m, sh) with the context filter list.
func (c *Context) drawFiltered(m *Mask, sh Shader) {
	st := &c.st
	pad := filterPad(st.filters)
	if c.hasShadow() {
		pad += int(math.Ceil(math.Abs(st.shadowBlur)*1.5)) + int(math.Ceil(math.Max(math.Abs(st.shadowDX), math.Abs(st.shadowDY))))
	}
	region := IRect{m.B.X0 - pad, m.B.Y0 - pad, m.B.X1 + pad, m.B.Y1 + pad}.Intersect(c.surf.R)
	if region.Empty() {
		return
	}
	tmp := NewSurfaceRect(region)
	cl := &clipState{R: region}
	c.drawShadow(m, sh, tmp, cl)
	composite(tmp, m, sh, SourceOver, cl)
	ApplyFilters(tmp, st.filters)
	compositeRect(c.surf, region, surfShader{tmp, 256}, st.op, st.clip)
}

// compositeRect composites sh over r only (no coverage mask).
func compositeRect(dst *Surface, r IRect, sh Shader, op CompositeOp, cl *clipState) {
	nc := *cl
	nc.R = cl.R.Intersect(r)
	if nc.R.Empty() {
		return
	}
	composite(dst, nil, sh, op, &nc)
}

// BeginLayer redirects drawing into a transparent offscreen group covering
// bounds (device space, padded for the filters). Clips active before the
// layer are applied when EndLayer composites it.
func (c *Context) BeginLayer(bounds IRect, filters []Filter) {
	pad := filterPad(filters)
	r := IRect{bounds.X0 - pad, bounds.Y0 - pad, bounds.X1 + pad, bounds.Y1 + pad}.Intersect(c.surf.R)
	if r.Empty() {
		r = IRect{c.surf.R.X0, c.surf.R.Y0, c.surf.R.X0 + 1, c.surf.R.Y0 + 1}
	}
	c.layers = append(c.layers, layerRec{parent: c.surf, pclip: c.st.clip, filters: filters})
	c.surf = NewSurfaceRect(r)
	c.st.clip = &clipState{R: r}
}

// EndLayer composites the innermost layer back with opacity and blend op.
func (c *Context) EndLayer(opacity float64, op CompositeOp) {
	n := len(c.layers)
	if n == 0 {
		return
	}
	rec := c.layers[n-1]
	c.layers = c.layers[:n-1]
	layer := c.surf
	c.surf = rec.parent
	// restore the clip that was active when the layer began; Restore() calls
	// inside the layer must not leak, so callers pair Save/Restore around it.
	c.st.clip = rec.pclip
	if len(rec.filters) > 0 {
		ApplyFilters(layer, rec.filters)
	}
	a := uint32(clamp01(opacity)*256 + 0.5)
	compositeRect(c.surf, layer.R, surfShader{layer, a}, op, rec.pclip)
}

// LayerDepth is the number of open layers.
func (c *Context) LayerDepth() int { return len(c.layers) }

// DrawSurface composites a whole surface at (x,y) device pixels using the
// current alpha, op and clip, ignoring the transform.
func (c *Context) DrawSurface(s *Surface, op CompositeOp, alpha float64) {
	compositeRect(c.surf, s.R, surfShader{s, uint32(clamp01(alpha)*256 + 0.5)}, op, c.st.clip)
}
