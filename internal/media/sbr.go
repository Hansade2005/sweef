package media

import (
	"errors"
	"math"
	"sync"
)

// Spectral band replication (ISO/IEC 14496-3 clause 4.6.18 and 4.6.19):
// bitstream parsing, frequency band tables, HF generation, envelope
// adjustment and the QMF banks (sbr_qmf.go). The structure follows the
// reference decoder: all frame level state lives in sbrElem (one per SCE or
// CPE) and sbrChan (one per channel).

var errSBR = errors.New("media: bad sbr data")

const (
	sbrFixFix = iota
	sbrFixVar
	sbrVarFix
	sbrVarVar
)

const sbrEnvOff = 2 // ENVELOPE_ADJUSTMENT_OFFSET (tHFAdj)

// ---- huffman -------------------------------------------------------------

type sbrVLC struct{ node [][2]int32 }

func buildSBRVLC(lens []uint8, codes []uint32) *sbrVLC {
	t := &sbrVLC{node: make([][2]int32, 1, 2*len(lens))}
	for sym, l := range lens {
		cur := 0
		for b := int(l) - 1; b >= 0; b-- {
			bit := (codes[sym] >> uint(b)) & 1
			if b == 0 {
				t.node[cur][bit] = -int32(sym) - 1
				break
			}
			nx := t.node[cur][bit]
			if nx <= 0 {
				t.node = append(t.node, [2]int32{})
				nx = int32(len(t.node) - 1)
				t.node[cur][bit] = nx
			}
			cur = int(nx)
		}
	}
	return t
}

func (r *aacBits) vlc(t *sbrVLC) int {
	cur := 0
	for i := 0; i < 24; i++ {
		nx := t.node[cur][r.bits(1)]
		if r.err || nx == 0 {
			r.err = true
			return 0
		}
		if nx < 0 {
			return int(-nx - 1)
		}
		cur = int(nx)
	}
	r.err = true
	return 0
}

var (
	sbrHuffOnce sync.Once
	sbrVLCs     [10]*sbrVLC
	psVLCs      [10]*sbrVLC
)

func sbrInitHuff() {
	sbrHuffOnce.Do(func() {
		for i := range sbrVLCs {
			sbrVLCs[i] = buildSBRVLC(sbrHuffLen[i], sbrHuffCode[i])
			psVLCs[i] = buildSBRVLC(psHuffLen[i], psHuffCode[i])
		}
	})
}

const (
	hTEnv15 = iota
	hFEnv15
	hTEnvBal15
	hFEnvBal15
	hTEnv30
	hFEnv30
	hTEnvBal30
	hFEnvBal30
	hTNoise30
	hTNoiseBal30
)

// ---- state ---------------------------------------------------------------

type sbrSpec struct{ startFreq, stopFreq, xoverBand, freqScale, alterScale, noiseBands int }

// sbrChan is the per channel SBR state.
type sbrChan struct {
	frameClass      int
	addHarmonicFlag bool
	numEnv          int
	freqRes         [8]int
	numNoise        int
	dfEnv           [8]bool
	dfNoise         [3]bool
	invfMode        [2][5]int
	addHarmonic     [48]bool
	ampRes          int
	tEnv            [8]int
	tEnvNumEnvOld   int
	tQ              [3]int
	envFacsQ        [8][48]int
	envFacs         [8][48]float64
	noiseFacsQ      [3][5]int
	noiseFacs       [3][5]float64
	bwArray         [5]float64
	sIndexMapped    [8][48]int
	eA              [2]int
	gTemp, qTemp    [48 + 8][48]float64
	fIndexNoise     int
	fIndexSine      int

	ana         sbrAnalysis
	syn         sbrSynthesis
	wCur, wPrev [32][32][2]float64
	yCur, yPrev [38][64][2]float64
	xOut        [38][64][2]float64
}

// sbrElem is the SBR decoder of one SCE or CPE element.
type sbrElem struct {
	cpe        bool
	sampleRate int // 2 * core rate
	down       bool
	start      bool
	ready      bool
	reset      bool
	kxPushed   bool
	coupling   bool

	ampResHeader                                    int
	spec, prevSpec                                  sbrSpec
	limiterBands, limiterGains, interpol, smoothing int
	haveHeader                                      bool

	k           [5]int
	kx, m       [2]int
	nMaster     int
	n           [2]int
	nQ, nLim    int
	fMaster     [64]int
	fLow, fHigh [32]int
	fNoise      [8]int
	fLim        [40]int
	numPatches  int
	patchNum    [8]int
	patchStart  [8]int

	data [2]sbrChan

	xLow           [32][40][2]float64
	xHigh          [64][40][2]float64
	alpha0, alpha1 [64][2]float64

	eCurr, eOrigMapped, qMapped, sMapped [8][64]float64
	gain, qM, sM                         [8][64]float64

	ps       *psState
	psOK     bool // PS permitted (explicit signalling or detected)
	psSeen   bool
	psActive bool
}

func newSBRElem(cpe bool, coreRate int, down bool) *sbrElem {
	s := &sbrElem{cpe: cpe, sampleRate: 2 * coreRate, down: down}
	s.turnoff()
	return s
}

func (s *sbrElem) turnoff() {
	s.start = false
	s.ready = false
	s.kx[1] = 32
	s.m[1] = 0
	s.data[0].eA[1] = -1
	s.data[1].eA[1] = -1
	s.spec = sbrSpec{-1, -1, -1, -1, -1, -1}
	s.haveHeader = false
	if s.ps != nil {
		s.ps.start = false
	}
}

// ---- parsing -------------------------------------------------------------

func (s *sbrElem) readHeader(r *aacBits) {
	s.start = true
	oldLim := s.limiterBands
	old := s.spec
	s.ampResHeader = r.bits(1)
	s.spec.startFreq = r.bits(4)
	s.spec.stopFreq = r.bits(4)
	s.spec.xoverBand = r.bits(3)
	r.bits(2)
	extra1 := r.bit()
	extra2 := r.bit()
	if extra1 {
		s.spec.freqScale = r.bits(2)
		s.spec.alterScale = r.bits(1)
		s.spec.noiseBands = r.bits(2)
	} else {
		s.spec.freqScale, s.spec.alterScale, s.spec.noiseBands = 2, 1, 2
	}
	if s.spec != old {
		s.reset = true
	}
	if extra2 {
		s.limiterBands = r.bits(2)
		s.limiterGains = r.bits(2)
		s.interpol = r.bits(1)
		s.smoothing = r.bits(1)
	} else {
		s.limiterBands, s.limiterGains, s.interpol, s.smoothing = 2, 2, 1, 1
	}
	if s.limiterBands != oldLim && !s.reset {
		s.makeLimTable()
	}
	s.haveHeader = true
}

