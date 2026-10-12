package media

import "math"

// LPD core decoding: ACELP and TCX frames, LPC handling, forward aliasing
// cancellation and the bass postfilter (ISO/IEC 23003-3 7.14-7.18). The
// structure and the order of operations follow libxaac's
// ixheaacd_lpc.c, ixheaacd_acelp_decode.c, ixheaacd_tcx_fwd_alcnx.c and
// ixheaacd_tcx_fwd_mdct.c (Apache-2.0, see NOTICE), written with floating
// point and amplitude units of 1.0 = full scale.

type lpdState struct {
	modePrev      int
	lspOld        [lpdOrder]float64
	lsfPrev       [lpdOrder]float64
	lpFltPrev     [2 * (lpdOrder + 1)]float64
	excPrev       [1 + 2*lpdFacLength + 16]float64
	xcitPrev      [lpdMaxPitch + lpdInterOrder + 1]float64
	synthPrev     [lpdMaxPitch + lpdSynthDelay]float64
	bpfPrev       [lpdFilterDly + lpdSubfr]float64
	pitchPrev     [8]int
	gainPrev      [8]float64
	bpfActivePrev bool
	gainThreshold float64
	facGain       float64
	facFd         [lpdFacLength]float64
	fscale        int

	synthBuf [lpdMaxPitch + lpdSynthDelay + lpdSuper + 64]float64
	excBuf   [lpdMaxPitch + lpdInterOrder + 1 + lpdSuper + 64]float64
	fdSynth  [lpdOrder + 2*256 + 64]float64
	pitchArr [40]int
	gainArr  [40]float64

	dct map[int]*usacMDCT
}

func newLPDState(fscale int) *lpdState {
	s := &lpdState{fscale: fscale, dct: map[int]*usacMDCT{}}
	s.reset(false)
	return s
}

// reset corresponds to ixheaacd_reset_acelp_data_fix without the overlap part.
func (s *lpdState) reset(wasShort bool) {
	if wasShort {
		s.modePrev = -2
	} else {
		s.modePrev = -1
	}
	for i := range s.pitchPrev {
		s.pitchPrev[i] = 64
		s.gainPrev[i] = 0
	}
	s.bpfActivePrev = false
}

func (s *lpdState) dct4(in []float64, out []float64) {
	n := len(in)
	p := s.dct[n]
	if p == nil {
		p = newUsacMDCT(n)
		s.dct[n] = p
	}
	copy(out, p.dct4(in))
}

// ---- LSF / LPC helpers -----------------------------------------------------------

func lpdLsp2Lsf(lsp []float64, lsf []float64) {
	for i := 0; i < lpdOrder; i++ {
		lsf[i] = math.Acos(lsp[i]) * 6400 / math.Pi
	}
}

