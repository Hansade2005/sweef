package av1

import "errors"

// parseFrameHeader implements uncompressed_header() (spec 5.9.2). On return
// with showExisting set only the fields needed for output are valid.
func (d *Decoder) parseFrameHeader(b *bitReader, temporalID, spatialID int) (*frameHdr, error) {
	s := d.seq
	fh := &frameHdr{}
	fh.orderHints = d.orderHints
	idLen := 0
	if s.frameIDNumbersPresent {
		idLen = s.addFrameIDLenM1 + s.deltaFrameIDLenM2 + 3
	}
	const allFrames = (1 << numRefFrames) - 1
	if s.reducedStill {
		fh.frameType = keyFrame
		fh.frameIsIntra = true
		fh.showFrame = true
	} else {
		fh.showExisting = b.flag()
		if fh.showExisting {
			fh.frameToShow = b.f(3)
			if s.decoderModelInfoPresent && !s.equalPictureInterval {
				b.f(s.framePresTimeLenM1 + 1)
			}
			if s.frameIDNumbersPresent {
				b.f(idLen)
			}
			r := d.ref[fh.frameToShow]
			if r == nil {
				return nil, errBitstream
			}
			fh.frameType = r.frameType
			if fh.frameType == keyFrame {
				fh.refreshFlags = allFrames
			}
			if s.filmGrainPresent {
				fh.film = r.film
			}
			fh.hdrBits = b.pos
			return fh, nil
		}
		fh.frameType = b.f(2)
		fh.frameIsIntra = fh.frameType == intraOnlyFrame || fh.frameType == keyFrame
		switch fh.frameType {
		case intraOnlyFrame:
			cov(covIntraOnly)
		case switchFrame:
			cov(covSwitchFrame)
		}
		fh.showFrame = b.flag()
		if fh.showFrame && s.decoderModelInfoPresent && !s.equalPictureInterval {
			b.f(s.framePresTimeLenM1 + 1)
		}
		if fh.showFrame {
			fh.showable = fh.frameType != keyFrame
		} else {
			fh.showable = b.flag()
		}
		fh.posErrorRes = b.pos
		if fh.frameType == switchFrame || (fh.frameType == keyFrame && fh.showFrame) {
			fh.errorRes = true
		} else {
			fh.errorRes = b.flag()
		}
	}
	if fh.frameType == keyFrame && fh.showFrame {
		for i := 0; i < numRefFrames; i++ {
			d.refValid[i] = false
			d.refOrderHint[i] = 0
		}
		for i := 0; i < refsPerFrame; i++ {
			fh.orderHints[lastFrame+i] = 0
		}
	}
	fh.disableCdfUpd = b.flag()
	if s.forceScreenContent == selectScreenCont {
		fh.allowScreenCT = b.flag()
	} else {
		fh.allowScreenCT = s.forceScreenContent != 0
	}
	if fh.allowScreenCT {
		if s.forceIntegerMv == selectIntegerMv {
			fh.forceIntMv = b.flag()
		} else {
			fh.forceIntMv = s.forceIntegerMv != 0
		}
	}
	if fh.frameIsIntra {
		fh.forceIntMv = true
	}
	if s.frameIDNumbersPresent {
		fh.currentFrameID = b.f(idLen)
		// mark_ref_frames
		diffLen := s.deltaFrameIDLenM2 + 2
		for i := 0; i < numRefFrames; i++ {
			if fh.currentFrameID > (1 << uint(diffLen)) {
				if d.refFrameID[i] > fh.currentFrameID || d.refFrameID[i] < fh.currentFrameID-(1<<uint(diffLen)) {
					d.refValid[i] = false
				}
			} else if d.refFrameID[i] > fh.currentFrameID && d.refFrameID[i] < (1<<uint(idLen))+fh.currentFrameID-(1<<uint(diffLen)) {
				d.refValid[i] = false
			}
		}
	}
	if fh.frameType == switchFrame {
		fh.sizeOverride = true
	} else if s.reducedStill {
		fh.sizeOverride = false
	} else {
		fh.sizeOverride = b.flag()
	}
	fh.orderHint = b.f(s.orderHintBits)
	if fh.frameIsIntra || fh.errorRes {
		fh.primaryRef = primaryRefNone
	} else {
		fh.primaryRef = b.f(3)
	}
	if s.decoderModelInfoPresent {
		if b.flag() { // buffer_removal_time_present_flag
			for op := 0; op < s.opCnt; op++ {
				if s.decoderModelPresentOp[op] {
					idc := s.opIdc[op]
					inT := (idc >> uint(temporalID)) & 1
					inS := (idc >> uint(spatialID+8)) & 1
					if idc == 0 || (inT != 0 && inS != 0) {
						b.f(s.bufferRemovalTimeLenM1 + 1)
					}
				}
			}
		}
	}
	fh.posRefresh = b.pos
	if fh.frameType == switchFrame || (fh.frameType == keyFrame && fh.showFrame) {
		fh.refreshFlags = allFrames
	} else {
		fh.refreshFlags = b.f(8)
	}
	if !fh.frameIsIntra || fh.refreshFlags != allFrames {
		if fh.errorRes && s.enableOrderHint {
			for i := 0; i < numRefFrames; i++ {
				roh := b.f(s.orderHintBits)
				if roh != d.refOrderHint[i] {
					d.refValid[i] = false
					// Missing frame: spec allows concealment; mark order hint.
					d.refOrderHint[i] = roh
				}
			}
		}
	}
	if fh.frameIsIntra {
		d.frameSize(b, fh)
		d.renderSize(b, fh)
		if fh.allowScreenCT && fh.upscaledWidth == fh.frameWidth {
			fh.allowIntrabc = b.flag()
		}
	} else {
		shortSignaling := false
		fh.posShortSig = b.pos
		if s.enableOrderHint {
			shortSignaling = b.flag()
			if shortSignaling {
				cov(covShortSignaling)
				lastIdx := b.f(3)
				goldIdx := b.f(3)
				d.setFrameRefs(fh, lastIdx, goldIdx)
			}
		}
		for i := 0; i < refsPerFrame; i++ {
			if !shortSignaling {
				fh.refFrameIdx[i] = b.f(3)
			}
			if s.frameIDNumbersPresent {
				b.f(s.deltaFrameIDLenM2 + 2)
			}
		}
		fh.posRefIdxEnd = b.pos
		for i := 0; i < refsPerFrame; i++ {
			if d.ref[fh.refFrameIdx[i]] == nil {
				return nil, errBitstream
			}
		}
		if fh.sizeOverride && !fh.errorRes {
			d.frameSizeWithRefs(b, fh)
		} else {
			d.frameSize(b, fh)
			d.renderSize(b, fh)
		}
		if fh.forceIntMv {
			fh.allowHighPrecMV = false
		} else {
			fh.allowHighPrecMV = b.flag()
		}
		if b.flag() { // is_filter_switchable
			fh.interpFilter = switchable
		} else {
			fh.interpFilter = b.f(2)
		}
		fh.motionModeSw = b.flag()
		if fh.errorRes || !s.enableRefFrameMvs {
			fh.useRefFrameMvs = false
		} else {
			fh.useRefFrameMvs = b.flag()
		}
		for i := 0; i < refsPerFrame; i++ {
			rf := lastFrame + i
			hint := d.refOrderHint[fh.refFrameIdx[i]]
			fh.orderHints[rf] = hint
			if !s.enableOrderHint {
				fh.refSignBias[rf] = false
			} else {
				fh.refSignBias[rf] = s.relDist(hint, fh.orderHint) > 0
			}
		}
	}
	if s.reducedStill || fh.disableCdfUpd {
		fh.disableFrameEndUpd = true
	} else {
		fh.disableFrameEndUpd = b.flag()
	}
	d.hdrCDF = &cdfCtx{}
	if fh.primaryRef == primaryRefNone {
		d.hdrCDF.initNonCoeff()
		// setup_past_independence
		fh.lfDeltaEnable = true
		fh.lfRefDeltas = [totalRefsPerFrame]int{1, 0, 0, 0, -1, 0, -1, -1}
		for r := lastFrame; r <= altrefFrame; r++ {
			for i := 0; i < 6; i++ {
				v := 0
				if i%3 == 2 {
					v = 1 << warpedModelPrec
				}
				fh.prevGmParams[r][i] = v
			}
		}
	} else {
		pr := d.ref[fh.refFrameIdx[fh.primaryRef]]
		if pr == nil {
			return nil, errBitstream
		}
		*d.hdrCDF = *pr.cdf
		d.hdrCDF.clearCounters()
		fh.prevGmParams = pr.gmParams
		fh.lfRefDeltas = pr.lfRefDeltas
		fh.lfModeDeltas = pr.lfModeDeltas
		fh.featEnabled = pr.featEnabled
		fh.featData = pr.featData
	}
	d.tileInfo(b, fh)
	d.quantParams(b, fh)
	d.segmentationParams(b, fh)
	// delta_q_params
	if fh.baseQIdx > 0 {
		fh.deltaQPresent = b.flag()
	}
	if fh.deltaQPresent {
		fh.deltaQRes = b.f(2)
	}
	// delta_lf_params
	if fh.deltaQPresent {
		if !fh.allowIntrabc {
			fh.deltaLfPresent = b.flag()
		}
		if fh.deltaLfPresent {
			fh.deltaLfRes = b.f(2)
			fh.deltaLfMulti = b.flag()
		}
	}
	if fh.primaryRef == primaryRefNone {
		d.hdrCDF.initCoeff(fh.baseQIdx)
		d.prevSegIDsFor = nil
	} else {
		d.prevSegIDsFor = d.ref[fh.refFrameIdx[fh.primaryRef]]
	}
	fh.codedLossless = true
	for seg := 0; seg < maxSegments; seg++ {
		q := fh.getQIndex(true, seg, 0)
		fh.losslessArray[seg] = q == 0 && fh.dqYDc == 0 && fh.dqUAc == 0 && fh.dqUDc == 0 && fh.dqVAc == 0 && fh.dqVDc == 0
		if !fh.losslessArray[seg] {
			fh.codedLossless = false
		}
		if fh.usingQmatrix {
			if fh.losslessArray[seg] {
				fh.segQMLevel[0][seg], fh.segQMLevel[1][seg], fh.segQMLevel[2][seg] = 15, 15, 15
			} else {
				fh.segQMLevel[0][seg], fh.segQMLevel[1][seg], fh.segQMLevel[2][seg] = fh.qmY, fh.qmU, fh.qmV
			}
		}
	}
	fh.allLossless = fh.codedLossless && fh.frameWidth == fh.upscaledWidth
	d.loopFilterParams(b, fh)
	d.cdefParams(b, fh)
	d.lrParams(b, fh)
	// read_tx_mode
	if fh.codedLossless {
		fh.txMode = only4x4
	} else if b.flag() {
		fh.txMode = txModeSelect
	} else {
		fh.txMode = txModeLargest
	}
	// frame_reference_mode
	if !fh.frameIsIntra {
		fh.referenceSelect = b.flag()
	}
	d.skipModeParams(b, fh)
	if fh.frameIsIntra || fh.errorRes || !s.enableWarped {
		fh.allowWarped = false
	} else {
		fh.allowWarped = b.flag()
	}
	fh.reducedTxSet = b.flag()
	d.globalMotionParams(b, fh)
	d.filmGrainParams(b, fh)
	fh.hdrBits = b.pos
	if b.err {
		return nil, errBitstream
	}
	return fh, nil
}

