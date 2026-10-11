package av1

// ---- motion vector prediction (spec 7.10) -----------------------------------

const (
	refCatLevel   = 640
	maxRefMvStack = 8
	invalidMv     = -32768
)

func hasNewmv(m int) bool {
	return m == modeNewMv || m == modeNewNew || m == modeNearNew || m == modeNewNear ||
		m == modeNearestNew || m == modeNewNearest
}

func (f *frameDec) lowerMvPrecision(mv *mvec) {
	fh := f.fh
	if fh.allowHighPrecMV {
		return
	}
	lower := func(v int16) int16 {
		x := int(v)
		if fh.forceIntMv {
			a := abs(x)
			aInt := (a + 3) >> 3
			if x > 0 {
				return int16(aInt << 3)
			}
			return int16(-(aInt << 3))
		}
		if x&1 != 0 {
			if x > 0 {
				x--
			} else {
				x++
			}
		}
		return int16(x)
	}
	mv.row = lower(mv.row)
	mv.col = lower(mv.col)
}

func (f *frameDec) setupGlobalMv(refList int) mvec {
	fh := f.fh
	ref := f.refFrame[refList]
	var mv mvec
	typ := gmIdentity
	if ref != intraFrame {
		typ = fh.gmType[ref]
	}
	if ref == intraFrame || typ == gmIdentity {
		mv = mvec{}
	} else if typ == gmTranslation {
		mv.row = int16(fh.gmParams[ref][0] >> (warpedModelPrec - 3))
		mv.col = int16(fh.gmParams[ref][1] >> (warpedModelPrec - 3))
	} else {
		bw := blockWidth(f.miSize)
		bh := blockHeight(f.miSize)
		x := f.miCol*4 + bw/2 - 1
		y := f.miRow*4 + bh/2 - 1
		gm := &fh.gmParams[ref]
		xc := (gm[2]-(1<<warpedModelPrec))*x + gm[3]*y + gm[0]
		yc := gm[4]*x + (gm[5]-(1<<warpedModelPrec))*y + gm[1]
		if fh.allowHighPrecMV {
			mv.row = int16(round2Signed(yc, warpedModelPrec-3))
			mv.col = int16(round2Signed(xc, warpedModelPrec-3))
		} else {
			mv.row = int16(round2Signed(yc, warpedModelPrec-2) * 2)
			mv.col = int16(round2Signed(xc, warpedModelPrec-2) * 2)
		}
	}
	f.lowerMvPrecision(&mv)
	return mv
}

func (f *frameDec) findMvStack(isCompound bool) {
	bw4, bh4 := f.bw4, f.bh4
	f.numMvFound = 0
	f.newMvCount = 0
	f.globalMvs[0] = f.setupGlobalMv(0)
	if isCompound {
		f.globalMvs[1] = f.setupGlobalMv(1)
	}
	f.foundMatch = false
	f.scanRow(-1, isCompound)
	foundAboveMatch := f.foundMatch
	f.foundMatch = false
	f.scanCol(-1, isCompound)
	foundLeftMatch := f.foundMatch
	f.foundMatch = false
	if imax(bw4, bh4) <= 16 {
		f.scanPoint(-1, bw4, isCompound)
	}
	if f.foundMatch {
		foundAboveMatch = true
	}
	f.closeMatches = b2i(foundAboveMatch) + b2i(foundLeftMatch)
	numNearest := f.numMvFound
	numNew := f.newMvCount
	for i := 0; i < numNearest; i++ {
		f.weightStack[i] += refCatLevel
	}
	f.zeroMvContext = 0
	if f.fh.useRefFrameMvs {
		f.temporalScan(isCompound)
	}
	f.scanPoint(-1, -1, isCompound)
	if f.foundMatch {
		foundAboveMatch = true
	}
	f.foundMatch = false
	f.scanRow(-3, isCompound)
	if f.foundMatch {
		foundAboveMatch = true
	}
	f.foundMatch = false
	f.scanCol(-3, isCompound)
	if f.foundMatch {
		foundLeftMatch = true
	}
	f.foundMatch = false
	if bh4 > 1 {
		f.scanRow(-5, isCompound)
	}
	if f.foundMatch {
		foundAboveMatch = true
	}
	f.foundMatch = false
	if bw4 > 1 {
		f.scanCol(-5, isCompound)
	}
	if f.foundMatch {
		foundLeftMatch = true
	}
	f.totalMatches = b2i(foundAboveMatch) + b2i(foundLeftMatch)
	f.sortStack(0, numNearest, isCompound)
	f.sortStack(numNearest, f.numMvFound, isCompound)
	if f.numMvFound < 2 {
		f.extraSearch(isCompound)
	}
	f.contextAndClamping(isCompound, numNew)
}