// sbrPtrBits is ceil(log2(numEnv+1)), the width of bs_pointer.
func sbrPtrBits(numEnv int) int {
	return [...]int{0, 1, 2, 2, 3, 3, 3, 3}[numEnv]
}

func (s *sbrElem) readGrid(r *aacBits, c *sbrChan) error {
	boundary := 16
	oldNumEnv := c.numEnv
	c.freqRes[0] = c.freqRes[c.numEnv]
	c.ampRes = s.ampResHeader
	c.tEnvNumEnvOld = c.tEnv[oldNumEnv]
	bsPointer := 0
	class := r.bits(2)
	var numRelLead, numRelTrail int
	switch class {
	case sbrFixFix:
		numEnv := 1 << uint(r.bits(2))
		if numEnv > 4 {
			return errSBR
		}
		c.numEnv = numEnv
		numRelLead = numEnv - 1
		if numEnv == 1 {
			c.ampRes = 0
		}
		c.tEnv[0] = 0
		c.tEnv[numEnv] = boundary
		step := (boundary + (numEnv >> 1)) / numEnv
		for i := 0; i < numRelLead; i++ {
			c.tEnv[i+1] = c.tEnv[i] + step
		}
		c.freqRes[1] = r.bits(1)
		for i := 1; i < numEnv; i++ {
			c.freqRes[i+1] = c.freqRes[1]
		}
	case sbrFixVar:
		boundary += r.bits(2)
		numRelTrail = r.bits(2)
		c.numEnv = numRelTrail + 1
		c.tEnv[0] = 0
		c.tEnv[c.numEnv] = boundary
		for i := 0; i < numRelTrail; i++ {
			c.tEnv[c.numEnv-1-i] = c.tEnv[c.numEnv-i] - 2*r.bits(2) - 2
		}
		bsPointer = r.bits(sbrPtrBits(c.numEnv))
		for i := 0; i < c.numEnv; i++ {
			c.freqRes[c.numEnv-i] = r.bits(1)
		}
	case sbrVarFix:
		c.tEnv[0] = r.bits(2)
		numRelLead = r.bits(2)
		c.numEnv = numRelLead + 1
		c.tEnv[c.numEnv] = boundary
		for i := 0; i < numRelLead; i++ {
			c.tEnv[i+1] = c.tEnv[i] + 2*r.bits(2) + 2
		}
		bsPointer = r.bits(sbrPtrBits(c.numEnv))
		for i := 0; i < c.numEnv; i++ {
			c.freqRes[i+1] = r.bits(1)
		}
	case sbrVarVar:
		c.tEnv[0] = r.bits(2)
		boundary += r.bits(2)
		numRelLead = r.bits(2)
		numRelTrail = r.bits(2)
		c.numEnv = numRelLead + numRelTrail + 1
		if c.numEnv > 5 {
			return errSBR
		}
		c.tEnv[c.numEnv] = boundary
		for i := 0; i < numRelLead; i++ {
			c.tEnv[i+1] = c.tEnv[i] + 2*r.bits(2) + 2
		}
		for i := 0; i < numRelTrail; i++ {
			c.tEnv[c.numEnv-1-i] = c.tEnv[c.numEnv-i] - 2*r.bits(2) - 2
		}
		bsPointer = r.bits(sbrPtrBits(c.numEnv))
		for i := 0; i < c.numEnv; i++ {
			c.freqRes[i+1] = r.bits(1)
		}
	}
	c.frameClass = class
	if r.err || bsPointer > c.numEnv+1 {
		return errSBR
	}
	for i := 1; i <= c.numEnv; i++ {
		if c.tEnv[i-1] >= c.tEnv[i] {
			return errSBR
		}
	}
	c.numNoise = 1
	if c.numEnv > 1 {
		c.numNoise = 2
	}
	c.tQ[0] = c.tEnv[0]
	c.tQ[c.numNoise] = c.tEnv[c.numEnv]
	if c.numNoise > 1 {
		var idx int
		switch {
		case class == sbrFixFix:
			idx = c.numEnv >> 1
		case class&1 != 0:
			p := bsPointer - 1
			if p < 1 {
				p = 1
			}
			idx = c.numEnv - p
		default:
			switch {
			case bsPointer == 0:
				idx = 1
			case bsPointer == 1:
				idx = c.numEnv - 1
			default:
				idx = bsPointer - 1
			}
		}
		c.tQ[1] = c.tEnv[idx]
	}
	if c.eA[1] != oldNumEnv {
		c.eA[0] = -1
	} else {
		c.eA[0] = 0
	}
	c.eA[1] = -1
	if class&1 != 0 && bsPointer != 0 {
		c.eA[1] = c.numEnv + 1 - bsPointer
	} else if class == sbrVarFix && bsPointer > 1 {
		c.eA[1] = bsPointer - 1
	}
	return nil
}

func (s *sbrElem) copyGrid(dst, src *sbrChan) {
	dst.freqRes[0] = dst.freqRes[dst.numEnv]
	dst.tEnvNumEnvOld = dst.tEnv[dst.numEnv]
	if dst.eA[1] != dst.numEnv {
		dst.eA[0] = -1
	} else {
		dst.eA[0] = 0
	}
	copy(dst.freqRes[1:], src.freqRes[1:])
	dst.tEnv = src.tEnv
	dst.tQ = src.tQ
	dst.numEnv = src.numEnv
	dst.ampRes = src.ampRes
	dst.numNoise = src.numNoise
	dst.frameClass = src.frameClass
	dst.eA[1] = src.eA[1]
}

func (s *sbrElem) readDtdf(r *aacBits, c *sbrChan) {
	for i := 0; i < c.numEnv; i++ {
		c.dfEnv[i] = r.bit()
	}
	for i := 0; i < c.numNoise; i++ {
		c.dfNoise[i] = r.bit()
	}
}

func (s *sbrElem) readInvf(r *aacBits, c *sbrChan) {
	c.invfMode[1] = c.invfMode[0]
	for i := 0; i < s.nQ; i++ {
		c.invfMode[0][i] = r.bits(2)
	}
}

