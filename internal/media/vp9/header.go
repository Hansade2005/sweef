package vp9

import "github.com/sweefbrowser/sweef/internal/media/yuv"

func (d *Decoder) readColorConfig(r *bitReader) error {
	d.bitDepth = 8
	if d.profile >= 2 {
		if r.flag() {
			d.bitDepth = 12
		} else {
			d.bitDepth = 10
		}
	}
	d.maxV = int32(1)<<uint(d.bitDepth) - 1
	d.colorSp = r.u(3)
	if d.colorSp != 7 { // not RGB
		d.fullRng = r.flag()
		if d.profile == 1 || d.profile == 3 {
			d.subX = r.u(1)
			d.subY = r.u(1)
			if r.u(1) != 0 {
				return errBitstream
			}
		} else {
			d.subX, d.subY = 1, 1
		}
	} else {
		d.fullRng = true
		if d.profile == 1 || d.profile == 3 {
			d.subX, d.subY = 0, 0
			if r.u(1) != 0 {
				return errBitstream
			}
		} else {
			return errBitstream
		}
	}
	return nil
}

func (d *Decoder) frameSize(r *bitReader) {
	d.width = r.u(16) + 1
	d.height = r.u(16) + 1
	d.computeImageSize()
}

func (d *Decoder) renderSize(r *bitReader) {
	if r.flag() {
		r.u(16)
		r.u(16)
	}
}

func (d *Decoder) computeImageSize() {
	d.miCols = (d.width + 7) >> 3
	d.miRows = (d.height + 7) >> 3
	d.sb64Cols = (d.miCols + 7) >> 3
	d.sb64Rows = (d.miRows + 7) >> 3
	n := d.miCols * d.miRows
	sizeChanged := !d.haveSize || d.width != d.lastW || d.height != d.lastH
	if sizeChanged || len(d.mi) != n {
		d.mi = make([]modeInfo, n)
		d.prevMi = make([]modeInfo, n)
		d.segIDs = make([]uint8, n)
		d.prevSegIDs = make([]uint8, n)
	}
	d.usePrevMvs = d.haveSize && !sizeChanged && d.lastShow && !d.errorRes && !d.frameIsIntra
	d.haveSize = true
	d.lastW, d.lastH = d.width, d.height
}

func (d *Decoder) setupPastIndependence() {
	d.seg.featEnabled = [8][4]bool{}
	d.seg.featData = [8][4]int{}
	d.seg.absDelta = false
	for i := range d.prevSegIDs {
		d.prevSegIDs[i] = 0
	}
	d.lfDeltaEnabled = true
	d.lfRefDeltas = [4]int{1, 0, -1, -1}
	d.lfModeDeltas = [2]int{}
	d.signBias = [4]bool{}
	d.p = defaultProbs()
}

