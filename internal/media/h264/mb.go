package h264

import "errors"

var errCorrupt = errors.New("h264: corrupt macroblock data")

// startMB prepares neighbour availability for macroblock mbAddr.
func (d *Decoder) startMB(mbAddr int) {
	d.mbIdx = mbAddr
	d.mbx, d.mby = mbAddr%d.mbW, mbAddr/d.mbW
	d.mbSlice[mbAddr] = d.curSlice
	d.availA = d.mbx > 0 && d.mbSlice[mbAddr-1] == d.curSlice
	d.availB = d.mby > 0 && d.mbSlice[mbAddr-d.mbW] == d.curSlice
	d.availC = d.mby > 0 && d.mbx < d.mbW-1 && d.mbSlice[mbAddr-d.mbW+1] == d.curSlice
	d.availD = d.mbx > 0 && d.mby > 0 && d.mbSlice[mbAddr-d.mbW-1] == d.curSlice
	d.curDirect = 0
	d.decodedMB++
}

func (d *Decoder) finishMBCommon(typ uint8, cbp int, t8 bool, chroma int) {
	i := d.mbIdx
	d.mbType[i] = typ
	d.mbCBP[i] = uint8(cbp)
	d.mbT8[i] = t8
	d.mbChroma[i] = uint8(chroma)
	d.mbDirect[i] = d.curDirect
	d.mbQP[i] = int8(d.qp)
}

// decodeSkipMB handles P_Skip and B_Skip macroblocks.
func (d *Decoder) decodeSkipMB(mbAddr int) {
	d.startMB(mbAddr)
	i := d.mbIdx
	for k := 0; k < 24; k++ {
		d.nz[i*24+k] = 0
	}
	d.mbDC[i] = 0
	d.nzMask[i] = 0
	d.lastDQ = false
	h := d.hdr
	var parts [16]rect
	np := 0
	if h.isB() {
		d.curDirect = 0xf
		d.directMB(0xf)
		d.finishMBCommon(mtBSkip, 0, false, 0)
		np = d.directRects(0xf, parts[:])
	} else {
		d.refListCheck()
		m := d.pSkipMV()
		d.storeMotion(0, rect{0, 0, 4, 4}, 0, m)
		d.finishMBCommon(mtPSkip, 0, false, 0)
		parts[0] = rect{0, 0, 4, 4}
		np = 1
	}
	for k := 0; k < np; k++ {
		d.mcRect(parts[k])
	}
}

func (d *Decoder) refListCheck() {}

// directRects lists the prediction rectangles of direct blocks in mask.
func (d *Decoder) directRects(mask uint8, out []rect) int {
	n := 0
	for b8 := 0; b8 < 4; b8++ {
		if mask&(1<<uint(b8)) == 0 {
			continue
		}
		bx, by := (b8&1)*2, (b8>>1)*2
		if d.hdr.sps.Direct8x8 {
			out[n] = rect{bx, by, 2, 2}
			n++
		} else {
			for k := 0; k < 4; k++ {
				out[n] = rect{bx + k&1, by + k>>1, 1, 1}
				n++
			}
		}
	}
	return n
}

func (d *Decoder) readQPDelta() (int, error) {
	var dq int
	if d.cabacOn {
		dq = d.cabacQPDelta()
	} else {
		dq = int(d.br.se())
	}
	if dq < -26 || dq > 25 {
		return 0, errCorrupt
	}
	d.lastDQ = dq != 0
	d.qp = (d.qp + dq + 52) % 52
	return dq, nil
}

