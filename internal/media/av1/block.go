package av1

// ---- block level (spec 5.11.5) ---------------------------------------------

func (f *frameDec) decodeBlock(r, c, subSize int) {
	fh := f.fh
	f.miRow, f.miCol, f.miSize = r, c, subSize
	bw4 := num4x4BlocksWide[subSize]
	bh4 := num4x4BlocksHigh[subSize]
	f.bw4, f.bh4 = bw4, bh4
	if bh4 == 1 && f.subY != 0 && (r&1) == 0 {
		f.hasChroma = false
	} else if bw4 == 1 && f.subX != 0 && (c&1) == 0 {
		f.hasChroma = false
	} else {
		f.hasChroma = f.numPlanes > 1
	}
	f.availU = f.isInside(r-1, c)
	f.availL = f.isInside(r, c-1)
	f.availUChroma = f.availU
	f.availLChroma = f.availL
	if f.hasChroma {
		if f.subY != 0 && bh4 == 1 {
			f.availUChroma = f.isInside(r-2, c)
		}
		if f.subX != 0 && bw4 == 1 {
			f.availLChroma = f.isInside(r, c-2)
		}
	} else {
		f.availUChroma = false
		f.availLChroma = false
	}
	// defaults for per-block state
	f.useFilterIntra = false
	f.interintra = false
	f.motionMode = motionSimple
	f.compoundType = compoundAverage
	f.compGroupIdx = 0
	f.compoundIdx = 0
	f.interpFilter = [2]int{}
	f.paletteSizeY, f.paletteSizeUV = 0, 0
	f.angleDeltaY, f.angleDeltaUV = 0, 0
	f.useIntrabc = false
	f.uvMode = dcPred
	f.mv = [2]mvec{}

	f.modeInfo()
	if f.paletteSizeY > 0 {
		cov(covPalette)
	}
	if f.paletteSizeUV > 0 {
		cov(covPaletteUV)
	}
	if f.useIntrabc {
		cov(covIntrabc)
	}
	if f.skipMode {
		cov(covSkipMode)
	}
	if f.lossless {
		cov(covLossless)
	}
	if f.useFilterIntra {
		cov(covFilterIntra)
	}
	f.paletteTokens()
	f.readBlockTxSize()
	if f.skip {
		f.resetBlockContext(bw4, bh4)
	}
	isCompound := f.refFrame[1] > intraFrame
	// store mode info (first batch)
	rowEnd := imin(r+bh4, f.miRows)
	colEnd := imin(c+bw4, f.miCols)
	var tmpl miInfo
	tmpl.yMode = uint8(f.yMode)
	tmpl.uvMode = uint8(f.uvMode)
	tmpl.refFrame = [2]int8{int8(f.refFrame[0]), int8(f.refFrame[1])}
	tmpl.isInter = f.isInter
	tmpl.skip = f.skip
	tmpl.skipMode = f.skipMode
	tmpl.segID = uint8(f.segmentID)
	tmpl.miSize = uint8(subSize)
	tmpl.paletteSize = [2]uint8{uint8(f.paletteSizeY), uint8(f.paletteSizeUV)}
	tmpl.compGroupIdx = uint8(f.compGroupIdx)
	tmpl.compoundIdx = uint8(f.compoundIdx)
	tmpl.interpFilter = [2]uint8{uint8(f.interpFilter[0]), uint8(f.interpFilter[1])}
	tmpl.motionMode = uint8(f.motionMode)
	tmpl.mv = f.mv
	if !isCompound {
		tmpl.mv[1] = mvec{}
	}
	for i := 0; i < frameLfCount; i++ {
		tmpl.deltaLF[i] = int8(f.deltaLF[i])
	}
	for y := r; y < rowEnd; y++ {
		row := f.mi[y*f.miCols+c : y*f.miCols+colEnd]
		for x := range row {
			inter := row[x].interTxSize
			row[x] = tmpl
			row[x].interTxSize = inter
		}
	}
	if f.paletteSizeY > 0 || f.paletteSizeUV > 0 {
		for y := r; y < rowEnd; y++ {
			for x := c; x < colEnd; x++ {
				idx := (y*f.miCols + x) * 8
				for i := 0; i < f.paletteSizeY; i++ {
					f.palColors[0][idx+i] = f.paletteColorsY[i]
				}
				for i := 0; i < f.paletteSizeUV; i++ {
					f.palColors[1][idx+i] = f.paletteColorsU[i]
				}
			}
		}
	}
	_ = fh
	f.computePrediction()
	f.residual()
}

