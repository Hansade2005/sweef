package media

import (
	"fmt"
	"math"
	"sort"
)

// USAC enhanced SBR (eSBR, ISO/IEC 23003-3 7.5): header handling and the
// frequency band tables. The tables are computed with the integer arithmetic
// of libxaac's ixheaacd_freq_sca.c (Apache-2.0, see NOTICE), so that the band
// borders agree with the reference decoder bit for bit.

const (
	esbrMaxFreqCoeffs    = 56
	esbrMaxFreqCoeffsSBR = 48
	esbrMaxFreq44100     = 35
	esbrMaxFreq48000     = 32
	esbrMaxNoiseCoeffs   = 5
	esbrMaxNoiseEnv      = 2
	esbrMaxEnvelopes     = 8
	esbrMaxPatches       = 6
	esbrMaxOctave        = 29
	esbrMaxSecondRegion  = 50
	esbrHfAdjOffset      = 2
)

// esbrHeader holds the SBR header data and the derived frequency tables
// (ia_sbr_header_data_struct, ia_freq_band_data_struct).
type esbrHeader struct {
	ampRes, startFreq, stopFreq, xoverBand int
	preProc, pvcMode                       int
	headerExtra1, headerExtra2             int
	freqScale, alterScale, noiseBands      int
	limiterBands, limiterGains             int
	interpolFreq, smoothingMode            int

	// configuration
	usf4     bool // 4:1 ratio
	upsamp   int  // 2 or 4
	outFs    int  // output sampling rate used by the band tables
	hbe, pvc bool // harmonic transposition / PVC enabled in the config
	interTes bool

	// derived tables
	fMaster      [esbrMaxFreqCoeffs + 1]int
	numMF        int
	fLow, fHigh  [esbrMaxFreqCoeffs + 1]int
	nSf          [2]int
	nNf          int
	noiseTbl     [esbrMaxNoiseCoeffs + 2]int
	subStart     int
	subEnd       int
	qmfSbPrev    int
	status       bool // tables recomputed, QMF state must be reset
	syncActive   bool
	esbrStartUp  bool
	esbrStartPVC bool
}

func shl32(a int32, n uint) int32 {
	return a << n
}

func shl32Sat(a int32, n uint) int32 {
	r := int64(a) << n
	if r > math.MaxInt32 {
		return math.MaxInt32
	}
	if r < math.MinInt32 {
		return math.MinInt32
	}
	return int32(r)
}

func add32Sat(a, b int32) int32 {
	r := int64(a) + int64(b)
	if r > math.MaxInt32 {
		return math.MaxInt32
	}
	if r < math.MinInt32 {
		return math.MinInt32
	}
	return int32(r)
}

func sub32Sat(a, b int32) int32 {
	r := int64(a) - int64(b)
	if r > math.MaxInt32 {
		return math.MaxInt32
	}
	if r < math.MinInt32 {
		return math.MinInt32
	}
	return int32(r)
}

// esbrCalcFreqRatio is ixheaacd_calc_freq_ratio.
func esbrCalcFreqRatio(kStart, kStop, numBands int) int16 {
	bandfactor := int32(0x3f000000)
	step := int32(0x20000000)
	direction := 1
	start := shl32(int32(int16(kStart)), 24)
	stop := shl32(int32(int16(kStop)), 24)
	i := 0
	for {
		i++
		temp := stop
		for j := 0; j < numBands; j++ {
			p := int64(int16(temp>>16)) * int64(int16(bandfactor>>16))
			temp = shl32Sat(int32(p), 1)
		}
		if temp < start {
			if direction == 0 {
				step >>= 1
			}
			direction = 1
			bandfactor = add32Sat(bandfactor, step)
		} else {
			if direction == 1 {
				step >>= 1
			}
			direction = 0
			bandfactor = sub32Sat(bandfactor, step)
		}
		if i > 100 {
			step = 0
		}
		if step <= 0 {
			break
		}
	}
	return int16(bandfactor >> 16)
}

