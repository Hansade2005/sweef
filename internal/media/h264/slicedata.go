package h264

// Slice data loop (7.3.4), including MBAFF macroblock pairs.

// predictFieldFlag infers mb_field_decoding_flag for a pair whose flag is
// not transmitted (both macroblocks skipped) from the left, else the upper
// neighbouring pair in the same slice.
func (d *Decoder) predictFieldFlag(mbx, mby int) {
	pic := d.curPic
	i := pic.mbIdx(mbx, mby)
	var t uint32
	if d.sliceTable[i-1] == d.curSlice {
		t = pic.mbType[i-1]
	} else if d.sliceTable[i-d.mbStride] == d.curSlice {
		t = pic.mbType[i-d.mbStride]
	}
	d.mbField = t&mbInterlaced != 0
}

// decodeMBEntry decodes the macroblock at (d.mbx, d.mby), handling skip
// signalling and the MBAFF field decoding flag (CAVLC and CABAC).
func (d *Decoder) decodeMBEntry() error {
	h := d.hdr
	if d.cabacOn {
		if !h.isI() {
			var skip bool
			if d.mbaffFrame && d.mby&1 == 1 && d.prevMBSkipped {
				skip = d.nextMBSkipped
			} else {
				skip = d.cabacSkipFlag(d.mbx, d.mby)
			}
			if skip {
				if d.mbaffFrame && d.mby&1 == 0 {
					idx := d.curPic.mbIdx(d.mbx, d.mby)
					d.curPic.mbType[idx] = mbSkip
					d.nextMBSkipped = d.cabacSkipFlag(d.mbx, d.mby+1)
					if !d.nextMBSkipped {
						d.mbField = d.cabacFieldDecodingFlag()
					}
				}
				d.startMB(d.mbx, d.mby)
				d.decodeSkipMB()
				return nil
			}
		}
		if d.mbaffFrame && d.mby&1 == 0 {
			d.mbField = d.cabacFieldDecodingFlag()
		}
		d.startMB(d.mbx, d.mby)
		return d.decodeMB()
	}
	if !h.isI() {
		if d.skipRun == -1 {
			run := int(d.br.ue())
			if run > d.mbW*d.mbH {
				return corrupt()
			}
			d.skipRun = run
		}
		old := d.skipRun
		d.skipRun--
		if old != 0 {
			if d.mbaffFrame && d.mby&1 == 0 && d.skipRun == 0 {
				d.mbField = d.br.flag()
			}
			d.startMB(d.mbx, d.mby)
			d.decodeSkipMB()
			return nil
		}
	}
	if d.mbaffFrame && d.mby&1 == 0 {
		d.mbField = d.br.flag()
	}
	d.startMB(d.mbx, d.mby)
	return d.decodeMB()
}

func (d *Decoder) decodeSlice(h *sliceHeader, r *bitReader) error {
	d.hdr = h
	d.br = r
	d.sliceNum++
	d.curSlice = uint16(d.sliceNum)
	d.slices = append(d.slices, sliceParams{
		disable: h.disableDeblock,
		alpha:   h.alphaOff,
		beta:    h.betaOff,
		cbOff:   h.pps.ChromaQPOffset[0],
		crOff:   h.pps.ChromaQPOffset[1],
	})
	d.listCount = 1 + btoi(h.isB())
	if h.isI() {
		d.listCount = 0
	}
	if !h.isI() {
		if !d.buildRefList(h, d.picStructure) {
			return corrupt()
		}
		if len(d.refList[0]) == 0 || (h.isB() && len(d.refList[1]) == 0) {
			return corrupt()
		}
		d.directRefListInit(h, d.picStructure)
		if h.isB() {
			d.setupImplicit(h)
		}
	}
	d.qp = h.qp
	d.lastDQ = false
	d.cabacOn = h.pps.CABAC
	d.skipRun = -1
	d.prevMBSkipped = false
	d.mbField = d.fieldPic
	if d.cabacOn {
		r.byteAlign()
		d.cab.data = r.b
		d.cab.initContexts(h.qp, h.cabacInitIdc, h.isI())
		d.cab.start(r.pos)
	}
	if !d.setupMBOrder(h) {
		return corrupt()
	}
	pairStep := 1 + btoi(d.mbaffFrame)
	for {
		err := d.decodeMBEntry()
		if err == nil && d.mbaffFrame {
			d.mby++
			err = d.decodeMBEntry()
			d.mby--
		}
		if err != nil {
			return err
		}
		eos := false
		if d.cabacOn {
			eos = d.cab.terminate() != 0
			if d.cab.pos > r.end+64 {
				return corrupt()
			}
		}
		if !d.advanceMB(pairStep) {
			break
		}
		if d.mbaffFrame && d.rowAdvanced {
			d.predictFieldFlag(d.mbx, d.mby)
		}
		if eos {
			break
		}
		if !d.cabacOn {
			if r.overrun() {
				return corrupt()
			}
			if !r.moreRBSPData() && d.skipRun <= 0 {
				break
			}
		}
	}
	return nil
}

