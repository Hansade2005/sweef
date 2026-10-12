package media

import (
	"fmt"
	"math"
)

// Harmonic bandwidth extension (HBE) transposer of the eSBR decoder, after
// libxaac's ixheaacd_hbe_trans.c and ixheaacd_esbr_polyphase.c (Apache-2.0,
// Ittiam Systems, see NOTICE). The transposer re-synthesises a few QMF bands
// to the time domain, analyses them with a finer filterbank and forms
// products of neighbouring (magnitude normalised) bands. The FFTs of the
// original are plain (unnormalised, inverse direction) DFTs here.

const (
	hbeOperWinLen   = 13
	hbeZeroBandIdx  = 6
	hbeBlkLen2      = 10
	hbeBlkLen3      = 8
	hbeBlkLen4      = 6
	hbeMaxStretch   = 4
	hbeQmfChannels  = 64
	hbeTwiceChan    = 128
	hbeMaxSynthSize = 20
)

type esbrHBE struct {
	xOver       [esbrMaxPatches]int
	maxStretch  int
	coreLen     int
	inLen       int
	outLen      int
	noBins      int
	startBand   int
	endBand     int
	usf4        bool
	kStart      int
	synthSize   int
	fftSet      bool
	inputBuf    []float32
	qmfIn       [][hbeTwiceChan]float32
	qmfOut      [][hbeTwiceChan]float32
	synthBuf    [1280]float32
	analyBuf    [640]float32
	synthWin    []float32
	analyWin    []float32
	synthCos    []float32
	analyCosSin []float32
	norm        [128][hbeTwiceChan]float32
}

func newESBRHBE(coreLen, outFrame int, usf4 bool) *esbrHBE {
	t := &esbrHBE{coreLen: coreLen, usf4: usf4}
	t.noBins = outFrame / hbeQmfChannels
	t.inLen = t.noBins
	t.outLen = 2 * t.inLen
	t.inputBuf = make([]float32, coreLen+hbeQmfChannels)
	t.qmfIn = make([][hbeTwiceChan]float32, t.inLen+hbeOperWinLen)
	t.qmfOut = make([][hbeTwiceChan]float32, t.outLen+16)
	return t
}

func esbrMapProtFilter(n int) ([]float32, bool) {
	var off int
	switch n {
	case 4:
		off = 0
	case 8:
		off = 40
	case 12:
		off = 120
	case 16:
		off = 240
	case 20:
		off = 400
	case 24:
		off = 600
	case 32:
		off = 840
	case 40:
		off = 1160
	default:
		return nil, false
	}
	return esbrSubSampWin[off : off+10*n], true
}

// reinit is ixheaacd_qmf_hbe_data_reinit.
func (t *esbrHBE) reinit(h *esbrHeader) error {
	t.startBand = h.fLow[0]
	t.endBand = h.fLow[h.nSf[0]]
	t.synthSize = 4 * ((t.startBand+4)/8 + 1)
	if t.startBand >= len(esbrStartSubband2kL) {
		return fmt.Errorf("%w: usac HBE start band", ErrUnsupported)
	}
	t.kStart = int(esbrStartSubband2kL[t.startBand])
	t.usf4 = h.usf4
	if t.usf4 {
		if t.kStart+t.synthSize > 16 {
			t.kStart = 16 - t.synthSize
		}
	} else if t.coreLen == 768 {
		if t.kStart+t.synthSize > 24 {
			t.kStart = 24 - t.synthSize
		}
	}
	for i := range t.synthBuf {
		t.synthBuf[i] = 0
	}
	n := t.synthSize
	t.fftSet = true
	switch n {
	case 4:
		t.synthCos, t.analyCosSin = esbrSynthCosKL4[:], esbrAnalyCosSinKL8[:]
	case 8:
		t.synthCos, t.analyCosSin = esbrSynthCosKL8[:], esbrAnalyCosSinKL16[:]
	case 12:
		t.synthCos, t.analyCosSin = esbrSynthCosKL12[:], esbrAnalyCosSinKL24[:]
	case 16:
		t.synthCos, t.analyCosSin = esbrSynthCosKL16[:], esbrAnalyCosSinKL32[:]
	case 20:
		t.synthCos, t.analyCosSin = esbrSynthCosKL20[:], esbrAnalyCosSinKL40[:]
		t.fftSet = false // libxaac has no FFT for this size and re-initialises on every call
	default:
		return fmt.Errorf("%w: usac HBE start band %d", ErrUnsupported, t.startBand)
	}
	var ok bool
	if t.synthWin, ok = esbrMapProtFilter(n); !ok {
		return fmt.Errorf("%w: usac HBE window", ErrUnsupported)
	}
	for i := range t.analyBuf {
		t.analyBuf[i] = 0
	}
	if t.analyWin, ok = esbrMapProtFilter(2 * n); !ok {
		return fmt.Errorf("%w: usac HBE window", ErrUnsupported)
	}
	for i := range t.xOver {
		t.xOver[i] = 0
	}
	stopPatch := hbeMaxStretch
	if t.usf4 {
		stopPatch = esbrMaxPatches
		t.maxStretch = hbeMaxStretch
	}
	sfb := 0
	nl := h.nSf[0]
	for patch := 1; patch <= stopPatch; patch++ {
		for sfb <= nl && h.fLow[sfb] <= patch*t.startBand {
			sfb++
		}
		if sfb <= nl {
			if patch*t.startBand-h.fLow[sfb-1] <= 3 {
				t.xOver[patch-1] = h.fLow[sfb-1]
			} else {
				s2 := 0
				for s2 <= h.nSf[1] && h.fHigh[s2] <= patch*t.startBand {
					s2++
				}
				t.xOver[patch-1] = h.fHigh[s2-1]
			}
		} else {
			t.xOver[patch-1] = t.endBand
			t.maxStretch = patch
			if t.maxStretch > hbeMaxStretch {
				t.maxStretch = hbeMaxStretch
			}
			break
		}
	}
	return nil
}

