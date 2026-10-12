package hevc

// Slice data parsing: coding tree units, coding quadtree, coding units and
// transform trees. Reconstruction is interleaved with parsing.

const (
	part2Nx2N = iota
	part2NxN
	partNx2N
	partNxN
	part2NxnU
	part2NxnD
	partnLx2N
	partnRx2N
)

func (d *Decoder) allocTables(s *SPS) {
	d.tabSPS = s
	d.cuTab = make([]cuInfo, s.wMinCb*s.hMinCb)
	d.ipm = make([]uint8, s.wTb4*s.hTb4)
	d.edge = make([]uint8, s.wTb4*s.hTb4)
	d.cbfTab = make([]uint8, s.wTb4*s.hTb4)
	n := s.wCtb * s.hCtb
	d.ctbSliceIdx = make([]int32, n)
	d.ctbSliceAddr = make([]int32, n)
	d.sao = make([]saoParams, n)
}

func (d *Decoder) resetPictureTables() {
	for i := range d.cuTab {
		d.cuTab[i] = cuInfo{}
	}
	for i := range d.edge {
		d.edge[i] = 0
		d.cbfTab[i] = 0
	}
	for i := range d.ctbSliceIdx {
		d.ctbSliceIdx[i] = -1
		d.ctbSliceAddr[i] = -1
		d.sao[i] = saoParams{}
	}
}

func (d *Decoder) cuAt(x, y int) *cuInfo {
	s := d.sp
	return &d.cuTab[(y>>uint(s.log2MinCb))*s.wMinCb+x>>uint(s.log2MinCb)]
}

// availZ is the z-scan order availability process (6.4.1).
func (d *Decoder) availZ(xC, yC, xN, yN int) bool {
	s := d.sp
	if xN < 0 || yN < 0 || xN >= s.width || yN >= s.height {
		return false
	}
	p := d.pp
	sh := uint(s.log2MinTb)
	if p.minTbZs[(yN>>sh)*p.zsW+(xN>>sh)] > p.minTbZs[(yC>>sh)*p.zsW+(xC>>sh)] {
		return false
	}
	l := uint(s.log2Ctb)
	cn := (yN>>l)*s.wCtb + xN>>l
	cc := (yC>>l)*s.wCtb + xC>>l
	if cn == cc {
		return true
	}
	if d.ctbSliceAddr[cn] != d.ctbSliceAddr[cc] || d.ctbSliceAddr[cn] < 0 {
		return false
	}
	return p.tileRs[cn] == p.tileRs[cc]
}

func (d *Decoder) initContexts() {
	sh := d.sh
	initType := 0
	switch sh.sliceType {
	case sliceP:
		initType = 1
		if sh.cabacInit {
			initType = 2
		}
	case sliceB:
		initType = 2
		if sh.cabacInit {
			initType = 1
		}
	}
	d.cab.initContexts(initType, sh.sliceQp)
	d.statCoeff = [4]uint8{}
}