func (f *frameDec) scanRow(deltaRow int, isCompound bool) {
	bw4 := f.bw4
	end4 := imin(imin(bw4, f.miCols-f.miCol), 16)
	deltaCol := 0
	useStep16 := bw4 >= 16
	if abs(deltaRow) > 1 {
		deltaRow += f.miRow & 1
		deltaCol = 1 - (f.miCol & 1)
	}
	i := 0
	for i < end4 {
		mvRow := f.miRow + deltaRow
		mvCol := f.miCol + deltaCol + i
		if !f.isInside(mvRow, mvCol) {
			break
		}
		l := imin(bw4, num4x4BlocksWide[f.miAt(mvRow, mvCol).miSize])
		if abs(deltaRow) > 1 {
			l = imax(2, l)
		}
		if useStep16 {
			l = imax(4, l)
		}
		f.addRefMvCandidate(mvRow, mvCol, isCompound, l*2)
		i += l
	}
}

func (f *frameDec) scanCol(deltaCol int, isCompound bool) {
	bh4 := f.bh4
	end4 := imin(imin(bh4, f.miRows-f.miRow), 16)
	deltaRow := 0
	useStep16 := bh4 >= 16
	if abs(deltaCol) > 1 {
		deltaRow = 1 - (f.miRow & 1)
		deltaCol += f.miCol & 1
	}
	i := 0
	for i < end4 {
		mvRow := f.miRow + deltaRow + i
		mvCol := f.miCol + deltaCol
		if !f.isInside(mvRow, mvCol) {
			break
		}
		l := imin(bh4, num4x4BlocksHigh[f.miAt(mvRow, mvCol).miSize])
		if abs(deltaCol) > 1 {
			l = imax(2, l)
		}
		if useStep16 {
			l = imax(4, l)
		}
		f.addRefMvCandidate(mvRow, mvCol, isCompound, l*2)
		i += l
	}
}

func (f *frameDec) scanPoint(deltaRow, deltaCol int, isCompound bool) {
	mvRow := f.miRow + deltaRow
	mvCol := f.miCol + deltaCol
	if f.isInside(mvRow, mvCol) && f.miAt(mvRow, mvCol).refFrame[0] != -2 {
		f.addRefMvCandidate(mvRow, mvCol, isCompound, 4)
	}
}

func (f *frameDec) temporalScan(isCompound bool) {
	cov(covRefMvTemporal)
	bw4, bh4 := f.bw4, f.bh4
	stepW4 := 2
	if bw4 >= 16 {
		stepW4 = 4
	}
	stepH4 := 2
	if bh4 >= 16 {
		stepH4 = 4
	}
	for dr := 0; dr < imin(bh4, 16); dr += stepH4 {
		for dc := 0; dc < imin(bw4, 16); dc += stepW4 {
			f.addTplRefMv(dr, dc, isCompound)
		}
	}
	allowExt := bh4 >= 2 && bh4 < 16 && bw4 >= 2 && bw4 < 16
	if allowExt {
		pos := [3][2]int{{bh4, -2}, {bh4, bw4}, {bh4 - 2, bw4}}
		for i := 0; i < 3; i++ {
			dr, dc := pos[i][0], pos[i][1]
			row := (f.miRow & 15) + dr
			col := (f.miCol & 15) + dc
			if row >= 0 && row < 16 && col >= 0 && col < 16 {
				f.addTplRefMv(dr, dc, isCompound)
			}
		}
	}
}

