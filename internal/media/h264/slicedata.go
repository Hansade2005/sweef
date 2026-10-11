package h264

// Slice data loop (7.3.4).

func (d *Decoder) decodeSlice(h *sliceHeader, r *bitReader) error {
	d.hdr = h
	d.br = r
	pic := d.cur
	d.sliceNum++
	d.curSlice = d.sliceNum
	d.slices = append(d.slices, sliceParams{
		disable: h.disableDeblock,
		alpha:   h.alphaOff,
		beta:    h.betaOff,
		cbOff:   h.pps.ChromaQPOffset[0],
		crOff:   h.pps.ChromaQPOffset[1],
	})
	d.buildRefLists(h, pic)
	if !h.isI() {
		if len(d.refList[0]) == 0 || (h.isB() && len(d.refList[1]) == 0) {
			return corrupt()
		}
	}
	if h.isB() {
		d.col = d.refList[1][0]
		d.setupImplicit(h)
	}
	d.qp = h.qp
	d.lastDQ = false
	d.cabacOn = h.pps.CABAC
	total := d.mbW * d.mbH
	if d.cabacOn {
		r.byteAlign()
		d.cab.data = r.b
		d.cab.initContexts(h.qp, h.cabacInitIdc, h.isI())
		d.cab.start(r.pos)
	}
	mbAddr := h.firstMB
	if mbAddr >= total {
		return corrupt()
	}
	if d.cabacOn {
		for mbAddr < total {
			if !h.isI() && d.cabacSkipFlagAt(mbAddr) {
				d.decodeSkipMB(mbAddr)
			} else {
				if err := d.decodeMB(mbAddr); err != nil {
					return err
				}
			}
			mbAddr++
			if d.cab.terminate() != 0 {
				break
			}
			if d.cab.pos > r.end+64 {
				return corrupt()
			}
		}
		return nil
	}
	for mbAddr < total {
		if !h.isI() {
			run := int(r.ue())
			if run > total-mbAddr {
				return corrupt()
			}
			for i := 0; i < run; i++ {
				d.decodeSkipMB(mbAddr)
				mbAddr++
			}
			if run > 0 && !r.moreRBSPData() {
				break
			}
			if mbAddr >= total {
				break
			}
		}
		if err := d.decodeMB(mbAddr); err != nil {
			return err
		}
		mbAddr++
		if r.overrun() {
			return corrupt()
		}
		if !r.moreRBSPData() {
			break
		}
	}
	return nil
}

// cabacSkipFlagAt sets up neighbour availability for mbAddr and decodes
// mb_skip_flag.
func (d *Decoder) cabacSkipFlagAt(mbAddr int) bool {
	mbx, mby := mbAddr%d.mbW, mbAddr/d.mbW
	d.mbIdx = mbAddr
	d.availA = mbx > 0 && d.mbSlice[mbAddr-1] == d.curSlice
	d.availB = mby > 0 && d.mbSlice[mbAddr-d.mbW] == d.curSlice
	return d.cabacSkipFlag()
}

// setupImplicit computes implicit bi-prediction weights (8.4.2.3.1).
func (d *Decoder) setupImplicit(h *sliceHeader) {
	if h.pps.WeightedBipredIdc != 2 {
		return
	}
	cur := d.cur.poc
	for i, p0 := range d.refList[0] {
		for j, p1 := range d.refList[1] {
			w1 := 32
			td := clip3(-128, 127, p1.poc-p0.poc)
			if td != 0 && !p0.longRef && !p1.longRef {
				tb := clip3(-128, 127, cur-p0.poc)
				tx := (16384 + abs(td/2)) / td
				dsf := clip3(-1024, 1023, (tb*tx+32)>>6)
				if dsf>>2 >= -64 && dsf>>2 <= 128 {
					w1 = dsf >> 2
				}
			}
			d.implicitW[i][j] = int16(w1)
		}
	}
}