// getQIndex implements get_qindex (spec 7.12.2).
func (fh *frameHdr) getQIndex(ignoreDeltaQ bool, seg int, curQIndex int) int {
	if fh.segEnabled && fh.featEnabled[seg][segLvlAltQ] {
		data := fh.featData[seg][segLvlAltQ]
		q := fh.baseQIdx + data
		if !ignoreDeltaQ && fh.deltaQPresent {
			q = curQIndex + data
		}
		return clip3(0, 255, q)
	}
	if !ignoreDeltaQ && fh.deltaQPresent {
		return curQIndex
	}
	return fh.baseQIdx
}

func (d *Decoder) superresParams(b *bitReader, fh *frameHdr) {
	s := d.seq
	fh.useSuperres = false
	if s.enableSuperres {
		fh.useSuperres = b.flag()
	}
	if fh.useSuperres {
		fh.superresDenom = b.f(superresDenomBits) + superresDenomMin
	} else {
		fh.superresDenom = superresNum
	}
	fh.upscaledWidth = fh.frameWidth
	fh.frameWidth = (fh.upscaledWidth*superresNum + fh.superresDenom/2) / fh.superresDenom
}

func (fh *frameHdr) computeImageSize() {
	fh.miCols = 2 * ((fh.frameWidth + 7) >> 3)
	fh.miRows = 2 * ((fh.frameHeight + 7) >> 3)
}