func (f *frameDec) addTplRefMv(deltaRow, deltaCol int, isCompound bool) {
	mvRow := (f.miRow + deltaRow) | 1
	mvCol := (f.miCol + deltaCol) | 1
	if !f.isInside(mvRow, mvCol) {
		return
	}
	x8 := mvCol >> 1
	y8 := mvRow >> 1
	w8 := f.miCols >> 1
	if deltaRow == 0 && deltaCol == 0 {
		f.zeroMvContext = 1
	}
	if !isCompound {
		candMv := f.motionFieldMvs[f.refFrame[0]][y8*w8+x8]
		if candMv.row == invalidMv {
			return
		}
		f.lowerMvPrecision(&candMv)
		if deltaRow == 0 && deltaCol == 0 {
			if abs(int(candMv.row)-int(f.globalMvs[0].row)) >= 16 || abs(int(candMv.col)-int(f.globalMvs[0].col)) >= 16 {
				f.zeroMvContext = 1
			} else {
				f.zeroMvContext = 0
			}
		}
		idx := 0
		for ; idx < f.numMvFound; idx++ {
			if candMv == f.refStackMv[idx][0] {
				break
			}
		}
		if idx < f.numMvFound {
			f.weightStack[idx] += 2
		} else if f.numMvFound < maxRefMvStack {
			f.refStackMv[f.numMvFound][0] = candMv
			f.weightStack[f.numMvFound] = 2
			f.numMvFound++
		}
	} else {
		candMv0 := f.motionFieldMvs[f.refFrame[0]][y8*w8+x8]
		if candMv0.row == invalidMv {
			return
		}
		candMv1 := f.motionFieldMvs[f.refFrame[1]][y8*w8+x8]
		if candMv1.row == invalidMv {
			return
		}
		f.lowerMvPrecision(&candMv0)
		f.lowerMvPrecision(&candMv1)
		if deltaRow == 0 && deltaCol == 0 {
			if abs(int(candMv0.row)-int(f.globalMvs[0].row)) >= 16 || abs(int(candMv0.col)-int(f.globalMvs[0].col)) >= 16 ||
				abs(int(candMv1.row)-int(f.globalMvs[1].row)) >= 16 || abs(int(candMv1.col)-int(f.globalMvs[1].col)) >= 16 {
				f.zeroMvContext = 1
			} else {
				f.zeroMvContext = 0
			}
		}
		idx := 0
		for ; idx < f.numMvFound; idx++ {
			if candMv0 == f.refStackMv[idx][0] && candMv1 == f.refStackMv[idx][1] {
				break
			}
		}
		if idx < f.numMvFound {
			f.weightStack[idx] += 2
		} else if f.numMvFound < maxRefMvStack {
			f.refStackMv[f.numMvFound][0] = candMv0
			f.refStackMv[f.numMvFound][1] = candMv1
			f.weightStack[f.numMvFound] = 2
			f.numMvFound++
		}
	}
}

func (f *frameDec) addRefMvCandidate(mvRow, mvCol int, isCompound bool, weight int) {
	m := f.miAt(mvRow, mvCol)
	if !m.isInter {
		return
	}
	if !isCompound {
		for candList := 0; candList < 2; candList++ {
			if int(m.refFrame[candList]) == f.refFrame[0] {
				f.searchStack(m, candList, weight)
			}
		}
	} else if int(m.refFrame[0]) == f.refFrame[0] && int(m.refFrame[1]) == f.refFrame[1] {
		f.compoundSearchStack(m, weight)
	}
}

func (f *frameDec) searchStack(m *miInfo, candList, weight int) {
	candMode := int(m.yMode)
	candSize := int(m.miSize)
	large := imin(blockWidth(candSize), blockHeight(candSize)) >= 8
	var candMv mvec
	if (candMode == modeGlobalMv || candMode == modeGlobalGlobal) && f.fh.gmType[f.refFrame[0]] > gmTranslation && large {
		candMv = f.globalMvs[0]
	} else {
		candMv = m.mv[candList]
	}
	f.lowerMvPrecision(&candMv)
	if hasNewmv(candMode) {
		f.newMvCount++
	}
	f.foundMatch = true
	idx := 0
	for ; idx < f.numMvFound; idx++ {
		if candMv == f.refStackMv[idx][0] {
			break
		}
	}
	if idx < f.numMvFound {
		f.weightStack[idx] += weight
	} else if f.numMvFound < maxRefMvStack {
		f.refStackMv[f.numMvFound][0] = candMv
		f.weightStack[f.numMvFound] = weight
		f.numMvFound++
	}
}

