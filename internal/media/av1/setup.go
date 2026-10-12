package av1

import "errors"

func alignUp(x, a int) int { return (x + a - 1) &^ (a - 1) }

var errRefMismatch = errors.New("av1: reference frame mismatch")

// setupFrame allocates the per-frame decoding state after a frame header has
// been parsed (and runs the header-time processes of 5.9.2).
func (d *Decoder) setupFrame(fh *frameHdr) error {
	s := d.seq
	if fh.frameWidth <= 0 || fh.frameHeight <= 0 || fh.upscaledWidth > 65536 || fh.frameHeight > 65536 ||
		fh.miCols*fh.miRows > 1<<24 {
		return ErrUnsupported
	}
	f := &frameDec{d: d, seq: s, fh: fh}
	f.bitDepth = s.bitDepth
	f.subX, f.subY = s.subX, s.subY
	f.numPlanes = s.numPlanes
	f.miCols, f.miRows = fh.miCols, fh.miRows
	if s.use128 {
		f.sbSize4, f.sbMask = 32, 31
	} else {
		f.sbSize4, f.sbMask = 16, 15
	}
	w := alignUp(f.miCols*4, 128) + 32
	h := alignUp(f.miRows*4, 128) + 32
	f.cur = newFrameBuf(w, h, f.subX, f.subY, f.numPlanes)
	n := f.miRows * f.miCols
	f.mi = make([]miInfo, n)
	for i := range f.mi {
		f.mi[i].refFrame = [2]int8{-2, -2}
	}
	f.txTypes = make([]uint8, n)
	for p := 0; p < f.numPlanes; p++ {
		f.lfTxSize[p] = make([]uint8, n)
		f.aboveLevelCtx[p] = make([]uint8, f.miCols+64)
		f.aboveDcCtx[p] = make([]uint8, f.miCols+64)
		f.leftLevelCtx[p] = make([]uint8, f.miRows+64)
		f.leftDcCtx[p] = make([]uint8, f.miRows+64)
	}
	f.aboveSegPred = make([]uint8, f.miCols+64)
	f.leftSegPred = make([]uint8, f.miRows+64)
	f.cdefStride = (f.miCols + 15) >> 4
	f.cdefIdx = make([]int8, ((f.miRows+15)>>4)*f.cdefStride)
	for i := range f.cdefIdx {
		f.cdefIdx[i] = -1
	}
	f.cdf = *d.hdrCDF

	// segmentation map from the primary reference frame
	f.prevSegIDs = make([]uint8, n)
	if pr := d.prevSegIDsFor; pr != nil && fh.segEnabled && pr.miCols == f.miCols && pr.miRows == f.miRows && pr.segIDs != nil {
		copy(f.prevSegIDs, pr.segIDs)
	}

	// loop restoration units
	if fh.usesLr {
		for p := 0; p < f.numPlanes; p++ {
			if fh.lrType[p] == restoreNone {
				continue
			}
			subX, subY := 0, 0
			if p > 0 {
				subX, subY = f.subX, f.subY
			}
			unitSize := fh.lrUnitSize[p]
			f.lrUnitRows[p] = countUnitsInFrame(unitSize, round2(fh.frameHeight, uint(subY)))
			f.lrUnitCols[p] = countUnitsInFrame(unitSize, round2(fh.upscaledWidth, uint(subX)))
			cnt := f.lrUnitRows[p] * f.lrUnitCols[p]
			f.lrType[p] = make([]uint8, cnt)
			f.lrWiener[p] = make([][2][3]int8, cnt)
			f.lrSgrSet[p] = make([]uint8, cnt)
			f.lrSgrXqd[p] = make([][2]int8, cnt)
		}
	}

	// reference frames
	if !fh.frameIsIntra {
		for i := 0; i < refsPerFrame; i++ {
			r := d.ref[fh.refFrameIdx[i]]
			if r == nil || r.buf == nil {
				return errRefMismatch
			}
			if r.bitDepth != f.bitDepth || r.subX != f.subX || r.subY != f.subY || r.mono != s.mono {
				return errRefMismatch
			}
			rf := lastFrame + i
			f.refBufs[rf] = r
			f.refScaleX[rf] = ((r.upscaledWidth << 14) + fh.frameWidth/2) / fh.frameWidth
			f.refScaleY[rf] = ((r.frameHeight << 14) + fh.frameHeight/2) / fh.frameHeight
			if 2*fh.frameWidth < r.upscaledWidth || 2*fh.frameHeight < r.frameHeight ||
				fh.frameWidth > 16*r.upscaledWidth || fh.frameHeight > 16*r.frameHeight {
				return errRefMismatch
			}
		}
		if fh.useRefFrameMvs {
			f.motionFieldEstimation()
		}
	}
	if fh.segEnabled {
		cov(covSegmentation)
	}
	if fh.deltaQPresent {
		cov(covDeltaQ)
	}
	if fh.deltaLfPresent {
		cov(covDeltaLF)
	}
	if fh.tileCols*fh.tileRows > 1 {
		cov(covTiles)
	}
	d.fd = f
	return nil
}

