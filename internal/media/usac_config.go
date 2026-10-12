package media

import (
	"fmt"
)

// USAC (Unified Speech and Audio Coding, xHE-AAC, ISO/IEC 23003-3 / AOT 42)
// configuration parsing. The decoding process lives in usac_*.go; the
// behaviour of the parts that FFmpeg's libavcodec/aac USAC decoder
// (LGPL-2.1-or-later, see NOTICE) implements follows that decoder.

const (
	usacElemSCE = 0
	usacElemCPE = 1
	usacElemLFE = 2
	usacElemEXT = 3
)

// usacExt element types (usacExtElementType).
const (
	usacExtFill     = 0
	usacExtMPEGS    = 1
	usacExtSAOC     = 2
	usacExtPreroll  = 3
	usacExtUniDRC   = 4
	usacCfgExtFill  = 0
	usacCfgExtLoud  = 2
	usacCfgExtSID   = 7
	usacCfgExtDMX   = 1
	usacCfgExtScene = 3
)

var usacRates = [32]int{
	96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050,
	16000, 12000, 11025, 8000, 7350, -1, -1, 57600,
	51200, 40000, 38400, 34150, 28800, 25600, 20000, 19200,
	17075, 14400, 12800, 9600, -1, -1, -1, -1,
}

type usacSbrCfg struct {
	harmonic, interTES, pvc bool

	startFreq, stopFreq               int
	freqScale, alterScale, noiseBands int
	limiterBands, limiterGains        int
	interpolFreq, smoothingMode       int
}

type usacMpsCfg struct {
	freqRes, fixedGain, tempShapeConfig, decorrConfig int
	highRateMode, phaseCoding                         bool
	ottBandsPhasePresent                              bool
	ottBandsPhase                                     int
	residualCoding                                    bool
	residualBands                                     int
	pseudoLR                                          bool
	envQuantMode                                      int
}

type usacElemCfg struct {
	typ       int
	twMDCT    bool
	noiseFill bool
	sbr       usacSbrCfg
	hasSBR    bool
	stereoCfg int // stereoConfigIndex (0: no MPS212)
	mps       usacMpsCfg

	// extension elements
	extType    int
	extDefLen  int
	extFrag    bool
	extPayload []byte // fragment accumulation
}

type usacLoudnessInfo struct {
	drcSetID, eqSetID, downmixID int
	samplePeak                   int // 12 bit, -1 absent
	truePeak                     int
	nMeas                        int
	methodDef                    [16]int
	methodVal                    [16]float64 // dB (loudness values) or the raw value
	measurement                  [16]int
	reliability                  [16]int
}

type usacConfig struct {
	sfIdx          int
	rate           int // usacSamplingFrequency (output rate)
	coreSbrIdx     int
	coreLen        int // 768 or 1024
	sbrRatioIdx    int // 0 none, 1 4:1, 2 8:3, 3 2:1
	chCfgIdx       int
	nOutCh         int
	chPos          []int // bsOutputChannelPos for chCfgIdx == 0
	elems          []usacElemCfg
	loudness       []usacLoudnessInfo
	albumLoudness  []usacLoudnessInfo
	streamID       int
	coreRate       int // core sampling rate (rate / sbr ratio)
	outFrame       int // output samples per frame
	nCoreCh        int
	hasLoudnessExt bool
}

func usacEscaped(r *aacBits, n1, n2, n3 int) int {
	v := r.bits(n1)
	if v < 1<<uint(n1)-1 {
		return v
	}
	v2 := r.bits(n2)
	v += v2
	if n3 != 0 && v2 == 1<<uint(n2)-1 {
		v += r.bits(n3)
	}
	return v
}