func (f *frameDec) compoundSearchStack(m *miInfo, weight int) {
	candMvs := m.mv
	candMode := int(m.yMode)
	if candMode == modeGlobalGlobal {
		for refList := 0; refList < 2; refList++ {
			if f.fh.gmType[f.refFrame[refList]] > gmTranslation {
				candMvs[refList] = f.globalMvs[refList]
			}
		}
	}
	f.lowerMvPrecision(&candMvs[0])
	f.lowerMvPrecision(&candMvs[1])
	f.foundMatch = true
	idx := 0
	for ; idx < f.numMvFound; idx++ {
		if candMvs[0] == f.refStackMv[idx][0] && candMvs[1] == f.refStackMv[idx][1] {
			break
		}
	}
	if idx < f.numMvFound {
		f.weightStack[idx] += weight
	} else if f.numMvFound < maxRefMvStack {
		f.refStackMv[f.numMvFound] = candMvs
		f.weightStack[f.numMvFound] = weight
		f.numMvFound++
	}
	if hasNewmv(candMode) {
		f.newMvCount++
	}
}

func (f *frameDec) sortStack(start, end int, isCompound bool) {
	for end > start {
		newEnd := start
		for idx := start + 1; idx < end; idx++ {
			if f.weightStack[idx-1] < f.weightStack[idx] {
				f.weightStack[idx-1], f.weightStack[idx] = f.weightStack[idx], f.weightStack[idx-1]
				f.refStackMv[idx-1], f.refStackMv[idx] = f.refStackMv[idx], f.refStackMv[idx-1]
				newEnd = idx
			}
		}
		end = newEnd
	}
}

func (f *frameDec) extraSearch(isCompound bool) {
	var refIDCount, refDiffCount [2]int
	var refIDMvs, refDiffMvs [2][2]mvec
	w4 := imin(16, f.bw4)
	h4 := imin(16, f.bh4)
	w4 = imin(w4, f.miCols-f.miCol)
	h4 = imin(h4, f.miRows-f.miRow)
	num4x4 := imin(w4, h4)
	for pass := 0; pass < 2; pass++ {
		idx := 0
		for idx < num4x4 && f.numMvFound < 2 {
			var mvRow, mvCol int
			if pass == 0 {
				mvRow = f.miRow - 1
				mvCol = f.miCol + idx
			} else {
				mvRow = f.miRow + idx
				mvCol = f.miCol - 1
			}
			if !f.isInside(mvRow, mvCol) {
				break
			}
			m := f.miAt(mvRow, mvCol)
			if isCompound {
				for candList := 0; candList < 2; candList++ {
					candRef := int(m.refFrame[candList])
					if candRef > intraFrame {
						for list := 0; list < 2; list++ {
							candMv := m.mv[candList]
							if candRef == f.refFrame[list] && refIDCount[list] < 2 {
								refIDMvs[list][refIDCount[list]] = candMv
								refIDCount[list]++
							} else if refDiffCount[list] < 2 {
								if f.fh.refSignBias[candRef] != f.fh.refSignBias[f.refFrame[list]] {
									candMv.row = -candMv.row
									candMv.col = -candMv.col
								}
								refDiffMvs[list][refDiffCount[list]] = candMv
								refDiffCount[list]++
							}
						}
					}
				}
			} else {
				for candList := 0; candList < 2; candList++ {
					candRef := int(m.refFrame[candList])
					if candRef > intraFrame {
						candMv := m.mv[candList]
						if f.fh.refSignBias[candRef] != f.fh.refSignBias[f.refFrame[0]] {
							candMv.row = -candMv.row
							candMv.col = -candMv.col
						}
						i := 0
						for ; i < f.numMvFound; i++ {
							if candMv == f.refStackMv[i][0] {
								break
							}
						}
						if i == f.numMvFound {
							f.refStackMv[i][0] = candMv
							f.weightStack[i] = 2
							f.numMvFound++
						}
					}
				}
			}
			if pass == 0 {
				idx += num4x4BlocksWide[m.miSize]
			} else {
				idx += num4x4BlocksHigh[m.miSize]
			}
		}
	}
	if isCompound {
		var combined [2][2]mvec
		for list := 0; list < 2; list++ {
			compCount := 0
			for idx := 0; idx < refIDCount[list]; idx++ {
				combined[compCount][list] = refIDMvs[list][idx]
				compCount++
			}
			for idx := 0; idx < refDiffCount[list] && compCount < 2; idx++ {
				combined[compCount][list] = refDiffMvs[list][idx]
				compCount++
			}
			for compCount < 2 {
				combined[compCount][list] = f.globalMvs[list]
				compCount++
			}
		}
		if f.numMvFound == 1 {
			if combined[0][0] == f.refStackMv[0][0] && combined[0][1] == f.refStackMv[0][1] {
				f.refStackMv[f.numMvFound] = combined[1]
			} else {
				f.refStackMv[f.numMvFound] = combined[0]
			}
			f.weightStack[f.numMvFound] = 2
			f.numMvFound++
		} else {
			for idx := 0; idx < 2; idx++ {
				f.refStackMv[f.numMvFound] = combined[idx]
				f.weightStack[f.numMvFound] = 2
				f.numMvFound++
			}
		}
	} else {
		for idx := f.numMvFound; idx < 2; idx++ {
			f.refStackMv[idx][0] = f.globalMvs[0]
		}
	}
}

