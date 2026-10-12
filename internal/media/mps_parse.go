package media

import (
	"errors"
	"fmt"
	"math"
)

// MPEG Surround 2-1-2 (USAC MPS212) side information parsing. The bitstream
// syntax is ISO/IEC 23003-3 5.2 (Mps212Data); the parameter handling follows
// the structure of libxaac's ixheaacd_mps_parse.c and ixheaacd_mps_dec.c
// (Apache-2.0, Ittiam Systems, see NOTICE).

const (
	mpsMaxBands  = 28
	mpsMaxSets   = 9
	mpsDiffFreq  = 0
	mpsDiffTime  = 1
	mpsFreqPair  = 0
	mpsTimePair  = 1
	mpsParamCLD  = 0
	mpsParamICC  = 1
	mpsMaxSlots  = 72
	mpsMaxHybrid = 71
)

var mpsFreqResTable = [8]int{0, 28, 20, 14, 10, 7, 5, 4}
var mpsStrideTable = [4]int{1, 2, 5, 28}
var mpsSmoothingTime = [4]int{64, 128, 256, 512}

var errMPSBitstream = errors.New("media: invalid mps212 data")

// mpsErr tags an invalid data error with the source line that detected it.
func mpsErr(line int) error { return fmt.Errorf("%w (mps_parse.go:%d)", errMPSBitstream, line) }

// mpsData is the per parameter type (CLD, ICC) state of the frame parser.
type mpsData struct {
	dataMode        [mpsMaxSets]int
	quantCoarse     [mpsMaxSets]int
	freqResStride   [mpsMaxSets]int
	quantCoarsePrev int
	quantCoarseFlag [mpsMaxSets]int
}

// mpsFrame holds the parsed parameters of one frame.
type mpsFrame struct {
	independency bool
	cld, icc     mpsData
	cmpCld       [mpsMaxSets][mpsMaxBands]int // per transmitted set
	cmpIcc       [mpsMaxSets][mpsMaxBands]int
	cmpCldPrev   [mpsMaxBands]int // last transmitted values (entropy coding history)
	cmpIccPrev   [mpsMaxBands]int
	cldIdx       [mpsMaxSets + 1][mpsMaxBands]int // mapped to parameter sets
	iccIdx       [mpsMaxSets + 1][mpsMaxBands]int
	cldIdxPre    [mpsMaxBands]int // last mapped values (for data modes 1, 2)
	iccIdxPre    [mpsMaxBands]int
	smoothMode   [mpsMaxSets]int
	smoothTime   [mpsMaxSets]int
	strideSmg    [mpsMaxSets]int
	smgData      [mpsMaxSets][mpsMaxBands]int
}

// mpsHuffRead walks a Huffman tree stored as flat [node][bit] pairs.
func mpsHuffRead(r *aacBits, tab []int16) int {
	node := 0
	for {
		b := r.bits(1)
		node = int(tab[node*2+b])
		if node <= 0 {
			return node
		}
		if r.err {
			return -1
		}
	}
}

// mpsHuffRead2D decodes a pair of 4 bit values (escape when node is 0).
func mpsHuffRead2D(r *aacBits, tab []int16, out *[2]int) (escape bool) {
	node := mpsHuffRead(r, tab)
	if node == 0 {
		return true
	}
	v := -(node + 1)
	out[0] = v >> 4
	out[1] = v & 0xf
	return false
}

func mpsSymRestore(r *aacBits, lav int, d *[2]int) {
	sum := d[0] + d[1]
	diff := d[0] - d[1]
	if sum > lav {
		d[0] = -sum + (2*lav + 1)
		d[1] = -diff
	} else {
		d[0] = sum
		d[1] = diff
	}
	if d[0]+d[1] != 0 {
		if r.bits(1) != 0 {
			d[0], d[1] = -d[0], -d[1]
		}
	}
	if d[0]-d[1] != 0 {
		if r.bits(1) != 0 {
			d[0], d[1] = d[1], d[0]
		}
	}
}

