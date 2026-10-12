package media

import (
	"fmt"
	"math"
)

// eSBR high frequency generation (plain patching and the LPC based
// inverse filtering), after libxaac's ixheaacd_sbrdec_lpfuncs.c
// (Apache-2.0, see NOTICE). The arithmetic is single precision like the
// reference because the covariance method is ill conditioned.

type esbrPatch struct {
	num   int
	start [esbrMaxPatches + 1]int
}

type esbrAutoCorr struct {
	p01r, p01i, p02r, p02i, p11, p12r, p12i, p22, det float32
}

func esbrCoVariance(a *esbrAutoCorr, re, im [][64]float32, bd, ln, base int) {
	*a = esbrAutoCorr{}
	for j := 0; j < ln; j++ {
		r0, i0 := re[base+j][bd], im[base+j][bd]
		r1, i1 := re[base+j-1][bd], im[base+j-1][bd]
		r2, i2 := re[base+j-2][bd], im[base+j-2][bd]
		a.p01r += r0*r1 + i0*i1
		a.p01i += i0*r1 - r0*i1
		a.p02r += r0*r2 + i0*i2
		a.p02i += i0*r2 - r0*i2
		a.p11 += r1*r1 + i1*i1
		a.p12r += r1*r2 + i1*i2
		a.p12i += i1*r2 - r1*i2
		a.p22 += r2*r2 + i2*i2
	}
	const relax = float32(0.999999)
	a.det = a.p11*a.p22 - (a.p12r*a.p12r+a.p12i*a.p12i)*relax
}

// alphas computes the two LPC coefficients of a band.
func (a *esbrAutoCorr) alphas() (a0r, a0i, a1r, a1i float32) {
	if a.det == 0 {
		a1r, a1i = 0, 0
	} else {
		fac := float32(1) / a.det
		a1r = (a.p01r*a.p12r - a.p01i*a.p12i - a.p02r*a.p11) * fac
		a1i = (a.p01i*a.p12r + a.p01r*a.p12i - a.p02i*a.p11) * fac
	}
	if a.p11 == 0 {
		a0r, a0i = 0, 0
	} else {
		fac := float32(1) / a.p11
		a0r = -(a.p01r + a1r*a.p12r + a1i*a.p12i) * fac
		a0i = -(a.p01i + a1i*a.p12r - a1r*a.p12i) * fac
	}
	if a0r*a0r+a0i*a0i >= 16 || a1r*a1r+a1i*a1i >= 16 {
		a0r, a0i, a1r, a1i = 0, 0, 0, 0
	}
	return
}

func esbrGaussSolve(n int, a *[4][4]float32, b, y *[4]float32) {
	for i := 0; i < n; i++ {
		imax := i
		for k := i + 1; k < n; k++ {
			if math.Abs(float64(a[k][i])) > math.Abs(float64(a[imax][i])) {
				imax = k
			}
		}
		if imax != i {
			b[imax], b[i] = b[i], b[imax]
			for j := i; j < n; j++ {
				a[imax][j], a[i][j] = a[i][j], a[imax][j]
			}
		}
		v := a[i][i]
		b[i] /= v
		for j := i; j < n; j++ {
			a[i][j] /= v
		}
		for k := i + 1; k < n; k++ {
			v = a[k][i]
			b[k] -= v * b[i]
			for j := i + 1; j < n; j++ {
				a[k][j] -= v * a[i][j]
			}
		}
	}
	for i := n - 1; i >= 0; i-- {
		y[i] = b[i]
		for j := i + 1; j < n; j++ {
			y[i] -= a[i][j] * y[j]
		}
	}
}

func esbrPolyfit(n int, y []float32, p *[4]float32) {
	const maxdeg = 3
	var a [4][4]float32
	var b [4]float32
	var v [2*maxdeg + 1]float32
	for k := 0; k < n; k++ {
		v[0] = 1
		for i := 1; i <= 2*maxdeg; i++ {
			v[i] = float32(k) * v[i-1]
		}
		for i := 0; i <= maxdeg; i++ {
			b[i] += v[maxdeg-i] * y[k]
			for j := 0; j <= maxdeg; j++ {
				a[i][j] += v[2*maxdeg-i-j]
			}
		}
	}
	esbrGaussSolve(maxdeg+1, &a, &b, p)
}

