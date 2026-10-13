package vp8enc

// Forward and inverse transforms. The inverse transforms are the same
// integer arithmetic the decoder (internal/media/vp8) runs, so the encoder's
// reconstruction is bit-identical to what a decoder shows.

// fdct4x4 is the forward 4x4 DCT (libvpx vp8_short_fdct4x4_c). in is a 4x4
// block of residuals in raster order; out receives the coefficients in raster order.
func fdct4x4(in *[16]int16, out *[16]int16) {
	var t [16]int32
	for i := 0; i < 4; i++ {
		a1 := (int32(in[i*4+0]) + int32(in[i*4+3])) * 8
		b1 := (int32(in[i*4+1]) + int32(in[i*4+2])) * 8
		c1 := (int32(in[i*4+1]) - int32(in[i*4+2])) * 8
		d1 := (int32(in[i*4+0]) - int32(in[i*4+3])) * 8
		t[i*4+0] = a1 + b1
		t[i*4+2] = a1 - b1
		t[i*4+1] = (c1*2217 + d1*5352 + 14500) >> 12
		t[i*4+3] = (d1*2217 - c1*5352 + 7500) >> 12
	}
	for i := 0; i < 4; i++ {
		a1 := t[i] + t[12+i]
		b1 := t[4+i] + t[8+i]
		c1 := t[4+i] - t[8+i]
		d1 := t[i] - t[12+i]
		out[i] = int16((a1 + b1 + 7) >> 4)
		out[8+i] = int16((a1 - b1 + 7) >> 4)
		v := (c1*2217 + d1*5352 + 12000) >> 16
		if d1 != 0 {
			v++
		}
		out[4+i] = int16(v)
		out[12+i] = int16((d1*2217 - c1*5352 + 51000) >> 16)
	}
}

// fwht4x4 is the forward Walsh-Hadamard transform of the sixteen luma DC
// coefficients (libvpx vp8_short_walsh4x4_c).
func fwht4x4(in *[16]int16, out *[16]int16) {
	var t [16]int32
	for i := 0; i < 4; i++ {
		a1 := (int32(in[i*4+0]) + int32(in[i*4+2])) * 4
		d1 := (int32(in[i*4+1]) + int32(in[i*4+3])) * 4
		c1 := (int32(in[i*4+1]) - int32(in[i*4+3])) * 4
		b1 := (int32(in[i*4+0]) - int32(in[i*4+2])) * 4
		v := a1 + d1
		if a1 != 0 {
			v++
		}
		t[i*4+0] = v
		t[i*4+1] = b1 + c1
		t[i*4+2] = b1 - c1
		t[i*4+3] = a1 - d1
	}
	for i := 0; i < 4; i++ {
		a1 := t[i] + t[8+i]
		d1 := t[4+i] + t[12+i]
		c1 := t[4+i] - t[12+i]
		b1 := t[i] - t[8+i]
		a2, b2, c2, d2 := a1+d1, b1+c1, b1-c1, a1-d1
		if a2 < 0 {
			a2++
		}
		if b2 < 0 {
			b2++
		}
		if c2 < 0 {
			c2++
		}
		if d2 < 0 {
			d2++
		}
		out[i] = int16((a2 + 3) >> 3)
		out[4+i] = int16((b2 + 3) >> 3)
		out[8+i] = int16((c2 + 3) >> 3)
		out[12+i] = int16((d2 + 3) >> 3)
	}
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

// idct4x4Add adds the inverse DCT of the dequantised coefficients c (raster
// order) to the 4x4 block at dst (decoder arithmetic, vp8 idct.go).
func idct4x4Add(c *[16]int16, dst []byte, stride int) {
	const (
		c1 = 85627
		c2 = 35468
	)
	var m [4][4]int32
	for i := 0; i < 4; i++ {
		a := int32(c[i]) + int32(c[8+i])
		b := int32(c[i]) - int32(c[8+i])
		cc := (int32(c[4+i])*c2)>>16 - (int32(c[12+i])*c1)>>16
		d := (int32(c[4+i])*c1)>>16 + (int32(c[12+i])*c2)>>16
		m[i][0] = a + d
		m[i][1] = b + cc
		m[i][2] = b - cc
		m[i][3] = a - d
	}
	for j := 0; j < 4; j++ {
		dc := m[0][j] + 4
		a := dc + m[2][j]
		b := dc - m[2][j]
		cc := (m[1][j]*c2)>>16 - (m[3][j]*c1)>>16
		d := (m[1][j]*c1)>>16 + (m[3][j]*c2)>>16
		row := dst[j*stride : j*stride+4]
		row[0] = clip8(int32(row[0]) + (a+d)>>3)
		row[1] = clip8(int32(row[1]) + (b+cc)>>3)
		row[2] = clip8(int32(row[2]) + (b-cc)>>3)
		row[3] = clip8(int32(row[3]) + (a-d)>>3)
	}
}

// idctDCAdd adds a DC-only inverse DCT.
func idctDCAdd(dc int16, dst []byte, stride int) {
	v := (int32(dc) + 4) >> 3
	for j := 0; j < 4; j++ {
		row := dst[j*stride : j*stride+4]
		row[0] = clip8(int32(row[0]) + v)
		row[1] = clip8(int32(row[1]) + v)
		row[2] = clip8(int32(row[2]) + v)
		row[3] = clip8(int32(row[3]) + v)
	}
}

// iwht4x4 is the decoder's inverse WHT: in holds the dequantised Y2
// coefficients in raster order, out[i] receives the DC of luma block i.
func iwht4x4(in *[16]int16, out *[16]int16) {
	var m [16]int32
	for i := 0; i < 4; i++ {
		a0 := int32(in[0+i]) + int32(in[12+i])
		a1 := int32(in[4+i]) + int32(in[8+i])
		a2 := int32(in[4+i]) - int32(in[8+i])
		a3 := int32(in[0+i]) - int32(in[12+i])
		m[0+i] = a0 + a1
		m[8+i] = a0 - a1
		m[4+i] = a3 + a2
		m[12+i] = a3 - a2
	}
	for i := 0; i < 4; i++ {
		dc := m[0+i*4] + 3
		a0 := dc + m[3+i*4]
		a1 := m[1+i*4] + m[2+i*4]
		a2 := m[1+i*4] - m[2+i*4]
		a3 := dc - m[3+i*4]
		out[i*4+0] = int16((a0 + a1) >> 3)
		out[i*4+1] = int16((a3 + a2) >> 3)
		out[i*4+2] = int16((a0 - a1) >> 3)
		out[i*4+3] = int16((a3 - a2) >> 3)
	}
}

// zigzag maps the position in the coded order to the raster position in a 4x4 block.
var zigzag = [16]uint8{0, 1, 4, 8, 5, 2, 3, 6, 9, 12, 13, 10, 7, 11, 14, 15}

// bands maps the coded position to the token probability band (entry 16 is the position after the last).
var bands = [17]uint8{0, 1, 2, 3, 6, 4, 5, 6, 6, 6, 6, 6, 6, 6, 6, 7, 0}
