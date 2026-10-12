package hevc

// In-loop deblocking filter (8.7.2), applied to a whole picture after all of
// its slices have been decoded.

var dbgNoDeblock, dbgNoVcopy bool
var dbgEdge func(d *Decoder, pic *Picture, vertical bool, xq, yq int, e uint8)
var dbgDeblock func(pic *Picture, ci int, vertical bool, cx, cy, bs, qpL, tc int, noP, noQ bool)

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func mvDiff(a, b [2]int16) bool {
	return absInt(int(a[0])-int(b[0])) >= 4 || absInt(int(a[1])-int(b[1])) >= 4
}

// motionBS compares the motion of two inter blocks (boundary strength 0 or 1).
func motionBS(a, b *mvField) uint8 {
	// a reference picture is identified by its POC and layer
	key := func(m *mvField, l int) int64 { return int64(m.poc[l])<<8 | int64(m.lay[l]) }
	if a.pf == 3 && b.pf == 3 {
		a0, a1, b0, b1 := key(a, 0), key(a, 1), key(b, 0), key(b, 1)
		if !((a0 == b0 && a1 == b1) || (a0 == b1 && a1 == b0)) {
			return 1
		}
		if a0 != a1 {
			if a0 == b0 {
				if mvDiff(a.mv[0], b.mv[0]) || mvDiff(a.mv[1], b.mv[1]) {
					return 1
				}
			} else if mvDiff(a.mv[0], b.mv[1]) || mvDiff(a.mv[1], b.mv[0]) {
				return 1
			}
			return 0
		}
		if (mvDiff(a.mv[0], b.mv[0]) || mvDiff(a.mv[1], b.mv[1])) &&
			(mvDiff(a.mv[0], b.mv[1]) || mvDiff(a.mv[1], b.mv[0])) {
			return 1
		}
		return 0
	}
	if a.pf != 3 && b.pf != 3 {
		la, lb := 0, 0
		if a.pf == 2 {
			la = 1
		}
		if b.pf == 2 {
			lb = 1
		}
		if key(a, la) != key(b, lb) {
			return 1
		}
		if mvDiff(a.mv[la], b.mv[lb]) {
			return 1
		}
		return 0
	}
	return 1
}

func (d *Decoder) sliceOfCtb(ctb int) *sliceHeader {
	i := d.ctbSliceIdx[ctb]
	if i < 0 || int(i) >= len(d.slices) {
		return nil
	}
	return d.slices[i]
}