func (s *sbrElem) readEnvelope(r *aacBits, c *sbrChan, ch int) error {
	sbrInitHuff()
	delta := 1
	if ch == 1 && s.coupling {
		delta = 2
	}
	odd := s.n[1] & 1
	var bits, tb, fb int
	if s.coupling && ch == 1 {
		if c.ampRes != 0 {
			bits, tb, fb = 5, hTEnvBal30, hFEnvBal30
		} else {
			bits, tb, fb = 6, hTEnvBal15, hFEnvBal15
		}
	} else {
		if c.ampRes != 0 {
			bits, tb, fb = 6, hTEnv30, hFEnv30
		} else {
			bits, tb, fb = 7, hTEnv15, hFEnv15
		}
	}
	tLav, fLav := sbrHuffLav[tb], sbrHuffLav[fb]
	tv, fv := sbrVLCs[tb], sbrVLCs[fb]
	for i := 0; i < c.numEnv; i++ {
		cur := c.freqRes[i+1]
		if c.dfEnv[i] {
			if cur == c.freqRes[i] {
				for j := 0; j < s.n[cur]; j++ {
					c.envFacsQ[i+1][j] = c.envFacsQ[i][j] + delta*(r.vlc(tv)-tLav)
					if c.envFacsQ[i+1][j] > 127 || c.envFacsQ[i+1][j] < -128 {
						return errSBR
					}
				}
			} else if cur != 0 {
				for j := 0; j < s.n[cur]; j++ {
					k := (j + odd) >> 1
					c.envFacsQ[i+1][j] = c.envFacsQ[i][k] + delta*(r.vlc(tv)-tLav)
				}
			} else {
				for j := 0; j < s.n[cur]; j++ {
					k := 0
					if j != 0 {
						k = 2*j - odd
					}
					c.envFacsQ[i+1][j] = c.envFacsQ[i][k] + delta*(r.vlc(tv)-tLav)
				}
			}
		} else {
			c.envFacsQ[i+1][0] = delta * r.bits(bits)
			for j := 1; j < s.n[cur]; j++ {
				c.envFacsQ[i+1][j] = c.envFacsQ[i+1][j-1] + delta*(r.vlc(fv)-fLav)
			}
		}
		if r.err {
			return errSBR
		}
	}
	c.envFacsQ[0] = c.envFacsQ[c.numEnv]
	return nil
}

func (s *sbrElem) readNoise(r *aacBits, c *sbrChan, ch int) error {
	sbrInitHuff()
	delta := 1
	var tb, fb int
	if s.coupling && ch == 1 {
		delta = 2
		tb, fb = hTNoiseBal30, hFEnvBal30
	} else {
		tb, fb = hTNoise30, hFEnv30
	}
	tLav, fLav := sbrHuffLav[tb], sbrHuffLav[fb]
	for i := 0; i < c.numNoise; i++ {
		if c.dfNoise[i] {
			for j := 0; j < s.nQ; j++ {
				c.noiseFacsQ[i+1][j] = c.noiseFacsQ[i][j] + delta*(r.vlc(sbrVLCs[tb])-tLav)
			}
		} else {
			c.noiseFacsQ[i+1][0] = delta * r.bits(5)
			for j := 1; j < s.nQ; j++ {
				c.noiseFacsQ[i+1][j] = c.noiseFacsQ[i+1][j-1] + delta*(r.vlc(sbrVLCs[fb])-fLav)
			}
		}
		if r.err {
			return errSBR
		}
	}
	c.noiseFacsQ[0] = c.noiseFacsQ[c.numNoise]
	return nil
}

func (s *sbrElem) readHarmonic(r *aacBits, c *sbrChan) {
	c.addHarmonicFlag = r.bit()
	if c.addHarmonicFlag {
		for i := 0; i < s.n[1]; i++ {
			c.addHarmonic[i] = r.bit()
		}
	}
}

// parse reads one sbr_extension_data payload of n bits (the bits after the
// extension_type nibble) from r. It returns an error when the data is bad, in
// which case SBR is switched off until the next header.
func (s *sbrElem) parse(r *aacBits, bitsAvail int, crc bool) (err error) {
	startPos := r.pos
	s.reset = false
	s.kx[0] = s.kx[1]
	s.m[0] = s.m[1]
	s.kxPushed = true
	if crc {
		r.bits(10)
	}
	if r.bit() { // bs_header_flag
		s.readHeader(r)
	}
	if s.reset {
		if e := s.resetTables(); e != nil {
			s.turnoff()
			return e
		}
	}
	if s.start {
		if e := s.readData(r, startPos+bitsAvail); e != nil {
			s.turnoff()
			return e
		}
	}
	if r.err {
		s.turnoff()
		return errSBR
	}
	return nil
}

func (s *sbrElem) readData(r *aacBits, endPos int) error {
	c0 := &s.data[0]
	if !s.cpe {
		if r.bit() {
			r.bits(4)
		}
		if err := s.readGrid(r, c0); err != nil {
			return err
		}
		s.readDtdf(r, c0)
		s.readInvf(r, c0)
		if err := s.readEnvelope(r, c0, 0); err != nil {
			return err
		}
		if err := s.readNoise(r, c0, 0); err != nil {
			return err
		}
		s.readHarmonic(r, c0)
	} else {
		c1 := &s.data[1]
		if r.bit() {
			r.bits(8)
		}
		s.coupling = r.bit()
		if s.coupling {
			if err := s.readGrid(r, c0); err != nil {
				return err
			}
			s.copyGrid(c1, c0)
			s.readDtdf(r, c0)
			s.readDtdf(r, c1)
			s.readInvf(r, c0)
			c1.invfMode[1] = c1.invfMode[0]
			c1.invfMode[0] = c0.invfMode[0]
			if err := s.readEnvelope(r, c0, 0); err != nil {
				return err
			}
			if err := s.readNoise(r, c0, 0); err != nil {
				return err
			}
			if err := s.readEnvelope(r, c1, 1); err != nil {
				return err
			}
			if err := s.readNoise(r, c1, 1); err != nil {
				return err
			}
		} else {
			if err := s.readGrid(r, c0); err != nil {
				return err
			}
			if err := s.readGrid(r, c1); err != nil {
				return err
			}
			s.readDtdf(r, c0)
			s.readDtdf(r, c1)
			s.readInvf(r, c0)
			s.readInvf(r, c1)
			if err := s.readEnvelope(r, c0, 0); err != nil {
				return err
			}
			if err := s.readEnvelope(r, c1, 1); err != nil {
				return err
			}
			if err := s.readNoise(r, c0, 0); err != nil {
				return err
			}
			if err := s.readNoise(r, c1, 1); err != nil {
				return err
			}
		}
		s.readHarmonic(r, c0)
		s.readHarmonic(r, c1)
	}
	if r.bit() { // bs_extended_data
		left := r.bits(4)
		if left == 15 {
			left += r.bits(8)
		}
		left <<= 3
		for left > 7 {
			left -= 2
			id := r.bits(2)
			used, err := s.readExtension(r, id, left)
			if err != nil {
				return err
			}
			left -= used
		}
		if left < 0 {
			return errSBR
		}
		if left > 0 {
			r.skip(left)
		}
	}
	s.ready = !r.err
	return nil
}

