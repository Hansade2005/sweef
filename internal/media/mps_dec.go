package media

import (
	"fmt"
	"math"
	"sync"
)

// MPEG Surround 2-1-2 decoder of the USAC stereoConfigIndex 1 and 2 elements
// (mono downmix and eSBR QMF output in, two QMF domain channels out). The
// processing chain follows libxaac's MPS212 decoder (ixheaacd_mps_*.c,
// Apache-2.0, Ittiam Systems, see NOTICE): hybrid analysis of the lowest QMF
// bands, lattice all-pass decorrelator, CLD/ICC prediction upmix matrices,
// hybrid synthesis and a 64 band FFT based QMF synthesis. Not supported:
// residual bands, phase coding (IPD/OPD), the transient steering decorrelator
// and temporal shaping. libxaac only runs MPS212 together with SBR.

type mpsCplx struct{ re, im float32 }

type mpsMat [2][2]float32

const (
	mpsHybOrder   = 13
	mpsQMFBands   = 64
	mpsDecorAlpha = float32(0.8)
	mpsDecorGamma = float32(1.5)
)

// mpsForcePS selects the stereoConfigIndex 1 (OTT) upmix matrices for
// stereoConfigIndex 2 streams; tests use it to cross check against libxaac.
var mpsForcePS bool

// mpsTestPseudoLR forces the pseudo L/R rotation of the core channels in tests.
var mpsTestPseudoLR bool

// mpsTestCfgHook lets tests override the parsed element configuration.
var mpsTestCfgHook func(c *usacMpsCfg)

var (
	mpsOneMinusAlpha = 1 - mpsDecorAlpha
	mpsAbsThr        = float32(1e-9) * 32768 * 32768
)

// mps212 is the decoder state of one MPS212 element.
type mps212 struct {
	cfg       *usacMpsCfg
	stereoCfg int
	timeSlots int
	// bands
	paramBands int
	hybToBand  []uint8
	inputGain  float32
	// frame parameters
	parseNext        bool
	numSets          int
	numSetsPrev      int
	paramSlots       [mpsMaxSets + 1]int
	paramSlotDiff    [mpsMaxSets + 1]int
	invParamSlotDiff [mpsMaxSets + 1]float32
	extFrame         bool
	tsdEnable        bool
	frame            mpsFrame
	// smoothing
	smoothingTime    [mpsMaxSets + 1]int
	invSmoothingTime [mpsMaxSets + 1]float32
	smoothingData    [mpsMaxSets + 1][mpsMaxBands]int
	prevSmgTime      int
	invPrevSmgTime   float32
	prevSmgData      [mpsMaxBands]int
	frameCount       int
	highRate         bool
	preMixReq        int
	// upmix matrices
	m1       [mpsMaxSets + 1][mpsMaxBands]mpsMat
	m2Dec    [mpsMaxSets + 1][mpsMaxBands]mpsMat
	m2Res    [mpsMaxSets + 1][mpsMaxBands]mpsMat
	m1Prev   [mpsMaxBands]mpsMat
	m2DecPrv [mpsMaxBands]mpsMat
	m2ResPrv [mpsMaxBands]mpsMat
	// per slot interpolated matrices
	rM1  [mpsMaxSlots][mpsMaxBands]float32
	rRes [mpsMaxSlots][mpsMaxBands][2]float32
	rDec [mpsMaxSlots][mpsMaxBands][2]float32

	// hybrid analysis history (3 lowest QMF bands, 12 samples)
	lfHist [3][mpsHybOrder - 1]mpsCplx
	// decorrelator
	decor mpsDecor
	// QMF synthesis state per output channel
	synState [2][18 * mpsQMFBands]float32

	// work buffers
	qmfIn   [mpsQMFBands][mpsMaxSlots]mpsCplx
	hybIn   [mpsMaxSlots][mpsMaxHybrid]mpsCplx
	v       [2][mpsMaxSlots][mpsMaxHybrid]mpsCplx
	wDir    [mpsMaxSlots][mpsMaxHybrid]mpsCplx
	wDiff   [mpsMaxSlots][mpsMaxHybrid]mpsCplx
	dirOut  [2][mpsMaxSlots][mpsMaxHybrid]mpsCplx
	diffOut [2][mpsMaxSlots][mpsMaxHybrid]mpsCplx
	qmfOut  [2][mpsMaxSlots][mpsQMFBands]mpsCplx
}

