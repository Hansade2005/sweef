package media

import (
	"errors"
	"fmt"
	"math"
	"sync"
)

// Pure-Go AAC-LC decoder (ISO/IEC 14496-3, 1024-sample frames): ADTS and
// raw access units with an AudioSpecificConfig, mono to 7.1, sampling rates
// 7.35-96 kHz, long/short/start/stop windows with sine or KBD shapes, TNS,
// PNS, intensity and mid/side stereo, pulse data. HE-AAC v1/v2 (SBR in
// sbr.go, parametric stereo in ps.go) layers on top, signalled explicitly
// (AOT 5/29, sync extensions) or implicitly (in-band fill elements). Not
// implemented: LTP, main-profile prediction, SSR gain control, coupling
// channels and 960-sample frames; those streams report ErrUnsupported.

var errAACUnsupported = fmt.Errorf("%w: aac feature", ErrUnsupported)

// nativeAAC reports whether the pure-Go AAC decoder handles the track.
func nativeAAC(a *Track) bool {
	return a != nil && a.CodecName == "aac" && (a.Codec == "mp4a.40.2" || a.Codec == "mp4a.40.5" || a.Codec == "mp4a.40.29")
}

type aacBits struct {
	b   []byte
	pos int
	err bool
}

func (r *aacBits) peek(n int) uint32 {
	if n == 0 {
		return 0
	}
	i := r.pos >> 3
	var v uint64
	for k := 0; k < 5; k++ {
		v <<= 8
		if i+k < len(r.b) {
			v |= uint64(r.b[i+k])
		}
	}
	return uint32(v>>uint(40-(r.pos&7)-n)) & (1<<uint(n) - 1)
}

func (r *aacBits) skip(n int) {
	r.pos += n
	if r.pos > len(r.b)*8 {
		r.err = true
	}
}

func (r *aacBits) bits(n int) int {
	v := r.peek(n)
	r.skip(n)
	return int(v)
}
func (r *aacBits) bit() bool { return r.bits(1) != 0 }
func (r *aacBits) align()    { r.pos = (r.pos + 7) &^ 7 }

func (r *aacBits) huff(h *huffTab) int {
	e := h.tab[r.peek(h.maxLen)]
	if e == 0 {
		r.err = true
		return 0
	}
	r.skip(int(e >> 16))
	return int(e & 0xffff)
}

const (
	aacOnlyLong = iota
	aacLongStart
	aacEightShort
	aacLongStop
)

type aacICS struct {
	seq, shape, maxSfb int
	numWindows         int
	numGroups          int
	groupLen           [8]int
	numSwb             int
	swb                []int
}

type aacTNS struct {
	nFilt  [8]int
	length [8][3]int
	order  [8][3]int
	dir    [8][3]bool
	coef   [8][3][20]float64
}

type aacChan struct {
	ics      aacICS
	gain     int
	cb       [8][64]uint8
	sf       [8][64]int
	q        [1024]int32
	coef     [1024]float64
	pulseN   int
	pulseSfb int
	pulseOff [4]int
	pulseAmp [4]int
	tnsOn    bool
	tns      aacTNS
}

// aacDecoder decodes access units of one stream.
type aacDecoder struct {
	sfi, rate, chCfg int
	nch              int
	swbLong          []int
	swbShort         []int
	plan1024         *imdctPlan
	plan128          *imdctPlan
	overlap          [][]float64
	prevShape        []int
	rnd              int32
	buf, tmp         []float64
	pcm              [][]float32
	order            []int    // output channel permutation
	stats            aacStats // feature usage counters (tests)

	// HE-AAC
	sbr      bool // SBR output (explicit signalling or detected)
	ps       bool // PS: mono core, stereo output
	down     bool // downsampled SBR: output rate == core rate
	probe    bool // detection pass: count SBR/PS, no output
	sbrEl    []*sbrElem
	sbrBuf   [2][]float32
	sawSBR   bool
	sawPS    bool
	endBit   int   // bit position of the END element of the last frame (tests)
	elemEnds []int // bit positions after each SCE/CPE/LFE of the last frame (tests)
	elemKind []int // 0 SCE, 1 CPE, 2 LFE (tests)
	outN     int   // output samples per core frame
}

type aacStats struct{ ms, is, pns, tns, pulse, short, start, stop, faults int }

type aacConfig struct {
	aot, sfi, rate, chCfg int
	sbr, ps               bool
	extRate               int  // SBR output rate (explicit signalling), 0 if unknown
	probe                 bool // implicit signalling: detect SBR/PS from the payload
}