var errSBRPS = errors.New("media: bad ps data")

// readExtension handles bs_extension_id; id 2 carries PS data. It returns the
// number of bits consumed.
func (s *sbrElem) readExtension(r *aacBits, id, left int) (int, error) {
	if id != 2 || !s.psOK {
		r.skip(left)
		return left, nil
	}
	if s.ps == nil {
		s.ps = newPS()
	}
	s.psSeen = true
	start := r.pos
	if err := s.ps.parse(r, left); err != nil {
		// PS data errors disable PS processing but keep SBR
		s.ps.start = false
		r.pos = start + left
		return left, nil
	}
	used := r.pos - start
	if used > left {
		return 0, errSBRPS
	}
	return used, nil
}

// ---- frequency tables -----------------------------------------------------

func sbrMakeBands(bands []int, start, stop, num int) {
	base := float32(math.Pow(float64(float32(stop)/float32(start)), float64(1.0/float32(num))))
	prod := float32(start)
	prev := start
	for k := 0; k < num-1; k++ {
		prod *= base
		present := int(math.Floor(float64(prod) + 0.5))
		bands[k] = present - prev
		prev = present
	}
	bands[num-1] = stop - prev
}

func sortInts(a []int) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j-1] > a[j]; j-- {
			a[j-1], a[j] = a[j], a[j-1]
		}
	}
}

func (s *sbrElem) checkNMaster() error {
	if s.nMaster <= 0 || s.spec.xoverBand >= s.nMaster {
		return errSBR
	}
	return nil
}

func (s *sbrElem) makeFMaster() error {
	sp := s.spec
	var temp int
	switch {
	case s.sampleRate < 32000:
		temp = 3000
	case s.sampleRate < 64000:
		temp = 4000
	default:
		temp = 5000
	}
	startMin := ((temp << 7) + (s.sampleRate >> 1)) / s.sampleRate
	stopMin := ((temp << 8) + (s.sampleRate >> 1)) / s.sampleRate
	var row int
	switch s.sampleRate {
	case 16000:
		row = 0
	case 22050:
		row = 1
	case 24000:
		row = 2
	case 32000:
		row = 3
	case 44100, 48000, 64000:
		row = 4
	case 88200, 96000, 128000, 176400, 192000:
		row = 5
	default:
		return errSBR
	}
	s.k[0] = startMin + int(sbrOffset[row*16+sp.startFreq])
	switch {
	case sp.stopFreq < 14:
		s.k[2] = stopMin
		var stopDk [13]int
		sbrMakeBands(stopDk[:], stopMin, 64, 13)
		sortInts(stopDk[:])
		for k := 0; k < sp.stopFreq; k++ {
			s.k[2] += stopDk[k]
		}
	case sp.stopFreq == 14:
		s.k[2] = 2 * s.k[0]
	default:
		s.k[2] = 3 * s.k[0]
	}
	if s.k[2] > 64 {
		s.k[2] = 64
	}
	var maxSub int
	switch {
	case s.sampleRate <= 32000:
		maxSub = 48
	case s.sampleRate == 44100:
		maxSub = 35
	default:
		maxSub = 32
	}
	if s.k[2]-s.k[0] > maxSub || s.k[0] < 0 || s.k[0] >= 64 {
		return errSBR
	}
	if sp.freqScale == 0 {
		dk := sp.alterScale + 1
		s.nMaster = ((s.k[2] - s.k[0] + (dk & 2)) >> uint(dk)) << 1
		if err := s.checkNMaster(); err != nil {
			return err
		}
		for k := 1; k <= s.nMaster; k++ {
			s.fMaster[k] = dk
		}
		k2diff := s.k[2] - s.k[0] - s.nMaster*dk
		if k2diff < 0 {
			s.fMaster[1]--
			if k2diff < -1 {
				s.fMaster[2]--
			}
		} else if k2diff != 0 {
			s.fMaster[s.nMaster]++
		}
		s.fMaster[0] = s.k[0]
		for k := 1; k <= s.nMaster; k++ {
			s.fMaster[k] += s.fMaster[k-1]
		}
		return nil
	}
	halfBands := 7 - sp.freqScale
	var twoRegions bool
	if 49*s.k[2] > 110*s.k[0] {
		twoRegions = true
		s.k[1] = 2 * s.k[0]
	} else {
		s.k[1] = s.k[2]
	}
	numBands0 := int(math.RoundToEven(float64(float32(halfBands)*float32(math.Log2(float64(float32(s.k[1])/float32(s.k[0]))))))) * 2
	if numBands0 <= 0 {
		return errSBR
	}
	var vk0 [64]int
	sbrMakeBands(vk0[1:], s.k[0], s.k[1], numBands0)
	sortInts(vk0[1 : numBands0+1])
	vdk0Max := vk0[numBands0]
	vk0[0] = s.k[0]
	for k := 1; k <= numBands0; k++ {
		if vk0[k] <= 0 {
			return errSBR
		}
		vk0[k] += vk0[k-1]
	}
	if twoRegions {
		var vk1 [64]int
		invwarp := float32(1.0)
		if sp.alterScale != 0 {
			invwarp = 0.76923076923076923077
		}
		numBands1 := int(math.RoundToEven(float64(float32(halfBands)*invwarp*float32(math.Log2(float64(float32(s.k[2])/float32(s.k[1]))))))) * 2
		if numBands1 <= 0 || numBands0+numBands1 > 48 {
			return errSBR
		}
		sbrMakeBands(vk1[1:], s.k[1], s.k[2], numBands1)
		vdk1Min := vk1[1]
		for _, v := range vk1[1 : numBands1+1] {
			if v < vdk1Min {
				vdk1Min = v
			}
		}
		if vdk1Min < vdk0Max {
			sortInts(vk1[1 : numBands1+1])
			change := vdk0Max - vk1[1]
			if h := (vk1[numBands1] - vk1[1]) >> 1; h < change {
				change = h
			}
			vk1[1] += change
			vk1[numBands1] -= change
		}
		sortInts(vk1[1 : numBands1+1])
		vk1[0] = s.k[1]
		for k := 1; k <= numBands1; k++ {
			if vk1[k] <= 0 {
				return errSBR
			}
			vk1[k] += vk1[k-1]
		}
		s.nMaster = numBands0 + numBands1
		if err := s.checkNMaster(); err != nil {
			return err
		}
		copy(s.fMaster[:numBands0+1], vk0[:numBands0+1])
		copy(s.fMaster[numBands0+1:], vk1[1:numBands1+1])
	} else {
		s.nMaster = numBands0
		if err := s.checkNMaster(); err != nil {
			return err
		}
		copy(s.fMaster[:numBands0+1], vk0[:numBands0+1])
	}
	return nil
}