// mpsDecor is the decorrelator of ixheaacd_mps_decorr.c.
type mpsDecor struct {
	numBins   int
	delay     [mpsMaxHybrid]int
	num       [mpsMaxHybrid][]float32
	den       [mpsMaxHybrid][]float32
	state     [mpsMaxHybrid][12]mpsCplx
	buf       [mpsMaxHybrid][mpsMaxSlots + 12]mpsCplx
	smoothIn  [mpsMaxBands]float32
	smoothOut [mpsMaxBands]float32
}

func newMPS212(cfg *usacMpsCfg, stereoCfg, timeSlots int) (*mps212, error) {
	if mpsTestCfgHook != nil {
		c := *cfg
		mpsTestCfgHook(&c)
		cfg = &c
	}
	if stereoCfg != 1 && stereoCfg != 2 {
		return nil, fmt.Errorf("%w: usac stereoConfigIndex %d", ErrUnsupported, stereoCfg)
	}
	if cfg.residualBands > 0 {
		return nil, fmt.Errorf("%w: usac mps212 residual coding", ErrUnsupported)
	}
	if cfg.phaseCoding {
		return nil, fmt.Errorf("%w: usac mps212 phase coding", ErrUnsupported)
	}
	if timeSlots != 32 && timeSlots != 64 {
		return nil, fmt.Errorf("%w: usac mps212 with %d time slots", ErrUnsupported, timeSlots)
	}
	if cfg.freqRes == 0 {
		return nil, fmt.Errorf("media: invalid mps212 frequency resolution")
	}
	if cfg.decorrConfig > 2 {
		return nil, fmt.Errorf("media: invalid mps212 decorrelator configuration")
	}
	m := &mps212{cfg: cfg, stereoCfg: stereoCfg, timeSlots: timeSlots, parseNext: true}
	m.setFreqRes(cfg.freqRes)
	m.inputGain = mpsClipGain[cfg.fixedGain&7]
	m.preMixReq = 1
	m.numSets = 1
	m.numSetsPrev = 1
	m.decor.init(cfg.decorrConfig)
	return m, nil
}

// setFreqRes selects the parameter band resolution (bsFreqRes).
func (m *mps212) setFreqRes(res int) {
	m.paramBands = mpsFreqResTable[res]
	switch m.paramBands {
	case 4:
		m.hybToBand = mpsHybBand4[:]
	case 5:
		m.hybToBand = mpsHybBand5[:]
	case 7:
		m.hybToBand = mpsHybBand7[:]
	case 10:
		m.hybToBand = mpsHybBand10[:]
	case 14:
		m.hybToBand = mpsHybBand14[:]
	case 20:
		m.hybToBand = mpsHybBand20[:]
	default:
		m.hybToBand = mpsHybBand28[:]
	}
}

func (d *mpsDecor) init(config int) {
	split := mpsSplitFreq0[:]
	switch config {
	case 1:
		split = mpsSplitFreq1[:]
	case 2:
		split = mpsSplitFreq2[:]
	}
	d.numBins = mpsMaxHybrid
	for i := 0; i < mpsMaxHybrid; i++ {
		rb := 0
		for rb < 3 && int(mpsHybToQMF[i]) >= int(split[rb])-1 {
			rb++
		}
		d.delay[i] = int(mpsDecorDelay[rb])
		switch rb {
		case 0:
			d.num[i], d.den[i] = mpsDecorNum0[:], mpsDecorDen0[:]
		case 1:
			d.num[i], d.den[i] = mpsDecorNum1[:], mpsDecorDen1[:]
		case 2:
			d.num[i], d.den[i] = mpsDecorNum2[:], mpsDecorDen2[:]
		default:
			d.num[i], d.den[i] = mpsDecorNum3[:], mpsDecorDen3[:]
		}
	}
}