func (d *Decoder) decodeSliceData(sh *sliceHeader, data []byte) error {
	sp, pp := d.asps, d.apps
	d.sp, d.pp, d.sh, d.data = sp, pp, sh, data
	if sh.dataOffset >= len(data) {
		return errBitstream
	}
	nCtb := sp.wCtb * sp.hCtb
	ctbAddrRs := sh.segAddr
	ctbAddrTs := pp.rsToTs[ctbAddrRs]
	d.cab.start(data, sh.dataOffset)
	d.log2MinCuQpDeltaSize = sp.log2Ctb - pp.diffCuQpDeltaDepth
	d.log2MinCuChromaQpSize = sp.log2Ctb - pp.diffCuChromaQpOffsetDepth
	d.cuQpOffsetCb, d.cuQpOffsetCr = 0, 0
	d.isCuChromaQpOffsetCoded = false
	d.isCuQpDeltaCoded = false
	d.cuQpDeltaVal = 0
	if !pp.cuQpDelta {
		d.qpY = sh.sliceQp
	}
	first := true
	for {
		if ctbAddrTs >= nCtb {
			return errBitstream
		}
		ctbAddrRs = pp.tsToRs[ctbAddrTs]
		ctbX, ctbY := ctbAddrRs%sp.wCtb, ctbAddrRs/sp.wCtb
		d.ctbAddrRs, d.ctbAddrTs, d.ctbX, d.ctbY = ctbAddrRs, ctbAddrTs, ctbX, ctbY
		x0, y0 := ctbX<<uint(sp.log2Ctb), ctbY<<uint(sp.log2Ctb)
		tileStart := ctbAddrTs == 0 || pp.tileID[ctbAddrTs] != pp.tileID[ctbAddrTs-1]
		colStart := 0
		if pp.tiles {
			for i := 0; i < pp.numTileCols; i++ {
				if ctbX >= pp.colBd[i] {
					colStart = pp.colBd[i]
				}
			}
		}
		rowStart := pp.wpp && ctbX == colStart
		d.ctbSliceIdx[ctbAddrRs] = int32(sh.idx)
		d.ctbSliceAddr[ctbAddrRs] = int32(sh.sliceAddrRs)

		switch {
		case tileStart:
			d.initContexts()
		case rowStart:
			if d.availZ(x0, y0, x0+sp.ctbSize, y0-sp.ctbSize) {
				d.cab.st = d.wppCtx
				d.statCoeff = d.wppStat
			} else {
				d.initContexts()
			}
		case first:
			if sh.dependent {
				d.cab.st = d.dsCtx
				d.statCoeff = d.dsStat
			} else {
				d.initContexts()
			}
		}
		if (first && !sh.dependent) || tileStart || rowStart {
			d.lastQpY = sh.sliceQp
		}
		first = false

		if err := d.codingTreeUnit(x0, y0); err != nil {
			return err
		}
		d.decodedCtbs++
		d.cab.term = termEndOfSlice
		end := d.cab.terminate() != 0
		if pp.wpp && ctbX == colStart+1 {
			d.wppCtx = d.cab.st
			d.wppStat = d.statCoeff
		}
		ctbAddrTs++
		if end {
			break
		}
		if ctbAddrTs >= nCtb {
			return errBitstream
		}
		nrs := pp.tsToRs[ctbAddrTs]
		newTile := pp.tiles && pp.tileID[ctbAddrTs] != pp.tileID[ctbAddrTs-1]
		newRow := false
		if pp.wpp {
			nx := nrs % sp.wCtb
			cs := 0
			if pp.tiles {
				for i := 0; i < pp.numTileCols; i++ {
					if nx >= pp.colBd[i] {
						cs = pp.colBd[i]
					}
				}
			}
			newRow = nx == cs
		}
		if newTile || newRow {
			d.cab.term = termEndOfSubset
			d.cab.terminate() // end_of_subset_one_bit
			d.cab.start(data, d.cab.bytePos())
		}
	}
	if pp.dependentSlices {
		d.dsCtx = d.cab.st
		d.dsStat = d.statCoeff
	}
	return nil
}

func (d *Decoder) codingTreeUnit(x0, y0 int) error {
	sh := d.sh
	if sh.saoLuma || sh.saoChroma {
		d.parseSao(x0>>uint(d.sp.log2Ctb), y0>>uint(d.sp.log2Ctb))
	}
	return d.codingQuadtree(x0, y0, d.sp.log2Ctb, 0)
}