// decodeMB decodes one non-skipped macroblock (macroblock_layer()).
func (d *Decoder) decodeMB(mbAddr int) error {
	d.startMB(mbAddr)
	h := d.hdr
	pps := h.pps
	idx := d.mbIdx
	br := d.br
	var raw int
	if d.cabacOn {
		switch {
		case h.isI():
			raw = d.cabacIntraMBType(true, 0)
		case h.isB():
			raw = d.cabacMBTypeB()
		default:
			raw = d.cabacMBTypeP()
		}
	} else {
		raw = int(br.ue())
	}
	intra := false
	itype := 0
	var it interMB
	switch {
	case h.isI():
		intra, itype = true, raw
	case h.isB():
		if raw >= 23 {
			intra, itype = true, raw-23
		} else {
			it = bMBTypes[raw]
		}
	default:
		if raw >= 5 {
			intra, itype = true, raw-5
		} else {
			it = pMBTypes[raw]
		}
	}
	if itype > 25 || raw < 0 {
		return corrupt()
	}
	for k := 0; k < 24; k++ {
		d.nz[idx*24+k] = 0
	}
	d.mbDC[idx] = 0
	d.nzMask[idx] = 0
	for i := range d.coef {
		d.coef[i] = 0
	}

	if intra && itype == 25 {
		return d.decodePCM()
	}

	var (
		cbp        int
		t8         bool
		chromaMode int
		i16Mode    int
		mbt        uint8
		parts      [16]rect
		np         int
		noSub8     = true
	)
	if intra {
		if itype == 0 {
			mbt = mtI4x4
			if pps.Transform8x8 {
				if d.cabacOn {
					t8 = d.cabacT8()
				} else {
					t8 = br.flag()
				}
			}
			d.mbT8[idx] = t8
			if err := d.parseIntraModes(t8); err != nil {
				return err
			}
		} else {
			mbt = mtI16
			i16Mode = (itype - 1) & 3
			cbp = ((itype-1)>>2)%3<<4 | btoi(itype >= 13)*15
			// record DC mode for neighbours' mode prediction
			for k := 0; k < 16; k++ {
				d.ipm[idx*16+k] = 2
			}
		}
		if d.cabacOn {
			chromaMode = d.cabacIntraChroma()
		} else {
			chromaMode = int(br.ue())
		}
		if chromaMode > 3 {
			return corrupt()
		}
		d.mbChroma[idx] = uint8(chromaMode)
	} else {
		mbt = mtInter
		if h.isB() && raw == 0 {
			mbt = mtBDirect
		}
		var err error
		np, noSub8, err = d.parseInter(&it, parts[:])
		if err != nil {
			return err
		}
	}
	if intra && itype == 0 || !intra {
		// coded_block_pattern
		if d.cabacOn {
			cbp = d.cabacCBP()
		} else {
			v := int(br.ue())
			if v > 47 {
				return corrupt()
			}
			if intra {
				cbp = int(golombToIntra4x4CBP[v])
			} else {
				cbp = int(golombToInterCBP[v])
			}
		}
		if !intra && cbp&15 != 0 && pps.Transform8x8 && noSub8 && (mbt != mtBDirect || h.sps.Direct8x8) {
			if d.cabacOn {
				t8 = d.cabacT8()
			} else {
				t8 = br.flag()
			}
			d.mbT8[idx] = t8
		}
	}
	d.mbCBP[idx] = uint8(cbp)
	d.mbType[idx] = mbt
	d.mbT8[idx] = t8
	d.mbDirect[idx] = d.curDirect
	// inter prediction first (needs only motion data)
	if !intra {
		for k := 0; k < np; k++ {
			d.mcRect(parts[k])
		}
	}
	if cbp > 0 || mbt == mtI16 {
		if _, err := d.readQPDelta(); err != nil {
			return err
		}
		d.mbQP[idx] = int8(d.qp)
		if err := d.parseResidual(intra, mbt == mtI16, cbp, t8); err != nil {
			return err
		}
	} else {
		d.lastDQ = false
		d.mbQP[idx] = int8(d.qp)
	}
	if d.mbChroma != nil {
		d.mbChroma[idx] = uint8(chromaMode)
	}
	if intra {
		d.reconIntra(mbt, i16Mode, chromaMode, cbp, t8)
	} else {
		d.addInterResidual(cbp, t8)
	}
	return nil
}

func (d *Decoder) decodePCM() error {
	idx := d.mbIdx
	pic := d.cur
	var src []byte
	if d.cabacOn {
		// after the terminating bin the stream continues byte aligned
		pos := (d.cab.pos + 7) &^ 7
		if (pos>>3)+384 > len(d.cab.data) {
			return corrupt()
		}
		src = d.cab.data[pos>>3 : pos>>3+384]
		d.cab.start(pos + 384*8)
	} else {
		d.br.byteAlign()
		p := d.br.pos >> 3
		if p+384 > len(d.br.b) {
			return corrupt()
		}
		src = d.br.b[p : p+384]
		d.br.pos += 384 * 8
	}
	for y := 0; y < 16; y++ {
		copy(pic.y[(d.mby*16+y)*pic.ys+d.mbx*16:], src[y*16:y*16+16])
	}
	for y := 0; y < 8; y++ {
		copy(pic.cb[(d.mby*8+y)*pic.cs+d.mbx*8:], src[256+y*8:256+y*8+8])
		copy(pic.cr[(d.mby*8+y)*pic.cs+d.mbx*8:], src[320+y*8:320+y*8+8])
	}
	for k := 0; k < 24; k++ {
		d.nz[idx*24+k] = 16
	}
	d.mbDC[idx] = 7
	d.nzMask[idx] = 0xffff
	d.mbType[idx] = mtPCM
	d.mbCBP[idx] = 0x2f
	d.mbT8[idx] = false
	d.mbChroma[idx] = 0
	d.mbDirect[idx] = 0
	for k := 0; k < 16; k++ {
		d.ipm[idx*16+k] = 2
	}
	d.mbQP[idx] = 0
	d.lastDQ = false
	return nil
}

