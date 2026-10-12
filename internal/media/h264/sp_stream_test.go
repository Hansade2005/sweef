package h264

import (
	"fmt"
	"math/rand"
	"testing"
)

// Synthetic SI / switching SP streams. No conformance stream with SI slices or
// with sp_for_switch_flag = 1 exists in the JVT suite (SP1_BT_A and sp2_bt_b
// carry only primary SP slices), so these tests generate such streams with a
// small CAVLC writer and compare the decoder with the model of
// sp_model_test.go. The model shares nothing with the decoder except the
// intra prediction functions (validated by the conformance suite).

func wu(w *bitWriter, n int, v int) {
	for i := n - 1; i >= 0; i-- {
		w.bit(v >> uint(i) & 1)
	}
}

func wse(w *bitWriter, v int) {
	if v > 0 {
		w.ue(2*v - 1)
	} else {
		w.ue(-2 * v)
	}
}

func nalUnit(refIdc, typ int, w *bitWriter) []byte {
	out := []byte{0, 0, 0, 1, byte(refIdc<<5 | typ)}
	return append(out, escapeRBSP(w.b)...)
}

// zigzag4Raster maps the scan position to y*4+x for frame macroblocks.
var zigzag4Raster = [16]int{0, 1, 4, 8, 5, 2, 3, 6, 9, 12, 13, 10, 7, 11, 14, 15}

// spec of the generated pictures
type spPic struct {
	mbw, mbh   int
	qp, qs     int
	qpDelta    int // slice_qp_delta
	chromaOff  int
	constraint bool
}

func (p *spPic) sps() []byte {
	w := &bitWriter{}
	wu(w, 8, 88)
	wu(w, 8, 0)
	wu(w, 8, 30)
	w.ue(0)
	w.ue(0) // log2_max_frame_num_minus4
	w.ue(2) // pic_order_cnt_type
	w.ue(1) // num_ref_frames
	w.bit(0)
	w.ue(p.mbw - 1)
	w.ue(p.mbh - 1)
	w.bit(1) // frame_mbs_only
	w.bit(1) // direct_8x8_inference
	w.bit(0)
	w.bit(0)
	w.trailing()
	return nalUnit(3, 7, w)
}

func (p *spPic) pps() []byte {
	w := &bitWriter{}
	w.ue(0)
	w.ue(0)
	w.bit(0) // CAVLC
	w.bit(0)
	w.ue(0)
	w.ue(0)
	w.ue(0)
	w.bit(0)
	wu(w, 2, 0)
	wse(w, 0) // pic_init_qp = 26
	wse(w, 0) // pic_init_qs = 26
	wse(w, p.chromaOff)
	w.bit(1) // deblocking_filter_control_present
	if p.constraint {
		w.bit(1)
	} else {
		w.bit(0)
	}
	w.bit(0)
	w.trailing()
	return nalUnit(3, 8, w)
}

// nzGrid tracks TotalCoeff per 4x4 block for the CAVLC nC derivation.
type nzGrid struct {
	w, h int
	v    []int
}

func newGrid(w, h int) *nzGrid { return &nzGrid{w, h, make([]int, w*h)} }

func (g *nzGrid) nC(x, y int) int {
	var a, b int
	av, bv := x > 0, y > 0
	if av {
		a = g.v[y*g.w+x-1]
	}
	if bv {
		b = g.v[(y-1)*g.w+x]
	}
	switch {
	case av && bv:
		return (a + b + 1) >> 1
	case av:
		return a
	case bv:
		return b
	}
	return 0
}