func (d *Decoder) parseSao(rx, ry int) {
	sp, pp, sh := d.sp, d.pp, d.sh
	c := &d.cab
	cur := &d.sao[d.ctbAddrRs]
	mergeLeft, mergeUp := false, false
	if rx > 0 {
		leftInSlice := d.ctbAddrRs > sh.sliceAddrRs
		leftInTile := pp.tileRs[d.ctbAddrRs] == pp.tileRs[d.ctbAddrRs-1]
		if leftInSlice && leftInTile {
			mergeLeft = c.bin(ctxSaoMerge) != 0
		}
	}
	if ry > 0 && !mergeLeft {
		upInSlice := d.ctbAddrRs-sp.wCtb >= sh.sliceAddrRs
		upInTile := pp.tileRs[d.ctbAddrRs] == pp.tileRs[d.ctbAddrRs-sp.wCtb]
		if upInSlice && upInTile {
			mergeUp = c.bin(ctxSaoMerge) != 0
		}
	}
	if mergeLeft {
		*cur = d.sao[d.ctbAddrRs-1]
		return
	}
	if mergeUp {
		*cur = d.sao[d.ctbAddrRs-sp.wCtb]
		return
	}
	nc := 3
	if sp.chromaArrayType == 0 {
		nc = 1
	}
	for ci := 0; ci < nc; ci++ {
		if !((sh.saoLuma && ci == 0) || (sh.saoChroma && ci > 0)) {
			continue
		}
		if ci == 0 || ci == 1 {
			t := 0
			if c.bin(ctxSaoType) != 0 {
				if c.bypass() == 0 {
					t = 1
				} else {
					t = 2
				}
			}
			cur.typ[ci] = uint8(t)
		} else {
			cur.typ[2] = cur.typ[1]
		}
		if cur.typ[ci] == 0 {
			continue
		}
		bd := sp.bitDepthY
		scale := pp.log2SaoOffsetScaleY
		if ci > 0 {
			bd = sp.bitDepthC
			scale = pp.log2SaoOffsetScaleC
		}
		if bd > 10 {
			bd = 10
		}
		cMax := (1 << uint(bd-5)) - 1
		var abs [4]int
		for i := 0; i < 4; i++ {
			v := 0
			for v < cMax && c.bypass() != 0 {
				v++
			}
			abs[i] = v
		}
		if cur.typ[ci] == 1 {
			for i := 0; i < 4; i++ {
				if abs[i] != 0 && c.bypass() != 0 {
					abs[i] = -abs[i]
				}
			}
			cur.band[ci] = uint8(c.bypassBits(5))
			for i := 0; i < 4; i++ {
				cur.offset[ci][i+1] = int16(abs[i] << uint(scale))
			}
		} else {
			if ci == 0 || ci == 1 {
				cur.eo[ci] = uint8(c.bypassBits(2))
			} else {
				cur.eo[2] = cur.eo[1]
			}
			for i := 0; i < 4; i++ {
				v := abs[i] << uint(scale)
				if i >= 2 {
					v = -v
				}
				cur.offset[ci][i+1] = int16(v)
			}
		}
	}
}

func (d *Decoder) startQG(x0, y0 int) {
	s := d.sp
	mask := s.ctbSize - 1
	prev := d.lastQpY
	qa, qb := prev, prev
	if x0&mask != 0 {
		qa = int(d.cuAt(x0-1, y0).qp)
	}
	if y0&mask != 0 {
		qb = int(d.cuAt(x0, y0-1).qp)
	}
	d.qgPred = (qa + qb + 1) >> 1
	d.setQp()
}

func (d *Decoder) setQp() {
	s := d.sp
	d.qpY = ((d.qgPred+d.cuQpDeltaVal+52+2*s.qpBdOffY)%(52+s.qpBdOffY) - s.qpBdOffY)
}