// preProcessing is ixheaacd_pre_processing.
func esbrPreProcessing(re, im [][64]float32, gain []float32, numBands, start, end, base int) {
	var lowEnv [64]float32
	var meanEnrg float32
	if numBands != 0 && end != start {
		for k := 0; k < numBands; k++ {
			var temp float32
			for i := start; i < end; i++ {
				temp += re[base+i][k]*re[base+i][k] + im[base+i][k]*im[base+i][k]
			}
			temp /= float32(end - start)
			lowEnv[k] = float32(10 * math.Log10(float64(temp+1)))
			meanEnrg += lowEnv[k]
		}
		meanEnrg /= float32(numBands)
	}
	var poly [4]float32
	esbrPolyfit(numBands, lowEnv[:], &poly)
	a0, a1, a2, a3 := poly[0], poly[1], poly[2], poly[3]
	var slope [64]float32
	for k := 0; k < numBands; k++ {
		x := float32(k)
		s := a3
		s = s + a2*x
		x = x * x
		s = s + a1*x
		x = x * float32(k)
		s = s + a0*x
		slope[k] = s
	}
	for i := 0; i < numBands; i++ {
		gain[i] = float32(math.Pow(10, float64((meanEnrg-slope[i])/20)))
	}
}

func esbrFindClosest(goal int, tbl []int, n, dir int) int {
	if goal <= tbl[0] {
		return tbl[0]
	}
	if goal >= tbl[n] {
		return tbl[n]
	}
	idx := 0
	if dir != 0 {
		for tbl[idx] < goal {
			idx++
		}
	} else {
		idx = n
		for tbl[idx] > goal {
			idx--
		}
	}
	return tbl[idx]
}