// apply runs the decorrelator over length slots of in (all bins < numBins).
func (d *mpsDecor) apply(in, out *[mpsMaxSlots][mpsMaxHybrid]mpsCplx, length int, hybToBand28 []uint8) {
	var scratch [mpsMaxSlots]mpsCplx
	for idx := 0; idx < d.numBins; idx++ {
		dl := d.delay[idx]
		buf := &d.buf[idx]
		for s := 0; s < length; s++ {
			buf[dl+s] = in[s][idx]
		}
		num, den := d.num[idx], d.den[idx]
		st := &d.state[idx]
		nl := len(num)
		for i := 0; i < length; i++ {
			x := buf[i]
			o := mpsCplx{st[0].re + x.re*num[0], st[0].im + x.im*num[0]}
			for j := 1; j < nl; j++ {
				st[j-1].re = st[j].re + num[j]*x.re - den[j]*o.re
				st[j-1].im = st[j].im + num[j]*x.im - den[j]*o.im
			}
			scratch[i] = o
		}
		for s := 0; s < length; s++ {
			out[s][idx] = scratch[s]
		}
		for s := 0; s < dl; s++ {
			buf[s] = buf[length+s]
		}
	}
	d.energyAdjust(in, out, length, hybToBand28)
}

func (d *mpsDecor) energyAdjust(in, out *[mpsMaxSlots][mpsMaxHybrid]mpsCplx, length int, hb []uint8) {
	for i := 0; i < length; i++ {
		var inE, outE, gain [mpsMaxBands]float32
		for j := 0; j < d.numBins; j++ {
			k := hb[j]
			a, b := in[i][j], out[i][j]
			inE[k] += a.re*a.re + a.im*a.im
			outE[k] += b.re*b.re + b.im*b.im
		}
		for k := 0; k < mpsMaxBands; k++ {
			d.smoothIn[k] = d.smoothIn[k]*mpsDecorAlpha + inE[k]*mpsOneMinusAlpha
			d.smoothOut[k] = d.smoothOut[k]*mpsDecorAlpha + outE[k]*mpsOneMinusAlpha
			gain[k] = 1
			if d.smoothOut[k] > d.smoothIn[k]*mpsDecorGamma {
				gain[k] = float32(math.Sqrt(float64(d.smoothIn[k] * mpsDecorGamma / (d.smoothOut[k] + mpsAbsThr))))
			}
			if d.smoothIn[k] > d.smoothOut[k]*mpsDecorGamma {
				g := float32(math.Sqrt(float64(d.smoothIn[k] / (mpsDecorGamma*d.smoothOut[k] + mpsAbsThr))))
				if g > 2 {
					g = 2
				}
				gain[k] = g
			}
		}
		for j := 0; j < d.numBins; j++ {
			g := gain[hb[j]]
			out[i][j].re *= g
			out[i][j].im *= g
		}
	}
}

// ---- hybrid filter bank --------------------------------------------------------