// cavlcBlock writes one residual block of coefficients in scan order and
// returns TotalCoeff.
func cavlcWrite(w *bitWriter, c []int, nC int) int {
	maxNum := len(c)
	var pos, lev []int
	for i, v := range c {
		if v != 0 {
			pos = append(pos, i)
			lev = append(lev, v)
		}
	}
	total := len(pos)
	t1 := 0
	for i := total - 1; i >= 0 && t1 < 3 && (lev[i] == 1 || lev[i] == -1); i-- {
		t1++
	}
	put := func(l, b uint8) {
		for i := int(l) - 1; i >= 0; i-- {
			w.bit(int(b>>uint(i)) & 1)
		}
	}
	sym := total*4 + t1
	switch {
	case nC == -1:
		put(chromaDCCoeffTokenLen[sym], chromaDCCoeffTokenBits[sym])
	case nC < 2:
		put(coeffTokenLen[0][sym], coeffTokenBits[0][sym])
	case nC < 4:
		put(coeffTokenLen[1][sym], coeffTokenBits[1][sym])
	case nC < 8:
		put(coeffTokenLen[2][sym], coeffTokenBits[2][sym])
	default:
		if total == 0 {
			wu(w, 6, 3)
		} else {
			wu(w, 6, (total-1)<<2|t1)
		}
	}
	if total == 0 {
		return 0
	}
	// levels in reverse scan order
	suffixLen := 0
	if total > 10 && t1 < 3 {
		suffixLen = 1
	}
	for i := 0; i < total; i++ {
		l := lev[total-1-i]
		if i < t1 {
			if l < 0 {
				w.bit(1)
			} else {
				w.bit(0)
			}
			continue
		}
		code := 2*l - 2
		if l < 0 {
			code = -2*l - 1
		}
		if i == t1 && t1 < 3 {
			code -= 2
		}
		switch {
		case suffixLen == 0 && code < 14:
			wu(w, code, 0)
			w.bit(1)
		case suffixLen == 0 && code < 30:
			wu(w, 14, 0)
			w.bit(1)
			wu(w, 4, code-14)
		case suffixLen > 0 && code < 15<<uint(suffixLen):
			wu(w, code>>uint(suffixLen), 0)
			w.bit(1)
			wu(w, suffixLen, code&(1<<uint(suffixLen)-1))
		default:
			wu(w, 15, 0)
			w.bit(1)
			if suffixLen == 0 {
				wu(w, 12, code-30)
			} else {
				wu(w, 12, code-15<<uint(suffixLen))
			}
		}
		if suffixLen == 0 {
			suffixLen = 1
		}
		al := l
		if al < 0 {
			al = -al
		}
		if al > 3<<uint(suffixLen-1) && suffixLen < 6 {
			suffixLen++
		}
	}
	zeros := pos[total-1] + 1 - total
	if total < maxNum {
		if maxNum == 4 {
			put(chromaDCTotalZerosLen[total-1][zeros], chromaDCTotalZerosBits[total-1][zeros])
		} else {
			put(totalZerosLen[total-1][zeros], totalZerosBits[total-1][zeros])
		}
	}
	zl := zeros
	for i := total - 1; i > 0 && zl > 0; i-- {
		run := pos[i] - pos[i-1] - 1
		k := min(zl, 7) - 1
		put(runLen[k][run], runBits[k][run])
		zl -= run
	}
	return total
}

// mbRes is the residual of one macroblock in the writer: levels in scan order.
type mbRes struct {
	cbp    int
	luma   [16][16]int // [blkIdx][scan]
	chDC   [2][4]int
	chAC   [2][4][15]int
	qpD    int
	hasQPD bool
}

func randRes(rng *rand.Rand, cbp int, mag int) *mbRes {
	r := &mbRes{cbp: cbp}
	lv := func() int {
		if rng.Intn(3) != 0 {
			return 0
		}
		v := rng.Intn(2*mag+1) - mag
		if rng.Intn(3) == 0 {
			v = rng.Intn(3) - 1
		}
		return v
	}
	for b8 := 0; b8 < 4; b8++ {
		if cbp>>uint(b8)&1 == 0 {
			continue
		}
		for k := 0; k < 4; k++ {
			for s := 0; s < 16; s++ {
				r.luma[b8*4+k][s] = lv()
			}
		}
	}
	if cbp>>4 != 0 {
		for c := 0; c < 2; c++ {
			for k := 0; k < 4; k++ {
				r.chDC[c][k] = lv()
			}
		}
	}
	if cbp>>4 == 2 {
		for c := 0; c < 2; c++ {
			for b := 0; b < 4; b++ {
				for s := 0; s < 15; s++ {
					r.chAC[c][b][s] = lv()
				}
			}
		}
	}
	return r
}

