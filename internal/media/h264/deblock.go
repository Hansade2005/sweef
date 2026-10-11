package h264

// In-loop deblocking filter (8.7).

func (d *Decoder) deblockPicture(pic *Picture) {
	for mby := 0; mby < d.mbH; mby++ {
		for mbx := 0; mbx < d.mbW; mbx++ {
			idx := mby*d.mbW + mbx
			s := d.mbSlice[idx]
			if s < 0 {
				continue
			}
			sp := d.slices[s-1]
			if sp.disable == 1 {
				continue
			}
			d.deblockMB(pic, mbx, mby, idx, &sp)
		}
	}
}

// refPair returns the picture ids a block refers to in both lists.
func (d *Decoder) blockRefs(x4, y4 int) (r0, r1 int32) {
	o := (y4>>1)*d.mbW*2 + x4>>1
	return d.cur.refID[0][o], d.cur.refID[1][o]
}

func mvFar(a, b mv16) bool {
	dx := int(a.x) - int(b.x)
	dy := int(a.y) - int(b.y)
	return dx >= 4 || dx <= -4 || dy >= 4 || dy <= -4
}

// mvBS returns 1 when the motion of the two 4x4 blocks differs enough for
// bS=1, else 0.
func (d *Decoder) mvBS(px, py, qx, qy int) int {
	pic := d.cur
	w4 := d.mbW * 4
	p0, p1 := d.blockRefs(px, py)
	q0, q1 := d.blockRefs(qx, qy)
	np := btoi(p0 >= 0) + btoi(p1 >= 0)
	nq := btoi(q0 >= 0) + btoi(q1 >= 0)
	if np != nq {
		return 1
	}
	mp0, mp1 := pic.mv[0][py*w4+px], pic.mv[1][py*w4+px]
	mq0, mq1 := pic.mv[0][qy*w4+qx], pic.mv[1][qy*w4+qx]
	if np == 1 {
		var rp, rq int32
		var mp, mq mv16
		if p0 >= 0 {
			rp, mp = p0, mp0
		} else {
			rp, mp = p1, mp1
		}
		if q0 >= 0 {
			rq, mq = q0, mq0
		} else {
			rq, mq = q1, mq1
		}
		if rp != rq {
			return 1
		}
		return btoi(mvFar(mp, mq))
	}
	if np == 0 {
		return 0
	}
	// two references each
	if !((p0 == q0 && p1 == q1) || (p0 == q1 && p1 == q0)) {
		return 1
	}
	if p0 != p1 {
		if p0 == q0 {
			return btoi(mvFar(mp0, mq0) || mvFar(mp1, mq1))
		}
		return btoi(mvFar(mp0, mq1) || mvFar(mp1, mq0))
	}
	return btoi((mvFar(mp0, mq0) || mvFar(mp1, mq1)) && (mvFar(mp0, mq1) || mvFar(mp1, mq0)))
}

func (d *Decoder) edgeBS(idxP, idxQ, pRaster, qRaster, px, py, qx, qy int, mbEdge bool) int {
	if isIntraMB(d.mbType[idxP]) || isIntraMB(d.mbType[idxQ]) {
		if mbEdge {
			return 4
		}
		return 3
	}
	if d.nzMask[idxP]>>uint(pRaster)&1 != 0 || d.nzMask[idxQ]>>uint(qRaster)&1 != 0 {
		return 2
	}
	return d.mvBS(px, py, qx, qy)
}