// mpsPCMDecode reads grouped PCM coded values. out1 and out2 may be nil.
func mpsPCMDecode(r *aacBits, out1, out2 []int, offset, numVal, numLevels int) {
	maxGrp := 1
	switch numLevels {
	case 3:
		maxGrp = 5
	case 7:
		maxGrp = 6
	case 11:
		maxGrp = 2
	case 13, 19, 51:
		maxGrp = 4
	case 25:
		maxGrp = 3
	case 4, 8, 15, 16, 26, 31:
		maxGrp = 1
	default:
		return
	}
	ld := float32(math.Log(float64(float32(numLevels))) / math.Log(2))
	var chunk [7]int
	for i := 1; i <= maxGrp; i++ {
		chunk[i] = int(math.Ceil(float64(float32(i) * ld)))
	}
	for i := 0; i < numVal; i += maxGrp {
		g := maxGrp
		if numVal-i < g {
			g = numVal - i
		}
		grp := r.bits(chunk[g])
		for j := 0; j < g; j++ {
			idx := i + (g - j - 1)
			next := grp % numLevels
			switch {
			case out2 == nil:
				out1[idx] = next - offset
			case out1 == nil:
				out2[idx] = next - offset
			default:
				if idx%2 != 0 {
					out2[idx/2] = next - offset
				} else {
					out1[idx/2] = next - offset
				}
			}
			grp = (grp - next) / numLevels
		}
	}
}

// mpsHuff1D decodes numVal values with a 1-D tree. The first value of a
// frequency differential vector uses the first band tree when p0 is set.
func mpsHuff1D(r *aacBits, tab, p0tab []int16, out []int, numVal int, p0 bool) {
	start := 0
	if p0 {
		out[0] = -(mpsHuffRead(r, p0tab) + 1)
		start = 1
	}
	for i := start; i < numVal; i++ {
		od := -(mpsHuffRead(r, tab) + 1)
		if od != 0 && r.bits(1) != 0 {
			od = -od
		}
		out[i] = od
	}
}

// mpsHuff2D decodes the pairs of a 2-D coded vector. tabs are the lav trees
// in order, lavBase and lavStep define the LAV from the index read from the
// stream: lav = lavStep*idx + lavBase.
func mpsHuff2D(r *aacBits, tabs [4][]int16, lavTab, p0tab []int16, lavBase int, out [][2]int, numVal, chFac int, p0a, p0b *int) {
	idx := -(mpsHuffRead(r, lavTab) + 1)
	lav := 2*idx + lavBase
	if p0a != nil {
		*p0a = -(mpsHuffRead(r, p0tab) + 1)
	}
	if p0b != nil {
		*p0b = -(mpsHuffRead(r, p0tab) + 1)
	}
	var tab []int16
	switch lavBase {
	case 3: // CLD: lav 3, 5, 7, 9
		tab = tabs[idx&3]
	case 1: // ICC: lav 1, 3, 5, 7
		tab = tabs[idx&3]
	}
	var escIdx [mpsMaxBands]int
	nesc := 0
	for i := 0; i < numVal; i += chFac {
		esc := mpsHuffRead2D(r, tab, &out[i])
		if esc {
			escIdx[nesc] = i
			nesc++
		} else {
			mpsSymRestore(r, lav, &out[i])
		}
	}
	if nesc > 0 {
		var e0, e1 [mpsMaxBands]int
		mpsPCMDecode(r, e0[:], e1[:], 0, 2*nesc, 2*lav+1)
		for i := 0; i < nesc; i++ {
			out[escIdx[i]][0] = e0[i] - lav
			out[escIdx[i]][1] = e1[i] - lav
		}
	}
}

func mpsCldTabs(dt, pairing int) (tabs [4][]int16) {
	base := (dt*2 + pairing) * 424
	t := mpsHuffCld2D[base : base+424]
	tabs[0] = t[0:30]
	tabs[1] = t[30:100]
	tabs[2] = t[100:226]
	tabs[3] = t[226:424]
	return
}