func (d *Decoder) decodeFrame(data []byte) (*yuv.Frame, error) {
	r := &bitReader{b: data}
	if r.u(2) != 2 {
		return nil, errBitstream
	}
	lo := r.u(1)
	hi := r.u(1)
	d.profile = hi<<1 | lo
	if d.profile == 3 {
		if r.u(1) != 0 {
			return nil, ErrUnsupported
		}
	}
	if r.flag() { // show_existing_frame
		idx := r.u(3)
		f := d.refs[idx]
		if f == nil {
			return nil, errBitstream
		}
		return frameOut(f), nil
	}
	d.lastFrameType = d.frameType
	d.frameType = r.u(1)
	d.showFrame = r.flag()
	d.errorRes = r.flag()
	if d.frameType == keyFrame {
		if r.u(8) != 0x49 || r.u(8) != 0x83 || r.u(8) != 0x42 {
			return nil, errBitstream
		}
		if err := d.readColorConfig(r); err != nil {
			return nil, err
		}
		d.frameIsIntra = true
		d.intraOnly = false
		d.resetFrameCtx = 0
		d.refreshFlags = 0xff
		d.frameSizeNew(r)
		d.renderSize(r)
	} else {
		d.intraOnly = false
		if !d.showFrame {
			d.intraOnly = r.flag()
		}
		d.frameIsIntra = d.intraOnly
		d.resetFrameCtx = 0
		if !d.errorRes {
			d.resetFrameCtx = r.u(2)
		}
		if d.intraOnly {
			if r.u(8) != 0x49 || r.u(8) != 0x83 || r.u(8) != 0x42 {
				return nil, errBitstream
			}
			if d.profile > 0 {
				if err := d.readColorConfig(r); err != nil {
					return nil, err
				}
			} else {
				// profile 0: 8-bit BT.601 4:2:0
				d.bitDepth, d.maxV, d.subX, d.subY, d.colorSp, d.fullRng = 8, 255, 1, 1, 1, false
			}
			d.refreshFlags = r.u(8)
			d.frameSizeNew(r)
			d.renderSize(r)
		} else {
			d.refreshFlags = r.u(8)
			for i := 0; i < 3; i++ {
				d.refFrameIdx[i] = r.u(3)
				d.signBias[lastFrame+i] = r.flag()
			}
			found := false
			for i := 0; i < 3; i++ {
				if r.flag() {
					rf := d.refs[d.refFrameIdx[i]]
					if rf == nil {
						return nil, errBitstream
					}
					d.width, d.height = rf.w, rf.h
					found = true
					break
				}
			}
			if !found {
				d.frameSizeNew(r)
			} else {
				d.computeImageSize()
			}
			d.renderSize(r)
			d.allowHp = r.flag()
			if r.flag() {
				d.interpFilter = switchable
			} else {
				d.interpFilter = int(literalToType[r.u(2)])
			}
			anyValid := false
			for i := 0; i < 3; i++ {
				rf := d.refs[d.refFrameIdx[i]]
				if rf == nil || rf.bd != d.bitDepth || rf.subX != d.subX || rf.subY != d.subY {
					return nil, errBitstream
				}
				// reference scaling limits (8.5.2.3)
				d.refValid[i] = 2*d.width >= rf.w && 2*d.height >= rf.h && d.width <= 16*rf.w && d.height <= 16*rf.h
				anyValid = anyValid || d.refValid[i]
			}
			if !anyValid {
				return nil, errBitstream
			}
		}
	}
	d.lastShow = d.showFrame
	if !d.errorRes {
		d.refreshFrameCtx = r.flag()
		d.frameParallel = r.flag()
	} else {
		d.refreshFrameCtx = false
		d.frameParallel = true
	}
	d.frameCtxIdx = r.u(2)
	if d.frameIsIntra || d.errorRes {
		d.setupPastIndependence()
		if d.frameType == keyFrame || d.errorRes || d.resetFrameCtx == 3 {
			for i := range d.ctxs {
				d.ctxs[i] = d.p
			}
		} else if d.resetFrameCtx == 2 {
			d.ctxs[d.frameCtxIdx] = d.p
		}
		d.frameCtxIdx = 0
	}
	// loop filter params
	d.lfLevel = r.u(6)
	d.lfSharp = r.u(3)
	d.lfDeltaEnabled = r.flag()
	if d.lfDeltaEnabled {
		if r.flag() {
			for i := 0; i < 4; i++ {
				if r.flag() {
					d.lfRefDeltas[i] = r.s(6)
				}
			}
			for i := 0; i < 2; i++ {
				if r.flag() {
					d.lfModeDeltas[i] = r.s(6)
				}
			}
		}
	}
	// quantization params
	d.baseQIdx = r.u(8)
	readDelta := func() int {
		if r.flag() {
			return r.s(4)
		}
		return 0
	}
	d.deltaQYDc = readDelta()
	d.deltaQUVDc = readDelta()
	d.deltaQUVAc = readDelta()
	d.lossless = d.baseQIdx == 0 && d.deltaQYDc == 0 && d.deltaQUVDc == 0 && d.deltaQUVAc == 0
	// segmentation params
	s := &d.seg
	s.enabled = r.flag()
	s.updateMap, s.temporal, s.updateData = false, false, false
	if s.enabled {
		s.updateMap = r.flag()
		if s.updateMap {
			readProb := func() uint8 {
				if r.flag() {
					return uint8(r.u(8))
				}
				return 255
			}
			for i := 0; i < 7; i++ {
				s.treeProbs[i] = readProb()
			}
			s.temporal = r.flag()
			for i := 0; i < 3; i++ {
				if s.temporal {
					s.predProbs[i] = readProb()
				} else {
					s.predProbs[i] = 255
				}
			}
		}
		s.updateData = r.flag()
		if s.updateData {
			s.absDelta = r.flag()
			for i := 0; i < 8; i++ {
				for j := 0; j < 4; j++ {
					val := 0
					en := r.flag()
					s.featEnabled[i][j] = en
					if en {
						val = r.u(segFeatureBits[j])
						if segFeatureSigned[j] && r.flag() {
							val = -val
						}
					}
					s.featData[i][j] = val
				}
			}
		}
	}
	// tile info
	minLog2 := 0
	for (64 << uint(minLog2)) < d.sb64Cols {
		minLog2++
	}
	maxLog2 := 1
	for (d.sb64Cols >> uint(maxLog2)) >= 4 {
		maxLog2++
	}
	maxLog2--
	d.tileColsLog2 = minLog2
	for d.tileColsLog2 < maxLog2 {
		if r.flag() {
			d.tileColsLog2++
		} else {
			break
		}
	}
	d.tileRowsLog2 = r.u(1)
	if d.tileRowsLog2 == 1 {
		d.tileRowsLog2 += r.u(1)
	}
	d.headerSize = r.u(16)
	if r.overrun() || d.headerSize == 0 {
		return nil, errBitstream
	}
	pos := (r.pos + 7) >> 3
	if pos+d.headerSize > len(data) {
		return nil, errBitstream
	}

	// load probabilities
	d.p = d.ctxs[d.frameCtxIdx]
	d.cnt = counts{}
	if err := d.compressedHeader(data[pos : pos+d.headerSize]); err != nil {
		return nil, err
	}
	pos += d.headerSize

	// allocate the new frame
	d.cur = newFrame(d.width, d.height, d.subX, d.subY, d.bitDepth)
	d.cur.cs, d.cur.full = d.colorSp, d.fullRng
	d.decodeTiles(data[pos:])

	if d.lfLevel != 0 {
		d.loopFilterFrame()
	}
	// probability adaptation
	if !d.errorRes && !d.frameParallel {
		saved := d.ctxs[d.frameCtxIdx]
		cur := d.p
		d.p = saved
		// load_probs keeps tx and skip of the current frame
		d.p.tx, d.p.skip = cur.tx, cur.skip
		uf := 112
		if !d.frameIsIntra && d.lastFrameType == keyFrame {
			uf = 128
		}
		d.p.adaptCoef(&d.cnt, uf)
		if !d.frameIsIntra {
			d.p.tx, d.p.skip = saved.tx, saved.skip
			d.p.adaptNonCoef(&d.cnt, d.interpFilter == switchable, d.txMode == txModeSelect, d.allowHp)
		}
	}
	if d.refreshFrameCtx {
		d.ctxs[d.frameCtxIdx] = d.p
	}
	// segmentation map and motion vector history
	if d.seg.enabled && d.seg.updateMap {
		copy(d.prevSegIDs, d.segIDs)
	}
	d.mi, d.prevMi = d.prevMi, d.mi
	// reference update
	for i := 0; i < 8; i++ {
		if d.refreshFlags>>uint(i)&1 != 0 {
			d.refs[i] = d.cur
		}
	}
	var out *yuv.Frame
	if d.showFrame {
		out = frameOut(d.cur)
	}
	d.cur = nil
	return out, nil
}

