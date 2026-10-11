package av1

// ---- inter frame mode info (5.11.18) -----------------------------------------

func (f *frameDec) interFrameModeInfo() {
	fh := f.fh
	f.useIntrabc = false
	f.leftRefFrame = [2]int{intraFrame, refNone}
	f.aboveRefFrame = [2]int{intraFrame, refNone}
	if f.availL {
		m := f.miAt(f.miRow, f.miCol-1)
		f.leftRefFrame = [2]int{int(m.refFrame[0]), int(m.refFrame[1])}
	}
	if f.availU {
		m := f.miAt(f.miRow-1, f.miCol)
		f.aboveRefFrame = [2]int{int(m.refFrame[0]), int(m.refFrame[1])}
	}
	f.leftIntra = f.leftRefFrame[0] <= intraFrame
	f.aboveIntra = f.aboveRefFrame[0] <= intraFrame
	f.leftSingle = f.leftRefFrame[1] <= intraFrame
	f.aboveSingle = f.aboveRefFrame[1] <= intraFrame
	f.skip = false
	f.interSegmentID(true)
	f.readSkipMode()
	if f.skipMode {
		f.skip = true
	} else {
		f.readSkip()
	}
	if !fh.segIDPreSkip {
		f.interSegmentID(false)
	}
	f.lossless = fh.losslessArray[f.segmentID]
	f.readCdef()
	f.readDeltaQIndex()
	f.readDeltaLf()
	f.readDeltas = false
	f.readIsInter()
	if f.isInter {
		f.interBlockModeInfo()
	} else {
		f.intraBlockModeInfo()
	}
}

func (f *frameDec) getSegmentID() int {
	xMis := imin(f.miCols-f.miCol, f.bw4)
	yMis := imin(f.miRows-f.miRow, f.bh4)
	seg := 7
	for y := 0; y < yMis; y++ {
		for x := 0; x < xMis; x++ {
			s := int(f.prevSegIDs[(f.miRow+y)*f.miCols+f.miCol+x])
			if s < seg {
				seg = s
			}
		}
	}
	return seg
}

func (f *frameDec) interSegmentID(preSkip bool) {
	fh := f.fh
	if !fh.segEnabled {
		f.segmentID = 0
		return
	}
	predicted := f.getSegmentID()
	if !fh.segUpdateMap {
		f.segmentID = predicted
		return
	}
	if preSkip && !fh.segIDPreSkip {
		f.segmentID = 0
		return
	}
	setCtx := func(v uint8) {
		for i := 0; i < f.bw4; i++ {
			f.aboveSegPred[f.miCol+i] = v
		}
		for i := 0; i < f.bh4; i++ {
			f.leftSegPred[f.miRow+i] = v
		}
	}
	if !preSkip {
		if f.skip {
			setCtx(0)
			f.readSegmentID()
			return
		}
	}
	if fh.segTemporalUpd {
		ctx := int(f.leftSegPred[f.miRow]) + int(f.aboveSegPred[f.miCol])
		pred := f.sym.symbol(f.tcdf.segmentIDPredicted[ctx][:])
		if pred != 0 {
			f.segmentID = predicted
		} else {
			f.readSegmentID()
		}
		setCtx(uint8(pred))
	} else {
		f.readSegmentID()
	}
}

func (f *frameDec) readIsInter() {
	fh := f.fh
	if f.skipMode {
		f.isInter = true
	} else if f.segFeatureActive(segLvlRefFrame) {
		f.isInter = fh.featData[f.segmentID][segLvlRefFrame] != intraFrame
	} else if f.segFeatureActive(segLvlGlobalMv) {
		f.isInter = true
	} else {
		ctx := 0
		if f.availU && f.availL {
			if f.leftIntra && f.aboveIntra {
				ctx = 3
			} else if f.leftIntra || f.aboveIntra {
				ctx = 1
			}
		} else if f.availU || f.availL {
			if f.availU {
				ctx = 2 * b2i(f.aboveIntra)
			} else {
				ctx = 2 * b2i(f.leftIntra)
			}
		}
		f.isInter = f.sym.symbol(f.tcdf.isInter[ctx][:]) != 0
	}
}

