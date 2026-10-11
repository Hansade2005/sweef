package h264

import "errors"

var errCorrupt = errors.New("h264: corrupt macroblock data")

// startMB prepares the state of macroblock (mbx, mby).
func (d *Decoder) startMB(mbx, mby int) {
	d.mbx, d.mby = mbx, mby
	d.mbIdx = d.curPic.mbIdx(mbx, mby)
	d.curDirect = 0
	d.decodedMB++
	d.scan4, d.scan8 = &zigzag4, &zigzag8
	if d.mbField {
		d.scan4, d.scan8 = &fieldScan4, &fieldScan8
	}
	d.bypassMB = false
	d.nzMask = 0
	d.cbpBits = 0
	d.setMBGeometry()
}

func (d *Decoder) clearNZCaches() {
	for p := 0; p < 3; p++ {
		for y := 0; y < 4; y++ {
			for x := 0; x < 4; x++ {
				d.nzCache[p][c0+y*8+x] = 0
			}
		}
	}
}

// writeBackNZ stores the non-zero counts of the current macroblock.
func (d *Decoder) writeBackNZ() {
	idx := d.mbIdx
	nPl := 3
	if d.f.cat == 0 {
		nPl = 1
	}
	for p := 0; p < nPl; p++ {
		w, h := d.nzW(p), d.nzH(p)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				d.nz[idx*48+p*16+y*w+x] = d.nzCache[p][c0+y*8+x]
			}
		}
	}
}

// finishMB stores the per-macroblock tables shared with later macroblocks
// and the deblocking filter.
func (d *Decoder) finishMB(mbType uint32, cbp int) {
	idx := d.mbIdx
	d.curPic.mbType[idx] = mbType
	d.cbpTable[idx] = uint16(cbp) | d.cbpBits
	d.qpTable[idx] = int8(d.qp + d.f.qpBdY)
	d.mbNZMask[idx] = d.nzMask
	d.mbBypass[idx] = d.bypassMB
}

// partition rectangles of the macroblock for motion compensation.
func (d *Decoder) mcParts(mbType uint32, out *[16]rect) int {
	n := 0
	switch {
	case mbType&mb8x8 != 0:
		for i := 0; i < 4; i++ {
			x, y := (i&1)*2, (i>>1)*2
			st := d.subMbType[i]
			switch {
			case st&mbDirect2 != 0:
				if d.hdr.sps.Direct8x8 {
					out[n] = rect{x, y, 2, 2}
					n++
				} else {
					for k := 0; k < 4; k++ {
						out[n] = rect{x + k&1, y + k>>1, 1, 1}
						n++
					}
				}
			case st&mb16x16 != 0:
				out[n] = rect{x, y, 2, 2}
				n++
			case st&mb16x8 != 0:
				out[n] = rect{x, y, 2, 1}
				out[n+1] = rect{x, y + 1, 2, 1}
				n += 2
			case st&mb8x16 != 0:
				out[n] = rect{x, y, 1, 2}
				out[n+1] = rect{x + 1, y, 1, 2}
				n += 2
			default:
				for k := 0; k < 4; k++ {
					out[n] = rect{x + k&1, y + k>>1, 1, 1}
					n++
				}
			}
		}
	case mbType&mb16x16 != 0:
		out[0] = rect{0, 0, 4, 4}
		n = 1
	case mbType&mb16x8 != 0:
		out[0] = rect{0, 0, 4, 2}
		out[1] = rect{0, 2, 4, 2}
		n = 2
	case mbType&mb8x16 != 0:
		out[0] = rect{0, 0, 2, 4}
		out[1] = rect{2, 0, 2, 4}
		n = 2
	default: // direct
		for i := 0; i < 4; i++ {
			x, y := (i&1)*2, (i>>1)*2
			if d.hdr.sps.Direct8x8 {
				out[n] = rect{x, y, 2, 2}
				n++
			} else {
				for k := 0; k < 4; k++ {
					out[n] = rect{x + k&1, y + k>>1, 1, 1}
					n++
				}
			}
		}
	}
	return n
}

