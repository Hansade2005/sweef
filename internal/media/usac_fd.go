package media

import (
	"fmt"
	"math"
	"sync"
)

// USAC frequency-domain (FD) core coder: ics_info, scalefactors, noise
// filling, TNS, M/S and prediction stereo, inverse MDCT and windowing. The
// structure and the behaviour in the details the standard leaves open follow
// FFmpeg's libavcodec/aac/aacdec_usac.c (LGPL-2.1-or-later, see NOTICE).

const (
	usacOnlyLong   = 0
	usacLongStart  = 1
	usacEightShort = 2
	usacLongStop   = 3
	usacStopStart  = 4 // internal: window with a short slope on both sides
)

type usacICS struct {
	seq, seqPrev int // window_sequence of this and of the previous frame
	seqEff       int // seq, with LONG_START following a short/start frame resolved to usacStopStart
	seqPrevEff   int
	kb, kbPrev   int
	maxSfb       int
	grouping     int
	numGroups    int
	numWindows   int
	groupLen     [8]int
	swb          []uint16
	numSwb       int
	tnsMax       int
}

type usacTNS struct {
	present bool
	nFilt   [8]int
	length  [8][3]int
	order   [8][3]int
	dir     [8][3]bool
	coef    [8][3][16]float64
}

// usacCh is one coded channel of an element.
type usacCh struct {
	ics         usacICS
	coreMode    int
	tnsData     bool
	tns         usacTNS
	noiseFill   bool // noise filling enabled for the element
	noiseLevel  int
	noiseOffset int
	noiseSeed   uint32
	sfo         [8 * 64]int
	q           [1024]int32
	coef        [1024]float64
	arith       usacArithState
	ov          [1024]float64 // overlap-add state
	facPresent  bool
}

// usacStereo holds the stereo prediction state of a channel pair.
type usacStereo struct {
	commonWindow bool
	commonTW     bool
	msMaskMode   int
	msMask       [8 * 64]bool
	maxSfbSte    int
	tnsOnLR      bool
	predUsed     [8 * 64]bool
	predDir      int
	complexCoef  bool
	usePrevFrame bool
	alphaRe      [8 * 64]float64
	alphaIm      [8 * 64]float64
	prevAlphaRe  [64]float64 // per sfb, the value of the latest group of the previous frame
	prevAlphaIm  [64]float64
	prevL, prevR [1024]float64 // spectra of the previous frame (last window)
	prevCplx     bool          // the previous frame used complex prediction
}

// ---- windows ------------------------------------------------------------------

var (
	usacWinMu    sync.Mutex
	usacWinCache = map[[2]int][]float64{}
)