// hybType1 filters 13 tap windows of one QMF band into 8 sub-bands.
func mpsHybType1(x []mpsCplx, ns int, out *[8][mpsMaxSlots]mpsCplx) {
	for i := 0; i < ns; i++ {
		var x0r, x0i [13]float32
		for n := 0; n < 13; n++ {
			c := mpsHybFilt8[12-n]
			x0r[n] = c * x[n+i].re
			x0i[n] = c * x[n+i].im
		}
		var r1, i1 [8]float32
		r1[0], i1[0] = x0r[6], x0i[6]
		r1[1], i1[1] = x0r[7], x0i[7]
		for k := 2; k <= 6; k++ {
			r1[k] = x0r[k+6] - x0r[k-2]
			i1[k] = x0i[k+6] - x0i[k-2]
		}
		r1[7], i1[7] = -x0r[5], -x0i[5]
		var ar, ai [8]float32
		ar[0] = r1[0]
		ar[1] = r1[1] - r1[7]
		ar[2] = r1[2] - r1[6]
		ar[3] = r1[3] - r1[5]
		ar[4] = i1[1] + i1[7]
		ar[5] = i1[2] + i1[6]
		ar[6] = i1[3] + i1[5]
		ar[7] = i1[4]
		ai[0] = i1[0]
		ai[1] = i1[1] - i1[7]
		ai[2] = i1[2] - i1[6]
		ai[3] = i1[3] - i1[5]
		ai[4] = r1[1] + r1[7]
		ai[5] = r1[2] + r1[6]
		ai[6] = r1[3] + r1[5]
		ai[7] = r1[4]
		for q := 0; q < 4; q++ {
			cs := mpsHybCosine[q*8 : q*8+8]
			sn := mpsHybSine[q*8 : q*8+8]
			var reL, imL float32
			reL += ar[0]
			reL += ar[1] * cs[1]
			reL += ar[2] * cs[2]
			reL += ar[3] * cs[3]
			reH := reL
			reL -= ar[4] * sn[1]
			reL -= ar[5] * sn[2]
			reL -= ar[6] * sn[3]
			reL -= ar[7] * sn[4]
			reH = reH - (reL - reH)
			imL += ai[0]
			imL += ai[1] * cs[1]
			imL += ai[2] * cs[2]
			imL += ai[3] * cs[3]
			imH := imL
			imL += ai[4] * sn[1]
			imL += ai[5] * sn[2]
			imL += ai[6] * sn[3]
			imL += ai[7] * sn[4]
			imH = imH - (imL - imH)
			out[q][i] = mpsCplx{reL, imL}
			out[7-q][i] = mpsCplx{reH, imH}
		}
	}
}

// hybType2 splits a QMF band into 2 sub-bands.
func mpsHybType2(x []mpsCplx, ns int, out *[8][mpsMaxSlots]mpsCplx) {
	for i := 0; i < ns; i++ {
		var tr, ti [13]float32
		for n := 1; n < 6; n += 2 {
			re := x[n+i].re + x[12-n+i].re
			im := x[n+i].im + x[12-n+i].im
			c := mpsHybFilt2[12-n]
			tr[n] = c * re
			ti[n] = c * im
		}
		c6 := mpsHybFilt2[6]
		tr[6] = c6 * x[6+i].re
		ti[6] = c6 * x[6+i].im
		tr[1] = tr[1] + tr[3] + tr[5]
		ti[1] = ti[1] + ti[3] + ti[5]
		out[0][i] = mpsCplx{tr[6] + tr[1], ti[6] + ti[1]}
		out[1][i] = mpsCplx{tr[6] - tr[1], ti[6] - ti[1]}
	}
}

// hybridAnalysis converts the QMF input (bands x slots) of one frame into hybrid
// bands (slot x hybrid band). The three lowest bands are split with a 13 tap
// filter (delay of 6 slots, compensated by the SBR regrouping), the others
// pass unchanged.
func (m *mps212) hybridAnalysis(bandCount int, out *[mpsMaxSlots][mpsMaxHybrid]mpsCplx) {
	ns := m.timeSlots
	var scratch [8][mpsMaxSlots]mpsCplx
	var x [mpsHybOrder - 1 + mpsMaxSlots]mpsCplx
	for k := 0; k < 3; k++ {
		copy(x[:12], m.lfHist[k][:])
		copy(x[12:], m.qmfIn[k][:ns])
		copy(m.lfHist[k][:], x[ns:ns+12])
		switch k {
		case 0:
			mpsHybType1(x[:], ns, &scratch)
			for j := 0; j < 2; j++ {
				for n := 0; n < ns; n++ {
					out[n][j] = scratch[j+6][n]
					out[n][j+2] = scratch[j][n]
					a, b := scratch[j+2][n], scratch[5-j][n]
					out[n][j+4] = mpsCplx{a.re + b.re, a.im + b.im}
				}
			}
		case 1:
			mpsHybType2(x[:], ns, &scratch)
			for j := 0; j < 2; j++ {
				for n := 0; n < ns; n++ {
					out[n][j+6] = scratch[1-j][n]
				}
			}
		case 2:
			mpsHybType2(x[:], ns, &scratch)
			for j := 0; j < 2; j++ {
				for n := 0; n < ns; n++ {
					out[n][j+8] = scratch[j][n]
				}
			}
		}
	}
	for k := 0; k < bandCount-3; k++ {
		for n := 0; n < ns; n++ {
			out[n][k+10] = m.qmfIn[k+3][n]
		}
	}
}

