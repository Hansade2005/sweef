package h264

// SP and SI decoding (ITU-T H.264 clause 8.6, Extended profile).
//
// Inter macroblocks of an SP slice (8.6.1, or 8.6.2 when sp_for_switch_flag
// is set) and SI macroblocks of an SI slice (8.6.2) are reconstructed in the
// transform domain: the prediction is transformed, combined with the parsed
// (not yet scaled) levels, re-quantised with QSY and then scaled and inverse
// transformed with QSY, and the result is the decoded sample rather than a
// residual. The levels are kept raw by the residual parser (d.spMB).

// spNormAdjust is normAdjust4x4 (8.5.9) per position class: both indices even,
// both odd, otherwise.
var spNormAdjust = [6][3]int{
	{10, 16, 13}, {11, 18, 14}, {13, 20, 16}, {14, 23, 18}, {16, 25, 20}, {18, 29, 23},
}

// spLevelScale2 is LevelScale2(m, i, j) of 8.6.1 per position class.
var spLevelScale2 = [6][3]int{
	{13107, 5243, 8066}, {11916, 4660, 7490}, {10082, 4194, 6554},
	{9362, 3647, 5825}, {8192, 3355, 5243}, {7282, 2893, 4559},
}

// spA is the matrix A of 8.6.1 per position class.
var spA = [3]int{16, 25, 20}

// spClass returns the position class of raster index r of a 4x4 block.
func spClass(r int) int {
	i, j := r>>2, r&3
	switch {
	case i&1 == 0 && j&1 == 0:
		return 0
	case i&1 == 1 && j&1 == 1:
		return 1
	}
	return 2
}

var spCls [16]int

func init() {
	for r := range spCls {
		spCls[r] = spClass(r)
	}
}

// spFwd4 is the forward core transform of 8.6.1 (c = A . p . A^T) of a
// block in raster order.
func spFwd4(p *[16]int) {
	var t [16]int
	for y := 0; y < 4; y++ {
		a, b, c, d := p[y*4], p[y*4+1], p[y*4+2], p[y*4+3]
		s0, s1, d0, d1 := a+d, b+c, a-d, b-c
		t[y*4] = s0 + s1
		t[y*4+1] = 2*d0 + d1
		t[y*4+2] = s0 - s1
		t[y*4+3] = d0 - 2*d1
	}
	for x := 0; x < 4; x++ {
		a, b, c, d := t[x], t[4+x], t[8+x], t[12+x]
		s0, s1, d0, d1 := a+d, b+c, a-d, b-c
		p[x] = s0 + s1
		p[4+x] = 2*d0 + d1
		p[8+x] = s0 - s1
		p[12+x] = d0 - 2*d1
	}
}

// spQuant quantises v with LevelScale2 ls and the shift sh (rounding offset
// half a step), keeping the sign.
func spQuant(v, ls, sh int) int {
	a := v
	if a < 0 {
		a = -a
	}
	q := (a*ls + (1 << uint(sh-1))) >> uint(sh)
	if v < 0 {
		return -q
	}
	return q
}

// spCombine returns the quantised level of one coefficient: cp is the
// transformed prediction, lev the parsed level.
//
//	switching: quantise the prediction with QS and add the level (8.6.2)
//	otherwise: add the scaled level to the prediction in the transform domain
//	           and quantise with QS (8.6.1)
//
// shift is 15 for 4x4 coefficients and 16 for chroma DC; cls is the position
// class, rem6 = QP % 6, per = QP / 6 for the macroblock QP (scale >> shDeq
// matches the >>6 / >>5 of the spec for the flat 16 weight scale).
func spCombine(cp, lev, cls int, switching bool, qpRem, qpPer, qsRem, qsPer, shift, shDeq int) int {
	if switching {
		return spQuant(cp, spLevelScale2[qsRem][cls], shift+qsPer) + lev
	}
	cs := cp + (((lev * spNormAdjust[qpRem][cls] * spA[cls]) << uint(qpPer)) >> uint(shDeq))
	return spQuant(cs, spLevelScale2[qsRem][cls], shift+qsPer)
}

// spBlock reconstructs one 4x4 block of a plane in place: dst holds the
// prediction on entry and the decoded samples on return. lev holds the raw
// levels in raster order.
func spBlock(dst []uint16, stride int, lev []int32, qp, qs int, switching bool) {
	var cp [16]int
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			cp[y*4+x] = int(dst[y*stride+x])
		}
	}
	spFwd4(&cp)
	var coef [16]int32
	qpRem, qpPer := qp%6, qp/6
	qsRem, qsPer := qs%6, qs/6
	for r := 0; r < 16; r++ {
		q := spCombine(cp[r], int(lev[r]), spCls[r], switching, qpRem, qpPer, qsRem, qsPer, 15, 6)
		coef[r] = dequant4(int32(q), int32(16*spNormAdjust[qsRem][spCls[r]]), qs)
	}
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			dst[y*stride+x] = 0
		}
	}
	idct4Add(dst, stride, coef[:], 255)
}

