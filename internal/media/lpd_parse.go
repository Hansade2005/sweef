package media

import "fmt"

// Bitstream parsing of the linear-prediction-domain (LPD) core coder
// (ISO/IEC 23003-3 lpd_channel_stream(): ACELP, TCX, LPC parameters, FAC).
// Structure follows libxaac's ixheaacd_acelp_bitparse.c (Apache-2.0, see NOTICE).

const (
	lpdOrder      = 16
	lpdSubfr      = 64
	lpdFacLength  = 128
	lpdMaxPitch   = 411
	lpdSynthDelay = 448 // SYNTH_DELAY_LMAX
	lpdFilterDly  = 12
	lpdInterOrder = 16
	lpdPreemph    = 0.68
	lpdTiltCode   = 0.3
	lpdSuper      = 1024
)

// lpdFrame holds the parsed data of one LPD superframe.
type lpdFrame struct {
	acelpCoreMode  int
	mod            [4]int
	bpfControl     bool
	coreModeLast   bool
	facDataPresent bool
	shortFac       bool
	lpcIdx         [400]int
	noiseFactor    [4]int
	globalGain     [4]int
	tcxLg          [4]int
	tcxQuant       [lpdSuper + 8]int32
	arithReset     bool
	acbIndex       [16]int
	ltpFilt        [16]int
	icb            [16][8]int
	gains          [16]int
	meanEnergy     [4]int
	fac            [4 * lpdFacLength]int // per k: AVQ decoded FAC (no gain)
	facData        [lpdFacLength + 1]int // [0] gain index, then values
}

func lpdModeLPC(set int, r *aacBits, nk *int) int {
	mode := 0
	switch set {
	case 4:
		mode = 0
	case 0, 2:
		mode = r.bits(1)
		if mode == 1 {
			*nk = 3
		}
	case 1:
		if r.bits(1) == 0 {
			mode, *nk = 2, 2
		} else if r.bits(1) == 0 {
			mode, *nk = 0, 0
		} else {
			mode, *nk = 1, 1
		}
	case 3:
		if r.bits(1) == 0 {
			mode, *nk = 1, 1
		} else if r.bits(1) == 0 {
			mode, *nk = 0, 0
		} else {
			if r.bits(1) == 0 {
				mode = 2
			} else {
				mode = 3
			}
			*nk = 2
		}
	}
	return mode
}

func lpdUnary(r *aacBits) int {
	n := 0
	for r.bits(1) == 1 {
		n++
		if n > 64 || r.err {
			r.err = true
			break
		}
	}
	return n
}

func lpdQnData(nk int, qn *[2]int, r *aacBits) {
	switch nk {
	case 1:
		for k := 0; k < 2; k++ {
			qn[k] = lpdUnary(r)
			if qn[k] > 0 {
				qn[k]++
			}
		}
	default:
		for k := 0; k < 2; k++ {
			qn[k] = 2 + r.bits(2)
		}
		if nk == 2 {
			for k := 0; k < 2; k++ {
				if qn[k] > 4 {
					qn[k] = lpdUnary(r)
					if qn[k] > 0 {
						qn[k] += 4
					}
				}
			}
		} else {
			for k := 0; k < 2; k++ {
				if qn[k] > 4 {
					e := lpdUnary(r)
					switch e {
					case 0:
						qn[k] = 5
					case 1:
						qn[k] = 6
					case 2:
						qn[k] = 0
					default:
						qn[k] = e + 4
					}
				}
			}
		}
	}
}

func (f *lpdFrame) codebookIndices(nk int, pos *int, r *aacBits) {
	var qn [2]int
	lpdQnData(nk, &qn, r)
	f.lpcIdx[*pos] = qn[0]
	*pos++
	f.lpcIdx[*pos] = qn[1]
	*pos++
	for k := 0; k < 2; k++ {
		if qn[k] > 0 {
			var nkb, n int
			if qn[k] > 4 {
				nkb = (qn[k] - 3) / 2
				n = qn[k] - nkb*2
			} else {
				n = qn[k]
			}
			f.lpcIdx[*pos] = r.bits(4 * n)
			*pos++
			for i := 0; i < 8; i++ {
				f.lpcIdx[*pos] = r.bits(nkb)
				*pos++
			}
		}
	}
}

func (f *lpdFrame) lpcData(first bool, r *aacBits) {
	nk := 0
	j := 0
	lpdModeLPC(4, r, &nk)
	f.lpcIdx[j] = r.bits(8)
	j++
	f.codebookIndices(nk, &j, r)
	if first {
		mode := lpdModeLPC(0, r, &nk)
		f.lpcIdx[j] = mode
		j++
		if mode == 0 {
			f.lpcIdx[j] = r.bits(8)
			j++
		}
		f.codebookIndices(nk, &j, r)
	}
	if f.mod[0] < 3 {
		mode := lpdModeLPC(2, r, &nk)
		f.lpcIdx[j] = mode
		j++
		if mode == 0 {
			f.lpcIdx[j] = r.bits(8)
			j++
		}
		f.codebookIndices(nk, &j, r)
	}
	if f.mod[0] < 2 {
		mode := lpdModeLPC(1, r, &nk)
		f.lpcIdx[j] = mode
		j++
		if mode == 0 {
			f.lpcIdx[j] = r.bits(8)
			j++
		}
		if mode != 1 {
			f.codebookIndices(nk, &j, r)
		}
	}
	if f.mod[2] < 2 {
		mode := lpdModeLPC(3, r, &nk)
		f.lpcIdx[j] = mode
		j++
		if mode == 0 {
			f.lpcIdx[j] = r.bits(8)
			j++
		}
		f.codebookIndices(nk, &j, r)
	}
}

