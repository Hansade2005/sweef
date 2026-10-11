package vp9

func (d *Decoder) interBlockModeInfo() {
	b := &d.blk
	d.readRefFrames()
	for j := 0; j < 2; j++ {
		if b.refFrame[j] > intraFrame {
			d.findMvRefs(b.refFrame[j], -1)
			d.findBestRefMvs(j)
		}
	}
	isCompound := b.refFrame[1] > intraFrame
	if d.segFeatureActive(segLvlSkip) {
		b.yMode = zeroMv
	} else if b.miSize >= block8x8 {
		ctx := b.modeContext[b.refFrame[0]]
		v := d.bd.tree(interModeTree, d.p.interMode[ctx][:])
		d.cnt.interMode[ctx][v]++
		b.yMode = nearestMv + v
	}
	if d.interpFilter == switchable {
		leftInterp, aboveInterp := 3, 3
		if b.availL && b.leftRef[0] > intraFrame {
			leftInterp = int(d.mi[b.miRow*d.miCols+b.miCol-1].filter)
		}
		if b.availU && b.aboveRef[0] > intraFrame {
			aboveInterp = int(d.mi[(b.miRow-1)*d.miCols+b.miCol].filter)
		}
		var ctx int
		switch {
		case leftInterp == aboveInterp:
			ctx = leftInterp
		case leftInterp == 3 && aboveInterp != 3:
			ctx = aboveInterp
		case leftInterp != 3 && aboveInterp == 3:
			ctx = leftInterp
		default:
			ctx = 3
		}
		v := d.bd.tree(interpFilterTree, d.p.interpFilter[ctx][:])
		d.cnt.interpFilter[ctx][v]++
		b.interpFilter = v
	} else {
		b.interpFilter = d.interpFilter
	}
	nref := 1
	if isCompound {
		nref = 2
	}
	if b.miSize < block8x8 {
		n4w := int(num4x4Wide[b.miSize])
		n4h := int(num4x4High[b.miSize])
		for idy := 0; idy < 2; idy += n4h {
			for idx := 0; idx < 2; idx += n4w {
				ctx := b.modeContext[b.refFrame[0]]
				v := d.bd.tree(interModeTree, d.p.interMode[ctx][:])
				d.cnt.interMode[ctx][v]++
				b.yMode = nearestMv + v
				if b.yMode == nearestMv || b.yMode == nearMv {
					for j := 0; j < nref; j++ {
						d.appendSub8x8Mvs(idy*2+idx, j)
					}
				}
				mvs := d.assignMv(isCompound)
				for y2 := 0; y2 < n4h; y2++ {
					for x2 := 0; x2 < n4w; x2++ {
						blk := (idy+y2)*2 + idx + x2
						for l := 0; l < nref; l++ {
							b.blockMvs[l][blk] = mvs[l]
						}
					}
				}
			}
		}
	} else {
		mvs := d.assignMv(isCompound)
		for l := 0; l < nref; l++ {
			for blk := 0; blk < 4; blk++ {
				b.blockMvs[l][blk] = mvs[l]
			}
		}
	}
}

func (d *Decoder) readRefFrames() {
	b := &d.blk
	if d.segFeatureActive(segLvlRefFrame) {
		b.refFrame = [2]int{d.seg.featData[b.segID][segLvlRefFrame], 0}
		return
	}
	compMode := d.referenceMode
	if d.referenceMode == referenceSelect {
		ctx := d.compModeCtx()
		v := d.bd.read(d.p.compMode[ctx])
		d.cnt.compMode[ctx][v]++
		compMode = v
	}
	if compMode == compoundRef {
		idx := btoi(d.signBias[d.compFixedRef])
		ctx := d.compRefCtx()
		v := d.bd.read(d.p.compRef[ctx])
		d.cnt.compRef[ctx][v]++
		b.refFrame[idx] = d.compFixedRef
		b.refFrame[1-idx] = d.compVarRef[v]
		return
	}
	ctx := d.singleRefP1Ctx()
	p1 := d.bd.read(d.p.singleRef[ctx][0])
	d.cnt.singleRef[ctx][0][p1]++
	if p1 == 1 {
		ctx2 := d.singleRefP2Ctx()
		p2 := d.bd.read(d.p.singleRef[ctx2][1])
		d.cnt.singleRef[ctx2][1][p2]++
		if p2 == 1 {
			b.refFrame[0] = altrefFrame
		} else {
			b.refFrame[0] = goldenFrame
		}
	} else {
		b.refFrame[0] = lastFrame
	}
	b.refFrame[1] = 0
}

