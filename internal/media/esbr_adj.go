package media

import (
	"fmt"
	"math"
	"sort"
)

// eSBR envelope adjustment (gain, noise and sinusoid addition, limiter and
// the inter-TES shaping), after libxaac's ixheaacd_esbr_envcal.c (Apache-2.0,
// see NOTICE).

const esbrEPS = float32(1e-12)
const esbrGuard = 1e-17

func esbrFir(n int) []float32 {
	switch n {
	case 0:
		return esbrFir0[:]
	case 1:
		return esbrFir1[:]
	case 2:
		return esbrFir2[:]
	case 3:
		return esbrFir3[:]
	}
	return esbrFir4[:]
}

type esbrLim struct {
	table [4][13]int
	gate  [4]int
}

// createLimiterBands is ixheaacd_createlimiterbands. xover is the transposer
// patch border table (nil without HBE).
func esbrCreateLimiterBands(l *esbrLim, freqTbl []int, numBands int, xover []int, patchingMode int, usf4 bool, pp *esbrPatch) error {
	subStart := freqTbl[0]
	subEnd := freqTbl[numBands]
	var patchBorders [esbrMaxPatches + 1]int
	numPatches := 0
	if patchingMode == 0 && xover != nil {
		if usf4 {
			for i := 1; i < esbrMaxPatches; i++ {
				if xover[i] != 0 {
					numPatches++
				}
			}
		} else {
			for i := 1; i < 4; i++ {
				if xover[i] != 0 {
					numPatches++
				}
			}
		}
		for i := 0; i < numPatches; i++ {
			patchBorders[i] = xover[i] - subStart
		}
	} else {
		numPatches = pp.num
		for i := 0; i < numPatches; i++ {
			patchBorders[i] = pp.start[i] - subStart
		}
	}
	patchBorders[numPatches] = subEnd - subStart
	l.table[0][0] = freqTbl[0] - subStart
	l.table[0][1] = freqTbl[numBands] - subStart
	l.gate[0] = 1
	limPerOct := [4]float64{0, 1.2, 2.0, 3.0}
	log2 := math.Log(2.0)
	for i := 1; i < 4; i++ {
		var tmp [32 + esbrMaxPatches + 1]int
		for k := 0; k <= numBands; k++ {
			tmp[k] = freqTbl[k] - subStart
		}
		for k := 1; k < numPatches; k++ {
			tmp[numBands+k] = patchBorders[k]
		}
		l.gate[i] = numBands + numPatches - 1
		sort.Ints(tmp[:l.gate[i]+1])
		for j := 1; j <= l.gate[i]; j++ {
			numOct := math.Log(float64(tmp[j]+subStart)/float64(tmp[j-1]+subStart)) / log2
			if numOct*limPerOct[i] < 0.49 {
				if tmp[j] == tmp[j-1] {
					tmp[j] = subEnd
					sort.Ints(tmp[:l.gate[i]+1])
					l.gate[i]--
					j--
					continue
				}
				var isBorder [2]bool
				for k := 0; k <= numPatches; k++ {
					if tmp[j-1] == patchBorders[k] {
						isBorder[0] = true
						break
					}
				}
				for k := 0; k <= numPatches; k++ {
					if tmp[j] == patchBorders[k] {
						isBorder[1] = true
						break
					}
				}
				if !isBorder[1] {
					tmp[j] = subEnd
					sort.Ints(tmp[:l.gate[i]+1])
					l.gate[i]--
					j--
				} else if !isBorder[0] {
					tmp[j-1] = subEnd
					sort.Ints(tmp[:l.gate[i]+1])
					l.gate[i]--
					j--
				}
			}
		}
		if l.gate[i] > 12 {
			return fmt.Errorf("media: usac sbr limiter bands")
		}
		for k := 0; k <= l.gate[i]; k++ {
			l.table[i][k] = tmp[k]
		}
	}
	return nil
}