var blkXY = func() (xs, ys [16]int) {
	for b := 0; b < 16; b++ {
		xs[b] = b&1 + (b>>2&1)*2
		ys[b] = b>>1&1 + (b>>3)*2
	}
	return
}

// writeResidual writes residual() of a macroblock and updates the grids.
func writeResidual(w *bitWriter, r *mbRes, mx, my int, gl *nzGrid, gc [2]*nzGrid) {
	xs, ys := blkXY()
	for b8 := 0; b8 < 4; b8++ {
		for k := 0; k < 4; k++ {
			b := b8*4 + k
			gx, gy := mx*4+xs[b], my*4+ys[b]
			if r.cbp>>uint(b8)&1 == 0 {
				gl.v[gy*gl.w+gx] = 0
				continue
			}
			gl.v[gy*gl.w+gx] = cavlcWrite(w, r.luma[b][:], gl.nC(gx, gy))
		}
	}
	if r.cbp>>4 != 0 {
		for c := 0; c < 2; c++ {
			cavlcWrite(w, r.chDC[c][:], -1)
		}
	}
	for c := 0; c < 2; c++ {
		for b := 0; b < 4; b++ {
			gx, gy := mx*2+b&1, my*2+b>>1
			if r.cbp>>4 != 2 {
				gc[c].v[gy*gc[c].w+gx] = 0
				continue
			}
			gc[c].v[gy*gc[c].w+gx] = cavlcWrite(w, r.chAC[c][b][:], gc[c].nC(gx, gy))
		}
	}
}

func cbpCodeNum(table *[48]uint8, cbp int) int {
	for i, v := range table {
		if int(v) == cbp {
			return i
		}
	}
	panic("cbp")
}

// levels4 returns the raw luma levels of block b as lev[x][y].
func (r *mbRes) levels4(b int) (lev [4][4]int) {
	for s := 0; s < 16; s++ {
		p := zigzag4Raster[s]
		lev[p&3][p>>2] = r.luma[b][s]
	}
	return
}

func (r *mbRes) chromaLevels(c int) (dc [4]int, ac [4][4][4]int) {
	dc = r.chDC[c]
	for b := 0; b < 4; b++ {
		for s := 0; s < 15; s++ {
			p := zigzag4Raster[s+1]
			ac[b][p&3][p>>2] = r.chAC[c][b][s]
		}
	}
	return
}

// planes of the model / reference picture
type spPlanes struct {
	w, h int
	y    []uint16
	cb   []uint16
	cr   []uint16
}

func newPlanes(w, h int) *spPlanes {
	return &spPlanes{w, h, make([]uint16, w*h), make([]uint16, w*h/4), make([]uint16, w*h/4)}
}

func (p *spPlanes) i420() []byte {
	var b []byte
	for _, pl := range [][]uint16{p.y, p.cb, p.cr} {
		for _, v := range pl {
			b = append(b, byte(v))
		}
	}
	return b
}

func chromaQP(qp, off int) int { return chromaQPf(max(0, min(51, qp+off))) }

