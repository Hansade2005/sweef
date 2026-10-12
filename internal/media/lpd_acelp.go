package media

import "math"

// ACELP excitation decoding (ISO/IEC 23003-3 7.16): algebraic codebook pulse
// positions, adaptive codebook, gains. After libxaac's ixheaacd_acelp_decode.c
// (Apache-2.0, see NOTICE).

func lpdAddPulse1(idx, m, off, track int, code []float64) {
	mask := (1 << uint(m)) - 1
	pos := (idx & mask) + off
	sign := (idx >> uint(m)) & 1
	k := pos<<2 + track
	if sign == 1 {
		code[k]--
	} else {
		code[k]++
	}
}

func lpdAddPulse2(idx, m, off, track int, code []float64) {
	mask := (1 << uint(m)) - 1
	p0 := ((idx >> uint(m)) & mask) + off
	p1 := (idx & mask) + off
	sign := (idx >> uint(2*m)) & 1
	m0 := p0<<2 + track
	m1 := p1<<2 + track
	if p1-p0 < 0 {
		if sign == 1 {
			code[m0]--
			code[m1]++
		} else {
			code[m0]++
			code[m1]--
		}
	} else {
		if sign == 1 {
			code[m0]--
			code[m1]--
		} else {
			code[m0]++
			code[m1]++
		}
	}
}

func lpdAddPulse3(idx, m, off, track int, code []float64) {
	mask := (1 << uint(2*m-1)) - 1
	idx2 := idx & mask
	j := off
	if (idx>>uint(2*m-1))&1 == 1 {
		j += 1 << uint(m-1)
	}
	lpdAddPulse2(idx2, m-1, j, track, code)
	mask = (1 << uint(m+1)) - 1
	idx1 := (idx >> uint(2*m)) & mask
	lpdAddPulse1(idx1, m, off, track, code)
}

func lpdAdd4Section(index, off, track int, code []float64) {
	idx2 := index & 31
	j := off
	if (index>>5)&1 == 1 {
		j += 4
	}
	lpdAddPulse2(idx2, 2, j, track, code)
	idx2 = (index >> 6) & 127
	lpdAddPulse2(idx2, 3, off, track, code)
}

func lpdAddPulse4(idx4, track int, code []float64) {
	switch (idx4 >> 14) & 3 {
	case 0:
		if (idx4>>13)&1 == 0 {
			lpdAdd4Section(idx4, 0, track, code)
		} else {
			lpdAdd4Section(idx4, 8, track, code)
		}
	case 1:
		lpdAddPulse1(idx4>>10, 3, 0, track, code)
		lpdAddPulse3(idx4, 3, 8, track, code)
	case 2:
		lpdAddPulse2(idx4>>7, 3, 0, track, code)
		lpdAddPulse2(idx4, 3, 8, track, code)
	case 3:
		lpdAddPulse3(idx4>>4, 3, 0, track, code)
		lpdAddPulse1(idx4, 3, 8, track, code)
	}
}

func lpdDecode1pN1(index, n, off int) int {
	mask := (1 << uint(n)) - 1
	pos := (index & mask) + off
	if (index>>uint(n))&1 == 1 {
		pos += 16
	}
	return pos
}

func lpdAddPulseSigned(pos, track int, code []float64) {
	i := (pos&15)<<2 + track
	if pos&16 == 0 {
		code[i]++
	} else {
		code[i]--
	}
}