func parseASC(b []byte) (aacConfig, error) {
	r := &aacBits{b: b}
	var c aacConfig
	readAOT := func() int {
		a := r.bits(5)
		if a == 31 {
			a = 32 + r.bits(6)
		}
		return a
	}
	readRate := func() (int, int) {
		i := r.bits(4)
		if i == 15 {
			rate := r.bits(24)
			for j, rt := range aacRates {
				if rt == rate {
					return j, rate
				}
			}
			return -1, rate
		}
		if i < 13 {
			return i, aacRates[i]
		}
		return -1, 0
	}
	c.aot = readAOT()
	c.sfi, c.rate = readRate()
	c.chCfg = r.bits(4)
	if c.aot == 5 || c.aot == 29 {
		c.sbr = true
		c.ps = c.aot == 29
		_, c.extRate = readRate()
		c.aot = readAOT()
	}
	if r.err || c.sfi < 0 || c.sfi > 12 {
		return c, fmt.Errorf("media: bad AudioSpecificConfig")
	}
	if c.aot != 2 && c.aot != 1 {
		return c, errAACUnsupported
	}
	if r.bit() { // frameLengthFlag: 960
		return c, errAACUnsupported
	}
	if r.bit() { // dependsOnCoreCoder
		return c, errAACUnsupported
	}
	r.bit() // extensionFlag
	// backward compatible signalling
	if !c.sbr && len(b)*8-r.pos >= 16 && r.peek(11) == 0x2b7 {
		r.bits(11)
		if readAOT() == 5 {
			if r.bit() {
				c.sbr = true
				_, c.extRate = readRate()
				if len(b)*8-r.pos >= 12 && r.peek(11) == 0x548 {
					r.bits(11)
					c.ps = r.bit()
				}
			}
		}
	}
	if c.sbr && c.extRate == 0 {
		c.extRate = 2 * c.rate
	}
	if c.ps && c.chCfg == 2 {
		c.chCfg = 1 // some encoders signal the PS output layout; the core is mono
	} else if c.ps && c.chCfg > 2 {
		c.ps = false // PS needs a mono core
	}
	return c, nil
}

var aacChanOrder = map[int][]int{
	// element order -> output index (L R C LFE Ls Rs ...)
	1: {0},
	2: {0, 1},
	3: {2, 0, 1},
	4: {2, 0, 1, 3},
	5: {2, 0, 1, 3, 4},
	6: {2, 0, 1, 4, 5, 3},
	7: {2, 6, 7, 0, 1, 4, 5, 3},
}
var aacChanCount = map[int]int{1: 1, 2: 2, 3: 3, 4: 4, 5: 5, 6: 6, 7: 8}

func newAACDecoder(c aacConfig) (*aacDecoder, error) {
	n, ok := aacChanCount[c.chCfg]
	if !ok {
		return nil, errAACUnsupported
	}
	aacInitHuff()
	aacInitWindows()
	cls := aacSwbClass[c.sfi]
	d := &aacDecoder{sfi: c.sfi, rate: c.rate, chCfg: c.chCfg, nch: n, sbr: c.sbr, ps: c.ps && c.chCfg == 1,
		down: c.sbr && c.extRate != 0 && c.extRate <= c.rate, probe: c.probe, outN: 1024,
		swbLong: aacSwbLong[cls], swbShort: aacSwbShort[cls],
		plan1024: newIMDCT(1024).clone(), plan128: newIMDCT(128).clone(),
		rnd: 0x1f2e3d4c, buf: make([]float64, 2048), tmp: make([]float64, 2048)}
	d.overlap = make([][]float64, n)
	d.prevShape = make([]int, n)
	for i := range d.overlap {
		d.overlap[i] = make([]float64, 1024)
	}
	// aacChanOrder[cfg][elementChannelIndex] = output index
	d.order = aacChanOrder[c.chCfg]
	if d.sbr && !d.down {
		d.outN = 2048
	}
	return d, nil
}