func (s *sbrElem) calcPatches() error {
	lastK, lastMsb := -1, -1
	sb := 0
	msb := s.k[0]
	usb := s.kx[1]
	goalSb := ((16000 << 7) + (s.sampleRate >> 1)) / s.sampleRate
	s.numPatches = 0
	var k int
	if goalSb < s.kx[1]+s.m[1] {
		for k = 0; s.fMaster[k] < goalSb; k++ {
		}
	} else {
		k = s.nMaster
	}
	for {
		odd := 0
		if k == lastK && msb == lastMsb {
			return errSBR
		}
		lastK, lastMsb = k, msb
		for i := k; i == k || sb > s.k[0]-1+msb-odd; i-- {
			if i < 0 {
				return errSBR
			}
			sb = s.fMaster[i]
			odd = (sb + s.k[0]) & 1
		}
		if s.numPatches > 5 {
			return errSBR
		}
		pn := sb - usb
		if pn < 0 {
			pn = 0
		}
		s.patchNum[s.numPatches] = pn
		s.patchStart[s.numPatches] = s.k[0] - odd - pn
		if pn > 0 {
			usb = sb
			msb = sb
			s.numPatches++
		} else {
			msb = s.kx[1]
		}
		if s.fMaster[k]-sb < 3 {
			k = s.nMaster
		}
		if sb == s.kx[1]+s.m[1] {
			break
		}
	}
	if s.numPatches > 1 && s.patchNum[s.numPatches-1] < 3 {
		s.numPatches--
	}
	return nil
}

func (s *sbrElem) makeLimTable() {
	if s.limiterBands > 0 {
		warp := [3]float32{1.32715174233856803909, 1.18509277094158210129, 1.11987160404675912501}[s.limiterBands-1]
		var borders [8]int
		borders[0] = s.kx[1]
		for k := 1; k <= s.numPatches; k++ {
			borders[k] = borders[k-1] + s.patchNum[k-1]
		}
		copy(s.fLim[:], s.fLow[:s.n[0]+1])
		if s.numPatches > 1 {
			copy(s.fLim[s.n[0]+1:], borders[1:s.numPatches])
		}
		sortInts(s.fLim[:s.numPatches+s.n[0]])
		s.nLim = s.n[0] + s.numPatches - 1
		in := 1
		out := 0
		inBorders := func(v int) bool {
			for _, b := range borders[:s.numPatches+1] {
				if b == v {
					return true
				}
			}
			return false
		}
		for out < s.nLim {
			switch {
			case float32(s.fLim[in]) >= float32(s.fLim[out])*warp:
				out++
				s.fLim[out] = s.fLim[in]
				in++
			case s.fLim[in] == s.fLim[out] || !inBorders(s.fLim[in]):
				in++
				s.nLim--
			case !inBorders(s.fLim[out]):
				s.fLim[out] = s.fLim[in]
				in++
				s.nLim--
			default:
				out++
				s.fLim[out] = s.fLim[in]
				in++
			}
		}
	} else {
		s.fLim[0] = s.fLow[0]
		s.fLim[1] = s.fLow[s.n[0]]
		s.nLim = 1
	}
}

func (s *sbrElem) makeFDerived() error {
	sp := s.spec
	s.n[1] = s.nMaster - sp.xoverBand
	s.n[0] = (s.n[1] + 1) >> 1
	if s.n[1] > 24 || s.n[1] < 1 {
		return errSBR
	}
	copy(s.fHigh[:s.n[1]+1], s.fMaster[sp.xoverBand:sp.xoverBand+s.n[1]+1])
	s.m[1] = s.fHigh[s.n[1]] - s.fHigh[0]
	s.kx[1] = s.fHigh[0]
	if s.kx[1]+s.m[1] > 64 || s.kx[1] > 32 {
		return errSBR
	}
	s.fLow[0] = s.fHigh[0]
	temp := s.n[1] & 1
	for k := 1; k <= s.n[0]; k++ {
		s.fLow[k] = s.fHigh[2*k-temp]
	}
	nq := int(math.RoundToEven(float64(float32(sp.noiseBands) * float32(math.Log2(float64(float32(s.k[2])/float32(s.kx[1])))))))
	if nq < 1 {
		nq = 1
	}
	s.nQ = nq
	if s.nQ > 5 {
		return errSBR
	}
	s.fNoise[0] = s.fLow[0]
	temp = 0
	for k := 1; k <= s.nQ; k++ {
		temp += (s.n[0] - temp) / (s.nQ + 1 - k)
		s.fNoise[k] = s.fLow[temp]
	}
	if err := s.calcPatches(); err != nil {
		return err
	}
	s.makeLimTable()
	s.data[0].fIndexNoise = 0
	s.data[1].fIndexNoise = 0
	return nil
}

func (s *sbrElem) resetTables() error {
	if err := s.makeFMaster(); err != nil {
		return err
	}
	return s.makeFDerived()
}

// ---- dequantisation --------------------------------------------------------

