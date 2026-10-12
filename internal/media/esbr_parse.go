package media

import "fmt"

// eSBR bitstream parsing (UsacSbrData of ISO/IEC 23003-3), after libxaac's
// ixheaacd_env_extr.c (Apache-2.0, see NOTICE).

const (
	esbrFixFix = 0
	esbrFixVar = 1
	esbrVarFix = 2
	esbrVarVar = 3

	esbrDirFreq = 0
	esbrDirTime = 1

	esbrOrig    = 0
	esbrPVCMode = 1
	esbrUnknown = 2

	esbrCouplingOff   = 0
	esbrCouplingLevel = 1
	esbrCouplingBal   = 2
)

type esbrFrameInfo struct {
	frameClass     int
	numEnv         int
	transientEnv   int
	numNoiseEnv    int
	borderVec      [esbrMaxEnvelopes + 1]int
	freqRes        [esbrMaxEnvelopes]int
	noiseBorderVec [esbrMaxNoiseEnv + 1]int
}

// esbrPrevData is ia_sbr_prev_frame_data_struct.
type esbrPrevData struct {
	sfbNrgPrev    [esbrMaxFreqCoeffs]int
	prevNoise     [esbrMaxNoiseCoeffs]int
	invfMode      [esbrMaxNoiseCoeffs]int
	endPosition   int
	couplingMode  int
	ampRes        int
	maxQmfSubband int
}

// esbrFrame is ia_sbr_frame_info_data_struct (one channel of an SBR frame).
type esbrFrame struct {
	info, pvcInfo, prevInfo esbrFrameInfo
	numTimeSlots            int

	delCodDir      [esbrMaxEnvelopes]int
	delCodDirNoise [esbrMaxEnvelopes]int

	intEnv   [esbrMaxEnvelopes * esbrMaxFreqCoeffs]int
	fltEnv   [esbrMaxEnvelopes * esbrMaxFreqCoeffs]float32
	intNoise [esbrMaxNoiseEnv * esbrMaxNoiseCoeffs]int
	fltNoise [esbrMaxNoiseEnv * esbrMaxNoiseCoeffs]float32
	// prevNoise is frame_data->prev_noise_level (float)
	prevNoise [esbrMaxNoiseEnv * esbrMaxNoiseCoeffs]float32

	numEnvSfac, numNoiseSfac int

	invfMode, invfModePrev [esbrMaxNoiseCoeffs]int
	addHarmonics           [esbrMaxFreqCoeffs]int
	couplingMode           int
	ampRes                 int

	patchingMode, prevPatchingMode int
	overSampling, pitchInBins      int
	interTes                       bool
	interTesMode                   [esbrMaxEnvelopes]int
	sbrMode, prevSbrMode           int

	// PVC
	varLen, varLenIDPrev                             int
	pvcMode                                          int
	sinStartCur, sinLenCur, sinStartNext, sinLenNext int
	sinePosition                                     int

	// side information of the envelope adjuster
	resetFlag bool
}

// esbrElem is the SBR processing of one USAC element (1 or 2 channels).
type esbrElem struct {
	hdr  esbrHeader
	dflt esbrHeader // default header of the config
	ch   [2]*esbrChan
	nch  int

	pvc          *esbrPVCData
	pvcPrevMode  int
	synced       bool
	patchedStart bool
	ch1Started   bool // libxaac leaves the second channel of a pair unprocessed in the first frame
}

func (r *aacBits) esbrHuff(table int) int { return r.vlc(sbrVLCs[table]) }