func (d *Decoder) compModeCtx() int {
	b := &d.blk
	fix := d.compFixedRef
	switch {
	case b.availU && b.availL:
		if b.aboveSingle && b.leftSingle {
			return btoi(b.aboveRef[0] == fix) ^ btoi(b.leftRef[0] == fix)
		} else if b.aboveSingle {
			return 2 + btoi(b.aboveRef[0] == fix || b.aboveIntra)
		} else if b.leftSingle {
			return 2 + btoi(b.leftRef[0] == fix || b.leftIntra)
		}
		return 4
	case b.availU:
		if b.aboveSingle {
			return btoi(b.aboveRef[0] == fix)
		}
		return 3
	case b.availL:
		if b.leftSingle {
			return btoi(b.leftRef[0] == fix)
		}
		return 3
	}
	return 1
}

func (d *Decoder) compRefCtx() int {
	b := &d.blk
	fixIdx := btoi(d.signBias[d.compFixedRef])
	varIdx := 1 - fixIdx
	v1 := d.compVarRef[1]
	v0 := d.compVarRef[0]
	fix := d.compFixedRef
	switch {
	case b.availU && b.availL:
		switch {
		case b.aboveIntra && b.leftIntra:
			return 2
		case b.leftIntra:
			if b.aboveSingle {
				return 1 + 2*btoi(b.aboveRef[0] != v1)
			}
			return 1 + 2*btoi(b.aboveRef[varIdx] != v1)
		case b.aboveIntra:
			if b.leftSingle {
				return 1 + 2*btoi(b.leftRef[0] != v1)
			}
			return 1 + 2*btoi(b.leftRef[varIdx] != v1)
		}
		vrfa := b.aboveRef[varIdx]
		if b.aboveSingle {
			vrfa = b.aboveRef[0]
		}
		vrfl := b.leftRef[varIdx]
		if b.leftSingle {
			vrfl = b.leftRef[0]
		}
		switch {
		case vrfa == vrfl && v1 == vrfa:
			return 0
		case b.leftSingle && b.aboveSingle:
			if (vrfa == fix && vrfl == v0) || (vrfl == fix && vrfa == v0) {
				return 4
			} else if vrfa == vrfl {
				return 3
			}
			return 1
		case b.leftSingle || b.aboveSingle:
			vrfc := vrfl
			if b.leftSingle {
				vrfc = vrfa
			}
			rfs := vrfl
			if b.aboveSingle {
				rfs = vrfa
			}
			if vrfc == v1 && rfs != v1 {
				return 1
			} else if rfs == v1 && vrfc != v1 {
				return 2
			}
			return 4
		case vrfa == vrfl:
			return 4
		}
		return 2
	case b.availU:
		if b.aboveIntra {
			return 2
		}
		if b.aboveSingle {
			return 3 * btoi(b.aboveRef[0] != v1)
		}
		return 4 * btoi(b.aboveRef[varIdx] != v1)
	case b.availL:
		if b.leftIntra {
			return 2
		}
		if b.leftSingle {
			return 3 * btoi(b.leftRef[0] != v1)
		}
		return 4 * btoi(b.leftRef[varIdx] != v1)
	}
	return 2
}