func (s *sbrElem) dequant() {
	if s.cpe && s.coupling {
		c0, c1 := &s.data[0], &s.data[1]
		alpha, pan := 0.5, 24.0
		if c0.ampRes != 0 {
			alpha, pan = 1.0, 12.0
		}
		for e := 1; e <= c0.numEnv; e++ {
			for k := 0; k < s.n[c0.freqRes[e]]; k++ {
				t1 := math.Exp2(float64(c0.envFacsQ[e][k])*alpha + 7)
				t2 := math.Exp2((pan - float64(c1.envFacsQ[e][k])) * alpha)
				if t1 > 1e20 {
					t1 = 1
				}
				fac := t1 / (1 + t2)
				c0.envFacs[e][k] = fac
				c1.envFacs[e][k] = fac * t2
			}
		}
		for e := 1; e <= c0.numNoise; e++ {
			for k := 0; k < s.nQ; k++ {
				t1 := math.Exp2(6 - float64(c0.noiseFacsQ[e][k]) + 1)
				t2 := math.Exp2(12 - float64(c1.noiseFacsQ[e][k]))
				if t1 > 1e20 {
					t1 = 1
				}
				fac := t1 / (1 + t2)
				c0.noiseFacs[e][k] = fac
				c1.noiseFacs[e][k] = fac * t2
			}
		}
		return
	}
	nch := 1
	if s.cpe {
		nch = 2
	}
	for ch := 0; ch < nch; ch++ {
		c := &s.data[ch]
		alpha := 0.5
		if c.ampRes != 0 {
			alpha = 1.0
		}
		for e := 1; e <= c.numEnv; e++ {
			for k := 0; k < s.n[c.freqRes[e]]; k++ {
				v := math.Exp2(alpha*float64(c.envFacsQ[e][k]) + 6)
				if v > 1e20 {
					v = 1
				}
				c.envFacs[e][k] = v
			}
		}
		for e := 1; e <= c.numNoise; e++ {
			for k := 0; k < s.nQ; k++ {
				c.noiseFacs[e][k] = math.Exp2(6 - float64(c.noiseFacsQ[e][k]))
			}
		}
	}
}

// ---- processing ------------------------------------------------------------

func (s *sbrElem) lfGen(c *sbrChan) {
	const tHFGen = 8
	for k := range s.xLow {
		for i := range s.xLow[k] {
			s.xLow[k][i] = [2]float64{}
		}
	}
	for k := 0; k < s.kx[1]; k++ {
		for i := tHFGen; i < 32+tHFGen; i++ {
			s.xLow[k][i] = c.wCur[i-tHFGen][k]
		}
	}
	for k := 0; k < s.kx[0]; k++ {
		for i := 0; i < tHFGen; i++ {
			s.xLow[k][i] = c.wPrev[i+32-tHFGen][k]
		}
	}
}

func (s *sbrElem) inverseFilter() {
	for k := 0; k < s.k[0]; k++ {
		x := &s.xLow[k]
		// phi(i,j) = sum_{n=0}^{37} x[n+2-i] conj(x[n+2-j])
		var p01, p02, p12 [2]float64
		var p11, p22 float64
		for n := 0; n < 38; n++ {
			a0, a1, a2 := x[n+2], x[n+1], x[n]
			// x[n+2]*conj(x[n+1])
			p01[0] += a0[0]*a1[0] + a0[1]*a1[1]
			p01[1] += a0[1]*a1[0] - a0[0]*a1[1]
			p02[0] += a0[0]*a2[0] + a0[1]*a2[1]
			p02[1] += a0[1]*a2[0] - a0[0]*a2[1]
			p12[0] += a1[0]*a2[0] + a1[1]*a2[1]
			p12[1] += a1[1]*a2[0] - a1[0]*a2[1]
			p11 += a1[0]*a1[0] + a1[1]*a1[1]
			p22 += a2[0]*a2[0] + a2[1]*a2[1]
		}
		dk := p22*p11 - (p12[0]*p12[0]+p12[1]*p12[1])/1.000001
		var a1r, a1i, a0r, a0i float64
		if dk != 0 {
			tr := p01[0]*p12[0] - p01[1]*p12[1] - p02[0]*p11
			ti := p01[0]*p12[1] + p01[1]*p12[0] - p02[1]*p11
			a1r, a1i = tr/dk, ti/dk
		}
		if p11 != 0 {
			tr := p01[0] + a1r*p12[0] + a1i*p12[1]
			ti := p01[1] + a1i*p12[0] - a1r*p12[1]
			a0r, a0i = -tr/p11, -ti/p11
		}
		if a1r*a1r+a1i*a1i >= 16 || a0r*a0r+a0i*a0i >= 16 {
			a1r, a1i, a0r, a0i = 0, 0, 0, 0
		}
		s.alpha0[k] = [2]float64{a0r, a0i}
		s.alpha1[k] = [2]float64{a1r, a1i}
	}
}

func (s *sbrElem) chirp(c *sbrChan) {
	bwTab := [4]float64{0, 0.75, 0.9, 0.98}
	for i := 0; i < s.nQ; i++ {
		var nb float64
		if c.invfMode[0][i]+c.invfMode[1][i] == 1 {
			nb = sbrChirpTransition
		} else {
			nb = bwTab[c.invfMode[0][i]]
		}
		if nb < c.bwArray[i] {
			nb = 0.75*nb + 0.25*c.bwArray[i]
		} else {
			nb = 0.90625*nb + 0.09375*c.bwArray[i]
		}
		if nb < 0.015625 {
			nb = 0
		}
		c.bwArray[i] = nb
	}
}

var sbrChirpTransition = 0.6

func (s *sbrElem) hfGen(c *sbrChan) error {
	g := 0
	k := s.kx[1]
	for j := 0; j < s.numPatches; j++ {
		for x := 0; x < s.patchNum[j]; x, k = x+1, k+1 {
			p := s.patchStart[j] + x
			for g <= s.nQ && k >= s.fNoise[g] {
				g++
			}
			g--
			if g < 0 {
				return errSBR
			}
			bw := c.bwArray[g]
			a0 := s.alpha0[p]
			a1 := s.alpha1[p]
			al := [4]float64{a1[0] * bw * bw, a1[1] * bw * bw, a0[0] * bw, a0[1] * bw}
			xl := &s.xLow[p]
			xh := &s.xHigh[k]
			for i := 2 * c.tEnv[0]; i < 2*c.tEnv[c.numEnv]; i++ {
				t := i + sbrEnvOff
				xh[t][0] = xl[t-2][0]*al[0] - xl[t-2][1]*al[1] + xl[t-1][0]*al[2] - xl[t-1][1]*al[3] + xl[t][0]
				xh[t][1] = xl[t-2][1]*al[0] + xl[t-2][0]*al[1] + xl[t-1][1]*al[2] + xl[t-1][0]*al[3] + xl[t][1]
			}
		}
	}
	if k < s.m[1]+s.kx[1] {
		for ; k < s.m[1]+s.kx[1]; k++ {
			s.xHigh[k] = [40][2]float64{}
		}
	}
	return nil
}