func mpsIccTabs(dt, pairing int) (tabs [4][]int16) {
	base := (dt*2 + pairing) * 232
	t := mpsHuffIcc2D[base : base+232]
	tabs[0] = t[0:6]
	tabs[1] = t[6:36]
	tabs[2] = t[36:106]
	tabs[3] = t[106:232]
	return
}

// mpsHuffDecode decodes one or two (pair) data vectors. cdg returns the
// coding scheme (bit 4: 2-D, bit 0: time pair).
func mpsHuffDecode(r *aacBits, out1, out2 []int, typ, dt1, dt2, numVal int) (cdg int) {
	cdg = r.bits(1) << 4
	if cdg>>4 == 1 {
		if out1 != nil && out2 != nil {
			cdg |= r.bits(1)
		}
	}
	var pairVec [mpsMaxBands + 1][2]int
	tab1d := func(dt int) []int16 {
		if typ == mpsParamCLD {
			return mpsHuffCld1D[dt*60 : dt*60+60]
		}
		return mpsHuffIcc1D[dt*14 : dt*14+14]
	}
	p0tab := mpsHuffPart0Cld[:]
	lavBase := 3
	if typ == mpsParamICC {
		p0tab = mpsHuffPart0Icc[:]
		lavBase = 1
	}
	tabs2d := func(dt, pairing int) [4][]int16 {
		if typ == mpsParamCLD {
			return mpsCldTabs(dt, pairing)
		}
		return mpsIccTabs(dt, pairing)
	}
	if cdg>>4 == 0 {
		if out1 != nil {
			mpsHuff1D(r, tab1d(dt1), p0tab, out1, numVal, dt1 == mpsDiffFreq)
		}
		if out2 != nil {
			mpsHuff1D(r, tab1d(dt2), p0tab, out2, numVal, dt2 == mpsDiffFreq)
		}
		return
	}
	if cdg&0xf == mpsFreqPair {
		n1, n2 := numVal, numVal
		o1, o2 := out1, out2
		var p0a, p0b *int
		rest1, rest2 := 0, 0
		if out1 != nil {
			if dt1 == mpsDiffFreq {
				p0a = &out1[0]
				n1--
				o1 = out1[1:]
			}
			rest1 = n1 % 2
			n1 -= rest1
		}
		if out2 != nil {
			if dt2 == mpsDiffFreq {
				p0b = &out2[0]
				n2--
				o2 = out2[1:]
			}
			rest2 = n2 % 2
			n2 -= rest2
		}
		if out1 != nil {
			mpsHuff2D(r, tabs2d(dt1, mpsFreqPair), mpsHuffLav[:], p0tab, lavBase, pairVec[:], n1, 2, p0a, nil)
			if rest1 != 0 {
				mpsHuff1D(r, tab1d(dt1), p0tab, o1[n1:], 1, false)
			}
		}
		if out2 != nil {
			mpsHuff2D(r, tabs2d(dt2, mpsFreqPair), mpsHuffLav[:], p0tab, lavBase, pairVec[1:], n2, 2, nil, p0b)
			if rest2 != 0 {
				mpsHuff1D(r, tab1d(dt2), p0tab, o2[n2:], 1, false)
			}
		}
		if out1 != nil {
			for i := 0; i < n1-1; i += 2 {
				o1[i] = pairVec[i][0]
				o1[i+1] = pairVec[i][1]
			}
		}
		if out2 != nil {
			for i := 0; i < n2-1; i += 2 {
				o2[i] = pairVec[i+1][0]
				o2[i+1] = pairVec[i+1][1]
			}
		}
		return
	}
	// time pair
	n1 := numVal
	o1, o2 := out1, out2
	var p0a, p0b *int
	if dt1 == mpsDiffFreq || dt2 == mpsDiffFreq {
		p0a, p0b = &out1[0], &out2[0]
		o1, o2 = out1[1:], out2[1:]
		n1--
	}
	dt := mpsDiffFreq
	if dt1 == mpsDiffTime || dt2 == mpsDiffTime {
		dt = mpsDiffTime
	}
	mpsHuff2D(r, tabs2d(dt, mpsTimePair), mpsHuffLav[:], p0tab, lavBase, pairVec[:], n1, 1, p0a, p0b)
	for i := 0; i < n1; i++ {
		o1[i] = pairVec[i][0]
		o2[i] = pairVec[i][1]
	}
	return
}

