package h264

// Inverse transforms (8.5.10 - 8.5.13).

func clipPix(v, maxv int32) uint16 {
	if uint32(v) > uint32(maxv) {
		if v < 0 {
			return 0
		}
		return uint16(maxv)
	}
	return uint16(v)
}

// idct4Add transforms the 16 dequantised coefficients in c (raster order)
// and adds the residual to dst.
func idct4Add(dst []uint16, stride int, c []int32, maxv int32) {
	var t [16]int32
	for i := 0; i < 4; i++ {
		d0, d1, d2, d3 := c[i*4], c[i*4+1], c[i*4+2], c[i*4+3]
		e0 := d0 + d2
		e1 := d0 - d2
		e2 := (d1 >> 1) - d3
		e3 := d1 + (d3 >> 1)
		t[i*4] = e0 + e3
		t[i*4+1] = e1 + e2
		t[i*4+2] = e1 - e2
		t[i*4+3] = e0 - e3
	}
	for j := 0; j < 4; j++ {
		f0, f1, f2, f3 := t[j], t[4+j], t[8+j], t[12+j]
		g0 := f0 + f2
		g1 := f0 - f2
		g2 := (f1 >> 1) - f3
		g3 := f1 + (f3 >> 1)
		h := [4]int32{g0 + g3, g1 + g2, g1 - g2, g0 - g3}
		for i := 0; i < 4; i++ {
			p := &dst[i*stride+j]
			*p = clipPix(int32(*p)+((h[i]+32)>>6), maxv)
		}
	}
}

func idct8Add(dst []uint16, stride int, c []int32, maxv int32) {
	var t [64]int32
	for i := 0; i < 8; i++ {
		d := c[i*8 : i*8+8]
		a0 := d[0] + d[4]
		a4 := d[0] - d[4]
		a2 := (d[2] >> 1) - d[6]
		a6 := d[2] + (d[6] >> 1)
		b0 := a0 + a6
		b2 := a4 + a2
		b4 := a4 - a2
		b6 := a0 - a6
		a1 := -d[3] + d[5] - d[7] - (d[7] >> 1)
		a3 := d[1] + d[7] - d[3] - (d[3] >> 1)
		a5 := -d[1] + d[7] + d[5] + (d[5] >> 1)
		a7 := d[3] + d[5] + d[1] + (d[1] >> 1)
		b1 := a1 + (a7 >> 2)
		b7 := a7 - (a1 >> 2)
		b3 := a3 + (a5 >> 2)
		b5 := (a3 >> 2) - a5
		o := t[i*8 : i*8+8]
		o[0] = b0 + b7
		o[1] = b2 + b5
		o[2] = b4 + b3
		o[3] = b6 + b1
		o[4] = b6 - b1
		o[5] = b4 - b3
		o[6] = b2 - b5
		o[7] = b0 - b7
	}
	for j := 0; j < 8; j++ {
		var d [8]int32
		for i := 0; i < 8; i++ {
			d[i] = t[i*8+j]
		}
		a0 := d[0] + d[4]
		a4 := d[0] - d[4]
		a2 := (d[2] >> 1) - d[6]
		a6 := d[2] + (d[6] >> 1)
		b0 := a0 + a6
		b2 := a4 + a2
		b4 := a4 - a2
		b6 := a0 - a6
		a1 := -d[3] + d[5] - d[7] - (d[7] >> 1)
		a3 := d[1] + d[7] - d[3] - (d[3] >> 1)
		a5 := -d[1] + d[7] + d[5] + (d[5] >> 1)
		a7 := d[3] + d[5] + d[1] + (d[1] >> 1)
		b1 := a1 + (a7 >> 2)
		b7 := a7 - (a1 >> 2)
		b3 := a3 + (a5 >> 2)
		b5 := (a3 >> 2) - a5
		r := [8]int32{b0 + b7, b2 + b5, b4 + b3, b6 + b1, b6 - b1, b4 - b3, b2 - b5, b0 - b7}
		for i := 0; i < 8; i++ {
			p := &dst[i*stride+j]
			*p = clipPix(int32(*p)+((r[i]+32)>>6), maxv)
		}
	}
}

