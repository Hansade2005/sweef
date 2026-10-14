package h264

// Fast deblocking path for frame and field pictures without MBAFF in 4:2:0 and monochrome. It computes the
// same boundary strengths and filters the same samples as deblockMB (which stays the reference and the only
// path for MBAFF, 4:2:2 and 4:4:4); the differences are structural: the motion of the blocks is loaded once
// into small records and compared as packed words, macroblocks whose partitions cannot differ skip the
// motion comparison, and the sample filters are specialised for luma and chroma and for the direction of the
// edge so that the inner loops work on row slices without bounds checks.

// deblockFast switches the fast path (tests compare it with the reference).
var deblockFast = true

// SetFastDeblock turns the fast deblocking path on or off (it is on by default; the output is identical
// either way). It exists for tests and measurements.
func SetFastDeblock(on bool) { deblockFast = on }

// blkMotion is the motion of one 4x4 block as the deblocking filter compares it: the identities of the
// reference pictures of both lists (-1 when a list is unused) and the motion vectors packed into words.
type blkMotion struct {
	r0, r1 int32
	m0, m1 uint32
}

func packMV(m mv16) uint32 { return uint32(uint16(m.x)) | uint32(uint16(m.y))<<16 }

// farMV reports whether two packed motion vectors differ by a whole sample or more horizontally or by
// limit quarter samples or more vertically.
func farMV(a, b uint32, limit int) bool {
	if a == b {
		return false
	}
	dx := int(int16(a)) - int(int16(b))
	dy := int(int16(a>>16)) - int(int16(b>>16))
	return uint(dx+3) >= 7 || dy >= limit || dy <= -limit
}

// motionDiffers is checkMV on loaded records: true when the boundary strength between the two blocks is 1.
func motionDiffers(p, q *blkMotion, two bool, limit int) bool {
	v := p.r0 != q.r0
	if !v && p.r0 != -1 {
		v = farMV(p.m0, q.m0, limit)
	}
	if two {
		if !v {
			v = p.r1 != q.r1 || farMV(p.m1, q.m1, limit)
		}
		if v {
			if p.r0 != q.r1 || p.r1 != q.r0 {
				return true
			}
			return farMV(p.m0, q.m1, limit) || farMV(p.m1, q.m0, limit)
		}
	}
	return v
}

func loadMotion(pic *Picture, idx, blk int, two bool) blkMotion {
	b8 := (blk>>3)*2 + (blk&3)>>1
	m := blkMotion{r0: pic.refID[0][idx*4+b8], m0: packMV(pic.mv[0][idx*16+blk]), r1: -1}
	if two {
		m.r1 = pic.refID[1][idx*4+b8]
		m.m1 = packMV(pic.mv[1][idx*16+blk])
	}
	return m
}

// loadMB loads the motion of all 16 blocks of macroblock idx (inter macroblocks only).
func loadMB(pic *Picture, idx int, typ uint32, two bool, out *[16]blkMotion) {
	if typ&mb16x16 != 0 {
		m := loadMotion(pic, idx, 0, two)
		for i := range out {
			out[i] = m
		}
		return
	}
	for i := range out {
		out[i] = loadMotion(pic, idx, i, two)
	}
}

// edgeAB holds the thresholds of one edge: alpha and beta already shifted for the bit depth and the row of
// the tc0 table.
type edgeAB struct {
	alpha, beta int32
	tc0         *[4]uint8
	ok          bool
}

func edgeParams(qpAv int, sp *sliceParams, qpBd int, bd int) edgeAB {
	ia := clip3(0, 51, qpAv-qpBd+sp.alpha)
	ib := clip3(0, 51, qpAv-qpBd+sp.beta)
	sh := uint(bd - 8)
	a := int32(alphaTable[ia]) << sh
	b := int32(betaTable[ib]) << sh
	return edgeAB{alpha: a, beta: b, tc0: &tc0Table[ia], ok: a != 0 && b != 0}
}

func abs32(a int32) int32 {
	if a < 0 {
		return -a
	}
	return a
}

