package av1

// Transform kinds for one dimension.
const (
	kindDCT  = 0
	kindADST = 1
	kindID   = 2
)

// rowKind / colKind give the 1-D transform used for the horizontal (row) and
// vertical (column) passes of each 2-D transform type (spec 7.13.3).
var rowKind = [16]uint8{
	dctDct: kindDCT, adstDct: kindDCT, dctAdst: kindADST, adstAdst: kindADST,
	flipadstDct: kindDCT, dctFlipadst: kindADST, flipadstFlipadst: kindADST,
	adstFlipadst: kindADST, flipadstAdst: kindADST, idtx: kindID,
	vDct: kindID, hDct: kindDCT, vAdst: kindID, hAdst: kindADST,
	vFlipadst: kindID, hFlipadst: kindADST,
}

var colKind = [16]uint8{
	dctDct: kindDCT, adstDct: kindADST, dctAdst: kindDCT, adstAdst: kindADST,
	flipadstDct: kindADST, dctFlipadst: kindDCT, flipadstFlipadst: kindADST,
	adstFlipadst: kindADST, flipadstAdst: kindADST, idtx: kindID,
	vDct: kindDCT, hDct: kindID, vAdst: kindADST, hAdst: kindID,
	vFlipadst: kindADST, hFlipadst: kindID,
}

func idct(n int, t []int32, lo, hi int32) {
	switch n {
	case 2:
		idct4(t, lo, hi)
	case 3:
		idct8(t, lo, hi)
	case 4:
		idct16(t, lo, hi)
	case 5:
		idct32(t, lo, hi)
	default:
		idct64(t, lo, hi)
	}
}

func iadst4(t []int32) {
	const (
		sinpi19 = 1321
		sinpi29 = 2482
		sinpi39 = 3344
		sinpi49 = 3803
	)
	t0, t1, t2, t3 := int64(t[0]), int64(t[1]), int64(t[2]), int64(t[3])
	s0 := sinpi19 * t0
	s1 := sinpi29 * t0
	s2 := sinpi39 * t1
	s3 := sinpi49 * t2
	s4 := sinpi19 * t2
	s5 := sinpi29 * t3
	s6 := sinpi49 * t3
	a7 := t0 - t2
	b7 := a7 + t3
	s0 += s3
	s1 -= s4
	s3 = s2
	s2 = sinpi39 * b7
	s0 += s5
	s1 -= s6
	x0 := s0 + s3
	x1 := s1 + s3
	x2 := s2
	x3 := s0 + s1
	x3 -= s3
	t[0] = int32((x0 + 2048) >> 12)
	t[1] = int32((x1 + 2048) >> 12)
	t[2] = int32((x2 + 2048) >> 12)
	t[3] = int32((x3 + 2048) >> 12)
}

func iadst(n int, t []int32, lo, hi int32) {
	switch n {
	case 2:
		iadst4(t)
	case 3:
		iadst8(t, lo, hi)
	default:
		iadst16(t, lo, hi)
	}
}

func iidentity(n int, t []int32) {
	switch n {
	case 2:
		for i := 0; i < 4; i++ {
			t[i] = int32((int64(t[i])*5793 + 2048) >> 12)
		}
	case 3:
		for i := 0; i < 8; i++ {
			t[i] *= 2
		}
	case 4:
		for i := 0; i < 16; i++ {
			t[i] = int32((int64(t[i])*11586 + 2048) >> 12)
		}
	default:
		for i := 0; i < 32; i++ {
			t[i] *= 4
		}
	}
}

func iwht(t []int32, shift uint) {
	a := t[0] >> shift
	c := t[1] >> shift
	d := t[2] >> shift
	b := t[3] >> shift
	a += c
	d -= b
	e := (a - d) >> 1
	b = e - b
	c = e - c
	a -= b
	d += c
	t[0], t[1], t[2], t[3] = a, b, c, d
}

// inverseTransform2D implements the 2D inverse transform process (spec
// 7.13.3). deq holds th rows of tw dequantized coefficients (stride tw); rows
// at or beyond nzRows are known to be zero. The result is written to res with
// stride w.
func inverseTransform2D(deq []int32, txSz, txType int, lossless bool, bitDepth int, nzRows int, res []int32) {
	log2W := txWidthLog2[txSz]
	log2H := txHeightLog2[txSz]
	w := 1 << uint(log2W)
	h := 1 << uint(log2H)
	tw := imin(32, w)
	rowShift := uint(transformRowShift[txSz])
	colShift := uint(4)
	if lossless {
		rowShift, colShift = 0, 0
	}
	rowClamp := bitDepth + 8
	colClamp := imax(bitDepth+6, 16)
	rlo, rhi := int32(-(1 << uint(rowClamp-1))), int32((1<<uint(rowClamp-1))-1)
	clo, chi := int32(-(1 << uint(colClamp-1))), int32((1<<uint(colClamp-1))-1)
	rk := rowKind[txType]
	ck := colKind[txType]
	rect := log2W-log2H == 1 || log2H-log2W == 1
	var t [64]int32
	if nzRows > 32 {
		nzRows = 32
	}
	if nzRows > h {
		nzRows = h
	}
	for i := 0; i < nzRows; i++ {
		row := res[i*w : i*w+w]
		src := deq[i*tw : i*tw+tw]
		copy(t[:tw], src)
		for j := tw; j < w; j++ {
			t[j] = 0
		}
		if rect {
			for j := 0; j < w; j++ {
				t[j] = int32((int64(t[j])*2896 + 2048) >> 12)
			}
		}
		if lossless {
			iwht(t[:4], 2)
		} else if rk == kindDCT {
			idct(log2W, t[:w], rlo, rhi)
		} else if rk == kindADST {
			iadst(log2W, t[:w], rlo, rhi)
		} else {
			iidentity(log2W, t[:w])
		}
		if rowShift == 0 {
			for j := 0; j < w; j++ {
				row[j] = clip(t[j], clo, chi)
			}
		} else {
			rnd := int32(1) << (rowShift - 1)
			for j := 0; j < w; j++ {
				row[j] = clip((t[j]+rnd)>>rowShift, clo, chi)
			}
		}
	}
	for i := nzRows; i < h; i++ {
		row := res[i*w : i*w+w]
		for j := range row {
			row[j] = 0
		}
	}
	for j := 0; j < w; j++ {
		for i := 0; i < h; i++ {
			t[i] = res[i*w+j]
		}
		if lossless {
			iwht(t[:4], 0)
		} else if ck == kindDCT {
			idct(log2H, t[:h], clo, chi)
		} else if ck == kindADST {
			iadst(log2H, t[:h], clo, chi)
		} else {
			iidentity(log2H, t[:h])
		}
		if colShift == 0 {
			for i := 0; i < h; i++ {
				res[i*w+j] = t[i]
			}
		} else {
			rnd := int32(1) << (colShift - 1)
			for i := 0; i < h; i++ {
				res[i*w+j] = (t[i] + rnd) >> colShift
			}
		}
	}
}