// readHeader is ixheaacd_sbr_read_header_data for the USAC case. It returns
// true when the band tables changed (SBR_RESET).
func (e *esbrElem) readHeader(r *aacBits, indep bool) (reset bool, err error) {
	h := &e.hdr
	prev := *h
	infoPresent, headerPresent := true, true
	if !indep {
		infoPresent = r.bit()
		if infoPresent {
			headerPresent = r.bit()
		} else {
			headerPresent = false
		}
	}
	if infoPresent {
		h.ampRes = r.bits(1)
		h.xoverBand = r.bits(4)
		h.preProc = r.bits(1)
		if h.pvc {
			h.pvcMode = r.bits(2)
		} else {
			h.pvcMode = 0
		}
	}
	useDflt := false
	if headerPresent {
		useDflt = r.bit()
		if useDflt {
			d := &e.dflt
			h.startFreq, h.stopFreq = d.startFreq, d.stopFreq
			h.headerExtra1, h.headerExtra2 = d.headerExtra1, d.headerExtra2
			h.freqScale, h.alterScale, h.noiseBands = d.freqScale, d.alterScale, d.noiseBands
			h.limiterBands, h.limiterGains = d.limiterBands, d.limiterGains
			h.interpolFreq, h.smoothingMode = d.interpolFreq, d.smoothingMode
		} else {
			h.startFreq = r.bits(4)
			h.stopFreq = r.bits(4)
			h.headerExtra1 = r.bits(1)
			h.headerExtra2 = r.bits(1)
		}
	}
	if !useDflt && headerPresent {
		if h.headerExtra1 != 0 {
			h.freqScale = r.bits(2)
			h.alterScale = r.bits(1)
			h.noiseBands = r.bits(2)
		} else {
			h.freqScale, h.alterScale, h.noiseBands = 2, 1, 2
		}
		if h.headerExtra2 != 0 {
			h.limiterBands = r.bits(2)
			h.limiterGains = r.bits(2)
			h.interpolFreq = r.bits(1)
			h.smoothingMode = r.bits(1)
		} else {
			h.limiterBands, h.limiterGains, h.interpolFreq, h.smoothingMode = 2, 2, 1, 1
		}
	}
	if r.err {
		return false, fmt.Errorf("media: usac sbr header truncated")
	}
	changed := !prev.syncActive || prev.startFreq != h.startFreq || prev.stopFreq != h.stopFreq ||
		prev.xoverBand != h.xoverBand || prev.freqScale != h.freqScale ||
		prev.alterScale != h.alterScale || prev.noiseBands != h.noiseBands
	return changed, nil
}

func esbrPointerBits(numRel int) int {
	return [7]int{1, 2, 2, 3, 3, 3, 3}[numRel]
}