// parseIntraModes reads the Intra4x4/8x8 prediction modes (7.3.5.1) into the
// per-block mode array d.ipm and d.curIPM.
func (d *Decoder) parseIntraModes(t8 bool) error {
	idx := d.mbIdx
	n := 16
	if t8 {
		n = 4
	}
	for k := 0; k < n; k++ {
		var bx, by int
		if t8 {
			bx, by = (k&1)*2, (k>>1)*2
		} else {
			bx, by = blkX[k], blkY[k]
		}
		var prev bool
		rem := 0
		if d.cabacOn {
			prev = d.cab.bin(68) != 0
			if !prev {
				rem = d.cab.bin(69)
				rem |= d.cab.bin(69) << 1
				rem |= d.cab.bin(69) << 2
			}
		} else {
			prev = d.br.flag()
			if !prev {
				rem = int(d.br.u(3))
			}
		}
		// predicted mode
		a := d.ipmAt(bx-1, by)
		b := d.ipmAt(bx, by-1)
		pm := 2
		if a >= 0 && b >= 0 {
			pm = min(a, b)
		}
		mode := pm
		if !prev {
			if rem < pm {
				mode = rem
			} else {
				mode = rem + 1
			}
		}
		if t8 {
			for q := 0; q < 4; q++ {
				d.ipm[idx*16+(by+q>>1)*4+bx+q&1] = int8(mode)
			}
		} else {
			d.ipm[idx*16+by*4+bx] = int8(mode)
		}
	}
	return nil
}

// ipmAt returns the intra mode of the block at macroblock-relative 4x4
// coordinates, -1 when unavailable for prediction (7.3.5.1 rules), and 2 for
// blocks of macroblocks that are not Intra NxN.
func (d *Decoder) ipmAt(bx, by int) int {
	idx := d.mbIdx
	if bx >= 0 && by >= 0 {
		return int(d.ipm[idx*16+by*4+bx])
	}
	var ni int
	switch {
	case bx < 0:
		if !d.availA {
			return -1
		}
		ni = idx - 1
		bx = 3
	default:
		if !d.availB {
			return -1
		}
		ni = idx - d.mbW
		by = 3
	}
	if d.hdr.pps.ConstrainedIntra && !isIntraMB(d.mbType[ni]) {
		return -1
	}
	if d.mbType[ni] != mtI4x4 {
		return 2
	}
	return int(d.ipm[ni*16+by*4+bx])
}