func (s *sbrElem) mapping(c *sbrChan) error {
	for i := range c.sIndexMapped[1] {
		for e := 1; e < 8; e++ {
			c.sIndexMapped[e][i] = 0
		}
	}
	for e := 0; e < c.numEnv; e++ {
		ilim := s.n[c.freqRes[e+1]]
		table := s.fLow[:]
		if c.freqRes[e+1] != 0 {
			table = s.fHigh[:]
		}
		if s.kx[1] != table[0] {
			return errSBR
		}
		for i := 0; i < ilim; i++ {
			for m := table[i]; m < table[i+1]; m++ {
				s.eOrigMapped[e][m-s.kx[1]] = c.envFacs[e+1][i]
			}
		}
		k := 0
		if c.numNoise > 1 && c.tEnv[e] >= c.tQ[1] {
			k = 1
		}
		for i := 0; i < s.nQ; i++ {
			for m := s.fNoise[i]; m < s.fNoise[i+1]; m++ {
				s.qMapped[e][m-s.kx[1]] = c.noiseFacs[k+1][i]
			}
		}
		for i := 0; i < s.n[1]; i++ {
			if c.addHarmonicFlag {
				mid := (s.fHigh[i] + s.fHigh[i+1]) >> 1
				v := 0
				if c.addHarmonic[i] && (e >= c.eA[1] || c.sIndexMapped[0][mid-s.kx[1]] == 1) {
					v = 1
				}
				c.sIndexMapped[e+1][mid-s.kx[1]] = v
			}
		}
		for i := 0; i < ilim; i++ {
			present := 0.0
			for m := table[i]; m < table[i+1]; m++ {
				if c.sIndexMapped[e+1][m-s.kx[1]] != 0 {
					present = 1
					break
				}
			}
			for m := table[i]; m < table[i+1]; m++ {
				s.sMapped[e][m-s.kx[1]] = present
			}
		}
	}
	c.sIndexMapped[0] = c.sIndexMapped[c.numEnv]
	return nil
}

func (s *sbrElem) envEstimate(c *sbrChan) {
	kx1 := s.kx[1]
	if s.interpol != 0 {
		for e := 0; e < c.numEnv; e++ {
			recip := 0.5 / float64(c.tEnv[e+1]-c.tEnv[e])
			ilb := c.tEnv[e]*2 + sbrEnvOff
			iub := c.tEnv[e+1]*2 + sbrEnvOff
			for m := 0; m < s.m[1]; m++ {
				sum := 0.0
				xh := &s.xHigh[m+kx1]
				for t := ilb; t < iub; t++ {
					sum += xh[t][0]*xh[t][0] + xh[t][1]*xh[t][1]
				}
				s.eCurr[e][m] = sum * recip
			}
		}
		return
	}
	for e := 0; e < c.numEnv; e++ {
		envSize := 2 * (c.tEnv[e+1] - c.tEnv[e])
		ilb := c.tEnv[e]*2 + sbrEnvOff
		iub := c.tEnv[e+1]*2 + sbrEnvOff
		table := s.fLow[:]
		if c.freqRes[e+1] != 0 {
			table = s.fHigh[:]
		}
		for p := 0; p < s.n[c.freqRes[e+1]]; p++ {
			sum := 0.0
			den := envSize * (table[p+1] - table[p])
			for k := table[p]; k < table[p+1]; k++ {
				xh := &s.xHigh[k]
				for t := ilb; t < iub; t++ {
					sum += xh[t][0]*xh[t][0] + xh[t][1]*xh[t][1]
				}
			}
			sum /= float64(den)
			for k := table[p]; k < table[p+1]; k++ {
				s.eCurr[e][k-kx1] = sum
			}
		}
	}
}

const sbrFltEps = 1.1920928955078125e-07

func (s *sbrElem) gainCalc(c *sbrChan) {
	limgain := [4]float64{0.70795, 1.0, 1.41254, 10000000000}
	kx1 := s.kx[1]
	for e := 0; e < c.numEnv; e++ {
		delta := !(e == c.eA[1] || e == c.eA[0])
		for k := 0; k < s.nLim; k++ {
			var sum [2]float64
			lo, hi := s.fLim[k]-kx1, s.fLim[k+1]-kx1
			for m := lo; m < hi; m++ {
				temp := s.eOrigMapped[e][m] / (1 + s.qMapped[e][m])
				s.qM[e][m] = math.Sqrt(temp * s.qMapped[e][m])
				s.sM[e][m] = math.Sqrt(temp * float64(c.sIndexMapped[e+1][m]))
				if s.sMapped[e][m] == 0 {
					if delta {
						s.gain[e][m] = math.Sqrt(s.eOrigMapped[e][m] / ((1 + s.eCurr[e][m]) * (1 + s.qMapped[e][m])))
					} else {
						s.gain[e][m] = math.Sqrt(s.eOrigMapped[e][m] / (1 + s.eCurr[e][m]))
					}
				} else {
					s.gain[e][m] = math.Sqrt(s.eOrigMapped[e][m] * s.qMapped[e][m] / ((1 + s.eCurr[e][m]) * (1 + s.qMapped[e][m])))
				}
				sum[0] += s.eOrigMapped[e][m]
				sum[1] += s.eCurr[e][m]
			}
			gainMax := limgain[s.limiterGains] * math.Sqrt((sbrFltEps+sum[0])/(sbrFltEps+sum[1]))
			if gainMax > 100000 {
				gainMax = 100000
			}
			for m := lo; m < hi; m++ {
				qMax := s.qM[e][m] * gainMax / s.gain[e][m]
				if qMax < s.qM[e][m] {
					s.qM[e][m] = qMax
				}
				if gainMax < s.gain[e][m] {
					s.gain[e][m] = gainMax
				}
			}
			sum[0], sum[1] = 0, 0
			for m := lo; m < hi; m++ {
				sum[0] += s.eOrigMapped[e][m]
				sum[1] += s.eCurr[e][m]*s.gain[e][m]*s.gain[e][m] + s.sM[e][m]*s.sM[e][m]
				if delta && s.sM[e][m] == 0 {
					sum[1] += s.qM[e][m] * s.qM[e][m]
				}
			}
			boost := math.Sqrt((sbrFltEps + sum[0]) / (sbrFltEps + sum[1]))
			if boost > 1.584893192 {
				boost = 1.584893192
			}
			for m := lo; m < hi; m++ {
				s.gain[e][m] *= boost
				s.qM[e][m] *= boost
				s.sM[e][m] *= boost
			}
		}
	}
}

var sbrHSmooth = [5]float64{0.33333333333333, 0.30150283239582, 0.21816949906249, 0.11516383427084, 0.03183050093751}