func (f *frameDec) resetBlockContext(bw4, bh4 int) {
	n := 1
	if f.hasChroma {
		n = 3
	}
	for plane := 0; plane < n; plane++ {
		subX, subY := 0, 0
		if plane > 0 {
			subX, subY = f.subX, f.subY
		}
		for i := f.miCol >> uint(subX); i < ((f.miCol + bw4) >> uint(subX)); i++ {
			f.aboveLevelCtx[plane][i] = 0
			f.aboveDcCtx[plane][i] = 0
		}
		for i := f.miRow >> uint(subY); i < ((f.miRow + bh4) >> uint(subY)); i++ {
			f.leftLevelCtx[plane][i] = 0
			f.leftDcCtx[plane][i] = 0
		}
	}
}

func (f *frameDec) segFeatureActiveIdx(idx, feature int) bool {
	return f.fh.segEnabled && f.fh.featEnabled[idx][feature]
}

func (f *frameDec) segFeatureActive(feature int) bool {
	return f.segFeatureActiveIdx(f.segmentID, feature)
}

func (f *frameDec) modeInfo() {
	if f.fh.frameIsIntra {
		f.intraFrameModeInfo()
	} else {
		f.interFrameModeInfo()
	}
}

// ---- intra frame mode info (5.11.7) ------------------------------------------

func (f *frameDec) intraFrameModeInfo() {
	fh := f.fh
	f.skip = false
	if fh.segIDPreSkip {
		f.intraSegmentID()
	}
	f.skipMode = false
	f.readSkip()
	if !fh.segIDPreSkip {
		f.intraSegmentID()
	}
	f.readCdef()
	f.readDeltaQIndex()
	f.readDeltaLf()
	f.readDeltas = false
	f.refFrame = [2]int{intraFrame, refNone}
	if fh.allowIntrabc {
		f.useIntrabc = f.sym.symbol(f.tcdf.intrabc[:]) != 0
	} else {
		f.useIntrabc = false
	}
	if f.useIntrabc {
		f.isInter = true
		f.yMode = dcPred
		f.uvMode = dcPred
		f.motionMode = motionSimple
		f.compoundType = compoundAverage
		f.paletteSizeY, f.paletteSizeUV = 0, 0
		f.interpFilter = [2]int{bilinear, bilinear}
		f.findMvStack(false)
		f.assignMv(false)
	} else {
		f.isInter = false
		above, left := dcPred, dcPred
		if f.availU {
			above = int(f.miAt(f.miRow-1, f.miCol).yMode)
		}
		if f.availL {
			left = int(f.miAt(f.miRow, f.miCol-1).yMode)
		}
		ac := intraModeContext[above]
		lc := intraModeContext[left]
		f.yMode = f.sym.symbol(f.tcdf.intraFrameYMode[ac][lc][:])
		f.intraAngleInfoY()
		if f.hasChroma {
			f.readUVMode()
			if f.uvMode == uvCflPred {
				f.readCflAlphas()
			}
			f.intraAngleInfoUV()
		}
		f.paletteSizeY, f.paletteSizeUV = 0, 0
		if f.miSize >= block8x8 && blockWidth(f.miSize) <= 64 && blockHeight(f.miSize) <= 64 && f.fh.allowScreenCT {
			f.paletteModeInfo()
		}
		f.filterIntraModeInfo()
	}
}

func (f *frameDec) readUVMode() {
	cflAllowed := false
	if f.lossless && getPlaneResidualSize(f.miSize, 1, f.subX, f.subY) == block4x4 {
		cflAllowed = true
	} else if !f.lossless && imax(blockWidth(f.miSize), blockHeight(f.miSize)) <= 32 {
		cflAllowed = true
	}
	if cflAllowed {
		f.uvMode = f.sym.symbol(f.tcdf.uvModeCflAllowed[f.yMode][:])
	} else {
		f.uvMode = f.sym.symbol(f.tcdf.uvModeCflNotAllowed[f.yMode][:])
	}
}

func isDirectionalMode(mode int) bool { return mode >= vPred && mode <= d67Pred }

func (f *frameDec) intraAngleInfoY() {
	f.angleDeltaY = 0
	if f.miSize >= block8x8 && isDirectionalMode(f.yMode) {
		f.angleDeltaY = f.sym.symbol(f.tcdf.angleDelta[f.yMode-vPred][:]) - 3
	}
}