// applyInterTes is ixheaacd_apply_inter_tes. q is the HF adjusted signal,
// low the original low band (both rows start at the first slot of the
// envelope).
func esbrApplyInterTes(lowRe, lowIm, qRe, qIm [][64]float32, off, num, subStart, numSub, gammaIdx int) error {
	const maxSlots = 78
	if num > maxSlots {
		return fmt.Errorf("media: usac sbr inter-TES")
	}
	gamma := esbrQGamma[gammaIdx]
	if gamma <= 0 {
		return nil
	}
	subEnd := subStart + numSub
	var powHigh, powLow, gain [maxSlots]float32
	var totHigh, totLow float32
	totHighAfter := float32(1.0e-6)
	for i := 0; i < num; i++ {
		for j := 0; j < subStart; j++ {
			qRe[off+i][j] = lowRe[off+i][j]
			qIm[off+i][j] = lowIm[off+i][j]
		}
	}
	for i := 0; i < num; i++ {
		for j := 0; j < subStart; j++ {
			powLow[i] += qRe[off+i][j] * qRe[off+i][j]
			powLow[i] += qIm[off+i][j] * qIm[off+i][j]
		}
		for j := subStart; j < subEnd; j++ {
			powHigh[i] += qRe[off+i][j] * qRe[off+i][j]
			powHigh[i] += qIm[off+i][j] * qIm[off+i][j]
		}
		totLow += powLow[i]
		totHigh += powHigh[i]
	}
	for i := 0; i < num; i++ {
		gain[i] = float32(math.Sqrt(float64(powLow[i] * float32(num) / (totLow + 1.0e-6))))
	}
	for i := 0; i < num; i++ {
		gain[i] = 1.0 + gamma*(gain[i]-1.0)
	}
	for i := 0; i < num; i++ {
		if gain[i] < 0.2 {
			gain[i] = 0.2
		}
		powHigh[i] *= gain[i] * gain[i]
		totHighAfter += powHigh[i]
	}
	gainAdj2 := totHigh / totHighAfter
	gainAdj := float32(math.Sqrt(float64(gainAdj2)))
	for i := 0; i < num; i++ {
		gain[i] *= gainAdj
		for j := subStart; j < subEnd; j++ {
			qRe[off+i][j] *= gain[i]
			qIm[off+i][j] *= gain[i]
		}
	}
	return nil
}