// hybridSynthesis sums the hybrid sub-bands of one channel back to QMF bands.
func (m *mps212) hybridSynthesis(src *[mpsMaxSlots][mpsMaxHybrid]mpsCplx, dst *[mpsMaxSlots][mpsQMFBands]mpsCplx) {
	for n := 0; n < m.timeSlots; n++ {
		h := &src[n]
		s := h[0]
		for k := 1; k < 6; k++ {
			s.re += h[k].re
			s.im += h[k].im
		}
		dst[n][0] = s
		dst[n][1] = mpsCplx{h[6].re + h[7].re, h[6].im + h[7].im}
		dst[n][2] = mpsCplx{h[8].re + h[9].re, h[8].im + h[9].im}
		copy(dst[n][3:], h[10:10+mpsQMFBands-3])
	}
}

// ---- upmix matrices --------------------------------------------------------------

func (m *mps212) matrices() {
	f := &m.frame
	// previous matrices: last set of the previous frame
	for pb := 0; pb < m.paramBands; pb++ {
		last := m.numSetsPrev - 1
		m.m1Prev[pb] = m.m1[last][pb]
		m.m2DecPrv[pb] = m.m2Dec[last][pb]
		m.m2ResPrv[pb] = m.m2Res[last][pb]
	}
	for ps := 0; ps < m.numSets; ps++ {
		for pb := 0; pb < m.paramBands; pb++ {
			cld := f.cldIdx[ps][pb] + 15
			icc := f.iccIdx[ps][pb] & 7
			var h [6]float32
			if m.stereoCfg == 2 && !mpsForcePS {
				// residual coding configuration: prediction upmix
				w := mpsWeight[icc*31+cld]
				rw := mpsReWeight[icc*31+cld]
				b := mpsBeta[icc*31+cld]
				h[0] = w - rw
				h[1] = w + rw
				h[2] = b
				h[3] = -b
			} else {
				cl := mpsCL[cld]
				cr := mpsCL[30-cld]
				h[0] = mpsCos[icc*31+cld] * cl
				h[1] = mpsCos[icc*31+30-cld] * cr
				h[2] = mpsSin[icc*31+cld] * cl
				h[3] = -mpsSin[icc*31+30-cld] * cr
			}
			m.m1[ps][pb] = mpsMat{{1, 0}, {1, 0}}
			m.m2Res[ps][pb] = mpsMat{{h[0], h[4]}, {h[1], h[5]}}
			m.m2Dec[ps][pb] = mpsMat{{0, h[2]}, {0, h[3]}}
		}
	}
}

// smoothMatrices applies the temporal smoothing of the matrices (smoothing
// modes of the high rate syntax).
func (m *mps212) smoothMatrices() {
	for ps := 0; ps < m.numSets; ps++ {
		delta := float32(m.paramSlotDiff[ps]) * m.invSmoothingTime[ps]
		omd := 1 - delta
		for pb := 0; pb < m.paramBands; pb++ {
			if m.smoothingData[ps][pb] == 0 {
				continue
			}
			var p1, pd, pr *mpsMat
			if ps == 0 {
				p1, pd, pr = &m.m1Prev[pb], &m.m2DecPrv[pb], &m.m2ResPrv[pb]
			} else {
				p1, pd, pr = &m.m1[ps-1][pb], &m.m2Dec[ps-1][pb], &m.m2Res[ps-1][pb]
			}
			for r := 0; r < 2; r++ {
				for c := 0; c < 2; c++ {
					m.m1[ps][pb][r][c] = delta*m.m1[ps][pb][r][c] + omd*p1[r][c]
					m.m2Dec[ps][pb][r][c] = delta*m.m2Dec[ps][pb][r][c] + omd*pd[r][c]
					m.m2Res[ps][pb][r][c] = delta*m.m2Res[ps][pb][r][c] + omd*pr[r][c]
				}
			}
			m.preMixReq++
		}
	}
}