func (d *Decoder) frameSize(b *bitReader, fh *frameHdr) {
	s := d.seq
	if fh.sizeOverride {
		fh.frameWidth = b.f(s.frameWidthBitsM1+1) + 1
		fh.frameHeight = b.f(s.frameHeightBitsM1+1) + 1
	} else {
		fh.frameWidth = s.maxFrameW
		fh.frameHeight = s.maxFrameH
	}
	d.superresParams(b, fh)
	fh.computeImageSize()
}

func (d *Decoder) renderSize(b *bitReader, fh *frameHdr) {
	if b.flag() {
		fh.renderWidth = b.f(16) + 1
		fh.renderHeight = b.f(16) + 1
	} else {
		fh.renderWidth = fh.upscaledWidth
		fh.renderHeight = fh.frameHeight
	}
}

func (d *Decoder) frameSizeWithRefs(b *bitReader, fh *frameHdr) {
	found := false
	for i := 0; i < refsPerFrame; i++ {
		if b.flag() {
			r := d.ref[fh.refFrameIdx[i]]
			fh.upscaledWidth = r.upscaledWidth
			fh.frameWidth = fh.upscaledWidth
			fh.frameHeight = r.frameHeight
			fh.renderWidth = r.renderWidth
			fh.renderHeight = r.renderHeight
			found = true
			break
		}
	}
	if !found {
		d.frameSize(b, fh)
		d.renderSize(b, fh)
	} else {
		d.superresParams(b, fh)
		fh.computeImageSize()
	}
}