// dftInv computes out[k] = sum_m in[m] exp(+j 2 pi k m / n), unnormalised.
var hbeTwid = map[int][]float64{}

func hbeTwiddle(n int) []float64 {
	if t, ok := hbeTwid[n]; ok {
		return t
	}
	t := make([]float64, 2*n)
	for i := 0; i < n; i++ {
		a := 2 * math.Pi * float64(i) / float64(n)
		t[2*i], t[2*i+1] = math.Cos(a), math.Sin(a)
	}
	hbeTwid[n] = t
	return t
}

// cdftInv: in/out interleaved complex of n points.
func cdftInv(in, out []float32, n int) {
	tw := hbeTwiddle(n)
	for k := 0; k < n; k++ {
		var sr, si float64
		for m := 0; m < n; m++ {
			c, s := tw[2*((k*m)%n)], tw[2*((k*m)%n)+1]
			xr, xi := float64(in[2*m]), float64(in[2*m+1])
			sr += xr*c - xi*s
			si += xr*s + xi*c
		}
		out[2*k], out[2*k+1] = float32(sr), float32(si)
	}
}

// realSynthFilt is ixheaacd_real_synth_filt.
func (t *esbrHBE) realSynthFilt(numCols int, qRe, qIm [][64]float32) error {
	n := t.synthSize
	if t.kStart < 0 {
		return fmt.Errorf("media: usac HBE k_start")
	}
	cosT := esbrCosTableTransQmf[:]
	cosOff := t.kStart * 32
	buffer := t.synthBuf[:]
	var g, w [640]float32
	var synthR [128]float32
	var synthOut [256]float32
	for idx := 0; idx < numCols; idx++ {
		out := t.inputBuf[(idx+1)*n:]
		for k := 0; k < n; k++ {
			ki := t.kStart + k
			synthR[k] = cosT[cosOff+2*k]*qRe[idx][ki] + cosT[cosOff+2*k+1]*qIm[idx][ki]
			synthR[k+n] = 0
		}
		for l := 20*n - 1; l >= 2*n; l-- {
			buffer[l] = buffer[l-2*n]
		}
		if n == 20 {
			pc := 0
			for l := 0; l < n+1; l++ {
				var acc float32
				for k := 0; k < n; k++ {
					acc += synthR[k] * t.synthCos[pc+k]
				}
				buffer[l] = acc
				buffer[n-l] = acc
				pc += n
			}
			for l := n + 1; l < 2*n-n/2; l++ {
				var acc float32
				for k := 0; k < n; k++ {
					acc += synthR[k] * t.synthCos[pc+k]
				}
				buffer[l] = acc
				buffer[3*n-l] = -acc
				pc += n
			}
			var acc float32
			for k := 0; k < n; k++ {
				acc += synthR[k] * t.synthCos[pc+k]
			}
			buffer[3*n>>1] = acc
		} else {
			var cin [256]float32
			for i := 0; i < 2*n; i++ {
				cin[2*i] = synthR[i]
				cin[2*i+1] = 0
			}
			cdftInv(cin[:4*n], synthOut[:4*n], 2*n)
			kmax := n >> 1
			sb := kmax
			kmax += n
			pu, pc := 0, 0
			for k := 0; k < kmax; k++ {
				tmp := synthOut[pu] * t.synthCos[pc]
				tmp -= synthOut[pu+1] * t.synthCos[pc+1]
				pu += 2
				pc += 2
				buffer[sb] = tmp
				sb++
			}
			sb = 0
			kmax -= n
			for k := 0; k < kmax; k++ {
				tmp := synthOut[pu] * t.synthCos[pc]
				tmp -= synthOut[pu+1] * t.synthCos[pc+1]
				pu += 2
				pc += 2
				buffer[sb] = tmp
				sb++
			}
		}
		for i := 0; i < 5; i++ {
			copy(g[(2*i+0)*n:(2*i+1)*n], buffer[(4*i+0)*n:(4*i+1)*n])
			copy(g[(2*i+1)*n:(2*i+2)*n], buffer[(4*i+3)*n:(4*i+4)*n])
		}
		for k := 0; k < 10*n; k++ {
			w[k] = g[k] * t.synthWin[k]
		}
		for i := 0; i < n; i++ {
			var acc float32
			for j := 0; j < 10; j++ {
				acc += w[n*j+i]
			}
			out[i] = acc
		}
	}
	return nil
}