func (d *aacDecoder) readICSInfo(r *aacBits, ics *aacICS) error {
	r.bit() // reserved
	ics.seq = r.bits(2)
	ics.shape = r.bits(1)
	if ics.seq == aacEightShort {
		ics.maxSfb = r.bits(4)
		g := r.bits(7)
		ics.numWindows = 8
		ics.numGroups = 1
		ics.groupLen = [8]int{1}
		for i := 0; i < 7; i++ {
			if g>>uint(6-i)&1 != 0 {
				ics.groupLen[ics.numGroups-1]++
			} else {
				ics.groupLen[ics.numGroups] = 1
				ics.numGroups++
			}
		}
		ics.swb = d.swbShort
	} else {
		ics.maxSfb = r.bits(6)
		ics.numWindows = 1
		ics.numGroups = 1
		ics.groupLen = [8]int{1}
		ics.swb = d.swbLong
		if r.bit() { // predictor_data_present
			return errAACUnsupported
		}
	}
	ics.numSwb = len(ics.swb) - 1
	if ics.maxSfb > ics.numSwb {
		return fmt.Errorf("media: aac max_sfb")
	}
	return nil
}

func (d *aacDecoder) readSections(r *aacBits, c *aacChan) error {
	ics := &c.ics
	bitsLen, esc := 5, 31
	if ics.seq == aacEightShort {
		bitsLen, esc = 3, 7
	}
	for g := 0; g < ics.numGroups; g++ {
		for k := 0; k < ics.maxSfb; {
			cb := r.bits(4)
			if cb == 12 {
				return fmt.Errorf("media: aac reserved codebook")
			}
			l := 0
			for {
				inc := r.bits(bitsLen)
				l += inc
				if inc != esc || r.err {
					break
				}
			}
			if r.err || l == 0 || k+l > ics.maxSfb {
				return fmt.Errorf("media: aac bad section")
			}
			for i := 0; i < l; i++ {
				c.cb[g][k+i] = uint8(cb)
			}
			k += l
		}
	}
	return nil
}

func (d *aacDecoder) readScaleFactors(r *aacBits, c *aacChan) {
	ics := &c.ics
	sf := c.gain
	isPos := 0
	noise := c.gain - 90
	firstNoise := true
	for g := 0; g < ics.numGroups; g++ {
		for b := 0; b < ics.maxSfb; b++ {
			switch cb := c.cb[g][b]; {
			case cb == 0:
				c.sf[g][b] = 0
			case cb >= 14:
				isPos += r.huff(aacSFHuff) - 60
				c.sf[g][b] = isPos
			case cb == 13:
				if firstNoise {
					noise += r.bits(9) - 256
					firstNoise = false
				} else {
					noise += r.huff(aacSFHuff) - 60
				}
				c.sf[g][b] = noise
			default:
				sf += r.huff(aacSFHuff) - 60
				c.sf[g][b] = sf
			}
		}
	}
}

func (d *aacDecoder) readTNS(r *aacBits, c *aacChan) {
	short := c.ics.seq == aacEightShort
	nb, lb, ob := 2, 6, 5
	if short {
		nb, lb, ob = 1, 4, 3
	}
	for w := 0; w < c.ics.numWindows; w++ {
		t := &c.tns
		t.nFilt[w] = r.bits(nb)
		if t.nFilt[w] == 0 {
			continue
		}
		res := r.bits(1)
		for f := 0; f < t.nFilt[w]; f++ {
			t.length[w][f] = r.bits(lb)
			t.order[w][f] = r.bits(ob)
			if t.order[w][f] > 20 {
				r.err = true
				return
			}
			if t.order[w][f] == 0 {
				continue
			}
			t.dir[w][f] = r.bit()
			compress := r.bits(1)
			resBits := 3 + res
			coefLen := resBits - compress
			for i := 0; i < t.order[w][f]; i++ {
				v := r.bits(coefLen)
				if v >= 1<<uint(coefLen-1) {
					v -= 1 << uint(coefLen)
				}
				var iq float64
				if v >= 0 {
					iq = (float64(int(1)<<uint(resBits-1)) - 0.5) / (math.Pi / 2)
				} else {
					iq = (float64(int(1)<<uint(resBits-1)) + 0.5) / (math.Pi / 2)
				}
				t.coef[w][f][i] = math.Sin(float64(v) / iq)
			}
		}
	}
}

func (d *aacDecoder) readPulse(r *aacBits, c *aacChan) error {
	c.pulseN = r.bits(2) + 1
	c.pulseSfb = r.bits(6)
	if c.pulseSfb > c.ics.numSwb {
		return fmt.Errorf("media: aac pulse sfb")
	}
	for i := 0; i < c.pulseN; i++ {
		c.pulseOff[i] = r.bits(5)
		c.pulseAmp[i] = r.bits(4)
	}
	return nil
}