func tileLog2(blk, target int) int {
	k := 0
	for (blk << uint(k)) < target {
		k++
	}
	return k
}

func (d *Decoder) tileInfo(b *bitReader, fh *frameHdr) {
	s := d.seq
	var sbCols, sbRows, sbShift int
	if s.use128 {
		sbCols = (fh.miCols + 31) >> 5
		sbRows = (fh.miRows + 31) >> 5
		sbShift = 5
	} else {
		sbCols = (fh.miCols + 15) >> 4
		sbRows = (fh.miRows + 15) >> 4
		sbShift = 4
	}
	sbSize := sbShift + 2
	maxTileWidthSb := maxTileWidth >> uint(sbSize)
	maxTileAreaSb := maxTileArea >> uint(2*sbSize)
	minLog2TileCols := tileLog2(maxTileWidthSb, sbCols)
	maxLog2TileCols := tileLog2(1, imin(sbCols, maxTileCols))
	maxLog2TileRows := tileLog2(1, imin(sbRows, maxTileRows))
	minLog2Tiles := imax(minLog2TileCols, tileLog2(maxTileAreaSb, sbRows*sbCols))
	if b.flag() { // uniform_tile_spacing_flag
		fh.tileColsLog2 = minLog2TileCols
		for fh.tileColsLog2 < maxLog2TileCols {
			if b.flag() {
				fh.tileColsLog2++
			} else {
				break
			}
		}
		tileWidthSb := (sbCols + (1 << uint(fh.tileColsLog2)) - 1) >> uint(fh.tileColsLog2)
		i := 0
		for startSb := 0; startSb < sbCols; startSb += tileWidthSb {
			fh.miColStarts[i] = startSb << uint(sbShift)
			i++
		}
		fh.miColStarts[i] = fh.miCols
		fh.tileCols = i
		minLog2TileRows := imax(minLog2Tiles-fh.tileColsLog2, 0)
		fh.tileRowsLog2 = minLog2TileRows
		for fh.tileRowsLog2 < maxLog2TileRows {
			if b.flag() {
				fh.tileRowsLog2++
			} else {
				break
			}
		}
		tileHeightSb := (sbRows + (1 << uint(fh.tileRowsLog2)) - 1) >> uint(fh.tileRowsLog2)
		i = 0
		for startSb := 0; startSb < sbRows; startSb += tileHeightSb {
			fh.miRowStarts[i] = startSb << uint(sbShift)
			i++
		}
		fh.miRowStarts[i] = fh.miRows
		fh.tileRows = i
	} else {
		cov(covNonUniformTiles)
		widestTileSb := 0
		startSb := 0
		i := 0
		for ; startSb < sbCols && i < maxTileCols; i++ {
			fh.miColStarts[i] = startSb << uint(sbShift)
			maxWidth := imin(sbCols-startSb, maxTileWidthSb)
			sizeSb := b.ns(maxWidth) + 1
			widestTileSb = imax(sizeSb, widestTileSb)
			startSb += sizeSb
		}
		fh.miColStarts[i] = fh.miCols
		fh.tileCols = i
		fh.tileColsLog2 = tileLog2(1, fh.tileCols)
		if minLog2Tiles > 0 {
			maxTileAreaSb = (sbRows * sbCols) >> uint(minLog2Tiles+1)
		} else {
			maxTileAreaSb = sbRows * sbCols
		}
		maxTileHeightSb := imax(maxTileAreaSb/imax(widestTileSb, 1), 1)
		startSb = 0
		i = 0
		for ; startSb < sbRows && i < maxTileRows; i++ {
			fh.miRowStarts[i] = startSb << uint(sbShift)
			maxHeight := imin(sbRows-startSb, maxTileHeightSb)
			sizeSb := b.ns(maxHeight) + 1
			startSb += sizeSb
		}
		fh.miRowStarts[i] = fh.miRows
		fh.tileRows = i
		fh.tileRowsLog2 = tileLog2(1, fh.tileRows)
	}
	if fh.tileColsLog2 > 0 || fh.tileRowsLog2 > 0 {
		fh.ctxUpdateTileID = b.f(fh.tileRowsLog2 + fh.tileColsLog2)
		fh.tileSizeBytes = b.f(2) + 1
	}
}