// spLuma reconstructs the 16 luma blocks of the current macroblock from the
// prediction in the picture.
func (d *Decoder) spLuma(switching bool) {
	pl, _ := d.planeOf(0)
	stride := d.mbStr[0]
	qp, qs := d.qp, d.hdr.qs
	for blk := 0; blk < 16; blk++ {
		d.spLumaBlock(pl, stride, blk, qp, qs, switching)
	}
}

// spLumaBlock reconstructs the luma 4x4 block blk.
func (d *Decoder) spLumaBlock(pl []uint16, stride, blk, qp, qs int, switching bool) {
	off := d.blockOff(0, blkX[blk], blkY[blk])
	spBlock(pl[off:], stride, d.coef[blk*16:blk*16+16], qp, qs, switching)
}

// spChroma reconstructs both 8x8 chroma blocks of the current macroblock
// (8.6.1 / 8.6.2, chroma transform coefficient decoding).
func (d *Decoder) spChroma(switching bool) {
	pps := d.hdr.pps
	for c := 1; c <= 2; c++ {
		pl, _ := d.planeOf(c)
		off := pps.ChromaQPOffset[c-1]
		var offs [4]int
		for b := range offs {
			offs[b] = d.blockOff(c, b&1, b>>1)
		}
		var lev [4][16]int32
		for b := range lev {
			copy(lev[b][:], d.coef[c*256+b*16:c*256+b*16+16])
		}
		spChromaPlane(pl, d.mbStr[c], offs, &lev, d.chromaQPPrime(d.qp, off), d.chromaQPPrime(d.hdr.qs, off), switching)
	}
}

// spChromaPlane reconstructs the four 4x4 blocks (at sample offsets offs,
// raster order of the 2x2 block grid) of one 4:2:0 chroma component in place.
// lev holds the raw levels of every block with the chroma DC level in
// position 0; qpc and qsc are QPc and QSc.
func spChromaPlane(pl []uint16, stride int, offs [4]int, lev *[4][16]int32, qpc, qsc int, switching bool) {
	qpRem, qpPer := qpc%6, qpc/6
	qsRem, qsPer := qsc%6, qsc/6

	// transformed predictions of the four blocks
	var cp [4][16]int
	for b := 0; b < 4; b++ {
		for y := 0; y < 4; y++ {
			for x := 0; x < 4; x++ {
				cp[b][y*4+x] = int(pl[offs[b]+y*stride+x])
			}
		}
		spFwd4(&cp[b])
	}
	// 2x2 transform of the DC coefficients, requantisation, and the scaling
	// and transform of the quantised DC values (8.5.11.2 with qP = QSc)
	a, b, cc, dd := cp[0][0], cp[1][0], cp[2][0], cp[3][0]
	dcp := [4]int{a + b + cc + dd, a - b + cc - dd, a + b - cc - dd, a - b - cc + dd}
	var dcq [8]int32
	for k := 0; k < 4; k++ {
		dcq[k] = int32(spCombine(dcp[k], int(lev[k][0]), 0, switching, qpRem, qpPer, qsRem, qsPer, 16, 5))
	}
	chromaDCTransform(&dcq, int32(16*spNormAdjust[qsRem][0]), qsc)

	for b := 0; b < 4; b++ {
		var coef [16]int32
		coef[0] = dcq[b]
		for r := 1; r < 16; r++ {
			q := spCombine(cp[b][r], int(lev[b][r]), spCls[r], switching, qpRem, qpPer, qsRem, qsPer, 15, 6)
			coef[r] = dequant4(int32(q), int32(16*spNormAdjust[qsRem][spCls[r]]), qsc)
		}
		for y := 0; y < 4; y++ {
			for x := 0; x < 4; x++ {
				pl[offs[b]+y*stride+x] = 0
			}
		}
		idct4Add(pl[offs[b]:], stride, coef[:], 255)
	}
}

// spInter reconstructs an inter macroblock of an SP slice whose prediction
// is already in the picture; the raw levels are in d.coef (all zero for
// skipped macroblocks and macroblocks without coded levels).
func (d *Decoder) spInter() {
	sw := d.hdr.spSwitch
	d.spLuma(sw)
	d.spChroma(sw)
}

// clearCoef zeroes the coefficient buffer of the macroblock.
func (d *Decoder) clearCoef() {
	for i := range d.coef {
		d.coef[i] = 0
	}
}