// usacSlope returns the rising half of a window of 2*half points: shape 0
// sine, 1 Kaiser-Bessel derived (alpha 4 for the long, 6 for the short slope).
func usacSlope(shape, half int, short bool) []float64 {
	key := [2]int{shape*2 + b2i(short), half}
	usacWinMu.Lock()
	defer usacWinMu.Unlock()
	if w, ok := usacWinCache[key]; ok {
		return w
	}
	w := make([]float64, half)
	if shape == 0 {
		for n := range w {
			w[n] = math.Sin(math.Pi * (float64(n) + 0.5) / float64(2*half))
		}
	} else {
		alpha := 4.0
		if short {
			alpha = 6.0
		}
		k := make([]float64, half+1)
		var tot float64
		for j := 0; j <= half; j++ {
			r := 2*float64(j)/float64(half) - 1
			k[j] = besselI0(math.Pi * alpha * math.Sqrt(1-r*r))
			tot += k[j]
		}
		var acc float64
		for n := 0; n < half; n++ {
			acc += k[n]
			w[n] = math.Sqrt(acc / tot)
		}
	}
	usacWinCache[key] = w
	return w
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ---- swb tables ---------------------------------------------------------------

var usacSwb768Map = [13][]uint16{
	usacSwbOffset768x96[:], usacSwbOffset768x96[:], usacSwbOffset768x64[:], usacSwbOffset768x48[:],
	usacSwbOffset768x48[:], usacSwbOffset768x48[:], usacSwbOffset768x24[:], usacSwbOffset768x24[:],
	usacSwbOffset768x16[:], usacSwbOffset768x16[:], usacSwbOffset768x16[:], usacSwbOffset768x8[:],
	usacSwbOffset768x8[:],
}

var usacSwb96Map = [13][]uint16{
	usacSwbOffset96x96[:], usacSwbOffset96x96[:], usacSwbOffset96x96[:], usacSwbOffset96x48[:],
	usacSwbOffset96x48[:], usacSwbOffset96x48[:], usacSwbOffset96x24[:], usacSwbOffset96x24[:],
	usacSwbOffset96x16[:], usacSwbOffset96x16[:], usacSwbOffset96x16[:], usacSwbOffset96x8[:],
	usacSwbOffset96x8[:],
}

func toU16(s []int) []uint16 {
	o := make([]uint16, len(s))
	for i, v := range s {
		o[i] = uint16(v)
	}
	return o
}

var (
	usacSwbOnce   sync.Once
	usacSwb1024   [13][]uint16
	usacSwb128    [13][]uint16
	usacSwbLongOf = [13]int{96, 96, 64, 48, 48, 32, 24, 24, 16, 16, 16, 8, 8}
)

func usacInitSwb() {
	usacSwbOnce.Do(func() {
		for i := 0; i < 13; i++ {
			cls := usacSwbLongOf[i]
			usacSwb1024[i] = toU16(aacSwbLong[cls])
			usacSwb128[i] = toU16(aacSwbShort[cls])
		}
	})
}

// sfIndexForRate maps a sampling rate to the sampling_frequency_index used
// for the band tables (-1 when not a standard rate).
func usacSfIndex(rate int) int {
	for i, r := range aacRates {
		if r == rate {
			return i
		}
	}
	return -1
}

// ---- ics info -----------------------------------------------------------------

// setup computes the window grouping and band tables of an ICS.
func (d *usacDec) setupICS(ics *usacICS) error {
	sfi := d.sfi
	if ics.seq == usacEightShort {
		if d.cfg.coreLen == 768 {
			ics.swb = usacSwb96Map[sfi]
		} else {
			ics.swb = usacSwb128[sfi]
		}
		ics.tnsMax = int(usacTnsMaxBands128[sfi])
		// scale_factor_grouping: bit 6-j set means window j+1 joins the group of window j
		cnt := 1
		g := 0
		var gl [8]int
		for j := 0; j < 7; j++ {
			if ics.grouping&(1<<uint(6-j)) != 0 {
				cnt++
			} else {
				gl[g] = cnt
				g++
				cnt = 1
			}
		}
		gl[g] = cnt
		g++
		ics.groupLen = gl
		ics.numGroups = g
		ics.numWindows = 8
	} else {
		if d.cfg.coreLen == 768 {
			ics.swb = usacSwb768Map[sfi]
		} else {
			ics.swb = usacSwb1024[sfi]
		}
		ics.tnsMax = int(usacTnsMaxBands1024[sfi])
		ics.numGroups = 1
		ics.groupLen = [8]int{1}
		ics.numWindows = 1
	}
	ics.numSwb = len(ics.swb) - 1
	if ics.maxSfb > ics.numSwb {
		return fmt.Errorf("media: usac max_sfb %d exceeds %d", ics.maxSfb, ics.numSwb)
	}
	return nil
}

// ---- TNS ----------------------------------------------------------------------

func (d *usacDec) readTNS(r *aacBits, t *usacTNS, ics *usacICS) error {
	short := ics.seq == usacEightShort
	nb, lb, ob := 2, 6, 4
	if short {
		nb, lb, ob = 1, 4, 3
	}
	t.present = true
	for w := 0; w < ics.numWindows; w++ {
		t.nFilt[w] = r.bits(nb)
		if t.nFilt[w] == 0 {
			continue
		}
		res := r.bits(1)
		for f := 0; f < t.nFilt[w]; f++ {
			t.length[w][f] = r.bits(lb)
			t.order[w][f] = r.bits(ob)
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
	return nil
}

func (d *usacDec) applyTNS(c *usacCh) {
	ics := &c.ics
	t := &c.tns
	mmm := ics.tnsMax
	if ics.maxSfb < mmm {
		mmm = ics.maxSfb
	}
	if mmm == 0 {
		return
	}
	stride := d.cfg.coreLen
	if ics.seq == usacEightShort {
		stride = d.cfg.coreLen / 8
	}
	var lpc, tmp [17]float64
	for w := 0; w < ics.numWindows; w++ {
		bottom := ics.numSwb
		for f := 0; f < t.nFilt[w]; f++ {
			top := bottom
			bottom = top - t.length[w][f]
			if bottom < 0 {
				bottom = 0
			}
			order := t.order[w][f]
			if order == 0 {
				continue
			}
			lpc[0] = 1
			for m := 1; m <= order; m++ {
				km := t.coef[w][f][m-1]
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
			start, end := int(ics.swb[tb]), int(ics.swb[tt])
			size := end - start
			if size <= 0 {
				continue
			}
			x := c.coef[w*stride : (w+1)*stride]
			if ics.seq != usacEightShort {
				x = c.coef[:stride]
			}
			if t.dir[w][f] {
				for m := 0; m < size; m++ {
					n := end - 1 - m
					acc := x[n]
					for i := 1; i <= order && i <= m; i++ {
						acc -= lpc[i] * x[n+i]
					}
					x[n] = acc
				}
			} else {
				for m := 0; m < size; m++ {
					n := start + m
					acc := x[n]
					for i := 1; i <= order && i <= m; i++ {
						acc -= lpc[i] * x[n-i]
					}
					x[n] = acc
				}
			}
		}
	}
}

// ---- scalefactors, noise filling ----------------------------------------------

func (d *usacDec) readScaleFactors(r *aacBits, c *usacCh, global int) error {
	ics := &c.ics
	off := global
	for g := 0; g < ics.numGroups; g++ {
		for sfb := 0; sfb < ics.maxSfb; sfb++ {
			if g != 0 || sfb != 0 {
				off += r.huff(aacSFHuff) - 60
			}
			if off < 0 || off > 255 {
				return fmt.Errorf("media: usac scalefactor %d out of range", off)
			}
			c.sfo[g*ics.maxSfb+sfb] = off - 100
		}
	}
	return nil
}

func usacNoiseSign(seed *uint32) float64 {
	*seed = *seed*69069 + 5
	if *seed&0x10000 != 0 {
		return -1
	}
	return 1
}

var usacNoiseStart = [2][2]int{{160, 20}, {120, 15}}

// dequantize turns the quantised lines into scaled spectral coefficients,
// applying noise filling.
func (d *usacDec) dequantize(c *usacCh) {
	ics := &c.ics
	short := ics.seq == usacEightShort
	stride := d.cfg.coreLen
	if short {
		stride = d.cfg.coreLen / 8
	}
	for i := range c.coef {
		c.coef[i] = 0
	}
	for i, v := range c.q {
		if v > 0 {
			c.coef[i] = pow43(int(v))
		} else if v < 0 {
			c.coef[i] = -pow43(int(-v))
		}
	}
	// noise filling
	if c.noiseFill {
		noiseVal := 0.0
		if c.noiseLevel != 0 {
			noiseVal = math.Pow(2, (float64(c.noiseLevel)-14)/3)
		}
		noiseOff := c.noiseOffset - 16
		bandOff := usacNoiseStart[b2i(d.cfg.coreLen == 768)][b2i(short)]
		base := 0
		for g := 0; g < ics.numGroups; g++ {
			for sfb := 0; sfb < ics.maxSfb; sfb++ {
				s, e := int(ics.swb[sfb]), int(ics.swb[sfb+1])
				if s < bandOff {
					continue
				}
				zero := true
				for w := 0; w < ics.groupLen[g]; w++ {
					cb := c.coef[base+w*stride+s : base+w*stride+e]
					for z := range cb {
						if cb[z] == 0 {
							cb[z] = usacNoiseSign(&c.noiseSeed) * noiseVal
						} else {
							zero = false
						}
					}
				}
				if zero {
					v := c.sfo[g*ics.maxSfb+sfb] + noiseOff
					if v < -200 {
						v = -200
					}
					c.sfo[g*ics.maxSfb+sfb] = v
				}
			}
			base += ics.groupLen[g] * stride
		}
	}
	// scalefactors
	base := 0
	for g := 0; g < ics.numGroups; g++ {
		for sfb := 0; sfb < ics.maxSfb; sfb++ {
			s, e := int(ics.swb[sfb]), int(ics.swb[sfb+1])
			sf := math.Exp2(float64(c.sfo[g*ics.maxSfb+sfb]) / 4)
			for w := 0; w < ics.groupLen[g]; w++ {
				cb := c.coef[base+w*stride+s : base+w*stride+e]
				for z := range cb {
					cb[z] *= sf
				}
			}
		}
		base += ics.groupLen[g] * stride
	}
}

// ---- inverse transform ---------------------------------------------------------

// synth runs the inverse MDCT of c.coef and the window overlap-add, writing
// coreLen samples (scaled to +-1 full scale) to out.
func (d *usacDec) synth(c *usacCh, out []float64) {
	n := d.cfg.coreLen
	m := n / 8
	a := (n - m) / 2
	ics := &c.ics
	buf := d.buf[:2*n]
	for i := range buf {
		buf[i] = 0
	}
	prevShape, curShape := ics.kbPrev, ics.kb
	sc := 1.0 / float64(n) / 32768
	if ics.seq != usacEightShort {
		d.mdctLong.run(c.coef[:n], d.tmp[:2*n])
		lr := usacSlope(prevShape, n, false)
		cr := usacSlope(curShape, n, false)
		sl := usacSlope(prevShape, m, true)
		sr := usacSlope(curShape, m, true)
		leftLong := (ics.seqPrevEff == usacOnlyLong || ics.seqPrevEff == usacLongStop) &&
			(ics.seqEff == usacOnlyLong || ics.seqEff == usacLongStart)
		for i := 0; i < n; i++ {
			var w float64
			if leftLong {
				w = lr[i]
			} else {
				switch {
				case i < a:
					w = 0
				case i < a+m:
					w = sl[i-a]
				default:
					w = 1
				}
			}
			buf[i] = d.tmp[i] * w
		}
		for i := n; i < 2*n; i++ {
			var w float64
			if ics.seq == usacLongStart {
				switch {
				case i < n+a:
					w = 1
				case i < n+a+m:
					w = sr[n+a+m-1-i]
				default:
					w = 0
				}
			} else {
				w = cr[2*n-1-i]
			}
			buf[i] = d.tmp[i] * w
		}
	} else {
		cur := usacSlope(curShape, m, true)
		prv := usacSlope(prevShape, m, true)
		for j := 0; j < 8; j++ {
			d.mdctShort.run(c.coef[j*m:(j+1)*m], d.tmp[:2*m])
			lw := cur
			if j == 0 {
				lw = prv
			}
			base := a + m*j
			for i := 0; i < m; i++ {
				buf[base+i] += 8 * d.tmp[i] * lw[i]
			}
			for i := m; i < 2*m; i++ {
				buf[base+i] += 8 * d.tmp[i] * cur[2*m-1-i]
			}
		}
	}
	for i := 0; i < n; i++ {
		out[i] = (buf[i] + c.ov[i]) * sc
		c.ov[i] = buf[n+i]
	}
}
