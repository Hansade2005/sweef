package media

// eSBR element and channel processing, after libxaac's ixheaacd_sbr_dec.c
// and ixheaacd_sbrdecoder.c (Apache-2.0, see NOTICE).

// esbrChan is the persistent state of one SBR channel.
type esbrChan struct {
	ana *esbrAnalysis
	syn *esbrSynthesis

	qRe, qIm [][64]float32 // QMF samples of the core signal
	hRe, hIm [][64]float32 // generated high band
	pRe, pIm [][64]float32 // transposer output (HBE)

	frame esbrFrame
	prev  esbrPrevData

	bwPrev [esbrMaxNoiseCoeffs]float32
	patch  esbrPatch
	lim    esbrLim

	eGain, noiseBuf    [5][64]float32
	harmFlagPrev       [64]int8
	harmFlagVarlen     [64]int8
	harmFlagVarlenPrev [64]int8
	harmIndex          int
	phaseIndex         int
	envShortFlagPrev   int

	hbe     *esbrHBE
	qmapped [64][48]float32 // quantised noise floor per band and time slot (PVC)
	tmpOut  []float64
}

func (e *esbrElem) opDelay() int {
	if e.hdr.usf4 {
		return 12
	}
	return 6
}

func (e *esbrElem) codecXDelay() int {
	d := 0
	if e.hdr.hbe {
		d = 32
	}
	if e.hdr.usf4 {
		d *= 2
	}
	return d
}

// newESBRElem creates the SBR state of a USAC element. cfg is the stream
// configuration, ec the element configuration.
func newESBRElem(cfg *usacConfig, ec *usacElemCfg, nch int) (*esbrElem, error) {
	esbrAnaInit()
	sbrInitHuff()
	e := &esbrElem{nch: nch, pvc: newESBRPVCData()}
	h := &e.hdr
	h.usf4 = cfg.sbrRatioIdx == 1
	h.upsamp = 2
	if h.usf4 {
		h.upsamp = 4
	}
	h.outFs = cfg.rate
	h.hbe, h.pvc, h.interTes = ec.sbr.harmonic, ec.sbr.pvc, ec.sbr.interTES
	d := &e.dflt
	d.startFreq, d.stopFreq = ec.sbr.startFreq, ec.sbr.stopFreq
	d.freqScale, d.alterScale, d.noiseBands = ec.sbr.freqScale, ec.sbr.alterScale, ec.sbr.noiseBands
	d.limiterBands, d.limiterGains = ec.sbr.limiterBands, ec.sbr.limiterGains
	d.interpolFreq, d.smoothingMode = ec.sbr.interpolFreq, ec.sbr.smoothingMode
	h.subStart = 64
	h.qmfSbPrev = 0
	h.esbrStartUp, h.esbrStartPVC = true, true
	m := 32
	switch cfg.sbrRatioIdx {
	case 2:
		m = 24
	case 1:
		m = 16
	}
	slots := cfg.outFrame / 64
	rows := slots + 16 + 64 + 8
	for i := 0; i < nch; i++ {
		c := &esbrChan{}
		c.ana = newESBRAnalysis(m)
		c.syn = &esbrSynthesis{}
		c.qRe, c.qIm = make([][64]float32, rows), make([][64]float32, rows)
		c.hRe, c.hIm = make([][64]float32, rows), make([][64]float32, rows)
		c.pRe, c.pIm = make([][64]float32, rows), make([][64]float32, rows)
		c.frame.resetFlag = true
		c.tmpOut = make([]float64, cfg.outFrame)
		if h.hbe {
			c.hbe = newESBRHBE(cfg.coreLen, cfg.outFrame, h.usf4)
		}
		e.ch[i] = c
	}
	return e, nil
}