func (d *Decoder) singleRefP1Ctx() int {
	b := &d.blk
	a0, a1 := b.aboveRef[0], b.aboveRef[1]
	l0, l1 := b.leftRef[0], b.leftRef[1]
	switch {
	case b.availU && b.availL:
		switch {
		case b.aboveIntra && b.leftIntra:
			return 2
		case b.leftIntra:
			if b.aboveSingle {
				return 4 * btoi(a0 == lastFrame)
			}
			return 1 + btoi(a0 == lastFrame || a1 == lastFrame)
		case b.aboveIntra:
			if b.leftSingle {
				return 4 * btoi(l0 == lastFrame)
			}
			return 1 + btoi(l0 == lastFrame || l1 == lastFrame)
		}
		if b.aboveSingle && b.leftSingle {
			return 2*btoi(a0 == lastFrame) + 2*btoi(l0 == lastFrame)
		} else if !b.aboveSingle && !b.leftSingle {
			return 1 + btoi(a0 == lastFrame || a1 == lastFrame || l0 == lastFrame || l1 == lastFrame)
		}
		rfs, crf1, crf2 := l0, a0, a1
		if b.aboveSingle {
			rfs, crf1, crf2 = a0, l0, l1
		}
		if rfs == lastFrame {
			return 3 + btoi(crf1 == lastFrame || crf2 == lastFrame)
		}
		return btoi(crf1 == lastFrame || crf2 == lastFrame)
	case b.availU:
		if b.aboveIntra {
			return 2
		}
		if b.aboveSingle {
			return 4 * btoi(a0 == lastFrame)
		}
		return 1 + btoi(a0 == lastFrame || a1 == lastFrame)
	case b.availL:
		if b.leftIntra {
			return 2
		}
		if b.leftSingle {
			return 4 * btoi(l0 == lastFrame)
		}
		return 1 + btoi(l0 == lastFrame || l1 == lastFrame)
	}
	return 2
}

func (d *Decoder) singleRefP2Ctx() int {
	b := &d.blk
	a0, a1 := b.aboveRef[0], b.aboveRef[1]
	l0, l1 := b.leftRef[0], b.leftRef[1]
	switch {
	case b.availU && b.availL:
		switch {
		case b.aboveIntra && b.leftIntra:
			return 2
		case b.leftIntra:
			if b.aboveSingle {
				if a0 == lastFrame {
					return 3
				}
				return 4 * btoi(a0 == goldenFrame)
			}
			return 1 + 2*btoi(a0 == goldenFrame || a1 == goldenFrame)
		case b.aboveIntra:
			if b.leftSingle {
				if l0 == lastFrame {
					return 3
				}
				return 4 * btoi(l0 == goldenFrame)
			}
			return 1 + 2*btoi(l0 == goldenFrame || l1 == goldenFrame)
		}
		if b.aboveSingle && b.leftSingle {
			switch {
			case a0 == lastFrame && l0 == lastFrame:
				return 3
			case a0 == lastFrame:
				return 4 * btoi(l0 == goldenFrame)
			case l0 == lastFrame:
				return 4 * btoi(a0 == goldenFrame)
			}
			return 2*btoi(a0 == goldenFrame) + 2*btoi(l0 == goldenFrame)
		} else if !b.aboveSingle && !b.leftSingle {
			if a0 == l0 && a1 == l1 {
				return 3 * btoi(a0 == goldenFrame || a1 == goldenFrame)
			}
			return 2
		}
		rfs, crf1, crf2 := l0, a0, a1
		if b.aboveSingle {
			rfs, crf1, crf2 = a0, l0, l1
		}
		switch rfs {
		case goldenFrame:
			return 3 + btoi(crf1 == goldenFrame || crf2 == goldenFrame)
		case altrefFrame:
			return btoi(crf1 == goldenFrame || crf2 == goldenFrame)
		}
		return 1 + 2*btoi(crf1 == goldenFrame || crf2 == goldenFrame)
	case b.availU:
		if b.aboveIntra || (a0 == lastFrame && b.aboveSingle) {
			return 2
		}
		if b.aboveSingle {
			return 4 * btoi(a0 == goldenFrame)
		}
		return 3 * btoi(a0 == goldenFrame || a1 == goldenFrame)
	case b.availL:
		if b.leftIntra || (l0 == lastFrame && b.leftSingle) {
			return 2
		}
		if b.leftSingle {
			return 4 * btoi(l0 == goldenFrame)
		}
		return 3 * btoi(l0 == goldenFrame || l1 == goldenFrame)
	}
	return 2
}

// ---- motion vectors -------------------------------------------------------------------------