// mpsECDataPair decodes one pair (or single) of parameter sets into out
// (rows setIdx, setIdx+1). history holds the previous values.
func mpsECDataPair(r *aacBits, out *[mpsMaxSets][mpsMaxBands]int, history *[mpsMaxBands]int, typ, setIdx, dataBands int, pair, coarse, diffTimeBack bool) error {
	var levels, offset int
	switch typ {
	case mpsParamCLD:
		levels, offset = 31, 15
		if coarse {
			levels, offset = 15, 7
		}
	default:
		levels = 8
		if coarse {
			levels = 4
		}
	}
	var dataPair, dataDiff [2][mpsMaxBands]int
	pcm := r.bits(1) != 0
	if pcm {
		if pair {
			mpsPCMDecode(r, dataPair[0][:], dataPair[1][:], offset, 2*dataBands, levels)
		} else {
			mpsPCMDecode(r, dataPair[0][:], nil, offset, dataBands, levels)
		}
	} else {
		diffType := [2]int{mpsDiffFreq, mpsDiffFreq}
		if pair || diffTimeBack {
			diffType[0] = r.bits(1)
		}
		if pair && (diffType[0] == mpsDiffFreq || diffTimeBack) {
			diffType[1] = r.bits(1)
		}
		if dataBands <= 0 {
			return mpsErr(376)
		}
		var d1, d2 []int
		d1 = dataDiff[0][:]
		if pair {
			d2 = dataDiff[1][:]
		}
		cdg := mpsHuffDecode(r, d1, d2, typ, diffType[0], diffType[1], dataBands)
		forward := false
		if diffType[0] == mpsDiffTime || diffType[1] == mpsDiffTime {
			if pair {
				if diffType[0] == mpsDiffTime && !diffTimeBack {
					forward = true
				} else if diffType[1] == mpsDiffTime {
					forward = false
				} else {
					forward = r.bits(1) != 0
				}
			}
		}
		mixed := diffType[0] != diffType[1] && cdg&0xf == mpsTimePair
		freqDec := func(diff, o *[mpsMaxBands]int) {
			o[0] = diff[0]
			for i := 1; i < dataBands; i++ {
				o[i] = o[i-1] + diff[i]
			}
		}
		timeDec := func(prev, diff, o *[mpsMaxBands]int, sign int) {
			if mixed {
				o[0] = diff[0]
				for i := 1; i < dataBands; i++ {
					o[i] = prev[i] + sign*diff[i]
				}
			} else {
				for i := 0; i < dataBands; i++ {
					o[i] = prev[i] + sign*diff[i]
				}
			}
		}
		if !forward {
			if diffType[0] == mpsDiffFreq {
				freqDec(&dataDiff[0], &dataPair[0])
			} else {
				var msb [mpsMaxBands]int
				for i := 0; i < dataBands; i++ {
					msb[i] = history[i] + offset
				}
				timeDec(&msb, &dataDiff[0], &dataPair[0], 1)
			}
			if pair {
				if diffType[1] == mpsDiffFreq {
					freqDec(&dataDiff[1], &dataPair[1])
				} else {
					timeDec(&dataPair[0], &dataDiff[1], &dataPair[1], 1)
				}
			}
		} else {
			freqDec(&dataDiff[1], &dataPair[1])
			if diffType[0] == mpsDiffFreq {
				freqDec(&dataDiff[0], &dataPair[0])
			} else {
				timeDec(&dataPair[1], &dataDiff[0], &dataPair[0], -1)
			}
		}
		for i := 0; i < dataBands; i++ {
			dataPair[0][i] -= offset
		}
		if pair {
			for i := 0; i < dataBands; i++ {
				dataPair[1][i] -= offset
			}
		}
	}
	copy(out[setIdx][:dataBands], dataPair[0][:dataBands])
	if pair {
		copy(out[setIdx+1][:dataBands], dataPair[1][:dataBands])
	}
	return nil
}