func (f *frameDec) intraAngleInfoUV() {
	f.angleDeltaUV = 0
	if f.miSize >= block8x8 && isDirectionalMode(f.uvMode) {
		f.angleDeltaUV = f.sym.symbol(f.tcdf.angleDelta[f.uvMode-vPred][:]) - 3
	}
}

func (f *frameDec) readCflAlphas() {
	signs := f.sym.symbol(f.tcdf.cflSign[:])
	signU := (signs + 1) / 3
	signV := (signs + 1) % 3
	if signU != 0 {
		ctx := (signU-1)*3 + signV
		f.cflAlphaU = 1 + f.sym.symbol(f.tcdf.cflAlpha[ctx][:])
		if signU == 1 {
			f.cflAlphaU = -f.cflAlphaU
		}
	} else {
		f.cflAlphaU = 0
	}
	if signV != 0 {
		ctx := (signV-1)*3 + signU
		f.cflAlphaV = 1 + f.sym.symbol(f.tcdf.cflAlpha[ctx][:])
		if signV == 1 {
			f.cflAlphaV = -f.cflAlphaV
		}
	} else {
		f.cflAlphaV = 0
	}
}

func (f *frameDec) filterIntraModeInfo() {
	f.useFilterIntra = false
	if f.seq.enableFilterIntra && f.yMode == dcPred && f.paletteSizeY == 0 &&
		imax(blockWidth(f.miSize), blockHeight(f.miSize)) <= 32 {
		f.useFilterIntra = f.sym.symbol(f.tcdf.filterIntra[f.miSize][:]) != 0
		if f.useFilterIntra {
			f.filterIntraMode = f.sym.symbol(f.tcdf.filterIntraMode[:])
		}
	}
}

// ---- segment id (5.11.8, 5.11.9, 5.11.19) ----------------------------------

func (f *frameDec) intraSegmentID() {
	if f.fh.segEnabled {
		f.readSegmentID()
	} else {
		f.segmentID = 0
	}
	f.lossless = f.fh.losslessArray[f.segmentID]
}

func negDeinterleave(diff, ref, max int) int {
	if ref == 0 {
		return diff
	}
	if ref >= max-1 {
		return max - diff - 1
	}
	if 2*ref < max {
		if diff <= 2*ref {
			if diff&1 != 0 {
				return ref + ((diff + 1) >> 1)
			}
			return ref - (diff >> 1)
		}
		return diff
	}
	if diff <= 2*(max-ref-1) {
		if diff&1 != 0 {
			return ref + ((diff + 1) >> 1)
		}
		return ref - (diff >> 1)
	}
	return max - (diff + 1)
}

func (f *frameDec) readSegmentID() {
	prevUL, prevU, prevL := -1, -1, -1
	if f.availU && f.availL {
		prevUL = int(f.miAt(f.miRow-1, f.miCol-1).segID)
	}
	if f.availU {
		prevU = int(f.miAt(f.miRow-1, f.miCol).segID)
	}
	if f.availL {
		prevL = int(f.miAt(f.miRow, f.miCol-1).segID)
	}
	var pred int
	if prevU == -1 {
		if prevL == -1 {
			pred = 0
		} else {
			pred = prevL
		}
	} else if prevL == -1 {
		pred = prevU
	} else if prevUL == prevU {
		pred = prevU
	} else {
		pred = prevL
	}
	if f.skip {
		f.segmentID = pred
		return
	}
	ctx := 0
	if prevUL < 0 {
		ctx = 0
	} else if prevUL == prevU && prevUL == prevL {
		ctx = 2
	} else if prevUL == prevU || prevUL == prevL || prevU == prevL {
		ctx = 1
	}
	v := f.sym.symbol(f.tcdf.segmentID[ctx][:])
	f.segmentID = negDeinterleave(v, pred, f.fh.lastActiveSegID+1)
	f.segmentID = clip3(0, f.fh.lastActiveSegID, f.segmentID)
}

func (f *frameDec) readSkipMode() {
	fh := f.fh
	if f.segFeatureActive(segLvlSkip) || f.segFeatureActive(segLvlRefFrame) || f.segFeatureActive(segLvlGlobalMv) ||
		!fh.skipModePresent || blockWidth(f.miSize) < 8 || blockHeight(f.miSize) < 8 {
		f.skipMode = false
		return
	}
	ctx := 0
	if f.availU && f.miAt(f.miRow-1, f.miCol).skipMode {
		ctx++
	}
	if f.availL && f.miAt(f.miRow, f.miCol-1).skipMode {
		ctx++
	}
	f.skipMode = f.sym.symbol(f.tcdf.skipMode[ctx][:]) != 0
}

