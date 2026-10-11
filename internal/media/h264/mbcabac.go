package h264

// CABAC syntax element decoding for the macroblock layer (9.3.2, 9.3.3.1).

// cabacSkipFlag decodes mb_skip_flag for macroblock (mbx, mby) using the
// current field decoding flag for the MBAFF neighbour derivation.
func (d *Decoder) cabacSkipFlag(mbx, mby int) bool {
	pic := d.curPic
	stride := d.mbStride
	var a, b int
	if d.mbaffFrame {
		mbxy := pic.mbIdx(mbx, mby&^1)
		a = mbxy - 1
		if mby&1 != 0 && d.sliceTable[a] == d.curSlice && d.mbField == (pic.mbType[a]&mbInterlaced != 0) {
			a += stride
		}
		if d.mbField {
			b = mbxy - stride
			if mby&1 == 0 && d.sliceTable[b] == d.curSlice && pic.mbType[b]&mbInterlaced != 0 {
				b -= stride
			}
		} else {
			b = pic.mbIdx(mbx, mby-1)
		}
	} else {
		mbxy := pic.mbIdx(mbx, mby)
		a = mbxy - 1
		b = mbxy - stride<<uint(btoi(d.fieldPic))
	}
	ctx := 0
	if d.sliceTable[a] == d.curSlice && pic.mbType[a]&mbSkip == 0 {
		ctx++
	}
	if d.sliceTable[b] == d.curSlice && pic.mbType[b]&mbSkip == 0 {
		ctx++
	}
	if d.hdr.isB() {
		ctx += 13
	}
	return d.cab.bin(11+ctx) != 0
}

func (d *Decoder) cabacFieldDecodingFlag() bool {
	pic := d.curPic
	b := d.mbIdx - 2*d.mbStride
	ctx := 0
	if d.mbx > 0 && d.mbField {
		ctx++
	}
	if pic.mbType[b]&mbInterlaced != 0 && d.sliceTable[b] == d.curSlice {
		ctx++
	}
	return d.cab.bin(70+ctx) != 0
}

// cabacIntraMBType decodes an intra mb_type (0..25). inI selects the I slice
// context set (offset 3, neighbour dependent first bin); otherwise base is 17
// (P) or 32 (B).
func (d *Decoder) cabacIntraMBType(inI bool, base int) int {
	c := &d.cab
	if inI {
		inc := 0
		if d.nb.leftType[0]&(mbIntra16x16|mbIntraPCM) != 0 {
			inc++
		}
		if d.nb.topType&(mbIntra16x16|mbIntraPCM) != 0 {
			inc++
		}
		if c.bin(3+inc) == 0 {
			return 0
		}
	} else if c.bin(base) == 0 {
		return 0
	}
	if c.terminate() != 0 {
		return 25
	}
	var cLuma, cChroma, cChroma2, cPM1, cPM1b, cPM0, cPM0b int
	if inI {
		cLuma, cChroma = 3+3, 3+4
		cChroma2, cPM1, cPM0 = 3+5, 3+6, 3+7 // chroma != 0 branch
		cPM1b, cPM0b = 3+6, 3+7              // chroma == 0 branch
	} else {
		cLuma, cChroma = base+1, base+2
		cChroma2, cPM1, cPM0 = base+2, base+3, base+3
		cPM1b, cPM0b = base+3, base+3
	}
	mb := 1
	if c.bin(cLuma) != 0 {
		mb += 12
	}
	if c.bin(cChroma) != 0 {
		chroma := 1 + c.bin(cChroma2)
		mb += 4 * chroma
		mb += 2 * c.bin(cPM1)
		mb += c.bin(cPM0)
	} else {
		mb += 2 * c.bin(cPM1b)
		mb += c.bin(cPM0b)
	}
	return mb
}

func (d *Decoder) cabacMBTypeP() int {
	c := &d.cab
	if c.bin(14) == 0 {
		if c.bin(15) == 0 {
			if c.bin(16) == 0 {
				return 0
			}
			return 3
		}
		if c.bin(17) == 0 {
			return 2
		}
		return 1
	}
	return 5 + d.cabacIntraMBType(false, 17)
}