func countUnitsInFrame(unitSize, frameSize int) int {
	return imax((frameSize+(unitSize>>1))/unitSize, 1)
}

// finishFrame runs the decode frame wrapup process (spec 7.4).
func (d *Decoder) finishFrame() error {
	f := d.fd
	fh := f.fh
	// frame end CDF update
	finalCdf := f.cdf
	if !fh.disableFrameEndUpd && f.savedCdf != nil {
		finalCdf = *f.savedCdf
	}
	lr := f.postFilter()
	// motion vector storage (7.19)
	n := f.miRows * f.miCols
	rf := &refFrame{
		upscaledWidth: fh.upscaledWidth,
		frameWidth:    fh.frameWidth,
		frameHeight:   fh.frameHeight,
		renderWidth:   fh.renderWidth,
		renderHeight:  fh.renderHeight,
		miCols:        f.miCols,
		miRows:        f.miRows,
		frameType:     fh.frameType,
		subX:          f.subX,
		subY:          f.subY,
		bitDepth:      f.bitDepth,
		orderHint:     fh.orderHint,
		frameID:       fh.currentFrameID,
		buf:           lr,
		gmParams:      fh.gmParams,
		film:          fh.film,
		lfRefDeltas:   fh.lfRefDeltas,
		lfModeDeltas:  fh.lfModeDeltas,
		featEnabled:   fh.featEnabled,
		featData:      fh.featData,
		showable:      fh.showable,
		mono:          d.seq.mono,
		spatialID:     d.spatialID,
		temporalID:    d.temporalID,
	}
	rf.savedOrderHints = fh.orderHints
	cdfCopy := finalCdf
	rf.cdf = &cdfCopy
	rf.savedRefFrames = make([]int8, n)
	rf.savedMvs = make([]mvec, n)
	if !fh.frameIsIntra || true {
		for row := 0; row < f.miRows; row++ {
			for col := 0; col < f.miCols; col++ {
				idx := row*f.miCols + col
				rf.savedRefFrames[idx] = refNone
				m := &f.mi[idx]
				for list := 0; list < 2; list++ {
					r := int(m.refFrame[list])
					if r > intraFrame {
						refIdx := fh.refFrameIdx[r-lastFrame]
						dist := d.seq.relDist(d.refOrderHint[refIdx], fh.orderHint)
						if dist < 0 {
							mvRow := int(m.mv[list].row)
							mvCol := int(m.mv[list].col)
							if abs(mvRow) <= 4095 && abs(mvCol) <= 4095 {
								rf.savedRefFrames[idx] = int8(r)
								rf.savedMvs[idx] = m.mv[list]
							}
						}
					}
				}
			}
		}
	}
	// segment ids
	rf.segIDs = make([]uint8, n)
	if fh.segEnabled && !fh.segUpdateMap {
		copy(rf.segIDs, f.prevSegIDs)
	} else {
		for i := range rf.segIDs {
			rf.segIDs[i] = f.mi[i].segID
		}
	}
	// reference frame update (7.20)
	for i := 0; i < numRefFrames; i++ {
		if (fh.refreshFlags>>uint(i))&1 != 0 {
			d.ref[i] = rf
			d.refValid[i] = true
			d.refFrameID[i] = fh.currentFrameID
			d.refOrderHint[i] = fh.orderHint
		}
	}
	if !d.LargeScale {
		d.recordAnchor(rf)
	}
	if fh.showFrame {
		d.output(rf, &fh.film)
	}
	return nil
}

// showExistingFrame handles show_existing_frame (spec 7.21 + 7.18).
func (d *Decoder) showExistingFrame(fh *frameHdr) error {
	r := d.ref[fh.frameToShow]
	if r == nil || r.buf == nil {
		return errBitstream
	}
	film := fh.film
	d.output(r, &film)
	if r.frameType == keyFrame {
		// reference frame loading process then refresh of all slots
		for i := 0; i < numRefFrames; i++ {
			d.ref[i] = r
			d.refValid[i] = true
			d.refOrderHint[i] = r.orderHint
			d.refFrameID[i] = r.frameID
		}
		d.orderHints = r.savedOrderHints
	}
	return nil
}