// esbrCalcBands is ixheaacd_calc_bands.
func esbrCalcBands(diff []int, start, stop, numBands int) {
	bandfactor := int32(esbrCalcFreqRatio(start, stop, numBands))
	previous := int32(stop)
	exact := shl32Sat(int32(int16(stop)), 24)
	for i := numBands - 1; i >= 0; i-- {
		exact = int32(int16(exact>>16)) * bandfactor
		temp := add32Sat(exact, 0x00400000)
		exact = exact << 1
		current := int32(int16(temp >> 23))
		diff[i] = int(previous - current)
		previous = current
	}
}

func esbrCalcStartBand(fsMapped, startFreq, upsamp int) int {
	var k0min int
	f := float32(fsMapped)
	if upsamp == 4 {
		switch {
		case fsMapped < 32000:
			k0min = int(float64(float32(3000*2*32)/f) + 0.5)
		case fsMapped < 64000:
			k0min = int(float64(float32(4000*2*32)/f) + 0.5)
		default:
			k0min = int(float64(float32(5000*2*32)/f) + 0.5)
		}
	} else {
		switch {
		case fsMapped < 32000:
			k0min = int(float64(float32(3000*2*64)/f) + 0.5)
		case fsMapped < 64000:
			k0min = int(float64(float32(4000*2*64)/f) + 0.5)
		default:
			k0min = int(float64(float32(5000*2*64)/f) + 0.5)
		}
	}
	var off []int
	switch fsMapped {
	case 16000:
		off = []int{-8, -7, -6, -5, -4, -3, -2, -1, 0, 1, 2, 3, 4, 5, 6, 7}
	case 22050:
		off = []int{-5, -4, -3, -2, -1, 0, 1, 2, 3, 4, 5, 6, 7, 9, 11, 13}
	case 24000:
		off = []int{-5, -3, -2, -1, 0, 1, 2, 3, 4, 5, 6, 7, 9, 11, 13, 16}
	case 32000:
		off = []int{-6, -4, -2, -1, 0, 1, 2, 3, 4, 5, 6, 7, 9, 11, 13, 16}
	case 40000:
		off = []int{-1, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 11, 13, 15, 17, 19}
	case 44100, 48000, 64000:
		off = []int{-4, -2, -1, 0, 1, 2, 3, 4, 5, 6, 7, 9, 11, 13, 16, 20}
	case 88200, 96000:
		off = []int{-2, -1, 0, 1, 2, 3, 4, 5, 6, 7, 9, 11, 13, 16, 20, 24}
	default:
		off = []int{0, 1, 2, 3, 4, 5, 6, 7, 9, 11, 13, 16, 20, 24, 28, 33}
	}
	return k0min + off[startFreq]
}

func esbrCalcStopBand(fs, stopFreq, upsamp int) int {
	var k1min int
	if upsamp == 4 {
		fs /= 2
		f := float32(fs)
		switch {
		case fs < 32000:
			k1min = int(float64(float32(6000*2*32)/f) + 0.5)
		case fs < 64000:
			k1min = int(float64(float32(8000*2*32)/f) + 0.5)
		default:
			k1min = int(float64(float32(10000*2*32)/f) + 0.5)
		}
	} else {
		f := float32(fs)
		switch {
		case fs < 32000:
			k1min = int(float64(float32(6000*2*64)/f) + 0.5)
		case fs < 64000:
			k1min = int(float64(float32(8000*2*64)/f) + 0.5)
		default:
			k1min = int(float64(float32(10000*2*64)/f) + 0.5)
		}
	}
	var stop [14]int
	for i := 0; i <= 13; i++ {
		stop[i] = int(int16(int(float64(k1min)*math.Pow(64.0/float64(k1min), float64(i)/13.0) + 0.5)))
	}
	var diff [13]int
	for i := 0; i <= 12; i++ {
		diff[i] = stop[i+1] - stop[i]
	}
	sort.Ints(diff[:])
	r := k1min
	for i := 0; i < stopFreq; i++ {
		r += diff[i]
	}
	return r
}