// facDecode reads FAC data of fac_length lines into prm (AVQ decoded).
func lpdFacDecode(facLen int, prm []int, r *aacBits) {
	for i := 0; i < facLen; i += 8 {
		qn := lpdUnary(r)
		if qn != 0 {
			qn++
		}
		nk, n := 0, qn
		if qn > 4 {
			nk = (qn - 3) >> 1
			n = qn - nk*2
		}
		idx := uint32(r.bits(4 * n))
		var kv [8]int
		for j := 0; j < 8; j++ {
			kv[j] = r.bits(nk)
		}
		lpdRotatedGosset(qn, idx, kv[:], prm[i:i+8])
	}
}

var lpdCelpBits = [8][4]int{
	{5, 5, 5, 5}, {9, 9, 5, 5}, {9, 9, 9, 9}, {13, 13, 9, 9},
	{13, 13, 13, 13}, {16, 16, 16, 16}, {1, 5, 1, 5}, {1, 5, 5, 5},
}

func (f *lpdFrame) acelpDecoding(k, numSubfr int, r *aacBits) {
	bits := lpdCelpBits[f.acelpCoreMode]
	f.meanEnergy[k] = r.bits(2)
	for sfr := 0; sfr < numSubfr; sfr++ {
		kk := k*4 + sfr
		if sfr == 0 || (numSubfr == 4 && sfr == 2) {
			f.acbIndex[kk] = r.bits(9)
		} else {
			f.acbIndex[kk] = r.bits(6)
		}
		f.ltpFilt[kk] = r.bits(1)
		if f.acelpCoreMode == 5 {
			for i := 0; i < 4; i++ {
				f.icb[kk][i] = r.bits(2)
			}
			for i := 4; i < 8; i++ {
				f.icb[kk][i] = r.bits(14)
			}
		} else {
			for i := 0; i < 4; i++ {
				f.icb[kk][i] = r.bits(bits[i])
			}
		}
		f.gains[kk] = r.bits(7)
	}
}

// parse reads lpd_channel_stream() of a channel. lastMode is the mode of the
// last LPD frame (st.modePrev); lenSub is ccfl/4.
func (d *usacDec) lpdParse(r *aacBits, c *usacCh, f *lpdFrame, indep bool) error {
	st := c.lpd
	lenSub := d.cfg.coreLen / 4
	numSub := d.cfg.coreLen * 4 / lpdSuper
	*f = lpdFrame{}
	f.acelpCoreMode = r.bits(3)
	mode := r.bits(5)
	if mode > 25 {
		return fmt.Errorf("media: usac bad lpd_mode")
	}
	switch {
	case mode == 25:
		f.mod = [4]int{3, 3, 3, 3}
	case mode == 24:
		f.mod = [4]int{2, 2, 2, 2}
	case mode >= 20:
		f.mod = [4]int{mode & 1, (mode >> 1) & 1, 2, 2}
	case mode >= 16:
		f.mod = [4]int{2, 2, mode & 1, (mode >> 1) & 1}
	default:
		f.mod = [4]int{mode & 1, (mode >> 1) & 1, (mode >> 2) & 1, (mode >> 3) & 1}
	}
	f.bpfControl = r.bit()
	f.coreModeLast = r.bit()
	f.facDataPresent = r.bit()
	first := !f.coreModeLast
	lastMode := st.modePrev
	firstTCX := true
	k := 0
	quant := 0
	for k < 4 {
		if k == 0 {
			if f.coreModeLast && f.facDataPresent {
				lpdFacDecode(lenSub/2, f.fac[k*lpdFacLength:], r)
			}
		} else if (lastMode == 0 && f.mod[k] > 0) || (lastMode > 0 && f.mod[k] == 0) {
			lpdFacDecode(lenSub/2, f.fac[k*lpdFacLength:], r)
		}
		if f.mod[k] == 0 {
			f.acelpDecoding(k, numSub, r)
			lastMode = 0
			f.tcxLg[k] = 0
			k++
		} else {
			f.noiseFactor[k] = r.bits(3)
			f.globalGain[k] = r.bits(7)
			switch f.mod[k] {
			case 1:
				f.tcxLg[k] = lenSub
			case 2:
				f.tcxLg[k] = 2 * lenSub
			default:
				f.tcxLg[k] = 4 * lenSub
			}
			if firstTCX {
				if indep {
					f.arithReset = true
				} else {
					f.arithReset = r.bit()
				}
			}
			lg := f.tcxLg[k]
			var q [lpdSuper]int32
			if !usacDecodeSpectrumAC(q[:], r, &c.arith, firstTCX && f.arithReset, lg, lg) {
				return fmt.Errorf("media: usac bad TCX spectral data")
			}
			copy(f.tcxQuant[quant:], q[:lg])
			lastMode = f.mod[k]
			quant += lg
			cnt := 1 << uint(f.mod[k]-1)
			for ii := 0; ii < cnt-1; ii++ {
				f.tcxLg[k+1+ii] = 0
			}
			k += cnt
			firstTCX = false
		}
		if r.err {
			return fmt.Errorf("media: usac truncated LPD frame")
		}
	}
	f.lpcData(first, r)
	if !f.coreModeLast && f.facDataPresent {
		f.shortFac = r.bit()
		facLen := d.cfg.coreLen / 8
		if f.shortFac {
			facLen = d.cfg.coreLen / 16
		}
		f.facData[0] = r.bits(7)
		if f.facData[0] > 128 {
			return fmt.Errorf("media: usac bad FAC gain")
		}
		lpdFacDecode(facLen, f.facData[1:], r)
	}
	if r.err {
		return fmt.Errorf("media: usac truncated LPD frame")
	}
	return nil
}