// bypassAdd adds an n x n block of untransformed residual (lossless
// macroblocks). hor/ver select the intra residual DPCM of 8.5.15 along rows
// (hor) or columns (ver); only the stripe from (x0, y0) of size w x h is
// accumulated. c holds the residual in raster order with row pitch cp.
func bypassAdd(dst []uint16, stride int, c []int32, cp, w, h int, maxv int32, hor, ver bool) {
	if hor {
		for y := 0; y < h; y++ {
			var acc int32
			for x := 0; x < w; x++ {
				acc += c[y*cp+x]
				c[y*cp+x] = acc
			}
		}
	} else if ver {
		for x := 0; x < w; x++ {
			var acc int32
			for y := 0; y < h; y++ {
				acc += c[y*cp+x]
				c[y*cp+x] = acc
			}
		}
	}
	for y := 0; y < h; y++ {
		row := dst[y*stride : y*stride+w]
		for x := range row {
			row[x] = clipPix(int32(row[x])+c[y*cp+x], maxv)
		}
	}
}

// lumaDCTransform performs the inverse Hadamard transform of the Intra16x16
// DC levels (raster 4x4 in c) and scales with LevelScale(qp%6,0,0).
func lumaDCTransform(c *[16]int32, ls int32, qp int) {
	var t [16]int32
	for i := 0; i < 4; i++ {
		a, b, cc, d := c[i*4], c[i*4+1], c[i*4+2], c[i*4+3]
		t[i*4] = a + b + cc + d
		t[i*4+1] = a + b - cc - d
		t[i*4+2] = a - b - cc + d
		t[i*4+3] = a - b + cc - d
	}
	for j := 0; j < 4; j++ {
		a, b, cc, d := t[j], t[4+j], t[8+j], t[12+j]
		f := [4]int32{a + b + cc + d, a + b - cc - d, a - b - cc + d, a - b + cc - d}
		for i := 0; i < 4; i++ {
			if qp >= 36 {
				c[i*4+j] = (f[i] * ls) << uint(qp/6-6)
			} else {
				c[i*4+j] = (f[i]*ls + (1 << uint(5-qp/6))) >> uint(6-qp/6)
			}
		}
	}
}

// chromaDCTransform performs the 2x2 transform and scaling of 4:2:0 chroma DC.
func chromaDCTransform(c *[8]int32, ls int32, qp int) {
	a, b, cc, d := c[0], c[1], c[2], c[3]
	f := [4]int32{a + b + cc + d, a - b + cc - d, a + b - cc - d, a - b - cc + d}
	for i := range f {
		c[i] = ((f[i] * ls) << uint(qp/6)) >> 5
	}
}

// chromaDC422Transform performs the 2x4 transform and scaling of 4:2:2
// chroma DC. c holds the 4 rows x 2 columns in raster order and qp is
// QP'c + 3.
func chromaDC422Transform(c *[8]int32, ls int32, qp int) {
	var t [8]int32 // t[y*2+x] = column transform
	for x := 0; x < 2; x++ {
		c0, c1, c2, c3 := c[x], c[2+x], c[4+x], c[6+x]
		t[x] = c0 + c1 + c2 + c3
		t[2+x] = c0 + c1 - c2 - c3
		t[4+x] = c0 - c1 - c2 + c3
		t[6+x] = c0 - c1 + c2 - c3
	}
	for y := 0; y < 4; y++ {
		f0 := t[y*2] + t[y*2+1]
		f1 := t[y*2] - t[y*2+1]
		if qp >= 36 {
			c[y*2] = (f0 * ls) << uint(qp/6-6)
			c[y*2+1] = (f1 * ls) << uint(qp/6-6)
		} else {
			r := int32(1) << uint(5-qp/6)
			c[y*2] = (f0*ls + r) >> uint(6-qp/6)
			c[y*2+1] = (f1*ls + r) >> uint(6-qp/6)
		}
	}
}

// dequant4 scales one level at raster position k of a 4x4 block.
func dequant4(level int32, ls int32, qp int) int32 {
	if qp >= 24 {
		return (level * ls) << uint(qp/6-4)
	}
	return (level*ls + (1 << uint(3-qp/6))) >> uint(4-qp/6)
}

func dequant8(level int32, ls int32, qp int) int32 {
	if qp >= 36 {
		return (level * ls) << uint(qp/6-6)
	}
	return (level*ls + (1 << uint(5-qp/6))) >> uint(6-qp/6)
}