var (
	aacPow43     [8192]float64
	aacPow43Once sync.Once
)

// initPow43 fills the table on first use rather than at process start.
func initPow43() {
	for i := range aacPow43 {
		aacPow43[i] = math.Pow(float64(i), 4.0/3.0)
	}
}

func pow43(v int) float64 {
	aacPow43Once.Do(initPow43)
	if v < len(aacPow43) {
		return aacPow43[v]
	}
	return math.Pow(float64(v), 4.0/3.0)
}

func (d *aacDecoder) readEscape(r *aacBits) int {
	n := 4
	for r.bit() {
		n++
		if n > 24 || r.err {
			r.err = true
			return 0
		}
	}
	return 1<<uint(n) + r.bits(n)
}

// readSpectrum decodes the spectral data of every band and dequantizes it.
func (d *aacDecoder) readSpectrum(r *aacBits, c *aacChan) {
	ics := &c.ics
	short := ics.seq == aacEightShort
	for i := range c.q {
		c.q[i], c.coef[i] = 0, 0
	}
	// pulse data modifies the quantized values: remember to apply it after
	// the band that holds it is decoded (all bands are decoded first)
	type bandRef struct{ g, b, win int }
	win := 0
	var tmp [4]int
	for g := 0; g < ics.numGroups; g++ {
		for b := 0; b < ics.maxSfb; b++ {
			cb := int(c.cb[g][b])
			if cb == 0 || cb >= 14 {
				continue
			}
			start, end := ics.swb[b], ics.swb[b+1]
			for w := 0; w < ics.groupLen[g]; w++ {
				off := 0
				if short {
					off = (win + w) * 128
				}
				if cb == 13 {
					// perceptual noise substitution
					d.stats.pns++
					var e float64
					for k := start; k < end; k++ {
						d.rnd = d.rnd*1664525 + 1013904223
						v := float64(d.rnd)
						c.coef[off+k] = v
						e += v * v
					}
					if e > 0 {
						s := math.Exp2(float64(c.sf[g][b])/4) / math.Sqrt(e)
						for k := start; k < end; k++ {
							c.coef[off+k] *= s
						}
					}
					continue
				}
				h := aacSpecHuff[cb]
				switch {
				case cb <= 4:
					for k := start; k < end; k += 4 {
						idx := r.huff(h)
						if cb <= 2 {
							tmp[0], tmp[1], tmp[2], tmp[3] = idx/27-1, idx/9%3-1, idx/3%3-1, idx%3-1
						} else {
							tmp[0], tmp[1], tmp[2], tmp[3] = idx/27, idx/9%3, idx/3%3, idx%3
							for i := 0; i < 4; i++ {
								if tmp[i] != 0 && r.bit() {
									tmp[i] = -tmp[i]
								}
							}
						}
						for i := 0; i < 4; i++ {
							c.q[off+k+i] = int32(tmp[i])
						}
					}
				default:
					mod := [12]int{5: 9, 6: 9, 7: 8, 8: 8, 9: 13, 10: 13, 11: 17}[cb]
					for k := start; k < end; k += 2 {
						idx := r.huff(h)
						a, bb := idx/mod, idx%mod
						if cb <= 6 {
							a, bb = a-4, bb-4
						} else {
							neg0, neg1 := false, false
							if a != 0 {
								neg0 = r.bit()
							}
							if bb != 0 {
								neg1 = r.bit()
							}
							if cb == 11 {
								if a == 16 {
									a = d.readEscape(r)
								}
								if bb == 16 {
									bb = d.readEscape(r)
								}
							}
							if neg0 {
								a = -a
							}
							if neg1 {
								bb = -bb
							}
						}
						c.q[off+k], c.q[off+k+1] = int32(a), int32(bb)
					}
				}
				if r.err {
					return
				}
			}
		}
		win += ics.groupLen[g]
	}
	// pulses (long windows only)
	if c.pulseN > 0 && !short {
		pos := ics.swb[c.pulseSfb]
		for i := 0; i < c.pulseN; i++ {
			pos += c.pulseOff[i]
			if pos >= 1024 {
				break
			}
			if c.q[pos] > 0 {
				c.q[pos] += int32(c.pulseAmp[i])
			} else {
				c.q[pos] -= int32(c.pulseAmp[i])
			}
		}
	}
	// dequantize
	win = 0
	for g := 0; g < ics.numGroups; g++ {
		for b := 0; b < ics.maxSfb; b++ {
			cb := int(c.cb[g][b])
			if cb == 0 || cb >= 13 {
				continue
			}
			gain := math.Exp2(float64(c.sf[g][b]-100) / 4)
			for w := 0; w < ics.groupLen[g]; w++ {
				off := 0
				if short {
					off = (win + w) * 128
				}
				for k := ics.swb[b]; k < ics.swb[b+1]; k++ {
					v := int(c.q[off+k])
					if v > 0 {
						c.coef[off+k] = pow43(v) * gain
					} else if v < 0 {
						c.coef[off+k] = -pow43(-v) * gain
					}
				}
			}
		}
		win += ics.groupLen[g]
	}
}

