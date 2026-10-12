package h264

import (
	"math/rand"
	"testing"
)

// An independent model of the SP / SI transform-domain reconstruction,
// transcribed from the structure of the JM reference decoder (JM 9.8,
// ldecod/src/block.c itrans_sp and itrans_sp_chroma, with the chroma QP offset
// handled per spec) rather than from this package's spBlock / spChromaPlane.
// Arrays are indexed [x][y] as in JM.

var jmQuant = [6][4][4]int{
	{{13107, 8066, 13107, 8066}, {8066, 5243, 8066, 5243}, {13107, 8066, 13107, 8066}, {8066, 5243, 8066, 5243}},
	{{11916, 7490, 11916, 7490}, {7490, 4660, 7490, 4660}, {11916, 7490, 11916, 7490}, {7490, 4660, 7490, 4660}},
	{{10082, 6554, 10082, 6554}, {6554, 4194, 6554, 4194}, {10082, 6554, 10082, 6554}, {6554, 4194, 6554, 4194}},
	{{9362, 5825, 9362, 5825}, {5825, 3647, 5825, 3647}, {9362, 5825, 9362, 5825}, {5825, 3647, 5825, 3647}},
	{{8192, 5243, 8192, 5243}, {5243, 3355, 5243, 3355}, {8192, 5243, 8192, 5243}, {5243, 3355, 5243, 3355}},
	{{7282, 4559, 7282, 4559}, {4559, 2893, 4559, 2893}, {7282, 4559, 7282, 4559}, {4559, 2893, 4559, 2893}},
}

var jmDequant = [6][4][4]int{
	{{10, 13, 10, 13}, {13, 16, 13, 16}, {10, 13, 10, 13}, {13, 16, 13, 16}},
	{{11, 14, 11, 14}, {14, 18, 14, 18}, {11, 14, 11, 14}, {14, 18, 14, 18}},
	{{13, 16, 13, 16}, {16, 20, 16, 20}, {13, 16, 13, 16}, {16, 20, 16, 20}},
	{{14, 18, 14, 18}, {18, 23, 18, 23}, {14, 18, 14, 18}, {18, 23, 18, 23}},
	{{16, 20, 16, 20}, {20, 25, 20, 25}, {16, 20, 16, 20}, {20, 25, 20, 25}},
	{{18, 23, 18, 23}, {23, 29, 23, 29}, {18, 23, 18, 23}, {23, 29, 23, 29}},
}

var jmA = [4][4]int{{16, 20, 16, 20}, {20, 25, 20, 25}, {16, 20, 16, 20}, {20, 25, 20, 25}}

func jmAbs(a int) int {
	if a < 0 {
		return -a
	}
	return a
}

func jmSign(a, b int) int {
	if b > 0 {
		return jmAbs(a)
	}
	return -jmAbs(a)
}

// jmFwd is the forward transform of a [x][y] block (horizontal then vertical).
func jmFwd(p *[4][4]int) {
	var m5 [4]int
	for j := 0; j < 4; j++ {
		for i := 0; i < 2; i++ {
			i1 := 3 - i
			m5[i] = p[i][j] + p[i1][j]
			m5[i1] = p[i][j] - p[i1][j]
		}
		p[0][j] = m5[0] + m5[1]
		p[2][j] = m5[0] - m5[1]
		p[1][j] = m5[3]*2 + m5[2]
		p[3][j] = m5[3] - m5[2]*2
	}
	for i := 0; i < 4; i++ {
		for j := 0; j < 2; j++ {
			j1 := 3 - j
			m5[j] = p[i][j] + p[i][j1]
			m5[j1] = p[i][j] - p[i][j1]
		}
		p[i][0] = m5[0] + m5[1]
		p[i][2] = m5[0] - m5[1]
		p[i][1] = m5[3]*2 + m5[2]
		p[i][3] = m5[3] - m5[2]*2
	}
}

