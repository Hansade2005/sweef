package media

import (
	"os"
	"fmt"
	"math"
)

var lpdFirIdx = func() [lpdFilterDly + 1]float64 {
	var a [lpdFilterDly + 1]float64
	for i := range a {
		a[i] = float64(usacBpfFir[i])
	}
	return a
}()

// lpdBassPostfilter: long-term postfilter of the LPD output.
// synth: buffer with sOff the index of the first output sample (the filter
// reads history before and look-ahead after it).
func lpdBassPostfilter(synth []float64, sOff int, pitch []int, pgain []float64, out []float64, lenFr, len2 int, bpfPrev []float64) error {
	var noiseBuf [lpdFilterDly + 2*lpdSubfr]float64
	// output = synth shifted by one subframe back
	copy(out[:lenFr], synth[sOff-lpdSubfr:sOff-lpdSubfr+lenFr])
	if lenFr%64 != 0 {
		for i := lenFr; i < lenFr+(lpdSubfr-lenFr%64); i++ {
			out[i] = 0
		}
	}
	sf := 0
	for ns := 0; ns < lenFr; ns, sf = ns+lpdSubfr, sf+1 {
		pl := pitch[sf]
		gain := pgain[sf]
		if (pl>>1)+96-ns > lpdMaxPitch {
			return fmt.Errorf("media: usac bad pitch")
		}
		if gain > 1 {
			gain = 1
		}
		if gain < 0 {
			gain = 0
		}
		xo := sOff + ns - 96
		yo := sOff + ns - pl/2 - 96
		xe, xy, ye := 0.01, 0.01, 0.01
		for i := 0; i < lpdSubfr+96; i++ {
			x, y := synth[xo+i], synth[yo+i]
			xe += x * x
			xy += x * y
			ye += y * y
		}
		nc := xy / math.Sqrt(xe*ye)
		if nc > 0.95 {
			pl >>= 1
		}
		lg := lenFr + len2 - pl - ns
		if lg < 0 {
			lg = 0
		}
		if lg > lpdSubfr {
			lg = lpdSubfr
		}
		if pl > lpdMaxPitch {
			return fmt.Errorf("media: usac bad pitch")
		}
		n2 := noiseBuf[lpdFilterDly+lpdSubfr:]
		if gain > 0 {
			if lg > 0 {
				tmp, energy := 0.01, 0.01
				for i := 0; i < lg; i++ {
					v := synth[sOff+i+ns]
					tmp += v * v
					w := synth[sOff+i+ns+pl]
					energy += w * w
				}
				tmp = math.Sqrt(tmp / energy)
				if tmp < gain {
					gain = tmp
				}
			}
			alpha := 0.5 * gain
			for i := 0; i < lg; i++ {
				n2[i] = alpha * (synth[sOff+i+ns] - 0.5*synth[sOff+i+ns-pl] - 0.5*synth[sOff+i+ns+pl])
			}
			for i := lg; i < lpdSubfr; i++ {
				n2[i] = alpha * (synth[sOff+i+ns] - synth[sOff+i+ns-pl])
			}
		} else {
			for i := 0; i < lpdSubfr; i++ {
				n2[i] = 0
			}
		}
		copy(noiseBuf[:], bpfPrev[:lpdFilterDly+lpdSubfr])
		copy(bpfPrev[:lpdFilterDly+lpdSubfr], noiseBuf[lpdSubfr:lpdSubfr+lpdFilterDly+lpdSubfr])
		n1 := lpdFilterDly // index of noise_tmp1[0]
		for i := 0; i < lpdSubfr; i++ {
			tmp := lpdFirIdx[0] * noiseBuf[n1+i]
			for j := 1; j <= lpdFilterDly; j++ {
				tmp += lpdFirIdx[j] * (noiseBuf[n1+i-j] + noiseBuf[n1+i+j])
			}
			out[ns+i] -= tmp
		}
	}
	return nil
}