func (f *frameDec) readSkip() {
	if f.fh.segIDPreSkip && f.segFeatureActive(segLvlSkip) {
		f.skip = true
		return
	}
	ctx := 0
	if f.availU && f.miAt(f.miRow-1, f.miCol).skip {
		ctx++
	}
	if f.availL && f.miAt(f.miRow, f.miCol-1).skip {
		ctx++
	}
	f.skip = f.sym.symbol(f.tcdf.skip[ctx][:]) != 0
}

func (f *frameDec) readCdef() {
	fh := f.fh
	if f.skip || fh.codedLossless || !f.seq.enableCdef || fh.allowIntrabc {
		return
	}
	r := f.miRow &^ 15
	c := f.miCol &^ 15
	idx := (r>>4)*f.cdefStride + (c >> 4)
	if f.cdefIdx[idx] == -1 {
		v := int8(f.sym.literal(fh.cdefBits))
		f.cdefIdx[idx] = v
		w4, h4 := f.bw4, f.bh4
		for i := r; i < r+h4; i += 16 {
			for j := c; j < c+w4; j += 16 {
				if (i>>4) < (f.miRows+15)>>4 && (j>>4) < f.cdefStride {
					f.cdefIdx[(i>>4)*f.cdefStride+(j>>4)] = v
				}
			}
		}
	}
}

func (f *frameDec) readDeltaQIndex() {
	sbSize := block64x64
	if f.seq.use128 {
		sbSize = block128x128
	}
	if f.miSize == sbSize && f.skip {
		return
	}
	if f.readDeltas {
		abs := f.sym.symbol(f.tcdf.deltaQ[:])
		if abs == 3 {
			remBits := f.sym.literal(3) + 1
			absBits := f.sym.literal(remBits)
			abs = absBits + (1 << uint(remBits)) + 1
		}
		if abs != 0 {
			sign := f.sym.literal(1)
			red := abs
			if sign != 0 {
				red = -abs
			}
			f.currentQIndex = clip3(1, 255, f.currentQIndex+(red<<uint(f.fh.deltaQRes)))
		}
	}
}

func (f *frameDec) readDeltaLf() {
	fh := f.fh
	sbSize := block64x64
	if f.seq.use128 {
		sbSize = block128x128
	}
	if f.miSize == sbSize && f.skip {
		return
	}
	if f.readDeltas && fh.deltaLfPresent {
		cnt := 1
		if fh.deltaLfMulti {
			if f.numPlanes > 1 {
				cnt = frameLfCount
			} else {
				cnt = frameLfCount - 2
			}
		}
		for i := 0; i < cnt; i++ {
			var cdf []uint16
			if fh.deltaLfMulti {
				cdf = f.tcdf.deltaLFMulti[i][:]
			} else {
				cdf = f.tcdf.deltaLF[:]
			}
			abs := f.sym.symbol(cdf)
			var deltaLfAbs int
			if abs == 3 {
				n := f.sym.literal(3) + 1
				absBits := f.sym.literal(n)
				deltaLfAbs = absBits + (1 << uint(n)) + 1
			} else {
				deltaLfAbs = abs
			}
			if deltaLfAbs != 0 {
				sign := f.sym.literal(1)
				red := deltaLfAbs
				if sign != 0 {
					red = -deltaLfAbs
				}
				f.deltaLF[i] = clip3(-maxLoopFilter, maxLoopFilter, f.deltaLF[i]+(red<<uint(fh.deltaLfRes)))
			}
		}
	}
}

// ---- transform size (5.11.15 - 5.11.17) -------------------------------------

func (f *frameDec) getAboveTxWidth(row, col int) int {
	if row == f.miRow {
		if !f.availU {
			return 64
		}
		m := f.miAt(row-1, col)
		if m.skip && m.isInter {
			return blockWidth(int(m.miSize))
		}
	}
	return txWidth[f.miAt(row-1, col).interTxSize]
}

func (f *frameDec) getLeftTxHeight(row, col int) int {
	if col == f.miCol {
		if !f.availL {
			return 64
		}
		m := f.miAt(row, col-1)
		if m.skip && m.isInter {
			return blockHeight(int(m.miSize))
		}
	}
	return txHeight[f.miAt(row, col-1).interTxSize]
}

