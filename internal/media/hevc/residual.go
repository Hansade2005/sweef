package hevc

type tbInfo struct {
	ts       bool
	explicit bool
	dir      int
	zero     bool // no coefficients (cross-component prediction only)
	maxX     int
	maxY     int
	nnz      int
	err      bool
}

type scratch struct {
	coef [32 * 32]int32
	res  [32 * 32]int32
	resY [32 * 32]int32
	tmp  [32 * 32]int32
	act  [3][32 * 32]int32 // residuals of a block with the adaptive colour transform

	// intra
	ref  [4*64 + 1]int32
	refF [4*64 + 1]int32
	av   [4*64 + 1]bool

	// inter
	pred    [2][64 * 64]int16
	predHi  [2][64 * 64]int32
	mcTmp   [(64 + 7) * 64]int16
	mcTmpHi [(64 + 7) * 64]int32
	edgeBf  [(64 + 8) * (64 + 8)]uint16
	saoBuf  [(64 + 2) * (64 + 2)]uint16
}

// chromaQp returns Qp'C for component cIdx (1 or 2).
func (d *Decoder) chromaQp(cIdx int) int {
	s, p, sh := d.sp, d.pp, d.sh
	var off int
	if cIdx == 1 {
		off = p.cbQpOffset + sh.cbQpOffset + d.cuQpOffsetCb
	} else {
		off = p.crQpOffset + sh.crQpOffset + d.cuQpOffsetCr
	}
	qpi := d.qpY + off
	if qpi < -s.qpBdOffC {
		qpi = -s.qpBdOffC
	} else if qpi > 57 {
		qpi = 57
	}
	var qpc int
	if s.chromaArrayType == 1 {
		switch {
		case qpi < 30:
			qpc = qpi
		case qpi >= 43:
			qpc = qpi - 6
		default:
			qpc = qpCTable[qpi-30]
		}
	} else {
		qpc = qpi
		if qpc > 51 {
			qpc = 51
		}
	}
	return qpc + s.qpBdOffC
}