func (s *sbrElem) hfAssemble(c *sbrChan) {
	hSL := 0
	if s.smoothing == 0 {
		hSL = 4
	}
	kx := s.kx[1]
	mMax := s.m[1]
	if s.reset {
		for i := 0; i < hSL; i++ {
			copy(c.gTemp[i+2*c.tEnv[0]][:mMax], s.gain[0][:mMax])
			copy(c.qTemp[i+2*c.tEnv[0]][:mMax], s.qM[0][:mMax])
		}
	} else if hSL != 0 {
		for i := 0; i < 4; i++ {
			c.gTemp[i+2*c.tEnv[0]] = c.gTemp[i+2*c.tEnvNumEnvOld]
			c.qTemp[i+2*c.tEnv[0]] = c.qTemp[i+2*c.tEnvNumEnvOld]
		}
	}
	for e := 0; e < c.numEnv; e++ {
		for i := 2 * c.tEnv[e]; i < 2*c.tEnv[e+1]; i++ {
			copy(c.gTemp[hSL+i][:mMax], s.gain[e][:mMax])
			copy(c.qTemp[hSL+i][:mMax], s.qM[e][:mMax])
		}
	}
	indexNoise := c.fIndexNoise
	indexSine := c.fIndexSine
	var gFiltTab, qFiltTab [64]float64
	for e := 0; e < c.numEnv; e++ {
		for i := 2 * c.tEnv[e]; i < 2*c.tEnv[e+1]; i++ {
			var gFilt, qFilt []float64
			if hSL != 0 && e != c.eA[0] && e != c.eA[1] {
				gFilt, qFilt = gFiltTab[:], qFiltTab[:]
				for m := 0; m < mMax; m++ {
					idx1 := i + hSL
					gv, qv := 0.0, 0.0
					for j := 0; j <= hSL; j++ {
						gv += c.gTemp[idx1-j][m] * sbrHSmooth[j]
						qv += c.qTemp[idx1-j][m] * sbrHSmooth[j]
					}
					gFilt[m], qFilt[m] = gv, qv
				}
			} else {
				gFilt, qFilt = c.gTemp[i+hSL][:], c.qTemp[i+hSL][:]
			}
			y := &c.yCur[i]
			for m := 0; m < mMax; m++ {
				y[kx+m][0] = s.xHigh[kx+m][i+sbrEnvOff][0] * gFilt[m]
				y[kx+m][1] = s.xHigh[kx+m][i+sbrEnvOff][1] * gFilt[m]
			}
			if e != c.eA[0] && e != c.eA[1] {
				// noise and sinusoids
				var ps0, ps1 float64
				switch indexSine & 3 {
				case 0:
					ps0, ps1 = 1, 0
				case 1:
					ps0, ps1 = 0, float64(1-2*(kx&1))
				case 2:
					ps0, ps1 = -1, 0
				default:
					ps0, ps1 = 0, -float64(1-2*(kx&1))
				}
				noise := indexNoise
				for m := 0; m < mMax; m++ {
					noise = (noise + 1) & 0x1ff
					if s.sM[e][m] != 0 {
						y[kx+m][0] += s.sM[e][m] * ps0
						y[kx+m][1] += s.sM[e][m] * ps1
					} else {
						y[kx+m][0] += qFilt[m] * float64(sbrNoise[2*noise])
						y[kx+m][1] += qFilt[m] * float64(sbrNoise[2*noise+1])
					}
					ps1 = -ps1
				}
			} else {
				idx := indexSine & 1
				A := 1 - ((indexSine + (kx & 1)) & 2)
				B := A
				if idx == 1 {
					B = -A
				}
				for m := 0; m < mMax; m++ {
					a := A
					if m&1 == 1 {
						a = B
					}
					y[kx+m][idx] += s.sM[e][m] * float64(a)
				}
			}
			indexNoise = (indexNoise + mMax) & 0x1ff
			indexSine = (indexSine + 1) & 3
		}
	}
	c.fIndexNoise = indexNoise
	c.fIndexSine = indexSine
}

// xGen assembles the QMF domain output of one channel: 32 slots of 64 bands
// plus the 6 low band look ahead slots the PS hybrid filter needs.
func (s *sbrElem) xGen(c *sbrChan) {
	const iF = 32
	iTemp := 2*c.tEnvNumEnvOld - iF
	if iTemp < 0 {
		iTemp = 0
	}
	if iTemp > 32 {
		iTemp = 32
	}
	for l := range c.xOut {
		c.xOut[l] = [64][2]float64{}
	}
	k := 0
	for ; k < s.kx[0]; k++ {
		for i := 0; i < iTemp; i++ {
			c.xOut[i][k] = s.xLow[k][i+sbrEnvOff]
		}
	}
	for ; k < s.kx[0]+s.m[0]; k++ {
		for i := 0; i < iTemp; i++ {
			c.xOut[i][k] = c.yPrev[i+iF][k]
		}
	}
	for k = 0; k < s.kx[1]; k++ {
		for i := iTemp; i < 38; i++ {
			c.xOut[i][k] = s.xLow[k][i+sbrEnvOff]
		}
	}
	for ; k < s.kx[1]+s.m[1]; k++ {
		for i := iTemp; i < iF; i++ {
			c.xOut[i][k] = c.yCur[i][k]
		}
	}
}

// apply runs the SBR tools on the core output (in: 1 or 2 channels of 1024
// samples) and writes the output (2048 samples per channel, or 1024 in
// downsampled mode). With PS active a mono input produces two outputs. It
// returns the number of output channels.
func (s *sbrElem) apply(in [][]float32, out [][]float32) int {
	nch := len(in)
	if s.start && !s.ready {
		s.turnoff()
	}
	if !s.kxPushed {
		s.kx[0] = s.kx[1]
		s.m[0] = s.m[1]
	} else {
		s.kxPushed = false
	}
	if s.start {
		s.dequant()
		s.ready = false
	}
	for ch := 0; ch < nch; ch++ {
		c := &s.data[ch]
		c.ana.run(in[ch], &c.wCur)
		s.lfGen(c)
		if s.start {
			s.inverseFilter()
			s.chirp(c)
			ok := s.hfGen(c) == nil
			if ok {
				ok = s.mapping(c) == nil
			}
			if ok {
				s.envEstimate(c)
				s.gainCalc(c)
				s.hfAssemble(c)
			}
		}
		s.xGen(c)
		c.yPrev, c.yCur = c.yCur, c.yPrev
		c.wPrev, c.wCur = c.wCur, c.wPrev
	}
	nout := nch
	if s.psOK && nch == 1 {
		nout = 2
		if s.ps != nil && s.ps.start {
			s.applyPS()
		} else {
			s.data[1].xOut = s.data[0].xOut
		}
	}
	for ch := 0; ch < nout; ch++ {
		c := &s.data[ch]
		c.syn.run(&c.xOut, out[ch], s.down)
	}
	s.reset = false
	return nout
}