// ---- stereo ----------------------------------------------------------------

func (d *aacDecoder) applyStereo(l, rt *aacChan, msUsed *[8][64]bool, msMode int) {
	ics := &l.ics
	short := ics.seq == aacEightShort
	win := 0
	for g := 0; g < ics.numGroups; g++ {
		for b := 0; b < ics.maxSfb; b++ {
			cbR := rt.cb[g][b]
			for w := 0; w < ics.groupLen[g]; w++ {
				off := 0
				if short {
					off = (win + w) * 128
				}
				s, e := ics.swb[b], ics.swb[b+1]
				switch {
				case cbR >= 14:
					d.stats.is++
					sign := float64(-1 + 2*(int(cbR)-14))
					if msMode != 0 && msUsed[g][b] {
						sign = -sign
					}
					sc := sign * math.Exp2(-0.25*float64(rt.sf[g][b]))
					for k := s; k < e; k++ {
						rt.coef[off+k] = l.coef[off+k] * sc
					}
				case msMode != 0 && msUsed[g][b] && l.cb[g][b] < 13 && cbR < 13:
					d.stats.ms++
					for k := s; k < e; k++ {
						m, sd := l.coef[off+k], rt.coef[off+k]
						l.coef[off+k], rt.coef[off+k] = m+sd, m-sd
					}
				}
			}
		}
		win += ics.groupLen[g]
	}
}

// ---- TNS --------------------------------------------------------------------

func (d *aacDecoder) applyTNS(c *aacChan) {
	ics := &c.ics
	short := ics.seq == aacEightShort
	maxBands := aacTnsMaxBandsLong[d.sfi]
	if short {
		maxBands = aacTnsMaxBandsShort[d.sfi]
	}
	mmm := ics.maxSfb
	if maxBands < mmm {
		mmm = maxBands
	}
	var lpc, tmp [21]float64
	for w := 0; w < ics.numWindows; w++ {
		off := 0
		if short {
			off = w * 128
		}
		bottom := ics.numSwb
		for f := 0; f < c.tns.nFilt[w]; f++ {
			top := bottom
			bottom = top - c.tns.length[w][f]
			if bottom < 0 {
				bottom = 0
			}
			order := c.tns.order[w][f]
			if order == 0 {
				continue
			}
			lpc[0] = 1
			for m := 1; m <= order; m++ {
				km := c.tns.coef[w][f][m-1]
				for i := 1; i < m; i++ {
					tmp[i] = lpc[i] + km*lpc[m-i]
				}
				for i := 1; i < m; i++ {
					lpc[i] = tmp[i]
				}
				lpc[m] = km
			}
			tb, tt := bottom, top
			if tb > mmm {
				tb = mmm
			}
			if tt > mmm {
				tt = mmm
			}
			start, end := ics.swb[tb], ics.swb[tt]
			if end <= start {
				continue
			}
			x := c.coef[:]
			if short {
				x = c.coef[off : off+128]
			}
			if c.tns.dir[w][f] {
				for n := end - 1; n >= start; n-- {
					acc := x[n]
					for i := 1; i <= order && n+i < end; i++ {
						acc -= lpc[i] * x[n+i]
					}
					x[n] = acc
				}
			} else {
				for n := start; n < end; n++ {
					acc := x[n]
					for i := 1; i <= order && n-i >= start; i++ {
						acc -= lpc[i] * x[n-i]
					}
					x[n] = acc
				}
			}
		}
	}
}

// ---- synthesis ----------------------------------------------------------------

const aacIMDCTScale = 1.0 / 1024 / 32768