func (f *frameDec) readTxSize(allowSelect bool) {
	if f.lossless {
		f.txSize = tx4x4
		return
	}
	maxRect := maxTxSizeRect[f.miSize]
	maxDepth := maxTxDepth[f.miSize]
	f.txSize = maxRect
	if f.miSize > block4x4 && allowSelect && f.fh.txMode == txModeSelect {
		maxW := txWidth[maxRect]
		maxH := txHeight[maxRect]
		aboveW, leftH := 0, 0
		if f.availU {
			m := f.miAt(f.miRow-1, f.miCol)
			if m.isInter {
				aboveW = blockWidth(int(m.miSize))
			} else {
				aboveW = f.getAboveTxWidth(f.miRow, f.miCol)
			}
		}
		if f.availL {
			m := f.miAt(f.miRow, f.miCol-1)
			if m.isInter {
				leftH = blockHeight(int(m.miSize))
			} else {
				leftH = f.getLeftTxHeight(f.miRow, f.miCol)
			}
		}
		ctx := b2i(aboveW >= maxW) + b2i(leftH >= maxH)
		var cdf []uint16
		switch maxDepth {
		case 4:
			cdf = f.tcdf.tx64x64[ctx][:]
		case 3:
			cdf = f.tcdf.tx32x32[ctx][:]
		case 2:
			cdf = f.tcdf.tx16x16[ctx][:]
		default:
			cdf = f.tcdf.tx8x8[ctx][:]
		}
		depth := f.sym.symbol(cdf)
		for i := 0; i < depth; i++ {
			f.txSize = splitTxSize[f.txSize]
		}
	}
}

func (f *frameDec) readBlockTxSize() {
	bw4, bh4 := f.bw4, f.bh4
	if f.fh.txMode == txModeSelect && f.miSize > block4x4 && f.isInter && !f.skip && !f.lossless {
		maxTx := maxTxSizeRect[f.miSize]
		txW4 := txWidth[maxTx] / 4
		txH4 := txHeight[maxTx] / 4
		for row := f.miRow; row < f.miRow+bh4; row += txH4 {
			for col := f.miCol; col < f.miCol+bw4; col += txW4 {
				f.readVarTxSize(row, col, maxTx, 0)
			}
		}
	} else {
		f.readTxSize(!f.skip || !f.isInter)
		rowEnd := imin(f.miRow+bh4, f.miRows)
		colEnd := imin(f.miCol+bw4, f.miCols)
		for row := f.miRow; row < rowEnd; row++ {
			for col := f.miCol; col < colEnd; col++ {
				f.miAt(row, col).interTxSize = uint8(f.txSize)
			}
		}
	}
}

func (f *frameDec) readVarTxSize(row, col, txSz, depth int) {
	if row >= f.miRows || col >= f.miCols {
		return
	}
	split := false
	if txSz != tx4x4 && depth != 2 {
		above := b2i(f.getAboveTxWidth(row, col) < txWidth[txSz])
		left := b2i(f.getLeftTxHeight(row, col) < txHeight[txSz])
		size := imin(64, imax(blockWidth(f.miSize), blockHeight(f.miSize)))
		maxTxSz := findTxSize(size, size)
		txSzSqrUp := txSizeSqrUp[txSz]
		ctx := b2i(txSzSqrUp != maxTxSz)*3 + (4-maxTxSz)*6 + above + left
		split = f.sym.symbol(f.tcdf.txfmSplit[ctx][:]) != 0
	}
	w4 := txWidth[txSz] / 4
	h4 := txHeight[txSz] / 4
	if split {
		sub := splitTxSize[txSz]
		stepW := txWidth[sub] / 4
		stepH := txHeight[sub] / 4
		for i := 0; i < h4; i += stepH {
			for j := 0; j < w4; j += stepW {
				f.readVarTxSize(row+i, col+j, sub, depth+1)
			}
		}
	} else {
		for i := 0; i < h4; i++ {
			for j := 0; j < w4; j++ {
				if row+i < f.miRows && col+j < f.miCols {
					f.miAt(row+i, col+j).interTxSize = uint8(txSz)
				}
			}
		}
		f.txSize = txSz
	}
}

func findTxSize(w, h int) int {
	for t := 0; t < txSizesAll; t++ {
		if txWidth[t] == w && txHeight[t] == h {
			return t
		}
	}
	return 0
}

func getPlaneResidualSize(subsize, plane, subX, subY int) int {
	if plane == 0 {
		return subsize
	}
	return subsampledSize[subsize][subX][subY]
}