func (d *Decoder) frameSizeNew(r *bitReader) {
	d.width = r.u(16) + 1
	d.height = r.u(16) + 1
	d.computeImageSize()
}

func newFrame(w, h, subX, subY, bd int) *Frame {
	f := &Frame{w: w, h: h, subX: subX, subY: subY, bd: bd}
	aw := (w + 63) &^ 63
	ah := (h + 63) &^ 63
	cw, ch := aw>>uint(subX), ah>>uint(subY)
	f.stride = [3]int{aw, cw, cw}
	f.pix[0] = make([]uint16, aw*ah)
	f.pix[1] = make([]uint16, cw*ch)
	f.pix[2] = make([]uint16, cw*ch)
	f.pw = [3]int{w, (w + subX) >> uint(subX), (w + subX) >> uint(subX)}
	f.ph = [3]int{h, (h + subY) >> uint(subY), (h + subY) >> uint(subY)}
	return f
}

// frameOut wraps a decoded frame as an output picture sharing its planes.
func frameOut(f *Frame) *yuv.Frame {
	out := &yuv.Frame{Depth: f.bd, Width: f.w, Height: f.h, FullRange: f.full}
	switch {
	case f.subX == 1 && f.subY == 1:
		out.Format = yuv.F420
	case f.subX == 1:
		out.Format = yuv.F422
	case f.subY == 1:
		out.Format = yuv.F440
	default:
		out.Format = yuv.F444
	}
	out.CWidth, out.CHeight = f.pw[1], f.ph[1]
	out.Planes = f.pix
	out.Stride = f.stride
	switch f.cs {
	case 1, 3, 4:
		out.Matrix = yuv.MatrixBT601
	case 2:
		out.Matrix = yuv.MatrixBT709
	case 5:
		out.Matrix = yuv.MatrixBT2020
	case 7:
		out.Matrix = yuv.MatrixIdentity
	default:
		out.Matrix = yuv.MatrixUnspecified
	}
	return out
}