// advanceMB moves to the next macroblock (or pair) of the slice and reports
// whether one exists.
func (d *Decoder) advanceMB(pairStep int) bool {
	d.rowAdvanced = false
	if d.fmoMap != nil {
		return d.advanceFMO()
	}
	d.mbx++
	if d.mbx >= d.mbW {
		d.mbx = 0
		d.mby += pairStep
		if d.fieldPic {
			d.mby += 1
		}
		d.rowAdvanced = true
	}
	return d.mby < d.mbH
}

// setupMBOrder sets the first macroblock of the slice.
func (d *Decoder) setupMBOrder(h *sliceHeader) bool {
	d.fmoMap = nil
	if h.pps.NumSliceGroups > 1 {
		d.fmoMap = d.sliceGroupMap(h)
	}
	first := h.firstMB
	if d.fmoMap != nil {
		return d.startFMO(first)
	}
	d.mbx = first % d.mbW
	row := first / d.mbW
	switch {
	case d.mbaffFrame:
		d.mby = row << 1
	case d.fieldPic:
		d.mby = row<<1 + btoi(d.picStructure == picBottom)
	default:
		d.mby = row
	}
	return d.mby < d.mbH
}

// setupImplicit computes the implicit bi-prediction weights (8.4.2.3.1).
func (d *Decoder) setupImplicit(h *sliceHeader) {
	if h.pps.WeightedBipredIdc != 2 {
		return
	}
	cur := d.curPic
	calc := func(tab int, l0, l1 []refEntry, curPOC int, n0, n1 int) {
		for i := 0; i < n0; i++ {
			for j := 0; j < n1; j++ {
				w1 := 32
				e0, e1 := &l0[i], &l1[j]
				if e0.pic != nil && e1.pic != nil && !e0.pic.longRef && !e1.pic.longRef {
					td := clip3(-128, 127, e1.poc-e0.poc)
					if td != 0 {
						tb := clip3(-128, 127, curPOC-e0.poc)
						tx := (16384 + abs(td)>>1) / td
						dsf := (tb*tx + 32) >> 8
						if dsf >= -64 && dsf <= 128 {
							w1 = dsf
						}
					}
				}
				d.implicitW[tab][i][j] = int16(w1)
			}
		}
	}
	curPOC := cur.poc
	if d.picStructure != picFrame {
		curPOC = cur.fieldPOC[d.picStructure-1]
	}
	calc(0, d.refList[0], d.refList[1], curPOC, len(d.refList[0]), len(d.refList[1]))
	if d.mbaffFrame {
		for f := 0; f < 2; f++ {
			// field macroblock of parity f: entries (r ^ f) of the field lists
			var l0, l1 [64]refEntry
			for i := range d.refListField[0] {
				l0[i^f] = d.refListField[0][i]
			}
			for i := range d.refListField[1] {
				l1[i^f] = d.refListField[1][i]
			}
			calc(1+f, l0[:], l1[:], cur.fieldPOC[f], len(d.refListField[0]), len(d.refListField[1]))
		}
	}
}