// timeFreqGrid is ixheaacd_sbr_time_freq_grid_info (16 time slots).
func (e *esbrElem) timeFreqGrid(r *aacBits, f *esbrFrame) bool {
	p := &f.info
	const slots = 16
	class := r.bits(2)
	p.frameClass = class
	var numEnv int
	switch class {
	case esbrFixFix:
		tmp := r.bits(3)
		n := (tmp & 6) >> 1
		numEnv = 1 << uint(n)
		*p = esbrFrameInfo{frameClass: esbrFixFix, numEnv: numEnv, transientEnv: -1}
		for i := 0; i <= numEnv; i++ {
			p.borderVec[i] = i * slots / numEnv
		}
		fr := 1
		if tmp&1 == 0 {
			fr = 0
		}
		for i := 0; i < numEnv; i++ {
			p.freqRes[i] = fr
		}
		if numEnv == 1 {
			p.numNoiseEnv = 1
			p.noiseBorderVec = [3]int{0, slots, 0}
		} else {
			p.numNoiseEnv = 2
			p.noiseBorderVec = [3]int{0, slots / 2, slots}
		}
		return true
	case esbrFixVar:
		v := r.bits(4)
		numRel := v & 3
		varBord := v >> 2
		numEnv = numRel + 1
		p.borderVec[0] = 0
		border := varBord + slots
		p.borderVec[numEnv] = border
		for k := numRel; k > 0; k-- {
			t := r.bits(2)
			border -= (t << 1) + 2
			if border < 0 {
				border = 0
			}
			p.borderVec[k] = border
		}
		ptr := r.bits(esbrPointerBits(numRel))
		if ptr-(numRel+1) > 0 {
			return false
		}
		for k := numRel; k >= 0; k-- {
			p.freqRes[k] = r.bits(1)
		}
		if ptr != 0 {
			p.transientEnv = numEnv + 1 - ptr
		} else {
			p.transientEnv = -1
		}
		if ptr == 0 || ptr == 1 {
			p.noiseBorderVec[1] = p.borderVec[numRel]
		} else {
			p.noiseBorderVec[1] = p.borderVec[p.transientEnv]
		}
	case esbrVarFix:
		v := r.bits(4)
		numRel := v & 3
		varBord := v >> 2
		numEnv = numRel + 1
		border := varBord
		p.borderVec[0] = border
		k := 1
		for ; k <= numRel; k++ {
			t := r.bits(2)
			border += (t << 1) + 2
			if border > slots {
				border = slots
			}
			p.borderVec[k] = border
		}
		p.borderVec[k] = slots
		ptr := r.bits(esbrPointerBits(numRel))
		if ptr-(numRel+1) > 0 {
			return false
		}
		if ptr == 0 || ptr-1 == 0 {
			p.transientEnv = -1
		} else {
			p.transientEnv = ptr - 1
		}
		for k := 0; k <= numRel; k++ {
			p.freqRes[k] = r.bits(1)
		}
		switch ptr {
		case 0:
			p.noiseBorderVec[1] = p.borderVec[1]
		case 1:
			p.noiseBorderVec[1] = p.borderVec[numRel]
		default:
			p.noiseBorderVec[1] = p.borderVec[p.transientEnv]
		}
	case esbrVarVar:
		v := r.bits(8)
		absTrail := ((v & 0x30) >> 4) + slots
		numRelTrail := (v & 0xc) >> 2
		numRelLead := v & 3
		absLead := v >> 6
		numEnv = numRelTrail + numRelLead + 1
		border := absLead
		p.borderVec[0] = border
		for k := 1; k <= numRelTrail; k++ {
			t := r.bits(2)
			border += (t << 1) + 2
			p.borderVec[k] = border
		}
		border = absTrail
		i := numEnv
		p.borderVec[i] = border
		for k := 0; k < numRelLead; k++ {
			t := r.bits(2)
			border -= (t << 1) + 2
			i--
			p.borderVec[i] = border
		}
		ptr := r.bits(esbrPointerBits(numRelTrail + numRelLead))
		if ptr-(numRelTrail+numRelLead+1) > 0 {
			return false
		}
		if ptr != 0 {
			p.transientEnv = numEnv + 1 - ptr
		} else {
			p.transientEnv = -1
		}
		for k := 0; k < numEnv; k++ {
			p.freqRes[k] = r.bits(1)
		}
		p.noiseBorderVec[0] = absLead
		if numEnv == 1 {
			p.noiseBorderVec[1] = absTrail
		} else {
			if ptr == 0 || ptr-1 == 0 {
				p.noiseBorderVec[1] = p.borderVec[numEnv-1]
			} else {
				p.noiseBorderVec[1] = p.borderVec[p.transientEnv]
			}
			p.noiseBorderVec[2] = absTrail
		}
	}
	p.numEnv = numEnv
	if numEnv == 1 {
		p.numNoiseEnv = 1
	} else {
		p.numNoiseEnv = 2
	}
	if class == esbrVarFix || class == esbrFixVar {
		p.noiseBorderVec[0] = p.borderVec[0]
		p.noiseBorderVec[p.numNoiseEnv] = p.borderVec[numEnv]
	}
	return true
}