// usacChannelsForConfig returns the element layout implied by
// channelConfigurationIndex (ISO/IEC 23003-3 table 67).
func usacChannelsForConfig(idx int) (n int, layout []int, ok bool) {
	switch idx {
	case 1:
		return 1, []int{usacElemSCE}, true
	case 2:
		return 2, []int{usacElemCPE}, true
	case 3:
		return 3, []int{usacElemSCE, usacElemCPE}, true
	case 4:
		return 4, []int{usacElemSCE, usacElemCPE, usacElemSCE}, true
	case 5:
		return 5, []int{usacElemSCE, usacElemCPE, usacElemCPE}, true
	case 6:
		return 6, []int{usacElemSCE, usacElemCPE, usacElemCPE, usacElemLFE}, true
	case 7:
		return 8, []int{usacElemSCE, usacElemCPE, usacElemCPE, usacElemCPE, usacElemLFE}, true
	case 11:
		return 7, []int{usacElemSCE, usacElemCPE, usacElemCPE, usacElemSCE, usacElemLFE}, true
	case 12:
		return 8, []int{usacElemSCE, usacElemCPE, usacElemCPE, usacElemCPE, usacElemLFE}, true
	case 14:
		return 8, []int{usacElemSCE, usacElemCPE, usacElemCPE, usacElemLFE, usacElemCPE}, true
	}
	return 0, nil, false
}

func (r *aacBits) usacSbrDefault(c *usacSbrCfg) {
	c.harmonic = r.bit()
	c.interTES = r.bit()
	c.pvc = r.bit()
	c.startFreq = r.bits(4)
	c.stopFreq = r.bits(4)
	e1 := r.bit()
	e2 := r.bit()
	c.freqScale, c.alterScale, c.noiseBands = 2, 1, 2
	if e1 {
		c.freqScale = r.bits(2)
		c.alterScale = r.bits(1)
		c.noiseBands = r.bits(2)
	}
	c.limiterBands, c.limiterGains, c.interpolFreq, c.smoothingMode = 2, 2, 1, 1
	if e2 {
		c.limiterBands = r.bits(2)
		c.limiterGains = r.bits(2)
		c.interpolFreq = r.bits(1)
		c.smoothingMode = r.bits(1)
	}
}

func (r *aacBits) usacMps212Cfg(e *usacElemCfg) {
	m := &e.mps
	m.freqRes = r.bits(3)
	m.fixedGain = r.bits(3)
	m.tempShapeConfig = r.bits(2)
	m.decorrConfig = r.bits(2)
	m.highRateMode = r.bit()
	m.phaseCoding = r.bit()
	m.ottBandsPhasePresent = r.bit()
	if m.ottBandsPhasePresent {
		m.ottBandsPhase = r.bits(5)
	}
	m.residualCoding = e.stereoCfg >= 2
	if m.residualCoding {
		m.residualBands = r.bits(5)
		if m.residualBands > m.ottBandsPhase {
			m.ottBandsPhase = m.residualBands
		}
		m.pseudoLR = r.bit()
	}
	if m.tempShapeConfig == 2 {
		m.envQuantMode = r.bits(1)
	}
}

// usacLoudnessInfo parses a loudnessInfo() of ISO/IEC 23003-4 (version 0 and,
// with version 1, the eqSetId field).
func (r *aacBits) usacLoudnessInfo(li *usacLoudnessInfo, version int) {
	li.drcSetID = r.bits(6)
	if version >= 1 {
		li.eqSetID = r.bits(6)
	}
	li.downmixID = r.bits(7)
	li.samplePeak, li.truePeak = -1, -1
	if r.bit() {
		li.samplePeak = r.bits(12)
	}
	if r.bit() {
		li.truePeak = r.bits(12)
		r.bits(4)
		r.bits(2)
	}
	li.nMeas = r.bits(4)
	for i := 0; i < li.nMeas && i < len(li.methodDef); i++ {
		li.methodDef[i] = r.bits(4)
		switch li.methodDef[i] {
		case 8: // mixing level
			li.methodVal[i] = float64(r.bits(5)) + 80
		case 9: // room type
			li.methodVal[i] = float64(r.bits(2))
		case 7: // loudness range
			v := r.bits(8)
			switch {
			case v == 0:
				li.methodVal[i] = 0
			case v <= 128:
				li.methodVal[i] = float64(v) * 0.25
			case v <= 204:
				li.methodVal[i] = 0.5*float64(v) - 32
			default:
				li.methodVal[i] = float64(v) - 134
			}
		case 10: // short-term loudness
			li.methodVal[i] = -116 + 0.5*float64(r.bits(8))
		default: // program, anchor, max loudness range, momentary max, short-term max, unknown
			li.methodVal[i] = -57.75 + 0.25*float64(r.bits(8))
		}
		li.measurement[i] = r.bits(4)
		li.reliability[i] = r.bits(2)
	}
}

