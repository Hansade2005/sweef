package media

import (
	"fmt"
	"math"
)

// Predictive vector coding (PVC) of the eSBR envelope, after libxaac's
// ixheaacd_pred_vec_block.c, ixheaacd_env_extr.c and ixheaacd_esbr_envcal.c
// (Apache-2.0, Ittiam Systems, see NOTICE).

const (
	pvcNumTimeSlots  = 16
	pvcNbLow         = 3
	pvcEsgMinVal     = float32(0.1)
	pvc10Log10EsgMin = float32(-10.0)
	escSinPos        = 31
)

// esbrPVCData is ia_pvc_data_struct.
type esbrPVCData struct {
	pvcMode, divMode, nsMode int
	numTimeSlots             int
	nbHigh, nbHighPerGrp     int
	prevPVCFlg               bool
	pvcRate, prevPVCRate     int
	pvcID                    [pvcNumTimeSlots]int
	prevPVCID                int
	prevFirstBndIdx          int
	esg                      [2*pvcNumTimeSlots - 1][3]float32
	smoothEsg                [pvcNumTimeSlots * 3]float32
	rangeEsg                 [pvcNumTimeSlots * 8]float32
}

func newESBRPVCData() *esbrPVCData {
	return &esbrPVCData{prevFirstBndIdx: -1, prevPVCRate: -1}
}

// readPVCEnvelope is ixheaacd_read_esbr_pvc_envelope.
func (e *esbrElem) readPVCEnvelope(r *aacBits, indep bool) error {
	pd := e.pvc
	divMode := r.bits(3)
	nsMode := r.bits(1)
	idBits := 7
	if pd.pvcMode == 3 {
		idBits = 0
	}
	var id [pvcNumTimeSlots + 1]int
	if divMode <= 3 {
		numLength := divMode
		reuse := 0
		if !indep {
			reuse = r.bits(1)
		}
		if reuse == 1 {
			id[0] = pd.prevPVCID
		} else {
			id[0] = r.bits(idBits)
		}
		k := 1
		if numLength != 0 {
			sum := 0
			for i := 0; i < numLength; i++ {
				lb := 4
				switch {
				case sum >= 13:
					lb = 1
				case sum >= 11:
					lb = 2
				case sum >= 7:
					lb = 3
				}
				length := r.bits(lb) + 1
				sum += length
				if k+length-1 > pvcNumTimeSlots {
					return fmt.Errorf("media: usac pvc length")
				}
				for j := 1; j < length; j++ {
					id[k] = id[k-1]
					k++
				}
				id[k] = r.bits(idBits)
				k++
			}
		}
		for ; k < 16; k++ {
			id[k] = id[k-1]
		}
	} else {
		var numGrid, fixedLen int
		switch divMode {
		case 4:
			numGrid, fixedLen = 2, 8
		case 5:
			numGrid, fixedLen = 4, 4
		case 6:
			numGrid, fixedLen = 8, 2
		case 7:
			numGrid, fixedLen = 16, 1
		}
		gridInfo := 1
		if !indep {
			gridInfo = r.bits(1)
		}
		if gridInfo != 0 {
			id[0] = r.bits(idBits)
		} else {
			id[0] = pd.prevPVCID
		}
		k := 1
		for j := 1; j < fixedLen; j++ {
			id[k] = id[k-1]
			k++
		}
		for i := 1; i < numGrid; i++ {
			if r.bits(1) == 1 {
				id[k] = r.bits(idBits)
			} else {
				id[k] = id[k-1]
			}
			k++
			for j := 1; j < fixedLen; j++ {
				id[k] = id[k-1]
				k++
			}
		}
	}
	pd.divMode, pd.nsMode = divMode, nsMode
	copy(pd.pvcID[:], id[:pvcNumTimeSlots])
	return nil
}