// parse reads the SBR data of the element (UsacSbrData) and decodes the
// envelopes. sbrCh is 1 for a mono SBR element, 2 for a channel pair.
func (e *esbrElem) parse(r *aacBits, indep bool, sbrCh int) error {
	h := &e.hdr
	e.nch = sbrCh
	changed, err := e.readHeader(r, indep)
	if err != nil {
		return err
	}
	if e.pvcPrevMode == 0 && h.pvcMode != 0 {
		e.pvc.prevPVCID = 0
	}
	e.pvcPrevMode = h.pvcMode
	if changed {
		if err := h.bandTables(); err != nil {
			h.syncActive = false
			return err
		}
		for i := 0; i < sbrCh; i++ {
			c := e.ch[i]
			c.frame.resetFlag = true
			if err := e.resetChannel(c); err != nil {
				return err
			}
		}
		h.status = false
		h.syncActive = true
	}
	for i := 0; i < sbrCh; i++ {
		c := e.ch[i]
		if e.hdr.pvcMode == 0 {
			c.frame.sbrMode = esbrOrig
		} else {
			c.frame.sbrMode = esbrPVCMode
		}
		c.frame.interTes = h.interTes
	}
	if !h.syncActive {
		return nil
	}
	if sbrCh == 2 {
		fr := [2]*esbrFrame{&e.ch[0].frame, &e.ch[1].frame}
		if err := e.readCPE(r, &fr, indep); err != nil {
			return err
		}
		e.decode(fr, [2]*esbrPrevData{&e.ch[0].prev, &e.ch[1].prev}, 2)
	} else {
		c := e.ch[0]
		if c.frame.sbrMode == esbrOrig {
			if err := e.readSCE(r, &c.frame, indep); err != nil {
				return err
			}
		} else if err := e.readPVCSCE(r, &c.frame, indep); err != nil {
			return err
		}
		e.decode([2]*esbrFrame{&c.frame, nil}, [2]*esbrPrevData{&c.prev, nil}, 1)
	}
	return nil
}

// resetChannel is the per-channel part of ixheaacd_sbr_dec_reset.
func (e *esbrElem) resetChannel(c *esbrChan) error {
	h := &e.hdr
	if !h.hbe {
		opd := 6 + esbrHfAdjOffset
		if h.usf4 {
			opd += 6
		}
		for l := 0; l < opd && l < len(c.qRe); l++ {
			for k := h.qmfSbPrev; k < h.subStart; k++ {
				c.qRe[l][k] = 0
				c.qIm[l][k] = 0
			}
		}
	}
	if c.hbe != nil {
		return e.hbePrime(c)
	}
	return nil
}

// hbePrime runs the transposer twice on the buffered QMF samples, as
// ixheaacd_sbr_dec_reset does, to prime its filter states.
func (e *esbrElem) hbePrime(c *esbrChan) error {
	h := &e.hdr
	if err := c.hbe.reinit(h); err != nil {
		return err
	}
	slots := c.hbe.noBins
	opd := 6 + esbrHfAdjOffset
	if h.usf4 {
		opd += 6
	}
	for k := 0; k < 2; k++ {
		if h.usf4 && k == 0 {
			continue
		}
		xpos := slots * k
		if h.usf4 {
			xpos = slots*k - 32
		}
		for i := 0; i < 8; i++ {
			c.pRe[i] = c.pRe[slots+i]
			c.pIm[i] = c.pIm[slots+i]
		}
		if err := c.hbe.apply(h, slots, c.qRe[opd+xpos:], c.qIm[opd+xpos:], c.pRe[opd:], c.pIm[opd:], c.frame.pitchInBins); err != nil {
			return err
		}
		if h.usf4 {
			esbrHbeReplSpec(c.hbe.xOver[:], c.pRe[opd:], c.pIm[opd:], slots, c.hbe.maxStretch)
		}
	}
	return nil
}

// process runs the SBR decoder on the core output of channel ci (16 bit
// units, coreLen samples) and returns outFrame samples in out.
func (e *esbrElem) process(ci int, core []float64, out []float64) error {
	sRe, sIm, err := e.processQMF(ci, core, len(out)/64, false)
	if err != nil {
		return err
	}
	e.ch[ci].syn.run(sRe, sIm, len(out)/64, out)
	return nil
}