// parseUsacConfig parses a UsacConfig() (the AudioSpecificConfig fields that
// precede it are handled by parseUsacASC).
func parseUsacConfig(r *aacBits) (*usacConfig, error) {
	c := &usacConfig{}
	c.sfIdx = r.bits(5)
	if c.sfIdx == 0x1f {
		c.rate = r.bits(24)
	} else {
		c.rate = usacRates[c.sfIdx]
	}
	if c.rate <= 0 {
		return nil, fmt.Errorf("media: usac bad sampling frequency index %d", c.sfIdx)
	}
	c.coreSbrIdx = r.bits(3)
	switch c.coreSbrIdx {
	case 0:
		c.coreLen, c.sbrRatioIdx = 768, 0
	case 1:
		c.coreLen, c.sbrRatioIdx = 1024, 0
	case 2:
		c.coreLen, c.sbrRatioIdx = 768, 2
	case 3:
		c.coreLen, c.sbrRatioIdx = 1024, 3
	case 4:
		c.coreLen, c.sbrRatioIdx = 1024, 1
	default:
		return nil, fmt.Errorf("%w: usac coreSbrFrameLengthIndex %d", ErrUnsupported, c.coreSbrIdx)
	}
	switch c.sbrRatioIdx {
	case 0:
		c.outFrame = c.coreLen
		c.coreRate = c.rate
	case 1:
		c.outFrame = c.coreLen * 4
		c.coreRate = c.rate / 4
	case 2:
		c.outFrame = c.coreLen * 8 / 3
		c.coreRate = c.rate * 3 / 8
	case 3:
		c.outFrame = c.coreLen * 2
		c.coreRate = c.rate / 2
	}
	c.chCfgIdx = r.bits(5)
	nExp := 0
	if c.chCfgIdx == 0 {
		c.nOutCh = usacEscaped(r, 5, 8, 16)
		if c.nOutCh > 64 || c.nOutCh == 0 {
			return nil, fmt.Errorf("media: usac bad channel count")
		}
		for i := 0; i < c.nOutCh; i++ {
			c.chPos = append(c.chPos, r.bits(5))
		}
		nExp = c.nOutCh
	} else {
		n, _, ok := usacChannelsForConfig(c.chCfgIdx)
		if !ok {
			return nil, fmt.Errorf("%w: usac channelConfigurationIndex %d", ErrUnsupported, c.chCfgIdx)
		}
		c.nOutCh = n
		nExp = n
	}
	nElems := usacEscaped(r, 4, 8, 16) + 1
	if nElems > 64 {
		return nil, fmt.Errorf("media: usac too many elements")
	}
	nch := 0
	for i := 0; i < nElems; i++ {
		var e usacElemCfg
		e.typ = r.bits(2)
		if e.typ != usacElemEXT {
			if nch+1 > nExp {
				return nil, fmt.Errorf("media: usac too many channels for the configuration")
			}
		}
		switch e.typ {
		case usacElemSCE, usacElemCPE:
			e.twMDCT = r.bit()
			e.noiseFill = r.bit()
			if c.sbrRatioIdx > 0 {
				e.hasSBR = true
				r.usacSbrDefault(&e.sbr)
			}
			if e.typ == usacElemCPE {
				if c.sbrRatioIdx > 0 {
					e.stereoCfg = r.bits(2)
				}
				if e.stereoCfg > 0 {
					r.usacMps212Cfg(&e)
				}
				nch += 2
			} else {
				nch++
			}
		case usacElemLFE:
			nch++
		case usacElemEXT:
			e.extType = usacEscaped(r, 4, 8, 16)
			cl := usacEscaped(r, 4, 8, 16)
			if r.bit() {
				e.extDefLen = usacEscaped(r, 8, 16, 0) + 1
			}
			e.extFrag = r.bit()
			r.skip(8 * cl)
		}
		if r.err {
			return nil, fmt.Errorf("media: usac config truncated")
		}
		c.elems = append(c.elems, e)
	}
	c.nCoreCh = nch
	if c.chCfgIdx != 0 && nch != c.nOutCh {
		return nil, fmt.Errorf("media: usac element layout does not match channelConfigurationIndex")
	}
	if c.chCfgIdx == 0 {
		c.nOutCh = nch
	}
	if r.bit() { // usacConfigExtensionPresent
		n := usacEscaped(r, 2, 4, 8) + 1
		for i := 0; i < n && !r.err; i++ {
			typ := usacEscaped(r, 4, 8, 16)
			l := usacEscaped(r, 4, 8, 16)
			start := r.pos
			switch typ {
			case usacCfgExtLoud:
				c.hasLoudnessExt = true
				na := r.bits(6)
				ni := r.bits(6)
				for k := 0; k < na; k++ {
					var li usacLoudnessInfo
					r.usacLoudnessInfo(&li, 0)
					c.albumLoudness = append(c.albumLoudness, li)
				}
				for k := 0; k < ni; k++ {
					var li usacLoudnessInfo
					r.usacLoudnessInfo(&li, 0)
					c.loudness = append(c.loudness, li)
				}
				// loudnessInfoSetExtension: V1 entries (with eqSetId) follow
				if r.bit() {
					for {
						typ := r.bits(4)
						if typ == 0 {
							break
						}
						bl := r.bits(4) + 4
						size := r.bits(bl) + 1
						ep := r.pos
						if typ == 1 {
							na1 := r.bits(6)
							ni1 := r.bits(6)
							for k := 0; k < na1; k++ {
								var li usacLoudnessInfo
								r.usacLoudnessInfo(&li, 1)
								c.albumLoudness = append(c.albumLoudness, li)
							}
							for k := 0; k < ni1; k++ {
								var li usacLoudnessInfo
								r.usacLoudnessInfo(&li, 1)
								c.loudness = append(c.loudness, li)
							}
						}
						r.pos = ep + size
						if r.err {
							break
						}
					}
				}
			case usacCfgExtSID:
				c.streamID = r.bits(16)
			}
			r.pos = start + 8*l
		}
	}
	if r.err {
		return nil, fmt.Errorf("media: usac config truncated")
	}
	return c, nil
}

