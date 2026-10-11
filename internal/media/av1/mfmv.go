package av1

// ---- motion field estimation (spec 7.9) ------------------------------------------

func getMvProjection(mv mvec, numerator, denominator int) mvec {
	clippedDen := imin(31, denominator)
	clippedNum := clip3(-31, 31, numerator)
	proj := func(v int16) int16 {
		scaled := round2Signed(int(v)*clippedNum*divMult[clippedDen], 14)
		return int16(clip3(-(1<<14)+1, (1<<14)-1, scaled))
	}
	return mvec{proj(mv.row), proj(mv.col)}
}

func mfProject(v8, delta, dstSign, max8, maxOff8 int) (int, bool) {
	base8 := (v8 >> 3) << 3
	var offset8 int
	if delta >= 0 {
		offset8 = delta >> 6
	} else {
		offset8 = -((-delta) >> 6)
	}
	v8 += dstSign * offset8
	if v8 < 0 || v8 >= max8 || v8 < base8-maxOff8 || v8 >= base8+8+maxOff8 {
		return v8, false
	}
	return v8, true
}

func (f *frameDec) motionFieldEstimation() {
	fh := f.fh
	s := f.seq
	cov(covMfmv)
	w8 := f.miCols >> 1
	h8 := f.miRows >> 1
	for ref := lastFrame; ref <= altrefFrame; ref++ {
		m := make([]mvec, w8*h8)
		for i := range m {
			m[i] = mvec{invalidMv, invalidMv}
		}
		f.motionFieldMvs[ref] = m
	}
	lastIdx := fh.refFrameIdx[0]
	curGoldOrderHint := fh.orderHints[goldenFrame]
	lastAltOrderHint := f.d.ref[lastIdx].savedOrderHints[altrefFrame]
	if lastAltOrderHint != curGoldOrderHint {
		f.mfProjection(lastFrame, -1)
	}
	refStamp := 3 - 2
	if s.relDist(fh.orderHints[bwdrefFrame], fh.orderHint) > 0 {
		if f.mfProjection(bwdrefFrame, 1) {
			refStamp--
		}
	}
	if s.relDist(fh.orderHints[altref2Frm], fh.orderHint) > 0 {
		if f.mfProjection(altref2Frm, 1) {
			refStamp--
		}
	}
	if s.relDist(fh.orderHints[altrefFrame], fh.orderHint) > 0 && refStamp >= 0 {
		if f.mfProjection(altrefFrame, 1) {
			refStamp--
		}
	}
	if refStamp >= 0 {
		f.mfProjection(last2Frame, -1)
	}
}

func (f *frameDec) mfProjection(src, dstSign int) bool {
	fh := f.fh
	s := f.seq
	srcIdx := fh.refFrameIdx[src-lastFrame]
	r := f.d.ref[srcIdx]
	w8 := f.miCols >> 1
	h8 := f.miRows >> 1
	if r.miRows != f.miRows || r.miCols != f.miCols || r.frameType == intraOnlyFrame || r.frameType == keyFrame {
		return false
	}
	for y8 := 0; y8 < h8; y8++ {
		for x8 := 0; x8 < w8; x8++ {
			row := 2*y8 + 1
			col := 2*x8 + 1
			srcRef := int(r.savedRefFrames[row*f.miCols+col])
			if srcRef <= intraFrame {
				continue
			}
			refToCur := s.relDist(fh.orderHints[src], fh.orderHint)
			refOffset := s.relDist(fh.orderHints[src], r.savedOrderHints[srcRef])
			posValid := abs(refToCur) <= 31 && abs(refOffset) <= 31 && refOffset > 0
			if !posValid {
				continue
			}
			mv := r.savedMvs[row*f.miCols+col]
			projMv := getMvProjection(mv, refToCur*dstSign, refOffset)
			posY8, ok1 := mfProject(y8, int(projMv.row), dstSign, f.miRows>>1, 0)
			posX8, ok2 := mfProject(x8, int(projMv.col), dstSign, f.miCols>>1, 8)
			if !ok1 || !ok2 {
				continue
			}
			for dst := lastFrame; dst <= altrefFrame; dst++ {
				refToDst := s.relDist(fh.orderHint, fh.orderHints[dst])
				f.motionFieldMvs[dst][posY8*w8+posX8] = getMvProjection(mv, refToDst, refOffset)
			}
		}
	}
	return true
}