func (d *Decoder) motionComp(mbType uint32) {
	d.mbTypeCur = mbType
	var parts [16]rect
	n := d.mcParts(mbType, &parts)
	for i := 0; i < n; i++ {
		d.mcRect(parts[i])
	}
}

// decodeSkipMB handles P_Skip and B_Skip macroblocks.
func (d *Decoder) decodeSkipMB() {
	h := d.hdr
	idx := d.mbIdx
	for k := 0; k < 48; k++ {
		d.nz[idx*48+k] = 0
	}
	var mbType uint32
	if d.mbField {
		mbType |= mbInterlaced
	}
	d.sliceTable[idx] = d.curSlice
	d.fillNeighbors()
	if h.isB() {
		mbType |= mbL0L1 | mbDirect2 | mbSkip
		d.curPic.mbType[idx] = mbType
		if h.directSpatial {
			d.fillCaches(mbType)
		}
		d.predDirect(0xf)
	} else {
		mbType |= mb16x16 | mbP0L0 | mbSkip
		fillRef(&d.refCache[0], 0, 0, 4, 4, 0)
		m := d.predPSkipMotion()
		fillMV(&d.mvCache[0], 0, 0, 4, 4, m)
		fillRef(&d.refCache[1], 0, 0, 4, 4, refNotUsed)
	}
	d.curPic.mbType[idx] = mbType
	d.writeBackMotion(mbType)
	d.cbpTable[idx] = 0
	d.chromaPredTable[idx] = 0
	d.lastDQ = false
	d.finishMB(mbType, 0)
	d.motionComp(mbType)
	d.prevMBSkipped = true
}

func (d *Decoder) readQPDelta() error {
	var dq int
	if d.cabacOn {
		dq = d.cabacQPDelta()
	} else {
		dq = int(d.br.se())
	}
	qpBd := d.f.qpBdY
	if dq < -(26+qpBd/2) || dq > 25+qpBd/2 {
		return errCorrupt
	}
	d.lastDQ = dq != 0
	d.qp = (d.qp+dq+52+2*qpBd)%(52+qpBd) - qpBd
	d.bypassMB = d.f.bypass && d.qp+qpBd == 0
	return nil
}

func (d *Decoder) predIntraMode(n int) int {
	idx := scan8[n]
	l, t := int(d.ipmCache[idx-1]), int(d.ipmCache[idx-8])
	m := min(l, t)
	if m < 0 {
		return 2
	}
	return m
}

// readRef reads ref_idx for a partition with rc active references.
func (d *Decoder) readRef(list, n, rc int) (int, error) {
	if rc <= 1 {
		return 0, nil
	}
	var v int
	if d.cabacOn {
		v = d.cabacRefIdx(list, n)
	} else if rc == 2 {
		v = int(d.br.u1()) ^ 1
	} else {
		v = int(d.br.ue())
	}
	if v < 0 || v >= rc {
		return 0, corrupt()
	}
	return v, nil
}

// readMVD reads a motion vector difference pair for the partition at block n
// and returns it with the stored (clamped) magnitudes.
func (d *Decoder) readMVD(list, n int) (mx, my int, ax, ay uint8) {
	if d.cabacOn {
		i := scan8[n]
		mc := &d.mvdCache[list]
		amvd0 := int(mc[i-1][0]) + int(mc[i-8][0])
		amvd1 := int(mc[i-1][1]) + int(mc[i-8][1])
		mx, ax = d.cabacMVD(40, amvd0)
		my, ay = d.cabacMVD(47, amvd1)
		return
	}
	return int(d.br.se()), int(d.br.se()), 0, 0
}