func mpsStrideMap(freqResStride, bandStart, bandStop int, strides *[mpsMaxBands + 2]int) int {
	chFac := mpsStrideTable[freqResStride]
	dataBands := (bandStop-bandStart-1)/chFac + 1
	strides[0] = bandStart
	for pb := 1; pb <= dataBands; pb++ {
		strides[pb] = strides[pb-1] + chFac
	}
	startOffset := 0
	for strides[dataBands] > bandStop {
		if startOffset < dataBands {
			startOffset++
		} else {
			startOffset = 1
		}
		for i := startOffset; i <= dataBands; i++ {
			strides[i]--
		}
	}
	return dataBands
}

func mpsCoarse2Fine(data []int, typ int) {
	for i := range data {
		data[i] <<= 1
	}
	if typ == mpsParamCLD {
		for i := range data {
			if data[i] == -14 {
				data[i] = -15
			} else if data[i] == 14 {
				data[i] = 15
			}
		}
	}
}

func mpsFine2Coarse(data []int) {
	for i := range data {
		data[i] /= 2
	}
}

// ecData reads the entropy coded parameter sets of one type.
func (m *mps212) ecData(r *aacBits, typ int) error {
	f := &m.frame
	var d *mpsData
	var last *[mpsMaxBands]int
	var cmp *[mpsMaxSets][mpsMaxBands]int
	if typ == mpsParamCLD {
		d, last, cmp = &f.cld, &f.cmpCldPrev, &f.cmpCld
	} else {
		d, last, cmp = &f.icc, &f.cmpIccPrev, &f.cmpIcc
	}
	bandStop := m.paramBands
	for i := 0; i < m.numSets; i++ {
		d.dataMode[i] = r.bits(2)
	}
	setIndex := 0
	pair := false
	oldCoarse := d.quantCoarsePrev
	var strides [mpsMaxBands + 2]int
	for i := 0; i < m.numSets; i++ {
		if d.dataMode[i] == 0 {
			for pb := 0; pb < bandStop; pb++ {
				last[pb] = 0
			}
			oldCoarse = 0
		}
		if d.dataMode[i] != 3 {
			continue
		}
		if pair {
			pair = false
			continue
		}
		pair = r.bits(1) != 0
		d.quantCoarse[setIndex] = r.bits(1)
		d.freqResStride[setIndex] = r.bits(2)
		if d.quantCoarse[setIndex] != oldCoarse {
			if oldCoarse != 0 {
				mpsCoarse2Fine(last[:bandStop], typ)
			} else {
				mpsFine2Coarse(last[:bandStop])
			}
		}
		dataBands := mpsStrideMap(d.freqResStride[setIndex], 0, bandStop, &strides)
		for pb := 0; pb < dataBands; pb++ {
			last[pb] = last[strides[pb]]
		}
		if setIndex+1 >= mpsMaxSets && pair {
			return mpsErr(546)
		}
		if err := mpsECDataPair(r, cmp, last, typ, setIndex, dataBands, pair, d.quantCoarse[setIndex] != 0,
			!(f.independency && i == 0) || setIndex > 0); err != nil {
			return err
		}
		sel := setIndex
		if pair {
			sel++
		}
		for pb := 0; pb < dataBands; pb++ {
			for j := strides[pb]; j < strides[pb+1]; j++ {
				last[j] = cmp[sel][pb]
			}
		}
		oldCoarse = d.quantCoarse[setIndex]
		if pair {
			d.quantCoarse[setIndex+1] = d.quantCoarse[setIndex]
			d.freqResStride[setIndex+1] = d.freqResStride[setIndex]
			setIndex++
		}
		setIndex++
		if r.err {
			return mpsErr(569)
		}
	}
	return nil
}