// lpdPulses decodes the algebraic codebook vector (64 samples).
func lpdPulses(cb []int, codeBits int, code []float64) {
	for i := range code[:lpdSubfr] {
		code[i] = 0
	}
	switch codeBits {
	case 12:
		for t := 0; t < 4; t += 2 {
			off := cb[2*(t/2)]
			index := cb[2*(t/2)+1]
			pos := lpdDecode1pN1(index, 4, 0)
			lpdAddPulseSigned(pos, 2*off+t/2, code)
		}
	case 16:
		i := 0
		off := cb[i]
		i++
		if off == 0 {
			off = 1
		} else {
			off = 3
		}
		for t := 0; t < 4; t++ {
			if t != off {
				index := cb[i]
				i++
				pos := lpdDecode1pN1(index, 4, 0)
				lpdAddPulseSigned(pos, t, code)
			}
		}
	case 20:
		for t := 0; t < 4; t++ {
			lpdAddPulse1(cb[t], 4, 0, t, code)
		}
	case 28:
		for t := 0; t < 2; t++ {
			lpdAddPulse2(cb[t], 4, 0, t, code)
		}
		for t := 2; t < 4; t++ {
			lpdAddPulse1(cb[t], 4, 0, t, code)
		}
	case 36:
		for t := 0; t < 4; t++ {
			lpdAddPulse2(cb[t], 4, 0, t, code)
		}
	case 44:
		for t := 0; t < 2; t++ {
			lpdAddPulse3(cb[t], 4, 0, t, code)
		}
		for t := 2; t < 4; t++ {
			lpdAddPulse2(cb[t], 4, 0, t, code)
		}
	case 52:
		for t := 0; t < 4; t++ {
			lpdAddPulse3(cb[t], 4, 0, t, code)
		}
	case 64:
		for t := 0; t < 4; t++ {
			idx := cb[t]<<14 + cb[t+4]
			lpdAddPulse4(idx, t, code)
		}
	}
}

var lpdCodeBitsTable = [8]int{20, 28, 36, 44, 52, 64, 12, 16}

// cbExc computes the adaptive codebook excitation (65 samples) in place:
// buf[base .. base+64] from the past excitation.
func lpdCBExc(buf []float64, base, lag, frac int) {
	x0 := base - lag
	frac = -frac
	if frac < 0 {
		frac += 4
		x0--
	}
	for j := 0; j < lpdSubfr+1; j++ {
		x1 := x0
		x0++
		x2 := x1 + 1
		c1 := frac
		c2 := 4 - frac
		var s float64
		for i := 0; i < lpdInterOrder; i++ {
			s += buf[x1]*float64(usacInterpolFilt[c1]) + buf[x2]*float64(usacInterpolFilt[c2])
			x1--
			x2++
			c1 += 4
			c2 += 4
		}
		buf[base+j] = s
	}
}

var lpdGamma = [17]float64{1, 0.92, 0.8464, 0.778688, 0.716393, 0.659082,
	0.606355, 0.557847, 0.513219, 0.472161, 0.434389, 0.399637,
	0.367666, 0.338253, 0.311193, 0.286298, 0.263394}

// lpdSynth runs 1/A(z) on x writing y; mem holds the last 16 outputs.
func lpdSynthFilter(a []float64, x, y []float64, mem []float64) {
	var buf [lpdOrder + 512]float64
	copy(buf[:lpdOrder], mem[:lpdOrder])
	n := len(x)
	for i := 0; i < n; i++ {
		s := x[i]
		for j := 1; j <= lpdOrder; j++ {
			s -= a[j] * buf[lpdOrder+i-j]
		}
		buf[lpdOrder+i] = s
		y[i] = s
	}
}

