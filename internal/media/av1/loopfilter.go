package av1

// ---- deblocking loop filter (spec 7.14) ---------------------------------------

func (f *frameDec) loopFilter() {
	fh := f.fh
	for plane := 0; plane < f.numPlanes; plane++ {
		if plane == 0 || fh.lfLevel[1+plane] != 0 {
			for pass := 0; pass < 2; pass++ {
				rowStep, colStep := 1, 1
				if plane > 0 {
					rowStep = 1 << uint(f.subY)
					colStep = 1 << uint(f.subX)
				}
				for row := 0; row < f.miRows; row += rowStep {
					for col := 0; col < f.miCols; col += colStep {
						f.loopFilterEdge(plane, pass, row, col)
					}
				}
			}
		}
	}
}

func (f *frameDec) filterLevel(m *miInfo, plane, pass int) int {
	fh := f.fh
	segment := int(m.segID)
	ref := int(m.refFrame[0])
	mode := int(m.yMode)
	modeType := 0
	if mode >= modeNearestMv && mode != modeGlobalMv && mode != modeGlobalGlobal {
		modeType = 1
	}
	i := pass
	if plane != 0 {
		i = plane + 1
	}
	var deltaLF int
	if !fh.deltaLfMulti {
		deltaLF = int(m.deltaLF[0])
	} else {
		deltaLF = int(m.deltaLF[i])
	}
	baseFilterLevel := clip3(0, maxLoopFilter, deltaLF+fh.lfLevel[i])
	lvlSeg := baseFilterLevel
	feature := segLvlAltLfYV + i
	if f.segFeatureActiveIdx(segment, feature) {
		lvlSeg = clip3(0, maxLoopFilter, fh.featData[segment][feature]+lvlSeg)
	}
	if fh.lfDeltaEnable {
		nShift := uint(lvlSeg >> 5)
		if ref == intraFrame {
			lvlSeg += fh.lfRefDeltas[intraFrame] << nShift
		} else if ref > intraFrame {
			lvlSeg += (fh.lfRefDeltas[ref] << nShift) + (fh.lfModeDeltas[modeType] << nShift)
		}
		lvlSeg = clip3(0, maxLoopFilter, lvlSeg)
	}
	return lvlSeg
}

func (f *frameDec) lfParams(lvl int) (limit, blimit, thresh int) {
	sharp := f.fh.lfSharpness
	shift := uint(0)
	if sharp > 4 {
		shift = 2
	} else if sharp > 0 {
		shift = 1
	}
	if sharp > 0 {
		limit = clip3(1, 9-sharp, lvl>>shift)
	} else {
		limit = imax(1, lvl>>shift)
	}
	blimit = 2*(lvl+2) + limit
	thresh = lvl >> 4
	return
}

func (f *frameDec) loopFilterEdge(plane, pass, row, col int) {
	subX, subY := 0, 0
	if plane > 0 {
		subX, subY = f.subX, f.subY
	}
	dx, dy := 1, 0
	if pass == 1 {
		dx, dy = 0, 1
	}
	x := col * 4
	y := row * 4
	row |= subY
	col |= subX
	if x >= f.fh.frameWidth || y >= f.fh.frameHeight {
		return
	}
	if pass == 0 && x == 0 {
		return
	}
	if pass == 1 && y == 0 {
		return
	}
	xP := x >> uint(subX)
	yP := y >> uint(subY)
	prevRow := row - (dy << uint(subY))
	prevCol := col - (dx << uint(subX))
	m := f.miAt(row, col)
	miSize := int(m.miSize)
	txSz := int(f.lfTxSize[plane][(row>>uint(subY))*f.miCols+(col>>uint(subX))])
	planeSize := getPlaneResidualSize(miSize, plane, f.subX, f.subY)
	skip := m.skip
	isIntra := m.refFrame[0] <= intraFrame
	prevTxSz := int(f.lfTxSize[plane][(prevRow>>uint(subY))*f.miCols+(prevCol>>uint(subX))])
	var isBlockEdge, isTxEdge bool
	if pass == 0 {
		isBlockEdge = xP%blockWidth(planeSize) == 0
		isTxEdge = xP%txWidth[txSz] == 0
	} else {
		isBlockEdge = yP%blockHeight(planeSize) == 0
		isTxEdge = yP%txHeight[txSz] == 0
	}
	if !isTxEdge {
		return
	}
	if !(isBlockEdge || !skip || isIntra) {
		return
	}
	var baseSize int
	if pass == 0 {
		baseSize = imin(txWidth[prevTxSz], txWidth[txSz])
	} else {
		baseSize = imin(txHeight[prevTxSz], txHeight[txSz])
	}
	filterSize := imin(16, baseSize)
	if plane > 0 {
		filterSize = imin(8, baseSize)
	}
	lvl := f.filterLevel(m, plane, pass)
	if lvl == 0 {
		lvl = f.filterLevel(f.miAt(prevRow, prevCol), plane, pass)
	}
	if lvl == 0 {
		return
	}
	limit, blimit, thresh := f.lfParams(lvl)
	pl := f.cur.pl[plane]
	stride := f.cur.stride[plane]
	step := 1
	along := stride
	if pass == 1 {
		step = stride
		along = 1
	}
	pos := yP*stride + xP
	for i := 0; i < 4; i++ {
		f.filterSample(pl, pos+i*along, step, limit, blimit, thresh, filterSize, plane)
	}
}