func clip32(lo, hi, v int32) int32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// lumaV filters a vertical luma edge of 16 lines whose first line has q0 at pl[pos]; bs holds one strength
// per group of four lines.
func lumaV(pl []uint16, pos, stride int, bs *[4]uint8, e *edgeAB, sh uint, maxv int32) {
	alpha, beta := e.alpha, e.beta
	for g := 0; g < 4; g++ {
		b := bs[g]
		if b == 0 {
			continue
		}
		var tc0 int32
		if b < 4 {
			tc0 = int32(e.tc0[b]) << sh
		}
		for k := 0; k < 4; k++ {
			o := pos + (g*4+k)*stride
			s := pl[o-4 : o+4 : o+4]
			p0, q0 := int32(s[3]), int32(s[4])
			if abs32(p0-q0) >= alpha {
				continue
			}
			p1, q1 := int32(s[2]), int32(s[5])
			if abs32(p1-p0) >= beta || abs32(q1-q0) >= beta {
				continue
			}
			p2, q2 := int32(s[1]), int32(s[6])
			ap, aq := abs32(p2-p0), abs32(q2-q0)
			if b < 4 {
				tc := tc0
				if ap < beta {
					tc++
				}
				if aq < beta {
					tc++
				}
				delta := clip32(-tc, tc, (((q0-p0)<<2)+(p1-q1)+4)>>3)
				s[3] = clipPix(p0+delta, maxv)
				s[4] = clipPix(q0-delta, maxv)
				if ap < beta {
					s[2] = uint16(p1 + clip32(-tc0, tc0, (p2+((p0+q0+1)>>1)-(p1<<1))>>1))
				}
				if aq < beta {
					s[5] = uint16(q1 + clip32(-tc0, tc0, (q2+((p0+q0+1)>>1)-(q1<<1))>>1))
				}
				continue
			}
			small := abs32(p0-q0) < (alpha>>2)+2
			if ap < beta && small {
				p3 := int32(s[0])
				s[3] = uint16((p2 + 2*p1 + 2*p0 + 2*q0 + q1 + 4) >> 3)
				s[2] = uint16((p2 + p1 + p0 + q0 + 2) >> 2)
				s[1] = uint16((2*p3 + 3*p2 + p1 + p0 + q0 + 4) >> 3)
			} else {
				s[3] = uint16((2*p1 + p0 + q1 + 2) >> 2)
			}
			if aq < beta && small {
				q3 := int32(s[7])
				s[4] = uint16((p1 + 2*p0 + 2*q0 + 2*q1 + q2 + 4) >> 3)
				s[5] = uint16((p0 + q0 + q1 + q2 + 2) >> 2)
				s[6] = uint16((2*q3 + 3*q2 + q1 + q0 + p0 + 4) >> 3)
			} else {
				s[4] = uint16((2*q1 + q0 + p1 + 2) >> 2)
			}
		}
	}
}