// writeIDRPCM returns an IDR I slice of random I_PCM macroblocks and the
// resulting picture.
func (p *spPic) idrPCM(rng *rand.Rand) ([]byte, *spPlanes) {
	pl := newPlanes(p.mbw*16, p.mbh*16)
	w := &bitWriter{}
	w.ue(0)
	w.ue(2)
	w.ue(0)
	wu(w, 4, 0)
	w.ue(0)
	w.bit(0)
	w.bit(0)
	wse(w, p.qp-26)
	w.ue(1)
	for my := 0; my < p.mbh; my++ {
		for mx := 0; mx < p.mbw; mx++ {
			w.ue(25)
			w.align()
			for y := 0; y < 16; y++ {
				for x := 0; x < 16; x++ {
					v := (mx*37+my*91+x*5+y*3)&0xff ^ rng.Intn(48)
					if rng.Intn(20) == 0 {
						v = rng.Intn(256)
					}
					pl.y[(my*16+y)*pl.w+mx*16+x] = uint16(v)
					wu(w, 8, v)
				}
			}
			for c := 0; c < 2; c++ {
				dst := pl.cb
				if c == 1 {
					dst = pl.cr
				}
				for y := 0; y < 8; y++ {
					for x := 0; x < 8; x++ {
						v := (mx*53+my*29+x*11+y*7+c*60)&0xff ^ rng.Intn(40)
						dst[(my*8+y)*(pl.w/2)+mx*8+x] = uint16(v)
						wu(w, 8, v)
					}
				}
			}
		}
	}
	w.trailing()
	return nalUnit(3, 5, w), pl
}

// spSlice builds a (switching or primary) SP slice of P_L0_16x16 (zero motion
// vector) and skipped macroblocks on top of ref, and the expected picture.
func (p *spPic) spSlice(rng *rand.Rand, ref *spPlanes, switching bool) ([]byte, *spPlanes) {
	out := newPlanes(ref.w, ref.h)
	w := &bitWriter{}
	w.ue(0)
	w.ue(3)
	w.ue(0)
	wu(w, 4, 1)
	w.bit(0)
	w.bit(0)
	w.bit(0)
	wse(w, p.qp-26)
	if switching {
		w.bit(1)
	} else {
		w.bit(0)
	}
	wse(w, p.qs-26)
	w.ue(1)
	gl := newGrid(p.mbw*4, p.mbh*4)
	gc := [2]*nzGrid{newGrid(p.mbw*2, p.mbh*2), newGrid(p.mbw*2, p.mbh*2)}
	qp := p.qp
	skip := 0
	for my := 0; my < p.mbh; my++ {
		for mx := 0; mx < p.mbw; mx++ {
			var res *mbRes
			if rng.Intn(4) != 0 {
				cbp := rng.Intn(48)
				res = randRes(rng, cbp, 12)
				if rng.Intn(5) == 0 {
					res = randRes(rng, cbp, 60)
				}
				res.qpD = rng.Intn(9) - 4
				if cbp == 0 {
					res.qpD = 0
				}
			}
			if res == nil {
				skip++
				for b := 0; b < 16; b++ {
					xs, ys := blkXY()
					gl.v[(my*4+ys[b])*gl.w+mx*4+xs[b]] = 0
				}
				for c := 0; c < 2; c++ {
					for b := 0; b < 4; b++ {
						gc[c].v[(my*2+b>>1)*gc[c].w+mx*2+b&1] = 0
					}
				}
			} else {
				w.ue(skip)
				skip = 0
				w.ue(0) // P_L0_16x16
				wse(w, 0)
				wse(w, 0)
				w.ue(cbpCodeNum(&golombToInterCBP, res.cbp))
				if res.cbp != 0 {
					wse(w, res.qpD)
					qp = (qp + res.qpD + 52) % 52
					writeResidual(w, res, mx, my, gl, gc)
				} else {
					for b := 0; b < 16; b++ {
						xs, ys := blkXY()
						gl.v[(my*4+ys[b])*gl.w+mx*4+xs[b]] = 0
					}
					for c := 0; c < 2; c++ {
						for b := 0; b < 4; b++ {
							gc[c].v[(my*2+b>>1)*gc[c].w+mx*2+b&1] = 0
						}
					}
				}
			}
			// model
			xs, ys := blkXY()
			for b := 0; b < 16; b++ {
				var pred, lev [4][4]int
				for x := 0; x < 4; x++ {
					for y := 0; y < 4; y++ {
						pred[x][y] = int(ref.y[(my*16+ys[b]*4+y)*ref.w+mx*16+xs[b]*4+x])
					}
				}
				if res != nil {
					lev = res.levels4(b)
				}
				rec := jmLuma(pred, lev, qp, p.qs, switching)
				for x := 0; x < 4; x++ {
					for y := 0; y < 4; y++ {
						out.y[(my*16+ys[b]*4+y)*ref.w+mx*16+xs[b]*4+x] = uint16(rec[x][y])
					}
				}
			}
			for c := 0; c < 2; c++ {
				src, dst := ref.cb, out.cb
				if c == 1 {
					src, dst = ref.cr, out.cr
				}
				cw := ref.w / 2
				var pred [8][8]int
				for x := 0; x < 8; x++ {
					for y := 0; y < 8; y++ {
						pred[x][y] = int(src[(my*8+y)*cw+mx*8+x])
					}
				}
				var dc [4]int
				var ac [4][4][4]int
				if res != nil {
					dc, ac = res.chromaLevels(c)
				}
				rec := jmChroma(pred, dc, ac, chromaQP(qp, p.chromaOff), chromaQP(p.qs, p.chromaOff), switching)
				for x := 0; x < 8; x++ {
					for y := 0; y < 8; y++ {
						dst[(my*8+y)*cw+mx*8+x] = uint16(rec[x][y])
					}
				}
			}
		}
	}
	if skip > 0 {
		w.ue(skip)
	}
	w.trailing()
	return nalUnit(2, 1, w), out
}

