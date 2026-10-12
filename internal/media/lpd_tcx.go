package media

import (
	"math"
	"sync"
)

// TCX decoding (ISO/IEC 23003-3 7.15): LPC based spectral noise shaping,
// noise filling, low-overlap windowed transform and forward aliasing
// cancellation. After libxaac's ixheaacd_tcx_fwd_alcnx.c / ixheaacd_tcx_fwd_mdct.c
// (Apache-2.0, see NOTICE).

var (
	lpdSineMu    sync.Mutex
	lpdSineCache = map[int][]float64{}
)

// lpdSine returns the rising sine slope of n samples.
func lpdSine(n int) []float64 {
	lpdSineMu.Lock()
	defer lpdSineMu.Unlock()
	if w, ok := lpdSineCache[n]; ok {
		return w
	}
	w := make([]float64, n)
	for i := range w {
		w[i] = math.Sin(math.Pi * (float64(i) + 0.5) / float64(2*n))
	}
	lpdSineCache[n] = w
	return w
}

// lpdLowFqDeemph: low frequency de-emphasis of the TCX spectrum.
func lpdLowFqDeemph(x []float64, lg int, gains []float64) {
	k := 8
	imax := lg / 4
	max := 0.01
	for i := 0; i < imax; i += k {
		rm := 0.01
		for j := i; j < i+k; j++ {
			rm += x[j] * x[j]
		}
		if rm > max {
			max = rm
		}
	}
	factor := 0.1
	for i := 0; i < imax; i += k {
		rm := 0.01
		for j := i; j < i+k; j++ {
			rm += x[j] * x[j]
		}
		rm = math.Sqrt(rm / max)
		if rm > factor {
			factor = rm
		}
		for j := i; j < i+k; j++ {
			x[j] *= factor
		}
		gains[i/k] = factor
	}
}

// lpdLPC2TD computes the gains 1/|A(w_k)| of the odd-frequency DFT of lg*2
// points.
func (s *lpdState) lpcToGains(coeff []float64, lg int, gains []float64) {
	n := 2 * lg
	data := make([]complex128, n)
	avg := math.Pi / float64(n)
	for i := 0; i <= lpdOrder; i++ {
		t := float64(i) * avg
		data[i] = complex(coeff[i]*math.Cos(t), -coeff[i]*math.Sin(t))
	}
	p := newFFTPlan(n)
	p.forward(data)
	for i := 0; i < n/2; i++ {
		re, im := real(data[i]), imag(data[i])
		gains[i] = 1 / (math.Sqrt(re*re+im*im) + 1.1920929e-07)
	}
}

func lpdNoiseShaping(r []float64, lg, m int, g1, g2 []float64) {
	k := lg / m
	rr := make([]float64, lg)
	copy(rr, r[:lg])
	prev := 0.0
	var a, b float64
	for i := 0; i < lg; i++ {
		if i%k == 0 {
			a = 2 * g1[i/k] * g2[i/k] / (g1[i/k] + g2[i/k])
			b = (g2[i/k] - g1[i/k]) / (g1[i/k] + g2[i/k])
		}
		rr[i] = a*rr[i] + b*prev
		prev = rr[i]
	}
	// the reference stores the even lines in the first and the reversed odd
	// lines in the second half to feed its FFT; the DCT-IV used here takes
	// the natural order
	copy(r[:lg], rr)
}

// lpdMDCTMain: inverse low-overlap transform. in has l+m lines; out gets
// 2l+m samples (before gain scaling), unnormalised.
func (s *lpdState) mdctMain(in []float64, out []float64, l, m int) {
	o := make([]float64, l+m)
	s.dct4(in[:l+m], o)
	for i := 0; i < m/2; i++ {
		out[l+m/2-1-i] = -o[m/2+l/2+i]
	}
	for i := 0; i < l/2; i++ {
		out[i] = o[m+l/2+i]
		out[l-1-i] = -o[m+l/2+i]
	}
	for i := 0; i < m/2; i++ {
		out[l+m/2+i] = -o[m/2+l/2-1-i]
	}
	for i := 0; i < l/2; i++ {
		out[l+m+i] = -o[l/2-1-i]
		out[2*l+m-1-i] = -o[l/2-1-i]
	}
}

func lpdRandomSign(seed *uint32) float64 {
	*seed = *seed*69069 + 5
	if *seed&0x10000 != 0 {
		return -1
	}
	return 1
}