func (f *frameDec) clampMvRow(mvec, border int) int {
	mbToTopEdge := -((f.miRow * 4) * 8)
	mbToBottomEdge := ((f.miRows - f.bh4 - f.miRow) * 4) * 8
	return clip3(mbToTopEdge-border, mbToBottomEdge+border, mvec)
}

func (f *frameDec) clampMvCol(mvec, border int) int {
	mbToLeftEdge := -((f.miCol * 4) * 8)
	mbToRightEdge := ((f.miCols - f.bw4 - f.miCol) * 4) * 8
	return clip3(mbToLeftEdge-border, mbToRightEdge+border, mvec)
}

func (f *frameDec) contextAndClamping(isCompound bool, numNew int) {
	bw := blockWidth(f.miSize)
	bh := blockHeight(f.miSize)
	numLists := 1
	if isCompound {
		numLists = 2
	}
	for idx := 0; idx < f.numMvFound; idx++ {
		z := 0
		if idx+1 < f.numMvFound {
			w0 := f.weightStack[idx]
			w1 := f.weightStack[idx+1]
			if w0 >= refCatLevel {
				if w1 < refCatLevel {
					z = 1
				}
			} else {
				z = 2
			}
		}
		f.drlCtxStack[idx] = z
	}
	for list := 0; list < numLists; list++ {
		for idx := 0; idx < f.numMvFound; idx++ {
			mv := &f.refStackMv[idx][list]
			mv.row = int16(f.clampMvRow(int(mv.row), 128+bh*8))
			mv.col = int16(f.clampMvCol(int(mv.col), 128+bw*8))
		}
	}
	if f.closeMatches == 0 {
		f.newMvContext = imin(f.totalMatches, 1)
		f.refMvContext = f.totalMatches
	} else if f.closeMatches == 1 {
		f.newMvContext = 3 - imin(numNew, 1)
		f.refMvContext = 2 + f.totalMatches
	} else {
		f.newMvContext = 5 - imin(numNew, 1)
		f.refMvContext = 5
	}
}

