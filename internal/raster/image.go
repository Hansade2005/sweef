package raster

import "math"

// DrawImage draws the source rectangle (sx,sy,sw,sh) of img into the
// destination rectangle (dx,dy,dw,dh) in user space, using the context's
// smoothing, alpha, shadow, filter, composite op and clip.
func (c *Context) DrawImage(img *Surface, sx, sy, sw, sh, dx, dy, dw, dh float64) {
	if img == nil || sw == 0 || sh == 0 || dw == 0 || dh == 0 || img.W() == 0 || img.H() == 0 {
		return
	}
	// normalise negative sizes (flips)
	m := Translate(dx, dy).Mul(Scale(dw/sw, dh/sh)).Mul(Translate(-sx, -sy))
	is := NewImageShader(img, c.st.ctm.Mul(m), false, false, c.filterMode(), c.st.alpha)
	x0, x1 := math.Min(sx, sx+sw), math.Max(sx, sx+sw)
	y0, y1 := math.Min(sy, sy+sh), math.Max(sy, sy+sh)
	is.SetClamp(IRect{int(math.Floor(x0 + 1e-9)), int(math.Floor(y0 + 1e-9)), int(math.Ceil(x1 - 1e-9)), int(math.Ceil(y1 - 1e-9))})
	p := NewPath()
	p.Rect(dx, dy, dw, dh)
	c.draw(FillMask(p, c.st.ctm, NonZero, c.drawBounds()), is)
}
