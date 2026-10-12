package h264

// In-loop deblocking filter (8.7) for frame, field and MBAFF pictures. The
// boundary strength derivation and the special MBAFF edge handling follow
// FFmpeg's loop filter.

func (d *Decoder) deblockPicture(pic *Picture) {
	step, row0 := 1, 0
	if d.fieldPic {
		step, row0 = 2, btoi(d.picStructure == picBottom)
	}
	if d.mbaffFrame {
		for pr := 0; pr < d.mbH; pr += 2 {
			for mbx := 0; mbx < d.mbW; mbx++ {
				d.deblockMB(mbx, pr)
				d.deblockMB(mbx, pr+1)
			}
		}
		return
	}
	for mby := row0; mby < d.mbH; mby += step {
		for mbx := 0; mbx < d.mbW; mbx++ {
			d.deblockMB(mbx, mby)
		}
	}
}

func mvFarY(a, b mv16, limit int) bool {
	dx := int(a.x) - int(b.x)
	dy := int(a.y) - int(b.y)
	return uint(dx+3) >= 7 || dy >= limit || dy <= -limit
}

// blockMotion returns the references (as picture identities) and vectors of
// 4x4 block blk (raster) of macroblock idx.
func (d *Decoder) blockMotion(idx, blk int) (r [2]int32, m [2]mv16) {
	pic := d.curPic
	b8 := (blk>>3)*2 + (blk&3)>>1
	for l := 0; l < 2; l++ {
		r[l] = pic.refID[l][idx*4+b8]
		m[l] = pic.mv[l][idx*16+blk]
	}
	return
}

// checkMV returns 1 when the motion of block bp of macroblock ip and block
// bq of iq differs enough for bS = 1.
func (d *Decoder) checkMV(ip, bp, iq, bq int, listCount, mvyLimit int) int {
	rp, mp := d.blockMotion(ip, bp)
	rq, mq := d.blockMotion(iq, bq)
	v := rp[0] != rq[0]
	if !v && rp[0] != -1 {
		v = mvFarY(mp[0], mq[0], mvyLimit)
	}
	if listCount == 2 {
		if !v {
			v = rp[1] != rq[1] || mvFarY(mp[1], mq[1], mvyLimit)
		}
		if v {
			if rp[0] != rq[1] || rp[1] != rq[0] {
				return 1
			}
			return btoi(mvFarY(mp[0], mq[1], mvyLimit) || mvFarY(mp[1], mq[0], mvyLimit))
		}
	}
	return btoi(v)
}

// bsFor computes the boundary strength between block bp of macroblock ip
// and block bq of macroblock iq for non-intra edges.
func (d *Decoder) bsInter(ip, bp, iq, bq, listCount, mvyLimit int, mixed bool) int {
	if d.mbNZMask[ip]>>uint(bp)&1 != 0 || d.mbNZMask[iq]>>uint(bq)&1 != 0 {
		return 2
	}
	if mixed {
		return 1
	}
	return d.checkMV(ip, bp, iq, bq, listCount, mvyLimit)
}