// lumaH filters a horizontal luma edge of 16 columns whose first column has q0 at pl[pos].
func lumaH(pl []uint16, pos, stride int, bs *[4]uint8, e *edgeAB, sh uint, maxv int32) {
	alpha, beta := e.alpha, e.beta
	r0 := pl[pos-4*stride : pos-4*stride+16 : pos-4*stride+16] // p3
	r1 := pl[pos-3*stride : pos-3*stride+16 : pos-3*stride+16] // p2
	r2 := pl[pos-2*stride : pos-2*stride+16 : pos-2*stride+16] // p1
	r3 := pl[pos-stride : pos-stride+16 : pos-stride+16]       // p0
	r4 := pl[pos : pos+16 : pos+16]                            // q0
	r5 := pl[pos+stride : pos+stride+16 : pos+stride+16]       // q1
	r6 := pl[pos+2*stride : pos+2*stride+16 : pos+2*stride+16] // q2
	r7 := pl[pos+3*stride : pos+3*stride+16 : pos+3*stride+16] // q3
	for g := 0; g < 4; g++ {
		b := bs[g]
		if b == 0 {
			continue
		}
		var tc0 int32
		if b < 4 {
			tc0 = int32(e.tc0[b]) << sh
		}
		for i := g * 4; i < g*4+4; i++ {
			p0, q0 := int32(r3[i]), int32(r4[i])
			if abs32(p0-q0) >= alpha {
				continue
			}
			p1, q1 := int32(r2[i]), int32(r5[i])
			if abs32(p1-p0) >= beta || abs32(q1-q0) >= beta {
				continue
			}
			p2, q2 := int32(r1[i]), int32(r6[i])
			ap, aq := abs32(p2-p0), abs32(q2-q0)
			if b < 4 {
				tc := tc0
				if ap < beta {
					tc++
				}
				if aq < beta {
					tc++
				}
				delta := clip32(-tc, tc, (((q0-p0)<<2)+(p1-q1)+4)>>3)
				r3[i] = clipPix(p0+delta, maxv)
				r4[i] = clipPix(q0-delta, maxv)
				if ap < beta {
					r2[i] = uint16(p1 + clip32(-tc0, tc0, (p2+((p0+q0+1)>>1)-(p1<<1))>>1))
				}
				if aq < beta {
					r5[i] = uint16(q1 + clip32(-tc0, tc0, (q2+((p0+q0+1)>>1)-(q1<<1))>>1))
				}
				continue
			}
			small := abs32(p0-q0) < (alpha>>2)+2
			if ap < beta && small {
				p3 := int32(r0[i])
				r3[i] = uint16((p2 + 2*p1 + 2*p0 + 2*q0 + q1 + 4) >> 3)
				r2[i] = uint16((p2 + p1 + p0 + q0 + 2) >> 2)
				r1[i] = uint16((2*p3 + 3*p2 + p1 + p0 + q0 + 4) >> 3)
			} else {
				r3[i] = uint16((2*p1 + p0 + q1 + 2) >> 2)
			}
			if aq < beta && small {
				q3 := int32(r7[i])
				r4[i] = uint16((p1 + 2*p0 + 2*q0 + 2*q1 + q2 + 4) >> 3)
				r5[i] = uint16((p0 + q0 + q1 + q2 + 2) >> 2)
				r6[i] = uint16((2*q3 + 3*q2 + q1 + q0 + p0 + 4) >> 3)
			} else {
				r4[i] = uint16((2*q1 + q0 + p1 + 2) >> 2)
			}
		}
	}
}

// chromaV filters a vertical 4:2:0 chroma edge of 8 lines (one strength per two lines).
func chromaV(pl []uint16, pos, stride int, bs *[4]uint8, e *edgeAB, sh uint, maxv int32) {
	alpha, beta := e.alpha, e.beta
	for g := 0; g < 4; g++ {
		b := bs[g]
		if b == 0 {
			continue
		}
		var tc int32
		if b < 4 {
			tc = int32(e.tc0[b])<<sh + 1
		}
		for k := 0; k < 2; k++ {
			o := pos + (g*2+k)*stride
			s := pl[o-2 : o+2 : o+2]
			p0, q0 := int32(s[1]), int32(s[2])
			if abs32(p0-q0) >= alpha {
				continue
			}
			p1, q1 := int32(s[0]), int32(s[3])
			if abs32(p1-p0) >= beta || abs32(q1-q0) >= beta {
				continue
			}
			if b < 4 {
				delta := clip32(-tc, tc, (((q0-p0)<<2)+(p1-q1)+4)>>3)
				s[1] = clipPix(p0+delta, maxv)
				s[2] = clipPix(q0-delta, maxv)
			} else {
				s[1] = uint16((2*p1 + p0 + q1 + 2) >> 2)
				s[2] = uint16((2*q1 + q0 + p1 + 2) >> 2)
			}
		}
	}
}