// complexAnalFilt is ixheaacd_complex_anal_filt (QMF HBE, esbr_hq == 0).
func (t *esbrHBE) complexAnalFilt() error {
	anal := 2 * t.synthSize
	N := 10 * anal
	noBins := t.noBins >> 1
	var wo [640]float32
	var u [128]float32
	var uIn, uOut [256]float32
	for idx := 0; idx < noBins; idx++ {
		row := &t.qmfIn[idx+hbeOperWinLen-1]
		for i := range row {
			row[i] = 0
		}
		inp := t.inputBuf[idx*2*t.synthSize+1:]
		ab := 4 * t.kStart
		x := t.analyBuf[:]
		for i := N - 1; i >= anal; i-- {
			x[i] = x[i-anal]
		}
		for i := anal - 1; i >= 0; i-- {
			x[i] = inp[anal-1-i]
		}
		for i := 0; i < N; i++ {
			wo[i] = x[i] * t.analyWin[i]
		}
		for i := 0; i < 2*anal; i++ {
			var acc float32
			for j := 0; j < 5; j++ {
				acc += wo[i+j*2*anal]
			}
			u[i] = acc
		}
		if anal == 40 || anal == 56 {
			for i := 1; i < anal; i++ {
				t1 := u[i] + u[2*anal-i]
				t2 := u[i] - u[2*anal-i]
				u[i] = t1
				u[2*anal-i] = t2
			}
			tab := 0
			for k := 0; k < anal; k++ {
				accR := u[anal]
				var accI float32
				if k&1 != 0 {
					accI = u[0]
				} else {
					accI = -u[0]
				}
				for l := 1; l < anal; l++ {
					accR += u[l] * t.analyCosSin[tab+2*l]
					accI += u[2*anal-l] * t.analyCosSin[tab+2*l+1]
				}
				tab += 2 * anal
				row[ab] = accR
				row[ab+1] = accI
				ab += 2
			}
		} else {
			for k := 0; k < anal*2; k++ {
				uIn[2*k] = t.analyCosSin[2*k] * u[k]
				uIn[2*k+1] = t.analyCosSin[2*k+1] * u[k]
			}
			cdftInv(uIn[:4*anal], uOut[:4*anal], anal*2)
			p := 0
			for k := 0; k < anal/2; k++ {
				row[ab+1] = -uOut[p]
				row[ab] = uOut[p+1]
				p += 2
				ab += 2
				row[ab+1] = uOut[p]
				row[ab] = -uOut[p+1]
				p += 2
				ab += 2
			}
		}
	}
	return nil
}

// apply is ixheaacd_qmf_hbe_apply. qRe/qIm start at the first row of the
// current frame (after the codec delay), pvRe/pvIm at the first output row.
func (t *esbrHBE) apply(h *esbrHeader, numCols int, qRe, qIm, pvRe, pvIm [][64]float32, pitch int) error {
	vocCols := t.noBins / 2
	copy(t.inputBuf[:t.synthSize], t.inputBuf[t.noBins*t.synthSize:t.noBins*t.synthSize+t.synthSize])
	if !t.fftSet {
		if err := t.reinit(h); err != nil {
			return err
		}
	}
	if err := t.realSynthFilt(numCols, qRe, qIm); err != nil {
		return err
	}
	for i := 0; i < hbeOperWinLen-1; i++ {
		t.qmfIn[i] = t.qmfIn[i+vocCols]
	}
	if err := t.complexAnalFilt(); err != nil {
		return err
	}
	i := 0
	for ; i < t.outLen-t.noBins; i++ {
		t.qmfOut[i] = t.qmfOut[i+t.noBins]
	}
	for ; i < t.outLen; i++ {
		t.qmfOut[i] = [hbeTwiceChan]float32{}
	}
	if err := t.postAnalProcess(pitch, t.usf4); err != nil {
		return err
	}
	for i := 0; i < t.noBins; i++ {
		for b := t.startBand; b < t.endBand; b++ {
			or, oi := t.qmfOut[i][2*b], t.qmfOut[i][2*b+1]
			pvRe[i][b] = or*esbrPhaseVocoderCos[b] - oi*esbrPhaseVocoderSin[b]
			pvIm[i][b] = or*esbrPhaseVocoderSin[b] + oi*esbrPhaseVocoderCos[b]
		}
	}
	return nil
}

func cbrtInv(base float64) float32 {
	a := float32(base)
	return float32(math.Cbrt(float64(float32(1.0) / a)))
}

func f32min(a, b float32) float32 {
	if a < b {
		return a
	}
	return b
}

// norm4 is ixheaacd_norm_qmf_in_buf_4 (fourth root normalisation).
func (t *esbrHBE) norm4(band int) {
	for ; band <= t.xOver[3]; band++ {
		for i := 0; i < t.inLen; i++ {
			xr, xi := t.qmfIn[i][2*band], t.qmfIn[i][2*band+1]
			base := 1e-17
			base += float64(xr * xr)
			base += float64(xi * xi)
			temp := float32(math.Sqrt(math.Sqrt(base)))
			fac := temp * float32(math.Sqrt(float64(temp)))
			fac = 1 / fac
			t.norm[i][2*band] = xr * fac
			t.norm[i][2*band+1] = xi * fac
		}
	}
}