// interp linearly interpolates one matrix element over the time slots from
// the previous frame's last set through the sets of this frame.
func (m *mps212) interp(cur *[mpsMaxSets + 1][mpsMaxBands]mpsMat, prev *[mpsMaxBands]mpsMat, pb, row, col int, allSets bool, dst func(ts int, v float32)) {
	ts := 0
	ks := m.invParamSlotDiff[0]
	ms := cur[0][pb][row][col]
	ls := prev[pb][row][col]
	step := ks * (ms - ls)
	base := ls + step
	for i := 1; i <= m.paramSlotDiff[0]; i++ {
		dst(ts, base)
		base += step
		ts++
	}
	if allSets {
		for ps := 1; ps < m.numSets; ps++ {
			ks = m.invParamSlotDiff[ps]
			ms = cur[ps][pb][row][col]
			ls = cur[ps-1][pb][row][col]
			step = ks * (ms - ls)
			base = ls + step
			for i := 1; i <= m.paramSlotDiff[ps]; i++ {
				dst(ts, base)
				base += step
				ts++
			}
		}
	}
}

func (m *mps212) interpolate(pre bool) {
	for pb := 0; pb < m.paramBands; pb++ {
		for row := 0; row < 2; row++ {
			row := row
			pb := pb
			m.interp(&m.m2Dec, &m.m2DecPrv, pb, row, 1, true, func(ts int, v float32) { m.rDec[ts][pb][row] = v })
			m.interp(&m.m2Res, &m.m2ResPrv, pb, row, 0, true, func(ts int, v float32) { m.rRes[ts][pb][row] = v })
		}
		if pre {
			pb := pb
			m.interp(&m.m1, &m.m1Prev, pb, 0, 0, m.highRate, func(ts int, v float32) { m.rM1[ts][pb] = v })
		}
	}
}

// ---- QMF synthesis ---------------------------------------------------------------

var (
	mpsFFTOnce sync.Once
	mpsFFTTw   [32][2]float32
)

func mpsFFT32(x []float32) {
	// x holds 32 interleaved complex values; forward DFT, no scaling.
	mpsFFTOnce.Do(func() {
		for k := 0; k < 32; k++ {
			a := -2 * math.Pi * float64(k) / 32
			mpsFFTTw[k] = [2]float32{float32(math.Cos(a)), float32(math.Sin(a))}
		}
	})
	var y [64]float64
	for k := 0; k < 32; k++ {
		var sr, si float64
		for n := 0; n < 32; n++ {
			t := mpsFFTTw[(n*k)&31]
			xr, xi := float64(x[2*n]), float64(x[2*n+1])
			sr += xr*float64(t[0]) - xi*float64(t[1])
			si += xr*float64(t[1]) + xi*float64(t[0])
		}
		y[2*k], y[2*k+1] = sr, si
	}
	for i := range y {
		x[i] = float32(y[i])
	}
}

// synthPreTwiddle is ixheaacd_mps_synth_pre_twidle.
func mpsSynthPre(outRe, outIm, c []float32) {
	const ln = 64
	cs, ce := 0, 2*ln-1
	prs, pis := 0, 0
	pie, pre := ln-1, ln-1
	t := 0
	tw := mpsPreTwid[:]
	for i := 0; i < ln; i += 4 {
		outRe[prs] = c[cs] * tw[t]
		cs++
		prs++
		outIm[pis] = -(c[cs] * tw[t])
		cs--
		pis++
		outIm[pis] = c[ce] * tw[t]
		ce--
		pis--
		outRe[prs] = c[ce] * tw[t]
		ce++
		t++
		prs--
		outIm[pis] += c[ce] * tw[t]
		ce--
		pis++
		outRe[prs] += c[ce] * tw[t]
		ce--
		prs++
		outRe[prs] -= c[cs] * tw[t]
		cs++
		prs++
		outIm[pis] += c[cs] * tw[t]
		cs++
		t++
		pis++
		outIm[pie] = c[ce] * tw[t]
		ce--
		pie--
		outRe[pre] = -(c[ce] * tw[t])
		ce++
		pre--
		outRe[pre] = c[cs] * tw[t]
		cs++
		pre++
		outIm[pie] = c[cs] * tw[t]
		cs--
		t++
		pie++
		outRe[pre] += c[cs] * tw[t]
		cs++
		pre--
		outIm[pie] += c[cs] * tw[t]
		cs++
		pie--
		outIm[pie] -= c[ce] * tw[t]
		ce--
		pie--
		outRe[pre] += c[ce] * tw[t]
		ce--
		t++
		pre--
	}
}