// jmInv is the inverse transform of dequantised [x][y] coefficients with the
// final (x + 32) >> 6 and clipping to 0..255.
func jmInv(c [4][4]int) (out [4][4]int) {
	var m5, m6 [4]int
	var m7 [4][4]int
	for j := 0; j < 4; j++ {
		for i := 0; i < 4; i++ {
			m5[i] = c[i][j]
		}
		m6[0] = m5[0] + m5[2]
		m6[1] = m5[0] - m5[2]
		m6[2] = (m5[1] >> 1) - m5[3]
		m6[3] = m5[1] + (m5[3] >> 1)
		for i := 0; i < 2; i++ {
			i1 := 3 - i
			m7[i][j] = m6[i] + m6[i1]
			m7[i1][j] = m6[i] - m6[i1]
		}
	}
	for i := 0; i < 4; i++ {
		for j := 0; j < 4; j++ {
			m5[j] = m7[i][j]
		}
		m6[0] = m5[0] + m5[2]
		m6[1] = m5[0] - m5[2]
		m6[2] = (m5[1] >> 1) - m5[3]
		m6[3] = m5[1] + (m5[3] >> 1)
		for j := 0; j < 2; j++ {
			j1 := 3 - j
			out[i][j] = max(0, min(255, (m6[j]+m6[j1]+32)>>6))
			out[i][j1] = max(0, min(255, (m6[j]-m6[j1]+32)>>6))
		}
	}
	return
}

// jmLuma is itrans_sp.
func jmLuma(pred [4][4]int, lev [4][4]int, qp, qsp int, sw bool) [4][4]int {
	qpPer, qpRem := qp/6, qp%6
	qsPer, qsRem := qsp/6, qsp%6
	qBitsSP := 15 + qsPer
	qpConst2 := (1 << uint(qBitsSP)) / 2
	jmFwd(&pred)
	var cof [4][4]int
	for j := 0; j < 4; j++ {
		for i := 0; i < 4; i++ {
			if sw {
				ilev := (jmAbs(pred[i][j])*jmQuant[qsRem][i][j] + qpConst2) >> uint(qBitsSP)
				ilev = jmSign(ilev, pred[i][j]) + lev[i][j]
				cof[i][j] = jmSign((jmAbs(ilev)*jmDequant[qsRem][i][j])<<uint(qsPer), ilev)
			} else {
				ilev := (((lev[i][j] * jmDequant[qpRem][i][j] * jmA[i][j]) << uint(qpPer)) >> 6) + pred[i][j]
				cof[i][j] = jmSign((jmAbs(ilev)*jmQuant[qsRem][i][j]+qpConst2)>>uint(qBitsSP), ilev) * jmDequant[qsRem][i][j] << uint(qsPer)
			}
		}
	}
	return jmInv(cof)
}