func lpdLsf2Lsp(lsf []float64, lsp []float64) {
	for i := 0; i < lpdOrder; i++ {
		lsp[i] = math.Cos(lsf[i] * math.Pi / 6400)
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// lpdLsp2Lp converts 16 LSPs to the 17 LPC coefficients a[0..16].
func lpdLsp2Lp(lsp []float64, a []float64) {
	// poly arrays are indexed with an offset of one so that f[-1] reads 0
	var p1, p2 [lpdOrder/2 + 2]float64
	var f1, f2 [lpdOrder/2 + 2]float64
	// f1/f2 use index -1 and -2 in the reference code: both are zero
	f1[0], f2[0] = 1, 1 // f[0]
	// emulate with explicit helpers
	g1 := func(i int) float64 {
		if i < 0 {
			return 0
		}
		return f1[i]
	}
	g2 := func(i int) float64 {
		if i < 0 {
			return 0
		}
		return f2[i]
	}
	q := 0
	for i := 1; i <= lpdOrder/2; i++ {
		b1 := -2 * lsp[q]
		b2 := -2 * lsp[q+1]
		q += 2
		f1[i] = b1*g1(i-1) + 2*g1(i-2)
		f2[i] = b2*g2(i-1) + 2*g2(i-2)
		for j := i - 1; j > 0; j-- {
			f1[j] += b1*g1(j-1) + g1(j-2)
			f2[j] += b2*g2(j-1) + g2(j-2)
		}
	}
	p1[0], p2[0] = 0, 0
	for i := 0; i <= lpdOrder/2; i++ {
		p1[i+1] = f1[i]
		p2[i+1] = f2[i]
	}
	// poly1/2 index: [0]=0, [1..]=f
	pp1 := lpdOrder/2 + 1
	pp2 := lpdOrder/2 + 1
	for i := 0; i < lpdOrder/2; i++ {
		p1[pp1] += p1[pp1-1]
		p2[pp2] -= p2[pp2-1]
		pp1--
		pp2--
	}
	a[0] = 1
	bot := 1
	top := lpdOrder
	i1, i2 := 2, 2
	for i := 0; i < lpdOrder/2; i++ {
		a[bot] = 0.5 * (p1[i1] + p2[i2])
		a[top] = 0.5 * (p1[i1] - p2[i2])
		bot++
		top--
		i1++
		i2++
	}
}

// lpdInterpLsp interpolates between two LSP vectors for nb subframes and
// returns (nb+1) LPC sets of 17 coefficients.
func lpdInterpLsp(old, nw []float64, a []float64, nb int) {
	factor := 1.0 / float64(nb)
	xp := 0.5 * factor
	var lsp [lpdOrder]float64
	for k := 0; k < nb; k++ {
		xm := 1 - xp
		for i := 0; i < lpdOrder; i++ {
			lsp[i] = old[i]*xm + nw[i]*xp
		}
		xp += factor
		lpdLsp2Lp(lsp[:], a[k*(lpdOrder+1):])
	}
	lpdLsp2Lp(nw, a[nb*(lpdOrder+1):])
}

// lpdLpcCoefGen: set 0 is the middle interpolation, 1 the old, 2 the new LSP.
func lpdLpcCoefGen(old, nw []float64, a []float64, nb int) {
	inc := 1 / float64(nb)
	fnew := 0.5 - 0.5*inc
	fold := 1 - fnew
	var lsf [lpdOrder]float64
	for i := 0; i < lpdOrder; i++ {
		lsf[i] = old[i]*fold + nw[i]*fnew
	}
	lpdLsp2Lp(lsf[:], a)
	lpdLsp2Lp(old, a[lpdOrder+1:])
	lpdLsp2Lp(nw, a[2*(lpdOrder+1):])
}

func lpdWeightLPC(a, ap []float64, gamma float64) {
	ap[0] = a[0]
	f := gamma
	for i := 1; i <= lpdOrder; i++ {
		ap[i] = f * a[i]
		f *= gamma
	}
}

// ---- LSF dequantisation -------------------------------------------------------------

const (
	lpdLsfGap  = 50.0
	lpdFreqMax = 6400.0
	lpdFreqDiv = 400.0
)

var lpdFactorTable = [4]float64{60, 65, 64, 63}

func lpdAVQDecode(read []int) (int, [lpdOrder]int) {
	var out [lpdOrder]int
	pos := 2
	for k := 0; k < 2; k++ {
		qn := read[k]
		var idx uint32
		var kv [8]int
		if qn > 0 {
			idx = uint32(read[pos])
			pos++
			copy(kv[:], read[pos:pos+8])
			pos += 8
		}
		lpdRotatedGosset(qn, idx, kv[:], out[k*8:k*8+8])
	}
	return pos, out
}

func lpdFirstApproxAbs(lsf []float64, idx []int) int {
	pos := 1
	w := usacLsfWeightAVQ[idx[0]*lpdOrder:]
	for i := 0; i < lpdOrder; i++ {
		lsf[i] = float64(usacLsfAbs8b[idx[0]*lpdOrder+i])
	}
	p, avq := lpdAVQDecode(idx[pos:])
	pos += p
	min := lpdLsfGap
	for i := 0; i < lpdOrder; i++ {
		lsf[i] += float64(w[i]) * float64(avq[i])
		if lsf[i] < min {
			lsf[i] = min
		}
		min = lsf[i] + lpdLsfGap
	}
	min = lpdFreqMax - lpdLsfGap
	for i := lpdOrder - 1; i >= 0; i-- {
		if lsf[i] > min {
			lsf[i] = min
		}
		min = lsf[i] - lpdLsfGap
	}
	return pos
}

func lpdFirstApproxRel(lsf []float64, idx []int, mode int) int {
	var d [lpdOrder + 1]float64
	d[0] = lsf[0]
	d[lpdOrder] = lpdFreqMax - lsf[lpdOrder-1]
	for i := 1; i < lpdOrder; i++ {
		d[i] = lsf[i] - lsf[i-1]
	}
	var w [lpdOrder]float64
	for i := 0; i < lpdOrder; i++ {
		w[i] = lpdFactorTable[mode] * math.Sqrt(d[i]*d[i+1]) / lpdFreqDiv
	}
	pos, avq := lpdAVQDecode(idx)
	min := lpdLsfGap
	for i := 0; i < lpdOrder; i++ {
		lsf[i] += w[i] * float64(avq[i])
		if lsf[i] < min {
			lsf[i] = min
		}
		min = lsf[i] + lpdLsfGap
	}
	return pos
}

// lpdDequant fills lsf[0..4] (5 vectors of 16) from the parsed indices.
func lpdDequant(f *lpdFrame, first bool, lsf []float64) {
	idx := f.lpcIdx[:]
	pos := lpdFirstApproxAbs(lsf[4*lpdOrder:], idx)
	idx = idx[pos:]
	if first {
		mode := idx[0]
		idx = idx[1:]
		if mode == 0 {
			pos = lpdFirstApproxAbs(lsf[0:], idx)
		} else if mode == 1 {
			copy(lsf[0:lpdOrder], lsf[4*lpdOrder:5*lpdOrder])
			pos = lpdFirstApproxRel(lsf[0:], idx, 3)
		}
		idx = idx[pos:]
	}
	if f.mod[0] < 3 {
		mode := idx[0]
		idx = idx[1:]
		if mode == 0 {
			pos = lpdFirstApproxAbs(lsf[2*lpdOrder:], idx)
		} else if mode == 1 {
			copy(lsf[2*lpdOrder:3*lpdOrder], lsf[4*lpdOrder:5*lpdOrder])
			pos = lpdFirstApproxRel(lsf[2*lpdOrder:], idx, 3)
		}
		idx = idx[pos:]
	}
	if f.mod[0] < 2 {
		mode := idx[0]
		idx = idx[1:]
		if mode == 1 {
			for i := 0; i < lpdOrder; i++ {
				lsf[lpdOrder+i] = 0.5 * (lsf[i] + lsf[2*lpdOrder+i])
			}
		} else {
			if mode == 0 {
				pos = lpdFirstApproxAbs(lsf[lpdOrder:], idx)
			} else if mode == 2 {
				copy(lsf[lpdOrder:2*lpdOrder], lsf[2*lpdOrder:3*lpdOrder])
				pos = lpdFirstApproxRel(lsf[lpdOrder:], idx, 2)
			}
			idx = idx[pos:]
		}
	}
	if f.mod[2] < 2 {
		mode := idx[0]
		idx = idx[1:]
		switch mode {
		case 0:
			pos = lpdFirstApproxAbs(lsf[3*lpdOrder:], idx)
		case 1:
			for i := 0; i < lpdOrder; i++ {
				lsf[3*lpdOrder+i] = 0.5 * (lsf[2*lpdOrder+i] + lsf[4*lpdOrder+i])
			}
			pos = lpdFirstApproxRel(lsf[3*lpdOrder:], idx, 1)
		case 2:
			copy(lsf[3*lpdOrder:4*lpdOrder], lsf[2*lpdOrder:3*lpdOrder])
			pos = lpdFirstApproxRel(lsf[3*lpdOrder:], idx, 2)
		case 3:
			copy(lsf[3*lpdOrder:4*lpdOrder], lsf[4*lpdOrder:5*lpdOrder])
			pos = lpdFirstApproxRel(lsf[3*lpdOrder:], idx, 2)
		}
		_ = idx[pos:]
	}
}