// chromaH filters a horizontal 4:2:0 chroma edge of 8 columns.
func chromaH(pl []uint16, pos, stride int, bs *[4]uint8, e *edgeAB, sh uint, maxv int32) {
	alpha, beta := e.alpha, e.beta
	r0 := pl[pos-2*stride : pos-2*stride+8 : pos-2*stride+8] // p1
	r1 := pl[pos-stride : pos-stride+8 : pos-stride+8]       // p0
	r2 := pl[pos : pos+8 : pos+8]                            // q0
	r3 := pl[pos+stride : pos+stride+8 : pos+stride+8]       // q1
	for g := 0; g < 4; g++ {
		b := bs[g]
		if b == 0 {
			continue
		}
		var tc int32
		if b < 4 {
			tc = int32(e.tc0[b])<<sh + 1
		}
		for i := g * 2; i < g*2+2; i++ {
			p0, q0 := int32(r1[i]), int32(r2[i])
			if abs32(p0-q0) >= alpha {
				continue
			}
			p1, q1 := int32(r0[i]), int32(r3[i])
			if abs32(p1-p0) >= beta || abs32(q1-q0) >= beta {
				continue
			}
			if b < 4 {
				delta := clip32(-tc, tc, (((q0-p0)<<2)+(p1-q1)+4)>>3)
				r1[i] = clipPix(p0+delta, maxv)
				r2[i] = clipPix(q0-delta, maxv)
			} else {
				r1[i] = uint16((2*p1 + p0 + q1 + 2) >> 2)
				r2[i] = uint16((2*q1 + q0 + p1 + 2) >> 2)
			}
		}
	}
}

