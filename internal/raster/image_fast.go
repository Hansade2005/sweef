package raster

import "math"

// The video path. A decoded video frame is an opaque RGBA image that is drawn once per frame, scaled, into a
// pixel aligned box. Through the general shader machinery (a coverage mask for the rectangle, a float
// sampler per pixel, a blend per pixel) that costs tens of milliseconds for a 1080p frame; the loop below
// computes the same bilinear samples (the same arithmetic, so the pixels are identical, which
// TestDrawImageFastMatchesGeneral checks) with the per-column work done once per draw and the per-row work
// once per row, and stores them straight into the destination.

// noImageFast disables the fast path (tests compare both).
var noImageFast bool

// colTap is the horizontal part of one bilinear sample: the byte offsets of the two source texels of a row
// and their weights (the weight of the second is 256 minus the first).
type colTap struct {
	o0, o1 int32
	w1     uint32
}

// drawImageFast draws img scaled from the source rectangle to the destination rectangle (user space) when
// that is the pixel aligned, opaque, bilinear case; it reports false when it is not and nothing was drawn.
func (c *Context) drawImageFast(img *Surface, sx, sy, sw, sh, dx, dy, dw, dh float64) bool {
	st := &c.st
	if noImageFast || !img.Opaque || st.alpha != 1 || st.op != SourceOver || len(st.filters) > 0 || st.clip.M != nil ||
		c.hasShadow() || c.filterMode() != FilterBilinear {
		return false
	}
	ctm := st.ctm
	if ctm[1] != 0 || ctm[2] != 0 {
		return false
	}
	// the destination rectangle in device space must lie on pixel boundaries: then every pixel of it is
	// covered completely and every pixel outside not at all, as the general path's coverage mask would be
	ax, bx := ctm[0]*dx+ctm[4], ctm[0]*(dx+dw)+ctm[4]
	ay, by := ctm[3]*dy+ctm[5], ctm[3]*(dy+dh)+ctm[5]
	if ax > bx {
		ax, bx = bx, ax
	}
	if ay > by {
		ay, by = by, ay
	}
	if math.Abs(ax) > 1<<24 || math.Abs(bx) > 1<<24 || math.Abs(ay) > 1<<24 || math.Abs(by) > 1<<24 {
		return false
	}
	ix0, ix1, iy0, iy1 := math.Round(ax), math.Round(bx), math.Round(ay), math.Round(by)
	const eps = 1e-9
	if math.Abs(ax-ix0) > eps || math.Abs(bx-ix1) > eps || math.Abs(ay-iy0) > eps || math.Abs(by-iy1) > eps {
		return false
	}
	m := Translate(dx, dy).Mul(Scale(dw/sw, dh/sh)).Mul(Translate(-sx, -sy))
	is := NewImageShader(img, ctm.Mul(m), false, false, FilterBilinear, 1)
	is.EdgeClamp = true
	x0, x1 := math.Min(sx, sx+sw), math.Max(sx, sx+sw)
	y0, y1 := math.Min(sy, sy+sh), math.Max(sy, sy+sh)
	is.SetClamp(IRect{int(math.Floor(x0 + 1e-9)), int(math.Floor(y0 + 1e-9)), int(math.Ceil(x1 - 1e-9)), int(math.Ceil(y1 - 1e-9))})
	if is.inv[1] != 0 || is.inv[2] != 0 || is.Clamp.Empty() || is.Img != img {
		return false
	}
	r := IRect{int(ix0), int(iy0), int(ix1), int(iy1)}.Intersect(st.clip.R).Intersect(c.surf.R)
	if r.Empty() {
		return true
	}
	is.blitOpaque(c.surf, r)
	return true
}