// mpsSynthPost is ixheaacd_mps_synth_post_twidle.
func mpsSynthPost(state, outRe, outIm []float32) {
	const ln = 64
	potw := 0
	prs, pis := 0, 0
	pre, pie := ln-2, ln-2
	x0 := outRe[pre]
	x1 := outRe[pre+1]
	x2 := outIm[pie]
	x3 := outIm[pie+1]
	outRe[pre+1] = -outRe[prs+1]
	outIm[pie+1] = -outIm[pis]
	outIm[pis] = outIm[pis+1]
	for i := 5; i < ln; i += 4 {
		twdr := mpsPostTwid[potw]
		twdi := mpsPostTwid[potw+1]
		potw += 2
		outRe[pre] = x0 * twdi
		outRe[pre] += x1 * twdr
		pre--
		prs++
		outRe[prs] = x0 * twdr
		outRe[prs] -= x1 * twdi
		prs++
		x1 = outRe[pre]
		pre--
		x0 = outRe[pre]
		pre++
		outRe[pre] = outRe[prs] * twdi
		prs++
		outRe[pre] += -(outRe[prs] * twdr)
		pre--
		tmp := outRe[prs] * twdi
		prs--
		outRe[prs] = tmp + outRe[prs]*twdr

		outIm[pie] = -(x2 * twdr)
		outIm[pie] += x3 * twdi
		pie--
		pis++
		outIm[pis] = -(x2 * twdi)
		outIm[pis] -= x3 * twdr
		pis++
		x3 = outIm[pie]
		pie--
		x2 = outIm[pie]
		pie++
		outIm[pie] = -(outIm[pis] * twdr)
		pis++
		outIm[pie] -= outIm[pis] * twdi
		pie--
		tmp = outIm[pis] * twdr
		pis--
		outIm[pis] = tmp - outIm[pis]*twdi
	}
	outRe[pre] = 0.7071067 * (x1 + x0)
	outIm[pie] = 0.7071067 * (x3 - x2)
	outRe[prs+1] = -0.7071067 * (x1 - x0)
	outIm[pis+1] = -0.7071067 * (x3 + x2)
	for i := 0; i < ln; i++ {
		state[i] = outIm[i] - outRe[i]
		state[ln+i] = outIm[ln-i-1] + outRe[ln-i-1]
		state[ln-i-1] = outIm[ln-i-1] - outRe[ln-i-1]
		state[2*ln-i-1] = outIm[i] + outRe[i]
	}
}

// synthesis runs the QMF synthesis of channel ch: timeSlots*64 samples.
func (m *mps212) synthesis(ch int, out []float32) {
	const res = mpsQMFBands
	st := &m.synState[ch]
	var cur [20 * res]float32
	var finRe, finIm [64]float32
	var outv [10 * res]float32
	for ts := 0; ts < m.timeSlots; ts++ {
		// shift the 18*res old values behind the 2*res new ones
		copy(cur[2*res:], st[:])
		var in [2 * res]float32
		for k := 0; k < res; k++ {
			in[2*k] = m.qmfOut[ch][ts][k].re
			in[2*k+1] = m.qmfOut[ch][ts][k].im
		}
		for i := range finRe {
			finRe[i], finIm[i] = 0, 0
		}
		mpsSynthPre(finRe[:], finIm[:], in[:])
		mpsFFT32(finRe[:])
		mpsFFT32(finIm[:])
		mpsSynthPost(cur[:2*res], finRe[:], finIm[:])
		// windowing
		fc := 0
		o1, o2 := 0, res
		s1, s2 := 0, 3*res
		for k := 0; k < 5; k++ {
			for l := 0; l < res; l++ {
				outv[o1] = cur[s1] * mpsPolyphase[fc]
				fc++
				outv[o2] = cur[s2] * mpsPolyphase[fc]
				fc++
				o1++
				o2++
				s1++
				s2++
			}
			o1 += res
			o2 += res
			s1 += 3 * res
			s2 += 3 * res
		}
		for k := 0; k < res; k++ {
			acc := outv[k]
			for l := 1; l < 10; l++ {
				acc += outv[res*l+k]
			}
			out[ts*res+k] = acc
		}
		copy(st[:], cur[:18*res])
	}
}