// acelpFrame decodes subframes of one ACELP frame k (len_subfr samples).
func (d *usacDec) lpdACELP(c *usacCh, f *lpdFrame, k int, lp []float64, stability float64) {
	st := c.lpd
	lenSub := d.cfg.coreLen / 4
	numSub := d.cfg.coreLen * 4 / lpdSuper
	by2 := (4*numSub)/2 - 1
	synthDelay := by2 * lpdSubfr
	sb := st.synthBuf[:]
	eb := st.excBuf[:]
	sBase := lenSub*k + lpdMaxPitch + synthDelay
	eBase := lenSub*k + lpdMaxPitch + lpdInterOrder + 1
	pgBase := k*numSub + by2
	facLen := lenSub / 2
	coreMode := f.acelpCoreMode
	var xn2 [2*lpdFacLength + 16]float64
	var x [lpdFacLength]float64
	var synthTemp [128 + 16]float64
	var code, post [lpdSubfr]float64

	if st.modePrev > 0 {
		for j := 0; j < facLen; j++ {
			x[j] = st.facGain * float64(f.fac[k*lpdFacLength+j])
		}
		for j := 0; j < facLen/4; j++ {
			x[j] *= st.facFd[j]
		}
		var tmp [lpdFacLength]float64
		st.dct4(x[:facLen], tmp[:facLen])
		for i := 0; i < facLen; i++ {
			xn2[facLen+i] = tmp[i] * 2 / float64(facLen)
		}
		for i := 0; i < facLen; i++ {
			xn2[i] = 0
		}
		// weighted synthesis filter 1/A(z/gamma) with the previous LPC
		for i := 0; i < facLen; i++ {
			s := xn2[facLen+i]
			for j := 1; j <= lpdOrder; j++ {
				idx := facLen + i - j
				s -= st.lpFltPrev[j] * lpdGamma[j] * xn2[idx]
			}
			xn2[facLen+i] = s
		}
		for i := 0; i < 2*facLen; i++ {
			xn2[i] += sb[sBase+i-2*facLen]
		}
		copy(sb[sBase-facLen:sBase], xn2[facLen:2*facLen])
		for i := 2*facLen - 1; i > 0; i-- {
			xn2[i] -= lpdPreemph * xn2[i-1]
		}
		ptr := 0
		ttt := facLen % lpdSubfr
		if ttt != 0 {
			lpdResidual(st.lpFltPrev[ptr:], xn2[:], facLen, eb, eBase+facLen-2*facLen, ttt, 1)
			ptr += lpdOrder + 1
		}
		loop := (facLen + ttt) / lpdSubfr
		lpdResidual(st.lpFltPrev[ptr:], xn2[:], facLen+ttt, eb, eBase+ttt-facLen, lpdSubfr, loop)
	}
	for i := 0; i < lpdOrder; i++ {
		synthTemp[i] = sb[sBase+i-lpdOrder] - lpdPreemph*sb[sBase+i-lpdOrder-1]
	}
	ii := (st.fscale*34+6400)/12800 - 34
	pitchMin := 34 + ii
	pitchFr2 := (162 - 34) - ii
	pitchFr1 := 160
	pitchMax := (27 + 6*34) + 6*ii
	lpOff := 0
	subNb := 0
	var pitchLag, pitchFrac, pitchLagMin, pitchLagMax int
	for sub := 0; sub < lenSub; sub += lpdSubfr {
		pflag := sub
		if lenSub == 256 && sub == 2*lpdSubfr {
			pflag = 0
		}
		index := f.acbIndex[k*4+subNb]
		if pflag == 0 {
			switch {
			case index < (pitchFr2-pitchMin)*4:
				pitchLag = pitchMin + index/4
				pitchFrac = index - (pitchLag-pitchMin)*4
			case index < (pitchFr2-pitchMin)*4+(pitchFr1-pitchFr2)*2:
				index -= (pitchFr2 - pitchMin) * 4
				pitchLag = pitchFr2 + index/2
				pitchFrac = (index - (pitchLag-pitchFr2)*2) * 2
			default:
				pitchLag = index + pitchFr1 - (pitchFr2-pitchMin)*4 - (pitchFr1-pitchFr2)*2
				pitchFrac = 0
			}
			pitchLagMin = pitchLag - 8
			if pitchLagMin < pitchMin {
				pitchLagMin = pitchMin
			}
			pitchLagMax = pitchLagMin + 15
			if pitchLagMax > pitchMax {
				pitchLagMax = pitchMax
				pitchLagMin = pitchLagMax - 15
			}
		} else {
			pitchLag = pitchLagMin + index/4
			pitchFrac = index - (pitchLag-pitchLagMin)*4
		}
		lpdCBExc(eb, eBase+sub, pitchLag, pitchFrac)
		meanEner := float64(f.meanEnergy[k])*12 + 18
		if f.ltpFilt[k*4+subNb] == 0 {
			for i := 0; i < lpdSubfr; i++ {
				code[i] = 0.18*eb[eBase+sub+i-1] + 0.64*eb[eBase+sub+i] + 0.18*eb[eBase+sub+i+1]
			}
			copy(eb[eBase+sub:eBase+sub+lpdSubfr], code[:])
		}
		lpdPulses(f.icb[k*4+subNb][:], lpdCodeBitsTable[coreMode], code[:])
		for i := lpdSubfr - 1; i > 0; i-- {
			code[i] -= lpdTiltCode * code[i-1]
		}
		ls := pitchLag
		if pitchFrac > 2 {
			ls++
		}
		if ls >= 0 {
			for i := ls; i < lpdSubfr; i++ {
				code[i] += code[i-ls] * 0.85
			}
		}
		// gains
		gi := f.gains[k*4+subNb]
		innov := 0.01
		for i := 0; i < lpdSubfr; i++ {
			innov += code[i] * code[i]
		}
		innovE := innov
		est := meanEner - 10*math.Log10(innov/lpdSubfr)
		est = math.Pow(10, 0.05*est)
		pitchGain := float64(usacAcelpGain[gi*2])
		gainCode := float64(usacAcelpGain[gi*2+1]) * est
		pe := 0.0
		for i := 0; i < lpdSubfr; i++ {
			v := eb[eBase+sub+i]
			pe += v * v
		}
		pe *= pitchGain * pitchGain
		innovE *= gainCode * gainCode
		rv := (pe - innovE) / (pe + innovE)
		for i := 0; i < lpdSubfr; i++ {
			post[i] = pitchGain * eb[eBase+sub+i]
		}
		for i := 0; i < lpdSubfr; i++ {
			eb[eBase+sub+i] = pitchGain*eb[eBase+sub+i] + gainCode*code[i]
		}
		pl := pitchLag
		if pitchFrac > 2 {
			pl++
		}
		if pl > pitchMax {
			pl = pitchMax
		}
		st.pitchArr[pgBase+subNb] = pl
		st.gainArr[pgBase+subNb] = pitchGain
		voicing := 0.5 * (1 - rv)
		gsf := stability * voicing
		g0 := gainCode
		if g0 < st.gainThreshold {
			g0 *= 1.19
			if g0 > st.gainThreshold {
				g0 = st.gainThreshold
			}
		} else {
			g0 /= 1.19
			if g0 < st.gainThreshold {
				g0 = st.gainThreshold
			}
		}
		st.gainThreshold = g0
		gs := gsf*g0 + (1-gsf)*gainCode
		for i := range code {
			code[i] *= gs
		}
		cpe := 0.125 * (1 + rv)
		post[0] += code[0] - cpe*code[1]
		for i := 1; i < lpdSubfr-1; i++ {
			post[i] += code[i] - cpe*(code[i-1]+code[i+1])
		}
		post[lpdSubfr-1] += code[lpdSubfr-1] - cpe*code[lpdSubfr-2]
		lpdSynthFilter(lp[lpOff:], post[:], sb[sBase+sub:sBase+sub+lpdSubfr], synthTemp[:lpdOrder])
		copy(synthTemp[:lpdOrder], sb[sBase+sub+lpdSubfr-lpdOrder:sBase+sub+lpdSubfr])
		lpOff += lpdOrder + 1
		subNb++
	}
	// de-emphasis
	mem := sb[sBase-1]
	sb[sBase] += lpdPreemph * mem
	for i := 1; i < lenSub; i++ {
		sb[sBase+i] += lpdPreemph * sb[sBase+i-1]
	}
	for i := 16; i < 144; i++ {
		synthTemp[i] = 0
	}
	// zero input response of 128 samples with the next LPC
	{
		s := synthTemp[:]
		for i := 0; i < 128; i++ {
			v := s[16+i]
			for j := 1; j <= lpdOrder; j++ {
				v -= lp[lpOff+j] * s[16+i-j]
			}
			s[16+i] = v
		}
	}
	lpOff -= 2 * (lpdOrder + 1)
	copy(st.lpFltPrev[:], lp[lpOff:lpOff+2*(lpdOrder+1)])
	copy(st.excPrev[:1+facLen], sb[sBase+lenSub-(1+facLen):sBase+lenSub])
	copy(st.excPrev[1+facLen:1+2*facLen], synthTemp[16:16+facLen])
	// de-emphasis of the second part with the last output as memory
	m := sb[sBase+lenSub-1]
	e := st.excPrev[1+facLen : 1+2*facLen]
	e[0] += lpdPreemph * m
	for i := 1; i < facLen; i++ {
		e[i] += lpdPreemph * e[i-1]
	}
}

// lpdResidual computes y = A(z) x for loops blocks of l samples; a advances
// by 17 per block, x and y by l. x is indexed from xOff (history before it
// is read), y written in buf at yOff.
func lpdResidual(a []float64, x []float64, xOff int, y []float64, yOff, l, loops int) {
	for j := 0; j < loops; j++ {
		aa := a[j*(lpdOrder+1):]
		for i := 0; i < l; i++ {
			xi := xOff + j*l + i
			s := x[xi]
			for t := 1; t <= lpdOrder; t++ {
				s += aa[t] * x[xi-t]
			}
			y[yOff+j*l+i] = s
		}
	}
}