// norm2 is ixheaacd_norm_qmf_in_buf_2.
func (t *esbrHBE) norm2(band int) {
	for ; band <= t.xOver[1]; band++ {
		for i := 0; i < t.inLen; i++ {
			xr, xi := t.qmfIn[i][2*band], t.qmfIn[i][2*band+1]
			base := 1e-17
			base += float64(xr * xr)
			base += float64(xi * xi)
			fac := float32(1.0 / base)
			fac = float32(math.Sqrt(math.Sqrt(float64(fac))))
			t.norm[i][2*band] = xr * fac
			t.norm[i][2*band+1] = xi * fac
		}
	}
}

func cmulf(ar, ai, br, bi float32) (float32, float32) {
	return ar*br - ai*bi, ar*bi + ai*br
}

// xprodProc3 is ixheaacd_hbe_xprod_proc_3.
func (t *esbrHBE) xprodProc3(band, col int, p float32, pitchIdx int) {
	inp := 2 * band / 3
	in := &t.qmfIn[col+hbeZeroBandIdx]
	magZero := in[2*inp]*in[2*inp] + in[2*inp+1]*in[2*inp+1]
	var maxMag float32
	maxTr, maxN1, maxN2 := 0, 0, 0
	for tr := 1; tr < 3; tr++ {
		tf := float64((2.0*float32(band) + 1 - float32(tr)*p)) * 0.3333334
		n1 := int(tf)
		n2 := int(tf + float64(p))
		if n1 < 0 || 2*n1+1 >= hbeTwiceChan || n2 < 0 || 2*n2+1 >= hbeTwiceChan {
			continue
		}
		m1 := in[2*n1]*in[2*n1] + in[2*n1+1]*in[2*n1+1]
		m2 := in[2*n2]*in[2*n2] + in[2*n2+1]*in[2*n2+1]
		tmp := f32min(m1, m2)
		if tmp > maxMag {
			maxMag = tmp
			maxTr, maxN1, maxN2 = tr, n1, n2
		}
	}
	if !(maxMag > magZero && maxN1 >= 0 && maxN2 < hbeQmfChannels) {
		return
	}
	const magGain = 1.8856
	var vyr, vyi, vor, voi [2]float32
	var coefR, coefI [2]float32
	var d1, d2 float32
	var xzr, xzi float32
	midTr := 3 - maxTr
	if maxTr == 1 {
		d1, d2 = 0, 1.5
		xzr, xzi = in[2*maxN1], in[2*maxN1+1]
		idx := ((maxN2 & 3) + 1) & 3
		coefR[0] = esbrHbeInterpCoeff[idx*2]
		coefI[0] = esbrHbeInterpCoeff[idx*2+1]
		coefR[1], coefI[1] = coefR[0], -coefI[0]
		vyr[1], vyi[1] = in[2*maxN2], in[2*maxN2+1]
		tr0 := t.qmfIn[col-2+hbeZeroBandIdx][2*maxN2]
		ti0 := t.qmfIn[col-2+hbeZeroBandIdx][2*maxN2+1]
		vyr[0] = coefR[1]*tr0 - coefI[1]*ti0
		vyi[0] = coefI[1]*tr0 + coefR[1]*ti0
		tr1 := t.qmfIn[col-1+hbeZeroBandIdx][2*maxN2]
		ti1 := t.qmfIn[col-1+hbeZeroBandIdx][2*maxN2+1]
		vyr[0] += coefR[0]*tr1 - coefI[0]*ti1
		vyi[0] += coefI[0]*tr1 + coefR[0]*ti1
	} else {
		d1, d2 = 1.5, 0
		midTr = maxTr
		maxTr = 3 - maxTr
		xzr, xzi = in[2*maxN2], in[2*maxN2+1]
		idx := ((maxN1 & 3) + 1) & 3
		coefR[0] = esbrHbeInterpCoeff[idx*2]
		coefI[0] = esbrHbeInterpCoeff[idx*2+1]
		coefR[1], coefI[1] = coefR[0], -coefI[0]
		vyr[1], vyi[1] = in[2*maxN1], in[2*maxN1+1]
		tr0 := t.qmfIn[col-2+hbeZeroBandIdx][2*maxN1]
		ti0 := t.qmfIn[col-2+hbeZeroBandIdx][2*maxN1+1]
		vyr[0] = coefR[1]*tr0 - coefI[1]*ti0
		vyi[0] = coefI[1]*tr0 + coefR[1]*ti0
		tr1 := t.qmfIn[col-1+hbeZeroBandIdx][2*maxN1]
		ti1 := t.qmfIn[col-1+hbeZeroBandIdx][2*maxN1+1]
		vyr[0] += coefR[0]*tr1 - coefI[0]*ti1
		vyi[0] += coefI[0]*tr1 + coefR[0]*ti1
	}
	base := 1e-17
	base += float64(xzr * xzr)
	base += float64(xzi * xzi)
	fac := cbrtInv(base)
	xzr *= fac
	xzi *= fac
	for k := 0; k < 2; k++ {
		base = 1e-17
		base += float64(vyr[k] * vyr[k])
		base += float64(vyi[k] * vyi[k])
		fac = cbrtInv(base)
		vyr[k] *= fac
		vyi[k] *= fac
	}
	tr, ti := xzr, xzi
	for idx := 0; idx < midTr-1; idx++ {
		tmp := xzr
		xzr = xzr*tr - xzi*ti
		xzi = tmp*ti + xzi*tr
	}
	for k := 0; k < 2; k++ {
		tr, ti = vyr[k], vyi[k]
		for idx := 0; idx < maxTr-1; idx++ {
			tmp := vyr[k]
			vyr[k] = vyr[k]*tr - vyi[k]*ti
			vyi[k] = tmp*ti + vyi[k]*tr
		}
	}
	for k := 0; k < 2; k++ {
		vor[k] = vyr[k]*xzr - vyi[k]*xzi
		voi[k] = vyr[k]*xzi + vyi[k]*xzr
	}
	cosT := esbrHbeXProdCos3[pitchIdx<<1]
	sinT := esbrHbeXProdCos3[(pitchIdx<<1)+1]
	if d2 < d1 {
		sinT = -sinT
	}
	tr, ti = vor[0], voi[0]
	vor[0] = cosT*tr - sinT*ti
	voi[0] = cosT*ti + sinT*tr
	for k := 0; k < 2; k++ {
		o := &t.qmfOut[col*2+(k+hbeZeroBandIdx-1)]
		o[2*band] += float32(magGain) * vor[k]
		o[2*band+1] += float32(magGain) * voi[k]
	}
}