// generateHF is ixheaacd_generate_hf. src is the QMF buffer of the core
// signal (index 0 is slot SBR_HF_ADJ_OFFSET of the buffer), dst receives the
// patched high band. ph is the transposer output (nil without HBE).
func (c *esbrChan) generateHF(f *esbrFrame, h *esbrHeader, srcRe, srcIm [][64]float32,
	phRe, phIm [][64]float32, dstRe, dstIm [][64]float32) error {
	const base = esbrHfAdjOffset
	var bwArr [esbrMaxPatches]float32
	invfTbl := h.noiseTbl[1:]
	numIf := h.nNf
	subStart := h.subStart
	fm := h.fMaster[:]
	numMF := h.numMF
	patchingMode := f.patchingMode
	preProc := h.preProc != 0
	fs := h.outFs
	lsb := fm[0]
	usb := fm[numMF]
	xoverOffset := subStart - fm[0]
	var gainVec [64]float32
	p := &f.info
	firstSlot := p.borderVec[0]
	endSlotOffs := p.borderVec[p.numEnv] - 16
	var start, end, covLen int
	if h.usf4 {
		start, end, covLen = firstSlot*4, 64+endSlotOffs*4, 76
	} else {
		start, end, covLen = firstSlot*2, 32+endSlotOffs*2, 38
	}
	if preProc {
		esbrPreProcessing(srcRe, srcIm, gainVec[:], fm[0], start, end, base)
	}
	// chirp factors
	for i := 0; i < numIf; i++ {
		b := esbrNewBw[f.invfModePrev[i]*4+f.invfMode[i]]
		if b < c.bwPrev[i] {
			b = 0.75*b + 0.25*c.bwPrev[i]
		} else {
			b = 0.90625*b + 0.09375*c.bwPrev[i]
		}
		if b < 0.015625 {
			b = 0
		}
		bwArr[i] = b
	}
	for i := start; i < end; i++ {
		for k := usb; k < 64; k++ {
			dstRe[base+i][k] = 0
			dstIm[base+i][k] = 0
		}
	}
	patch := 0
	if patchingMode != 0 || !h.hbe {
		var alphaRe, alphaIm [64][2]float32
		covCount := fm[0]
		var ac esbrAutoCorr
		for k := 1; k < covCount; k++ {
			esbrCoVariance(&ac, srcRe, srcIm, k, covLen, base)
			a0r, a0i, a1r, a1i := ac.alphas()
			alphaRe[k][0], alphaIm[k][0], alphaRe[k][1], alphaIm[k][1] = a0r, a0i, a1r, a1i
		}
		goalSb := int(2.048e6/float32(fs) + 0.5)
		if goalSb < fm[numMF] {
			idx := 0
			for fm[idx] < goalSb {
				idx++
			}
			goalSb = fm[idx]
		} else {
			goalSb = fm[numMF]
		}
		sourceStart := xoverOffset + 1
		sb := lsb + xoverOffset
		flagBreak := false
		for sb < usb {
			if patch >= esbrMaxPatches {
				return fmt.Errorf("media: usac sbr too many patches")
			}
			c.patch.start[patch] = sb
			numBands := goalSb - sb
			if numBands >= lsb-sourceStart {
				stride := sb - sourceStart
				stride &^= 1
				numBands = lsb - (sb - stride)
				numBands = esbrFindClosest(sb+numBands, fm, numMF, 0) - sb
			}
			stride := numBands + sb - lsb
			stride = (stride + 1) &^ 1
			sourceStart = 1
			if goalSb-(sb+numBands) < 3 {
				goalSb = usb
			}
			if numBands < 3 && patch > 0 && sb+numBands == usb {
				for i := start; i < end; i++ {
					for k2 := sb; k2 < sb+numBands; k2++ {
						dstRe[base+i][k2] = 0
						dstIm[base+i][k2] = 0
					}
				}
				break
			}
			if numBands < 0 && flagBreak {
				break
			}
			if numBands < 0 {
				flagBreak = true
				continue
			}
			flagBreak = false
			for k2 := sb; k2 < sb+numBands; k2++ {
				k := k2 - stride
				bwIndex := 0
				for k2 >= invfTbl[bwIndex] {
					bwIndex++
					if bwIndex >= esbrMaxNoiseCoeffs {
						return fmt.Errorf("media: usac sbr bad noise table")
					}
				}
				bw := bwArr[bwIndex]
				a0r := bw * alphaRe[k][0]
				a0i := bw * alphaIm[k][0]
				bw *= bw
				a1r := bw * alphaRe[k][1]
				a1i := bw * alphaIm[k][1]
				gain := float32(1)
				if preProc {
					gain = gainVec[k]
				}
				for i := start; i < end; i++ {
					dr := srcRe[base+i][k] * gain
					di := srcIm[base+i][k] * gain
					if bw > 0 {
						dr += (a0r*srcRe[base+i-1][k] - a0i*srcIm[base+i-1][k] +
							a1r*srcRe[base+i-2][k] - a1i*srcIm[base+i-2][k]) * gain
						di += (a0i*srcRe[base+i-1][k] + a0r*srcIm[base+i-1][k] +
							a1i*srcRe[base+i-2][k] + a1r*srcIm[base+i-2][k]) * gain
					}
					dstRe[base+i][k2] = dr
					dstIm[base+i][k2] = di
				}
			}
			sb += numBands
			patch++
		}
	}
	if phRe != nil && h.hbe && patchingMode == 0 {
		var ac esbrAutoCorr
		bwIndex := 0
		patch = 1
		for k2 := subStart; k2 < fm[numMF]; k2++ {
			esbrCoVariance(&ac, phRe, phIm, k2, covLen, base)
			a0r, a0i, a1r, a1i := ac.alphas()
			for k2 >= invfTbl[bwIndex] {
				bwIndex++
				if bwIndex >= esbrMaxNoiseCoeffs {
					return fmt.Errorf("media: usac sbr bad noise table")
				}
			}
			bw := bwArr[bwIndex]
			b0r := bw * a0r
			b0i := bw * a0i
			bw *= bw
			b1r := bw * a1r
			b1i := bw * a1i
			if bw > 0 {
				for i := start; i < end; i++ {
					tr := phRe[base+i][k2]
					ti := phIm[base+i][k2]
					r1, i1 := phRe[base+i-1][k2], phIm[base+i-1][k2]
					r2, i2 := phRe[base+i-2][k2], phIm[base+i-2][k2]
					tr += (b0r*r1 - b0i*i1) + (b1r*r2 - b1i*i2)
					ti += (b0i*r1 + b0r*i1) + (b1i*r2 + b1r*i2)
					dstRe[base+i][k2] = tr
					dstIm[base+i][k2] = ti
				}
			} else {
				for i := start; i < end; i++ {
					dstRe[base+i][k2] = phRe[base+i][k2]
					dstIm[base+i][k2] = phIm[base+i][k2]
				}
			}
		}
	}
	if patch > esbrMaxPatches {
		return fmt.Errorf("media: usac sbr too many patches")
	}
	c.patch.num = patch
	for i := 0; i < numIf; i++ {
		c.bwPrev[i] = bwArr[i]
	}
	return nil
}