// parseFrame reads Mps212Data (the frame payload that follows the SBR data).
func (m *mps212) parseFrame(r *aacBits, indep bool) error {
	f := &m.frame
	if !m.parseNext {
		return nil
	}
	m.numSetsPrev = m.numSets
	m.setFreqRes(m.cfg.freqRes)
	m.frameCount++
	if mpsTestFreqRes != nil {
		m.setFreqRes(mpsTestFreqRes(m.frameCount))
	}
	m.highRate = m.cfg.highRateMode
	if mpsTestHighRate != nil {
		m.highRate = mpsTestHighRate(m.frameCount)
	}
	frameType := 0
	if m.highRate {
		frameType = r.bits(1)
		m.numSets = r.bits(3) + 1
	} else {
		m.numSets = 1
	}
	bitsSlot := 0
	switch m.timeSlots {
	case 32:
		bitsSlot = 5
	case 64:
		bitsSlot = 6
	}
	if frameType != 0 {
		prev := -1
		for i := 0; i < m.numSets; i++ {
			m.paramSlots[i] = r.bits(bitsSlot)
			if prev >= m.paramSlots[i] || m.paramSlots[i] >= m.timeSlots {
				return mpsErr(602)
			}
			prev = m.paramSlots[i]
		}
	} else {
		for i := 0; i < m.numSets; i++ {
			m.paramSlots[i] = (m.timeSlots*(i+1)+m.numSets-1)/m.numSets - 1
		}
	}
	if !indep {
		f.independency = r.bits(1) != 0
	} else {
		f.independency = true
	}
	if err := m.ecData(r, mpsParamCLD); err != nil {
		return err
	}
	if err := m.ecData(r, mpsParamICC); err != nil {
		return err
	}
	if m.highRate {
		for ps := 0; ps < m.numSets; ps++ {
			f.smoothMode[ps] = r.bits(2)
			if f.smoothMode[ps] >= 2 {
				f.smoothTime[ps] = r.bits(2)
			}
			if f.smoothMode[ps] == 3 {
				f.strideSmg[ps] = r.bits(2)
				db := (m.paramBands-1)/mpsStrideTable[f.strideSmg[ps]] + 1
				for pg := 0; pg < db; pg++ {
					f.smgData[ps][pg] = r.bits(1)
				}
			}
		}
	} else {
		for ps := 0; ps < m.numSets; ps++ {
			f.smoothMode[ps] = 0
		}
	}
	m.tsdEnable = false
	switch m.cfg.tempShapeConfig {
	case 3:
		m.tsdEnable = r.bits(1) != 0
		if m.tsdEnable {
			return fmt.Errorf("%w: usac mps212 transient steering decorrelator", ErrUnsupported)
		}
	case 1, 2:
		if r.bits(1) != 0 {
			return fmt.Errorf("%w: usac mps212 temporal shaping", ErrUnsupported)
		}
	}
	if r.err {
		return mpsErr(654)
	}
	m.parseNext = false
	return nil
}

func mpsCreateMapping(bandStart, bandStop, chFac int, mp *[mpsMaxBands + 2]int) {
	input := bandStop - bandStart
	outBands := (input-1)/chFac + 1
	if outBands < 1 {
		outBands = 1
	}
	diff := input - outBands*chFac
	var vdk [mpsMaxBands + 2]int
	for i := 0; i < outBands; i++ {
		vdk[i] = chFac
	}
	incr, k := 1, 0
	if diff > 0 {
		incr, k = -1, outBands-1
	}
	for diff != 0 {
		vdk[k] -= incr
		k += incr
		diff += incr
		if k >= outBands {
			if diff > 0 {
				k = outBands - 1
			} else if diff < 0 {
				k = 0
			}
		}
	}
	mp[0] = bandStart
	for i := 0; i < outBands; i++ {
		mp[i+1] = mp[i] + vdk[i]
	}
}