func (d *Decoder) cabacMBTypeB() int {
	c := &d.cab
	inc := 0
	if d.nb.leftType[0] != 0 && d.nb.leftType[0]&mbDirect2 == 0 {
		inc++
	}
	if d.nb.topType != 0 && d.nb.topType&mbDirect2 == 0 {
		inc++
	}
	if c.bin(27+inc) == 0 {
		return 0
	}
	if c.bin(27+3) == 0 {
		return 1 + c.bin(27+5)
	}
	b := c.bin(27+4) << 3
	b |= c.bin(27+5) << 2
	b |= c.bin(27+5) << 1
	b |= c.bin(27 + 5)
	switch {
	case b < 8:
		return b + 3
	case b == 13:
		return 23 + d.cabacIntraMBType(false, 32)
	case b == 14:
		return 11
	case b == 15:
		return 22
	}
	b = b<<1 | c.bin(27+5)
	return b - 4
}

func (d *Decoder) cabacSubTypeP() int {
	c := &d.cab
	if c.bin(21) != 0 {
		return 0
	}
	if c.bin(22) == 0 {
		return 1
	}
	if c.bin(23) != 0 {
		return 2
	}
	return 3
}

func (d *Decoder) cabacSubTypeB() int {
	c := &d.cab
	if c.bin(36) == 0 {
		return 0
	}
	if c.bin(37) == 0 {
		return 1 + c.bin(39)
	}
	t := 3
	if c.bin(38) != 0 {
		if c.bin(39) != 0 {
			return 11 + c.bin(39)
		}
		t += 4
	}
	t += 2 * c.bin(39)
	t += c.bin(39)
	return t
}

// cabacRefIdx decodes ref_idx for the partition whose first block is n.
func (d *Decoder) cabacRefIdx(list, n int) int {
	c := &d.cab
	refa := int(d.refCache[list][scan8[n]-1])
	refb := int(d.refCache[list][scan8[n]-8])
	ctx := 0
	if d.hdr.isB() {
		if refa > 0 && d.directCache[scan8[n]-1]&(mbDirect2>>1) == 0 {
			ctx++
		}
		if refb > 0 && d.directCache[scan8[n]-8]&(mbDirect2>>1) == 0 {
			ctx += 2
		}
	} else {
		if refa > 0 {
			ctx++
		}
		if refb > 0 {
			ctx += 2
		}
	}
	ref := 0
	for c.bin(54+ctx) != 0 {
		ref++
		ctx = ctx>>2 + 4
		if ref >= 32 {
			return -1
		}
	}
	return ref
}

// cabacMVD decodes one motion vector difference component with base context
// ctxBase (40 or 47) and the sum amvd of the neighbouring absolute values.
// It returns the value and the (clamped) absolute value to store.
func (d *Decoder) cabacMVD(ctxBase, amvd int) (int, uint8) {
	c := &d.cab
	inc := 0
	if amvd >= 3 {
		inc = 1
		if amvd > 32 {
			inc = 2
		}
	}
	if c.bin(ctxBase+inc) == 0 {
		return 0, 0
	}
	v := 1
	ctx := ctxBase + 3
	for v < 9 && c.bin(ctx) != 0 {
		if v < 4 {
			ctx++
		}
		v++
	}
	var stored int
	if v >= 9 {
		k := 3
		for c.bypass() != 0 {
			v += 1 << uint(k)
			k++
			if k > 24 {
				break
			}
		}
		for k > 0 {
			k--
			v += c.bypass() << uint(k)
		}
		stored = min(v, 70)
	} else {
		stored = v
	}
	if c.bypass() != 0 {
		v = -v
	}
	return v, uint8(stored)
}

func (d *Decoder) cabacIntraChroma() int {
	c := &d.cab
	inc := 0
	if d.nb.leftType[0] != 0 && d.chromaPredTable[d.nb.leftXY[0]] != 0 {
		inc++
	}
	if d.nb.topType != 0 && d.chromaPredTable[d.nb.topXY] != 0 {
		inc++
	}
	if c.bin(64+inc) == 0 {
		return 0
	}
	if c.bin(67) == 0 {
		return 1
	}
	if c.bin(67) == 0 {
		return 2
	}
	return 3
}

func (d *Decoder) cabacCBPLuma() int {
	c := &d.cab
	a, b := d.leftCBP, d.topCBP
	cbp := 0
	ctx := (btoi(a&0x02 == 0)) + 2*btoi(b&0x04 == 0)
	cbp += c.bin(73 + ctx)
	ctx = btoi(cbp&0x01 == 0) + 2*btoi(b&0x08 == 0)
	cbp += c.bin(73+ctx) << 1
	ctx = btoi(a&0x08 == 0) + 2*btoi(cbp&0x01 == 0)
	cbp += c.bin(73+ctx) << 2
	ctx = btoi(cbp&0x04 == 0) + 2*btoi(cbp&0x02 == 0)
	cbp += c.bin(73+ctx) << 3
	return cbp
}

