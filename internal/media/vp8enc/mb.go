package vp8enc

import "math/bits"

// nzCtx is the "has coefficients" context of one edge of a macroblock.
type nzCtx struct {
	y  [4]uint8
	u  [2]uint8
	v  [2]uint8
	y2 uint8
}

// quantSet holds the dequantisation steps for a frame's quantiser index.
type quantSet struct {
	y1dc, y1ac, y2dc, y2ac, uvdc, uvac int32
	// reciprocal multipliers (16.16) for the quantiser
	inv [6]int32
}

func newQuantSet(q int) quantSet {
	var s quantSet
	s.y1dc = int32(dcTab[clampI(q, 0, 127)])
	s.y1ac = int32(acTab[clampI(q, 0, 127)])
	s.y2dc = int32(dcTab[clampI(q, 0, 127)]) * 2
	s.y2ac = int32(acTab[clampI(q, 0, 127)]) * 155 / 100
	if s.y2ac < 8 {
		s.y2ac = 8
	}
	s.uvdc = int32(dcTab[clampI(q, 0, 117)])
	s.uvac = int32(acTab[clampI(q, 0, 127)])
	for i, st := range [6]int32{s.y1dc, s.y1ac, s.y2dc, s.y2ac, s.uvdc, s.uvac} {
		s.inv[i] = int32(65536 / st)
	}
	return s
}