func (f *frameDec) intraBlockModeInfo() {
	f.refFrame = [2]int{intraFrame, refNone}
	f.yMode = f.sym.symbol(f.tcdf.yMode[sizeGroup[f.miSize]][:])
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

// ---- inter block mode info (5.11.23) ----------------------------------------

func hasNearmv(m int) bool {
	return m == modeNearMv || m == modeNearNear || m == modeNearNew || m == modeNewNear
}

func (f *frameDec) interBlockModeInfo() {
	fh := f.fh
	f.paletteSizeY, f.paletteSizeUV = 0, 0
	f.readRefFrames()
	isCompound := f.refFrame[1] > intraFrame
	f.findMvStack(isCompound)
	if f.skipMode {
		f.yMode = modeNearestNearest
	} else if f.segFeatureActive(segLvlSkip) || f.segFeatureActive(segLvlGlobalMv) {
		f.yMode = modeGlobalMv
	} else if isCompound {
		ctx := compoundModeCtxMap[f.refMvContext>>1][imin(f.newMvContext, 4)]
		f.yMode = modeNearestNearest + f.sym.symbol(f.tcdf.compoundMode[ctx][:])
	} else {
		if f.sym.symbol(f.tcdf.newMv[f.newMvContext][:]) == 0 {
			f.yMode = modeNewMv
		} else if f.sym.symbol(f.tcdf.zeroMv[f.zeroMvContext][:]) == 0 {
			f.yMode = modeGlobalMv
		} else if f.sym.symbol(f.tcdf.refMv[f.refMvContext][:]) == 0 {
			f.yMode = modeNearestMv
		} else {
			f.yMode = modeNearMv
		}
	}
	f.refMvIdx = 0
	if f.yMode == modeNewMv || f.yMode == modeNewNew {
		for idx := 0; idx < 2; idx++ {
			if f.numMvFound > idx+1 {
				drl := f.sym.symbol(f.tcdf.drlMode[f.drlCtxStack[idx]][:])
				if drl == 0 {
					f.refMvIdx = idx
					break
				}
				f.refMvIdx = idx + 1
			}
		}
	} else if hasNearmv(f.yMode) {
		f.refMvIdx = 1
		for idx := 1; idx < 3; idx++ {
			if f.numMvFound > idx+1 {
				drl := f.sym.symbol(f.tcdf.drlMode[f.drlCtxStack[idx]][:])
				if drl == 0 {
					f.refMvIdx = idx
					break
				}
				f.refMvIdx = idx + 1
			}
		}
	}
	f.assignMv(isCompound)
	f.readInterIntraMode(isCompound)
	f.readMotionMode(isCompound)
	f.readCompoundType(isCompound)
	if fh.interpFilter == switchable {
		n := 1
		if f.seq.enableDualFilter {
			n = 2
			cov(covInterpDual)
		}
		for dir := 0; dir < n; dir++ {
			if f.needsInterpFilter() {
				ctx := ((dir&1)*2 + b2i(f.refFrame[1] > intraFrame)) * 4
				leftType, aboveType := 3, 3
				if f.availL {
					m := f.miAt(f.miRow, f.miCol-1)
					if int(m.refFrame[0]) == f.refFrame[0] || int(m.refFrame[1]) == f.refFrame[0] {
						leftType = int(m.interpFilter[dir])
					}
				}
				if f.availU {
					m := f.miAt(f.miRow-1, f.miCol)
					if int(m.refFrame[0]) == f.refFrame[0] || int(m.refFrame[1]) == f.refFrame[0] {
						aboveType = int(m.interpFilter[dir])
					}
				}
				if leftType == aboveType {
					ctx += leftType
				} else if leftType == 3 {
					ctx += aboveType
				} else if aboveType == 3 {
					ctx += leftType
				} else {
					ctx += 3
				}
				f.interpFilter[dir] = f.sym.symbol(f.tcdf.interpFilter[ctx][:])
			} else {
				f.interpFilter[dir] = eighttap
			}
		}
		if !f.seq.enableDualFilter {
			f.interpFilter[1] = f.interpFilter[0]
		}
	} else {
		f.interpFilter[0] = fh.interpFilter
		f.interpFilter[1] = fh.interpFilter
	}
}

func (f *frameDec) needsInterpFilter() bool {
	fh := f.fh
	large := imin(blockWidth(f.miSize), blockHeight(f.miSize)) >= 8
	if f.skipMode || f.motionMode == motionLocalWarp {
		return false
	} else if large && f.yMode == modeGlobalMv {
		return fh.gmType[f.refFrame[0]] == gmTranslation
	} else if large && f.yMode == modeGlobalGlobal {
		return fh.gmType[f.refFrame[0]] == gmTranslation || fh.gmType[f.refFrame[1]] == gmTranslation
	}
	return true
}

// ---- reference frames (5.11.25) ---------------------------------------------

func (f *frameDec) countRefs(frameType int) int {
	c := 0
	if f.availU {
		if f.aboveRefFrame[0] == frameType {
			c++
		}
		if f.aboveRefFrame[1] == frameType {
			c++
		}
	}
	if f.availL {
		if f.leftRefFrame[0] == frameType {
			c++
		}
		if f.leftRefFrame[1] == frameType {
			c++
		}
	}
	return c
}

func refCountCtx(c0, c1 int) int {
	if c0 < c1 {
		return 0
	} else if c0 == c1 {
		return 1
	}
	return 2
}

func checkBackward(r int) bool { return r >= bwdrefFrame && r <= altrefFrame }

func (f *frameDec) ctxCompRef() int {
	return refCountCtx(f.countRefs(lastFrame)+f.countRefs(last2Frame), f.countRefs(last3Frame)+f.countRefs(goldenFrame))
}
func (f *frameDec) ctxCompRefP1() int {
	return refCountCtx(f.countRefs(lastFrame), f.countRefs(last2Frame))
}
func (f *frameDec) ctxCompRefP2() int {
	return refCountCtx(f.countRefs(last3Frame), f.countRefs(goldenFrame))
}
func (f *frameDec) ctxCompBwdref() int {
	return refCountCtx(f.countRefs(bwdrefFrame)+f.countRefs(altref2Frm), f.countRefs(altrefFrame))
}
func (f *frameDec) ctxCompBwdrefP1() int {
	return refCountCtx(f.countRefs(bwdrefFrame), f.countRefs(altref2Frm))
}
func (f *frameDec) ctxSingleRefP1() int {
	fwd := f.countRefs(lastFrame) + f.countRefs(last2Frame) + f.countRefs(last3Frame) + f.countRefs(goldenFrame)
	bwd := f.countRefs(bwdrefFrame) + f.countRefs(altref2Frm) + f.countRefs(altrefFrame)
	return refCountCtx(fwd, bwd)
}

func isSamedirRefPair(r0, r1 int) bool { return (r0 >= bwdrefFrame) == (r1 >= bwdrefFrame) }

func (f *frameDec) compModeCtx() int {
	if f.availU && f.availL {
		if f.aboveSingle && f.leftSingle {
			return b2i(checkBackward(f.aboveRefFrame[0])) ^ b2i(checkBackward(f.leftRefFrame[0]))
		} else if f.aboveSingle {
			return 2 + b2i(checkBackward(f.aboveRefFrame[0]) || f.aboveIntra)
		} else if f.leftSingle {
			return 2 + b2i(checkBackward(f.leftRefFrame[0]) || f.leftIntra)
		}
		return 4
	} else if f.availU {
		if f.aboveSingle {
			return b2i(checkBackward(f.aboveRefFrame[0]))
		}
		return 3
	} else if f.availL {
		if f.leftSingle {
			return b2i(checkBackward(f.leftRefFrame[0]))
		}
		return 3
	}
	return 1
}

func (f *frameDec) compRefTypeCtx() int {
	above0, above1 := f.aboveRefFrame[0], f.aboveRefFrame[1]
	left0, left1 := f.leftRefFrame[0], f.leftRefFrame[1]
	aboveCompInter := f.availU && !f.aboveIntra && !f.aboveSingle
	leftCompInter := f.availL && !f.leftIntra && !f.leftSingle
	aboveUniComp := aboveCompInter && isSamedirRefPair(above0, above1)
	leftUniComp := leftCompInter && isSamedirRefPair(left0, left1)
	if f.availU && !f.aboveIntra && f.availL && !f.leftIntra {
		samedir := b2i(isSamedirRefPair(above0, left0))
		if !aboveCompInter && !leftCompInter {
			return 1 + 2*samedir
		} else if !aboveCompInter {
			if !leftUniComp {
				return 1
			}
			return 3 + samedir
		} else if !leftCompInter {
			if !aboveUniComp {
				return 1
			}
			return 3 + samedir
		}
		if !aboveUniComp && !leftUniComp {
			return 0
		} else if !aboveUniComp || !leftUniComp {
			return 2
		}
		return 3 + b2i((above0 == bwdrefFrame) == (left0 == bwdrefFrame))
	} else if f.availU && f.availL {
		if aboveCompInter {
			return 1 + 2*b2i(aboveUniComp)
		} else if leftCompInter {
			return 1 + 2*b2i(leftUniComp)
		}
		return 2
	} else if aboveCompInter {
		return 4 * b2i(aboveUniComp)
	} else if leftCompInter {
		return 4 * b2i(leftUniComp)
	}
	return 2
}

func (f *frameDec) readRefFrames() {
	fh := f.fh
	if f.skipMode {
		f.refFrame = [2]int{fh.skipModeFrame[0], fh.skipModeFrame[1]}
		return
	}
	if f.segFeatureActive(segLvlRefFrame) {
		f.refFrame = [2]int{fh.featData[f.segmentID][segLvlRefFrame], refNone}
		return
	}
	if f.segFeatureActive(segLvlSkip) || f.segFeatureActive(segLvlGlobalMv) {
		f.refFrame = [2]int{lastFrame, refNone}
		return
	}
	compMode := 0
	if fh.referenceSelect && imin(f.bw4, f.bh4) >= 2 {
		compMode = f.sym.symbol(f.tcdf.compMode[f.compModeCtx()][:])
	}
	if compMode == 1 {
		compRefType := f.sym.symbol(f.tcdf.compRefType[f.compRefTypeCtx()][:])
		if compRefType == 0 { // UNIDIR
			ctx0 := f.ctxSingleRefP1()
			if f.sym.symbol(f.tcdf.uniCompRef[ctx0][0][:]) != 0 {
				f.refFrame = [2]int{bwdrefFrame, altrefFrame}
			} else {
				ctx1 := refCountCtx(f.countRefs(last2Frame), f.countRefs(last3Frame)+f.countRefs(goldenFrame))
				if f.sym.symbol(f.tcdf.uniCompRef[ctx1][1][:]) != 0 {
					ctx2 := f.ctxCompRefP2()
					if f.sym.symbol(f.tcdf.uniCompRef[ctx2][2][:]) != 0 {
						f.refFrame = [2]int{lastFrame, goldenFrame}
					} else {
						f.refFrame = [2]int{lastFrame, last3Frame}
					}
				} else {
					f.refFrame = [2]int{lastFrame, last2Frame}
				}
			}
		} else {
			if f.sym.symbol(f.tcdf.compRef[f.ctxCompRef()][0][:]) == 0 {
				if f.sym.symbol(f.tcdf.compRef[f.ctxCompRefP1()][1][:]) != 0 {
					f.refFrame[0] = last2Frame
				} else {
					f.refFrame[0] = lastFrame
				}
			} else {
				if f.sym.symbol(f.tcdf.compRef[f.ctxCompRefP2()][2][:]) != 0 {
					f.refFrame[0] = goldenFrame
				} else {
					f.refFrame[0] = last3Frame
				}
			}
			if f.sym.symbol(f.tcdf.compBwdRef[f.ctxCompBwdref()][0][:]) == 0 {
				if f.sym.symbol(f.tcdf.compBwdRef[f.ctxCompBwdrefP1()][1][:]) != 0 {
					f.refFrame[1] = altref2Frm
				} else {
					f.refFrame[1] = bwdrefFrame
				}
			} else {
				f.refFrame[1] = altrefFrame
			}
		}
		return
	}
	if f.sym.symbol(f.tcdf.singleRef[f.ctxSingleRefP1()][0][:]) != 0 {
		if f.sym.symbol(f.tcdf.singleRef[f.ctxCompBwdref()][1][:]) == 0 {
			if f.sym.symbol(f.tcdf.singleRef[f.ctxCompBwdrefP1()][5][:]) != 0 {
				f.refFrame[0] = altref2Frm
			} else {
				f.refFrame[0] = bwdrefFrame
			}
		} else {
			f.refFrame[0] = altrefFrame
		}
	} else {
		if f.sym.symbol(f.tcdf.singleRef[f.ctxCompRef()][2][:]) != 0 {
			if f.sym.symbol(f.tcdf.singleRef[f.ctxCompRefP2()][4][:]) != 0 {
				f.refFrame[0] = goldenFrame
			} else {
				f.refFrame[0] = last3Frame
			}
		} else {
			if f.sym.symbol(f.tcdf.singleRef[f.ctxCompRefP1()][3][:]) != 0 {
				f.refFrame[0] = last2Frame
			} else {
				f.refFrame[0] = lastFrame
			}
		}
	}
	f.refFrame[1] = refNone
}

// ---- motion vectors (5.11.26, 5.11.30 - 5.11.32) ------------------------------

func (f *frameDec) getMode(refList int) int {
	y := f.yMode
	if refList == 0 {
		switch {
		case y < modeNearestNearest:
			return y
		case y == modeNewNew || y == modeNewNearest || y == modeNewNear:
			return modeNewMv
		case y == modeNearestNearest || y == modeNearestNew:
			return modeNearestMv
		case y == modeNearNear || y == modeNearNew:
			return modeNearMv
		}
		return modeGlobalMv
	}
	switch {
	case y == modeNewNew || y == modeNearestNew || y == modeNearNew:
		return modeNewMv
	case y == modeNearestNearest || y == modeNewNearest:
		return modeNearestMv
	case y == modeNearNear || y == modeNewNear:
		return modeNearMv
	}
	return modeGlobalMv
}

func (f *frameDec) assignMv(isCompound bool) {
	n := 1
	if isCompound {
		n = 2
	}
	for i := 0; i < n; i++ {
		compMode := modeNewMv
		if !f.useIntrabc {
			compMode = f.getMode(i)
		}
		if f.useIntrabc {
			f.predMv[0] = f.refStackMv[0][0]
			if f.predMv[0].row == 0 && f.predMv[0].col == 0 {
				f.predMv[0] = f.refStackMv[1][0]
			}
			if f.predMv[0].row == 0 && f.predMv[0].col == 0 {
				sbSize4 := 16
				if f.seq.use128 {
					sbSize4 = 32
				}
				if f.miRow-sbSize4 < f.miRowStart {
					f.predMv[0].row = 0
					f.predMv[0].col = int16(-(sbSize4*4 + 256) * 8)
				} else {
					f.predMv[0].row = int16(-(sbSize4 * 4 * 8))
					f.predMv[0].col = 0
				}
			}
		} else if compMode == modeGlobalMv {
			f.predMv[i] = f.globalMvs[i]
		} else {
			pos := f.refMvIdx
			if compMode == modeNearestMv {
				pos = 0
			}
			if compMode == modeNewMv && f.numMvFound <= 1 {
				pos = 0
			}
			f.predMv[i] = f.refStackMv[pos][i]
		}
		if compMode == modeNewMv {
			f.readMv(i)
		} else {
			f.mv[i] = f.predMv[i]
		}
	}
}

func (f *frameDec) readMv(ref int) {
	var diff [2]int
	ctx := 0
	if f.useIntrabc {
		ctx = 1
	}
	joint := f.sym.symbol(f.tcdf.mvJoint[ctx][:])
	if joint == 2 || joint == 3 {
		diff[0] = f.readMvComponent(ctx, 0)
	}
	if joint == 1 || joint == 3 {
		diff[1] = f.readMvComponent(ctx, 1)
	}
	f.mv[ref].row = int16(int(f.predMv[ref].row) + diff[0])
	f.mv[ref].col = int16(int(f.predMv[ref].col) + diff[1])
}

func (f *frameDec) readMvComponent(ctx, comp int) int {
	fh := f.fh
	sign := f.sym.symbol(f.tcdf.mvSign[ctx][comp][:])
	class := f.sym.symbol(f.tcdf.mvClass[ctx][comp][:])
	var mag int
	if class == 0 {
		bit := f.sym.symbol(f.tcdf.mvClass0Bit[ctx][comp][:])
		fr := 3
		if !fh.forceIntMv {
			fr = f.sym.symbol(f.tcdf.mvClass0Fr[ctx][comp][bit][:])
		}
		hp := 1
		if fh.allowHighPrecMV {
			hp = f.sym.symbol(f.tcdf.mvClass0Hp[ctx][comp][:])
		}
		mag = ((bit << 3) | (fr << 1) | hp) + 1
	} else {
		d := 0
		for i := 0; i < class; i++ {
			d |= f.sym.symbol(f.tcdf.mvBit[ctx][comp][i][:]) << uint(i)
		}
		mag = 2 << uint(class+2)
		fr := 3
		if !fh.forceIntMv {
			fr = f.sym.symbol(f.tcdf.mvFr[ctx][comp][:])
		}
		hp := 1
		if fh.allowHighPrecMV {
			hp = f.sym.symbol(f.tcdf.mvHp[ctx][comp][:])
		}
		mag += ((d << 3) | (fr << 1) | hp) + 1
	}
	if sign != 0 {
		return -mag
	}
	return mag
}

// ---- inter-intra, motion mode, compound type -------------------------------------

func (f *frameDec) isScaled(refFrame int) bool {
	return f.refScaleX[refFrame] != 1<<14 || f.refScaleY[refFrame] != 1<<14
}

func (f *frameDec) readInterIntraMode(isCompound bool) {
	f.interintra = false
	if !f.skipMode && f.seq.enableInterintra && !isCompound && f.miSize >= block8x8 && f.miSize <= block32x32 {
		ctx := sizeGroup[f.miSize] - 1
		f.interintra = f.sym.symbol(f.tcdf.interIntra[ctx][:]) != 0
		if f.interintra {
			f.interintraMode = f.sym.symbol(f.tcdf.interIntraMode[ctx][:])
			f.refFrame[1] = intraFrame
			f.angleDeltaY, f.angleDeltaUV = 0, 0
			f.useFilterIntra = false
			f.wedgeInterintra = f.sym.symbol(f.tcdf.wedgeInterIntra[f.miSize][:]) != 0
			if f.wedgeInterintra {
				f.wedgeIndex = f.sym.symbol(f.tcdf.wedgeIndex[f.miSize][:])
				f.wedgeSign = 0
			}
		}
	}
}

func (f *frameDec) readMotionMode(isCompound bool) {
	fh := f.fh
	f.motionMode = motionSimple
	if f.skipMode || !fh.motionModeSw {
		return
	}
	if imin(blockWidth(f.miSize), blockHeight(f.miSize)) < 8 {
		return
	}
	if !fh.forceIntMv && (f.yMode == modeGlobalMv || f.yMode == modeGlobalGlobal) {
		if fh.gmType[f.refFrame[0]] > gmTranslation {
			return
		}
	}
	if isCompound || f.refFrame[1] == intraFrame || !f.hasOverlappableCandidates() {
		return
	}
	f.findWarpSamples()
	if fh.forceIntMv || f.numSamples == 0 || !fh.allowWarped || f.isScaled(f.refFrame[0]) {
		if f.sym.symbol(f.tcdf.useObmc[f.miSize][:]) != 0 {
			f.motionMode = motionObmc
		}
	} else {
		f.motionMode = f.sym.symbol(f.tcdf.motionMode[f.miSize][:])
	}
}

func (f *frameDec) readCompoundType(isCompound bool) {
	f.compGroupIdx = 0
	f.compoundIdx = 1
	if f.skipMode {
		f.compoundType = compoundAverage
		return
	}
	if isCompound {
		n := wedgeBits[f.miSize]
		if f.seq.enableMasked {
			ctx := 0
			if f.availU {
				if !f.aboveSingle {
					ctx += int(f.miAt(f.miRow-1, f.miCol).compGroupIdx)
				} else if f.aboveRefFrame[0] == altrefFrame {
					ctx += 3
				}
			}
			if f.availL {
				if !f.leftSingle {
					ctx += int(f.miAt(f.miRow, f.miCol-1).compGroupIdx)
				} else if f.leftRefFrame[0] == altrefFrame {
					ctx += 3
				}
			}
			ctx = imin(5, ctx)
			f.compGroupIdx = f.sym.symbol(f.tcdf.compGroupIdx[ctx][:])
		}
		if f.compGroupIdx == 0 {
			if f.seq.enableJntComp {
				fwd := abs(f.seq.relDist(f.fh.orderHints[f.refFrame[0]], f.fh.orderHint))
				bck := abs(f.seq.relDist(f.fh.orderHints[f.refFrame[1]], f.fh.orderHint))
				ctx := 0
				if fwd == bck {
					ctx = 3
				}
				if f.availU {
					if !f.aboveSingle {
						ctx += int(f.miAt(f.miRow-1, f.miCol).compoundIdx)
					} else if f.aboveRefFrame[0] == altrefFrame {
						ctx++
					}
				}
				if f.availL {
					if !f.leftSingle {
						ctx += int(f.miAt(f.miRow, f.miCol-1).compoundIdx)
					} else if f.leftRefFrame[0] == altrefFrame {
						ctx++
					}
				}
				f.compoundIdx = f.sym.symbol(f.tcdf.compoundIdx[ctx][:])
				if f.compoundIdx != 0 {
					f.compoundType = compoundAverage
				} else {
					f.compoundType = compoundDistance
				}
			} else {
				f.compoundType = compoundAverage
			}
		} else {
			if n == 0 {
				f.compoundType = compoundDiffwtd
			} else {
				f.compoundType = f.sym.symbol(f.tcdf.compoundType[f.miSize][:])
			}
		}
		if f.compoundType == compoundWedge {
			f.wedgeIndex = f.sym.symbol(f.tcdf.wedgeIndex[f.miSize][:])
			f.wedgeSign = f.sym.literal(1)
		} else if f.compoundType == compoundDiffwtd {
			f.maskType = f.sym.literal(1)
		}
	} else {
		if f.interintra {
			if f.wedgeInterintra {
				f.compoundType = compoundWedge
			} else {
				f.compoundType = compoundIntra
			}
		} else {
			f.compoundType = compoundAverage
		}
	}
}