func mpsBound(v, lo, hi int) int {
	if v > hi {
		v = hi
	}
	if v < lo {
		v = lo
	}
	return v
}

// mapIndexData expands the transmitted sets of one parameter type to
// idx[numSets+ext][band] (ISO/IEC 23003-1 5.3).
func (m *mps212) mapIndexData(d *mpsData, idx *[mpsMaxSets + 1][mpsMaxBands]int, cmp *[mpsMaxSets][mpsMaxBands]int, prev *[mpsMaxBands]int, typ int) error {
	bandStop := m.paramBands
	var interp [mpsMaxSets + 1]bool
	var mp [mpsMaxBands + 2]int
	setIndex := 0
	for i := 0; i < m.numSets; i++ {
		switch d.dataMode[i] {
		case 0:
			d.quantCoarseFlag[i] = 0
			for b := 0; b < bandStop; b++ {
				idx[i][b] = 0
				prev[b] = 0
			}
			d.quantCoarsePrev = 0
		case 1:
			copy(idx[i][:bandStop], prev[:bandStop])
			d.quantCoarseFlag[i] = d.quantCoarsePrev
		case 2:
			copy(idx[i][:bandStop], prev[:bandStop])
			d.quantCoarseFlag[i] = d.quantCoarsePrev
			interp[i] = true
		case 3:
			chFac := mpsStrideTable[d.freqResStride[setIndex]]
			dataBands := (bandStop-1)/chFac + 1
			mpsCreateMapping(0, bandStop, chFac, &mp)
			for k := 0; k < dataBands; k++ {
				v := cmp[setIndex][k]
				for j := mp[k]; j < mp[k+1]; j++ {
					idx[i][j] = v
				}
			}
			copy(prev[:bandStop], idx[i][:bandStop])
			d.quantCoarsePrev = d.quantCoarse[setIndex]
			d.quantCoarseFlag[i] = d.quantCoarse[setIndex]
			setIndex++
		}
	}
	for i := 0; i < m.numSets; i++ {
		if d.quantCoarseFlag[i] == 1 {
			mpsCoarse2Fine(idx[i][:bandStop], typ)
			d.quantCoarseFlag[i] = 0
		}
	}
	i1 := -1
	for i := 0; i < m.numSets; i++ {
		if !interp[i] {
			i1 = i
		}
		i2 := i
		for interp[i2] {
			i2++
		}
		x1 := 0
		if i1 == -1 {
			i1 = 0
		} else {
			x1 = m.paramSlots[i1]
		}
		xi := m.paramSlots[i]
		x2 := m.paramSlots[i2]
		if interp[i] {
			if i2 < m.numSets {
				return mpsErr(767)
			}
			for b := 0; b < bandStop; b++ {
				yi := 0
				y1, y2 := idx[i1][b], idx[i2][b]
				if x2 != x1 {
					yi = y1 + (xi-x1)*(y2-y1)/(x2-x1)
				}
				idx[i][b] = yi
			}
		}
	}
	for ps := 0; ps < m.numSets; ps++ {
		for b := 0; b < bandStop; b++ {
			if typ == mpsParamCLD {
				idx[ps][b] = mpsBound(idx[ps][b], -15, 15)
			} else {
				idx[ps][b] = mpsBound(idx[ps][b], 0, 7)
			}
		}
	}
	if m.extFrame {
		copy(idx[m.numSets][:bandStop], idx[m.numSets-1][:bandStop])
	}
	return nil
}