// xprodProc4 is ixheaacd_hbe_xprod_proc_4.
func (t *esbrHBE) xprodProc4(band, col int, p float32, pitchIdx int) {
	inp := band >> 1
	in := &t.qmfIn[col+hbeZeroBandIdx]
	magZero := in[2*inp]*in[2*inp] + in[2*inp+1]*in[2*inp+1]
	var maxMag float32
	maxTr, maxN1, maxN2 := 0, 0, 0
	for tr := 1; tr < 4; tr++ {
		tf := (2.0*float64(band) + 1 - float64(float32(tr)*p)) * 0.25
		n1 := int(tf) << 1
		n2 := int(tf+float64(p)) << 1
		if n1 < 0 || n1+1 >= hbeTwiceChan || n2 < 0 || n2+1 >= hbeTwiceChan {
			continue
		}
		m1 := in[n1]*in[n1] + in[n1+1]*in[n1+1]
		m2 := in[n2]*in[n2] + in[n2+1]*in[n2+1]
		tmp := f32min(m1, m2)
		if tmp > maxMag {
			maxMag = tmp
			maxTr, maxN1, maxN2 = tr, n1, n2
		}
	}
	if !(maxMag > magZero && maxN1 >= 0 && maxN2 < hbeTwiceChan) {
		return
	}
	const magGain = 2.0
	var vyr, vyi, vor, voi [2]float32
	var d1, d2 float32
	var xzr, xzi float32
	midTr := 4 - maxTr
	switch maxTr {
	case 1:
		d1, d2 = 0, 2
		xzr, xzi = in[maxN1], in[maxN1+1]
		for k := 0; k < 2; k++ {
			vyr[k] = t.qmfIn[col+hbeZeroBandIdx+2*(k-1)][maxN2]
			vyi[k] = t.qmfIn[col+hbeZeroBandIdx+2*(k-1)][maxN2+1]
		}
	case 2:
		d1, d2 = 0, 1
		xzr, xzi = in[maxN1], in[maxN1+1]
		for k := 0; k < 2; k++ {
			vyr[k] = t.qmfIn[col+hbeZeroBandIdx+(k-1)][maxN2]
			vyi[k] = t.qmfIn[col+hbeZeroBandIdx+(k-1)][maxN2+1]
		}
	default:
		d1, d2 = 2, 0
		midTr = maxTr
		maxTr = 4 - maxTr
		xzr, xzi = in[maxN2], in[maxN2+1]
		for k := 0; k < 2; k++ {
			vyr[k] = t.qmfIn[col+hbeZeroBandIdx+2*(k-1)][maxN1]
			vyi[k] = t.qmfIn[col+hbeZeroBandIdx+2*(k-1)][maxN1+1]
		}
	}
	norm4f := func(r, i float32) float32 {
		base := 1e-17
		base += float64(r * r)
		base += float64(i * i)
		temp := float32(math.Sqrt(math.Sqrt(base)))
		fac := temp * float32(math.Sqrt(float64(temp)))
		return 1 / fac
	}
	fac := norm4f(xzr, xzi)
	xzr *= fac
	xzi *= fac
	for k := 0; k < 2; k++ {
		fac = norm4f(vyr[k], vyi[k])
		vyr[k] *= fac
		vyi[k] *= fac
	}
	tr, ti := xzr, xzi
	for idx := 0; idx < midTr-1; idx++ {
		tmp := xzr
		xzr = xzr*tr - xzi*ti
		xzi = tmp*ti + xzi*tr
	}
	for k := 0; k < 2; k++ {
		tr, ti = vyr[k], vyi[k]
		for idx := 0; idx < maxTr-1; idx++ {
			tmp := vyr[k]
			vyr[k] = vyr[k]*tr - vyi[k]*ti
			vyi[k] = tmp*ti + vyi[k]*tr
		}
	}
	for k := 0; k < 2; k++ {
		vor[k] = vyr[k]*xzr - vyi[k]*xzi
		voi[k] = vyr[k]*xzi + vyi[k]*xzr
	}
	var cosT, sinT float32
	if d2 == 1 {
		cosT = esbrHbeXProdCos41[pitchIdx<<1]
		sinT = esbrHbeXProdCos41[(pitchIdx<<1)+1]
	} else {
		cosT = esbrHbeXProdCos4[pitchIdx<<1]
		sinT = esbrHbeXProdCos4[(pitchIdx<<1)+1]
		if d2 < d1 {
			sinT = -sinT
		}
	}
	tr, ti = vor[0], voi[0]
	vor[0] = cosT*tr - sinT*ti
	voi[0] = cosT*ti + sinT*tr
	for k := 0; k < 2; k++ {
		o := &t.qmfOut[col*2+(k+hbeZeroBandIdx-1)]
		o[2*band] += float32(magGain) * vor[k]
		o[2*band+1] += float32(magGain) * voi[k]
	}
}