// deblockUnit filters the edge segment on the left (vertical) or top
// (horizontal) side of the 4x4 unit (bx, by): the luma segment of 4 samples
// and, when the edge lies on the chroma 8 sample grid and has strength 2, the
// 4 chroma samples starting at the same position.
func (d *Decoder) deblockUnit(pic *Picture, vertical bool, bx, by int, doLuma bool, plane int, tcOv, betaOv int) uint8 {
	s, pp := d.sp, d.pp
	w4 := s.wTb4
	if bx >= w4 || by >= s.hTb4 {
		return 0
	}
	var e uint8
	if vertical {
		if bx&1 != 0 || bx == 0 {
			return 0
		}
		e = d.edge[by*w4+bx] & 3
	} else {
		if by&1 != 0 || by == 0 {
			return 0
		}
		e = (d.edge[by*w4+bx] >> 2) & 3
	}
	if e == 0 {
		return 0
	}
	l2c := uint(s.log2Ctb)
	l2m := uint(s.log2MinCb)
	xq, yq := bx<<2, by<<2
	xp, yp := xq, yq
	if vertical {
		xp--
	} else {
		yp--
	}
	ctbQ := (yq>>l2c)*s.wCtb + xq>>l2c
	ctbP := (yp>>l2c)*s.wCtb + xp>>l2c
	sl := d.sliceOfCtb(ctbQ)
	if sl == nil || sl.deblockDisabled {
		return 0
	}
	if ctbP != ctbQ {
		if d.sliceOfCtb(ctbP) == nil {
			return 0
		}
		if d.ctbSliceAddr[ctbP] != d.ctbSliceAddr[ctbQ] && !sl.loopFilterAcross {
			return 0
		}
		if pp.tileRs[ctbP] != pp.tileRs[ctbQ] && !pp.loopFilterAcrossTiles {
			return 0
		}
	}
	cq := d.cuTab[(yq>>l2m)*s.wMinCb+xq>>l2m]
	cp := d.cuTab[(yp>>l2m)*s.wMinCb+xp>>l2m]
	var bs uint8
	switch {
	case cq.flags&cuIntra != 0 || cp.flags&cuIntra != 0:
		bs = 2
	case e&1 != 0 && (d.cbfTab[by*w4+bx] != 0 || d.cbfTab[(yp>>2)*w4+xp>>2] != 0):
		bs = 1
	default:
		bs = motionBS(&pic.mvf[(yq>>2)*pic.w4+xq>>2], &pic.mvf[(yp>>2)*pic.w4+xp>>2])
	}
	if bs == 0 {
		return 0
	}
	qpL := (int(cq.qp) + int(cp.qp) + 1) >> 1
	noP := cp.flags&(cuNoFilter|cuPalette) != 0
	noQ := cq.flags&(cuNoFilter|cuPalette) != 0
	if doLuma {
		betaOff, tcOffL := sl.betaOffsetDiv2, sl.tcOffsetDiv2
		if tcOv != noTcOv {
			tcOffL = tcOv
		}
		if betaOv != noTcOv {
			betaOff = betaOv
		}
		bq := clip3(0, 51, qpL+(betaOff<<1))
		beta := int(betaTable[bq]) << uint(s.bitDepthY-8)
		tq := clip3(0, 53, qpL+2*(int(bs)-1)+(tcOffL<<1))
		tc := int(tcTable[tq]) << uint(s.bitDepthY-8)
		stride := pic.stride[0]
		if vertical {
			filterLuma(pic.planes[0], yq*stride+xq, 1, stride, beta, tc, noP, noQ, s.bitDepthY)
		} else {
			filterLuma(pic.planes[0], yq*stride+xq, stride, 1, beta, tc, noP, noQ, s.bitDepthY)
		}
	}
	if plane == 0 || s.chromaArrayType == 0 || bs != 2 {
		return bs
	}
	tcOff := sl.tcOffsetDiv2
	if tcOv != noTcOv {
		tcOff = tcOv
	}
	cx, cy := xq>>s.subW, yq>>s.subH
	// chroma edges lie on an 8 sample chroma grid and are filtered in
	// segments of 4 chroma samples that use the strength and QPs of the
	// luma position of their first sample.
	if vertical {
		if cx&7 != 0 || yq&((4<<s.subH)-1) != 0 {
			return bs
		}
	} else if cy&7 != 0 || xq&((4<<s.subW)-1) != 0 {
		return bs
	}
	for ci := plane; ci <= plane; ci++ {
		off := pp.cbQpOffset
		if ci == 2 {
			off = pp.crQpOffset
		}
		qpi := qpL + off
		var qpc int
		if s.chromaArrayType == 1 {
			switch {
			case qpi < 30:
				qpc = qpi
			case qpi >= 43:
				qpc = qpi - 6
			default:
				qpc = qpCTable[qpi-30]
			}
		} else {
			qpc = qpi
			if qpc > 51 {
				qpc = 51
			}
		}
		tq := clip3(0, 53, qpc+2*(int(bs)-1)+(tcOff<<1))
		tc := int(tcTable[tq]) << uint(s.bitDepthC-8)
		stride := pic.stride[ci]
		if vertical {
			filterChroma(pic.planes[ci], cy*stride+cx, 1, stride, 4, tc, noP, noQ, s.bitDepthC)
		} else {
			filterChroma(pic.planes[ci], cy*stride+cx, stride, 1, 4, tc, noP, noQ, s.bitDepthC)
		}
	}
	return bs
}