func (d *Decoder) deblockMB(mbx, mby int) {
	pic := d.curPic
	idx := pic.mbIdx(mbx, mby)
	st := d.sliceTable[idx]
	if st == 0xffff {
		return
	}
	sn := int(st) - 1
	if st == 0xfffe || sn >= len(d.slices) {
		return
	}
	sp := &d.slices[sn]
	if sp.disable == 1 {
		return
	}
	typ := pic.mbType[idx]
	field := typ&mbInterlaced != 0
	d.mbField = field
	d.mbx, d.mby = mbx, mby
	d.setMBGeometry()
	stride := d.mbStride
	mbaff := d.mbaffFrame

	topXY := idx - stride<<uint(btoi(field))
	leftXY := [2]int{idx - 1, idx - 1}
	if mbaff {
		leftField := pic.mbType[idx-1]&mbInterlaced != 0
		if mby&1 != 0 {
			if leftField != field {
				leftXY[0] -= stride
			}
		} else {
			if field && pic.mbType[topXY]&mbInterlaced == 0 {
				topXY += stride
			}
			if leftField != field {
				leftXY[1] += stride
			}
		}
	}
	avail := func(i int) bool {
		s := d.sliceTable[i]
		if s == 0xffff {
			return false
		}
		if sp.disable == 2 {
			return s == st
		}
		return true
	}
	topType := pic.mbType[topXY]
	if !avail(topXY) {
		topType = 0
	}
	leftType := [2]uint32{pic.mbType[leftXY[0]], pic.mbType[leftXY[1]]}
	if !avail(leftXY[1]) {
		leftType = [2]uint32{}
	}
	mvyLimit := 4
	if field {
		mvyLimit = 2
	}
	listCount := sp.listCount
	qp := int(d.qpTable[idx])
	t8 := typ&mb8x8DCT != 0
	nPl := 3
	if d.f.cat == 0 {
		nPl = 1
	}
	cat := d.f.cat
	chroma := cat != 0
	qpBd := d.f.qpBdY
	cqp := func(c, q int) int {
		off := sp.cbOff
		if c == 2 {
			off = sp.crOff
		}
		return d.chromaQPPrime(q, off)
	}
	curCQP := [3]int{qp, cqp(1, qp), cqp(2, qp)}
	if cat == 0 {
		curCQP = [3]int{qp, qp, qp}
	}
	_ = chroma

	// filter wraps filterEdge for plane c.
	filter := func(c, pos, across, along, n int, bs [4]int, qpAv int) {
		pl, _ := d.planeOf(c)
		bd := d.f.bdY
		if c > 0 {
			bd = d.f.bdC
		}
		filterEdge(pl, pos, across, along, n, bs, qpAv, sp, qpBd, c > 0 && cat != 3, bd)
	}
	// planeDims
	var wC, hC [3]int
	for c := 0; c < nPl; c++ {
		wC[c], hC[c] = d.mbDims(c)
	}

	firstVertDone := false
	if mbaff && leftType[0] != 0 && (typ&mbInterlaced != 0) != (leftType[0]&mbInterlaced != 0) {
		// the left macroblock pair has the other field/frame mode
		firstVertDone = true
		var bS [8]int
		if isIntraDB(typ) {
			for i := range bS {
				bS[i] = 4
			}
		} else {
			var off [8]int
			switch {
			case field:
				off = [8]int{0, 1, 2, 3, 0, 1, 2, 3}
			case mby&1 == 0:
				off = [8]int{0, 0, 0, 0, 1, 1, 1, 1}
			default:
				off = [8]int{2, 2, 2, 2, 3, 3, 3, 3}
			}
			for i := 0; i < 8; i++ {
				j := i & 1
				if field {
					j = i >> 2
				}
				mn := leftXY[j]
				if isIntraDB(leftType[j]) {
					bS[i] = 4
					continue
				}
				cb := (i >> 1) * 4
				bS[i] = 1 + btoi(d.mbNZMask[idx]>>uint(cb)&1 != 0 || d.mbNZMask[mn]>>uint(off[i]*4+3)&1 != 0)
			}
		}
		mbQP := qp
		var q [2]int
		var cq [2][3]int
		for j := 0; j < 2; j++ {
			ln := int(d.qpTable[leftXY[j]])
			q[j] = (mbQP + ln + 1) >> 1
			for c := 1; c < nPl; c++ {
				cq[j][c] = (curCQP[c] + cqp(c, ln) + 1) >> 1
			}
		}
		sub := func(i0, step int) [4]int {
			return [4]int{bS[i0], bS[i0+step], bS[i0+2*step], bS[i0+3*step]}
		}
		for c := 0; c < nPl; c++ {
			_, ps := d.planeOf(c)
			w, h := wC[c], hC[c]
			lumaLike := c == 0 || cat == 3
			base := d.mbOff[c]
			qa := q
			if c > 0 {
				qa = [2]int{cq[0][c], cq[1][c]}
			}
			_ = ps
			stride := d.mbStr[c]
			if field {
				// two halves of the field macroblock, each against one left macroblock
				half := h / 2
				for hv := 0; hv < 2; hv++ {
					n := 8
					if !lumaLike {
						n = h / 2
					}
					filter(c, base+hv*half*stride, 1, stride, n, sub(hv*4, 1), qa[hv])
				}
				_ = w
			} else {
				// frame macroblock: even rows against the left top field macroblock, odd rows against the bottom one
				for hv := 0; hv < 2; hv++ {
					n := 8
					if !lumaLike {
						n = h / 2
					}
					filter(c, base+hv*stride, 1, 2*stride, n, sub(hv, 2), qa[hv])
				}
			}
		}
	}

	for dir := 0; dir < 2; dir++ {
		mbm := leftXY[0]
		mbmType := leftType[0]
		if dir == 1 {
			mbm = topXY
			mbmType = topType
		}
		// edge 0
		if mbmType != 0 && !(dir == 0 && firstVertDone) {
			if mbaff && dir == 1 && mby&1 == 0 && mbmType&mbInterlaced != 0 && !field {
				// frame macroblock below a field macroblock pair: filter once per field
				for j := 0; j < 2; j++ {
					mn := idx - 2*stride + j*stride
					var bS [4]int
					if isIntraDB(typ | pic.mbType[mn]) {
						bS = [4]int{3, 3, 3, 3}
					} else {
						for i := 0; i < 4; i++ {
							bS[i] = 1 + btoi(d.mbNZMask[idx]>>uint(i)&1 != 0 || d.mbNZMask[mn]>>uint(12+i)&1 != 0)
						}
					}
					q := (qp + int(d.qpTable[mn]) + 1) >> 1
					for c := 0; c < nPl; c++ {
						pl := d.mbOff[c] + j*d.mbStr[c]
						qa := q
						if c > 0 {
							qa = (curCQP[c] + cqp(c, int(d.qpTable[mn])) + 1) >> 1
						}
						n := wC[c]
						filter(c, pl, 2*d.mbStr[c], 1, n, bS, qa)
					}
				}
			} else {
				var bS [4]int
				if isIntraDB(typ | mbmType) {
					v := 3
					if (typ|mbmType)&mbInterlaced == 0 || ((mbaff || d.fieldPic) && dir == 0) {
						v = 4
					}
					bS = [4]int{v, v, v, v}
				} else {
					mixed := dir == 1 && mbaff && (typ^mbmType)&mbInterlaced != 0
					for i := 0; i < 4; i++ {
						var bp, bq int
						if dir == 0 {
							bq, bp = i*4, i*4+3
						} else {
							bq, bp = i, 12+i
						}
						bS[i] = d.bsInter(mbm, bp, idx, bq, listCount, mvyLimit, mixed)
					}
				}
				if bS[0]+bS[1]+bS[2]+bS[3] != 0 {
					q := (qp + int(d.qpTable[mbm]) + 1) >> 1
					for c := 0; c < nPl; c++ {
						qa := q
						if c > 0 {
							qa = (curCQP[c] + cqp(c, int(d.qpTable[mbm])) + 1) >> 1
						}
						base := d.mbOff[c]
						stride := d.mbStr[c]
						if dir == 0 {
							filter(c, base, 1, stride, hC[c], bS, qa)
						} else {
							filter(c, base, stride, 1, wC[c], bS, qa)
						}
					}
				}
			}
		}
		// internal edges
		for edge := 1; edge < 4; edge++ {
			lumaEdge := !(t8 && edge&1 == 1)
			chroma422H := cat == 2 && dir == 1
			if !lumaEdge && !chroma422H {
				continue
			}
			var bS [4]int
			if isIntraDB(typ) {
				bS = [4]int{3, 3, 3, 3}
			} else {
				any := false
				for i := 0; i < 4; i++ {
					var bp, bq int
					if dir == 0 {
						bp, bq = i*4+edge-1, i*4+edge
					} else {
						bp, bq = (edge-1)*4+i, edge*4+i
					}
					bS[i] = d.bsInter(idx, bp, idx, bq, listCount, mvyLimit, false)
					if bS[i] != 0 {
						any = true
					}
				}
				if !any {
					continue
				}
			}
			for c := 0; c < nPl; c++ {
				qa := curCQP[c]
				base := d.mbOff[c]
				stride := d.mbStr[c]
				lumaLike := c == 0 || cat == 3
				switch {
				case dir == 0 && lumaLike:
					if lumaEdge {
						filter(c, base+4*edge, 1, stride, hC[c], bS, qa)
					}
				case dir == 0:
					if edge&1 == 0 {
						filter(c, base+2*edge, 1, stride, hC[c], bS, qa)
					}
				case lumaLike:
					if lumaEdge {
						filter(c, base+4*edge*stride, stride, 1, wC[c], bS, qa)
					}
				case cat == 2:
					filter(c, base+4*edge*stride, stride, 1, wC[c], bS, qa)
				default:
					if edge&1 == 0 {
						filter(c, base+2*edge*stride, stride, 1, wC[c], bS, qa)
					}
				}
			}
		}
	}
}