func (f *frameDec) filterSample(pl []uint16, pos, step, limit, blimit, thresh, filterSize, plane int) {
	bd := uint(f.bitDepth)
	q0 := int(pl[pos])
	q1 := int(pl[pos+step])
	p0 := int(pl[pos-step])
	p1 := int(pl[pos-2*step])
	threshBd := thresh << (bd - 8)
	hev := abs(p1-p0) > threshBd || abs(q1-q0) > threshBd
	filterLen := 16
	if filterSize == 4 {
		filterLen = 4
	} else if plane != 0 {
		filterLen = 6
	} else if filterSize == 8 {
		filterLen = 8
	}
	limitBd := limit << (bd - 8)
	blimitBd := blimit << (bd - 8)
	if abs(p1-p0) > limitBd || abs(q1-q0) > limitBd || abs(p0-q0)*2+abs(p1-q1)/2 > blimitBd {
		return
	}
	var p2, q2, p3, q3 int
	if filterLen >= 6 {
		q2 = int(pl[pos+2*step])
		p2 = int(pl[pos-3*step])
		if abs(p2-p1) > limitBd || abs(q2-q1) > limitBd {
			return
		}
	}
	if filterLen >= 8 {
		q3 = int(pl[pos+3*step])
		p3 = int(pl[pos-4*step])
		if abs(p3-p2) > limitBd || abs(q3-q2) > limitBd {
			return
		}
	}
	thresholdBd := 1 << (bd - 8)
	flat := false
	if filterSize >= 8 {
		flat = !(abs(p1-p0) > thresholdBd || abs(q1-q0) > thresholdBd || abs(p2-p0) > thresholdBd || abs(q2-q0) > thresholdBd)
		if flat && filterLen >= 8 {
			flat = !(abs(p3-p0) > thresholdBd || abs(q3-q0) > thresholdBd)
		}
	}
	flat2 := false
	if filterSize >= 16 {
		q4 := int(pl[pos+4*step])
		q5 := int(pl[pos+5*step])
		q6 := int(pl[pos+6*step])
		p4 := int(pl[pos-5*step])
		p5 := int(pl[pos-6*step])
		p6 := int(pl[pos-7*step])
		flat2 = !(abs(p6-p0) > thresholdBd || abs(q6-q0) > thresholdBd || abs(p5-p0) > thresholdBd ||
			abs(q5-q0) > thresholdBd || abs(p4-p0) > thresholdBd || abs(q4-q0) > thresholdBd)
	}
	if filterSize == 4 || !flat {
		// narrow filter
		off := 0x80 << (bd - 8)
		lo := -(1 << (bd - 1))
		hi := (1 << (bd - 1)) - 1
		c := func(v int) int { return clip3(lo, hi, v) }
		ps1 := p1 - off
		ps0 := p0 - off
		qs0 := q0 - off
		qs1 := q1 - off
		filter := 0
		if hev {
			filter = c(ps1 - qs1)
		}
		filter = c(filter + 3*(qs0-ps0))
		filter1 := c(filter+4) >> 3
		filter2 := c(filter+3) >> 3
		pl[pos] = uint16(c(qs0-filter1) + off)
		pl[pos-step] = uint16(c(ps0+filter2) + off)
		if !hev {
			filter = round2(filter1, 1)
			pl[pos+step] = uint16(c(qs1-filter) + off)
			pl[pos-2*step] = uint16(c(ps1+filter) + off)
		}
		return
	}
	log2Size := uint(4)
	if filterSize == 8 || !flat2 {
		log2Size = 3
	}
	var n, n2 int
	if log2Size == 4 {
		n = 6
	} else if plane == 0 {
		n = 3
	} else {
		n = 2
	}
	if log2Size == 3 && plane == 0 {
		n2 = 0
	} else {
		n2 = 1
	}
	var F [12]int
	for i := -n; i < n; i++ {
		t := 0
		for j := -n; j <= n; j++ {
			p := clip3(-(n + 1), n, i+j)
			tap := 1
			if abs(j) <= n2 {
				tap = 2
			}
			t += int(pl[pos+p*step]) * tap
		}
		F[i+n] = round2(t, log2Size)
	}
	for i := -n; i < n; i++ {
		pl[pos+i*step] = uint16(F[i+n])
	}
}