// processQMF runs the SBR decoder up to the regrouped QMF domain signal of
// slots time slots. With mps set the output is prepared for an MPEG Surround
// upmix (stereoConfigIndex 1 and 2): the three lowest bands are taken six
// slots later, which the MPS hybrid analysis delay compensates.
func (e *esbrElem) processQMF(ci int, core []float64, slots int, mps bool) (sRe, sIm [][64]float32, err error) {
	h := &e.hdr
	c := e.ch[ci]
	f := &c.frame
	apply := h.syncActive
	if ci == 1 && !e.ch1Started {
		apply = false
	}
	if ci == 1 {
		e.ch1Started = true
	}
	m := c.ana.def.m
	opd := e.opDelay()
	cxd := e.codecXDelay()
	// history
	hist := opd + esbrHfAdjOffset + cxd
	copy(c.qRe[:hist], c.qRe[slots:slots+hist])
	copy(c.qIm[:hist], c.qIm[slots:slots+hist])
	h2 := opd + esbrHfAdjOffset
	copy(c.hRe[:h2], c.hRe[slots:slots+h2])
	copy(c.hIm[:h2], c.hIm[slots:slots+h2])
	if h.hbe {
		copy(c.pRe[:h2], c.pRe[slots:slots+h2])
		copy(c.pIm[:h2], c.pIm[slots:slots+h2])
	}
	// analysis
	row := opd + cxd + esbrHfAdjOffset
	re := make([]float32, 64)
	im := make([]float32, 64)
	for s := 0; s < slots; s++ {
		c.ana.slot(core[s*m:(s+1)*m], re, im)
		for k := 0; k < m; k++ {
			c.qRe[row+s][k] = re[k]
			c.qIm[row+s][k] = im[k]
		}
		for k := m; k < 64; k++ {
			c.qRe[row+s][k] = 0
			c.qIm[row+s][k] = 0
		}
	}
	if apply && h.hbe {
		hrow := opd + esbrHfAdjOffset
		if err := c.hbe.apply(h, slots, c.qRe[hrow+32:], c.qIm[hrow+32:], c.pRe[hrow:], c.pIm[hrow:], f.pitchInBins); err != nil {
			return nil, nil, err
		}
		if h.usf4 {
			esbrHbeReplSpec(c.hbe.xOver[:], c.pRe[hrow:], c.pIm[hrow:], slots, c.hbe.maxStretch)
		}
	}
	if apply {
		var phRe, phIm [][64]float32
		var xover []int
		if h.hbe {
			phRe, phIm = c.pRe, c.pIm
			xover = c.hbe.xOver[:]
		}
		if err := c.generateHF(f, h, c.qRe, c.qIm, phRe, phIm, c.hRe, c.hIm); err != nil {
			return nil, nil, err
		}
		pd := e.pvc
		pd.pvcRate = h.upsamp
		pvcOut := make([]float32, 16*64)
		if f.sbrMode == esbrPVCMode {
			ener := make([]float32, 512)
			esbrQmfEnrgCalc(c.qRe, c.qIm, slots, h.usf4, ener)
			if err := pd.process(h.subStart, f.pvcInfo.borderVec[0], ener, pvcOut); err != nil {
				return nil, nil, err
			}
			pd.prevPVCFlg = true
		} else {
			pd.prevPVCFlg = false
		}
		pd.prevFirstBndIdx = h.subStart
		pd.prevPVCRate = pd.pvcRate
		if err := c.envCalc(f, h, c.hRe[esbrHfAdjOffset:], c.hIm[esbrHfAdjOffset:],
			c.qRe[esbrHfAdjOffset:], c.qIm[esbrHfAdjOffset:], xover, pvcOut); err != nil {
			return nil, nil, err
		}
	} else {
		for i := range c.hRe {
			c.hRe[i] = [64]float32{}
			c.hIm[i] = [64]float32{}
		}
	}
	// regroup and synthesis
	xover := m
	stop := 0
	if apply {
		if h.usf4 {
			stop = 4 * f.info.borderVec[0]
		} else {
			stop = 2 * f.info.borderVec[0]
		}
		xover = h.qmfSbPrev
	}
	sRe = make([][64]float32, slots)
	sIm = make([][64]float32, slots)
	for i := 0; i < slots; i++ {
		if i == stop && apply {
			xover = h.subStart
		}
		if mps && i >= stop {
			xover = h.subStart
		}
		for k := 0; k < 64; k++ {
			switch {
			case mps && k < 3:
				sRe[i][k] = c.qRe[2+i+6][k]
				sIm[i][k] = c.qIm[2+i+6][k]
			case k < xover:
				sRe[i][k] = c.qRe[2+i][k]
				sIm[i][k] = c.qIm[2+i][k]
			default:
				sRe[i][k] = c.hRe[2+i][k]
				sIm[i][k] = c.hIm[2+i][k]
			}
		}
	}
	f.prevSbrMode = f.sbrMode
	if apply || !(ci == 1 && h.syncActive) {
		f.resetFlag = false
	}
	if apply {
		h.qmfSbPrev = h.subStart
	}
	return sRe, sIm, nil
}