// envCalc is ixheaacd_sbr_env_calc. in is the patched high band (dst of the
// HF generator), low the original QMF samples; both start at the SBR_HF_ADJ
// offset. xover is the transposer patch table (nil without HBE); pvcOut the
// PVC envelope (nil unless the frame uses PVC).
func (c *esbrChan) envCalc(f *esbrFrame, h *esbrHeader, inRe, inIm, lowRe, lowIm [][64]float32, xover []int, pvcOut []float32) error {
	rate := 2
	if h.usf4 {
		rate = 4
	}
	p := &f.info
	subStart, subEnd := h.subStart, h.subEnd
	numSub := subEnd - subStart
	smoothingLen := 4
	if h.smoothingMode != 0 {
		smoothingLen = 0
	}
	intMode := h.interpolFreq != 0
	limBand := h.limiterBands
	limGains := h.limiterGains
	numEnv := p.numEnv
	transEnv := p.transientEnv
	sbrMode := f.sbrMode
	nNf := h.nNf
	reset := f.resetFlag
	startUp := h.esbrStartUp
	startUpPVC := h.esbrStartPVC
	if reset {
		startUp, startUpPVC = true, true
		if reset {
			c.phaseIndex = 0
		}
		if err := esbrCreateLimiterBands(&c.lim, h.fLow[:], h.nSf[0], xover, f.patchingMode, h.usf4, &c.patch); err != nil {
			return err
		}
	}
	if f.patchingMode != f.prevPatchingMode {
		if err := esbrCreateLimiterBands(&c.lim, h.fLow[:], h.nSf[0], xover, f.patchingMode, h.usf4, &c.patch); err != nil {
			return err
		}
		f.prevPatchingMode = f.patchingMode
	}
	var harmonics [64]int8
	if sbrMode == esbrPVCMode {
		if err := c.envCalcPVC(f, h, inRe, inIm, lowRe, lowIm, pvcOut, &harmonics, &startUp, &startUpPVC); err != nil {
			return err
		}
	} else {
		for i := 0; i < h.nSf[1]; i++ {
			li := h.fHigh[i]
			ui := h.fHigh[i+1]
			t := (ui + li - subStart<<1) >> 1
			if t >= 64 || t < 0 {
				return fmt.Errorf("media: usac sbr harmonic index")
			}
			harmonics[t] = int8(f.addHarmonics[i])
		}
		var nrgTone, noiseLevel, nrgEst, nrgRef, nrgGain [64]float32
		kk, next, m := 0, -1, 0
		for i := 0; i < numEnv; i++ {
			if kk > esbrMaxNoiseEnv {
				return fmt.Errorf("media: usac sbr noise envelope")
			}
			if p.borderVec[i] == p.noiseBorderVec[kk] {
				kk++
				next++
			}
			noiseAbsc := i == transEnv || i == c.envShortFlagPrev
			sl := smoothingLen
			if noiseAbsc {
				sl = 0
			}
			filt := esbrFir(sl)
			fr := p.freqRes[i]
			tbl := h.fLow[:]
			if fr != 0 {
				tbl = h.fHigh[:]
			}
			cc, o := 0, 0
			for j := 0; j < h.nSf[fr]; j++ {
				li, ui := tbl[j], tbl[j+1]
				ui2 := h.noiseTbl[o+1]
				flag := false
				if p.borderVec[i] >= p.borderVec[i+1] {
					for k := li; k < ui; k++ {
						if harmonics[cc] != 0 && (i >= transEnv || c.harmFlagPrev[cc+subStart] != 0) {
							flag = true
						}
						nrgEst[cc] = 0
						cc++
					}
				} else {
					for k := li; k < ui; k++ {
						var nrg float32
						for l := rate * p.borderVec[i]; l < rate*p.borderVec[i+1]; l++ {
							nrg += inRe[l][k]*inRe[l][k] + inIm[l][k]*inIm[l][k]
						}
						if harmonics[cc] != 0 && (i >= transEnv || c.harmFlagPrev[cc+subStart] != 0) {
							flag = true
						}
						nrgEst[cc] = nrg / float32(rate*p.borderVec[i+1]-rate*p.borderVec[i])
						cc++
					}
				}
				var nrgAvg float32
				if !intMode && ui != li {
					for k := cc - (ui - li); k < cc; k++ {
						nrgAvg += nrgEst[k]
					}
					nrgAvg /= float32(ui - li)
				}
				cc -= ui - li
				for k := 0; k < ui-li; k++ {
					if k+li >= ui2 {
						o++
					}
					if o >= esbrMaxNoiseCoeffs {
						return fmt.Errorf("media: usac sbr noise band")
					}
					ui2 = h.noiseTbl[o+1]
					nrgRef[cc] = f.fltEnv[m]
					if !intMode {
						nrgEst[cc] = nrgAvg
					}
					nrgTone[cc] = 0
					nf := f.fltNoise[next*nNf+o]
					tmp := float64(nf) / (float64(1+nf) + esbrGuard)
					ref := float64(nrgRef[cc])
					est1 := float64(nrgEst[cc] + 1)
					if flag {
						nrgGain[cc] = float32(math.Sqrt(ref * tmp / est1))
						if harmonics[cc] != 0 && (i >= transEnv || c.harmFlagPrev[cc+subStart] != 0) {
							nrgTone[cc] = float32(math.Sqrt(ref * tmp / math.Abs(float64(nf)+esbrGuard)))
						}
					} else {
						if noiseAbsc {
							nrgGain[cc] = float32(math.Sqrt(ref / est1))
						} else {
							nrgGain[cc] = float32(math.Sqrt(ref * tmp / (est1 * math.Abs(float64(nf)+esbrGuard))))
						}
					}
					noiseLevel[cc] = float32(math.Sqrt(ref * tmp))
					cc++
				}
				m++
			}
			// limiter
			lim := &c.lim
			for cb := 0; cb < lim.gate[limBand]; cb++ {
				var pRef, pEst float32
				lo, hi := lim.table[limBand][cb], lim.table[limBand][cb+1]
				for k := lo; k < hi; k++ {
					pRef += nrgRef[k]
					pEst += nrgEst[k]
				}
				avgGain := float32(math.Sqrt(float64((pRef + esbrEPS) / (pEst + esbrEPS))))
				gMax := avgGain * esbrLimGains[limGains]
				if gMax > 1.0e5 {
					gMax = 1.0e5
				}
				for k := lo; k < hi; k++ {
					if gMax <= nrgGain[k] {
						noiseLevel[k] = float32(float64(noiseLevel[k]) * (float64(gMax) / (float64(nrgGain[k]) + esbrGuard)))
						nrgGain[k] = gMax
					}
				}
				var pAdj float32
				for k := lo; k < hi; k++ {
					pAdj += nrgGain[k] * nrgGain[k] * nrgEst[k]
					if nrgTone[k] != 0 {
						pAdj += nrgTone[k] * nrgTone[k]
					} else if !noiseAbsc {
						pAdj += noiseLevel[k] * noiseLevel[k]
					}
				}
				boost := float32(math.Sqrt(float64((pRef + esbrEPS) / (pAdj + esbrEPS))))
				if boost > 1.584893192 {
					boost = 1.584893192
				}
				for k := lo; k < hi; k++ {
					nrgGain[k] *= boost
					noiseLevel[k] *= boost
					nrgTone[k] *= boost
				}
			}
			if startUp {
				for n := 0; n < 4; n++ {
					copy(c.eGain[n][:numSub], nrgGain[:numSub])
					copy(c.noiseBuf[n][:numSub], noiseLevel[:numSub])
				}
				startUp = false
				startUpPVC = false
			}
			for l := rate * p.borderVec[i]; l < rate*p.borderVec[i+1]; l++ {
				rr := inRe[l][subStart:]
				ri := inIm[l][subStart:]
				for k := 0; k < numSub; k++ {
					c.eGain[4][k] = nrgGain[k]
					c.noiseBuf[4][k] = noiseLevel[k]
					var sbGain, sbNoise float32
					ci := 0
					for n := 4 - sl; n <= 4; n++ {
						sbGain += c.eGain[n][k] * filt[ci]
						sbNoise += c.noiseBuf[n][k] * filt[ci]
						ci++
					}
					c.phaseIndex = (c.phaseIndex + 1) & 511
					if nrgTone[k] != 0 || noiseAbsc {
						sbNoise = 0
					}
					rr[k] = rr[k]*sbGain + sbNoise*esbrRandomPhase[c.phaseIndex*2]
					ri[k] = ri[k]*sbGain + sbNoise*esbrRandomPhase[c.phaseIndex*2+1]
				}
				tmp := c.eGain[0]
				copy(c.eGain[0:4], c.eGain[1:5])
				c.eGain[4] = tmp
				tmpN := c.noiseBuf[0]
				copy(c.noiseBuf[0:4], c.noiseBuf[1:5])
				c.noiseBuf[4] = tmpN
			}
			off := rate * p.borderVec[i]
			if err := esbrApplyInterTes(lowRe, lowIm, inRe, inIm, off,
				rate*p.borderVec[i+1]-off, subStart, numSub, f.interTesMode[i]); err != nil {
				return err
			}
			for l := rate * p.borderVec[i]; l < rate*p.borderVec[i+1]; l++ {
				rr := inRe[l][subStart:]
				ri := inIm[l][subStart:]
				freqInv := float32(1)
				if subStart&1 != 0 {
					freqInv = -1
				}
				for k := 0; k < numSub; k++ {
					rr[k] += nrgTone[k] * esbrHPhase[c.harmIndex]
					ri[k] += nrgTone[k] * freqInv * esbrHPhase[4+c.harmIndex]
					freqInv = -freqInv
				}
				c.harmIndex = (c.harmIndex + 1) & 3
			}
		}
	}
	c.harmFlagVarlenPrev = c.harmFlagPrev
	for i := 0; i < 64; i++ {
		c.harmFlagVarlen[i] = harmonics[i]
	}
	for i := 0; i < 64-subStart; i++ {
		c.harmFlagPrev[subStart+i] = harmonics[i]
	}
	if transEnv == numEnv {
		c.envShortFlagPrev = 0
	} else {
		c.envShortFlagPrev = -1
	}
	f.prevInfo = f.info
	if f.info.numEnv == 1 {
		f.varLenIDPrev = 0
	} else if f.info.numEnv == 2 {
		f.varLenIDPrev = 1
	}
	if f.info.numNoiseEnv < 1 || f.info.numNoiseEnv > 2 {
		return fmt.Errorf("media: usac sbr noise envelope count")
	}
	for i := 0; i < nNf; i++ {
		f.prevNoise[i] = f.fltNoise[(f.info.numNoiseEnv-1)*nNf+i]
	}
	h.esbrStartUp = startUp
	h.esbrStartPVC = startUpPVC
	return nil
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}