// deblockCTB deblocks the edges belonging to the CTB at luma position
// (x0, y0). The order of operations follows a CTB-by-CTB schedule in which the
// horizontal edges of a CTB column are filtered when the next CTB is
// processed, so that the vertical edge on its right has already been done.
func (d *Decoder) deblockCTB(pic *Picture, x0, y0 int) {
	s := d.sp
	ctb := s.ctbSize
	xEnd := min(x0+ctb, s.width)
	yEnd := min(y0+ctb, s.height)
	xEnd2 := xEnd
	if xEnd2 != s.width {
		xEnd2 -= 8
	}
	// ffmpeg takes the tc offsets of chroma edges from the current and the
	// left CTB in a way that does not always follow the slice of the edge;
	// tcSt mimics its variable that carries over between the loops.
	ctbAddr := (y0>>uint(s.log2Ctb))*s.wCtb + x0>>uint(s.log2Ctb)
	offOf := func(c int) (int, int) {
		if sl := d.sliceOfCtb(c); sl != nil {
			return sl.betaOffsetDiv2, sl.tcOffsetDiv2
		}
		return 0, 0
	}
	curBeta, curTc := offOf(ctbAddr)
	leftBeta, leftTc := 0, 0
	if x0 > 0 {
		leftBeta, leftTc = offOf(ctbAddr - 1)
	}
	tcSt, betaSt := curTc, curBeta
	for y := y0; y < yEnd; y += 8 {
		for x := max(x0, 8); x < xEnd; x += 8 {
			d.deblockUnit(pic, true, x>>2, y>>2, true, 0, tcSt, betaSt)
			d.deblockUnit(pic, true, x>>2, y>>2+1, true, 0, tcSt, betaSt)
		}
		if y == 0 {
			continue
		}
		xs := 0
		if x0 > 0 {
			xs = x0 - 8
		}
		for x := xs; x < xEnd2; x += 8 {
			b0 := d.deblockUnit(pic, false, x>>2, y>>2, true, 0, noTcOv, noTcOv)
			b1 := d.deblockUnit(pic, false, x>>2+1, y>>2, true, 0, noTcOv, noTcOv)
			if b0 != 0 || b1 != 0 {
				if x >= x0 {
					tcSt, betaSt = curTc, curBeta
				} else {
					tcSt, betaSt = leftTc, leftBeta
				}
			}
		}
	}
	if s.chromaArrayType == 0 {
		return
	}
	h, v := 1<<s.subW, 1<<s.subH
	for pl := 1; pl <= 2; pl++ {
		for y := y0; y < yEnd; y += 8 * v {
			for x := max(x0, 8*h); x < xEnd; x += 8 * h {
				d.deblockUnit(pic, true, x>>2, y>>2, false, pl, tcSt, noTcOv)
				d.deblockUnit(pic, true, x>>2, (y+4*v)>>2, false, pl, tcSt, noTcOv)
			}
			if y == 0 {
				continue
			}
			xe := xEnd
			if xEnd != s.width {
				xe = xEnd - 8*h
			}
			xs := 0
			if x0 > 0 {
				xs = x0 - 8*h
			}
			tcSt = curTc
			if x0 > 0 {
				tcSt = leftTc
			}
			for x := xs; x < xe; x += 8 * h {
				d.deblockUnit(pic, false, x>>2, y>>2, false, pl, tcSt, noTcOv)
				d.deblockUnit(pic, false, (x+4*h)>>2, y>>2, false, pl, curTc, noTcOv)
			}
		}
	}
}

const noTcOv = 1 << 20