// lpdDecode decodes one LPD superframe into out (ccfl samples). c.olap holds
// the overlap data of the previous (FD) frame in libxaac's layout.
func (d *usacDec) lpdDecode(c *usacCh, f *lpdFrame, first bool, out []float64) error {
	st := c.lpd
	if st.fscale > 24000 {
		// the pitch ranges of the reference decoder end at a 24 kHz core
		return fmt.Errorf("%w: usac LPD above 24 kHz", ErrUnsupported)
	}
	cfg := d.cfg
	lenFr := cfg.coreLen
	lenSub := lenFr / 4
	numSub := lenFr * 4 / lpdSuper
	nInSuper := 4 * numSub
	by2 := nInSuper/2 - 1
	synthDelay := by2 * lpdSubfr
	sb := st.synthBuf[:]
	eb := st.excBuf[:]
	synthBase := lpdMaxPitch + synthDelay
	// history
	copy(sb[:lpdMaxPitch+synthDelay], st.synthPrev[:lpdMaxPitch+synthDelay])
	for i := 0; i < lpdSynthDelay+lpdSuper-synthDelay; i++ {
		sb[synthBase+i] = 0
	}
	eCur := lpdMaxPitch + lpdInterOrder + 1
	copy(eb[:eCur], st.xcitPrev[:])
	for i := 0; i < lpdSuper+1; i++ {
		eb[eCur+i] = 0
	}
	pitch := st.pitchArr[:]
	pgain := st.gainArr[:]
	for i := 0; i < by2; i++ {
		pitch[i] = st.pitchPrev[i]
		pgain[i] = st.gainPrev[i]
	}
	for i := 0; i < nInSuper; i++ {
		pitch[i+by2] = 64
		pgain[i+by2] = 0
	}
	var lsf [(2*4 + 1) * lpdOrder]float64
	if !first {
		lpdLsp2Lsf(st.lspOld[:], lsf[:lpdOrder])
	}
	lpdDequant(f, first, lsf[:])
	if os.Getenv("MODS") != "" && lpdDbgN2 < 8 {
		lpdDbgN2++
		println("MYLSF", first, fmt.Sprintf("%.5f %.5f %.5f %.5f %.5f %.5f", lsf[64], lsf[65], lsf[66], lsf[67], lsf[68], lsf[79]))
	}
	if first {
		copy(st.lsfPrev[:], lsf[:lpdOrder])
		lpdLsf2Lsp(st.lsfPrev[:], st.lspOld[:])
	}
	mod := f.mod
	if os.Getenv("MODS") != "" {
		println("MODS", mod[0], mod[1], mod[2], mod[3], f.bpfControl)
	}
	if (first && mod[0] == 0) || (first && mod[1] == 0) || (first && mod[2] == 0 && lenSub != 256) {
		var lpA [9 * (lpdOrder + 1)]float64
		lpdInterpLsp(st.lspOld[:], st.lspOld[:], lpA[:], 8)
		copy(st.lpFltPrev[:lpdOrder+1], lpA[:lpdOrder+1])
		copy(st.lpFltPrev[lpdOrder+1:], lpA[:lpdOrder+1])
		if mod[0] == 0 {
			facLen := lenSub / 2
			if f.shortFac {
				facLen = lenSub * 4 / 16
			}
			gain := math.Pow(10, float64(f.facData[0])/28)
			var fd [lpdFacLength + 1]float64
			// reorder (interleave -> block form)
			for i := 0; i < facLen; i++ {
				fd[i] = float64(f.facData[1+i])
			}
			d.lpdFwdAlias(c, fd[:facLen], facLen, lpA[:lpdOrder+1], gain)
			for i := 0; i < facLen; i++ {
				c.olap[lenFr/2+i] = 0
			}
		}
		for i := 0; i < 2*lenSub; i++ {
			st.fdSynth[lpdOrder+i] = c.olap[i] * 32768
		}
		copy(sb[synthBase-2*lenSub:synthBase], st.fdSynth[lpdOrder:lpdOrder+2*lenSub])
		// pre-emphasis
		for i := 2*lenSub - 1; i > 0; i-- {
			st.fdSynth[lpdOrder+i] -= lpdPreemph * st.fdSynth[lpdOrder+i-1]
		}
		st.fdSynth[lpdOrder] -= 0
		for i := 0; i < lpdOrder; i++ {
			st.fdSynth[i] = 0
		}
		tmpRes := make([]float64, 2*lenSub+8)
		lpdResidual(lpA[:], st.fdSynth[:], lpdOrder, tmpRes, 0, lpdSubfr, 2*lenSub/lpdSubfr)
		// residual_tool_float1 uses the same LPC for all blocks
		for j := 0; j < 2*lenSub/lpdSubfr; j++ {
			for i := 0; i < lpdSubfr; i++ {
				xi := lpdOrder + j*lpdSubfr + i
				s := st.fdSynth[xi]
				for t := 1; t <= lpdOrder; t++ {
					s += lpA[t] * st.fdSynth[xi-t]
				}
				tmpRes[j*lpdSubfr+i] = s
			}
		}
		var ns int
		if mod[0] != 0 && (lenSub == 256 || mod[1] != 0) {
			ns = lenSub
			if lpdMaxPitch+lpdInterOrder+1 < ns {
				ns = lpdMaxPitch + lpdInterOrder + 1
			}
		} else {
			ns = 2 * lenSub
			if lpdMaxPitch+lpdInterOrder+1 < ns {
				ns = lpdMaxPitch + lpdInterOrder + 1
			}
		}
		copy(eb[eCur-ns:eCur], tmpRes[2*lenSub-ns:2*lenSub])
	}
	var lpFlt [(4*3 + 4) * (lpdOrder + 1)]float64
	var lspCur, lsfCur [lpdOrder]float64
	k := 0
	for k < 4 {
		mode := mod[k]
		if st.modePrev == 0 && mode > 0 && (k != 0 || st.bpfActivePrev) {
			i := k*numSub + by2
			pitch[i+1], pitch[i] = pitch[i-1], pitch[i-1]
			pgain[i+1], pgain[i] = pgain[i-1], pgain[i-1]
		}
		switch mode {
		case 0, 1:
			copy(lsfCur[:], lsf[(k+1)*lpdOrder:(k+2)*lpdOrder])
		case 2:
			copy(lsfCur[:], lsf[(k+2)*lpdOrder:(k+3)*lpdOrder])
		default:
			copy(lsfCur[:], lsf[(k+4)*lpdOrder:(k+5)*lpdOrder])
		}
		lpdLsf2Lsp(lsfCur[:], lspCur[:])
		var tmp float64
		for i := 0; i < lpdOrder; i++ {
			dd := lsfCur[i] - st.lsfPrev[i]
			tmp += dd * dd
		}
		stability := 1.25 - tmp/400000
		if stability > 1 {
			stability = 1
		}
		if stability < 0 {
			stability = 0
		}
		if mode == 0 {
			lpdLspN++
			if lpdLspN == 1 {
				println("MYLSP old", fmt.Sprintf("%.7f new %.7f k %d", st.lspOld[:], lspCur[:], k))
			}
			lpdInterpLsp(st.lspOld[:], lspCur[:], lpFlt[:], numSub)
			d.lpdACELPCall(c, f, k, lpFlt[:], stability)
			if st.modePrev != 0 && f.bpfControl {
				i := k*numSub + by2
				pitch[i-1] = pitch[i]
				pgain[i-1] = pgain[i]
				if st.modePrev != -2 {
					pitch[i-2] = pitch[i]
					pgain[i-2] = pgain[i]
				}
			}
			k++
		} else {
			var nSub, subLen int
			switch mode {
			case 1:
				subLen, nSub = lenSub, numSub
			case 2:
				subLen, nSub = lenSub<<1, nInSuper/2
			default:
				subLen, nSub = lenSub<<2, nInSuper
			}
			lpdLpcCoefGen(st.lspOld[:], lspCur[:], lpFlt[:], nSub)
			d.lpdTCX(c, f, k, lpFlt[:], subLen)
			k += 1 << uint(mode-1)
		}
		st.modePrev = mode
		copy(st.lspOld[:], lspCur[:])
		copy(st.lsfPrev[:], lsfCur[:])
	}
	if lpdDbg && lpdDbgN2 < 8 {
		lpdDbgN2++
		println("MYSY", lpdDbgN2, sb[synthBase], sb[synthBase+1], sb[synthBase+2], sb[synthBase+3], "256:", sb[synthBase+256], sb[synthBase+257], sb[synthBase+258], sb[synthBase+259], "800:", sb[synthBase+800], sb[synthBase+801], sb[synthBase+802], sb[synthBase+803], st.lspOld[0], st.lspOld[1])
	}
	copy(st.xcitPrev[:], eb[lenFr:lenFr+lpdMaxPitch+lpdInterOrder+1])
	copy(st.synthPrev[:], sb[lenFr:lenFr+lpdMaxPitch+synthDelay])
	if !f.bpfControl {
		if mod[0] != 0 && st.bpfActivePrev {
			for i := 2; i < nInSuper; i++ {
				pgain[by2+i] = 0
			}
		} else {
			for i := 0; i < nInSuper; i++ {
				pgain[by2+i] = 0
			}
		}
	}
	st.bpfActivePrev = f.bpfControl
	for i := 0; i < by2; i++ {
		st.pitchPrev[i] = pitch[nInSuper+i]
		st.gainPrev[i] = pgain[nInSuper+i]
	}
	for i := 0; i < nInSuper; i++ {
		tp := pitch[i]
		if pgain[i] > 0 {
			var corr float64
			en := 1e-6
			for kk := 0; kk < lpdSubfr; kk++ {
				a := sb[lpdMaxPitch+i*lpdSubfr+kk]
				b := sb[lpdMaxPitch+i*lpdSubfr-tp+kk]
				corr += a * b
				en += b * b
			}
			pgain[i] = corr / en
		}
	}
	len2 := synthDelay
	if mod[3] != 0 {
		len2 = synthDelay - lenSub/2
	}
	buf := make([]float64, lenFr+lpdSubfr)
	if err := lpdBassPostfilter(sb, lpdMaxPitch, pitch, pgain, buf, lenFr, len2, st.bpfPrev[:]); err != nil {
		return err
	}
	copy(out, buf[:lenFr])
	return nil
}

