package media

// Complex prediction stereo (ISO/IEC 23003-3 7.7.2). The estimation of the
// imaginary part of the downmix and the band handling follow the standard;
// the filter coefficient tables are the Q15 tables of usac_tables_xaac_gen.go.

// usacMdstSet returns the (current frame, previous frame) 7-tap filters for
// the window sequence and the window shapes.
func usacMdstSet(seqEff, prevShape, curShape int) (cur, prev []int16) {
	switch seqEff {
	case usacLongStart:
		cur = [2][2][]int16{{usacMdstQ15_startsin[:], usacMdstQ15_startsinkbd[:]}, {usacMdstQ15_startkbdsin[:], usacMdstQ15_startkbd[:]}}[prevShape][curShape]
		prev = [2][]int16{usacMdstQ15_lsstartleftsin[:], usacMdstQ15_lsstartleftkbd[:]}[prevShape]
	case usacLongStop:
		cur = [2][2][]int16{{usacMdstQ15_stopsin[:], usacMdstQ15_stopsinkbd[:]}, {usacMdstQ15_stopkbdsin[:], usacMdstQ15_stopkbd[:]}}[prevShape][curShape]
		prev = [2][]int16{usacMdstQ15_stopstopstartleftsin[:], usacMdstQ15_stopstopstartleftkbd[:]}[prevShape]
	case usacStopStart:
		cur = [2][2][]int16{{usacMdstQ15_stopstartsin[:], usacMdstQ15_stopstartsinkbd[:]}, {usacMdstQ15_stopstartkbdsin[:], usacMdstQ15_stopstartkbd[:]}}[prevShape][curShape]
		prev = [2][]int16{usacMdstQ15_stopstopstartleftsin[:], usacMdstQ15_stopstopstartleftkbd[:]}[prevShape]
	default: // only long, eight short
		cur = [2][2][]int16{{usacMdstQ15_longsin[:], usacMdstQ15_longsinkbd[:]}, {usacMdstQ15_longkbdsin[:], usacMdstQ15_longkbd[:]}}[prevShape][curShape]
		prev = [2][]int16{usacMdstQ15_lsstartleftsin[:], usacMdstQ15_lsstartleftkbd[:]}[prevShape]
	}
	return
}

// usacFilterAdd adds factor * sum_j f[j]*in[k-3+j] to out[k] (the input is
// mirrored at both ends); factor is fe for even k and fo for odd k.
func usacFilterAdd(in, out []float64, f []int16, fe, fo float64) {
	n := len(in)
	var ff [7]float64
	for j := range ff {
		ff[j] = float64(f[j]) / 32768
	}
	at := func(i int) float64 {
		if i < 0 {
			i = -1 - i
		} else if i >= n {
			i = 2*n - 1 - i
		}
		return in[i]
	}
	for k := 0; k < n; k++ {
		var s float64
		if k >= 3 && k+3 < n {
			s = ff[0]*in[k-3] + ff[1]*in[k-2] + ff[2]*in[k-1] + ff[3]*in[k] + ff[4]*in[k+1] + ff[5]*in[k+2] + ff[6]*in[k+3]
		} else {
			for j := 0; j < 7; j++ {
				s += ff[j] * at(k-3+j)
			}
		}
		if k&1 == 0 {
			out[k] += s * fe
		} else {
			out[k] += s * fo
		}
	}
}

func (d *usacDec) complexStereo(el *usacElem) {
	st := &el.st
	c0, c1 := &el.ch[0], &el.ch[1]
	ics := &c0.ics
	short := ics.seq == usacEightShort
	stride := d.cfg.coreLen
	if short {
		stride = d.cfg.coreLen / 8
	}
	ste := st.maxSfbSte
	factor := 1.0
	if st.predDir != 0 {
		factor = -1
	}
	var dmxRe, dmxIm [1024]float64
	base := 0
	for g := 0; g < ics.numGroups; g++ {
		for w := 0; w < ics.groupLen[g]; w++ {
			for sfb := 0; sfb < ste; sfb++ {
				s, e := int(ics.swb[sfb]), int(ics.swb[sfb+1])
				o := base + w*stride
				if st.predUsed[g*ste+sfb] {
					copy(dmxRe[o+s:o+e], c0.coef[o+s:o+e])
				} else {
					for k := s; k < e; k++ {
						dmxRe[o+k] = 0.5 * (c0.coef[o+k] + factor*c1.coef[o+k])
					}
				}
			}
		}
		base += ics.groupLen[g] * stride
	}
	if st.complexCoef {
		cur, prev := usacMdstSet(ics.seqEff, ics.kbPrev, ics.kb)
		offs := d.cfg.coreLen - stride
		var prevDmx [1024]float64
		if st.usePrevFrame {
			for k := 0; k < stride; k++ {
				prevDmx[k] = 0.5 * (st.prevL[offs+k] + factor*st.prevR[offs+k])
			}
		}
		for w := 0; w < ics.numWindows; w++ {
			re := dmxRe[w*stride : (w+1)*stride]
			im := dmxIm[w*stride : (w+1)*stride]
			usacFilterAdd(re, im, cur, 1, 1)
			if w == 0 {
				if st.usePrevFrame {
					usacFilterAdd(prevDmx[:stride], im, prev, -1, 1)
				}
			} else {
				usacFilterAdd(dmxRe[(w-1)*stride:w*stride], im, prev, -1, 1)
			}
		}
	}
	base = 0
	for g := 0; g < ics.numGroups; g++ {
		for w := 0; w < ics.groupLen[g]; w++ {
			for sfb := 0; sfb < ste; sfb++ {
				if !st.predUsed[g*ste+sfb] {
					continue
				}
				s, e := int(ics.swb[sfb]), int(ics.swb[sfb+1])
				ar, ai := st.alphaRe[g*ste+sfb], st.alphaIm[g*ste+sfb]
				o := base + w*stride
				for k := o + s; k < o+e; k++ {
					l, r := c0.coef[k], c1.coef[k]
					ms := r - ar*l
					if st.complexCoef {
						ms -= ai * dmxIm[k]
					}
					c1.coef[k] = factor * (l - ms)
					c0.coef[k] = l + ms
				}
			}
		}
		base += ics.groupLen[g] * stride
	}
}

// savePrevDmx keeps the spectra of the last window for the next frame's
// downmix estimate.
func (d *usacDec) savePrevDmx(el *usacElem) {
	st := &el.st
	if el.ch[0].coreMode != 0 || el.ch[1].coreMode != 0 {
		return
	}
	stride := d.cfg.coreLen
	if el.ch[0].ics.seq == usacEightShort {
		stride = d.cfg.coreLen / 8
	}
	offs := d.cfg.coreLen - stride
	copy(st.prevL[offs:offs+stride], el.ch[0].coef[offs:offs+stride])
	copy(st.prevR[offs:offs+stride], el.ch[1].coef[offs:offs+stride])
}
