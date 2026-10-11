package h264

// CABAC syntax element decoding for the macroblock layer (9.3.2, 9.3.3.1).

func (d *Decoder) nbIdxA() int {
	if d.availA {
		return d.mbIdx - 1
	}
	return -1
}

func (d *Decoder) nbIdxB() int {
	if d.availB {
		return d.mbIdx - d.mbW
	}
	return -1
}

func (d *Decoder) cabacSkipFlag() bool {
	inc := 0
	if a := d.nbIdxA(); a >= 0 && d.mbType[a] != mtPSkip && d.mbType[a] != mtBSkip {
		inc++
	}
	if b := d.nbIdxB(); b >= 0 && d.mbType[b] != mtPSkip && d.mbType[b] != mtBSkip {
		inc++
	}
	base := 11
	if d.hdr.isB() {
		base = 24
	}
	return d.cab.bin(base+inc) != 0
}

// cabacIntraMBType decodes an intra mb_type (0..25). inI selects the I slice
// context set (offset 3, neighbour dependent first bin); otherwise base is 17
// (P) or 32 (B).
func (d *Decoder) cabacIntraMBType(inI bool, base int) int {
	c := &d.cab
	if inI {
		inc := 0
		if a := d.nbIdxA(); a >= 0 && d.mbType[a] != mtI4x4 {
			inc++
		}
		if b := d.nbIdxB(); b >= 0 && d.mbType[b] != mtI4x4 {
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
	chroma := 0
	if c.bin(cChroma) != 0 {
		chroma = 1 + c.bin(cChroma2)
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
	if a := d.nbIdxA(); a >= 0 && d.mbType[a] != mtBSkip && d.mbType[a] != mtBDirect {
		inc++
	}
	if b := d.nbIdxB(); b >= 0 && d.mbType[b] != mtBSkip && d.mbType[b] != mtBDirect {
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

// refIdxPositive reports whether the (non-direct) reference index of the 8x8
// block at absolute 8x8 coordinates is greater than zero.
func (d *Decoder) refIdxCond(list, ax8, ay8 int) int {
	if ax8 < 0 || ay8 < 0 {
		return 0
	}
	nmx, nmy := ax8>>1, ay8>>1
	var direct uint8
	if nmx == d.mbx && nmy == d.mby {
		direct = d.curDirect
	} else {
		ni := nmy*d.mbW + nmx
		if d.mbSlice[ni] != d.curSlice {
			return 0
		}
		direct = d.mbDirect[ni]
	}
	if direct&(1<<uint((ay8&1)*2+(ax8&1))) != 0 {
		return 0
	}
	if d.cur.refIdx[list][ay8*d.mbW*2+ax8] > 0 {
		return 1
	}
	return 0
}

func (d *Decoder) cabacRefIdx(list, bx, by int) int {
	c := &d.cab
	ax8 := d.mbx*2 + bx>>1
	ay8 := d.mby*2 + by>>1
	inc := d.refIdxCond(list, ax8-1, ay8) + 2*d.refIdxCond(list, ax8, ay8-1)
	if c.bin(54+inc) == 0 {
		return 0
	}
	v := 1
	ctx := 54 + 4
	for c.bin(ctx) != 0 {
		v++
		ctx = 54 + 5
		if v >= 32 {
			return -1
		}
	}
	return v
}

func (d *Decoder) mvdAt(list, ax, ay, comp int) int {
	if ax < 0 || ay < 0 {
		return 0
	}
	nmx, nmy := ax>>2, ay>>2
	if !(nmx == d.mbx && nmy == d.mby) && d.mbSlice[nmy*d.mbW+nmx] != d.curSlice {
		return 0
	}
	return int(d.mvd[list][ay*d.mbW*4+ax][comp])
}

func (d *Decoder) cabacMVD(list, comp, bx, by int) int {
	c := &d.cab
	ax, ay := d.mbx*4+bx, d.mby*4+by
	sum := d.mvdAt(list, ax-1, ay, comp) + d.mvdAt(list, ax, ay-1, comp)
	inc := 0
	if sum >= 3 {
		inc = 1
		if sum > 32 {
			inc = 2
		}
	}
	base := 40
	if comp == 1 {
		base = 47
	}
	if c.bin(base+inc) == 0 {
		return 0
	}
	v := 1
	ctx := 3
	for v < 9 && c.bin(base+ctx) != 0 {
		v++
		if ctx < 6 {
			ctx++
		}
	}
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
	}
	if c.bypass() != 0 {
		v = -v
	}
	return v
}

func (d *Decoder) cabacIntraChroma() int {
	c := &d.cab
	inc := 0
	if a := d.nbIdxA(); a >= 0 && d.mbChroma[a] != 0 {
		inc++
	}
	if b := d.nbIdxB(); b >= 0 && d.mbChroma[b] != 0 {
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

func (d *Decoder) cabacCBP() int {
	c := &d.cab
	cbp := 0
	a, b := d.nbIdxA(), d.nbIdxB()
	for b8 := 0; b8 < 4; b8++ {
		bx, by := b8&1, b8>>1
		condA, condB := 0, 0
		if bx > 0 {
			if (cbp>>uint(b8-1))&1 == 0 {
				condA = 1
			}
		} else if a >= 0 && (d.mbCBP[a]>>uint(by*2+1))&1 == 0 {
			condA = 1
		}
		if by > 0 {
			if (cbp>>uint(b8-2))&1 == 0 {
				condB = 1
			}
		} else if b >= 0 && (d.mbCBP[b]>>uint(2+bx))&1 == 0 {
			condB = 1
		}
		cbp |= c.bin(73+condA+2*condB) << uint(b8)
	}
	ca, cb := 0, 0
	if a >= 0 && d.mbCBP[a]>>4 != 0 {
		ca = 1
	}
	if b >= 0 && d.mbCBP[b]>>4 != 0 {
		cb = 1
	}
	if c.bin(77+ca+2*cb) == 0 {
		return cbp
	}
	ca, cb = 0, 0
	if a >= 0 && d.mbCBP[a]>>4 == 2 {
		ca = 1
	}
	if b >= 0 && d.mbCBP[b]>>4 == 2 {
		cb = 1
	}
	return cbp | (1+c.bin(77+4+ca+2*cb))<<4
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
	inc := 0
	if a := d.nbIdxA(); a >= 0 && d.mbT8[a] {
		inc++
	}
	if b := d.nbIdxB(); b >= 0 && d.mbT8[b] {
		inc++
	}
	return d.cab.bin(399+inc) != 0
}

// cabacBlock decodes one residual block (7.3.5.3.3). cbf is the coded_block_flag
// context increment, or -1 when the flag is not transmitted (8x8 luma).
func (d *Decoder) cabacBlock(cat, cbf, maxNum int, lv *[64]int32) int {
	c := &d.cab
	if cbf >= 0 {
		if c.bin(85+[5]int{0, 4, 8, 12, 16}[cat]+cbf) == 0 {
			return 0
		}
	}
	for i := 0; i < maxNum; i++ {
		lv[i] = 0
	}
	sigB := int(sigCoeffOffset[0][cat])
	lastB := int(lastCoeffOffset[0][cat])
	absB := int(absLevelOffset[cat])
	var idx [64]int
	n := 0
	found := false
	if cat == 5 {
		for i := 0; i < 63; i++ {
			if c.bin(sigB+int(sigCoeffOffset8x8[0][i])) != 0 {
				idx[n] = i
				n++
				if c.bin(lastB+int(lastCoeffOffset8x8[i])) != 0 {
					found = true
					break
				}
			}
		}
	} else {
		for i := 0; i < maxNum-1; i++ {
			inc := i
			if cat == 3 && i > 2 {
				inc = 2
			}
			if c.bin(sigB+inc) != 0 {
				idx[n] = i
				n++
				if c.bin(lastB+inc) != 0 {
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
			ctx2 := absB + 5 + min(4-btoi(cat == 3), gt1)
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