func (d *usacDec) lpdACELPCall(c *usacCh, f *lpdFrame, k int, lp []float64, stability float64) {
	d.lpdACELP(c, f, k, lp, stability)
}

// lpdFwdAlias: forward aliasing cancellation for an ACELP first frame after
// an FD frame: adds the FAC synthesis to the overlap data.
func (d *usacDec) lpdFwdAlias(c *usacCh, x []float64, facLen int, a []float64, gain float64) {
	st := c.lpd
	sig := make([]float64, facLen)
	st.dct4(x[:facLen], sig)
	var ap [lpdOrder + 1]float64
	lpdWeightLPC(a, ap[:], 0.92)
	var zero [lpdOrder]float64
	lpdSynthFilter(ap[:], sig, sig, zero[:])
	if os.Getenv("MODS") != "" {
		println("MYFA gain", float32(gain), "x", int(x[0]), int(x[1]), int(x[2]), int(x[3]), int(x[4]), int(x[5]), "sig", float32(sig[0]), float32(sig[1]), float32(sig[2]), float32(sig[3]))
	}
	base := d.cfg.coreLen/2 - facLen
	for i := 0; i < facLen; i++ {
		c.olap[base+i] += sig[i] * gain / 32768
	}
}

var lpdDbgN2 int

var lpdLspN int