// lpdTCX decodes the TCX frame k of length lg; lp holds the LPC sets
// (middle, old, new).
func (d *usacDec) lpdTCX(c *usacCh, f *lpdFrame, k int, lp []float64, lg int) {
	st := c.lpd
	lenSub := d.cfg.coreLen / 4
	numSub := d.cfg.coreLen * 4 / lpdSuper
	by2 := (4*numSub)/2 - 1
	synthDelay := by2 * lpdSubfr
	sb := st.synthBuf[:]
	eb := st.excBuf[:]
	sBase := lenSub*k + lpdMaxPitch + synthDelay
	eBase := lenSub*k + lpdMaxPitch + lpdInterOrder + 1
	fl := lenSub / 2
	flp := fl
	if st.modePrev == -2 {
		flp = d.cfg.coreLen / 16
	}
	sineCur := lpdSine(2 * fl)
	sinePrev := lpdSine(2 * flp)

	xnBuf := make([]float64, lg+2*fl+lpdOrder+8)
	// xn = xnBuf[fl:], with history before it
	xnOff := fl + lpdOrder + 1 // keep room for ORDER+1 history samples in front
	_ = xnOff
	if st.modePrev != 0 {
		if st.modePrev > 0 {
			for i := 0; i < 2*flp; i++ {
				st.excPrev[i+fl-flp+1] *= sinePrev[2*flp-1-i]
			}
		}
		for i := 0; i < fl-flp; i++ {
			st.excPrev[i+fl+flp+1] = 0
		}
	}
	noiseLevel := 0.0625 * (8 - float64(f.noiseFactor[k]))
	off := 0
	for i := 0; i < k; i++ {
		off += f.tcxLg[i]
	}
	q := f.tcxQuant[off : off+lg]
	x := make([]float64, lg)
	for i := range x {
		x[i] = float64(q[i])
	}
	for i := lg / 6; i < lg; i += 8 {
		maxk := i + 8
		if maxk > lg {
			maxk = lg
		}
		var tmp float64
		for j := i; j < maxk; j++ {
			tmp += float64(q[j]) * float64(q[j])
		}
		if tmp == 0 {
			for j := i; j < maxk; j++ {
				x[j] = noiseLevel * lpdRandomSign(&c.noiseSeed)
			}
		}
	}
	var alfd [lpdSuper / 32]float64
	lpdLowFqDeemph(x, lg, alfd[:])
	var gain1, gain2 [lpdSuper / 4]float64
	var ap [lpdOrder + 1]float64
	lpdWeightLPC(lp[lpdOrder+1:], ap[:], 0.92)
	st.lpcToGains(ap[:], lenSub/4, gain1[:])
	lpdWeightLPC(lp[2*(lpdOrder+1):], ap[:], 0.92)
	st.lpcToGains(ap[:], lenSub/4, gain2[:])
	energy := 0.01
	for i := 0; i < lg; i++ {
		energy += x[i] * x[i]
	}
	tmp := math.Sqrt(energy) / float64(lg)
	gainTCX := math.Pow(10, float64(f.globalGain[k])/28) / (tmp * 2)
	if lpdDbg && lpdDbgN < 1 {
		println("MYXB", x[40], x[41], x[42], x[43], x[44], x[45], x[46], x[47], x[48], x[49], x[50], x[51])
	}
	lpdNoiseShaping(x, lg, lenSub/4, gain1[:], gain2[:])
	if lpdDbg && lpdDbgN < 1 {
		println("MYXA", x[40], x[41], x[42], x[43], x[44], x[45], x[46], x[47], x[48], x[49], x[50], x[51])
	}
	if lpdDbg && lpdDbgN < 3 {
		println("MYTCX", lpdDbgN, "k", k, "lg", lg, "gain_tcx", gainTCX, "gg", f.globalGain[k], "nf", f.noiseFactor[k], "g1", gain1[0], "g2", gain2[0])
	}
	st.mdctMain(x, xnBuf, 2*fl, lg-2*fl)
	sc := 2 / float64(lg)
	for i := 0; i < lg+2*fl; i++ {
		xnBuf[i] *= sc
	}
	st.facGain = gainTCX * 0.5 * math.Sqrt(float64(fl)/float64(lg))
	for i := 0; i < fl/4; i++ {
		st.facFd[i] = alfd[i*lg/(8*fl)]
	}
	var xn1 [2*lpdFacLength + 16]float64
	if st.modePrev == 0 {
		var facWin [2 * lpdFacLength]float64
		for i := 0; i < flp; i++ {
			facWin[i] = sinePrev[i] * sinePrev[2*flp-1-i]
			facWin[flp+i] = 1 - sinePrev[flp+i]*sinePrev[flp+i]
		}
		var xx [lpdFacLength]float64
		for j := 0; j < fl; j++ {
			xx[j] = st.facGain * float64(f.fac[k*lpdFacLength+j])
		}
		for j := 0; j < fl/4; j++ {
			xx[j] *= st.facFd[j]
		}
		var t [lpdFacLength]float64
		st.dct4(xx[:fl], t[:fl])
		for i := 0; i < fl; i++ {
			xn1[i] = t[i] * 2 / float64(fl)
		}
		for i := fl; i < 2*fl; i++ {
			xn1[i] = 0
		}
		lpdWeightLPC(lp[lpdOrder+1:], ap[:], 0.92)
		var zero [lpdOrder]float64
		lpdSynthFilter(ap[:], xn1[:2*fl], xn1[:2*fl], zero[:])
		for i := 0; i < fl; i++ {
			xn1[i] += st.excPrev[1+fl+i]*facWin[fl+i] + st.excPrev[fl-i]*facWin[fl-1-i]
		}
	}
	for i := 0; i < lg+2*fl; i++ {
		xnBuf[i] *= gainTCX
	}
	for i := 0; i < 2*flp; i++ {
		xnBuf[i+fl-flp] *= sinePrev[i]
	}
	for i := 0; i < fl-flp; i++ {
		xnBuf[i] = 0
	}
	if st.modePrev != 0 {
		for i := fl - flp; i < fl+flp; i++ {
			xnBuf[i] += st.excPrev[1+i]
		}
	} else {
		for i := fl - flp; i < fl+flp; i++ {
			xnBuf[i+fl] += xn1[i]
		}
	}
	copy(st.excPrev[:1+2*fl], xnBuf[lg-1:lg+2*fl])
	for i := 0; i < 2*fl; i++ {
		xnBuf[i+lg] *= sineCur[2*fl-1-i]
	}
	if st.modePrev != 0 {
		copy(sb[sBase-flp:sBase], xnBuf[fl-flp:fl])
		buf := make([]float64, lpdOrder+fl+8)
		for i := 0; i < lpdOrder+fl; i++ {
			buf[i] = sb[sBase+i-lpdOrder-fl] - lpdPreemph*sb[sBase+i-lpdOrder-fl-1]
		}
		ttt := fl % lpdSubfr
		if ttt != 0 {
			lpdResidual(st.lpFltPrev[:], buf, lpdOrder, eb, eBase-fl, ttt, 1)
		}
		loops := (fl - ttt) / lpdSubfr
		lpdResidual(st.lpFltPrev[:], buf, lpdOrder+ttt, eb, eBase+ttt-fl, lpdSubfr, loops)
	}
	if lpdDbg && lpdDbgN < 3 {
		println("MYTCX2", lpdDbgN, xnBuf[fl], xnBuf[fl+1], xnBuf[fl+2], xnBuf[fl+3], xnBuf[fl+4], xnBuf[fl+5], "mid", xnBuf[fl+lg/2], xnBuf[fl+lg/2+1])
		lpdDbgN++
	}
	copy(sb[sBase:sBase+lg], xnBuf[fl:fl+lg])
	// pre-emphasised copy of the output for the excitation
	pre := make([]float64, lpdOrder+1+lg)
	copy(pre[:lpdOrder+1], sb[sBase-lpdOrder-1:sBase])
	copy(pre[lpdOrder+1:], xnBuf[fl:fl+lg])
	mem := pre[0]
	for i := len(pre) - 1; i > 0; i-- {
		pre[i] -= lpdPreemph * pre[i-1]
	}
	pre[0] -= lpdPreemph * mem
	// pre[1+ORDER-ORDER...]: xn starts at index ORDER+1 of pre; the residual
	// reads history down to ORDER samples before it
	lpdResidual(lp[2*(lpdOrder+1):], pre, lpdOrder+1, eb, eBase, lg, 1)
	copy(st.lpFltPrev[:lpdOrder+1], lp[2*(lpdOrder+1):3*(lpdOrder+1)])
	copy(st.lpFltPrev[lpdOrder+1:], lp[2*(lpdOrder+1):3*(lpdOrder+1)])
}

var lpdDbg = false
var lpdDbgN int