func (d *aacDecoder) synth(ch int, c *aacChan, out []float32) {
	ics := &c.ics
	prev := d.prevShape[ch]
	cur := ics.shape
	buf := d.buf
	for i := range buf {
		buf[i] = 0
	}
	if ics.seq != aacEightShort {
		d.plan1024.run(c.coef[:], d.tmp)
		lr, cr := aacWin[prev][0], aacWin[cur][0]
		sr := aacWin[cur][1]
		sl := aacWin[prev][1]
		for n := 0; n < 1024; n++ {
			var w float64
			if ics.seq == aacLongStop {
				switch {
				case n < 448:
					w = 0
				case n < 576:
					w = sl[n-448]
				default:
					w = 1
				}
			} else {
				w = lr[n]
			}
			buf[n] = d.tmp[n] * w
		}
		for n := 1024; n < 2048; n++ {
			var w float64
			if ics.seq == aacLongStart {
				switch {
				case n < 1472:
					w = 1
				case n < 1600:
					w = sr[1599-n]
				default:
					w = 0
				}
			} else {
				w = cr[2047-n]
			}
			buf[n] = d.tmp[n] * w
		}
	} else {
		for j := 0; j < 8; j++ {
			d.plan128.run(c.coef[j*128:(j+1)*128], d.tmp[:256])
			lw := aacWin[cur][1]
			if j == 0 {
				lw = aacWin[prev][1]
			}
			rw := aacWin[cur][1]
			base := 448 + 128*j
			for n := 0; n < 128; n++ {
				buf[base+n] += 8 * d.tmp[n] * lw[n]
			}
			for n := 128; n < 256; n++ {
				buf[base+n] += 8 * d.tmp[n] * rw[255-n]
			}
		}
	}
	ov := d.overlap[ch]
	for n := 0; n < 1024; n++ {
		out[n] = float32((buf[n] + ov[n]) * aacIMDCTScale)
		ov[n] = buf[1024+n]
	}
	d.prevShape[ch] = cur
}

// ---- access unit --------------------------------------------------------------

var errAACEnd = errors.New("end")

// decodeFrameSafe decodes a frame, turning a panic caused by a damaged
// bitstream into an error.
func (d *aacDecoder) decodeFrameSafe(data []byte) (err error) {
	defer func() {
		if r := recover(); r != nil {
			d.stats.faults++
			err = fmt.Errorf("media: aac decoder fault: %v", r)
		}
	}()
	return d.decodeFrame(data)
}

// aacElem records an element of the frame being decoded (for SBR).
type aacElem struct {
	first, n int
	lfe      bool
}