func (d *Decoder) deblockMB(pic *Picture, mbx, mby, idx int, sp *sliceParams) {
	t8 := d.mbT8[idx]
	leftOK := mbx > 0 && (sp.disable != 2 || d.mbSlice[idx-1] == d.mbSlice[idx])
	topOK := mby > 0 && (sp.disable != 2 || d.mbSlice[idx-d.mbW] == d.mbSlice[idx])
	var bsV, bsH [4][4]int
	for e := 0; e < 4; e++ {
		for g := 0; g < 4; g++ {
			// vertical edge e, rows group g: q block (e, g)
			if e == 0 {
				if leftOK {
					bsV[e][g] = d.edgeBS(idx-1, idx, g*4+3, g*4, mbx*4-1, mby*4+g, mbx*4, mby*4+g, true)
				}
			} else {
				bsV[e][g] = d.edgeBS(idx, idx, g*4+e-1, g*4+e, mbx*4+e-1, mby*4+g, mbx*4+e, mby*4+g, false)
			}
			// horizontal edge e, column group g: q block (g, e)
			if e == 0 {
				if topOK {
					bsH[e][g] = d.edgeBS(idx-d.mbW, idx, 12+g, g, mbx*4+g, mby*4-1, mbx*4+g, mby*4, true)
				}
			} else {
				bsH[e][g] = d.edgeBS(idx, idx, (e-1)*4+g, e*4+g, mbx*4+g, mby*4+e-1, mbx*4+g, mby*4+e, false)
			}
		}
	}
	for c := 0; c < 3; c++ {
		var pl []byte
		var stride, size, base int
		switch c {
		case 0:
			pl, stride, size = pic.y, pic.ys, 16
			base = mby*16*stride + mbx*16
		case 1:
			pl, stride, size = pic.cb, pic.cs, 8
			base = mby*8*stride + mbx*8
		default:
			pl, stride, size = pic.cr, pic.cs, 8
			base = mby*8*stride + mbx*8
		}
		// per-edge QP average
		qpOf := func(i int) int {
			q := int(d.mbQP[i])
			if c == 0 {
				return q
			}
			off := sp.cbOff
			if c == 2 {
				off = sp.crOff
			}
			return chromaQP(q, off)
		}
		qpq := qpOf(idx)
		// vertical edges
		edges := 4
		if c > 0 {
			edges = 2
		}
		for e := 0; e < edges; e++ {
			var bs [4]int
			var qpAv int
			if c == 0 {
				if t8 && e&1 == 1 {
					continue
				}
				bs = bsV[e]
			} else {
				bs = bsV[e*2]
			}
			if e == 0 {
				if !leftOK {
					continue
				}
				qpAv = (qpOf(idx-1) + qpq + 1) >> 1
			} else {
				qpAv = qpq
			}
			filterEdge(pl, base+e*4, 1, stride, size, bs, qpAv, sp, c > 0)
		}
		for e := 0; e < edges; e++ {
			var bs [4]int
			var qpAv int
			if c == 0 {
				if t8 && e&1 == 1 {
					continue
				}
				bs = bsH[e]
			} else {
				bs = bsH[e*2]
			}
			if e == 0 {
				if !topOK {
					continue
				}
				qpAv = (qpOf(idx-d.mbW) + qpq + 1) >> 1
			} else {
				qpAv = qpq
			}
			filterEdge(pl, base+e*4*stride, stride, 1, size, bs, qpAv, sp, c > 0)
		}
	}
}

// filterEdge filters one edge of length n samples. across is the step across
// the edge, along the step along it. bs holds the four boundary strengths
// (one per quarter of the edge).
func filterEdge(pl []byte, pos, across, along, n int, bs [4]int, qpAv int, sp *sliceParams, chroma bool) {
	indexA := clip3(0, 51, qpAv+sp.alpha)
	indexB := clip3(0, 51, qpAv+sp.beta)
	alpha := int(alphaTable[indexA])
	beta := int(betaTable[indexB])
	if alpha == 0 || beta == 0 {
		return
	}
	per := n / 4
	for g := 0; g < 4; g++ {
		b := bs[g]
		if b == 0 {
			continue
		}
		tc0 := 0
		if b < 4 {
			tc0 = int(tc0Table[indexA][b])
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
					pl[o-across] = clip255(int32(p0 + delta))
					pl[o] = clip255(int32(q0 - delta))
				} else {
					pl[o-across] = byte((2*p1 + p0 + q1 + 2) >> 2)
					pl[o] = byte((2*q1 + q0 + p1 + 2) >> 2)
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
				pl[o-across] = clip255(int32(p0 + delta))
				pl[o] = clip255(int32(q0 - delta))
				if ap < beta {
					pl[o-2*across] = byte(p1 + clip3(-tc0, tc0, (p2+((p0+q0+1)>>1)-(p1<<1))>>1))
				}
				if aq < beta {
					pl[o+across] = byte(q1 + clip3(-tc0, tc0, (q2+((p0+q0+1)>>1)-(q1<<1))>>1))
				}
				continue
			}
			// bS == 4
			small := abs(p0-q0) < (alpha>>2)+2
			if ap < beta && small {
				p3 := int(pl[o-4*across])
				pl[o-across] = byte((p2 + 2*p1 + 2*p0 + 2*q0 + q1 + 4) >> 3)
				pl[o-2*across] = byte((p2 + p1 + p0 + q0 + 2) >> 2)
				pl[o-3*across] = byte((2*p3 + 3*p2 + p1 + p0 + q0 + 4) >> 3)
			} else {
				pl[o-across] = byte((2*p1 + p0 + q1 + 2) >> 2)
			}
			if aq < beta && small {
				q3 := int(pl[o+3*across])
				pl[o] = byte((p1 + 2*p0 + 2*q0 + 2*q1 + q2 + 4) >> 3)
				pl[o+across] = byte((p0 + q0 + q1 + q2 + 2) >> 2)
				pl[o+2*across] = byte((2*q3 + 3*q2 + q1 + q0 + p0 + 4) >> 3)
			} else {
				pl[o] = byte((2*q1 + q0 + p1 + 2) >> 2)
			}
		}
	}
}