// filterEdge filters one edge of length n samples. across is the step across
// the edge, along the step along it. bs holds the four boundary strengths
// (one per quarter of the edge). qpAv is the average QP' of the two
// macroblocks.
func filterEdge(pl []uint16, pos, across, along, n int, bs [4]int, qpAv int, sp *sliceParams, qpBd int, chroma bool, bd int) {
	indexA := clip3(0, 51, qpAv-qpBd+sp.alpha)
	indexB := clip3(0, 51, qpAv-qpBd+sp.beta)
	sh := uint(bd - 8)
	alpha := int(alphaTable[indexA]) << sh
	beta := int(betaTable[indexB]) << sh
	if alpha == 0 || beta == 0 {
		return
	}
	maxv := int32(1)<<uint(bd) - 1
	per := n / 4
	for g := 0; g < 4; g++ {
		b := bs[g]
		if b == 0 {
			continue
		}
		tc0 := 0
		if b < 4 {
			tc0 = int(tc0Table[indexA][b]) << sh
		}
		for k := 0; k < per; k++ {
			o := pos + (g*per+k)*along
			p0 := int(pl[o-across])
			q0 := int(pl[o])
			if abs(p0-q0) >= alpha {
				continue
			}
			p1 := int(pl[o-2*across])
			q1 := int(pl[o+across])
			if abs(p1-p0) >= beta || abs(q1-q0) >= beta {
				continue
			}
			if chroma {
				if b < 4 {
					tc := tc0 + 1
					delta := clip3(-tc, tc, (((q0-p0)<<2)+(p1-q1)+4)>>3)
					pl[o-across] = clipPix(int32(p0+delta), maxv)
					pl[o] = clipPix(int32(q0-delta), maxv)
				} else {
					pl[o-across] = uint16((2*p1 + p0 + q1 + 2) >> 2)
					pl[o] = uint16((2*q1 + q0 + p1 + 2) >> 2)
				}
				continue
			}
			p2 := int(pl[o-3*across])
			q2 := int(pl[o+2*across])
			ap := abs(p2 - p0)
			aq := abs(q2 - q0)
			if b < 4 {
				tc := tc0
				if ap < beta {
					tc++
				}
				if aq < beta {
					tc++
				}
				delta := clip3(-tc, tc, (((q0-p0)<<2)+(p1-q1)+4)>>3)
				pl[o-across] = clipPix(int32(p0+delta), maxv)
				pl[o] = clipPix(int32(q0-delta), maxv)
				if ap < beta {
					pl[o-2*across] = uint16(p1 + clip3(-tc0, tc0, (p2+((p0+q0+1)>>1)-(p1<<1))>>1))
				}
				if aq < beta {
					pl[o+across] = uint16(q1 + clip3(-tc0, tc0, (q2+((p0+q0+1)>>1)-(q1<<1))>>1))
				}
				continue
			}
			// bS == 4
			small := abs(p0-q0) < (alpha>>2)+2
			if ap < beta && small {
				p3 := int(pl[o-4*across])
				pl[o-across] = uint16((p2 + 2*p1 + 2*p0 + 2*q0 + q1 + 4) >> 3)
				pl[o-2*across] = uint16((p2 + p1 + p0 + q0 + 2) >> 2)
				pl[o-3*across] = uint16((2*p3 + 3*p2 + p1 + p0 + q0 + 4) >> 3)
			} else {
				pl[o-across] = uint16((2*p1 + p0 + q1 + 2) >> 2)
			}
			if aq < beta && small {
				q3 := int(pl[o+3*across])
				pl[o] = uint16((p1 + 2*p0 + 2*q0 + 2*q1 + q2 + 4) >> 3)
				pl[o+across] = uint16((p0 + q0 + q1 + q2 + 2) >> 2)
				pl[o+2*across] = uint16((2*q3 + 3*q2 + q1 + q0 + p0 + 4) >> 3)
			} else {
				pl[o] = uint16((2*q1 + q0 + p1 + 2) >> 2)
			}
		}
	}
}