func (b *bitReader) readDeltaQ() int {
	if b.flag() {
		return b.su(7)
	}
	return 0
}

func (d *Decoder) quantParams(b *bitReader, fh *frameHdr) {
	s := d.seq
	fh.baseQIdx = b.f(8)
	fh.dqYDc = b.readDeltaQ()
	if s.numPlanes > 1 {
		diffUV := false
		if s.separateUVDeltaQ {
			diffUV = b.flag()
		}
		fh.dqUDc = b.readDeltaQ()
		fh.dqUAc = b.readDeltaQ()
		if diffUV {
			fh.dqVDc = b.readDeltaQ()
			fh.dqVAc = b.readDeltaQ()
		} else {
			fh.dqVDc = fh.dqUDc
			fh.dqVAc = fh.dqUAc
		}
	}
	fh.usingQmatrix = b.flag()
	if fh.usingQmatrix {
		fh.qmY = b.f(4)
		fh.qmU = b.f(4)
		if !s.separateUVDeltaQ {
			fh.qmV = fh.qmU
		} else {
			fh.qmV = b.f(4)
		}
	}
}

func (d *Decoder) segmentationParams(b *bitReader, fh *frameHdr) {
	fh.segEnabled = b.flag()
	if fh.segEnabled {
		if fh.primaryRef == primaryRefNone {
			fh.segUpdateMap = true
			fh.segTemporalUpd = false
			fh.segUpdateData = true
		} else {
			fh.segUpdateMap = b.flag()
			if fh.segUpdateMap {
				fh.segTemporalUpd = b.flag()
			}
			fh.segUpdateData = b.flag()
		}
		if fh.segUpdateData {
			for i := 0; i < maxSegments; i++ {
				for j := 0; j < segLvlMax; j++ {
					en := b.flag()
					fh.featEnabled[i][j] = en
					clipped := 0
					if en {
						bits := segmentationFeatureBits[j]
						limit := segmentationFeatureMax[j]
						if segmentationFeatureSigned[j] == 1 {
							clipped = clip3(-limit, limit, b.su(1+bits))
						} else {
							clipped = clip3(0, limit, b.f(bits))
						}
					}
					fh.featData[i][j] = clipped
				}
			}
		}
	} else {
		for i := 0; i < maxSegments; i++ {
			for j := 0; j < segLvlMax; j++ {
				fh.featEnabled[i][j] = false
				fh.featData[i][j] = 0
			}
		}
	}
	fh.segIDPreSkip = false
	fh.lastActiveSegID = 0
	for i := 0; i < maxSegments; i++ {
		for j := 0; j < segLvlMax; j++ {
			if fh.featEnabled[i][j] {
				fh.lastActiveSegID = i
				if j >= segLvlRefFrame {
					fh.segIDPreSkip = true
				}
			}
		}
	}
}

func (d *Decoder) loopFilterParams(b *bitReader, fh *frameHdr) {
	s := d.seq
	if fh.codedLossless || fh.allowIntrabc {
		fh.lfLevel[0], fh.lfLevel[1] = 0, 0
		fh.lfRefDeltas = [totalRefsPerFrame]int{1, 0, 0, 0, -1, 0, -1, -1}
		fh.lfModeDeltas = [2]int{}
		return
	}
	fh.lfLevel[0] = b.f(6)
	fh.lfLevel[1] = b.f(6)
	if s.numPlanes > 1 {
		if fh.lfLevel[0] != 0 || fh.lfLevel[1] != 0 {
			fh.lfLevel[2] = b.f(6)
			fh.lfLevel[3] = b.f(6)
		}
	}
	fh.lfSharpness = b.f(3)
	fh.lfDeltaEnable = b.flag()
	if fh.lfDeltaEnable {
		if b.flag() { // loop_filter_delta_update
			for i := 0; i < totalRefsPerFrame; i++ {
				if b.flag() {
					fh.lfRefDeltas[i] = b.su(7)
				}
			}
			for i := 0; i < 2; i++ {
				if b.flag() {
					fh.lfModeDeltas[i] = b.su(7)
				}
			}
		}
	}
}