// ---- compressed header --------------------------------------------------------------------

func (d *Decoder) diffUpdateProb(b *boolDec, p *uint8) {
	if b.read(252) == 0 {
		return
	}
	var delta int
	switch {
	case b.literal(1) == 0:
		delta = b.literal(4)
	case b.literal(1) == 0:
		delta = b.literal(4) + 16
	case b.literal(1) == 0:
		delta = b.literal(5) + 32
	default:
		v := b.literal(7)
		if v < 65 {
			delta = v + 64
		} else {
			bit := b.literal(1)
			delta = (v << 1) - 1 + bit
		}
	}
	if delta >= len(invMapTable) {
		delta = len(invMapTable) - 1
	}
	v := int(invMapTable[delta])
	m := int(*p) - 1
	if m<<1 <= 255 {
		m = 1 + invRecenter(v, m)
	} else {
		m = 255 - invRecenter(v, 255-1-m)
	}
	*p = uint8(m)
}

func invRecenter(v, m int) int {
	if v > 2*m {
		return v
	}
	if v&1 != 0 {
		return m - ((v + 1) >> 1)
	}
	return m + (v >> 1)
}

func (d *Decoder) compressedHeader(buf []byte) error {
	var b boolDec
	if !b.init(buf) {
		return errBitstream
	}
	// tx mode
	if d.lossless {
		d.txMode = only4x4
	} else {
		d.txMode = b.literal(2)
		if d.txMode == allow32x32 {
			d.txMode += b.literal(1)
		}
	}
	if d.txMode == txModeSelect {
		for i := 0; i < 2; i++ {
			for j := 0; j < 1; j++ {
				d.diffUpdateProb(&b, &d.p.tx[tx8x8][i][j])
			}
		}
		for i := 0; i < 2; i++ {
			for j := 0; j < 2; j++ {
				d.diffUpdateProb(&b, &d.p.tx[tx16x16][i][j])
			}
		}
		for i := 0; i < 2; i++ {
			for j := 0; j < 3; j++ {
				d.diffUpdateProb(&b, &d.p.tx[tx32x32][i][j])
			}
		}
	}
	// coef probs
	maxTx := int(txModeBiggest[d.txMode])
	for t := 0; t <= maxTx; t++ {
		if b.literal(1) == 1 {
			for i := 0; i < 2; i++ {
				for j := 0; j < 2; j++ {
					for k := 0; k < 6; k++ {
						maxL := 6
						if k == 0 {
							maxL = 3
						}
						for l := 0; l < maxL; l++ {
							for m := 0; m < 3; m++ {
								d.diffUpdateProb(&b, &d.p.coef[t][i][j][k][l][m])
							}
						}
					}
				}
			}
		}
	}
	for i := 0; i < 3; i++ {
		d.diffUpdateProb(&b, &d.p.skip[i])
	}
	if !d.frameIsIntra {
		for i := 0; i < 7; i++ {
			for j := 0; j < 3; j++ {
				d.diffUpdateProb(&b, &d.p.interMode[i][j])
			}
		}
		if d.interpFilter == switchable {
			for j := 0; j < 4; j++ {
				for i := 0; i < 2; i++ {
					d.diffUpdateProb(&b, &d.p.interpFilter[j][i])
				}
			}
		}
		for i := 0; i < 4; i++ {
			d.diffUpdateProb(&b, &d.p.isInter[i])
		}
		// frame_reference_mode
		compAllowed := false
		for i := 1; i < 3; i++ {
			if d.signBias[i+1] != d.signBias[1] {
				compAllowed = true
			}
		}
		d.referenceMode = singleReference
		if compAllowed {
			if b.literal(1) == 1 {
				if b.literal(1) == 0 {
					d.referenceMode = compoundRef
				} else {
					d.referenceMode = referenceSelect
				}
				d.setupCompound()
			}
		}
		if d.referenceMode == referenceSelect {
			for i := 0; i < 5; i++ {
				d.diffUpdateProb(&b, &d.p.compMode[i])
			}
		}
		if d.referenceMode != compoundRef {
			for i := 0; i < 5; i++ {
				d.diffUpdateProb(&b, &d.p.singleRef[i][0])
				d.diffUpdateProb(&b, &d.p.singleRef[i][1])
			}
		}
		if d.referenceMode != singleReference {
			for i := 0; i < 5; i++ {
				d.diffUpdateProb(&b, &d.p.compRef[i])
			}
		}
		for i := 0; i < 4; i++ {
			for j := 0; j < 9; j++ {
				d.diffUpdateProb(&b, &d.p.yMode[i][j])
			}
		}
		for i := 0; i < 16; i++ {
			for j := 0; j < 3; j++ {
				d.diffUpdateProb(&b, &d.p.partition[i][j])
			}
		}
		// mv probs
		updMv := func(p *uint8) {
			if b.read(252) == 1 {
				*p = uint8(b.literal(7)<<1 | 1)
			}
		}
		for j := 0; j < 3; j++ {
			updMv(&d.p.mvJoint[j])
		}
		for i := 0; i < 2; i++ {
			updMv(&d.p.mvSign[i])
			for j := 0; j < 10; j++ {
				updMv(&d.p.mvClass[i][j])
			}
			updMv(&d.p.mvClass0Bit[i])
			for j := 0; j < 10; j++ {
				updMv(&d.p.mvBits[i][j])
			}
		}
		for i := 0; i < 2; i++ {
			for j := 0; j < 2; j++ {
				for k := 0; k < 3; k++ {
					updMv(&d.p.mvClass0Fr[i][j][k])
				}
			}
			for k := 0; k < 3; k++ {
				updMv(&d.p.mvFr[i][k])
			}
		}
		if d.allowHp {
			for i := 0; i < 2; i++ {
				updMv(&d.p.mvClass0Hp[i])
				updMv(&d.p.mvHp[i])
			}
		}
	}
	return nil
}

func (d *Decoder) setupCompound() {
	switch {
	case d.signBias[lastFrame] == d.signBias[goldenFrame]:
		d.compFixedRef = altrefFrame
		d.compVarRef = [2]int{lastFrame, goldenFrame}
	case d.signBias[lastFrame] == d.signBias[altrefFrame]:
		d.compFixedRef = goldenFrame
		d.compVarRef = [2]int{lastFrame, altrefFrame}
	default:
		d.compFixedRef = lastFrame
		d.compVarRef = [2]int{goldenFrame, altrefFrame}
	}
}