func (d *Decoder) assignMv(isCompound bool) [2]mv {
	b := &d.blk
	var out [2]mv
	n := 1
	if isCompound {
		n = 2
	}
	for i := 0; i < n; i++ {
		switch b.yMode {
		case newMv:
			out[i] = d.readMv(i)
		case nearestMv:
			out[i] = b.nearestMv[i]
		case nearMv:
			out[i] = b.nearMv[i]
		}
	}
	return out
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func useMvHp(m mv) bool {
	return absInt(int(m.row))>>3 < 8 && absInt(int(m.col))>>3 < 8
}

func (d *Decoder) readMv(ref int) mv {
	b := &d.blk
	useHp := d.allowHp && useMvHp(b.bestMv[ref])
	var diff [2]int
	j := d.bd.tree(mvJointTree, d.p.mvJoint[:])
	d.cnt.mvJoint[j]++
	if j == 2 || j == 3 { // HZVNZ or HNZVNZ: row changes
		diff[0] = d.readMvComponent(0, useHp)
	}
	if j == 1 || j == 3 {
		diff[1] = d.readMvComponent(1, useHp)
	}
	return mv{int16(int(b.bestMv[ref].row) + diff[0]), int16(int(b.bestMv[ref].col) + diff[1])}
}

func (d *Decoder) readMvComponent(comp int, useHp bool) int {
	sign := d.bd.read(d.p.mvSign[comp])
	d.cnt.mvSign[comp][sign]++
	class := d.bd.tree(mvClassTree, d.p.mvClass[comp][:])
	d.cnt.mvClass[comp][class]++
	var mag int
	if class == 0 {
		bit := d.bd.read(d.p.mvClass0Bit[comp])
		d.cnt.mvClass0Bit[comp][bit]++
		fr := d.bd.tree(mvFrTree, d.p.mvClass0Fr[comp][bit][:])
		d.cnt.mvClass0Fr[comp][bit][fr]++
		hp := 1
		if useHp {
			hp = d.bd.read(d.p.mvClass0Hp[comp])
		}
		d.cnt.mvClass0Hp[comp][hp]++
		mag = (bit<<3 | fr<<1 | hp) + 1
	} else {
		dd := 0
		for i := 0; i < class; i++ {
			bit := d.bd.read(d.p.mvBits[comp][i])
			d.cnt.mvBits[comp][i][bit]++
			dd |= bit << uint(i)
		}
		mag = 2 << uint(class+2)
		fr := d.bd.tree(mvFrTree, d.p.mvFr[comp][:])
		d.cnt.mvFr[comp][fr]++
		hp := 1
		if useHp {
			hp = d.bd.read(d.p.mvHp[comp])
		}
		d.cnt.mvHp[comp][hp]++
		mag += (dd<<3 | fr<<1 | hp) + 1
	}
	if sign == 1 {
		return -mag
	}
	return mag
}

func (d *Decoder) isInside(r, c int) bool {
	return r >= 0 && r < d.miRows && c >= d.miColStart && c < d.miColEnd
}

func (d *Decoder) addMvRefList(m mv) {
	b := &d.blk
	if b.refMvCount >= 2 {
		return
	}
	if b.refMvCount > 0 && m == b.refListMv[0] {
		return
	}
	b.refListMv[b.refMvCount] = m
	b.refMvCount++
}

func (d *Decoder) scaleMv(m mv, candFrame, refFrame int) mv {
	if d.signBias[candFrame] != d.signBias[refFrame] {
		return mv{-m.row, -m.col}
	}
	return m
}

func (d *Decoder) ifSameRefFrameAddMv(mi *modeInfo, refFrame int) {
	for j := 0; j < 2; j++ {
		if int(mi.refFrame[j]) == refFrame {
			d.addMvRefList(mi.mv[j])
			return
		}
	}
}

func (d *Decoder) ifDiffRefFrameAddMv(mi *modeInfo, refFrame int) {
	mvsSame := mi.mv[0] == mi.mv[1]
	f0, f1 := int(mi.refFrame[0]), int(mi.refFrame[1])
	if f0 > intraFrame && f0 != refFrame {
		d.addMvRefList(d.scaleMv(mi.mv[0], f0, refFrame))
	}
	if f1 > intraFrame && f1 != refFrame && !mvsSame {
		d.addMvRefList(d.scaleMv(mi.mv[1], f1, refFrame))
	}
}

func (d *Decoder) clampMvRow(v int, border int) int {
	b := &d.blk
	bh := int(num8x8High[b.miSize])
	top := -((b.miRow * 8) * 8)
	bottom := ((d.miRows - bh - b.miRow) * 8) * 8
	return clip3(top-border, bottom+border, v)
}

func (d *Decoder) clampMvCol(v int, border int) int {
	b := &d.blk
	bw := int(num8x8Wide[b.miSize])
	left := -((b.miCol * 8) * 8)
	right := ((d.miCols - bw - b.miCol) * 8) * 8
	return clip3(left-border, right+border, v)
}

func clip3(lo, hi, v int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func (d *Decoder) findMvRefs(refFrame, block int) {
	b := &d.blk
	b.refMvCount = 0
	b.refListMv = [2]mv{}
	differentRefFound := false
	contextCounter := 0
	search := &mvRefBlocks[b.miSize]
	for i := 0; i < 2; i++ {
		cr := b.miRow + int(search[i][0])
		cc := b.miCol + int(search[i][1])
		if d.isInside(cr, cc) {
			mi := &d.mi[cr*d.miCols+cc]
			differentRefFound = true
			contextCounter += int(mode2counter[mi.yMode])
			for j := 0; j < 2; j++ {
				if int(mi.refFrame[j]) == refFrame {
					idx := 3
					if block >= 0 {
						idx = int(idxNColumnToSubblock[block][btoi(search[i][1] == 0)])
					}
					d.addMvRefList(mi.subMvs[j][idx])
					break
				}
			}
		}
	}
	for i := 2; i < 8; i++ {
		cr := b.miRow + int(search[i][0])
		cc := b.miCol + int(search[i][1])
		if d.isInside(cr, cc) {
			differentRefFound = true
			d.ifSameRefFrameAddMv(&d.mi[cr*d.miCols+cc], refFrame)
		}
	}
	if d.usePrevMvs {
		d.ifSameRefFrameAddMv(&d.prevMi[b.miRow*d.miCols+b.miCol], refFrame)
	}
	if differentRefFound {
		for i := 0; i < 8; i++ {
			cr := b.miRow + int(search[i][0])
			cc := b.miCol + int(search[i][1])
			if d.isInside(cr, cc) {
				d.ifDiffRefFrameAddMv(&d.mi[cr*d.miCols+cc], refFrame)
			}
		}
	}
	if d.usePrevMvs {
		d.ifDiffRefFrameAddMv(&d.prevMi[b.miRow*d.miCols+b.miCol], refFrame)
	}
	b.modeContext[refFrame] = int(counterToContext[contextCounter])
	for i := 0; i < 2; i++ {
		b.refListMv[i].row = int16(d.clampMvRow(int(b.refListMv[i].row), 128))
		b.refListMv[i].col = int16(d.clampMvCol(int(b.refListMv[i].col), 128))
	}
}

func (d *Decoder) findBestRefMvs(refList int) {
	b := &d.blk
	for i := 0; i < 2; i++ {
		dr := int(b.refListMv[i].row)
		dc := int(b.refListMv[i].col)
		if !d.allowHp || !useMvHp(b.refListMv[i]) {
			if dr&1 != 0 {
				if dr > 0 {
					dr--
				} else {
					dr++
				}
			}
			if dc&1 != 0 {
				if dc > 0 {
					dc--
				} else {
					dc++
				}
			}
		}
		b.refListMv[i].row = int16(d.clampMvRow(dr, (160-4)<<3))
		b.refListMv[i].col = int16(d.clampMvCol(dc, (160-4)<<3))
	}
	b.nearestMv[refList] = b.refListMv[0]
	b.nearMv[refList] = b.refListMv[1]
	b.bestMv[refList] = b.refListMv[0]
}

func (d *Decoder) appendSub8x8Mvs(block, refList int) {
	b := &d.blk
	d.findMvRefs(b.refFrame[refList], block)
	var sub [2]mv
	dst := 0
	if block == 0 {
		sub[0], sub[1] = b.refListMv[0], b.refListMv[1]
		dst = 2
	} else if block <= 2 {
		sub[dst] = b.blockMvs[refList][0]
		dst++
	} else {
		sub[dst] = b.blockMvs[refList][2]
		dst++
		for idx := 1; idx >= 0 && dst < 2; idx-- {
			if b.blockMvs[refList][idx] != sub[0] {
				sub[dst] = b.blockMvs[refList][idx]
				dst++
			}
		}
	}
	for n := 0; n < 2 && dst < 2; n++ {
		if b.refListMv[n] != sub[0] {
			sub[dst] = b.refListMv[n]
			dst++
		}
	}
	if dst < 2 {
		sub[dst] = mv{}
	}
	b.nearestMv[refList] = sub[0]
	b.nearMv[refList] = sub[1]
}