// prod2 is ixheaacd_hbe_post_anal_prod2.
func (t *esbrHBE) prod2(vocCols, band int) {
	t.norm2(band)
	for ; band < t.xOver[1]; band++ {
		for i := 0; i < vocCols; i++ {
			xr, xi := t.norm[hbeZeroBandIdx+i][2*band], t.norm[hbeZeroBandIdx+i][2*band+1]
			for k := 0; k < hbeBlkLen2; k++ {
				tr, ti := t.norm[1+i+k][2*band], t.norm[1+i+k][2*band+1]
				o := &t.qmfOut[1+2*i+k]
				o[2*band] += (tr*xr - ti*xi) * 0.3333333
				o[2*band+1] += (tr*xi + ti*xr) * 0.3333333
			}
		}
	}
}

// xprod2 is ixheaacd_hbe_post_anal_xprod2.
func (t *esbrHBE) xprod2(vocCols, band int, p float32, cs [2]float32) {
	t.norm2(band)
	for ; band < t.xOver[1]; band++ {
		const magGain = 1.666666667
		tf := (2.0*float64(band) + 1 - float64(p)) * 0.5
		n1 := int(tf) << 1
		n2 := int(tf+float64(p)) << 1
		for i := 0; i < vocCols; i++ {
			xr, xi := t.norm[hbeZeroBandIdx+i][2*band], t.norm[hbeZeroBandIdx+i][2*band+1]
			for k := 1; k < hbeBlkLen2+1; k++ {
				tr, ti := t.norm[i+k][2*band], t.norm[i+k][2*band+1]
				o := &t.qmfOut[2*i+k]
				o[2*band] += (tr*xr - ti*xi) * 0.3333333
				o[2*band+1] += (tr*xi + ti*xr) * 0.3333333
			}
			in := &t.qmfIn[i+hbeZeroBandIdx]
			magZero := in[2*band]*in[2*band] + in[2*band+1]*in[2*band+1]
			var maxMag float32
			maxTr, maxN1, maxN2 := 0, 0, 0
			if n1 >= 0 && n1+1 < hbeTwiceChan && n2 >= 0 && n2+1 < hbeTwiceChan {
				m1 := in[n1]*in[n1] + in[n1+1]*in[n1+1]
				m2 := in[n2]*in[n2] + in[n2+1]*in[n2+1]
				tmp := f32min(m1, m2)
				if tmp > 0 {
					maxMag = tmp
					maxTr, maxN1, maxN2 = 1, n1, n2
				}
			}
			if !(maxMag > magZero && maxN1 >= 0 && maxN2 < hbeTwiceChan) {
				continue
			}
			midTr := 2 - maxTr
			zr, zi := in[maxN1], in[maxN1+1]
			base := 1e-17
			base += float64(zr * zr)
			base += float64(zi * zi)
			fac := float32(1.0 / base)
			fac = float32(math.Sqrt(math.Sqrt(float64(fac))))
			zr *= fac
			zi *= fac
			tr, ti := zr, zi
			for idx := 0; idx < midTr-1; idx++ {
				tmp := zr
				zr = zr*tr - zi*ti
				zi = tmp*ti + zi*tr
			}
			var vyr, vyi [2]float32
			for k := 0; k < 2; k++ {
				tr = t.qmfIn[i+hbeZeroBandIdx-1+k][maxN2]
				ti = t.qmfIn[i+hbeZeroBandIdx-1+k][maxN2+1]
				base = 1e-17
				base += float64(tr * tr)
				base += float64(ti * ti)
				fac = float32(1.0 / base)
				fac = float32(math.Sqrt(math.Sqrt(float64(fac))))
				vyr[k], vyi[k] = tr*fac, ti*fac
			}
			tr = vyr[0]*zr - vyi[0]*zi
			ti = vyr[0]*zi + vyi[0]*zr
			tr1 := cs[0]*tr - cs[1]*ti
			ti = cs[0]*ti + cs[1]*tr
			o := &t.qmfOut[i*2+(hbeZeroBandIdx-1)]
			o[2*band] += float32(magGain) * tr1
			o[2*band+1] += float32(magGain) * ti
			tr = vyr[1]*zr - vyi[1]*zi
			ti = vyr[1]*zi + vyi[1]*zr
			o = &t.qmfOut[i*2+(1+hbeZeroBandIdx-1)]
			o[2*band] += float32(magGain) * tr
			o[2*band+1] += float32(magGain) * ti
		}
	}
}

