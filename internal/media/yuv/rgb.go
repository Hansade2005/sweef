package yuv

import "image"

type matrixCoeffs struct{ kr, kb float64 }

func coeffsFor(matrix, height int) matrixCoeffs {
	switch matrix {
	case MatrixBT709:
		return matrixCoeffs{0.2126, 0.0722}
	case 5, 6, 4:
		return matrixCoeffs{0.299, 0.114}
	case MatrixBT2020, 10:
		return matrixCoeffs{0.2627, 0.0593}
	}
	if height >= 720 {
		return matrixCoeffs{0.2126, 0.0722}
	}
	return matrixCoeffs{0.299, 0.114}
}

// RGBA converts the frame to 8-bit RGBA using limited or full range as
// flagged, the frame's matrix (BT.709 for unspecified HD content, BT.601
// otherwise) and round-to-nearest, matching the usual 8-bit pipeline within
// rounding. Identity-matrix 4:4:4 frames (GBR) are copied through.
func (f *Frame) RGBA() *image.RGBA {
	w, h := f.Width, f.Height
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	d := uint(f.Depth)
	maxv := int64(1)<<d - 1
	sx, sy := f.Format.Subsampling()
	if f.Format == F400 {
		sx, sy = 1, 1
	}
	if f.Matrix == MatrixIdentity && f.Format == F444 {
		sh := d - 8
		for y := 0; y < h; y++ {
			o := dst.Pix[y*dst.Stride:]
			g, b, r := f.Planes[0][y*f.Stride[0]:], f.Planes[1][y*f.Stride[1]:], f.Planes[2][y*f.Stride[2]:]
			for x := 0; x < w; x++ {
				o[4*x] = narrow(int64(r[x]), sh, maxv)
				o[4*x+1] = narrow(int64(g[x]), sh, maxv)
				o[4*x+2] = narrow(int64(b[x]), sh, maxv)
				o[4*x+3] = 255
			}
		}
		return dst
	}
	cf := coeffsFor(f.Matrix, h)
	kg := 1 - cf.kr - cf.kb
	var ys, cs float64 // output-8-bit units per input code at this depth
	var yoff, coff int64
	scale := 1.0 / float64(int64(1)<<(d-8))
	if f.FullRange {
		ys, cs = 1, 1
		yoff = 0
	} else {
		ys, cs = 255.0/219, 255.0/224
		yoff = 16 << (d - 8)
	}
	coff = 128 << (d - 8)
	const fb = 16
	one := float64(int64(1) << fb)
	cY := int64(ys*scale*one + 0.5)
	cRV := int64(2*(1-cf.kr)*cs*scale*one + 0.5)
	cBU := int64(2*(1-cf.kb)*cs*scale*one + 0.5)
	cGU := int64(2*cf.kb*(1-cf.kb)/kg*cs*scale*one + 0.5)
	cGV := int64(2*cf.kr*(1-cf.kr)/kg*cs*scale*one + 0.5)
	round := int64(1) << (fb - 1)
	for y := 0; y < h; y++ {
		o := dst.Pix[y*dst.Stride:]
		yr := f.Planes[0][y*f.Stride[0]:]
		var ur, vr []uint16
		if f.Format != F400 {
			cy := y >> sy
			ur = f.Planes[1][cy*f.Stride[1]:]
			vr = f.Planes[2][cy*f.Stride[2]:]
		}
		for x := 0; x < w; x++ {
			yy := (int64(yr[x]) - yoff) * cY
			var u, v int64
			if ur != nil {
				u = int64(ur[x>>sx]) - coff
				v = int64(vr[x>>sx]) - coff
			}
			o[4*x] = clip8(int32((yy + cRV*v + round) >> fb))
			o[4*x+1] = clip8(int32((yy - cGU*u - cGV*v + round) >> fb))
			o[4*x+2] = clip8(int32((yy + cBU*u + round) >> fb))
			o[4*x+3] = 255
		}
	}
	return dst
}

func narrow(v int64, sh uint, maxv int64) uint8 {
	if sh > 0 {
		v = (v + 1<<(sh-1)) >> sh
	}
	if v > 255 {
		v = 255
	}
	return uint8(v)
}

func clip8(v int32) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}