// jmChroma is itrans_sp_chroma for one component: pred is the 8x8 prediction
// [x][y]; dc the four chroma DC levels (raster 2x2); ac[blk][x][y] the AC
// levels of block blk = x + 2*y (AC position 0 unused).
func jmChroma(pred [8][8]int, dc [4]int, ac [4][4][4]int, qpc, qsc int, sw bool) (out [8][8]int) {
	qpPer, qpRem := qpc/6, qpc%6
	qsPer, qsRem := qsc/6, qsc%6
	qBitsSP := 15 + qsPer
	qpConst2 := (1 << uint(qBitsSP)) / 2
	var blk [4][4][4]int // transformed predictions per block [blk][x][y]
	for b := 0; b < 4; b++ {
		var p [4][4]int
		for x := 0; x < 4; x++ {
			for y := 0; y < 4; y++ {
				p[x][y] = pred[(b&1)*4+x][(b>>1)*4+y]
			}
		}
		jmFwd(&p)
		blk[b] = p
	}
	mp1 := [4]int{
		blk[0][0][0] + blk[1][0][0] + blk[2][0][0] + blk[3][0][0],
		blk[0][0][0] - blk[1][0][0] + blk[2][0][0] - blk[3][0][0],
		blk[0][0][0] + blk[1][0][0] - blk[2][0][0] - blk[3][0][0],
		blk[0][0][0] - blk[1][0][0] - blk[2][0][0] + blk[3][0][0],
	}
	for k := 0; k < 4; k++ {
		if sw {
			ilev := (jmAbs(mp1[k])*jmQuant[qsRem][0][0] + 2*qpConst2) >> uint(qBitsSP+1)
			ilev = dc[k] + jmSign(ilev, mp1[k])
			mp1[k] = (ilev * jmDequant[qsRem][0][0]) << uint(qsPer)
		} else {
			ilev := (((dc[k] * jmDequant[qpRem][0][0] * jmA[0][0]) << uint(qpPer)) >> 5) + mp1[k]
			mp1[k] = jmSign((jmAbs(ilev)*jmQuant[qsRem][0][0]+2*qpConst2)>>uint(qBitsSP+1), ilev) * jmDequant[qsRem][0][0] << uint(qsPer)
		}
	}
	dcOut := [4]int{
		(mp1[0] + mp1[1] + mp1[2] + mp1[3]) >> 1,
		(mp1[0] - mp1[1] + mp1[2] - mp1[3]) >> 1,
		(mp1[0] + mp1[1] - mp1[2] - mp1[3]) >> 1,
		(mp1[0] - mp1[1] - mp1[2] + mp1[3]) >> 1,
	}
	for b := 0; b < 4; b++ {
		var cof [4][4]int
		for i := 0; i < 4; i++ {
			for j := 0; j < 4; j++ {
				if i == 0 && j == 0 {
					continue
				}
				if sw {
					ilev := (jmAbs(blk[b][i][j])*jmQuant[qsRem][i][j] + qpConst2) >> uint(qBitsSP)
					ilev = jmSign(ilev, blk[b][i][j]) + ac[b][i][j]
					cof[i][j] = ilev * jmDequant[qsRem][i][j] << uint(qsPer)
				} else {
					ilev := (((ac[b][i][j] * jmDequant[qpRem][i][j] * jmA[i][j]) << uint(qpPer)) >> 6) + blk[b][i][j]
					cof[i][j] = jmSign((jmAbs(ilev)*jmQuant[qsRem][i][j]+qpConst2)>>uint(qBitsSP), ilev) * jmDequant[qsRem][i][j] << uint(qsPer)
				}
			}
		}
		cof[0][0] = dcOut[b]
		r := jmInv(cof)
		for x := 0; x < 4; x++ {
			for y := 0; y < 4; y++ {
				out[(b&1)*4+x][(b>>1)*4+y] = r[x][y]
			}
		}
	}
	return
}

func randLevel(rng *rand.Rand) int {
	switch rng.Intn(6) {
	case 0, 1, 2:
		return 0
	case 3:
		return rng.Intn(5) - 2
	case 4:
		return rng.Intn(21) - 10
	}
	return rng.Intn(201) - 100
}

func randBlockPix(rng *rand.Rand) int {
	if rng.Intn(4) == 0 {
		return []int{0, 255}[rng.Intn(2)]
	}
	return rng.Intn(256)
}

// TestSPLumaModel cross-checks the luma reconstruction against the model.
func TestSPLumaModel(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for it := 0; it < 200000; it++ {
		qp, qs := rng.Intn(52), rng.Intn(52)
		sw := rng.Intn(2) == 0
		var pred, lev [4][4]int
		dst := make([]uint16, 4*7)
		var raw [16]int32
		base := rng.Intn(256)
		for x := 0; x < 4; x++ {
			for y := 0; y < 4; y++ {
				v := base + rng.Intn(9) - 4
				if it%3 == 0 {
					v = randBlockPix(rng)
				}
				v = max(0, min(255, v))
				pred[x][y] = v
				dst[y*7+x] = uint16(v)
				l := randLevel(rng)
				lev[x][y] = l
				raw[y*4+x] = int32(l)
			}
		}
		want := jmLuma(pred, lev, qp, qs, sw)
		spBlock(dst, 7, raw[:], qp, qs, sw)
		for x := 0; x < 4; x++ {
			for y := 0; y < 4; y++ {
				if int(dst[y*7+x]) != want[x][y] {
					t.Fatalf("it %d qp %d qs %d sw %v: (%d,%d) got %d want %d", it, qp, qs, sw, x, y, dst[y*7+x], want[x][y])
				}
			}
		}
	}
}