// decodeFrame decodes one raw_data_block and appends the output samples of
// every channel to d.pcm (1024 per core frame, 2048 with SBR).
func (d *aacDecoder) decodeFrame(data []byte) error {
	r := &aacBits{b: data}
	var frame [8][1024]float32
	var elems []aacElem
	d.elemEnds = d.elemEnds[:0]
	d.elemKind = d.elemKind[:0]
	nextCh := 0
	last := -1 // index of the SCE/CPE a following fill element belongs to
	for {
		id := r.bits(3)
		if r.err {
			return fmt.Errorf("media: aac truncated frame")
		}
		switch id {
		case 7:
			d.endBit = r.pos - 3
			return d.finishFrame(&frame, elems)
		case 0, 3: // SCE, LFE
			r.bits(4)
			if nextCh >= d.nch {
				return fmt.Errorf("media: aac too many channels")
			}
			c := &aacChan{}
			if err := d.readICS(r, c, false); err != nil {
				return err
			}
			d.finishChannel(nextCh, c, frame[nextCh][:])
			elems = append(elems, aacElem{nextCh, 1, id == 3})
			d.elemEnds = append(d.elemEnds, r.pos)
			d.elemKind = append(d.elemKind, id/3*2)
			if id == 0 {
				last = len(elems) - 1
			} else {
				last = -1
			}
			nextCh++
		case 1: // CPE
			r.bits(4)
			if nextCh+2 > d.nch {
				return fmt.Errorf("media: aac too many channels")
			}
			common := r.bit()
			l, rt := &aacChan{}, &aacChan{}
			var msUsed [8][64]bool
			msMode := 0
			if common {
				if err := d.readICSInfo(r, &l.ics); err != nil {
					return err
				}
				rt.ics = l.ics
				msMode = r.bits(2)
				switch msMode {
				case 1:
					for g := 0; g < l.ics.numGroups; g++ {
						for b := 0; b < l.ics.maxSfb; b++ {
							msUsed[g][b] = r.bit()
						}
					}
				case 2:
					for g := 0; g < l.ics.numGroups; g++ {
						for b := 0; b < l.ics.maxSfb; b++ {
							msUsed[g][b] = true
						}
					}
				case 3:
					return fmt.Errorf("media: aac bad ms_mask_present")
				}
			}
			if err := d.readICS(r, l, common); err != nil {
				return err
			}
			if err := d.readICS(r, rt, common); err != nil {
				return err
			}
			d.applyStereo(l, rt, &msUsed, msMode)
			d.finishChannel(nextCh, l, frame[nextCh][:])
			d.finishChannel(nextCh+1, rt, frame[nextCh+1][:])
			elems = append(elems, aacElem{nextCh, 2, false})
			d.elemEnds = append(d.elemEnds, r.pos)
			d.elemKind = append(d.elemKind, 1)
			last = len(elems) - 1
			nextCh += 2
		case 4: // DSE
			r.bits(4)
			alignF := r.bit()
			cnt := r.bits(8)
			if cnt == 255 {
				cnt += r.bits(8)
			}
			if alignF {
				r.align()
			}
			r.skip(8 * cnt)
		case 5: // PCE
			r.bits(4 + 2 + 4)
			nf, ns, nb, nl, na, nc := r.bits(4), r.bits(4), r.bits(4), r.bits(2), r.bits(3), r.bits(4)
			if r.bit() {
				r.bits(4)
			}
			if r.bit() {
				r.bits(4)
			}
			if r.bit() {
				r.bits(3)
			}
			r.skip(5 * (nf + ns + nb))
			r.skip(4 * nl)
			r.skip(4 * na)
			r.skip(5 * nc)
			r.align()
			r.skip(8 * r.bits(8))
		case 6: // FIL
			cnt := r.bits(4)
			if cnt == 15 {
				cnt += r.bits(8) - 1
			}
			if cnt > 0 {
				ext := r.bits(4)
				if (ext == 13 || ext == 14) && last >= 0 && (d.sbr || d.probe) {
					start := r.pos
					d.parseSBR(r, last, elems[last], 8*cnt-4, ext == 14)
					r.pos = start + 8*cnt - 4
					if r.pos > len(data)*8 {
						r.err = true
					}
				} else {
					r.skip(8*cnt - 4)
				}
			}
		default: // CCE
			return errAACUnsupported
		}
		if r.err {
			return fmt.Errorf("media: aac truncated frame")
		}
	}
}

// sbrFor returns the SBR decoder of element ordinal ei.
func (d *aacDecoder) sbrFor(ei int, e aacElem) *sbrElem {
	for len(d.sbrEl) <= ei {
		d.sbrEl = append(d.sbrEl, nil)
	}
	if d.sbrEl[ei] == nil {
		el := newSBRElem(e.n == 2, d.rate, d.down)
		el.psOK = (d.ps || d.probe) && e.n == 1 && d.chCfg == 1
		d.sbrEl[ei] = el
	}
	return d.sbrEl[ei]
}

func (d *aacDecoder) parseSBR(r *aacBits, ei int, e aacElem, bits int, crc bool) {
	el := d.sbrFor(ei, e)
	d.sawSBR = true
	sub := &aacBits{b: r.b, pos: r.pos}
	// the payload must not be read past its fill element
	if sub.pos+bits > len(sub.b)*8 {
		return
	}
	el.parse(sub, bits, crc)
	if el.psSeen {
		d.sawPS = true
	}
}

// finishFrame emits the PCM of a decoded frame, running SBR/PS where enabled.
func (d *aacDecoder) finishFrame(frame *[8][1024]float32, elems []aacElem) error {
	if d.probe {
		return nil
	}
	nOut := d.nch
	if d.ps {
		nOut++
	}
	if !d.sbr {
		for c := 0; c < d.nch; c++ {
			d.pcm[d.outCh(c)] = append(d.pcm[d.outCh(c)], frame[c][:]...)
		}
		return nil
	}
	var covered [8]bool
	var out [2][]float32
	if d.sbrBuf[0] == nil {
		d.sbrBuf = [2][]float32{make([]float32, 2048), make([]float32, 2048)}
	}
	out = d.sbrBuf
	for ei, e := range elems {
		el := d.sbrFor(ei, e)
		in := make([][]float32, e.n)
		for k := range in {
			in[k] = frame[e.first+k][:]
		}
		nout := el.apply(in, out[:])
		for k := 0; k < nout; k++ {
			ch := d.outCh(e.first + k)
			if e.first+k >= d.nch { // PS second channel
				ch = e.first + k
			}
			d.pcm[ch] = append(d.pcm[ch], out[k][:d.outN]...)
			if e.first+k < 8 {
				covered[e.first+k] = true
			}
		}
	}
	for c := 0; c < nOut && c < 8; c++ {
		if !covered[c] {
			ch := d.outCh(c)
			if c >= d.nch {
				ch = c
			}
			d.pcm[ch] = append(d.pcm[ch], make([]float32, d.outN)...)
		}
	}
	return nil
}