// ---- frame processing ------------------------------------------------------------

// apply upmixes one frame. in holds the regrouped eSBR QMF output (timeSlots
// slots of 64 bands, 16 bit units), bandCount the number of bands carrying
// signal. The two output channels (timeSlots*64 samples each, 16 bit units)
// are returned in out.
func (m *mps212) apply(inRe, inIm [][64]float32, bandCount int, out *[2][]float32) error {
	if bandCount < 4 || bandCount > mpsQMFBands {
		return fmt.Errorf("media: invalid mps212 band count %d", bandCount)
	}
	if err := m.frameDecode(); err != nil {
		return err
	}
	m.matrices()
	m.smoothMatrices()
	ns := m.timeSlots
	hybCount := bandCount - 3 + 10
	m.decor.numBins = hybCount
	for ts := 0; ts < ns; ts++ {
		for qs := 0; qs < bandCount; qs++ {
			m.qmfIn[qs][ts] = mpsCplx{m.inputGain * inRe[ts][qs], m.inputGain * inIm[ts][qs]}
		}
	}
	for ch := 0; ch < 2; ch++ {
		for ts := 0; ts < ns; ts++ {
			for qs := hybCount; qs < mpsMaxHybrid; qs++ {
				m.dirOut[ch][ts][qs] = mpsCplx{}
			}
		}
	}
	pre := m.preMixReq != 0
	m.hybridAnalysis(bandCount, &m.hybIn)
	m.interpolate(pre)
	hb := m.hybToBand
	if !pre {
		m.decor.apply(&m.hybIn, &m.wDiff, ns, mpsHybBand28[:])
		for ts := 0; ts < ns; ts++ {
			for qs := 0; qs < hybCount; qs++ {
				x := m.hybIn[ts][qs]
				p := hb[qs]
				for row := 0; row < 2; row++ {
					a := m.rRes[ts][p][row]
					m.dirOut[row][ts][qs] = mpsCplx{x.re * a, x.im * a}
					d := m.wDiff[ts][qs]
					b := m.rDec[ts][p][row]
					m.diffOut[row][ts][qs] = mpsCplx{d.re * b, d.im * b}
				}
			}
		}
	} else {
		for ts := 0; ts < ns; ts++ {
			for qs := 0; qs < hybCount; qs++ {
				a := m.rM1[ts][hb[qs]]
				x := m.hybIn[ts][qs]
				c := mpsCplx{x.re * a, x.im * a}
				m.v[0][ts][qs] = c
				m.v[1][ts][qs] = c
			}
		}
		m.decor.apply(&m.v[1], &m.wDiff, ns, mpsHybBand28[:])
		for ts := 0; ts < ns; ts++ {
			for qs := 0; qs < hybCount; qs++ {
				x := m.v[0][ts][qs]
				p := hb[qs]
				for row := 0; row < 2; row++ {
					a := m.rRes[ts][p][row]
					m.dirOut[row][ts][qs] = mpsCplx{x.re * a, x.im * a}
					d := m.wDiff[ts][qs]
					b := m.rDec[ts][p][row]
					m.diffOut[row][ts][qs] = mpsCplx{d.re * b, d.im * b}
				}
			}
		}
	}
	// temporal processing off: add the decorrelated part
	for ch := 0; ch < 2; ch++ {
		for ts := 0; ts < ns; ts++ {
			for qs := 0; qs < hybCount; qs++ {
				m.dirOut[ch][ts][qs].re += m.diffOut[ch][ts][qs].re
				m.dirOut[ch][ts][qs].im += m.diffOut[ch][ts][qs].im
			}
		}
		m.hybridSynthesis(&m.dirOut[ch], &m.qmfOut[ch])
		m.synthesis(ch, out[ch])
	}
	m.parseNext = true
	m.preMixReq = 0
	return nil
}