// parseInter parses the prediction syntax of an inter macroblock (mb_pred /
// sub_mb_pred) including motion vector derivation. It fills parts with the
// prediction rectangles.
func (d *Decoder) parseInter(it *interMB, parts []rect) (int, bool, error) {
	h := d.hdr
	noSub8 := true
	np := 0
	isB := h.isB()
	cab := d.cabacOn
	br := d.br
	var sub [4]subMB
	var subRaw [4]int
	type partSpec struct {
		r  rect
		pm uint8
		sh int
	}
	var specs [16]partSpec // partitions (non-8x8) or 8x8 blocks
	switch {
	case it.direct && !it.is8x8: // B_Direct_16x16
		d.curDirect = 0xf
		d.directMB(0xf)
		np = d.directRects(0xf, parts)
		return np, h.sps.Direct8x8, nil
	case it.is8x8:
		for b8 := 0; b8 < 4; b8++ {
			var t int
			if cab {
				if isB {
					t = d.cabacSubTypeB()
				} else {
					t = d.cabacSubTypeP()
				}
			} else {
				t = int(br.ue())
			}
			subRaw[b8] = t
			if isB {
				if t > 12 {
					return 0, false, corrupt()
				}
				sub[b8] = bSubTypes[t]
			} else {
				if t > 3 {
					return 0, false, corrupt()
				}
				sub[b8] = pSubTypes[t]
			}
			if sub[b8].direct {
				d.curDirect |= 1 << uint(b8)
				if !h.sps.Direct8x8 {
					noSub8 = false
				}
			} else if sub[b8].n > 1 {
				noSub8 = false
			}
		}
		if d.curDirect != 0 {
			d.directMB(d.curDirect)
		}
		for b8 := 0; b8 < 4; b8++ {
			specs[b8] = partSpec{rect{(b8 & 1) * 2, (b8 >> 1) * 2, 2, 2}, sub[b8].pm, 0}
		}
	default:
		for p := 0; p < it.n; p++ {
			var r rect
			sh := 0
			switch {
			case it.n == 1:
				r = rect{0, 0, 4, 4}
			case it.h == 2: // 16x8
				r = rect{0, p * 2, 4, 2}
				sh = 1 + p
			default: // 8x16
				r = rect{p * 2, 0, 2, 4}
				sh = 3 + p
			}
			specs[p] = partSpec{r, it.pm[p], sh}
		}
	}
	nspec := it.n
	if it.is8x8 {
		nspec = 4
	}
	// reference indices
	var refs [2][4]int
	for l := 0; l < 2; l++ {
		nref := h.numRef[l]
		for p := 0; p < nspec; p++ {
			sp := specs[p]
			use := sp.pm&(1<<uint(l)) != 0
			if it.is8x8 && sub[p].direct {
				use = false
			}
			if !use {
				continue
			}
			ref := 0
			if nref > 1 && !it.ref0 {
				if cab {
					ref = d.cabacRefIdx(l, sp.r.x, sp.r.y)
				} else {
					ref = int(br.te(uint32(nref - 1)))
				}
			}
			if ref < 0 || ref >= nref {
				return 0, false, corrupt()
			}
			refs[l][p] = ref
			d.storeRef(l, sp.r, ref)
		}
	}
	// motion vectors
	for l := 0; l < 2; l++ {
		for p := 0; p < nspec; p++ {
			sp := specs[p]
			use := sp.pm&(1<<uint(l)) != 0
			if it.is8x8 && sub[p].direct {
				continue
			}
			if !use {
				continue
			}
			ref := refs[l][p]
			if it.is8x8 {
				s := sub[p]
				for q := 0; q < s.n; q++ {
					r := subRect(sp.r, s, q)
					if err := d.readMVFor(l, ref, r, 0); err != nil {
						return 0, false, err
					}
				}
			} else {
				if err := d.readMVFor(l, ref, sp.r, sp.sh); err != nil {
					return 0, false, err
				}
			}
		}
	}
	// prediction rectangles
	if it.is8x8 {
		for b8 := 0; b8 < 4; b8++ {
			s := sub[b8]
			sp := specs[b8]
			if s.direct {
				if h.sps.Direct8x8 {
					parts[np] = sp.r
					np++
				} else {
					for k := 0; k < 4; k++ {
						parts[np] = rect{sp.r.x + k&1, sp.r.y + k>>1, 1, 1}
						np++
					}
				}
				continue
			}
			for q := 0; q < s.n; q++ {
				r := subRect(sp.r, s, q)
				parts[np] = r
				np++
			}
		}
	} else {
		for p := 0; p < nspec; p++ {
			parts[np] = specs[p].r
			np++
		}
	}
	return np, noSub8, nil
}

// readMVFor reads mvd for list l of rectangle r, predicts and stores the
// vector.
func (d *Decoder) readMVFor(l, ref int, r rect, shape int) error {
	var mx, my int
	if d.cabacOn {
		mx = d.cabacMVD(l, 0, r.x, r.y)
		my = d.cabacMVD(l, 1, r.x, r.y)
	} else {
		mx = int(d.br.se())
		my = int(d.br.se())
	}
	p := d.predMV(l, ref, r.x, r.y, r.w, shape)
	m := mv16{int16(int(p.x) + mx), int16(int(p.y) + my)}
	d.storeMotion(l, r, ref, m)
	if d.cabacOn {
		ax := min(abs(mx), 255)
		ay := min(abs(my), 255)
		d.storeMVD(l, r, [2]uint8{uint8(ax), uint8(ay)})
	}
	return nil
}

func subRect(sp rect, s subMB, q int) rect {
	switch {
	case s.w == 2 && s.h == 1:
		return rect{sp.x, sp.y + q, 2, 1}
	case s.w == 1 && s.h == 2:
		return rect{sp.x + q, sp.y, 1, 2}
	case s.w == 1 && s.h == 1:
		return rect{sp.x + q&1, sp.y + q>>1, 1, 1}
	}
	return sp
}