func esbrCalcK0K2(samp, startFreq, stopFreq, upsamp int) (k0, k2 int, err error) {
	fs := samp
	if upsamp == 4 {
		fs /= 2
	}
	var mapped int
	switch {
	case fs >= 0 && fs < 18783:
		mapped = 16000
	case fs >= 18783 && fs < 23004:
		mapped = 22050
	case fs >= 23004 && fs < 27713:
		mapped = 24000
	case fs >= 27713 && fs < 35777:
		mapped = 32000
	case fs >= 35777 && fs < 42000:
		mapped = 40000
	case fs >= 42000 && fs < 46009:
		mapped = 44100
	case fs >= 46009 && fs < 55426:
		mapped = 48000
	case fs >= 55426 && fs < 75132:
		mapped = 64000
	case fs >= 75132 && fs < 92017:
		mapped = 88200
	case fs >= 92017:
		mapped = 96000
	default:
		return 0, 0, fmt.Errorf("media: usac sbr bad rate")
	}
	k0 = esbrCalcStartBand(mapped, startFreq, upsamp)
	switch {
	case stopFreq < 14:
		k2 = esbrCalcStopBand(samp, stopFreq, upsamp)
	case stopFreq == 14:
		k2 = 2 * k0
	default:
		k2 = 3 * k0
	}
	if k2 > 64 {
		k2 = 64
	}
	return k0, k2, nil
}

// esbrMasterTable is ixheaacd_calc_master_frq_bnd_tbl.
func (h *esbrHeader) masterTable() error {
	fs := h.outFs
	k0, k2, err := esbrCalcK0K2(fs, h.startFreq, h.stopFreq, h.upsamp)
	if err != nil {
		return err
	}
	if k2 > 64 {
		k2 = 64
	}
	bad := fmt.Errorf("media: usac sbr bad band configuration fs=%d k0=%d k2=%d start=%d stop=%d fscale=%d up=%d", fs, k0, k2, h.startFreq, h.stopFreq, h.freqScale, h.upsamp)
	if h.upsamp == 4 {
		if k2-k0 > esbrMaxFreqCoeffs || k2 <= k0 {
			return bad
		}
	} else {
		if k2-k0 > esbrMaxFreqCoeffsSBR || k2 <= k0 {
			return bad
		}
		if fs == 44100 && k2-k0 > esbrMaxFreq44100 {
			return bad
		}
		if fs >= 48000 && k2-k0 > esbrMaxFreq48000 {
			return bad
		}
	}
	var numMF int
	if h.freqScale == 0 {
		var dk, numBands int
		if h.alterScale == 0 {
			dk = 1
			numBands = k2 - k0
			numBands -= numBands & 1
		} else {
			dk = 2
			numBands = (k2 - k0 + 2) >> 2
			numBands <<= 1
		}
		if numBands < 1 {
			return bad
		}
		k2Achieved := k0 + (numBands << uint(dk-1))
		k2Diff := k2 - k2Achieved
		vec := make([]int, numBands)
		for k := range vec {
			vec[k] = dk
		}
		incr, k := 0, 0
		if k2Diff < 0 {
			incr = 1
			k = 0
		}
		if k2Diff > 0 {
			incr = -1
			k = numBands - 1
		}
		for k2Diff != 0 {
			vec[k] -= incr
			k += incr
			k2Diff += incr
		}
		h.fMaster[0] = k0
		for k := 1; k <= numBands; k++ {
			h.fMaster[k] = h.fMaster[k-1] + vec[k-1]
		}
		numMF = numBands
	} else {
		var bands int
		switch h.freqScale {
		case 1:
			bands = 12
		case 2:
			bands = 10
		default:
			bands = 8
		}
		if h.upsamp == 4 && k0 < bands {
			bands = k0 - (k0 & 1)
		}
		if 10000*k2 > 22449*k0 {
			k1 := k0 << 1
			numBands0 := bands
			numBands1 := int(esbrLogDual[k2]) - int(esbrLogDual[k1])
			numBands1 = bands * numBands1
			if h.alterScale != 0 {
				numBands1 = int((int64(numBands1) * 0x6276) >> 15)
			}
			numBands1 += 0x1000
			numBands1 >>= 13
			numBands1 <<= 1
			if numBands0 < 1 || numBands1 < 1 {
				return bad
			}
			vecDk0 := make([]int, numBands0)
			vecDk1 := make([]int, numBands1)
			esbrCalcBands(vecDk0, k0, k1, numBands0)
			sort.Ints(vecDk0)
			h.fMaster[0] = k0
			for k := 1; k <= numBands0; k++ {
				h.fMaster[k] = h.fMaster[k-1] + vecDk0[k-1]
			}
			esbrCalcBands(vecDk1, k1, k2, numBands1)
			sort.Ints(vecDk1)
			if vecDk1[0] < vecDk0[numBands0-1] {
				change := vecDk0[numBands0-1] - vecDk1[0]
				temp := (vecDk1[numBands1-1] - vecDk1[0]) >> 1
				if change > temp {
					change = temp
				}
				vecDk1[0] += change
				vecDk1[numBands1-1] -= change
				sort.Ints(vecDk1)
			}
			h.fMaster[numBands0] = k1
			for k := 1; k <= numBands1; k++ {
				h.fMaster[numBands0+k] = h.fMaster[numBands0+k-1] + vecDk1[k-1]
			}
			numMF = numBands0 + numBands1
		} else {
			k1 := k2
			numBands0 := int(esbrLogDual[k1]) - int(esbrLogDual[k0])
			numBands0 = bands * numBands0
			numBands0 += 0x1000
			numBands0 >>= 13
			numBands0 <<= 1
			if numBands0 < 1 {
				return bad
			}
			vecDk0 := make([]int, numBands0)
			esbrCalcBands(vecDk0, k0, k1, numBands0)
			sort.Ints(vecDk0)
			if vecDk0[0] == 0 {
				return bad
			}
			h.fMaster[0] = k0
			for k := 1; k <= numBands0; k++ {
				h.fMaster[k] = h.fMaster[k-1] + vecDk0[k-1]
			}
			numMF = numBands0
		}
	}
	if numMF < 1 {
		return bad
	}
	h.numMF = numMF
	if h.upsamp == 4 {
		for k := 1; k < numMF; k++ {
			if !(h.fMaster[k]-h.fMaster[k-1] <= k0-2) {
				return bad
			}
		}
	}
	return nil
}