// filterLuma filters one 4-sample long luma edge segment. q0 of the first line
// is at index base; across steps from p to q, along steps between lines.
func filterLuma(pl []uint16, base, across, along, beta, tc int, noP, noQ bool, bd int) {
	px := func(line, i int) int { return int(pl[base+line*along-(i+1)*across]) }
	qx := func(line, i int) int { return int(pl[base+line*along+i*across]) }
	dp0 := absInt(px(0, 2) - 2*px(0, 1) + px(0, 0))
	dp3 := absInt(px(3, 2) - 2*px(3, 1) + px(3, 0))
	dq0 := absInt(qx(0, 2) - 2*qx(0, 1) + qx(0, 0))
	dq3 := absInt(qx(3, 2) - 2*qx(3, 1) + qx(3, 0))
	dpq0 := dp0 + dq0
	dpq3 := dp3 + dq3
	dp := dp0 + dp3
	dq := dq0 + dq3
	if dpq0+dpq3 >= beta {
		return
	}
	dSam := func(line, dpq int) bool {
		return 2*dpq < (beta>>2) &&
			absInt(px(line, 3)-px(line, 0))+absInt(qx(line, 0)-qx(line, 3)) < (beta>>3) &&
			absInt(px(line, 0)-qx(line, 0)) < ((5*tc+1)>>1)
	}
	strong := dSam(0, dpq0) && dSam(3, dpq3)
	dEp := dp < ((beta + (beta >> 1)) >> 3)
	dEq := dq < ((beta + (beta >> 1)) >> 3)
	maxV := 1<<uint(bd) - 1
	for line := 0; line < 4; line++ {
		o := base + line*along
		p0, p1, p2, p3 := int(pl[o-across]), int(pl[o-2*across]), int(pl[o-3*across]), int(pl[o-4*across])
		q0, q1, q2, q3 := int(pl[o]), int(pl[o+across]), int(pl[o+2*across]), int(pl[o+3*across])
		if strong {
			if !noP {
				pl[o-across] = uint16(clip3(p0-2*tc, p0+2*tc, (p2+2*p1+2*p0+2*q0+q1+4)>>3))
				pl[o-2*across] = uint16(clip3(p1-2*tc, p1+2*tc, (p2+p1+p0+q0+2)>>2))
				pl[o-3*across] = uint16(clip3(p2-2*tc, p2+2*tc, (2*p3+3*p2+p1+p0+q0+4)>>3))
			}
			if !noQ {
				pl[o] = uint16(clip3(q0-2*tc, q0+2*tc, (p1+2*p0+2*q0+2*q1+q2+4)>>3))
				pl[o+across] = uint16(clip3(q1-2*tc, q1+2*tc, (p0+q0+q1+q2+2)>>2))
				pl[o+2*across] = uint16(clip3(q2-2*tc, q2+2*tc, (p0+q0+q1+3*q2+2*q3+4)>>3))
			}
			continue
		}
		delta := (9*(q0-p0) - 3*(q1-p1) + 8) >> 4
		if absInt(delta) >= tc*10 {
			continue
		}
		delta = clip3(-tc, tc, delta)
		if !noP {
			pl[o-across] = uint16(clip3(0, maxV, p0+delta))
			if dEp {
				dpv := clip3(-(tc >> 1), tc>>1, (((p2+p0+1)>>1)-p1+delta)>>1)
				pl[o-2*across] = uint16(clip3(0, maxV, p1+dpv))
			}
		}
		if !noQ {
			pl[o] = uint16(clip3(0, maxV, q0-delta))
			if dEq {
				dqv := clip3(-(tc >> 1), tc>>1, (((q2+q0+1)>>1)-q1-delta)>>1)
				pl[o+across] = uint16(clip3(0, maxV, q1+dqv))
			}
		}
	}
}

func filterChroma(pl []uint16, base, across, along, n, tc int, noP, noQ bool, bd int) {
	maxV := 1<<uint(bd) - 1
	for line := 0; line < n; line++ {
		o := base + line*along
		p0, p1 := int(pl[o-across]), int(pl[o-2*across])
		q0, q1 := int(pl[o]), int(pl[o+across])
		delta := clip3(-tc, tc, ((((q0 - p0) << 2) + p1 - q1 + 4) >> 3))
		if !noP {
			pl[o-across] = uint16(clip3(0, maxV, p0+delta))
		}
		if !noQ {
			pl[o] = uint16(clip3(0, maxV, q0-delta))
		}
	}
}