// deblockMBFast filters the edges of macroblock (mbx, mby): the same result as deblockMB for pictures
// without MBAFF in 4:2:0 or 4:0:0.
func (d *Decoder) deblockMBFast(mbx, mby int) {
	pic := d.curPic
	idx := pic.mbIdx(mbx, mby)
	st := d.sliceTable[idx]
	if st == 0xffff || st == 0xfffe {
		return
	}
	sn := int(st) - 1
	if sn >= len(d.slices) {
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

	topXY := idx - stride<<uint(btoi(field))
	leftXY := idx - 1
	availOK := func(i int) bool {
		s := d.sliceTable[i]
		if s == 0xffff {
			return false
		}
		if sp.disable == 2 {
			return s == st
		}
		return true
	}
	var topType, leftType uint32
	if availOK(topXY) {
		topType = pic.mbType[topXY]
	}
	if availOK(leftXY) {
		leftType = pic.mbType[leftXY]
	}
	limit := 4
	if field {
		limit = 2
	}
	two := sp.listCount == 2
	qp := int(d.qpTable[idx])
	t8 := typ&mb8x8DCT != 0
	cat := d.f.cat
	chroma := cat == 1
	qpBd := d.f.qpBdY
	bdY, bdC := d.f.bdY, d.f.bdC
	shY, shC := uint(bdY-8), uint(bdC-8)
	maxY, maxC := d.f.maxY, d.f.maxC
	intra := isIntraDB(typ)
	nzCur := uint(d.mbNZMask[idx])

	yPl, cbPl, crPl := pic.y, pic.cb, pic.cr
	yOff, yStr := d.mbOff[0], d.mbStr[0]
	cOff, cStr := d.mbOff[1], d.mbStr[1]

	var cqpCur [3]int
	if chroma {
		cqpCur[1] = d.chromaQPPrime(qp, sp.cbOff)
		cqpCur[2] = d.chromaQPPrime(qp, sp.crOff)
	}

	// motion of the current macroblock
	var cur [16]blkMotion
	if !intra {
		loadMB(pic, idx, typ, two, &cur)
	}

	for dir := 0; dir < 2; dir++ {
		mbm, mbmType := leftXY, leftType
		if dir == 1 {
			mbm, mbmType = topXY, topType
		}
		// the edge with the neighbouring macroblock
		if mbmType != 0 {
			var bS [4]uint8
			if intra || isIntraDB(mbmType) {
				v := uint8(3)
				if (typ|mbmType)&mbInterlaced == 0 || (d.fieldPic && dir == 0) {
					v = 4
				}
				bS = [4]uint8{v, v, v, v}
			} else {
				nzN := uint(d.mbNZMask[mbm])
				for i := 0; i < 4; i++ {
					var bp, bq int
					if dir == 0 {
						bq, bp = i*4, i*4+3
					} else {
						bq, bp = i, 12+i
					}
					switch {
					case nzCur>>uint(bq)&1|nzN>>uint(bp)&1 != 0:
						bS[i] = 2
					default:
						pm := loadMotion(pic, mbm, bp, two)
						if motionDiffers(&pm, &cur[bq], two, limit) {
							bS[i] = 1
						}
					}
				}
			}
			if bS[0]|bS[1]|bS[2]|bS[3] != 0 {
				qn := int(d.qpTable[mbm])
				e := edgeParams((qp+qn+1)>>1, sp, qpBd, bdY)
				if e.ok {
					if dir == 0 {
						lumaV(yPl, yOff, yStr, &bS, &e, shY, maxY)
					} else {
						lumaH(yPl, yOff, yStr, &bS, &e, shY, maxY)
					}
				}
				if chroma {
					for c := 1; c <= 2; c++ {
						pl, off := cbPl, cOff
						cqn := d.chromaQPPrime(qn, sp.cbOff)
						if c == 2 {
							pl = crPl
							cqn = d.chromaQPPrime(qn, sp.crOff)
						}
						ec := edgeParams((cqpCur[c]+cqn+1)>>1, sp, qpBd, bdC)
						if !ec.ok {
							continue
						}
						if dir == 0 {
							chromaV(pl, off, cStr, &bS, &ec, shC, maxC)
						} else {
							chromaH(pl, off, cStr, &bS, &ec, shC, maxC)
						}
					}
				}
			}
		}
		// internal edges
		var eY, eCb, eCr edgeAB
		var haveE bool
		for edge := 1; edge < 4; edge++ {
			if t8 && edge&1 == 1 {
				continue
			}
			var bS [4]uint8
			if intra {
				bS = [4]uint8{3, 3, 3, 3}
			} else {
				// partitions of a macroblock never differ in motion across edges that no partition
				// boundary lies on
				motion := true
				switch {
				case typ&mb16x16 != 0:
					motion = false
				case typ&mb16x8 != 0:
					motion = dir == 1 && edge == 2
				case typ&mb8x16 != 0:
					motion = dir == 0 && edge == 2
				}
				any := uint8(0)
				for i := 0; i < 4; i++ {
					var bp, bq int
					if dir == 0 {
						bp, bq = i*4+edge-1, i*4+edge
					} else {
						bp, bq = (edge-1)*4+i, edge*4+i
					}
					switch {
					case (nzCur>>uint(bp)|nzCur>>uint(bq))&1 != 0:
						bS[i] = 2
					case motion && motionDiffers(&cur[bp], &cur[bq], two, limit):
						bS[i] = 1
					}
					any |= bS[i]
				}
				if any == 0 {
					continue
				}
			}
			if !haveE {
				haveE = true
				eY = edgeParams(qp, sp, qpBd, bdY)
				if chroma {
					eCb = edgeParams(cqpCur[1], sp, qpBd, bdC)
					eCr = edgeParams(cqpCur[2], sp, qpBd, bdC)
				}
			}
			if eY.ok {
				if dir == 0 {
					lumaV(yPl, yOff+4*edge, yStr, &bS, &eY, shY, maxY)
				} else {
					lumaH(yPl, yOff+4*edge*yStr, yStr, &bS, &eY, shY, maxY)
				}
			}
			if chroma && edge == 2 {
				if dir == 0 {
					if eCb.ok {
						chromaV(cbPl, cOff+4, cStr, &bS, &eCb, shC, maxC)
					}
					if eCr.ok {
						chromaV(crPl, cOff+4, cStr, &bS, &eCr, shC, maxC)
					}
				} else {
					if eCb.ok {
						chromaH(cbPl, cOff+4*cStr, cStr, &bS, &eCb, shC, maxC)
					}
					if eCr.ok {
						chromaH(crPl, cOff+4*cStr, cStr, &bS, &eCr, shC, maxC)
					}
				}
			}
		}
	}
}
