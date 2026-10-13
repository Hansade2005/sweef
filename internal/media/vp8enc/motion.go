package vp8enc

// Motion estimation and inter/intra mode decision for predicted frames.

const maxMVPixels = 120 // keeps vector differences inside the coded range

// searchCtx is the state of one macroblock's motion search.
type searchCtx struct {
	w       *worker
	srcY    []byte
	ys      int
	x0, y0  int
	best    mv // predictor the vector difference is coded against
	nearest mv
	near    mv
	lam     int
}

func roundFP(v int16) int16 { return int16((int(v) + 4) >> 3 << 3) }

// inBounds reports whether a block starting at (px, py) stays inside the padded reference.
func (e *Encoder) inBounds(px, py int) bool {
	return px >= -16 && px <= e.w16 && py >= -16 && py <= e.h16
}

func (s *searchCtx) mvCost(m mv) int {
	if m == (mv{}) || m == s.nearest || m == s.near {
		return s.lam * 2 // ZERO, NEAREST and NEAR cost a mode decision only
	}
	return s.lam * (mvBits(int(m.r)-int(s.best.r)) + mvBits(int(m.c)-int(s.best.c)) + 3)
}

// fullPelCost is the SAD plus vector cost of a full-pel vector against ref (math.MaxInt-ish when out of range).
func (s *searchCtx) fullPelCost(ref *plane, m mv, limit int) int {
	e := s.w.e
	px, py := s.x0+int(m.c>>3), s.y0+int(m.r>>3)
	if !e.inBounds(px, py) || abs(int(m.r)) > maxMVPixels*8 || abs(int(m.c)) > maxMVPixels*8 {
		return 1 << 30
	}
	c := s.mvCost(m)
	if c >= limit {
		return c
	}
	return c + sad16(s.srcY, s.ys, ref.pix[ref.idx(px, py):], ref.stride, limit-c)
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// subPelCost motion-compensates the whole luma block at m and returns SAD plus vector cost.
func (s *searchCtx) subPelCost(ref *plane, m mv, dst []byte) int {
	e := s.w.e
	px, py := s.x0+int(m.c>>3), s.y0+int(m.r>>3)
	if !e.inBounds(px, py) || abs(int(m.r)) > maxMVPixels*8 || abs(int(m.c)) > maxMVPixels*8 {
		return 1 << 30
	}
	predictInter(ref, px, py, int(m.c&7), int(m.r&7), 16, 16, dst, 16, s.w.mc[:])
	return s.mvCost(m) + sad16(s.srcY, s.ys, dst, 16, 1<<30)
}

// chooseInter decides the reference, mode and vector of a macroblock in a predicted frame and
// leaves the prediction in w.pred{Y,U,V}.
func (w *worker) chooseInter(mbx, mby, ii int, srcY []byte, ys int, srcU, srcV []byte, cs int, out *mbOut) {
	e := w.e
	best, nearest, near, cnt := findNearMVs(e.infos, ii, e.miStride, mbx, mby, e.mbw, e.mbh)
	for i := range cnt {
		out.cnt[i] = uint8(cnt[i])
	}
	out.best = best
	idx := mby*e.mbw + mbx
	s := &searchCtx{w: w, srcY: srcY, ys: ys, x0: mbx * 16, y0: mby * 16, best: best, nearest: nearest, near: near, lam: e.lambda}

	ref := e.last.y
	// candidates at full-pel precision
	cands := [8]mv{{}, mv{roundFP(best.r), roundFP(best.c)}, mv{roundFP(nearest.r), roundFP(nearest.c)}, mv{roundFP(near.r), roundFP(near.c)}}
	nc := 4
	if pm := e.prevField[idx]; pm != (mv{}) {
		cands[nc] = mv{roundFP(pm.r), roundFP(pm.c)}
		nc++
	}
	bm := mv{}
	bc := s.fullPelCost(ref, bm, 1<<30)
	zeroCost := bc
	for i := 1; i < nc; i++ {
		c := cands[i]
		dup := false
		for j := 0; j < i; j++ {
			if cands[j] == c {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		if cc := s.fullPelCost(ref, c, bc); cc < bc {
			bc, bm = cc, c
		}
	}
	sadOnly := bc - s.mvCost(bm)
	subpel := false
	if sadOnly > e.staticSAD {
		// pattern search around the best candidate
		step := e.searchStep
		for ; step >= 1; step >>= 1 {
			for iter := 0; iter < 6; iter++ {
				moved := false
				c0 := bm
				for _, d := range [8][2]int{{-1, 0}, {1, 0}, {0, -1}, {0, 1}, {-1, -1}, {-1, 1}, {1, -1}, {1, 1}} {
					c := mv{int16(int(c0.r) + d[0]*step*8), int16(int(c0.c) + d[1]*step*8)}
					if cc := s.fullPelCost(ref, c, bc); cc < bc {
						bc, bm = cc, c
						moved = true
					}
				}
				if !moved {
					break
				}
			}
		}
		if e.speed >= 1 {
			// sub-pel refinement: half then (speed 2) quarter
			for _, st := range []int{4, 2} {
				if st == 2 && e.speed < 2 {
					break
				}
				c0 := bm
				bestAxis := [2]int{}
				bestAxisCost := [2]int{bc, bc}
				for k, d := range [4][2]int{{-st, 0}, {st, 0}, {0, -st}, {0, st}} {
					c := mv{int16(int(c0.r) + d[0]), int16(int(c0.c) + d[1])}
					if cc := s.subPelCost(ref, c, w.candY[:]); cc < bc {
						a := k >> 1
						if cc < bestAxisCost[a] {
							bestAxisCost[a] = cc
							bestAxis[a] = d[0] + d[1]
						}
					}
				}
				nb := c0
				moved := false
				if bestAxisCost[0] < bc {
					nb.r += int16(bestAxis[0])
					moved = true
				}
				if bestAxisCost[1] < bc {
					nb.c += int16(bestAxis[1])
					moved = true
				}
				if moved {
					if cc := s.subPelCost(ref, nb, w.candY[:]); cc < bc {
						bc, bm = cc, nb
					} else if bestAxisCost[0] <= bestAxisCost[1] && bestAxisCost[0] < bc {
						bc, bm = bestAxisCost[0], mv{c0.r + int16(bestAxis[0]), c0.c}
					} else if bestAxisCost[1] < bc {
						bc, bm = bestAxisCost[1], mv{c0.r, c0.c + int16(bestAxis[1])}
					}
					subpel = true
				}
			}
		}
	}
	if sadOnly > e.staticSAD && e.speed >= 1 {
		// snap to a predictor when it costs almost nothing extra: NEAREST and NEAR are much cheaper than NEW
		for _, c := range [2]mv{nearest, near} {
			if c != bm && c != (mv{}) {
				if cc := s.subPelCost(ref, c, w.candY[:]); cc <= bc+s.lam*2 {
					bc, bm = cc, c
				}
			}
		}
	}
	refSel := uint8(refLast)
	// golden and altref: the same vector, and zero
	if e.speed >= 1 && sadOnly > e.staticSAD/2 {
		for r, rf := range [2]*frame{e.golden, e.alt} {
			if rf == e.last {
				continue
			}
			rp := rf.y
			zc := s.fullPelCost(rp, mv{}, bc)
			if zc < bc-bc/16 {
				bc, bm, refSel, subpel = zc, mv{}, uint8(refGolden+r), false
			}
		}
	}
	_ = zeroCost

	// intra alternative
	intra := e.key
	if bc-s.mvCost(bm) > e.intraCheckSAD {
		_, isad := w.bestIntraY(mbx, mby, srcY, ys)
		if isad+e.intraBias < bc {
			intra = true
		}
	}
	if intra {
		mode, _ := w.bestIntraY(mbx, mby, srcY, ys)
		out.ref, out.mode, out.mv = refIntra, uint8(mode), mv{}
		out.uvmode = uint8(w.bestIntraUV(mbx, mby, srcU, srcV, cs))
		w.nIntra++
		return
	}
	// inter prediction of all three planes
	rf := e.last
	switch refSel {
	case refGolden:
		rf = e.golden
	case refAlt:
		rf = e.alt
	}
	px, py := mbx*16+int(bm.c>>3), mby*16+int(bm.r>>3)
	_ = subpel
	predictInter(rf.y, px, py, int(bm.c&7), int(bm.r&7), 16, 16, w.predY[:], 16, w.mc[:])
	cr, cc := int(bm.r), int(bm.c)
	cr = (cr + (1 | cr>>31)) / 2
	cc = (cc + (1 | cc>>31)) / 2
	cx, cy := mbx*8+cc>>3, mby*8+cr>>3
	predictInter(rf.u, cx, cy, cc&7, cr&7, 8, 8, w.predU[:], 8, w.mc[:])
	predictInter(rf.v, cx, cy, cc&7, cr&7, 8, 8, w.predV[:], 8, w.mc[:])

	out.ref, out.mv = refSel, bm
	switch {
	case bm == (mv{}):
		out.mode = mbZero
	case bm == nearest:
		out.mode = mbNearest
	case bm == near:
		out.mode = mbNear
	default:
		out.mode = mbNew
		w.newMV++
	}
	w.nRef[refSel]++
}