func (d *Decoder) cabacCBPChroma() int {
	c := &d.cab
	a := (d.leftCBP >> 4) & 3
	b := (d.topCBP >> 4) & 3
	ctx := btoi(a > 0) + 2*btoi(b > 0)
	if c.bin(77+ctx) == 0 {
		return 0
	}
	ctx = 4 + btoi(a == 2) + 2*btoi(b == 2)
	return 1 + c.bin(77+ctx)
}

func (d *Decoder) cabacQPDelta() int {
	c := &d.cab
	inc := 0
	if d.lastDQ {
		inc = 1
	}
	if c.bin(60+inc) == 0 {
		return 0
	}
	v := 1
	ctx := 60 + 2
	for c.bin(ctx) != 0 {
		v++
		ctx = 60 + 3
		if v > 110 {
			return 0
		}
	}
	if v&1 != 0 {
		return (v + 1) >> 1
	}
	return -((v + 1) >> 1)
}

func (d *Decoder) cabacT8() bool {
	return d.cab.bin(399+d.neighborT8) != 0
}

// cbfBase is the ctxIdxOffset of coded_block_flag per ctxBlockCat.
var cbfBase = [14]int{85, 89, 93, 97, 101, 1012, 460, 464, 468, 1016, 472, 476, 480, 1020}

// sigCoeffOffsetDC422 is the ctxIdxInc of significant_coeff_flag and
// last_significant_coeff_flag for 4:2:2 chroma DC levels (Min(i/2, 2)).
var sigCoeffOffsetDC422 = [7]uint8{0, 0, 1, 1, 2, 2, 2}

// cabacBlock decodes one residual block (7.3.5.3.3). cbf is the coded_block_flag
// context increment, or -1 when the flag is not transmitted (8x8 luma).
func (d *Decoder) cabacBlock(cat, cbf, maxNum int, lv *[64]int32) int {
	c := &d.cab
	if cbf >= 0 {
		if c.bin(cbfBase[cat]+cbf) == 0 {
			return 0
		}
	}
	for i := 0; i < maxNum; i++ {
		lv[i] = 0
	}
	fld := btoi(d.mbField)
	sigB := int(sigCoeffOffset[fld][cat])
	lastB := int(lastCoeffOffset[fld][cat])
	absB := int(absLevelOffset[cat])
	var idx [64]int
	n := 0
	found := false
	switch {
	case maxNum == 64:
		for i := 0; i < 63; i++ {
			if c.bin(sigB+int(sigCoeffOffset8x8[fld][i])) != 0 {
				idx[n] = i
				n++
				if c.bin(lastB+int(lastCoeffOffset8x8[i])) != 0 {
					found = true
					break
				}
			}
		}
	case cat == 3 && maxNum == 8:
		for i := 0; i < 7; i++ {
			inc := int(sigCoeffOffsetDC422[i])
			if c.bin(sigB+inc) != 0 {
				idx[n] = i
				n++
				if c.bin(lastB+inc) != 0 {
					found = true
					break
				}
			}
		}
	default:
		for i := 0; i < maxNum-1; i++ {
			if c.bin(sigB+i) != 0 {
				idx[n] = i
				n++
				if c.bin(lastB+i) != 0 {
					found = true
					break
				}
			}
		}
	}
	if !found {
		idx[n] = maxNum - 1
		n++
	}
	gtMax := 4
	if cat == 3 {
		gtMax = 3
	}
	eq1, gt1 := 0, 0
	for k := n - 1; k >= 0; k-- {
		inc := 0
		if gt1 != 0 {
			inc = 0
		} else {
			inc = min(4, 1+eq1)
		}
		a := 0
		if c.bin(absB+inc) != 0 {
			ctx2 := absB + 5 + min(gtMax, gt1)
			a = 1
			for a < 14 && c.bin(ctx2) != 0 {
				a++
			}
			if a >= 14 {
				kk := 0
				for c.bypass() != 0 {
					a += 1 << uint(kk)
					kk++
					if kk > 24 {
						break
					}
				}
				for kk > 0 {
					kk--
					a += c.bypass() << uint(kk)
				}
			}
		}
		v := int32(a + 1)
		if c.bypass() != 0 {
			v = -v
		}
		lv[idx[k]] = v
		if a == 0 {
			eq1++
		} else {
			gt1++
		}
	}
	return n
}