func (d *aacDecoder) outCh(i int) int {
	if i < len(d.order) {
		return d.order[i]
	}
	return i
}

func (d *aacDecoder) readICS(r *aacBits, c *aacChan, common bool) error {
	c.gain = r.bits(8)
	if !common {
		if err := d.readICSInfo(r, &c.ics); err != nil {
			return err
		}
	}
	if err := d.readSections(r, c); err != nil {
		return err
	}
	d.readScaleFactors(r, c)
	if r.bit() {
		if c.ics.seq == aacEightShort {
			return fmt.Errorf("media: aac pulse in short window")
		}
		if err := d.readPulse(r, c); err != nil {
			return err
		}
	}
	if r.bit() {
		c.tnsOn = true
		d.readTNS(r, c)
	}
	if r.bit() { // gain control (SSR)
		return errAACUnsupported
	}
	d.readSpectrum(r, c)
	if r.err {
		return fmt.Errorf("media: aac truncated spectrum")
	}
	return nil
}

func (d *aacDecoder) finishChannel(ch int, c *aacChan, out []float32) {
	if c.pulseN > 0 {
		d.stats.pulse++
	}
	switch c.ics.seq {
	case aacEightShort:
		d.stats.short++
	case aacLongStart:
		d.stats.start++
	case aacLongStop:
		d.stats.stop++
	}
	if c.tnsOn {
		d.stats.tns++
		d.applyTNS(c)
	}
	d.synth(ch, c, out)
}

// decodeAACStream decodes every access unit of the stream to planar PCM.
func decodeAACStream(data []byte, as *audioStream) (*PCM, error) {
	cfg, err := parseASC(as.cfg)
	if err != nil {
		return nil, err
	}
	// implicit (backward compatible) signalling: the SBR / PS payload is only
	// visible in-band, so look at the first access units
	if !(cfg.sbr && (cfg.ps || cfg.chCfg != 1)) {
		pc := cfg
		pc.probe = true
		if pd, err := newAACDecoder(pc); err == nil {
			for i, p := range as.packets {
				if i >= 8 || (pd.sawSBR && (pd.sawPS || cfg.chCfg != 1)) {
					break
				}
				pd.decodeFrameSafe(p)
			}
			if pd.sawSBR && !cfg.sbr {
				cfg.sbr = true
				cfg.extRate = 2 * cfg.rate
			}
			if pd.sawPS && cfg.chCfg == 1 {
				cfg.ps = true
			}
		}
	}
	d, err := newAACDecoder(cfg)
	if err != nil {
		return nil, err
	}
	nOut := d.nch
	if d.ps {
		nOut++
	}
	outRate := cfg.rate
	if d.sbr && !d.down {
		outRate = 2 * cfg.rate
	}
	d.pcm = make([][]float32, nOut)
	for i := range d.pcm {
		d.pcm[i] = make([]float32, 0, len(as.packets)*d.outN)
	}
	for _, p := range as.packets {
		if err := d.decodeFrameSafe(p); err != nil {
			if errors.Is(err, ErrUnsupported) {
				return nil, err
			}
			if len(d.pcm[0]) == 0 {
				return nil, err
			}
			// a damaged frame becomes silence
			for c := range d.pcm {
				d.pcm[c] = append(d.pcm[c], make([]float32, d.outN)...)
			}
		}
	}
	out := &PCM{SampleRate: outRate, Data: d.pcm}
	lastAACStats = d.stats
	skip := 0
	if as.skip > 0 {
		skip = as.skip
	}
	total := -1 // like ffmpeg, only the start of the edit list is honoured
	if skip > 0 || total >= 0 {
		n := out.Frames()
		if skip > n {
			skip = n
		}
		end := n
		if total >= 0 && skip+total < n {
			end = skip + total
		}
		for c := range out.Data {
			out.Data[c] = out.Data[c][skip:end]
		}
	}
	return out, nil
}

// lastAACStats records the feature usage of the latest decode (tests only).
var lastAACStats aacStats