// TestSPSyntheticStreams decodes generated IDR(I_PCM) + SP streams, with
// sp_for_switch_flag 0 and 1, many QP/QS/chroma offset combinations.
func TestSPSyntheticStreams(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for it := 0; it < 60; it++ {
		p := &spPic{mbw: 3, mbh: 3, qp: 10 + rng.Intn(35), qs: 6 + rng.Intn(40), chromaOff: rng.Intn(9) - 4}
		if it%10 == 0 {
			p.qp, p.qs = rng.Intn(52), rng.Intn(52)
		}
		sw := it%2 == 1
		idr, ref := p.idrPCM(rng)
		sp, want := p.spSlice(rng, ref, sw)
		es := append(append(append(p.sps(), p.pps()...), idr...), sp...)
		name := fmt.Sprintf("it%d_qp%d_qs%d_off%d_sw%v", it, p.qp, p.qs, p.chromaOff, sw)
		t.Run(name, func(t *testing.T) {
			frames := decodeToI420(t, es)
			if len(frames) != 2 {
				t.Fatalf("%d frames", len(frames))
			}
			if string(frames[0]) != string(ref.i420()) {
				t.Fatal("IDR picture differs")
			}
			got, exp := frames[1], want.i420()
			for i := range got {
				if got[i] != exp[i] {
					t.Fatalf("sample %d: got %d want %d", i, got[i], exp[i])
				}
			}
		})
	}
}

// siMB describes a macroblock of the generated SI picture.
type siMB struct {
	kind  int // 0 SI, 1 Intra_4x4, 2 I_PCM
	modes [16]int
}