// readPVCAddi is ixheaacd_read_sbr_addi_data.
func (e *esbrElem) readPVCAddi(r *aacBits, f *esbrFrame) {
	flag := r.bit()
	f.sinStartCur, f.sinLenCur = f.sinStartNext, f.sinLenNext
	f.sinStartNext, f.sinLenNext = 0, 0
	if !flag {
		return
	}
	for i := 0; i < e.hdr.nSf[1]; i++ {
		f.addHarmonics[i] = r.bits(1)
	}
	if e.hdr.pvcMode != 0 {
		f.sinePosition = escSinPos
		if r.bit() {
			f.sinePosition = r.bits(5)
		}
		if f.varLen > 0 {
			if f.sinePosition > 16 {
				if f.sinePosition == 31 {
					f.sinStartNext, f.sinLenNext = 0, f.varLen
				} else if f.varLen+16 == f.sinePosition {
					f.sinStartNext, f.sinLenNext = 0, f.varLen
				} else {
					f.sinStartNext, f.sinLenNext = f.sinePosition-16, f.varLen
				}
			} else {
				f.sinStartNext, f.sinLenNext = 0, f.varLen
			}
		}
	}
}

// readPVCSCE is ixheaacd_sbr_read_pvc_sce.
func (e *esbrElem) readPVCSCE(r *aacBits, f *esbrFrame, indep bool) error {
	h := &e.hdr
	if h.hbe {
		readESBRPatching(r, f)
	}
	f.numTimeSlots = 16
	if !e.pvcTimeFreqGrid(r, f) {
		return fmt.Errorf("media: usac sbr bad pvc frame grid")
	}
	// pvc dtdf: only the noise floor direction is transmitted
	if indep {
		f.delCodDirNoise[0] = 0
	} else {
		f.delCodDirNoise[0] = r.bits(1)
	}
	for i := 1; i < f.info.numNoiseEnv; i++ {
		f.delCodDirNoise[i] = r.bits(1)
	}
	for i := 0; i < h.nNf; i++ {
		f.invfModePrev[i] = f.invfMode[i]
		f.invfMode[i] = r.bits(2)
	}
	e.pvc.pvcMode = h.pvcMode
	if err := e.readPVCEnvelope(r, indep); err != nil {
		return err
	}
	e.readNoise(r, f)
	for i := range f.addHarmonics {
		f.addHarmonics[i] = 0
	}
	f.pvcMode = h.pvcMode
	e.readPVCAddi(r, f)
	f.couplingMode = esbrCouplingOff
	if r.err {
		return fmt.Errorf("media: usac sbr truncated")
	}
	return nil
}

// qmfEnrgCalc is ixheaacd_qmf_enrg_calc: the low band energies of the
// frame, averaged to the 16 PVC time slots.
func esbrQmfEnrgCalc(qRe, qIm [][64]float32, slots int, usf4 bool, out []float32) {
	var ener [64][32]float32
	nb := 32
	if usf4 {
		nb = 16
	}
	for i := 0; i < slots; i++ {
		for j := 0; j < nb; j++ {
			ener[i][j] = qRe[2+i][j] * qRe[2+i][j]
			ener[i][j] += qIm[2+i][j] * qIm[2+i][j]
		}
	}
	for i := 0; i < 16; i++ {
		for j := 0; j < nb; j++ {
			if usf4 {
				out[32*i+j] = (ener[4*i+0][j] + ener[4*i+1][j] + ener[4*i+2][j] + ener[4*i+3][j]) * 0.25
			} else {
				out[32*i+j] = (ener[2*i+0][j] + ener[2*i+1][j]) * 0.5
			}
		}
	}
}