// decodeMB decodes one non-skipped macroblock (macroblock_layer()).
func (d *Decoder) decodeMB() error {
	h := d.hdr
	pps := h.pps
	idx := d.mbIdx
	br := d.br
	cabac := d.cabacOn
	cat := d.f.cat
	decodeChroma := cat == 1 || cat == 2
	dct8x8Allowed := pps.Transform8x8
	mbaffShift := uint(btoi(d.mbMbaff()))

	d.fillNeighbors()
	var mbType uint32
	partCount := 0
	cbp := 0
	i16Mode := 0
	intraRaw := -1
	switch {
	case h.isB():
		var raw int
		if cabac {
			raw = d.cabacMBTypeB()
		} else {
			raw = int(br.ue())
		}
		if raw < 23 {
			mbType, partCount = bMbTypeInfo[raw].typ, bMbTypeInfo[raw].parts
		} else {
			intraRaw = raw - 23
		}
	case h.isI():
		if cabac {
			intraRaw = d.cabacIntraMBType(true, 0)
		} else {
			intraRaw = int(br.ue())
		}
	default:
		var raw int
		if cabac {
			raw = d.cabacMBTypeP()
		} else {
			raw = int(br.ue())
		}
		if raw < 5 {
			mbType, partCount = pMbTypeInfo[raw].typ, pMbTypeInfo[raw].parts
		} else {
			intraRaw = raw - 5
		}
	}
	if intraRaw >= 0 {
		if intraRaw > 25 {
			return corrupt()
		}
		info := iMbTypeInfo[intraRaw]
		mbType, cbp = info.typ, int(info.cbp)
		if intraRaw >= 1 && intraRaw <= 24 {
			i16Mode = (intraRaw - 1) & 3
		}
	}
	if d.mbField {
		mbType |= mbInterlaced
	}
	d.sliceTable[idx] = d.curSlice

	if mbType&mbIntraPCM != 0 {
		return d.decodePCM(mbType)
	}
	d.fillCaches(mbType)

	chromaMode := 0
	if isIntraT(mbType) {
		if mbType&mbIntra4x4 != 0 {
			step := 1
			if dct8x8Allowed {
				var t8 bool
				if cabac {
					t8 = d.cabacT8()
				} else {
					t8 = br.flag()
				}
				if t8 {
					mbType |= mb8x8DCT
					step = 4
				}
			}
			for i := 0; i < 16; i += step {
				mode := d.predIntraMode(i)
				if cabac {
					if d.cab.bin(68) == 0 {
						rem := d.cab.bin(69)
						rem += 2 * d.cab.bin(69)
						rem += 4 * d.cab.bin(69)
						mode = rem + btoi(rem >= mode)
					}
				} else if !br.flag() {
					rem := int(br.u(3))
					mode = rem + btoi(rem >= mode)
				}
				if step == 4 {
					c := scan8[i]
					d.ipmCache[c], d.ipmCache[c+1], d.ipmCache[c+8], d.ipmCache[c+9] = int8(mode), int8(mode), int8(mode), int8(mode)
				} else {
					d.ipmCache[scan8[i]] = int8(mode)
				}
			}
			for y := 0; y < 4; y++ {
				for x := 0; x < 4; x++ {
					d.ipm[idx*16+y*4+x] = d.ipmCache[c0+y*8+x]
				}
			}
		}
		if decodeChroma {
			if cabac {
				chromaMode = d.cabacIntraChroma()
			} else {
				chromaMode = int(br.ue())
			}
			if chromaMode > 3 {
				return corrupt()
			}
		}
		d.chromaPredTable[idx] = uint8(chromaMode)
	} else if partCount == 4 {
		var subCount [4]int
		isB := h.isB()
		directMask := uint8(0)
		for i := 0; i < 4; i++ {
			var t int
			if isB {
				if cabac {
					t = d.cabacSubTypeB()
				} else {
					t = int(br.ue())
				}
				if t >= 13 {
					return corrupt()
				}
				subCount[i], d.subMbType[i] = bSubMbTypeInfo[t].parts, bSubMbTypeInfo[t].typ
			} else {
				if cabac {
					t = d.cabacSubTypeP()
				} else {
					t = int(br.ue())
				}
				if t >= 4 {
					return corrupt()
				}
				subCount[i], d.subMbType[i] = pSubMbTypeInfo[t].parts, pSubMbTypeInfo[t].typ
			}
			if d.subMbType[i]&mbDirect2 != 0 {
				directMask |= 1 << uint(i)
			}
		}
		if directMask != 0 {
			d.predDirect(directMask)
			for l := 0; l < 2; l++ {
				d.refCache[l][scan8[4]] = refNotAvail
				d.refCache[l][scan8[12]] = refNotAvail
			}
			if cabac {
				for i := 0; i < 4; i++ {
					v := uint8(d.subMbType[i] >> 1)
					c := scan8[4*i]
					d.directCache[c], d.directCache[c+1], d.directCache[c+8], d.directCache[c+9] = v, v, v, v
				}
			}
		}
		var ref [2][4]int
		for list := 0; list < d.listCount; list++ {
			rc := h.numRef[list] << mbaffShift
			if mbType&mbRef0 != 0 {
				rc = 1
			}
			for i := 0; i < 4; i++ {
				if d.subMbType[i]&mbDirect2 != 0 {
					continue
				}
				if isDir(d.subMbType[i], 0, list) {
					v, err := d.readRef(list, 4*i, rc)
					if err != nil {
						return err
					}
					ref[list][i] = v
				} else {
					ref[list][i] = -1
				}
				c := scan8[4*i]
				r := int8(ref[list][i])
				d.refCache[list][c+1], d.refCache[list][c+8], d.refCache[list][c+9] = r, r, r
			}
		}
		if dct8x8Allowed {
			dct8x8Allowed = d.dct8x8Allowed()
		}
		for list := 0; list < d.listCount; list++ {
			for i := 0; i < 4; i++ {
				c := scan8[4*i]
				if d.subMbType[i]&mbDirect2 != 0 {
					d.refCache[list][c] = d.refCache[list][c+1]
					if cabac {
						fillMVD(&d.mvdCache[list], (i&1)*2, (i>>1)*2, 2, 2, [2]uint8{})
					}
					continue
				}
				d.refCache[list][c] = d.refCache[list][c+1]
				if isDir(d.subMbType[i], 0, list) {
					st := d.subMbType[i]
					bw := 1
					if st&(mb16x16|mb16x8) != 0 {
						bw = 2
					}
					for j := 0; j < subCount[i]; j++ {
						index := 4*i + bw*j
						ci := scan8[index]
						mx, my := d.predMotion(index, bw, list, int(d.refCache[list][ci]))
						dx, dy, ax, ay := d.readMVD(list, index)
						mx += dx
						my += dy
						m := mv16{int16(mx), int16(my)}
						mvd := [2]uint8{ax, ay}
						mc := &d.mvCache[list]
						md := &d.mvdCache[list]
						switch {
						case st&mb16x16 != 0: // 8x8
							mc[ci+1], mc[ci+8], mc[ci+9] = m, m, m
							md[ci+1], md[ci+8], md[ci+9] = mvd, mvd, mvd
						case st&mb16x8 != 0: // 8x4
							mc[ci+1] = m
							md[ci+1] = mvd
						case st&mb8x16 != 0: // 4x8
							mc[ci+8] = m
							md[ci+8] = mvd
						}
						mc[ci] = m
						md[ci] = mvd
					}
				} else {
					fillMV(&d.mvCache[list], (i&1)*2, (i>>1)*2, 2, 2, mv16{})
					if cabac {
						fillMVD(&d.mvdCache[list], (i&1)*2, (i>>1)*2, 2, 2, [2]uint8{})
					}
				}
			}
		}
	} else if mbType&mbDirect2 != 0 {
		d.predDirect(0xf)
		if cabac {
			fillMVD(&d.mvdCache[0], 0, 0, 4, 4, [2]uint8{})
			fillMVD(&d.mvdCache[1], 0, 0, 4, 4, [2]uint8{})
		}
		dct8x8Allowed = dct8x8Allowed && h.sps.Direct8x8
	} else {
		for list := 0; list < d.listCount; list++ {
			rc := h.numRef[list] << mbaffShift
			switch {
			case mbType&mb16x16 != 0:
				if isDir(mbType, 0, list) {
					v, err := d.readRef(list, 0, rc)
					if err != nil {
						return err
					}
					fillRef(&d.refCache[list], 0, 0, 4, 4, int8(v))
				}
			case mbType&mb16x8 != 0:
				for i := 0; i < 2; i++ {
					if isDir(mbType, i, list) {
						v, err := d.readRef(list, 8*i, rc)
						if err != nil {
							return err
						}
						fillRef(&d.refCache[list], 0, 2*i, 4, 2, int8(v))
					} else {
						fillRef(&d.refCache[list], 0, 2*i, 4, 2, refNotUsed)
					}
				}
			default: // 8x16
				for i := 0; i < 2; i++ {
					if isDir(mbType, i, list) {
						v, err := d.readRef(list, 4*i, rc)
						if err != nil {
							return err
						}
						fillRef(&d.refCache[list], 2*i, 0, 2, 4, int8(v))
					} else {
						fillRef(&d.refCache[list], 2*i, 0, 2, 4, refNotUsed)
					}
				}
			}
		}
		for list := 0; list < d.listCount; list++ {
			switch {
			case mbType&mb16x16 != 0:
				if isDir(mbType, 0, list) {
					mx, my := d.predMotion(0, 4, list, int(d.refCache[list][c0]))
					dx, dy, ax, ay := d.readMVD(list, 0)
					fillMVD(&d.mvdCache[list], 0, 0, 4, 4, [2]uint8{ax, ay})
					fillMV(&d.mvCache[list], 0, 0, 4, 4, mv16{int16(mx + dx), int16(my + dy)})
				}
			case mbType&mb16x8 != 0:
				for i := 0; i < 2; i++ {
					if isDir(mbType, i, list) {
						mx, my := d.pred16x8Motion(8*i, list, int(d.refCache[list][c0+16*i]))
						dx, dy, ax, ay := d.readMVD(list, 8*i)
						fillMVD(&d.mvdCache[list], 0, 2*i, 4, 2, [2]uint8{ax, ay})
						fillMV(&d.mvCache[list], 0, 2*i, 4, 2, mv16{int16(mx + dx), int16(my + dy)})
					} else {
						fillMVD(&d.mvdCache[list], 0, 2*i, 4, 2, [2]uint8{})
						fillMV(&d.mvCache[list], 0, 2*i, 4, 2, mv16{})
					}
				}
			default:
				for i := 0; i < 2; i++ {
					if isDir(mbType, i, list) {
						mx, my := d.pred8x16Motion(4*i, list, int(d.refCache[list][c0+2*i]))
						dx, dy, ax, ay := d.readMVD(list, 4*i)
						fillMVD(&d.mvdCache[list], 2*i, 0, 2, 4, [2]uint8{ax, ay})
						fillMV(&d.mvCache[list], 2*i, 0, 2, 4, mv16{int16(mx + dx), int16(my + dy)})
					} else {
						fillMVD(&d.mvdCache[list], 2*i, 0, 2, 4, [2]uint8{})
						fillMV(&d.mvCache[list], 2*i, 0, 2, 4, mv16{})
					}
				}
			}
		}
	}
	if isInterT(mbType) || mbType&mbDirect2 != 0 {
		d.chromaPredTable[idx] = 0
		d.writeBackMotion(mbType)
	}

	if mbType&mbIntra16x16 == 0 {
		if cabac {
			cbp = d.cabacCBPLuma()
			if decodeChroma {
				cbp |= d.cabacCBPChroma() << 4
			}
		} else {
			v := int(br.ue())
			if decodeChroma {
				if v > 47 {
					return corrupt()
				}
				if mbType&mbIntra4x4 != 0 {
					cbp = int(golombToIntra4x4CBP[v])
				} else {
					cbp = int(golombToInterCBP[v])
				}
			} else {
				if v > 15 {
					return corrupt()
				}
				if mbType&mbIntra4x4 != 0 {
					cbp = int(golombToIntra4x4CBPGray[v])
				} else {
					cbp = int(golombToInterCBPGray[v])
				}
			}
		}
		if dct8x8Allowed && cbp&15 != 0 && !isIntraT(mbType) {
			var t8 bool
			if cabac {
				t8 = d.cabacT8()
			} else {
				t8 = br.flag()
			}
			if t8 {
				mbType |= mb8x8DCT
			}
		}
	}
	d.curPic.mbType[idx] = mbType
	t8 := mbType&mb8x8DCT != 0
	d.bypassMB = d.f.bypass && d.qp+d.f.qpBdY == 0
	for i := range d.coef {
		d.coef[i] = 0
	}
	d.clearNZCaches()
	if cbp != 0 || mbType&mbIntra16x16 != 0 {
		if err := d.readQPDelta(); err != nil {
			return err
		}
		if err := d.parseResidual(isIntraT(mbType), mbType&mbIntra16x16 != 0, cbp, t8); err != nil {
			return err
		}
	} else {
		d.lastDQ = false
	}
	d.writeBackNZ()
	d.finishMB(mbType, cbp)
	if isIntraT(mbType) {
		d.reconIntra(mbType, i16Mode, chromaMode, cbp, t8)
	} else {
		d.motionComp(mbType)
		d.addInterResidual(cbp, t8)
	}
	d.prevMBSkipped = false
	return nil
}