func (d *Decoder) codingQuadtree(x0, y0, log2Cb, depth int) error {
	s := d.sp
	size := 1 << uint(log2Cb)
	split := false
	if x0+size <= s.width && y0+size <= s.height && log2Cb > s.log2MinCb {
		inc := 0
		if d.availZ(x0, y0, x0-1, y0) && int(d.cuAt(x0-1, y0).depth) > depth {
			inc++
		}
		if d.availZ(x0, y0, x0, y0-1) && int(d.cuAt(x0, y0-1).depth) > depth {
			inc++
		}
		split = d.cab.bin(ctxSplitCu+inc) != 0
	} else {
		split = log2Cb > s.log2MinCb
	}
	if d.pp.cuQpDelta && log2Cb >= d.log2MinCuQpDeltaSize {
		d.isCuQpDeltaCoded = false
		d.cuQpDeltaVal = 0
		d.startQG(x0, y0)
	}
	if d.sh.cuChromaQpOffsetEnabled && log2Cb >= d.log2MinCuChromaQpSize {
		d.isCuChromaQpOffsetCoded = false
	}
	if split {
		h := size >> 1
		x1, y1 := x0+h, y0+h
		if err := d.codingQuadtree(x0, y0, log2Cb-1, depth+1); err != nil {
			return err
		}
		if x1 < s.width {
			if err := d.codingQuadtree(x1, y0, log2Cb-1, depth+1); err != nil {
				return err
			}
		}
		if y1 < s.height {
			if err := d.codingQuadtree(x0, y1, log2Cb-1, depth+1); err != nil {
				return err
			}
		}
		if x1 < s.width && y1 < s.height {
			if err := d.codingQuadtree(x1, y1, log2Cb-1, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	return d.codingUnit(x0, y0, log2Cb, depth)
}

func (d *Decoder) fillCU(x0, y0, log2Cb int, info cuInfo) {
	s := d.sp
	sh := uint(s.log2MinCb)
	n := 1 << (uint(log2Cb) - sh)
	bx, by := x0>>sh, y0>>sh
	for j := 0; j < n; j++ {
		row := d.cuTab[(by+j)*s.wMinCb+bx : (by+j)*s.wMinCb+bx+n]
		for i := range row {
			row[i] = info
		}
	}
}

func (d *Decoder) fillIPM(x0, y0, size int, mode uint8) {
	s := d.sp
	n := size >> 2
	if n == 0 {
		n = 1
	}
	bx, by := x0>>2, y0>>2
	for j := 0; j < n; j++ {
		row := d.ipm[(by+j)*s.wTb4+bx : (by+j)*s.wTb4+bx+n]
		for i := range row {
			row[i] = mode
		}
	}
}

// markEdges records transform / prediction block edges for deblocking.
// kind: 1 = transform edge, 2 = prediction edge.
func (d *Decoder) markEdges(x0, y0, w, h int, kind uint8) {
	s := d.sp
	if x0&7 == 0 {
		bx := x0 >> 2
		for y := y0 >> 2; y < (y0+h)>>2; y++ {
			d.edge[y*s.wTb4+bx] |= kind
		}
	}
	if y0&7 == 0 {
		by := y0 >> 2
		for x := x0 >> 2; x < (x0+w)>>2; x++ {
			d.edge[by*s.wTb4+x] |= kind << 2
		}
	}
}

func (d *Decoder) codingUnit(x0, y0, log2Cb, depth int) error {
	s, p, sh := d.sp, d.pp, d.sh
	c := &d.cab
	nCbS := 1 << uint(log2Cb)
	cu := &d.cu
	*cu = cuState{x0: x0, y0: y0, log2: log2Cb}
	if p.transquantBypass {
		cu.bypass = c.bin(ctxTqBypass) != 0
	}
	skip := false
	if sh.sliceType != sliceI {
		inc := 0
		if d.availZ(x0, y0, x0-1, y0) && d.cuAt(x0-1, y0).flags&cuSkip != 0 {
			inc++
		}
		if d.availZ(x0, y0, x0, y0-1) && d.cuAt(x0, y0-1).flags&cuSkip != 0 {
			inc++
		}
		skip = c.bin(ctxSkip+inc) != 0
	}
	if !p.cuQpDelta {
		d.qpY = sh.sliceQp
	} else {
		d.setQp()
	}
	info := cuInfo{depth: uint8(depth)}
	if cu.bypass {
		info.flags |= cuBypass | cuNoFilter
	}
	d.markEdges(x0, y0, nCbS, nCbS, 1)
	cu.partMode = part2Nx2N
	if skip {
		info.flags |= cuSkip
		info.qp = int8(d.qpY)
		d.fillCU(x0, y0, log2Cb, info)
		d.fillIPM(x0, y0, nCbS, 1)
		if err := d.predictionUnit(x0, y0, nCbS, x0, y0, nCbS, nCbS, 0, true); err != nil {
			return err
		}
	} else {
		intra := true
		if sh.sliceType != sliceI {
			intra = c.bin(ctxPredMode) != 0
		}
		cu.intra = intra
		if intra {
			info.flags |= cuIntra
		}
		info.qp = int8(d.qpY)
		d.fillCU(x0, y0, log2Cb, info)
		if !intra || log2Cb == s.log2MinCb {
			cu.partMode = d.parsePartMode(intra, log2Cb)
		}
		cu.intraSplit = intra && cu.partMode == partNxN
		if intra {
			if cu.partMode == part2Nx2N && s.pcm && log2Cb >= s.log2MinPcm && log2Cb <= s.log2MaxPcm {
				c.term = termPCM
				cu.pcm = c.terminate() != 0
			}
			if cu.pcm {
				d.fillIPM(x0, y0, nCbS, 1)
				d.decodePCM(x0, y0, log2Cb)
				if s.pcmLoopFilterOff {
					info.flags |= cuNoFilter
				}
				info.flags |= cuPcm
				d.fillCU(x0, y0, log2Cb, info)
			} else {
				d.parseIntraModes(x0, y0, log2Cb)
			}
		} else {
			d.fillIPM(x0, y0, nCbS, 1)
			if err := d.interPUs(x0, y0, nCbS); err != nil {
				return err
			}
		}
		if !cu.pcm {
			rqt := true
			if !intra {
				if !(cu.partMode == part2Nx2N && d.lastMerge) {
					rqt = c.bin(ctxNoResidual) != 0
				}
			}
			cu.rqtRoot = rqt
			if rqt {
				if intra {
					cu.maxTrafoDepth = s.maxTHDepthIntra
					if cu.intraSplit {
						cu.maxTrafoDepth++
					}
				} else {
					cu.maxTrafoDepth = s.maxTHDepthInter
				}
				if err := d.transformTree(x0, y0, x0, y0, log2Cb, 0, 0, [2]bool{}, [2]bool{}); err != nil {
					return err
				}
			}
		}
	}
	// final QP of the CU
	if p.cuQpDelta {
		d.setQp()
	}
	q := int8(d.qpY)
	sh2 := uint(s.log2MinCb)
	n := 1 << (uint(log2Cb) - sh2)
	bx, by := x0>>sh2, y0>>sh2
	for j := 0; j < n; j++ {
		row := d.cuTab[(by+j)*s.wMinCb+bx : (by+j)*s.wMinCb+bx+n]
		for i := range row {
			row[i].qp = q
		}
	}
	d.lastQpY = d.qpY
	return nil
}

func (d *Decoder) parsePartMode(intra bool, log2Cb int) int {
	c := &d.cab
	if c.bin(ctxPartMode) != 0 {
		return part2Nx2N
	}
	if intra {
		return partNxN
	}
	s := d.sp
	if log2Cb == s.log2MinCb {
		if log2Cb == 3 {
			if c.bin(ctxPartMode+1) != 0 {
				return part2NxN
			}
			return partNx2N
		}
		if c.bin(ctxPartMode+1) != 0 {
			return part2NxN
		}
		if c.bin(ctxPartMode+2) != 0 {
			return partNx2N
		}
		return partNxN
	}
	if !s.amp {
		if c.bin(ctxPartMode+1) != 0 {
			return part2NxN
		}
		return partNx2N
	}
	if c.bin(ctxPartMode+1) != 0 {
		if c.bin(ctxPartMode+3) != 0 {
			return part2NxN
		}
		if c.bypass() != 0 {
			return part2NxnD
		}
		return part2NxnU
	}
	if c.bin(ctxPartMode+3) != 0 {
		return partNx2N
	}
	if c.bypass() != 0 {
		return partnRx2N
	}
	return partnLx2N
}

func (d *Decoder) decodePCM(x0, y0, log2Cb int) {
	s := d.sp
	var r *bitReader
	rd := func(n int) uint32 { return r.u(n) }
	if d.cab.gen != nil {
		rd = d.cab.gen.pcmBits
	} else {
		r = &bitReader{b: d.data, pos: d.cab.bytePos() * 8}
	}
	pic := d.cur
	n := 1 << uint(log2Cb)
	shY := uint(s.bitDepthY - s.pcmBitDepthY)
	for y := 0; y < n; y++ {
		row := pic.planes[0][(y0+y)*pic.stride[0]+x0:]
		for x := 0; x < n; x++ {
			row[x] = uint16(rd(s.pcmBitDepthY) << shY)
		}
	}
	if s.chromaArrayType != 0 {
		cw, ch := n>>s.subW, n>>s.subH
		cx, cy := x0>>s.subW, y0>>s.subH
		shC := uint(s.bitDepthC - s.pcmBitDepthC)
		for c := 1; c < 3; c++ {
			for y := 0; y < ch; y++ {
				row := pic.planes[c][(cy+y)*pic.stride[c]+cx:]
				for x := 0; x < cw; x++ {
					row[x] = uint16(rd(s.pcmBitDepthC) << shC)
				}
			}
		}
	}
	if r != nil {
		d.cab.start(d.data, (r.pos+7)>>3)
	} else {
		d.cab.start(nil, 0)
	}
}

// parseIntraModes parses prev_intra_luma_pred_flag / mpm_idx / rem and the
// chroma prediction mode(s) of an intra CU.
func (d *Decoder) parseIntraModes(x0, y0, log2Cb int) {
	s := d.sp
	c := &d.cab
	cu := &d.cu
	nCbS := 1 << uint(log2Cb)
	nParts := 1
	pb := nCbS
	if cu.partMode == partNxN {
		nParts = 4
		pb = nCbS >> 1
	}
	var prev [4]bool
	for i := 0; i < nParts; i++ {
		prev[i] = c.bin(ctxPrevIntra) != 0
	}
	var modes [4]int
	for i := 0; i < nParts; i++ {
		xPb := x0 + (i&1)*pb
		yPb := y0 + (i>>1)*pb
		mpm, rem := 0, 0
		if prev[i] {
			if c.bypass() != 0 {
				mpm = 1 + c.bypass()
			}
		} else {
			rem = c.bypassBits(5)
		}
		// candidates
		candA, candB := 1, 1
		if d.availZ(xPb, yPb, xPb-1, yPb) {
			candA = int(d.ipm[(yPb>>2)*s.wTb4+(xPb-1)>>2])
		}
		if d.availZ(xPb, yPb, xPb, yPb-1) && yPb-1 >= (yPb>>uint(s.log2Ctb))<<uint(s.log2Ctb) {
			candB = int(d.ipm[((yPb-1)>>2)*s.wTb4+xPb>>2])
		}
		var list [3]int
		if candA == candB {
			if candA < 2 {
				list = [3]int{0, 1, 26}
			} else {
				list = [3]int{candA, 2 + (candA+29)%32, 2 + (candA-2+1)%32}
			}
		} else {
			list[0], list[1] = candA, candB
			switch {
			case candA != 0 && candB != 0:
				list[2] = 0
			case candA != 1 && candB != 1:
				list[2] = 1
			default:
				list[2] = 26
			}
		}
		var mode int
		if prev[i] {
			mode = list[mpm]
		} else {
			if list[0] > list[1] {
				list[0], list[1] = list[1], list[0]
			}
			if list[0] > list[2] {
				list[0], list[2] = list[2], list[0]
			}
			if list[1] > list[2] {
				list[1], list[2] = list[2], list[1]
			}
			mode = rem
			for k := 0; k < 3; k++ {
				if mode >= list[k] {
					mode++
				}
			}
		}
		modes[i] = mode
		d.fillIPM(xPb, yPb, pb, uint8(mode))
	}
	if s.chromaArrayType == 0 {
		return
	}
	nc := 1
	if s.chromaArrayType == 3 && cu.partMode == partNxN {
		nc = 4
	}
	for i := 0; i < nc; i++ {
		syn := 4
		if c.bin(ctxChromaPred) != 0 {
			syn = c.bypassBits(2)
		}
		cu.chromaModeSyn[i] = syn
		luma := modes[i]
		var m int
		if syn == 4 {
			m = luma
		} else {
			m = [4]int{0, 26, 10, 1}[syn]
			if m == luma {
				m = 34
			}
		}
		if s.chromaArrayType == 2 {
			m = int(mode422[m])
		}
		cu.chromaMode[i] = m
	}
	if nc == 1 {
		for i := 1; i < 4; i++ {
			cu.chromaMode[i] = cu.chromaMode[0]
			cu.chromaModeSyn[i] = cu.chromaModeSyn[0]
		}
	}
}

func (d *Decoder) transformTree(x0, y0, xBase, yBase, log2, depth, blkIdx int, pcb, pcr [2]bool) error {
	s := d.sp
	cu := &d.cu
	c := &d.cab
	var split bool
	if log2 <= s.log2MaxTb && log2 > s.log2MinTb && depth < cu.maxTrafoDepth && !(cu.intraSplit && depth == 0) {
		split = c.bin(ctxSplitTransform+5-log2) != 0
	} else {
		interSplit := s.maxTHDepthInter == 0 && !cu.intra && cu.partMode != part2Nx2N && depth == 0
		split = log2 > s.log2MaxTb || (cu.intraSplit && depth == 0) || interSplit
	}
	var cb, cr [2]bool
	cat := s.chromaArrayType
	if (log2 > 2 && cat != 0) || cat == 3 {
		if depth == 0 || pcb[0] {
			cb[0] = c.bin(ctxCbfChroma+depth) != 0
			if cat == 2 && (!split || log2 == 3) {
				cb[1] = c.bin(ctxCbfChroma+depth) != 0
			}
		}
		if depth == 0 || pcr[0] {
			cr[0] = c.bin(ctxCbfChroma+depth) != 0
			if cat == 2 && (!split || log2 == 3) {
				cr[1] = c.bin(ctxCbfChroma+depth) != 0
			}
		}
	} else if log2 == 2 && depth > 0 && cat != 0 {
		cb, cr = pcb, pcr
	}
	if split {
		h := 1 << uint(log2-1)
		x1, y1 := x0+h, y0+h
		if err := d.transformTree(x0, y0, x0, y0, log2-1, depth+1, 0, cb, cr); err != nil {
			return err
		}
		if err := d.transformTree(x1, y0, x0, y0, log2-1, depth+1, 1, cb, cr); err != nil {
			return err
		}
		if err := d.transformTree(x0, y1, x0, y0, log2-1, depth+1, 2, cb, cr); err != nil {
			return err
		}
		return d.transformTree(x1, y1, x0, y0, log2-1, depth+1, 3, cb, cr)
	}
	cbfL := true
	if cu.intra || depth != 0 || cb[0] || cb[1] || cr[0] || cr[1] {
		inc := 0
		if depth == 0 {
			inc = 1
		}
		cbfL = c.bin(ctxCbfLuma+inc) != 0
	}
	return d.transformUnit(x0, y0, xBase, yBase, log2, depth, blkIdx, cbfL, cb, cr)
}

func (d *Decoder) transformUnit(x0, y0, xBase, yBase, log2, depth, blkIdx int, cbfL bool, cb, cr [2]bool) error {
	s, p, sh := d.sp, d.pp, d.sh
	c := &d.cab
	cu := &d.cu
	cat := s.chromaArrayType
	size := 1 << uint(log2)
	d.markEdges(x0, y0, size, size, 1)
	if cbfL {
		bx := x0 >> 2
		for y := y0 >> 2; y < (y0+size)>>2; y++ {
			row := d.cbfTab[y*s.wTb4+bx : y*s.wTb4+bx+(size>>2)]
			for i := range row {
				row[i] = 1
			}
		}
	}
	cbfChroma := cat != 0 && (cb[0] || cb[1] || cr[0] || cr[1])
	if cbfL || cbfChroma {
		if p.cuQpDelta && !d.isCuQpDeltaCoded {
			// cu_qp_delta_abs
			v := 0
			if c.bin(ctxCuQpDelta) != 0 {
				v = 1
				for v < 5 && c.bin(ctxCuQpDelta+1) != 0 {
					v++
				}
				if v == 5 {
					// EG0 suffix
					k := 0
					for c.bypass() != 0 {
						v += 1 << uint(k)
						k++
						if k > 30 {
							return errBitstream
						}
					}
					if k > 0 {
						v += c.bypassBits(k)
					}
				}
			}
			if v != 0 && c.bypass() != 0 {
				v = -v
			}
			d.isCuQpDeltaCoded = true
			d.cuQpDeltaVal = v
			d.setQp()
		}
		if sh.cuChromaQpOffsetEnabled && cbfChroma && !cu.bypass && !d.isCuChromaQpOffsetCoded {
			f := c.bin(ctxCuChromaQpFlag) != 0
			if f {
				idx := 0
				if p.chromaQpOffsetListLen > 1 {
					for idx < p.chromaQpOffsetListLen-1 && c.bin(ctxCuChromaQpIdx) != 0 {
						idx++
					}
				}
				d.cuQpOffsetCb = p.cbQpOffsetList[idx]
				d.cuQpOffsetCr = p.crQpOffsetList[idx]
			} else {
				d.cuQpOffsetCb, d.cuQpOffsetCr = 0, 0
			}
			d.isCuChromaQpOffsetCoded = true
		}
	}
	if dbgTU != nil {
		dbgTU(d, x0, y0, log2, blkIdx, cbfL, cb, cr)
	}
	// partition index of this TU within an NxN intra CU
	part := 0
	if cu.intraSplit {
		half := 1 << uint(cu.log2-1)
		if x0 >= cu.x0+half {
			part |= 1
		}
		if y0 >= cu.y0+half {
			part |= 2
		}
	}
	// luma
	lumaMode := 0
	if cu.intra {
		lumaMode = int(d.ipm[(y0>>2)*s.wTb4+x0>>2])
		d.intraPredict(0, x0, y0, log2, lumaMode)
	}
	haveResY := false
	if cbfL {
		ti := d.residualCoding(log2, 0, cu.intra, lumaMode)
		d.reconTB(0, x0, y0, log2, &ti, cu.intra, lumaMode, 0)
		haveResY = true
	}
	if cat == 0 {
		return nil
	}
	if !(log2 > 2 || cat == 3 || blkIdx == 3) {
		return nil
	}
	xc, yc := x0, y0
	log2c := log2
	if cat != 3 {
		log2c = log2 - 1
		if log2 == 2 {
			xc, yc = xBase, yBase
			log2c = 2
		}
	}
	chromaMode := cu.chromaMode[0]
	if cat == 3 && cu.intraSplit {
		chromaMode = cu.chromaMode[part]
	}
	nT := 1
	if cat == 2 {
		nT = 2
	}
	crossOK := cat == 3 && p.crossComponentPred && cbfL && haveResY && (!cu.intra || cu.chromaModeSyn[part] == 4)
	for ci := 1; ci <= 2; ci++ {
		cbf := cb
		if ci == 2 {
			cbf = cr
		}
		resScale := 0
		if crossOK {
			// cross_comp_pred
			l := 0
			for l < 4 && c.bin(ctxLog2ResScale+4*(ci-1)+l) != 0 {
				l++
			}
			if l > 0 {
				sign := c.bin(ctxResScaleSign + ci - 1)
				resScale = (1 << uint(l-1)) * (1 - 2*sign)
			}
		}
		for t := 0; t < nT; t++ {
			xC := xc >> s.subW
			yC := (yc >> s.subH) + t<<uint(log2c)
			if cu.intra {
				d.intraPredict(ci, xC, yC, log2c, chromaMode)
			}
			if cbf[t] {
				ti := d.residualCoding(log2c, ci, cu.intra, chromaMode)
				d.reconTB(ci, xC, yC, log2c, &ti, cu.intra, chromaMode, resScale)
			} else if resScale != 0 {
				var ti tbInfo
				ti.zero = true
				d.reconTB(ci, xC, yC, log2c, &ti, cu.intra, chromaMode, resScale)
			}
		}
	}
	return nil
}

var dbgTU func(d *Decoder, x0, y0, log2, blkIdx int, cbfL bool, cb, cr [2]bool)

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