// TestSPChromaModel cross-checks the chroma reconstruction against the model.
func TestSPChromaModel(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	for it := 0; it < 100000; it++ {
		qp, qs := rng.Intn(52), rng.Intn(52)
		qpc, qsc := chromaQPf(qp), chromaQPf(qs)
		sw := rng.Intn(2) == 0
		var pred [8][8]int
		var dc [4]int
		var ac [4][4][4]int
		const stride = 11
		pl := make([]uint16, stride*8)
		var lev [4][16]int32
		base := rng.Intn(256)
		for x := 0; x < 8; x++ {
			for y := 0; y < 8; y++ {
				v := base + rng.Intn(13) - 6
				if it%3 == 0 {
					v = randBlockPix(rng)
				}
				v = max(0, min(255, v))
				pred[x][y] = v
				pl[y*stride+x] = uint16(v)
			}
		}
		for b := 0; b < 4; b++ {
			dc[b] = randLevel(rng)
			lev[b][0] = int32(dc[b])
			for x := 0; x < 4; x++ {
				for y := 0; y < 4; y++ {
					if x == 0 && y == 0 {
						continue
					}
					l := 0
					if rng.Intn(3) == 0 {
						l = randLevel(rng)
					}
					ac[b][x][y] = l
					lev[b][y*4+x] = int32(l)
				}
			}
		}
		want := jmChroma(pred, dc, ac, qpc, qsc, sw)
		var offs [4]int
		for b := range offs {
			offs[b] = (b>>1)*4*stride + (b&1)*4
		}
		spChromaPlane(pl, stride, offs, &lev, qpc, qsc, sw)
		for x := 0; x < 8; x++ {
			for y := 0; y < 8; y++ {
				if int(pl[y*stride+x]) != want[x][y] {
					t.Fatalf("it %d qpc %d qsc %d sw %v: (%d,%d) got %d want %d", it, qpc, qsc, sw, x, y, pl[y*stride+x], want[x][y])
				}
			}
		}
	}
}

// TestSPSwitchIdentity checks the defining property of switching SP pictures
// from the formulas of 8.6.1 and 8.6.2 only: for a prediction P, levels r of a
// primary SP slice (QP, QS) and the levels
//
//	r' = Q_QS(T(P) + dequant_QP(r)) - Q_QS(T(P))
//
// a switching SP slice (levels r', QS) reconstructs exactly the same samples.
func TestSPSwitchIdentity(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	quant := func(v, cls, qs, sh int) int {
		a := jmAbs(v)
		q := (a*spLevelScale2[qs%6][cls] + (1 << uint(sh+qs/6-1))) >> uint(sh+qs/6)
		if v < 0 {
			return -q
		}
		return q
	}
	for it := 0; it < 50000; it++ {
		qp, qs := rng.Intn(52), rng.Intn(52)
		dst0 := make([]uint16, 16)
		dst1 := make([]uint16, 16)
		var cp [16]int
		for i := range dst0 {
			v := uint16(randBlockPix(rng))
			dst0[i], dst1[i] = v, v
			cp[i] = int(v)
		}
		spFwd4(&cp)
		var r, r2 [16]int32
		for k := 0; k < 16; k++ {
			r[k] = int32(randLevel(rng))
			cls := spCls[k]
			cs := cp[k] + ((int(r[k]) * spNormAdjust[qp%6][cls] * spA[cls] << uint(qp/6)) >> 6)
			r2[k] = int32(quant(cs, cls, qs, 15) - quant(cp[k], cls, qs, 15))
		}
		spBlock(dst0, 4, r[:], qp, qs, false)
		spBlock(dst1, 4, r2[:], qp, qs, true)
		for i := range dst0 {
			if dst0[i] != dst1[i] {
				t.Fatalf("it %d qp %d qs %d: sample %d primary %d switching %d", it, qp, qs, i, dst0[i], dst1[i])
			}
		}
	}
}