func esbrValidateFrameInfo(p *esbrFrameInfo) bool {
	const timeSlots, ovSlots = 16, 3
	n := p.numEnv
	if n < 1 || n > esbrMaxEnvelopes || p.numNoiseEnv > esbrMaxNoiseEnv {
		return false
	}
	start, end := p.borderVec[0], p.borderVec[n]
	if p.transientEnv > n {
		return false
	}
	startN, endN := p.noiseBorderVec[0], p.noiseBorderVec[p.numNoiseEnv]
	if start < 0 || start >= end || start > ovSlots {
		return false
	}
	if end < timeSlots || end > timeSlots+ovSlots {
		return false
	}
	for i := 0; i < n; i++ {
		if p.borderVec[i] > p.borderVec[i+1] {
			return false
		}
	}
	if n == 1 && p.numNoiseEnv > 1 {
		return false
	}
	if start != startN || end != endN {
		return false
	}
	for i := 0; i < p.numNoiseEnv; i++ {
		ok := false
		for j := 0; j < n; j++ {
			if p.borderVec[j] == p.noiseBorderVec[i] {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// pvcTimeFreqGrid is ixheaacd_pvc_time_freq_grid_info.
func (e *esbrElem) pvcTimeFreqGrid(r *aacBits, f *esbrFrame) bool {
	var timeBorder, pvcBorder [esbrMaxEnvelopes + 1]int
	var timeBorderNoise, pvcBorderNoise [3]int
	var freqRes [esbrMaxEnvelopes + 1]int
	p := &f.info
	pvc := &f.pvcInfo
	prevMode := f.prevSbrMode
	noisePos := r.bits(4)
	if r.bit() {
		f.varLen = r.bits(2) + 1
		if f.varLen > 3 {
			return false
		}
	} else {
		f.varLen = 0
	}
	varLen := f.varLen
	if p.numEnv > 0 {
		timeBorder[0] = p.borderVec[p.numEnv] - 16
	} else {
		timeBorder[0] = 0
	}
	if timeBorder[0] < 0 {
		return false
	}
	pvcBorder[0] = 0
	freqRes[0] = 0
	if f.prevSbrMode == 0 {
		pvcBorder[0] = timeBorder[0]
	}
	var numNoise, numEnv int
	if noisePos == 0 {
		timeBorder[1] = 16 + varLen
		pvcBorder[1] = 16
		numNoise, numEnv = 1, 1
	} else {
		timeBorder[1] = noisePos
		pvcBorder[1] = noisePos
		timeBorder[2] = 16 + varLen
		pvcBorder[2] = 16
		freqRes[1] = 0
		numNoise, numEnv = 2, 2
	}
	for i := 0; i < 3; i++ {
		timeBorderNoise[i] = timeBorder[i]
		pvcBorderNoise[i] = pvcBorder[i]
	}
	if prevMode == esbrOrig {
		pvcBorder[0] = timeBorder[0]
		pvcBorderNoise[0] = timeBorder[0]
	}
	pvc.numEnv = numEnv
	for i := 0; i <= numEnv; i++ {
		pvc.borderVec[i] = pvcBorder[i]
	}
	for i := 0; i < numEnv; i++ {
		pvc.freqRes[i] = freqRes[i]
	}
	pvc.transientEnv = -1
	pvc.numNoiseEnv = numNoise
	for i := 0; i <= numNoise; i++ {
		pvc.noiseBorderVec[i] = pvcBorderNoise[i]
	}
	p.numEnv = numEnv
	for i := 0; i <= numEnv; i++ {
		p.borderVec[i] = timeBorder[i]
	}
	for i := 0; i < numEnv; i++ {
		p.freqRes[i] = freqRes[i]
	}
	p.transientEnv = -1
	p.numNoiseEnv = numNoise
	for i := 0; i <= numNoise; i++ {
		p.noiseBorderVec[i] = timeBorderNoise[i]
	}
	return true
}

// readDtdf is ixheaacd_sbr_env_dtdf_data for the USAC case.
func (e *esbrElem) readDtdf(r *aacBits, f *esbrFrame, indep bool) {
	n := f.info.numEnv
	nn := f.info.numNoiseEnv
	k := 0
	if indep {
		f.delCodDir[0] = 0
	} else {
		f.delCodDir[0] = r.bits(1)
	}
	k = 1
	for i := n - 1; i >= 1; i-- {
		f.delCodDir[k] = r.bits(1)
		k++
	}
	k = 0
	if indep {
		f.delCodDirNoise[0] = 0
	} else {
		f.delCodDirNoise[0] = r.bits(1)
	}
	k = 1
	for i := nn - 1; i >= 1; i-- {
		f.delCodDirNoise[k] = r.bits(1)
		k++
	}
}

func esbrHuffTables(coupling int, ampRes int, noise bool) (ht, hf, lav, startBits, startBitsBal, comp int) {
	if noise {
		startBits, startBitsBal = 5, 5
		if coupling == esbrCouplingBal {
			return hTNoiseBal30, hFEnvBal30, 12, startBits, startBitsBal, 1
		}
		return hTNoise30, hFEnv30, 31, startBits, startBitsBal, 0
	}
	if ampRes == 1 {
		startBits, startBitsBal = 6, 5
	} else {
		startBits, startBitsBal = 7, 6
	}
	if coupling == esbrCouplingBal {
		if ampRes == 0 {
			return hTEnvBal15, hFEnvBal15, 24, startBits, startBitsBal, 1
		}
		return hTEnvBal30, hFEnvBal30, 12, startBits, startBitsBal, 1
	}
	if ampRes == 0 {
		return hTEnv15, hFEnv15, 60, startBits, startBitsBal, 0
	}
	return hTEnv30, hFEnv30, 31, startBits, startBitsBal, 0
}

// readEnvData is ixheaacd_read_env_data. noise selects the noise floor arrays.
func (e *esbrElem) readEnvData(r *aacBits, f *esbrFrame, noise bool, noBand []int, numEnv int) {
	ht, hf, lav, startBits, startBitsBal, comp := esbrHuffTables(f.couplingMode, f.ampRes, noise)
	dir := f.delCodDir[:]
	if noise {
		dir = f.delCodDirNoise[:]
	}
	var bits, shift int
	if f.couplingMode == esbrCouplingBal {
		bits, shift = startBitsBal, comp
	} else {
		bits, shift = startBits, 0
	}
	off := 0
	for j := 0; j < numEnv; j++ {
		d := dir[j]
		var tab *sbrVLC
		if d == esbrDirFreq {
			v := r.bits(bits) << uint(shift)
			if noise {
				f.intNoise[off] = v
				f.fltNoise[off] = float32(v)
			} else {
				f.intEnv[off] = v
				f.fltEnv[off] = float32(v)
			}
			tab = sbrVLCs[hf]
		} else {
			tab = sbrVLCs[ht]
		}
		for i := 1 - d; i < noBand[j]; i++ {
			idx := r.vlc(tab)
			delta := (idx - lav) << uint(comp)
			if noise {
				f.intNoise[off+i] = delta
				f.fltNoise[off+i] = float32(delta)
			} else {
				f.intEnv[off+i] = delta
				f.fltEnv[off+i] = float32(delta)
			}
		}
		if !noise {
			f.interTesMode[j] = 0
			if f.interTes {
				if r.bit() {
					f.interTesMode[j] = r.bits(2)
				}
			}
		}
		off += noBand[j]
	}
}

// readEnvelope is ixheaacd_read_sbr_env_data.
func (e *esbrElem) readEnvelope(r *aacBits, f *esbrFrame) bool {
	h := &e.hdr
	ampRes := h.ampRes
	numEnv := f.info.numEnv
	f.numEnvSfac = 0
	if f.info.frameClass == esbrFixFix && numEnv == 1 {
		ampRes = 0
	}
	f.ampRes = ampRes
	var noBand [esbrMaxEnvelopes]int
	for i := 0; i < numEnv; i++ {
		noBand[i] = h.nSf[f.info.freqRes[i]]
		f.numEnvSfac += noBand[i]
	}
	if f.numEnvSfac > esbrMaxEnvelopes*esbrMaxFreqCoeffs {
		return false
	}
	e.readEnvData(r, f, false, noBand[:], numEnv)
	return true
}

// readNoise is ixheaacd_read_sbr_noise_floor_data.
func (e *esbrElem) readNoise(r *aacBits, f *esbrFrame) {
	numNoise := f.info.numNoiseEnv
	var nb [esbrMaxNoiseEnv]int
	for i := 0; i < numNoise; i++ {
		nb[i] = e.hdr.nNf
	}
	e.readEnvData(r, f, true, nb[:], numNoise)
}

func (e *esbrElem) readSinCoding(r *aacBits, f *esbrFrame) {
	for i := 0; i < e.hdr.nSf[1]; i++ {
		f.addHarmonics[i] = r.bits(1)
	}
}

// readPatching reads the harmonic transposer side information.
func readESBRPatching(r *aacBits, f *esbrFrame) {
	f.patchingMode = r.bits(1)
	if f.patchingMode == 0 {
		f.overSampling = r.bits(1)
		if r.bit() {
			f.pitchInBins = r.bits(7)
		} else {
			f.pitchInBins = 0
		}
	} else {
		f.overSampling, f.pitchInBins = 0, 0
	}
}

// readSCE is ixheaacd_sbr_read_sce for the USAC case.
func (e *esbrElem) readSCE(r *aacBits, f *esbrFrame, indep bool) error {
	h := &e.hdr
	f.couplingMode = esbrCouplingOff
	if h.hbe {
		readESBRPatching(r, f)
	}
	f.numTimeSlots = 16
	if !e.timeFreqGrid(r, f) {
		return fmt.Errorf("media: usac sbr bad frame grid")
	}
	if !esbrValidateFrameInfo(&f.info) {
		return fmt.Errorf("media: usac sbr invalid frame info")
	}
	f.prevSbrMode = esbrOrig
	e.readDtdf(r, f, indep)
	for i := 0; i < h.nNf; i++ {
		f.invfModePrev[i] = f.invfMode[i]
		f.invfMode[i] = r.bits(2)
	}
	if !e.readEnvelope(r, f) {
		return fmt.Errorf("media: usac sbr too many envelope values")
	}
	e.readNoise(r, f)
	for i := range f.addHarmonics {
		f.addHarmonics[i] = 0
	}
	f.couplingMode = esbrCouplingOff
	if r.bit() {
		e.readSinCoding(r, f)
	}
	if r.err {
		return fmt.Errorf("media: usac sbr truncated")
	}
	return nil
}

// readCPE is ixheaacd_sbr_read_cpe for the USAC case.
func (e *esbrElem) readCPE(r *aacBits, f *[2]*esbrFrame, indep bool) error {
	h := &e.hdr
	f0, f1 := f[0], f[1]
	if r.bit() { // bs_coupling
		if h.hbe {
			readESBRPatching(r, f0)
			f1.patchingMode, f1.overSampling, f1.pitchInBins = f0.patchingMode, f0.overSampling, f0.pitchInBins
		}
		f0.couplingMode = esbrCouplingLevel
		f1.couplingMode = esbrCouplingBal
	} else {
		if h.hbe {
			readESBRPatching(r, f0)
			readESBRPatching(r, f1)
		}
		f0.couplingMode = esbrCouplingOff
		f1.couplingMode = esbrCouplingOff
	}
	numCh := 2
	for i := 0; i < numCh; i++ {
		f[i].numTimeSlots = 16
		if !e.timeFreqGrid(r, f[i]) {
			return fmt.Errorf("media: usac sbr bad frame grid")
		}
		if !esbrValidateFrameInfo(&f[i].info) {
			return fmt.Errorf("media: usac sbr invalid frame info")
		}
		if f0.couplingMode != 0 {
			f1.info = f0.info
			numCh = 1
		}
	}
	if f0.couplingMode != 0 {
		e.readDtdf(r, f0, indep)
		e.readDtdf(r, f1, indep)
		for i := 0; i < h.nNf; i++ {
			f0.invfModePrev[i] = f0.invfMode[i]
			f1.invfModePrev[i] = f1.invfMode[i]
			f0.invfMode[i] = r.bits(2)
			f1.invfMode[i] = f0.invfMode[i]
		}
		if !e.readEnvelope(r, f0) {
			return fmt.Errorf("media: usac sbr too many envelope values")
		}
		e.readNoise(r, f0)
		if !e.readEnvelope(r, f1) {
			return fmt.Errorf("media: usac sbr too many envelope values")
		}
		e.readNoise(r, f1)
		for i := 0; i < h.nSf[1]; i++ {
			f0.addHarmonics[i], f1.addHarmonics[i] = 0, 0
		}
	} else {
		e.readDtdf(r, f0, indep)
		e.readDtdf(r, f1, indep)
		for k := 0; k < numCh; k++ {
			for i := 0; i < h.nNf; i++ {
				f[k].invfModePrev[i] = f[k].invfMode[i]
				f[k].invfMode[i] = r.bits(2)
			}
		}
		if !e.readEnvelope(r, f0) || !e.readEnvelope(r, f1) {
			return fmt.Errorf("media: usac sbr too many envelope values")
		}
		e.readNoise(r, f0)
		e.readNoise(r, f1)
	}
	for k := 0; k < 2; k++ {
		if r.bit() {
			e.readSinCoding(r, f[k])
		} else {
			for i := range f[k].addHarmonics {
				f[k].addHarmonics[i] = 0
			}
		}
	}
	if r.err {
		return fmt.Errorf("media: usac sbr truncated")
	}
	return nil
}