func (d *Decoder) cdefParams(b *bitReader, fh *frameHdr) {
	s := d.seq
	if fh.codedLossless || fh.allowIntrabc || !s.enableCdef {
		fh.cdefBits = 0
		fh.cdefYPri[0], fh.cdefYSec[0], fh.cdefUVPri[0], fh.cdefUVSec[0] = 0, 0, 0, 0
		fh.cdefDamping = 3
		return
	}
	fh.cdefDamping = b.f(2) + 3
	fh.cdefBits = b.f(2)
	for i := 0; i < (1 << uint(fh.cdefBits)); i++ {
		fh.cdefYPri[i] = b.f(4)
		fh.cdefYSec[i] = b.f(2)
		if fh.cdefYSec[i] == 3 {
			fh.cdefYSec[i]++
		}
		if s.numPlanes > 1 {
			fh.cdefUVPri[i] = b.f(4)
			fh.cdefUVSec[i] = b.f(2)
			if fh.cdefUVSec[i] == 3 {
				fh.cdefUVSec[i]++
			}
		}
	}
}

func (d *Decoder) lrParams(b *bitReader, fh *frameHdr) {
	s := d.seq
	fh.usesLr = false
	if fh.allLossless || fh.allowIntrabc || !s.enableRestoration {
		return
	}
	usesChromaLr := false
	for i := 0; i < s.numPlanes; i++ {
		t := b.f(2)
		fh.lrType[i] = remapLrType[t]
		if fh.lrType[i] != restoreNone {
			fh.usesLr = true
			if i > 0 {
				usesChromaLr = true
			}
		}
	}
	if fh.usesLr {
		shift := 0
		if s.use128 {
			shift = b.f(1) + 1
		} else {
			shift = b.f(1)
			if shift != 0 {
				shift += b.f(1)
			}
		}
		fh.lrUnitSize[0] = 256 >> uint(2-shift)
		uvShift := 0
		if s.subX != 0 && s.subY != 0 && usesChromaLr {
			uvShift = b.f(1)
		}
		fh.lrUnitSize[1] = fh.lrUnitSize[0] >> uint(uvShift)
		fh.lrUnitSize[2] = fh.lrUnitSize[0] >> uint(uvShift)
	}
}

func (d *Decoder) skipModeParams(b *bitReader, fh *frameHdr) {
	s := d.seq
	allowed := false
	if !(fh.frameIsIntra || !fh.referenceSelect || !s.enableOrderHint) {
		forwardIdx, backwardIdx := -1, -1
		forwardHint, backwardHint := 0, 0
		for i := 0; i < refsPerFrame; i++ {
			refHint := d.refOrderHint[fh.refFrameIdx[i]]
			if s.relDist(refHint, fh.orderHint) < 0 {
				if forwardIdx < 0 || s.relDist(refHint, forwardHint) > 0 {
					forwardIdx = i
					forwardHint = refHint
				}
			} else if s.relDist(refHint, fh.orderHint) > 0 {
				if backwardIdx < 0 || s.relDist(refHint, backwardHint) < 0 {
					backwardIdx = i
					backwardHint = refHint
				}
			}
		}
		if forwardIdx < 0 {
			allowed = false
		} else if backwardIdx >= 0 {
			allowed = true
			fh.skipModeFrame[0] = lastFrame + imin(forwardIdx, backwardIdx)
			fh.skipModeFrame[1] = lastFrame + imax(forwardIdx, backwardIdx)
		} else {
			secondIdx := -1
			secondHint := 0
			for i := 0; i < refsPerFrame; i++ {
				refHint := d.refOrderHint[fh.refFrameIdx[i]]
				if s.relDist(refHint, forwardHint) < 0 {
					if secondIdx < 0 || s.relDist(refHint, secondHint) > 0 {
						secondIdx = i
						secondHint = refHint
					}
				}
			}
			if secondIdx >= 0 {
				allowed = true
				fh.skipModeFrame[0] = lastFrame + imin(forwardIdx, secondIdx)
				fh.skipModeFrame[1] = lastFrame + imax(forwardIdx, secondIdx)
			}
		}
	}
	if allowed {
		fh.skipModePresent = b.flag()
	}
}

func inverseRecenter(r, v int) int {
	if v > 2*r {
		return v
	} else if v&1 != 0 {
		return r - ((v + 1) >> 1)
	}
	return r + (v >> 1)
}