// dct8x8Allowed reports whether the 8x8 transform may be used by a B_8x8 /
// P_8x8 macroblock given its sub-macroblock types.
func (d *Decoder) dct8x8Allowed() bool {
	for i := 0; i < 4; i++ {
		st := d.subMbType[i]
		if st&mbDirect2 != 0 {
			if !d.hdr.sps.Direct8x8 {
				return false
			}
			continue
		}
		if st&(mb16x8|mb8x16|mb8x8) != 0 {
			return false
		}
	}
	return true
}

func (d *Decoder) decodePCM(mbType uint32) error {
	idx := d.mbIdx
	pic := d.cur
	var r *bitReader
	if d.cabacOn {
		// after the terminating bin the stream continues byte aligned
		pos := (d.cab.pos + 7) &^ 7
		r = &bitReader{b: d.cab.data, pos: pos, end: len(d.cab.data) * 8}
	} else {
		d.br.byteAlign()
		r = d.br
	}
	need := 256 * d.f.bdY
	if d.f.cat != 0 {
		need += 2 * d.f.mbWC * d.f.mbHC * d.f.bdC
	}
	if r.pos+need > r.end || (d.cabacOn && r.pos+need > len(r.b)*8-64) {
		return corrupt()
	}
	_ = pic
	pl, _ := d.planeOf(0)
	for y := 0; y < 16; y++ {
		row := pl[d.mbOff[0]+y*d.mbStr[0]:]
		for x := 0; x < 16; x++ {
			row[x] = uint16(r.u(uint(d.f.bdY)))
		}
	}
	if d.f.cat != 0 {
		for c := 1; c <= 2; c++ {
			cpl, _ := d.planeOf(c)
			for y := 0; y < d.f.mbHC; y++ {
				row := cpl[d.mbOff[c]+y*d.mbStr[c]:]
				for x := 0; x < d.f.mbWC; x++ {
					row[x] = uint16(r.u(uint(d.f.bdC)))
				}
			}
		}
	}
	if d.cabacOn {
		d.cab.start(r.pos)
	}
	for k := 0; k < 48; k++ {
		d.nz[idx*48+k] = 16
	}
	d.curPic.mbType[idx] = mbType
	d.cbpTable[idx] = 0xf7ef
	d.chromaPredTable[idx] = 0
	d.qpTable[idx] = 0
	d.mbNZMask[idx] = 0xffff
	d.mbBypass[idx] = false
	d.lastDQ = false
	d.prevMBSkipped = false
	return nil
}