// bandTables is ixheaacd_calc_frq_bnd_tbls.
func (h *esbrHeader) bandTables() error {
	if err := h.masterTable(); err != nil {
		return err
	}
	if h.xoverBand > h.numMF {
		return fmt.Errorf("media: usac sbr bad crossover band")
	}
	// derive the high and low resolution tables
	master := h.fMaster[h.xoverBand:]
	numHF := h.numMF - h.xoverBand
	hi, lo := 0, 0
	k := 0
	h.fLow[lo], h.fHigh[hi] = master[0], master[0]
	lo++
	hi++
	mp := 1
	k++
	if numHF&1 != 0 {
		h.fLow[lo], h.fHigh[hi] = master[mp], master[mp]
		hi++
		lo++
		mp++
		k++
	}
	for ; k <= numHF; k++ {
		h.fHigh[hi] = master[mp]
		hi++
		mp++
		k++
		h.fLow[lo], h.fHigh[hi] = master[mp], master[mp]
		hi++
		mp++
		lo++
	}
	numLF := (numHF + 1) >> 1
	h.nSf[0], h.nSf[1] = numLF, numHF
	if numLF <= 0 || numLF > esbrMaxFreqCoeffs>>1 {
		return fmt.Errorf("media: usac sbr bad band count")
	}
	lsb := h.fLow[0]
	usb := h.fLow[numLF]
	h.subStart = lsb
	h.status = true
	limit := 32
	if h.usf4 {
		limit = 16
	}
	if lsb > limit || lsb >= usb {
		return fmt.Errorf("media: usac sbr bad low band")
	}
	// noise bands
	k2 := h.fHigh[numHF]
	kx := h.fHigh[0]
	var temp int
	if h.noiseBands == 0 {
		temp = 1
	} else {
		temp = int(esbrLogDual[k2]) - int(esbrLogDual[kx])
		temp *= h.noiseBands
		temp += 0x800
		temp >>= 12
		if temp == 0 {
			temp = 1
		}
	}
	if temp > esbrMaxNoiseCoeffs {
		return fmt.Errorf("media: usac sbr bad noise bands")
	}
	h.nNf = temp
	num, den := numLF, h.nNf
	h.noiseTbl[0] = h.fLow[0]
	ik := 0
	for kk := 1; kk <= h.nNf; kk++ {
		ik += num / den
		h.noiseTbl[kk] = h.fLow[ik]
		num = numLF - ik
		den--
	}
	h.subStart = lsb
	h.subEnd = usb
	return nil
}
