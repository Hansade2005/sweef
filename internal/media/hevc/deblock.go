package hevc

// In-loop deblocking filter (8.7.2), applied to a whole picture after all of
// its slices have been decoded.

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
	if a.pf == 3 && b.pf == 3 {
		a0, a1, b0, b1 := a.poc[0], a.poc[1], b.poc[0], b.poc[1]
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
		if a.poc[la] != b.poc[lb] {
			return 1
		}
		if mvDiff(a.mv[la], b.mv[lb]) {
			return 1
		}
		return 0
	}
	return 1
}

func (d *Decoder) deblockPicture(pic *Picture) {
	s := pic.sps
	pp := d.apps
	if pp == nil || s != d.asps || len(d.slices) == 0 {
		return
	}
	d.sp, d.pp = s, pp
	any := false
	for _, sh := range d.slices {
		if !sh.deblockDisabled {
			any = true
			break
		}
	}
	if !any {
		return
	}
	d.deblockPass(pic, true)
	d.deblockPass(pic, false)
}

func (d *Decoder) sliceOfCtb(ctb int) *sliceHeader {
	i := d.ctbSliceIdx[ctb]
	if i < 0 || int(i) >= len(d.slices) {
		return nil
	}
	return d.slices[i]
}

func (d *Decoder) deblockPass(pic *Picture, vertical bool) {
	s, pp := d.sp, d.pp
	w4, h4 := s.wTb4, s.hTb4
	l2c := uint(s.log2Ctb)
	l2m := uint(s.log2MinCb)
	cat := s.chromaArrayType
	for by := 0; by < h4; by++ {
		for bx := 0; bx < w4; bx++ {
			var e uint8
			if vertical {
				if bx&1 != 0 || bx == 0 {
					continue
				}
				e = d.edge[by*w4+bx] & 3
			} else {
				if by&1 != 0 || by == 0 {
					continue
				}
				e = (d.edge[by*w4+bx] >> 2) & 3
			}
			if e == 0 {
				continue
			}
			xq, yq := bx<<2, by<<2
			xp, yp := xq, yq
			if vertical {
				xp--
			} else {
				yp--
			}
			ctbQ := (yq>>l2c)*s.wCtb + xq>>l2c
			ctbP := (yp>>l2c)*s.wCtb + xp>>l2c
			if dbgEdge != nil {
				dbgEdge(d, pic, vertical, xq, yq, e)
			}
			sl := d.sliceOfCtb(ctbQ)
			if sl == nil || sl.deblockDisabled {
				continue
			}
			if ctbP != ctbQ {
				slP := d.sliceOfCtb(ctbP)
				if slP == nil {
					continue
				}
				if d.ctbSliceAddr[ctbP] != d.ctbSliceAddr[ctbQ] && !sl.loopFilterAcross {
					continue
				}
				if pp.tileRs[ctbP] != pp.tileRs[ctbQ] && !pp.loopFilterAcrossTiles {
					continue
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
				continue
			}
			qpL := (int(cq.qp) + int(cp.qp) + 1) >> 1
			noP := cp.flags&cuNoFilter != 0
			noQ := cq.flags&cuNoFilter != 0
			// luma
			{
				bq := clip3(0, 51, qpL+(sl.betaOffsetDiv2<<1))
				beta := int(betaTable[bq]) << uint(s.bitDepthY-8)
				tq := clip3(0, 53, qpL+2*(int(bs)-1)+(sl.tcOffsetDiv2<<1))
				tc := int(tcTable[tq]) << uint(s.bitDepthY-8)
				stride := pic.stride[0]
				if vertical {
					filterLuma(pic.planes[0], yq*stride+xq, 1, stride, beta, tc, noP, noQ, s.bitDepthY)
				} else {
					filterLuma(pic.planes[0], yq*stride+xq, stride, 1, beta, tc, noP, noQ, s.bitDepthY)
				}
			}
			if cat == 0 || bs != 2 {
				continue
			}
			// chroma
			var cx, cy int
			cx, cy = xq>>s.subW, yq>>s.subH
			// chroma edges lie on an 8 sample chroma grid and are filtered in
			// segments of 4 chroma samples that use the strength and QPs of the
			// luma position of their first sample.
			if vertical {
				if cx&7 != 0 || yq&((4<<s.subH)-1) != 0 {
					continue
				}
			} else if cy&7 != 0 || xq&((4<<s.subW)-1) != 0 {
				continue
			}
			for ci := 1; ci <= 2; ci++ {
				off := pp.cbQpOffset
				if ci == 2 {
					off = pp.crQpOffset
				}
				qpi := qpL + off
				var qpc int
				if cat == 1 {
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
				tq := clip3(0, 53, qpc+2*(int(bs)-1)+(sl.tcOffsetDiv2<<1))
				tc := int(tcTable[tq]) << uint(s.bitDepthC-8)
				stride := pic.stride[ci]
				if dbgDeblock != nil {
					dbgDeblock(pic, ci, vertical, cx, cy, int(bs), qpL, tc, noP, noQ)
				}
				if vertical {
					filterChroma(pic.planes[ci], cy*stride+cx, 1, stride, 4, tc, noP, noQ, s.bitDepthC)
				} else {
					filterChroma(pic.planes[ci], cy*stride+cx, stride, 1, 4, tc, noP, noQ, s.bitDepthC)
				}
			}
		}
	}
}

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