// process is ixheaacd_pvc_process.
func (pd *esbrPVCData) process(firstBnd, firstSlot int, qmfEner []float32, qmfh []float32) error {
	var predTab1, predTab2 []uint8
	var idBound []uint8
	var qfac []float32
	var smooth []float32
	switch pd.pvcMode {
	case 1:
		pd.nbHigh = 8
		pd.nbHighPerGrp = 8 / pd.pvcRate
		predTab1, predTab2 = esbrPvcPred1Mode1[:], esbrPvcPred2Mode1[:]
		idBound = esbrPvcIDBound1[:]
		qfac = esbrPvcQFac1[:]
		if pd.nsMode != 0 {
			pd.numTimeSlots, smooth = 4, esbrPvcSmoothNs4[:]
		} else {
			pd.numTimeSlots, smooth = 16, esbrPvcSmoothNs16[:]
		}
	case 2:
		pd.nbHigh = 6
		pd.nbHighPerGrp = 12 / pd.pvcRate
		predTab1, predTab2 = esbrPvcPred1Mode2[:], esbrPvcPred2Mode2[:]
		idBound = esbrPvcIDBound2[:]
		qfac = esbrPvcQFac2[:]
		if pd.nsMode != 0 {
			pd.numTimeSlots, smooth = 3, esbrPvcSmoothNs3[:]
		} else {
			pd.numTimeSlots, smooth = 12, esbrPvcSmoothNs12[:]
		}
	default:
		return fmt.Errorf("%w: usac pvc mode %d", ErrUnsupported, pd.pvcMode)
	}
	pd.prevPVCID = pd.pvcID[pvcNumTimeSlots-1]

	// QMF grouping
	lbw := 8 / pd.pvcRate
	for ts := 0; ts < pvcNumTimeSlots; ts++ {
		for ksg := 0; ksg < pvcNbLow; ksg++ {
			start := firstBnd - lbw*pvcNbLow + lbw*ksg
			end := start + lbw - 1
			var esg float32
			if start >= 0 {
				for ib := start; ib <= end; ib++ {
					esg += qmfEner[ts*32+ib]
				}
				esg = esg / float32(lbw)
			} else {
				esg = pvcEsgMinVal
			}
			if esg > pvcEsgMinVal {
				pd.esg[ts+16-1][ksg] = 10 * float32(math.Log10(float64(esg)))
			} else {
				pd.esg[ts+16-1][ksg] = pvc10Log10EsgMin
			}
		}
	}
	if !pd.prevPVCFlg || firstBnd*pd.pvcRate != pd.prevFirstBndIdx*pd.prevPVCRate {
		for ts := 0; ts < 16-1+firstSlot; ts++ {
			for ksg := 0; ksg < pvcNbLow; ksg++ {
				pd.esg[ts][ksg] = pd.esg[16-1+firstSlot][ksg]
			}
		}
	}
	// time smoothing
	for ts := 0; ts < pvcNumTimeSlots; ts++ {
		var sm [3]float32
		for j := 0; j < pd.numTimeSlots; j++ {
			for ksg := pvcNbLow - 1; ksg >= 0; ksg-- {
				sm[ksg] += pd.esg[ts+16-1-j][ksg] * smooth[j]
			}
		}
		copy(pd.smoothEsg[ts*3:], sm[:])
	}
	// prediction of the envelope scale factors
	for ts := 0; ts < pvcNumTimeSlots; ts++ {
		idx2 := pd.pvcID[ts]
		var idx1 int
		switch {
		case idx2 < int(idBound[0]):
			idx1 = 0
		case idx2 < int(idBound[1]):
			idx1 = 1
		default:
			idx1 = 2
		}
		p1 := predTab1[idx1*pvcNbLow*pd.nbHigh:]
		p2 := predTab2[idx2*pd.nbHigh:]
		rng := pd.rangeEsg[ts*8:]
		for ksg := 0; ksg < pd.nbHigh; ksg++ {
			rng[ksg] = float32(int8(p2[ksg])) * qfac[pvcNbLow]
		}
		pi := 0
		for kb := 0; kb < pvcNbLow; kb++ {
			for ksg := 0; ksg < pd.nbHigh; ksg++ {
				t := float32(int8(p1[pi])) * qfac[kb]
				pi++
				rng[ksg] += t * pd.smoothEsg[ts*3+kb]
			}
		}
	}
	// subband parsing
	for ts := 0; ts < pvcNumTimeSlots; ts++ {
		start := firstBnd
		end := start + pd.nbHighPerGrp - 1
		rng := pd.rangeEsg[ts*8:]
		for ksg := 0; ksg < pd.nbHigh; ksg++ {
			for k := start; k <= end; k++ {
				qmfh[ts*64+k] = float32(math.Pow(10.0, float64(rng[ksg])/10.0))
			}
			start += pd.nbHighPerGrp
			if ksg >= pd.nbHigh-2 {
				end = 63
			} else {
				end = start + pd.nbHighPerGrp - 1
				if end >= 63 {
					end = 63
				}
			}
		}
	}
	for i := 0; i < pvcNumTimeSlots-1; i++ {
		pd.esg[i] = pd.esg[pvcNumTimeSlots+i]
	}
	return nil
}