// blitOpaque stores the bilinear samples of the image for the device rectangle r (inside dst) into dst. It is
// ShadeRow's arithmetic for an axis aligned transform with edge replication.
func (s *ImageShader) blitOpaque(dst *Surface, r IRect) {
	w := r.W()
	cl := s.Clamp
	img := s.Img
	if s.inv[0] == 1 && s.inv[3] == 1 && s.inv[4] == math.Trunc(s.inv[4]) && s.inv[5] == math.Trunc(s.inv[5]) &&
		math.Abs(s.inv[4]) < 1<<24 && math.Abs(s.inv[5]) < 1<<24 {
		// pixel for pixel: every sample has the weight 1 on a single texel
		tx, ty := int(s.inv[4]), int(s.inv[5])
		if r.X0+tx >= cl.X0 && r.X1+tx <= cl.X1 && r.Y0+ty >= cl.Y0 && r.Y1+ty <= cl.Y1 {
			parallelFor(r.H(), max(1, 65536/w), func(lo, hi int) {
				for y := r.Y0 + lo; y < r.Y0+hi; y++ {
					o := (y+ty)*img.Stride + (r.X0+tx)*4
					copy(dst.Row(y, r.X0, r.X1), img.Pix[o:o+w*4])
				}
			})
			return
		}
	}
	cols := make([]colTap, w)
	// u = inv0*fx + inv2*fy + inv4, with inv2 == 0: the zero term is kept so that the rounding is the
	// general sampler's on every architecture (fused multiply-add)
	zx := s.inv[2] * (float64(r.Y0) + 0.5)
	for i := range cols {
		fx := float64(r.X0+i) + 0.5
		u := s.inv[0]*fx + zx + s.inv[4]
		uu := u - 0.5
		x0f := math.Floor(uu)
		fxw := uint32((uu-x0f)*256 + 0.5)
		ix0 := min(max(int(x0f), cl.X0), cl.X1-1)
		ix1 := min(max(int(x0f)+1, cl.X0), cl.X1-1)
		cols[i] = colTap{o0: int32(ix0 * 4), o1: int32(ix1 * 4), w1: fxw}
	}
	zy := s.inv[1] * (float64(r.X0) + 0.5)
	parallelFor(r.H(), max(1, 16384/w), func(lo, hi int) {
		for y := r.Y0 + lo; y < r.Y0+hi; y++ {
			fy := float64(y) + 0.5
			v := zy + s.inv[3]*fy + s.inv[5]
			vv := v - 0.5
			y0f := math.Floor(vv)
			fyw := uint32((vv-y0f)*256 + 0.5)
			iy0 := min(max(int(y0f), cl.Y0), cl.Y1-1)
			iy1 := min(max(int(y0f)+1, cl.Y0), cl.Y1-1)
			row0 := img.Pix[iy0*img.Stride : iy0*img.Stride+img.W()*4]
			row1 := row0
			if iy1 != iy0 {
				row1 = img.Pix[iy1*img.Stride : iy1*img.Stride+img.W()*4]
			}
			out := dst.Row(y, r.X0, r.X1)
			scaleRowOpaque(out, row0, row1, cols, 256-fyw, fyw)
		}
	})
}

// scaleRowOpaque computes one output row: for every column the bilinear blend of the texels (o0, o1) of the
// two source rows with the weights (256-wx)*wy0, wx*wy0, (256-wx)*wy1, wx*wy1 (they add up to 65536), rounded
// once at the end, exactly as the general sampler does.
func scaleRowOpaque(out, row0, row1 []uint8, cols []colTap, wy0, wy1 uint32) {
	out = out[:len(cols)*4]
	if wy1 == 0 {
		// no vertical blend: two taps
		for i := range cols {
			c := &cols[i]
			wx1 := c.w1 * 256
			wx0 := (256 - c.w1) * 256
			a := row0[c.o0 : c.o0+4 : c.o0+4]
			b := row0[c.o1 : c.o1+4 : c.o1+4]
			o := out[i*4 : i*4+4 : i*4+4]
			o[0] = uint8((uint32(a[0])*wx0 + uint32(b[0])*wx1 + 32768) >> 16)
			o[1] = uint8((uint32(a[1])*wx0 + uint32(b[1])*wx1 + 32768) >> 16)
			o[2] = uint8((uint32(a[2])*wx0 + uint32(b[2])*wx1 + 32768) >> 16)
			o[3] = 255
		}
		return
	}
	for i := range cols {
		c := &cols[i]
		wx1 := c.w1
		wx0 := 256 - wx1
		w00, w10, w01, w11 := wx0*wy0, wx1*wy0, wx0*wy1, wx1*wy1
		a := row0[c.o0 : c.o0+4 : c.o0+4]
		b := row0[c.o1 : c.o1+4 : c.o1+4]
		d := row1[c.o0 : c.o0+4 : c.o0+4]
		e := row1[c.o1 : c.o1+4 : c.o1+4]
		o := out[i*4 : i*4+4 : i*4+4]
		o[0] = uint8((uint32(a[0])*w00 + uint32(b[0])*w10 + uint32(d[0])*w01 + uint32(e[0])*w11 + 32768) >> 16)
		o[1] = uint8((uint32(a[1])*w00 + uint32(b[1])*w10 + uint32(d[1])*w01 + uint32(e[1])*w11 + 32768) >> 16)
		o[2] = uint8((uint32(a[2])*w00 + uint32(b[2])*w10 + uint32(d[2])*w01 + uint32(e[2])*w11 + 32768) >> 16)
		o[3] = 255
	}
}