// mapSmoothing derives the per set smoothing parameters.
func (m *mps212) mapSmoothing() {
	f := &m.frame
	for ps := 0; ps < m.numSets; ps++ {
		switch f.smoothMode[ps] {
		case 0:
			m.smoothingTime[ps] = 256
			m.invSmoothingTime[ps] = 1.0 / 256.0
			for pb := 0; pb < m.paramBands; pb++ {
				m.smoothingData[ps][pb] = 0
			}
		case 1:
			if ps > 0 {
				m.smoothingTime[ps] = m.smoothingTime[ps-1]
				m.invSmoothingTime[ps] = m.invSmoothingTime[ps-1]
				m.smoothingData[ps] = m.smoothingData[ps-1]
			} else {
				m.smoothingTime[ps] = m.prevSmgTime
				m.invSmoothingTime[ps] = m.invPrevSmgTime
				m.smoothingData[ps] = m.prevSmgData
			}
		case 2:
			m.smoothingTime[ps] = mpsSmoothingTime[f.smoothTime[ps]]
			m.invSmoothingTime[ps] = 1 / float32(mpsSmoothingTime[f.smoothTime[ps]])
			for pb := 0; pb < m.paramBands; pb++ {
				m.smoothingData[ps][pb] = 1
			}
		case 3:
			m.smoothingTime[ps] = mpsSmoothingTime[f.smoothTime[ps]]
			m.invSmoothingTime[ps] = 1 / float32(mpsSmoothingTime[f.smoothTime[ps]])
			chFac := mpsStrideTable[f.strideSmg[ps]]
			dataBands := (m.paramBands-1)/chFac + 1
			var g2b [mpsMaxBands + 2]int
			mpsCreateMapping(0, m.paramBands, chFac, &g2b)
			for pg := 0; pg < dataBands; pg++ {
				for pb := g2b[pg]; pb < g2b[pg+1]; pb++ {
					m.smoothingData[ps][pb] = f.smgData[ps][pg]
				}
			}
		}
	}
	last := m.numSets - 1
	m.prevSmgTime = m.smoothingTime[last]
	m.invPrevSmgTime = m.invSmoothingTime[last]
	m.prevSmgData = m.smoothingData[last]
	if m.extFrame {
		m.smoothingTime[m.numSets] = m.smoothingTime[last]
		m.invSmoothingTime[m.numSets] = m.invSmoothingTime[last]
		m.smoothingData[m.numSets] = m.smoothingData[last]
	}
}

// mpsTestHook lets tests replace the frame parameters (cross checks against
// libxaac with parameter sets the encoder does not produce).
var mpsTestHook func(m *mps212)

// mpsTestHighRate lets tests switch the high rate syntax on per frame.
var mpsTestHighRate func(frame int) bool

// mpsTestFreqRes lets tests change the parameter band resolution per frame.
var mpsTestFreqRes func(frame int) int

// frameDecode maps the parsed data to the parameter sets of the frame.
func (m *mps212) frameDecode() error {
	if m.parseNext {
		return nil
	}
	f := &m.frame
	m.extFrame = m.paramSlots[m.numSets-1] != m.timeSlots-1
	if err := m.mapIndexData(&f.cld, &f.cldIdx, &f.cmpCld, &f.cldIdxPre, mpsParamCLD); err != nil {
		return err
	}
	if err := m.mapIndexData(&f.icc, &f.iccIdx, &f.cmpIcc, &f.iccIdxPre, mpsParamICC); err != nil {
		return err
	}
	m.mapSmoothing()
	if mpsTestHook != nil {
		mpsTestHook(m)
	}
	if m.extFrame {
		m.numSets++
		m.paramSlots[m.numSets-1] = m.timeSlots - 1
	}
	m.paramSlotDiff[0] = m.paramSlots[0] + 1
	if m.paramSlotDiff[0] > mpsMaxSlots {
		return mpsErr(873)
	}
	m.invParamSlotDiff[0] = 1 / float32(m.paramSlotDiff[0])
	for i := 1; i < m.numSets; i++ {
		m.paramSlotDiff[i] = m.paramSlots[i] - m.paramSlots[i-1]
		if m.paramSlotDiff[0]+m.paramSlotDiff[i] > mpsMaxSlots || m.paramSlotDiff[i] == 0 {
			return mpsErr(879)
		}
		m.invParamSlotDiff[i] = 1 / float32(m.paramSlotDiff[i])
	}
	return nil
}