// envCalcPVC is the PVC branch of ixheaacd_sbr_env_calc.
func (c *esbrChan) envCalcPVC(f *esbrFrame, h *esbrHeader, inRe, inIm, lowRe, lowIm [][64]float32, pvcOut []float32, harmonics *[64]int8, startUp, startUpPVC *bool) error {
	rate := 2
	if h.usf4 {
		rate = 4
	}
	p := &f.info
	pvcInfo := &f.pvcInfo
	subStart, subEnd := h.subStart, h.subEnd
	numSub := subEnd - subStart
	smoothingLen := 4
	if h.smoothingMode != 0 {
		smoothingLen = 0
	}
	intMode := h.interpolFreq != 0
	limBand, limGains := h.limiterBands, h.limiterGains
	numEnv := p.numEnv
	transEnv := p.transientEnv
	nNf := h.nNf
	for i := 0; i < h.nSf[1]; i++ {
		li, ui := h.fHigh[i], h.fHigh[i+1]
		t := (ui + li - subStart<<1) >> 1
		if t >= 64 || t < 0 {
			return fmt.Errorf("media: usac sbr harmonic index")
		}
		harmonics[t] = int8(f.addHarmonics[i])
	}
	for t := 0; t < p.borderVec[0]; t++ {
		for cc := 0; cc < 64; cc++ {
			c.qmapped[cc][t] = c.qmapped[cc][t+16]
		}
	}
	tbl := func(fr int) []int {
		if fr != 0 {
			return h.fHigh[:]
		}
		return h.fLow[:]
	}
	// quantised noise floor per band and time slot
	kk, next := 0, -1
	for i := 0; i < numEnv; i++ {
		if kk > esbrMaxNoiseEnv {
			return fmt.Errorf("media: usac sbr noise envelope")
		}
		if p.borderVec[i] == p.noiseBorderVec[kk] {
			kk++
			next++
		}
		start, end := p.borderVec[i], p.borderVec[i+1]
		if start < 0 || end > 48 {
			return fmt.Errorf("media: usac sbr pvc border")
		}
		for t := start; t < end; t++ {
			fr := p.freqRes[i]
			o := 0
			cc := 0
			for j := 0; j < h.nSf[fr]; j++ {
				li, ui := tbl(fr)[j], tbl(fr)[j+1]
				ui2 := h.noiseTbl[o+1]
				for k := 0; k < ui-li; k++ {
					if k+li >= ui2 {
						o++
					}
					if o >= esbrMaxNoiseCoeffs {
						return fmt.Errorf("media: usac sbr noise band")
					}
					ui2 = h.noiseTbl[o+1]
					c.qmapped[cc][t] = f.fltNoise[next*nNf+o]
					cc++
				}
			}
		}
	}
	kk, next = 0, -1
	var envTmp, nrgRef, nrgEst, nrgGain, nrgTone, noiseLevel [64][48]float32
	lim := &c.lim
	for i := 0; i < numEnv; i++ {
		if kk > esbrMaxNoiseEnv {
			return fmt.Errorf("media: usac sbr noise envelope")
		}
		if p.borderVec[i] == p.noiseBorderVec[kk] {
			kk++
			next++
		}
		start, end := pvcInfo.borderVec[i], pvcInfo.borderVec[i+1]
		if start < 0 || end > 48 {
			return fmt.Errorf("media: usac sbr pvc border")
		}
		for t := start; t < end; t++ {
			for cc := 0; cc < 64; cc++ {
				envTmp[cc][t] = pvcOut[64*t+cc]
			}
		}
		noiseAbsc := i == transEnv || i == c.envShortFlagPrev
		if f.prevSbrMode == esbrOrig {
			noiseAbsc = false
		}
		sl := smoothingLen
		if noiseAbsc {
			sl = 0
		}
		filt := esbrFir(sl)

		// adjustment of one time slot; withVarlen selects the variant used
		// for the slots still covered by the sinusoids of the previous frame.
		adjust := func(t int, fr int, withVarlen bool) error {
			bt := tbl(fr)
			cc, o := 0, 0
			for j := 0; j < h.nSf[fr]; j++ {
				li, ui := bt[j], bt[j+1]
				ui2 := h.noiseTbl[o+1]
				flag := false
				for k := li; k < ui; k++ {
					if withVarlen {
						if c.harmFlagVarlen[cc] != 0 && (t >= f.sinStartCur || c.harmFlagVarlenPrev[cc+subStart] != 0) {
							flag = true
						}
					} else {
						if harmonics[cc] != 0 && (t >= f.sinePosition || c.harmFlagPrev[cc+subStart] != 0) {
							flag = true
						}
					}
					nrgRef[cc][t] = envTmp[k][t]
					var nrg float32
					for l := 0; l < rate; l++ {
						nrg += inRe[rate*t+l][k]*inRe[rate*t+l][k] + inIm[rate*t+l][k]*inIm[rate*t+l][k]
					}
					nrgEst[cc][t] = nrg / float32(rate)
					cc++
				}
				var nrgAvg float32
				if !intMode && ui != li {
					for k := cc - (ui - li); k < cc; k++ {
						nrgAvg += nrgEst[k][t]
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
					if !intMode {
						nrgEst[cc][t] = nrgAvg
					}
					nrgTone[cc][t] = 0
					q := c.qmapped[cc][t]
					tmp := float64(q) / (float64(1+q) + esbrGuard)
					ref := float64(nrgRef[cc][t])
					est1 := float64(nrgEst[cc][t] + 1)
					if flag {
						nrgGain[cc][t] = float32(math.Sqrt(ref * tmp / est1))
						if harmonics[cc] != 0 && (t >= f.sinePosition || c.harmFlagPrev[cc+subStart] != 0) {
							nrgTone[cc][t] = float32(math.Sqrt(ref * tmp / (float64(q) + esbrGuard)))
						}
						if withVarlen && c.harmFlagVarlen[cc] != 0 && (t >= f.sinStartCur || c.harmFlagVarlenPrev[cc+subStart] != 0) {
							nrgTone[cc][t] = float32(math.Sqrt(ref * tmp / (float64(f.prevNoise[o]) + esbrGuard)))
						}
					} else {
						if noiseAbsc {
							nrgGain[cc][t] = float32(math.Sqrt(ref / est1))
						} else {
							nrgGain[cc][t] = float32(math.Sqrt(ref * tmp / (est1 * (float64(q) + esbrGuard))))
						}
					}
					noiseLevel[cc][t] = float32(math.Sqrt(ref * tmp))
					cc++
				}
			}
			for cb := 0; cb < lim.gate[limBand]; cb++ {
				var pRef, pEst, pAdj float32
				lo, hi := lim.table[limBand][cb], lim.table[limBand][cb+1]
				for k := lo; k < hi; k++ {
					pRef += nrgRef[k][t]
					pEst += nrgEst[k][t]
				}
				avgGain := float32(math.Sqrt(float64((pRef + esbrEPS) / (pEst + esbrEPS))))
				gMax := avgGain * esbrLimGains[limGains]
				if gMax > 1.0e5 {
					gMax = 1.0e5
				}
				for k := lo; k < hi; k++ {
					if gMax <= nrgGain[k][t] {
						noiseLevel[k][t] = float32(float64(noiseLevel[k][t]) * (float64(gMax) / (float64(nrgGain[k][t]) + esbrGuard)))
						nrgGain[k][t] = gMax
					}
					pAdj += nrgGain[k][t] * nrgGain[k][t] * nrgEst[k][t]
					if nrgTone[k][t] != 0 {
						pAdj += nrgTone[k][t] * nrgTone[k][t]
					} else if !noiseAbsc {
						pAdj += noiseLevel[k][t] * noiseLevel[k][t]
					}
				}
				boost := float32(math.Sqrt(float64((pRef + esbrEPS) / (pAdj + esbrEPS))))
				if boost > 1.584893192 {
					boost = 1.584893192
				}
				for k := lo; k < hi; k++ {
					nrgGain[k][t] *= boost
					noiseLevel[k][t] *= boost
					nrgTone[k][t] *= boost
				}
			}
			return nil
		}

		t := start
		prevFr := f.prevInfo.freqRes[f.varLenIDPrev]
		for ; t < f.sinLenCur; t++ {
			if err := adjust(t, prevFr, true); err != nil {
				return err
			}
		}
		for ; t < end; t++ {
			if err := adjust(t, pvcInfo.freqRes[i], false); err != nil {
				return err
			}
		}
		if *startUpPVC {
			for n := 0; n < 4; n++ {
				for cc := 0; cc < numSub; cc++ {
					c.eGain[n][cc] = nrgGain[cc][start]
					c.noiseBuf[n][cc] = noiseLevel[cc][start]
				}
			}
			*startUpPVC = false
			*startUp = false
		}
		for l := rate * start; l < rate*end; l++ {
			rr := inRe[l][subStart:]
			ri := inIm[l][subStart:]
			slot := l / rate
			freqInv := float32(1)
			if subStart&1 != 0 {
				freqInv = -1
			}
			for k := 0; k < numSub; k++ {
				c.eGain[4][k] = nrgGain[k][slot]
				c.noiseBuf[4][k] = noiseLevel[k][slot]
				var sbGain, sbNoise float32
				ci := 0
				for n := 4 - sl; n <= 4; n++ {
					sbGain += c.eGain[n][k] * filt[ci]
					sbNoise += c.noiseBuf[n][k] * filt[ci]
					ci++
				}
				c.phaseIndex = (c.phaseIndex + 1) & 511
				if nrgTone[k][slot] != 0 || noiseAbsc {
					sbNoise = 0
				}
				rr[k] = rr[k]*sbGain + sbNoise*esbrRandomPhase[c.phaseIndex*2] + nrgTone[k][slot]*esbrHPhase[c.harmIndex]
				ri[k] = ri[k]*sbGain + sbNoise*esbrRandomPhase[c.phaseIndex*2+1] + nrgTone[k][slot]*freqInv*esbrHPhase[4+c.harmIndex]
				freqInv = -freqInv
			}
			c.harmIndex = (c.harmIndex + 1) & 3
			tmp := c.eGain[0]
			copy(c.eGain[0:4], c.eGain[1:5])
			c.eGain[4] = tmp
			tmpN := c.noiseBuf[0]
			copy(c.noiseBuf[0:4], c.noiseBuf[1:5])
			c.noiseBuf[4] = tmpN
		}
	}
	return nil
}
