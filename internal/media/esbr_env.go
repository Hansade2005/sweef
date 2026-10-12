package media

import "math"

// eSBR envelope and noise floor decoding (delta decoding and dequantisation),
// after libxaac's ixheaacd_env_dec.c (Apache-2.0, see NOTICE).

func esbrMapResEnergy(cur int, prev []int, offset, index, res int) {
	if res == 0 { // LOW
		if offset >= 0 {
			if index < offset {
				prev[index] = cur
			} else {
				i2 := index + index - offset
				prev[i2] = cur
				prev[i2+1] = cur
			}
		} else {
			offset = -offset
			if index < offset {
				i3 := index + index + index
				prev[i3] = cur
				prev[i3+1] = cur
				prev[i3+2] = cur
			} else {
				i2 := index + index + offset
				prev[i2] = cur
				prev[i2+1] = cur
			}
		}
	} else {
		prev[index] = cur
	}
}

func imin(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// processDelCodEnvData is ixheaacd_process_del_cod_env_data.
func (e *esbrElem) processDelCodEnvData(f *esbrFrame, pd *esbrPrevData) {
	h := &e.hdr
	prev := pd.sfbNrgPrev[:]
	pos := 0
	offset := (h.nSf[0] << 1) - h.nSf[1]
	for i := 0; i < f.info.numEnv; i++ {
		dir := f.delCodDir[i]
		fr := f.info.freqRes[i]
		nb := h.nSf[fr]
		if dir == esbrDirFreq {
			esbrMapResEnergy(f.intEnv[pos], prev, offset, 0, fr)
			p0 := pos
			pos++
			for band := 1; band < nb; band++ {
				f.intEnv[pos] += f.intEnv[pos-1]
				esbrMapResEnergy(f.intEnv[pos], prev, offset, band, fr)
				f.fltEnv[pos] = float32(f.intEnv[pos])
				pos++
			}
			_ = p0
		} else if fr == 0 {
			if offset < 0 {
				offset = -offset
				tar := imin(offset, nb)
				band := 0
				for ; band < tar; band++ {
					i3 := 3 * band
					t := f.intEnv[pos] + prev[i3]
					prev[i3], prev[i3+1], prev[i3+2] = t, t, t
					f.intEnv[pos] = t
					f.fltEnv[pos] = float32(t)
					pos++
				}
				for ; band < nb; band++ {
					i3 := band<<1 + offset
					t := f.intEnv[pos] + prev[i3]
					prev[i3], prev[i3+1] = t, t
					f.intEnv[pos] = t
					f.fltEnv[pos] = float32(t)
					pos++
				}
			} else {
				tar := imin(offset, nb)
				band := 0
				for ; band < tar; band++ {
					f.intEnv[pos] += prev[band]
					prev[band] = f.intEnv[pos]
					f.fltEnv[pos] = float32(f.intEnv[pos])
					pos++
				}
				for ; band < nb; band++ {
					idx := band
					if band >= offset {
						idx = band<<1 - offset
					}
					t := f.intEnv[pos] + prev[idx]
					prev[idx], prev[idx+1] = t, t
					f.intEnv[pos] = t
					f.fltEnv[pos] = float32(t)
					pos++
				}
			}
		} else {
			for band := 0; band < nb; band++ {
				f.intEnv[pos] += prev[band]
				prev[band] = f.intEnv[pos]
				f.fltEnv[pos] = float32(f.intEnv[pos])
				pos++
			}
		}
	}
}

// calcNoiseFloor is ixheaacd_calc_noise_floor (the USAC / enh_sbr path).
func (e *esbrElem) calcNoiseFloor(f *esbrFrame, pd *esbrPrevData) {
	nb := e.hdr.nNf
	nenv := f.info.numNoiseEnv
	nf := f.intNoise[:]
	if f.delCodDirNoise[0] == esbrDirFreq {
		for i := 1; i < nb; i++ {
			nf[i] += nf[i-1]
		}
	} else {
		for i := 0; i < nb; i++ {
			nf[i] += pd.prevNoise[i]
		}
	}
	if nenv > 1 {
		if f.delCodDirNoise[1] == esbrDirFreq {
			for i := 1; i < nb; i++ {
				nf[nb+i] += nf[nb+i-1]
			}
		} else {
			for i := 0; i < nb; i++ {
				nf[nb+i] += nf[i]
			}
		}
	}
	for i := 0; i < nb*nenv; i++ {
		if nf[i] > 35 {
			nf[i] = 35
		} else if nf[i] < 0 {
			nf[i] = 0
		}
	}
	copy(pd.prevNoise[:nb], nf[nb*(nenv-1):nb*nenv])
	for i := 0; i < nb*nenv; i++ {
		f.fltNoise[i] = float32(nf[i])
	}
}

func (e *esbrElem) dequantEnv(f *esbrFrame) {
	a := float32(0.5)
	if f.ampRes == 1 {
		a = 1.0
	}
	for i := 0; i < f.numEnvSfac; i++ {
		f.fltEnv[i] = float32(math.Pow(2, float64(f.fltEnv[i]*a)) * 64)
	}
	for i := 0; i < f.numNoiseSfac; i++ {
		t := float32(6) - f.fltNoise[i]
		f.fltNoise[i] = float32(math.Pow(2, float64(t)))
	}
}

func (e *esbrElem) dequantNoiseOnly(f *esbrFrame) {
	for i := 0; i < f.numNoiseSfac; i++ {
		t := float32(6) - f.fltNoise[i]
		f.fltNoise[i] = float32(math.Pow(2, float64(t)))
	}
}

// dequantCoupling is ixheaacd_sbr_env_dequant_coup.
func (e *esbrElem) dequantCoupling(l, r *esbrFrame) {
	panOff := [2]float32{24, 12}
	aArr := [2]float32{0.5, 1.0}
	amp := l.ampRes
	a := aArr[amp]
	for i := 0; i < l.numEnvSfac; i++ {
		tl, tr := l.fltEnv[i], r.fltEnv[i]
		l.fltEnv[i] = float32(64 * (math.Pow(2, float64(tl*a+1)) / (1 + math.Pow(2, float64((panOff[amp]-tr)*a)))))
		r.fltEnv[i] = float32(64 * (math.Pow(2, float64(tl*a+1)) / (1 + math.Pow(2, float64((tr-panOff[amp])*a)))))
	}
	for i := 0; i < l.numNoiseSfac; i++ {
		tl, tr := l.fltNoise[i], r.fltNoise[i]
		l.fltNoise[i] = float32(math.Pow(2, float64(6-tl+1)) / (1 + math.Pow(2, float64(panOff[1]-tr))))
		r.fltNoise[i] = float32(math.Pow(2, float64(6-tl+1)) / (1 + math.Pow(2, float64(tr-panOff[1]))))
	}
}

// decode is ixheaacd_dec_sbrdata for one element (USAC path).
func (e *esbrElem) decode(f [2]*esbrFrame, pd [2]*esbrPrevData, nch int) {
	h := &e.hdr
	for c := 0; c < nch; c++ {
		fr := f[c]
		if fr.sbrMode == esbrPVCMode {
			// PVC: the envelope is predicted, only the noise floor is decoded
			e.calcNoiseFloor(fr, pd[c])
			if fr.couplingMode == 0 {
				fr.numNoiseSfac = h.nNf * fr.info.numNoiseEnv
				e.dequantNoiseOnly(fr)
			}
			continue
		}
		e.processDelCodEnvData(fr, pd[c])
		e.calcNoiseFloor(fr, pd[c])
		if fr.couplingMode == 0 {
			fr.numNoiseSfac = h.nNf * fr.info.numNoiseEnv
			e.dequantEnv(fr)
		}
	}
	if nch == 2 && f[0].couplingMode != 0 {
		f[0].numNoiseSfac = h.nNf * f[1].info.numNoiseEnv
		e.dequantCoupling(f[0], f[1])
	}
}