// siPicture builds an IDR picture made of an SI slice with SI, Intra_4x4
// (cbp 0) and I_PCM macroblocks, together with the expected samples.
func (p *spPic) siPicture(rng *rand.Rand, siShare int) ([]byte, *spPlanes) {
	out := newPlanes(p.mbw*16, p.mbh*16)
	w := &bitWriter{}
	w.ue(0)
	w.ue(4) // SI
	w.ue(0)
	wu(w, 4, 0)
	w.ue(0)
	w.bit(0)
	w.bit(0)
	wse(w, p.qp-26)
	wse(w, p.qs-26)
	w.ue(1)
	gl := newGrid(p.mbw*4, p.mbh*4)
	gc := [2]*nzGrid{newGrid(p.mbw*2, p.mbh*2), newGrid(p.mbw*2, p.mbh*2)}
	mbs := make([]siMB, p.mbw*p.mbh)
	qp := p.qp
	xs, ys := blkXY()
	inPic := func(mx, my int) bool { return mx >= 0 && my >= 0 && mx < p.mbw && my < p.mbh }
	for my := 0; my < p.mbh; my++ {
		for mx := 0; mx < p.mbw; mx++ {
			mb := &mbs[my*p.mbw+mx]
			switch r := rng.Intn(100); {
			case r < siShare:
				mb.kind = 0
			case r < 92:
				mb.kind = 1
			default:
				mb.kind = 2
			}
			curSI := mb.kind == 0
			// sample / mode availability of a neighbouring macroblock
			avail := func(nx, ny int) bool {
				if !inPic(nx, ny) {
					return false
				}
				return !(p.constraint && mbs[ny*p.mbw+nx].kind == 0 && !curSI)
			}
			aA, aB, aC, aD := avail(mx-1, my), avail(mx, my-1), avail(mx+1, my-1), avail(mx-1, my-1)
			if mb.kind == 2 {
				w.ue(26)
				w.align()
				for y := 0; y < 16; y++ {
					for x := 0; x < 16; x++ {
						v := rng.Intn(256)
						out.y[(my*16+y)*out.w+mx*16+x] = uint16(v)
						wu(w, 8, v)
					}
				}
				for c := 0; c < 2; c++ {
					dst := out.cb
					if c == 1 {
						dst = out.cr
					}
					for y := 0; y < 8; y++ {
						for x := 0; x < 8; x++ {
							v := rng.Intn(256)
							dst[(my*8+y)*(out.w/2)+mx*8+x] = uint16(v)
							wu(w, 8, v)
						}
					}
				}
				for b := 0; b < 16; b++ {
					gl.v[(my*4+ys[b])*gl.w+mx*4+xs[b]] = 16
				}
				for c := 0; c < 2; c++ {
					for b := 0; b < 4; b++ {
						gc[c].v[(my*2+b>>1)*gc[c].w+mx*2+b&1] = 16
					}
				}
				continue
			}
			if curSI {
				w.ue(0)
			} else {
				w.ue(1)
			}
			var res *mbRes
			if curSI {
				cbp := rng.Intn(48)
				res = randRes(rng, cbp, 10)
				if rng.Intn(6) == 0 {
					res = randRes(rng, cbp, 50)
				}
				res.qpD = rng.Intn(7) - 3
			} else {
				res = &mbRes{}
			}
			// intra 4x4 modes: choose, write, and predict-and-reconstruct
			// luma in decoding order
			blkMode := func(bx, by int) (m int, ok bool) {
				// mode of the 4x4 block at (bx,by) (may lie in a neighbouring macroblock)
				nx, ny := mx, my
				if bx < 0 {
					nx, bx = mx-1, 3
				}
				if by < 0 {
					ny, by = my-1, 3
				}
				if nx != mx || ny != my {
					if !avail(nx, ny) {
						return 0, false
					}
					nb := &mbs[ny*p.mbw+nx]
					if nb.kind == 2 {
						return 2, true
					}
					return nb.modes[by*4+bx], true
				}
				return mb.modes[by*4+bx], true
			}
			for b := 0; b < 16; b++ {
				bx, by := xs[b], ys[b]
				left := bx > 0 || aA
				top := by > 0 || aB
				var tl, tr bool
				switch {
				case bx > 0 && by > 0:
					tl = true
				case bx > 0:
					tl = aB
				case by > 0:
					tl = aA
				default:
					tl = aD
				}
				switch {
				case by == 0 && bx < 3:
					tr = aB
				case by == 0:
					tr = aC
				case bx == 3:
					tr = false
				default:
					// already decoded iff its block index is smaller
					tr = (func() int {
						for k := 0; k < 16; k++ {
							if xs[k] == bx+1 && ys[k] == by-1 {
								return k
							}
						}
						return 99
					})() < b
				}
				var cand []int
				cand = append(cand, 2)
				if top {
					cand = append(cand, 0, 3, 7)
				}
				if left {
					cand = append(cand, 1, 8)
				}
				if top && left && tl {
					cand = append(cand, 4, 5, 6)
				}
				mode := cand[rng.Intn(len(cand))]
				ma, oka := blkMode(bx-1, by)
				mb2, okb := blkMode(bx, by-1)
				pred := 2
				if oka && okb {
					pred = min(ma, mb2)
				}
				mb.modes[by*4+bx] = mode
				if mode == pred {
					w.bit(1)
				} else {
					w.bit(0)
					rem := mode
					if mode > pred {
						rem--
					}
					wu(w, 3, rem)
				}
				// model reconstruction of this block
				off := (my*16+by*4)*out.w + mx*16 + bx*4
				pred4x4(out.y, off, out.w, mode, left, top, tl, tr, 8)
				if curSI {
					var pr [4][4]int
					for x := 0; x < 4; x++ {
						for y := 0; y < 4; y++ {
							pr[x][y] = int(out.y[off+y*out.w+x])
						}
					}
					rec := jmLuma(pr, res.levels4(b), qp+0, p.qs, true)
					for x := 0; x < 4; x++ {
						for y := 0; y < 4; y++ {
							out.y[off+y*out.w+x] = uint16(rec[x][y])
						}
					}
				}
			}
			// chroma prediction mode
			cands := []int{0}
			if aA {
				cands = append(cands, 1)
			}
			if aB {
				cands = append(cands, 2)
			}
			if aA && aB && aD {
				cands = append(cands, 3)
			}
			cmode := cands[rng.Intn(len(cands))]
			w.ue(cmode)
			w.ue(cbpCodeNum(&golombToIntra4x4CBP, res.cbp))
			if res.cbp != 0 {
				wse(w, res.qpD)
				qp = (qp + res.qpD + 52) % 52
				writeResidual(w, res, mx, my, gl, gc)
			} else {
				for b := 0; b < 16; b++ {
					gl.v[(my*4+ys[b])*gl.w+mx*4+xs[b]] = 0
				}
				for c := 0; c < 2; c++ {
					for b := 0; b < 4; b++ {
						gc[c].v[(my*2+b>>1)*gc[c].w+mx*2+b&1] = 0
					}
				}
			}
			for c := 0; c < 2; c++ {
				pl := out.cb
				if c == 1 {
					pl = out.cr
				}
				cw := out.w / 2
				off := my*8*cw + mx*8
				predChroma(pl, off, cw, cmode, [4]bool{aA, aA, aA, aA}, aB, 8, 8, 8)
				if curSI {
					var pr [8][8]int
					for x := 0; x < 8; x++ {
						for y := 0; y < 8; y++ {
							pr[x][y] = int(pl[off+y*cw+x])
						}
					}
					dc, ac := res.chromaLevels(c)
					rec := jmChroma(pr, dc, ac, chromaQP(p.qs, p.chromaOff), chromaQP(p.qs, p.chromaOff), true)
					for x := 0; x < 8; x++ {
						for y := 0; y < 8; y++ {
							pl[off+y*cw+x] = uint16(rec[x][y])
						}
					}
				}
			}
		}
	}
	w.trailing()
	return nalUnit(3, 5, w), out
}

// TestSISyntheticStreams decodes generated IDR pictures made of an SI slice.
func TestSISyntheticStreams(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	for it := 0; it < 80; it++ {
		p := &spPic{mbw: 4, mbh: 3, qp: 10 + rng.Intn(35), qs: 4 + rng.Intn(44), chromaOff: rng.Intn(9) - 4, constraint: it%2 == 1}
		pic, want := p.siPicture(rng, []int{100, 85, 60}[it%3])
		es := append(append(p.sps(), p.pps()...), pic...)
		name := fmt.Sprintf("it%d_qs%d_off%d_ci%v", it, p.qs, p.chromaOff, p.constraint)
		t.Run(name, func(t *testing.T) {
			frames := decodeToI420(t, es)
			if len(frames) != 1 {
				t.Fatalf("%d frames", len(frames))
			}
			got, exp := frames[0], want.i420()
			for i := range got {
				if got[i] != exp[i] {
					plane, o := "Y", i
					if i >= want.w*want.h {
						plane, o = "C", i-want.w*want.h
					}
					t.Fatalf("%s sample %d: got %d want %d", plane, o, got[i], exp[i])
				}
			}
		})
	}
}