func (b *bitReader) decodeSubexp(numSyms int) int {
	i, mk, k := 0, 0, 3
	for {
		b2 := k
		if i != 0 {
			b2 = k + i - 1
		}
		a := 1 << uint(b2)
		if numSyms <= mk+3*a {
			return b.ns(numSyms-mk) + mk
		}
		if b.flag() {
			i++
			mk += a
		} else {
			return b.f(b2) + mk
		}
	}
}

func (b *bitReader) decodeUnsignedSubexpWithRef(mx, r int) int {
	v := b.decodeSubexp(mx)
	if (r << 1) <= mx {
		return inverseRecenter(r, v)
	}
	return mx - 1 - inverseRecenter(mx-1-r, v)
}

func (b *bitReader) decodeSignedSubexpWithRef(low, high, r int) int {
	return b.decodeUnsignedSubexpWithRef(high-low, r-low) + low
}

func (d *Decoder) globalMotionParams(b *bitReader, fh *frameHdr) {
	for r := lastFrame; r <= altrefFrame; r++ {
		fh.gmType[r] = gmIdentity
		for i := 0; i < 6; i++ {
			if i%3 == 2 {
				fh.gmParams[r][i] = 1 << warpedModelPrec
			} else {
				fh.gmParams[r][i] = 0
			}
		}
	}
	if fh.frameIsIntra {
		return
	}
	for r := lastFrame; r <= altrefFrame; r++ {
		typ := gmIdentity
		if b.flag() { // is_global
			if b.flag() { // is_rot_zoom
				typ = gmRotzoom
			} else if b.flag() {
				typ = gmTranslation
			} else {
				typ = gmAffine
			}
		}
		fh.gmType[r] = typ
		if typ >= gmRotzoom {
			d.readGlobalParam(b, fh, typ, r, 2)
			d.readGlobalParam(b, fh, typ, r, 3)
			if typ == gmAffine {
				d.readGlobalParam(b, fh, typ, r, 4)
				d.readGlobalParam(b, fh, typ, r, 5)
			} else {
				fh.gmParams[r][4] = -fh.gmParams[r][3]
				fh.gmParams[r][5] = fh.gmParams[r][2]
			}
		}
		if typ >= gmTranslation {
			d.readGlobalParam(b, fh, typ, r, 0)
			d.readGlobalParam(b, fh, typ, r, 1)
		}
	}
}

func (d *Decoder) readGlobalParam(b *bitReader, fh *frameHdr, typ, ref, idx int) {
	absBits := 12  // GM_ABS_ALPHA_BITS
	precBits := 15 // GM_ALPHA_PREC_BITS
	if idx < 2 {
		if typ == gmTranslation {
			hp := b2i(!fh.allowHighPrecMV)
			absBits = 9 - hp
			precBits = 3 - hp
		} else {
			absBits = 12
			precBits = 6
		}
	}
	precDiff := warpedModelPrec - precBits
	round, sub := 0, 0
	if idx%3 == 2 {
		round = 1 << warpedModelPrec
		sub = 1 << uint(precBits)
	}
	mx := 1 << uint(absBits)
	r := (fh.prevGmParams[ref][idx] >> uint(precDiff)) - sub
	fh.gmParams[ref][idx] = (b.decodeSignedSubexpWithRef(-mx, mx+1, r) << uint(precDiff)) + round
}

