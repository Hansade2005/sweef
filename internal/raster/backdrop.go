package raster

import (
	"github.com/sweefbrowser/sweef/internal/render"
)

// backdrop applies backdrop-filter: the area behind the element is copied,
// filtered, and drawn back clipped to the (rounded) border box.
func (p *Painter) backdrop(o render.BackdropFilter) {
	c := p.c
	if len(o.Filter) == 0 || o.Rect.W <= 0 || o.Rect.H <= 0 {
		return
	}
	c.Save()
	defer c.Restore()
	m := c.Transform()
	pa := rrPath(o.Rect, o.Radii)
	bb, ok := pa.Transform(m).Bounds()
	if !ok {
		return
	}
	pad := 0
	fl := make([]Filter, len(o.Filter))
	sc := m.MaxScale()
	for i, f := range o.Filter {
		fl[i] = Filter{Name: f.Name, Args: append([]float64(nil), f.Args...), Color: col(f.Color)}
		switch f.Name {
		case "blur":
			fl[i].Args[0] *= sc
		case "drop-shadow":
			for k := range fl[i].Args {
				fl[i].Args[k] *= sc
			}
		}
	}
	pad = filterPad(fl)
	box := bb.OutsetBounds().Intersect(c.surf.R)
	if box.Empty() {
		return
	}
	// Chromium filters only the backdrop inside the border box, mirroring the
	// edge pixels outward so blurs do not fade to transparent at the edges.
	region := IRect{box.X0 - pad, box.Y0 - pad, box.X1 + pad, box.Y1 + pad}
	tmp := NewSurfaceRect(region)
	bw, bh := box.W(), box.H()
	refl := func(v, n int) int {
		if n <= 1 {
			return 0
		}
		period := 2 * n
		v %= period
		if v < 0 {
			v += period
		}
		if v >= n {
			v = period - 1 - v
		}
		return v
	}
	for y := region.Y0; y < region.Y1; y++ {
		sy := box.Y0 + refl(y-box.Y0, bh)
		for x := region.X0; x < region.X1; x++ {
			sx := box.X0 + refl(x-box.X0, bw)
			si := (sy-c.surf.R.Y0)*c.surf.Stride + (sx-c.surf.R.X0)*4
			di := (y-region.Y0)*tmp.Stride + (x-region.X0)*4
			copy(tmp.Pix[di:di+4], c.surf.Pix[si:si+4])
		}
	}
	ApplyFilters(tmp, fl)
	c.ClipPath(pa, NonZero)
	compositeRect(c.surf, box, surfShader{tmp, 256}, Copy, c.st.clip)
}