// residualCoding parses residual_coding() for one transform block and leaves
// the (scaled) coefficients in d.scratch.coef.
func (d *Decoder) residualCoding(log2, cIdx int, intra bool, predMode int) tbInfo {
	s, p := d.sp, d.pp
	c := &d.cab
	cu := &d.cu
	sc := &d.scratch
	var ti tbInfo
	n := 1 << uint(log2)
	cc := 0
	if cIdx > 0 {
		cc = 1
	}
	if p.transformSkip && !cu.bypass && log2 <= p.log2MaxTsSize {
		ti.ts = c.bin(ctxTransformSkip+cc) != 0
	}
	if !intra && s.explicitRdpcm && (ti.ts || cu.bypass) {
		ti.explicit = c.bin(ctxRdpcm+cc) != 0
		if ti.explicit {
			ti.dir = c.bin(ctxRdpcmDir + cc)
		}
	}
	// last significant coefficient position
	var ctxOff, ctxShift int
	if cIdx == 0 {
		ctxOff = 3*(log2-2) + ((log2 - 1) >> 2)
		ctxShift = (log2 + 1) >> 2
	} else {
		ctxOff = 15
		ctxShift = log2 - 2
	}
	cMax := (log2 << 1) - 1
	px := 0
	for px < cMax && c.bin(ctxLastX+ctxOff+(px>>uint(ctxShift))) != 0 {
		px++
	}
	py := 0
	for py < cMax && c.bin(ctxLastY+ctxOff+(py>>uint(ctxShift))) != 0 {
		py++
	}
	lastX, lastY := px, py
	if px > 3 {
		nb := (px >> 1) - 1
		lastX = (1<<uint(nb))*(2+(px&1)) + c.bypassBits(nb)
	}
	if py > 3 {
		nb := (py >> 1) - 1
		lastY = (1<<uint(nb))*(2+(py&1)) + c.bypassBits(nb)
	}
	scanIdx := 0
	if intra && (log2 == 2 || (log2 == 3 && cIdx == 0) || (log2 == 3 && s.chromaArrayType == 3)) {
		if predMode >= 6 && predMode <= 14 {
			scanIdx = 2
		} else if predMode >= 22 && predMode <= 30 {
			scanIdx = 1
		}
	}
	if scanIdx == 2 {
		lastX, lastY = lastY, lastX
	}
	if lastX >= n || lastY >= n {
		ti.err = true
		return ti
	}
	subLog := log2 - 2
	subScan := scanOrder[scanIdx][subLog]
	posScan := scanOrder[scanIdx][2]
	lastSub := int(scanPos[scanIdx][subLog][(lastY>>2)<<uint(subLog)|(lastX>>2)])
	lastPos := int(scanPos[scanIdx][2][(lastY&3)<<2|(lastX&3)])

	// dequantisation parameters
	qp := d.tuQp(cIdx)
	bd := s.bitDepthY
	if cIdx > 0 {
		bd = s.bitDepthC
	}
	log2Range := 15
	if s.extPrecision {
		log2Range = max(15, bd+6)
	}
	cMin, cMaxC := int64(-1)<<uint(log2Range), int64(1)<<uint(log2Range)-1
	bdShift := uint(bd + log2 + 10 - log2Range)
	rnd := int64(1) << (bdShift - 1)
	scale := int64(16) * levelScale[qp%6] << uint(qp/6)
	var mtab []uint8
	if p.factor != nil && !(ti.ts && n > 4) {
		m := cIdx
		if !intra {
			m += 3
		}
		mtab = p.factor.m[log2-2][m]
	}
	bypass := cu.bypass

	signHiddenOff := bypass || ti.explicit || (intra && s.implicitRdpcm && ti.ts && (predMode == 10 || predMode == 26))
	tsCtx := s.tsContext && (ti.ts || bypass)
	sbType := 0
	if cIdx == 0 {
		sbType = 2
	}
	if ti.ts || bypass {
		sbType++
	}

	var csbf [64]uint8 // by subblock y*8+x
	gt1Ctx := 1
	maxX, maxY := 0, 0
	coef := sc.coef[:n*n]
	for i := lastSub; i >= 0; i-- {
		xS, yS := int(subScan[i][0]), int(subScan[i][1])
		inferDC := false
		coded := true
		right, below := 0, 0
		if xS+1 < 1<<uint(subLog) {
			right = int(csbf[yS*8+xS+1])
		}
		if yS+1 < 1<<uint(subLog) {
			below = int(csbf[(yS+1)*8+xS])
		}
		if i < lastSub && i > 0 {
			inc := right | below
			if inc > 1 {
				inc = 1
			}
			coded = c.bin(ctxCsbf+inc+2*cc) != 0
			inferDC = true
		}
		if !coded {
			continue
		}
		csbf[yS*8+xS] = 1
		prevCsbf := right | below<<1
		var sigPos [16]uint8
		nSig := 0
		start := 15
		if i == lastSub {
			start = lastPos - 1
			sigPos[0] = uint8(lastPos)
			nSig = 1
		}
		for nn := start; nn >= 0; nn-- {
			xP, yP := int(posScan[nn][0]), int(posScan[nn][1])
			if nn > 0 || !inferDC {
				var sigCtx int
				switch {
				case tsCtx:
					if cIdx == 0 {
						sigCtx = 42
					} else {
						sigCtx = 16
					}
				case log2 == 2:
					sigCtx = int(ctxIdxMap4[(yP<<2)+xP])
				case xS == 0 && yS == 0 && nn == 0:
					sigCtx = 0
				default:
					switch prevCsbf {
					case 0:
						if xP+yP == 0 {
							sigCtx = 2
						} else if xP+yP < 3 {
							sigCtx = 1
						}
					case 1:
						if yP == 0 {
							sigCtx = 2
						} else if yP == 1 {
							sigCtx = 1
						}
					case 2:
						if xP == 0 {
							sigCtx = 2
						} else if xP == 1 {
							sigCtx = 1
						}
					default:
						sigCtx = 2
					}
					if cIdx == 0 {
						if xS > 0 || yS > 0 {
							sigCtx += 3
						}
						if log2 == 3 {
							if scanIdx == 0 {
								sigCtx += 9
							} else {
								sigCtx += 15
							}
						} else {
							sigCtx += 21
						}
					} else {
						if log2 == 3 {
							sigCtx += 9
						} else {
							sigCtx += 12
						}
					}
				}
				if cIdx > 0 {
					sigCtx += 27
				}
				if c.bin(ctxSig+sigCtx) != 0 {
					sigPos[nSig] = uint8(nn)
					nSig++
					inferDC = false
				}
			} else {
				sigPos[nSig] = 0
				nSig++
			}
		}
		if nSig == 0 {
			continue
		}
		// greater1 / greater2
		ctxSet := 2
		if i == 0 || cIdx > 0 {
			ctxSet = 0
		}
		if i != lastSub && gt1Ctx == 0 {
			ctxSet++
		}
		gt1Ctx = 1
		nG1 := nSig
		if nG1 > 8 {
			nG1 = 8
		}
		var g1 [8]bool
		firstG1 := -1
		escape := nSig > 8
		for m := 0; m < nG1; m++ {
			inc := ctxSet*4 + gt1Ctx
			if cIdx > 0 {
				inc += 16
			}
			f := c.bin(ctxGt1+inc) != 0
			g1[m] = f
			if f {
				gt1Ctx = 0
				if firstG1 < 0 {
					firstG1 = m
				} else {
					escape = true
				}
			} else if gt1Ctx > 0 && gt1Ctx < 3 {
				gt1Ctx++
			}
		}
		g2 := false
		if firstG1 >= 0 {
			inc := ctxSet
			if cIdx > 0 {
				inc += 4
			}
			g2 = c.bin(ctxGt2+inc) != 0
			if g2 {
				escape = true
			}
		}
		if s.cabacBypassAlign && escape {
			c.align()
		}
		hidden := false
		if p.signHiding && !signHiddenOff {
			hidden = int(sigPos[0])-int(sigPos[nSig-1]) > 3
		}
		nSign := nSig
		if hidden {
			nSign--
		}
		signs := c.bypassBits(nSign) << uint(16-nSign)
		// remaining levels
		rice := 0
		if s.persistentRice {
			rice = int(d.statCoeff[sbType]) / 4
		}
		riceInit := false
		sumAbs := 0
		for m := 0; m < nSig; m++ {
			base := 1
			thr := 1
			if m < 8 {
				if g1[m] {
					base = 2
				}
				if m == firstG1 {
					if g2 {
						base = 3
					}
					thr = 3
				} else {
					thr = 2
				}
			}
			abs := base
			if base == thr {
				// coeff_abs_level_remaining
				prefix := 0
				maxPrefix := 32
				if s.extPrecision {
					maxPrefix = 32 - log2Range
				}
				for prefix < maxPrefix && c.bypass() != 0 {
					prefix++
				}
				var rem int
				if prefix <= 3 {
					rem = prefix<<uint(rice) + c.bypassBits(rice)
				} else if s.extPrecision {
					// limited prefix length escape (9.3.3.12)
					pl := prefix - 3
					sl := pl + rice
					if prefix == maxPrefix {
						sl = log2Range
					}
					rem = (((1 << uint(pl)) - 1 + 3) << uint(rice)) + c.bypassBits(sl)
				} else {
					sl := prefix - 3 + rice
					if sl > 30 {
						ti.err = true
						return ti
					}
					rem = (((1 << uint(prefix-3)) + 3 - 1) << uint(rice)) + c.bypassBits(sl)
				}
				abs = base + rem
				if s.persistentRice && !riceInit {
					st := int(d.statCoeff[sbType])
					if rem >= (3 << uint(st/4)) {
						d.statCoeff[sbType]++
					} else if 2*rem < (1<<uint(st/4)) && st > 0 {
						d.statCoeff[sbType]--
					}
					riceInit = true
				}
				if abs > 3<<uint(rice) {
					if s.persistentRice {
						rice++
					} else if rice < 4 {
						rice++
					}
				}
			}
			val := abs
			sumAbs += abs
			if signs&(1<<15) != 0 && m < nSign {
				val = -val
			}
			signs <<= 1
			if hidden && m == nSig-1 && sumAbs&1 != 0 {
				val = -val
			}
			pos := int(sigPos[m])
			xC := xS<<2 + int(posScan[pos][0])
			yC := yS<<2 + int(posScan[pos][1])
			if int64(val) > cMaxC {
				val = int(cMaxC)
			} else if int64(val) < cMin {
				val = int(cMin)
			}
			var v int32
			if bypass {
				v = int32(val)
			} else {
				var sc64 int64
				if mtab != nil {
					sc64 = int64(mtab[yC*n+xC]) * levelScale[qp%6] << uint(qp/6)
				} else {
					sc64 = scale
				}
				r := (int64(val)*sc64 + rnd) >> bdShift
				if r > cMaxC {
					r = cMaxC
				} else if r < cMin {
					r = cMin
				}
				v = int32(r)
			}
			coef[yC*n+xC] = v
			if xC > maxX {
				maxX = xC
			}
			if yC > maxY {
				maxY = yC
			}
			ti.nnz++
		}
	}
	ti.maxX, ti.maxY = maxX, maxY
	return ti
}

// tuQp returns the quantization parameter Qp' of component cIdx of the
// current transform unit.
func (d *Decoder) tuQp(cIdx int) int {
	s, sh := d.sp, d.sh
	if !d.tuAct {
		if cIdx == 0 {
			return d.qpY + s.qpBdOffY
		}
		return d.chromaQp(cIdx)
	}
	// adaptive colour transform: the offsets of the transform replace the
	// picture and slice chroma offsets
	off := d.pp.actQpOffset[cIdx] + sh.actDelta[cIdx]
	if cIdx == 0 {
		return clip3(0, 51+s.qpBdOffY, d.qpY+s.qpBdOffY+off)
	}
	if cIdx == 1 {
		off += d.cuQpOffsetCb
	} else {
		off += d.cuQpOffsetCr
	}
	qpi := clip3(-s.qpBdOffC, 57, d.qpY+off)
	if qpi > 51 {
		qpi = 51
	}
	return qpi + s.qpBdOffC
}