// prod3 is ixheaacd_hbe_post_anal_prod3 (cross products when withX is set,
// which is ixheaacd_hbe_post_anal_xprod3).
func (t *esbrHBE) prod3(vocCols, band int, withX bool, p float32, pitchIdx int) {
	for ; band < t.xOver[2]; band++ {
		inp := 2 * band / 3
		sel := esbrSelCase[((inp+1)&3)*8:]
		sel1 := esbrSelCase[(((inp+1)&3)+1)*8:]
		rem := 2*band - 3*inp
		if rem == 0 || rem == 1 {
			for i := 0; i < vocCols; i++ {
				var vec [2 * hbeOperWinLen]float32
				r := i
				pv := 0
				for k := 0; k < hbeBlkLen3; k += 2 {
					tr, ti := t.qmfIn[r][2*inp], t.qmfIn[r][2*inp+1]
					r += 2
					base1 := 1e-17 + float64(tr*tr)
					base1 += float64(ti * ti)
					fac := cbrtInv(base1)
					vec[pv], vec[pv+1] = tr*fac, ti*fac
					tr, ti = t.qmfIn[r][2*inp], t.qmfIn[r][2*inp+1]
					r--
					tr1 := sel[0]*tr + sel[1]*ti
					ti1 := sel[2]*tr + sel[3]*ti
					tr, ti = t.qmfIn[r][2*inp], t.qmfIn[r][2*inp+1]
					tr1 += sel[4]*tr + sel[5]*ti
					ti1 += sel[6]*tr + sel[7]*ti
					tr1 *= 0.3984033437
					ti1 *= 0.3984033437
					base1 = 1e-17 + float64(tr1*tr1)
					base1 += float64(ti1 * ti1)
					fac = cbrtInv(base1)
					vec[pv+2], vec[pv+3] = tr1*fac, ti1*fac
					pv += 4
					r += 2 // in_buf: +2 rows, -1 row, +2 rows = +3 rows per step
				}
				tr, ti := vec[2*(hbeZeroBandIdx-2)], vec[2*(hbeZeroBandIdx-2)+1]
				xzr := tr*tr - ti*ti
				xzi := tr*ti + ti*tr
				for k := 0; k < hbeBlkLen3; k++ {
					tr = vec[2*k]*xzr - vec[2*k+1]*xzi
					ti = vec[2*k]*xzi + vec[2*k+1]*xzr
					o := &t.qmfOut[2+2*i+k]
					o[2*band] += tr * 0.4714045
					o[2*band+1] += ti * 0.4714045
				}
				if withX {
					t.xprodProc3(band, i, p, pitchIdx)
				}
			}
		} else {
			for i := 0; i < vocCols; i++ {
				var vec, vecCap [2 * hbeOperWinLen]float32
				r := i
				pv := 0
				for k := 0; k < hbeBlkLen3; k += 2 {
					tr1, ti1 := t.qmfIn[r][2*inp], t.qmfIn[r][2*inp+1]
					tr, ti := t.qmfIn[r][2*(inp+1)], t.qmfIn[r][2*(inp+1)+1]
					base1 := 1e-17 + float64(tr*tr)
					base1 += float64(ti * ti)
					fac := cbrtInv(base1)
					vec[pv], vec[pv+1] = tr*fac, ti*fac
					base1 = 1e-17 + float64(tr1*tr1)
					base1 += float64(ti1 * ti1)
					fac = cbrtInv(base1)
					vecCap[pv], vecCap[pv+1] = tr1*fac, ti1*fac
					// in_buf (inp): +2 rows, read, -1 row, read; in_buf1: same
					ra := r + 2
					tr, ti = t.qmfIn[ra][2*inp], t.qmfIn[ra][2*inp+1]
					tr1 = sel[0]*tr + sel[1]*ti
					ti1 = sel[2]*tr + sel[3]*ti
					rb := r + 1
					tr, ti = t.qmfIn[rb][2*inp], t.qmfIn[rb][2*inp+1]
					tcr := tr1 + sel[4]*tr + sel[5]*ti
					tci := ti1 + sel[6]*tr + sel[7]*ti
					tr, ti = t.qmfIn[ra][2*(inp+1)], t.qmfIn[ra][2*(inp+1)+1]
					tr1 = sel1[0]*tr + sel1[1]*ti
					ti1 = sel1[2]*tr + sel1[3]*ti
					tr, ti = t.qmfIn[rb][2*(inp+1)], t.qmfIn[rb][2*(inp+1)+1]
					tvr := tr1 + sel1[4]*tr + sel1[5]*ti
					tvi := ti1 + sel1[6]*tr + sel1[7]*ti
					tcr *= 0.3984033437
					tci *= 0.3984033437
					tvr *= 0.3984033437
					tvi *= 0.3984033437
					base1 = 1e-17 + float64(tvr*tvr)
					base1 += float64(tvi * tvi)
					fac = cbrtInv(base1)
					vec[pv+2], vec[pv+3] = tvr*fac, tvi*fac
					base1 = 1e-17 + float64(tcr*tcr)
					base1 += float64(tci * tci)
					fac = cbrtInv(base1)
					vecCap[pv+2], vecCap[pv+3] = tcr*fac, tci*fac
					r += 3
					pv += 4
				}
				tr, ti := vecCap[2*(hbeZeroBandIdx-2)], vecCap[2*(hbeZeroBandIdx-2)+1]
				tr1, ti1 := vec[2*(hbeZeroBandIdx-2)], vec[2*(hbeZeroBandIdx-2)+1]
				xzr := tr*tr - ti*ti
				xzi := tr*ti + ti*tr
				tr = tr1*tr1 - ti1*ti1
				ti = tr1*ti1 + ti1*tr1
				for k := 0; k < hbeBlkLen3; k++ {
					tr1 = vec[2*k]*xzr - vec[2*k+1]*xzi
					ti1 = vec[2*k]*xzi + vec[2*k+1]*xzr
					tr1 += vecCap[2*k]*tr - vecCap[2*k+1]*ti
					ti1 += vecCap[2*k]*ti + vecCap[2*k+1]*tr
					o := &t.qmfOut[2+2*i+k]
					o[2*band] += tr1 * 0.23570225
					o[2*band+1] += ti1 * 0.23570225
				}
				if withX {
					t.xprodProc3(band, i, p, pitchIdx)
				}
			}
		}
	}
}