func (d *Decoder) filmGrainParams(b *bitReader, fh *frameHdr) {
	s := d.seq
	fg := &fh.film
	*fg = filmGrain{}
	if !s.filmGrainPresent || (!fh.showFrame && !fh.showable) {
		return
	}
	fg.apply = b.flag()
	if !fg.apply {
		return
	}
	fg.seed = b.f(16)
	if fh.frameType == interFrame {
		fg.update = b.flag()
	} else {
		fg.update = true
	}
	if !fg.update {
		idx := b.f(3)
		seed := fg.seed
		if r := d.ref[idx]; r != nil {
			*fg = r.film
		}
		fg.seed = seed
		return
	}
	fg.numYPoints = b.f(4)
	for i := 0; i < fg.numYPoints; i++ {
		fg.yValue[i] = b.f(8)
		fg.yScaling[i] = b.f(8)
	}
	if s.mono {
		fg.chromaFromLuma = false
	} else {
		fg.chromaFromLuma = b.flag()
	}
	if s.mono || fg.chromaFromLuma || (s.subX == 1 && s.subY == 1 && fg.numYPoints == 0) {
		fg.numCbPoints, fg.numCrPoints = 0, 0
	} else {
		fg.numCbPoints = b.f(4)
		for i := 0; i < fg.numCbPoints; i++ {
			fg.cbValue[i] = b.f(8)
			fg.cbScale[i] = b.f(8)
		}
		fg.numCrPoints = b.f(4)
		for i := 0; i < fg.numCrPoints; i++ {
			fg.crValue[i] = b.f(8)
			fg.crScale[i] = b.f(8)
		}
	}
	fg.scalingShift = b.f(2) + 8
	fg.arCoeffLag = b.f(2)
	numPosLuma := 2 * fg.arCoeffLag * (fg.arCoeffLag + 1)
	numPosChroma := numPosLuma
	if fg.numYPoints != 0 {
		numPosChroma = numPosLuma + 1
		for i := 0; i < numPosLuma; i++ {
			fg.arCoeffsY[i] = b.f(8) - 128
		}
	}
	if fg.chromaFromLuma || fg.numCbPoints != 0 {
		for i := 0; i < numPosChroma; i++ {
			fg.arCoeffsCb[i] = b.f(8) - 128
		}
	}
	if fg.chromaFromLuma || fg.numCrPoints != 0 {
		for i := 0; i < numPosChroma; i++ {
			fg.arCoeffsCr[i] = b.f(8) - 128
		}
	}
	fg.arCoeffShift = b.f(2) + 6
	fg.grainScaleShift = b.f(2)
	if fg.numCbPoints != 0 {
		fg.cbMult = b.f(8)
		fg.cbLumaMul = b.f(8)
		fg.cbOffset = b.f(9)
	}
	if fg.numCrPoints != 0 {
		fg.crMult = b.f(8)
		fg.crLumaMul = b.f(8)
		fg.crOffset = b.f(9)
	}
	fg.overlap = b.flag()
	fg.clipRestricted = b.flag()
}

// setFrameRefs implements the set frame refs process (spec 7.8).
func (d *Decoder) setFrameRefs(fh *frameHdr, lastIdx, goldIdx int) {
	s := d.seq
	var idx [refsPerFrame]int
	for i := range idx {
		idx[i] = -1
	}
	idx[lastFrame-lastFrame] = lastIdx
	idx[goldenFrame-lastFrame] = goldIdx
	var used [numRefFrames]bool
	used[lastIdx] = true
	used[goldIdx] = true
	curHint := 1 << uint(s.orderHintBits-1)
	var shifted [numRefFrames]int
	for i := 0; i < numRefFrames; i++ {
		shifted[i] = curHint + s.relDist(d.refOrderHint[i], fh.orderHint)
	}
	findLatestBackward := func() int {
		ref, latest := -1, 0
		for i := 0; i < numRefFrames; i++ {
			h := shifted[i]
			if !used[i] && h >= curHint && (ref < 0 || h >= latest) {
				ref, latest = i, h
			}
		}
		return ref
	}
	findEarliestBackward := func() int {
		ref, earliest := -1, 0
		for i := 0; i < numRefFrames; i++ {
			h := shifted[i]
			if !used[i] && h >= curHint && (ref < 0 || h < earliest) {
				ref, earliest = i, h
			}
		}
		return ref
	}
	findLatestForward := func() int {
		ref, latest := -1, 0
		for i := 0; i < numRefFrames; i++ {
			h := shifted[i]
			if !used[i] && h < curHint && (ref < 0 || h >= latest) {
				ref, latest = i, h
			}
		}
		return ref
	}
	if ref := findLatestBackward(); ref >= 0 {
		idx[altrefFrame-lastFrame] = ref
		used[ref] = true
	}
	if ref := findEarliestBackward(); ref >= 0 {
		idx[bwdrefFrame-lastFrame] = ref
		used[ref] = true
	}
	if ref := findEarliestBackward(); ref >= 0 {
		idx[altref2Frm-lastFrame] = ref
		used[ref] = true
	}
	for i := 0; i < refsPerFrame-2; i++ {
		rf := refFrameList[i]
		if idx[rf-lastFrame] < 0 {
			if ref := findLatestForward(); ref >= 0 {
				idx[rf-lastFrame] = ref
				used[ref] = true
			}
		}
	}
	ref, earliest := -1, 0
	for i := 0; i < numRefFrames; i++ {
		h := shifted[i]
		if ref < 0 || h < earliest {
			ref, earliest = i, h
		}
	}
	for i := 0; i < refsPerFrame; i++ {
		if idx[i] < 0 {
			idx[i] = ref
		}
	}
	fh.refFrameIdx = idx
}

var _ = errors.New