// parseUsacASC parses an AudioSpecificConfig whose audioObjectType is 42.
// It returns (nil, nil) when the object type is not USAC.
func parseUsacASC(b []byte) (*usacConfig, error) {
	r := &aacBits{b: b}
	aot := r.bits(5)
	if aot == 31 {
		aot = 32 + r.bits(6)
	}
	if aot != 42 {
		return nil, nil
	}
	if i := r.bits(4); i == 15 {
		r.bits(24)
	}
	r.bits(4) // channelConfiguration
	return parseUsacConfig(r)
}

// isUSACConfig reports whether an AudioSpecificConfig signals USAC.
func isUSACConfig(b []byte) bool {
	if len(b) < 2 {
		return false
	}
	r := &aacBits{b: b}
	aot := r.bits(5)
	if aot == 31 {
		aot = 32 + r.bits(6)
	}
	return aot == 42
}

// signature identifies the decoding-relevant parameters of a configuration.
func (c *usacConfig) signature() string {
	s := fmt.Sprintf("%d/%d/%d/%d/%d", c.rate, c.coreSbrIdx, c.chCfgIdx, c.nOutCh, len(c.chPos))
	for _, p := range c.chPos {
		s += fmt.Sprintf(",%d", p)
	}
	for _, e := range c.elems {
		s += fmt.Sprintf("|%d,%v,%v,%+v,%d,%+v,%d,%d,%v", e.typ, e.twMDCT, e.noiseFill, e.sbr, e.stereoCfg, e.mps, e.extType, e.extDefLen, e.extFrag)
	}
	return s
}