// prod4 is ixheaacd_hbe_post_anal_prod4 / _xprod4.
func (t *esbrHBE) prod4(vocCols, band int, withX bool, p float32, pitchIdx int) {
	t.norm4((band >> 1) - 1)
	for ; band < t.xOver[3]; band++ {
		inp := band >> 1
		ip := inp - 1
		if band&1 != 0 {
			ip = inp + 1
		}
		for i := 0; i < vocCols; i++ {
			xr, xi := t.norm[hbeZeroBandIdx+i][2*inp], t.norm[hbeZeroBandIdx+i][2*inp+1]
			tr, ti := xr, xi
			temp := xr*xr - xi*xi
			xi = xr*xi + xi*xr
			xr = tr*temp - ti*xi
			xi = tr*xi + ti*temp
			// recompute exactly as the original: x_zero_band_i was overwritten before use
			for k := 0; k < hbeBlkLen4; k++ {
				nr, ni := t.norm[i+2*k][2*ip], t.norm[i+2*k][2*ip+1]
				outR := nr*xr - ni*xi
				outI := nr*xi + ni*xr
				o := &t.qmfOut[3+2*i+k]
				o[2*band] += outR * 0.6666667
				o[2*band+1] += outI * 0.6666667
			}
			if withX {
				t.xprodProc4(band, i, p, pitchIdx)
			}
		}
	}
}

// postAnalProcess is ixheaacd_hbe_post_anal_process.
func (t *esbrHBE) postAnalProcess(pitch int, usf4 bool) error {
	vocCols := t.noBins / 2
	var p float32
	u := 0
	if usf4 {
		p = float32(float64(pitch) * 0.04166666666666)
		u = 1
	} else {
		p = float32(float64(pitch) * 0.08333333333333)
	}
	const pmin = 1.0
	if p < pmin {
		if 2 <= t.maxStretch {
			t.prod2(vocCols, t.xOver[0])
		}
		if 3 <= t.maxStretch {
			t.prod3(vocCols, t.xOver[1], false, 0, 0)
		}
		if 4 <= t.maxStretch {
			if t.xOver[2] <= 1 {
				return fmt.Errorf("media: usac HBE band table")
			}
			t.prod4(vocCols, t.xOver[2], false, 0, 0)
		}
		return nil
	}
	pi := pitch + u*128
	if 2 <= t.maxStretch {
		cs := [2]float32{esbrHbeXProdCos2[pi<<1], esbrHbeXProdCos2[(pi<<1)+1]}
		t.xprod2(vocCols, t.xOver[0], p, cs)
	}
	if 3 <= t.maxStretch {
		t.prod3(vocCols, t.xOver[1], true, p, pi)
	}
	if 4 <= t.maxStretch {
		if t.xOver[2] <= 1 {
			return fmt.Errorf("media: usac HBE band table")
		}
		t.prod4(vocCols, t.xOver[2], true, p, pi)
	}
	return nil
}

// replSpec is ixheaacd_hbe_repl_spec (4:1 only).
func esbrHbeReplSpec(xOver []int, re, im [][64]float32, noBins, maxStretch int) {
	numPatches := 0
	for i := 1; i < esbrMaxPatches; i++ {
		if xOver[i] != 0 {
			numPatches++
		}
	}
	for patch := maxStretch - 1; patch < numPatches; patch++ {
		patchBands := xOver[patch+1] - xOver[patch]
		target := xOver[patch]
		sourceBands := xOver[maxStretch-1] - xOver[maxStretch-2]
		for patchBands > 0 {
			nb := sourceBands
			startBand := xOver[maxStretch-1] - 1
			if target+nb >= xOver[patch+1] {
				nb = xOver[patch+1] - target
			}
			if (((target+nb-1)&1)+((xOver[maxStretch-1]-1)&1))&1 != 0 {
				if nb == sourceBands {
					nb--
				} else {
					startBand--
				}
			}
			if nb == 0 {
				break
			}
			for col := 0; col < noBins; col++ {
				band := target + nb - 1
				if band >= 64 {
					band = 63
				}
				if xOver[patch+1] <= band {
					band = xOver[patch+1] - 1
				}
				for i := 0; i < nb; i, band = i+1, band-1 {
					re[col][band] = re[col][startBand-i]
					im[col][band] = im[col][startBand-i]
				}
			}
			target += nb
			patchBands -= nb
		}
	}
}