func clampI(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// quantBlock quantises the raster coefficients in into levels in coded order, starting at
// coded index first, and returns the index after the last non-zero level.
// dcRound and acRound are rounding offsets in 1/256 of a step.
func quantBlock(in *[16]int16, lv *[16]int16, first int, dcStep, acStep, dcInv, acInv int32, dcRound, acRound int32) int {
	eob := 0
	for i := 0; i < first; i++ {
		lv[i] = 0
	}
	for i := first; i < 16; i++ {
		c := int32(in[zigzag[i]])
		step, inv, rnd := acStep, acInv, acRound
		if i == 0 {
			step, inv, rnd = dcStep, dcInv, dcRound
		}
		a := c
		if a < 0 {
			a = -a
		}
		l := ((a + (step*rnd)>>8) * inv) >> 16
		if l > 2047+66 {
			l = 2047 + 66
		}
		if c < 0 {
			l = -l
		}
		lv[i] = int16(l)
		if l != 0 {
			eob = i + 1
		}
	}
	return eob
}

// worker holds the scratch space of one analysis goroutine.
type worker struct {
	e *Encoder

	predY      [256]byte
	predU      [64]byte
	predV      [64]byte
	candY      [256]byte
	candU      [64]byte
	candV      [64]byte
	mc         [21 * 16]byte
	resid      [16]int16
	coef       [25][16]int16
	lv         [25][16]int16
	eob        [25]int
	left       nzCtx
	cnt        tokenCounts
	nIntra     int
	nRef       [4]int
	nSkip      int
	nNoSkip    int
	newMV      int
	sumQ       int
	sad        int // luma SAD of the chosen prediction, summed (diagnostics / rate control)
	zeroBlocks int
}

// codeRow analyses and codes one macroblock row.
func (w *worker) codeRow(mby int) {
	e := w.e
	w.left = nzCtx{}
	toks := e.rowToks[mby][:0]
	for mbx := 0; mbx < e.mbw; mbx++ {
		if mby > 0 {
			e.waitRow(mby-1, mbx+1)
		}
		toks = w.codeMB(mbx, mby, toks)
		e.publishRow(mby, mbx+1)
	}
	e.rowToks[mby] = toks
}

func (w *worker) codeMB(mbx, mby int, toks []tokEntry) []tokEntry {
	e := w.e
	idx := mby*e.mbw + mbx
	ii := (mby+1)*e.miStride + mbx + 1
	out := &e.mbOut[idx]
	*out = mbOut{}
	x0, y0 := mbx*16, mby*16
	sy, su, sv := e.src.y, e.src.u, e.src.v
	srcY := sy.pix[sy.idx(x0, y0):]
	srcU := su.pix[su.idx(x0/2, y0/2):]
	srcV := sv.pix[sv.idx(x0/2, y0/2):]
	ys, cs := sy.stride, su.stride

	info := &e.infos[ii]
	if e.key {
		mode, _ := w.bestIntraY(mbx, mby, srcY, ys)
		out.ref, out.mode = refIntra, uint8(mode)
		out.uvmode = uint8(w.bestIntraUV(mbx, mby, srcU, srcV, cs))
		w.nIntra++
		*info = mbInfo{ref: refIntra, mode: mbDC}
	} else {
		w.chooseInter(mbx, mby, ii, srcY, ys, srcU, srcV, cs, out)
		*info = mbInfo{ref: out.ref, mode: out.mode, mv: out.mv}
	}
	e.mvField[idx] = out.mv

	// residual, quantisation, reconstruction
	return w.residual(mbx, mby, srcY, ys, srcU, srcV, cs, out, toks)
}

// bestIntraY picks the best 16x16 intra mode by SAD and leaves its prediction in w.predY.
func (w *worker) bestIntraY(mbx, mby int, srcY []byte, ys int) (mode, sad int) {
	cur := w.e.cur.y
	best := -1
	for m := 0; m < 4; m++ {
		predictIntra(cur, mbx*16, mby*16, 16, m, w.candY[:])
		s := sadBlock(srcY, ys, w.candY[:], 16, 16, 16)
		if best < 0 || s < sad {
			best, sad = m, s
			w.predY = w.candY
		}
	}
	return best, sad
}

func (w *worker) bestIntraUV(mbx, mby int, srcU, srcV []byte, cs int) int {
	e := w.e
	best, bs := -1, 0
	for m := 0; m < 4; m++ {
		predictIntra(e.cur.u, mbx*8, mby*8, 8, m, w.candU[:])
		predictIntra(e.cur.v, mbx*8, mby*8, 8, m, w.candV[:])
		s := sadBlock(srcU, cs, w.candU[:], 8, 8, 8) + sadBlock(srcV, cs, w.candV[:], 8, 8, 8)
		if best < 0 || s < bs {
			best, bs = m, s
			w.predU, w.predV = w.candU, w.candV
		}
	}
	return best
}

// residual transforms, quantises and reconstructs the macroblock given the prediction in
// w.pred*, appends its tokens and fills in the skip flag.
func (w *worker) residual(mbx, mby int, srcY []byte, ys int, srcU, srcV []byte, cs int, out *mbOut, toks []tokEntry) []tokEntry {
	e := w.e
	q := &e.qs
	// forward transforms
	for b := 0; b < 16; b++ {
		bx, by := (b&3)*4, (b>>2)*4
		for j := 0; j < 4; j++ {
			s := srcY[(by+j)*ys+bx : (by+j)*ys+bx+4]
			p := w.predY[(by+j)*16+bx : (by+j)*16+bx+4]
			w.resid[j*4+0] = int16(s[0]) - int16(p[0])
			w.resid[j*4+1] = int16(s[1]) - int16(p[1])
			w.resid[j*4+2] = int16(s[2]) - int16(p[2])
			w.resid[j*4+3] = int16(s[3]) - int16(p[3])
		}
		fdct4x4(&w.resid, &w.coef[b])
	}
	var y2in [16]int16
	for b := 0; b < 16; b++ {
		y2in[b] = w.coef[b][0]
	}
	fwht4x4(&y2in, &w.coef[24])
	for c := 0; c < 2; c++ {
		src, pred := srcU, w.predU[:]
		if c == 1 {
			src, pred = srcV, w.predV[:]
		}
		for b := 0; b < 4; b++ {
			bx, by := (b&1)*4, (b>>1)*4
			for j := 0; j < 4; j++ {
				s := src[(by+j)*cs+bx : (by+j)*cs+bx+4]
				p := pred[(by+j)*8+bx : (by+j)*8+bx+4]
				w.resid[j*4+0] = int16(s[0]) - int16(p[0])
				w.resid[j*4+1] = int16(s[1]) - int16(p[1])
				w.resid[j*4+2] = int16(s[2]) - int16(p[2])
				w.resid[j*4+3] = int16(s[3]) - int16(p[3])
			}
			fdct4x4(&w.resid, &w.coef[16+c*4+b])
		}
	}

	// quantisation (a dead zone: rounding below one half keeps the stream small)
	dcR, acR := int32(e.roundDC), int32(e.roundAC)
	if out.ref != refIntra {
		acR = int32(e.roundACInter)
	}
	any := false
	w.eob[24] = quantBlock(&w.coef[24], &w.lv[24], 0, q.y2dc, q.y2ac, q.inv[2], q.inv[3], dcR, acR)
	if w.eob[24] > 0 {
		any = true
	}
	for b := 0; b < 16; b++ {
		w.eob[b] = quantBlock(&w.coef[b], &w.lv[b], 1, q.y1dc, q.y1ac, q.inv[0], q.inv[1], dcR, acR)
		if w.eob[b] > 1 {
			any = true
		}
	}
	for b := 16; b < 24; b++ {
		w.eob[b] = quantBlock(&w.coef[b], &w.lv[b], 0, q.uvdc, q.uvac, q.inv[4], q.inv[5], dcR, acR)
		if w.eob[b] > 0 {
			any = true
		}
	}
	out.skip = !any

	// reconstruction into the current frame
	cur := e.cur
	dy := cur.y.pix[cur.y.idx(mbx*16, mby*16):]
	for j := 0; j < 16; j++ {
		copy(dy[j*cur.y.stride:j*cur.y.stride+16], w.predY[j*16:j*16+16])
	}
	du := cur.u.pix[cur.u.idx(mbx*8, mby*8):]
	dv := cur.v.pix[cur.v.idx(mbx*8, mby*8):]
	for j := 0; j < 8; j++ {
		copy(du[j*cur.u.stride:j*cur.u.stride+8], w.predU[j*8:j*8+8])
		copy(dv[j*cur.v.stride:j*cur.v.stride+8], w.predV[j*8:j*8+8])
	}
	if any {
		var y2dq, dcs [16]int16
		for i := 0; i < w.eob[24]; i++ {
			st := q.y2ac
			if i == 0 {
				st = q.y2dc
			}
			y2dq[zigzag[i]] = int16(int32(w.lv[24][i]) * st)
		}
		if w.eob[24] > 0 {
			iwht4x4(&y2dq, &dcs)
		}
		for b := 0; b < 16; b++ {
			dst := dy[(b>>2)*4*cur.y.stride+(b&3)*4:]
			if w.eob[b] > 1 {
				var dq [16]int16
				dq[0] = dcs[b]
				for i := 1; i < w.eob[b]; i++ {
					dq[zigzag[i]] = int16(int32(w.lv[b][i]) * q.y1ac)
				}
				idct4x4Add(&dq, dst, cur.y.stride)
			} else if dcs[b] != 0 {
				idctDCAdd(dcs[b], dst, cur.y.stride)
			}
		}
		for b := 16; b < 24; b++ {
			var dst []byte
			cb := b - 16
			pl := cur.u
			base := du
			if cb >= 4 {
				cb -= 4
				pl, base = cur.v, dv
			}
			dst = base[(cb>>1)*4*pl.stride+(cb&1)*4:]
			if w.eob[b] > 1 {
				var dq [16]int16
				for i := 0; i < w.eob[b]; i++ {
					st := q.uvac
					if i == 0 {
						st = q.uvdc
					}
					dq[zigzag[i]] = int16(int32(w.lv[b][i]) * st)
				}
				idct4x4Add(&dq, dst, pl.stride)
			} else if w.eob[b] == 1 {
				idctDCAdd(int16(int32(w.lv[b][0])*q.uvdc), dst, pl.stride)
			}
		}
	}

	// tokens
	above := &e.above[mbx]
	left := &w.left
	if out.skip {
		*above = nzCtx{}
		*left = nzCtx{}
		w.nSkip++
		return toks
	}
	w.nNoSkip++
	var nz bool
	toks, nz = emitBlock(toks, &w.cnt, planeY2, int(above.y2+left.y2), &w.lv[24], 0, w.eob[24])
	above.y2, left.y2 = b2u(nz), b2u(nz)
	for b := 0; b < 16; b++ {
		i, j := b&3, b>>2
		toks, nz = emitBlock(toks, &w.cnt, planeY1WithY2, int(above.y[i]+left.y[j]), &w.lv[b], 1, w.eob[b])
		above.y[i], left.y[j] = b2u(nz), b2u(nz)
	}
	for b := 16; b < 20; b++ {
		i, j := (b-16)&1, (b-16)>>1
		toks, nz = emitBlock(toks, &w.cnt, planeUV, int(above.u[i]+left.u[j]), &w.lv[b], 0, w.eob[b])
		above.u[i], left.u[j] = b2u(nz), b2u(nz)
	}
	for b := 20; b < 24; b++ {
		i, j := (b-20)&1, (b-20)>>1
		toks, nz = emitBlock(toks, &w.cnt, planeUV, int(above.v[i]+left.v[j]), &w.lv[b], 0, w.eob[b])
		above.v[i], left.v[j] = b2u(nz), b2u(nz)
	}
	return toks
}

func b2u(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}

// mvBits approximates the cost in bits of a vector component difference (eighth-pel units).
func mvBits(d int) int {
	if d < 0 {
		d = -d
	}
	d >>= 1 // quarter-pel units as coded
	if d == 0 {
		return 1
	}
	if d < 8 {
		return 4
	}
	return 4 + 2*bits.Len(uint(d))
}