func (f *frameDec) hasOverlappableCandidates() bool {
	if f.availU {
		w4 := f.bw4
		for x4 := f.miCol; x4 < imin(f.miCols, f.miCol+w4); x4 += 2 {
			c := x4 | 1
			if c >= f.miCols {
				c = f.miCols - 1
			}
			if f.miAt(f.miRow-1, c).refFrame[0] > intraFrame {
				return true
			}
		}
	}
	if f.availL {
		h4 := f.bh4
		for y4 := f.miRow; y4 < imin(f.miRows, f.miRow+h4); y4 += 2 {
			r := y4 | 1
			if r >= f.miRows {
				r = f.miRows - 1
			}
			if f.miAt(r, f.miCol-1).refFrame[0] > intraFrame {
				return true
			}
		}
	}
	return false
}

func (f *frameDec) findWarpSamples() {
	f.numSamples = 0
	f.numSamplesScanned = 0
	w4, h4 := f.bw4, f.bh4
	doTopLeft, doTopRight := true, true
	if f.availU {
		srcSize := int(f.miAt(f.miRow-1, f.miCol).miSize)
		srcW := num4x4BlocksWide[srcSize]
		if w4 <= srcW {
			colOffset := -(f.miCol & (srcW - 1))
			if colOffset < 0 {
				doTopLeft = false
			}
			if colOffset+srcW > w4 {
				doTopRight = false
			}
			f.addSample(-1, 0)
		} else {
			miStep := 0
			for i := 0; i < imin(w4, f.miCols-f.miCol); i += miStep {
				srcSize = int(f.miAt(f.miRow-1, f.miCol+i).miSize)
				srcW = num4x4BlocksWide[srcSize]
				miStep = imin(w4, srcW)
				f.addSample(-1, i)
			}
		}
	}
	if f.availL {
		srcSize := int(f.miAt(f.miRow, f.miCol-1).miSize)
		srcH := num4x4BlocksHigh[srcSize]
		if h4 <= srcH {
			rowOffset := -(f.miRow & (srcH - 1))
			if rowOffset < 0 {
				doTopLeft = false
			}
			f.addSample(0, -1)
		} else {
			miStep := 0
			for i := 0; i < imin(h4, f.miRows-f.miRow); i += miStep {
				srcSize = int(f.miAt(f.miRow+i, f.miCol-1).miSize)
				srcH = num4x4BlocksHigh[srcSize]
				miStep = imin(h4, srcH)
				f.addSample(i, -1)
			}
		}
	}
	if doTopLeft {
		f.addSample(-1, -1)
	}
	if doTopRight {
		if imax(w4, h4) <= 16 {
			f.addSample(-1, w4)
		}
	}
	if f.numSamples == 0 && f.numSamplesScanned > 0 {
		f.numSamples = 1
	}
}

func (f *frameDec) addSample(deltaRow, deltaCol int) {
	if f.numSamplesScanned >= 8 {
		return
	}
	mvRow := f.miRow + deltaRow
	mvCol := f.miCol + deltaCol
	if !f.isInside(mvRow, mvCol) {
		return
	}
	m := f.miAt(mvRow, mvCol)
	if m.refFrame[0] == -2 {
		return
	}
	if int(m.refFrame[0]) != f.refFrame[0] {
		return
	}
	if m.refFrame[1] != refNone {
		return
	}
	candSz := int(m.miSize)
	candW4 := num4x4BlocksWide[candSz]
	candH4 := num4x4BlocksHigh[candSz]
	candRow := mvRow &^ (candH4 - 1)
	candCol := mvCol &^ (candW4 - 1)
	midY := candRow*4 + candH4*2 - 1
	midX := candCol*4 + candW4*2 - 1
	threshold := clip3(16, 112, imax(blockWidth(f.miSize), blockHeight(f.miSize)))
	cm := f.miAt(candRow, candCol)
	mvDiffRow := abs(int(cm.mv[0].row) - int(f.mv[0].row))
	mvDiffCol := abs(int(cm.mv[0].col) - int(f.mv[0].col))
	valid := mvDiffRow+mvDiffCol <= threshold
	f.numSamplesScanned++
	if !valid && f.numSamplesScanned > 1 {
		return
	}
	f.candList[f.numSamples] = [4]int{midY * 8, midX * 8, midY*8 + int(cm.mv[0].row), midX*8 + int(cm.mv[0].col)}
	if valid {
		f.numSamples++
	}
}
