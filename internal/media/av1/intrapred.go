package av1

// ---- intra prediction (spec 7.11.2) ------------------------------------------

func smWeights(log2 int) []int {
	switch log2 {
	case 2:
		return smWeightsTx4x4[:]
	case 3:
		return smWeightsTx8x8[:]
	case 4:
		return smWeightsTx16x16[:]
	case 5:
		return smWeightsTx32x32[:]
	}
	return smWeightsTx64x64[:]
}

func (f *frameDec) isSmooth(row, col, plane int) bool {
	m := f.miAt(row, col)
	var mode int
	if plane == 0 {
		mode = int(m.yMode)
	} else {
		if m.refFrame[0] > intraFrame {
			return false
		}
		mode = int(m.uvMode)
	}
	return mode == smoothPred || mode == smoothVPred || mode == smoothHPred
}

func (f *frameDec) getFilterType(plane int) int {
	aboveSmooth, leftSmooth := false, false
	avU, avL := f.availU, f.availL
	if plane > 0 {
		avU, avL = f.availUChroma, f.availLChroma
	}
	if avU {
		r := f.miRow - 1
		c := f.miCol
		if plane > 0 {
			if f.subX != 0 && c&1 == 0 {
				c++
			}
			if f.subY != 0 && f.miRow&1 != 0 {
				r--
			}
		}
		aboveSmooth = f.isSmooth(r, c, plane)
	}
	if avL {
		r := f.miRow
		c := f.miCol - 1
		if plane > 0 {
			if f.subX != 0 && f.miCol&1 != 0 {
				c--
			}
			if f.subY != 0 && r&1 == 0 {
				r++
			}
		}
		leftSmooth = f.isSmooth(r, c, plane)
	}
	return b2i(aboveSmooth || leftSmooth)
}

func intraEdgeFilterStrength(w, h, filterType, delta int) int {
	d := abs(delta)
	blkWh := w + h
	strength := 0
	if filterType == 0 {
		if blkWh <= 8 {
			if d >= 56 {
				strength = 1
			}
		} else if blkWh <= 12 {
			if d >= 40 {
				strength = 1
			}
		} else if blkWh <= 16 {
			if d >= 40 {
				strength = 1
			}
		} else if blkWh <= 24 {
			if d >= 8 {
				strength = 1
			}
			if d >= 16 {
				strength = 2
			}
			if d >= 32 {
				strength = 3
			}
		} else if blkWh <= 32 {
			strength = 1
			if d >= 4 {
				strength = 2
			}
			if d >= 32 {
				strength = 3
			}
		} else {
			strength = 3
		}
	} else {
		if blkWh <= 8 {
			if d >= 40 {
				strength = 1
			}
			if d >= 64 {
				strength = 2
			}
		} else if blkWh <= 16 {
			if d >= 20 {
				strength = 1
			}
			if d >= 48 {
				strength = 2
			}
		} else if blkWh <= 24 {
			if d >= 4 {
				strength = 3
			}
		} else {
			strength = 3
		}
	}
	return strength
}

func intraEdgeUpsample(w, h, filterType, delta int) bool {
	d := abs(delta)
	blkWh := w + h
	if d <= 0 || d >= 40 {
		return false
	}
	if filterType == 0 {
		return blkWh <= 16
	}
	return blkWh <= 8
}

// edgeFilter applies the intra edge filter (7.11.2.12) to buf, whose element
// -1 is at buf[off-1]. sz counts entries starting at index -1.
func edgeFilter(buf []int32, off, sz, strength int) {
	if strength == 0 {
		return
	}
	var edge [160]int32
	for i := 0; i < sz; i++ {
		edge[i] = buf[off+i-1]
	}
	k := &intraEdgeKernel[strength-1]
	for i := 1; i < sz; i++ {
		s := 0
		for j := 0; j < 5; j++ {
			kk := clip3(0, sz-1, i-2+j)
			s += k[j] * int(edge[kk])
		}
		buf[off+i-1] = int32((s + 8) >> 4)
	}
}

func (f *frameDec) edgeUpsample(buf []int32, off, numPx int) {
	var dup [64]int32
	dup[0] = buf[off-1]
	for i := -1; i < numPx; i++ {
		dup[i+2] = buf[off+i]
	}
	dup[numPx+2] = buf[off+numPx-1]
	buf[off-2] = dup[0]
	maxV := int32(1)<<uint(f.bitDepth) - 1
	for i := 0; i < numPx; i++ {
		s := -dup[i] + 9*dup[i+1] + 9*dup[i+2] - dup[i+3]
		s = (s + 8) >> 4
		if s < 0 {
			s = 0
		} else if s > maxV {
			s = maxV
		}
		buf[off+2*i-1] = s
		buf[off+2*i] = dup[i+2]
	}
}

func (f *frameDec) predictIntra(plane, x, y int, haveLeft, haveAbove, haveAboveRight, haveBelowLeft bool, mode, log2W, log2H int) {
	w := 1 << uint(log2W)
	h := 1 << uint(log2H)
	bd := f.bitDepth
	maxX := f.miCols*4 - 1
	maxY := f.miRows*4 - 1
	if plane > 0 {
		maxX = ((f.miCols * 4) >> uint(f.subX)) - 1
		maxY = ((f.miRows * 4) >> uint(f.subY)) - 1
	}
	pl := f.cur.pl[plane]
	stride := f.cur.stride[plane]
	const off = 16
	above := f.edgeAbove[:]
	left := f.edgeLeft[:]
	n := w + h
	if !haveAbove && haveLeft {
		v := int32(pl[y*stride+x-1])
		for i := 0; i < n; i++ {
			above[off+i] = v
		}
	} else if !haveAbove && !haveLeft {
		v := int32(1<<uint(bd-1)) - 1
		for i := 0; i < n; i++ {
			above[off+i] = v
		}
	} else {
		lim := x + w - 1
		if haveAboveRight {
			lim = x + 2*w - 1
		}
		aboveLimit := imin(maxX, lim)
		row := pl[(y-1)*stride:]
		for i := 0; i < n; i++ {
			above[off+i] = int32(row[imin(aboveLimit, x+i)])
		}
	}
	if !haveLeft && haveAbove {
		v := int32(pl[(y-1)*stride+x])
		for i := 0; i < n; i++ {
			left[off+i] = v
		}
	} else if !haveLeft && !haveAbove {
		v := int32(1<<uint(bd-1)) + 1
		for i := 0; i < n; i++ {
			left[off+i] = v
		}
	} else {
		lim := y + h - 1
		if haveBelowLeft {
			lim = y + 2*h - 1
		}
		leftLimit := imin(maxY, lim)
		for i := 0; i < n; i++ {
			left[off+i] = int32(pl[imin(leftLimit, y+i)*stride+x-1])
		}
	}
	switch {
	case haveAbove && haveLeft:
		above[off-1] = int32(pl[(y-1)*stride+x-1])
	case haveAbove:
		above[off-1] = int32(pl[(y-1)*stride+x])
	case haveLeft:
		above[off-1] = int32(pl[y*stride+x-1])
	default:
		above[off-1] = int32(1 << uint(bd-1))
	}
	left[off-1] = above[off-1]

	dst := pl[y*stride+x:]
	maxPix := int32(1)<<uint(bd) - 1
	if plane == 0 && f.useFilterIntra {
		// recursive intra prediction
		w4 := w >> 2
		h2 := h >> 1
		taps := &intraFilterTaps[f.filterIntraMode]
		for i2 := 0; i2 < h2; i2++ {
			for j4 := 0; j4 < w4; j4++ {
				var p [7]int
				for i := 0; i < 7; i++ {
					if i < 5 {
						if i2 == 0 {
							p[i] = int(above[off+(j4<<2)+i-1])
						} else if j4 == 0 && i == 0 {
							p[i] = int(left[off+(i2<<1)-1])
						} else {
							p[i] = int(dst[((i2<<1)-1)*stride+(j4<<2)+i-1])
						}
					} else {
						if j4 == 0 {
							p[i] = int(left[off+(i2<<1)+i-5])
						} else {
							p[i] = int(dst[((i2<<1)+i-5)*stride+(j4<<2)-1])
						}
					}
				}
				for i1 := 0; i1 < 2; i1++ {
					for j1 := 0; j1 < 4; j1++ {
						pr := 0
						t := &taps[(i1<<2)+j1]
						for i := 0; i < 7; i++ {
							pr += t[i] * p[i]
						}
						v := round2Signed(pr, 4)
						if v < 0 {
							v = 0
						} else if v > int(maxPix) {
							v = int(maxPix)
						}
						dst[((i2<<1)+i1)*stride+(j4<<2)+j1] = uint16(v)
					}
				}
			}
		}
		return
	}
	if isDirectionalMode(mode) {
		f.directionalPred(plane, x, y, haveLeft, haveAbove, mode, w, h, maxX, maxY, dst, stride)
		return
	}
	switch mode {
	case smoothPred:
		cov(covSmooth)
		wx := smWeights(log2W)
		wy := smWeights(log2H)
		bl := int(left[off+h-1])
		tr := int(above[off+w-1])
		for i := 0; i < h; i++ {
			for j := 0; j < w; j++ {
				s := wy[i]*int(above[off+j]) + (256-wy[i])*bl + wx[j]*int(left[off+i]) + (256-wx[j])*tr
				dst[i*stride+j] = uint16((s + 256) >> 9)
			}
		}
	case smoothVPred:
		wy := smWeights(log2H)
		bl := int(left[off+h-1])
		for i := 0; i < h; i++ {
			for j := 0; j < w; j++ {
				s := wy[i]*int(above[off+j]) + (256-wy[i])*bl
				dst[i*stride+j] = uint16((s + 128) >> 8)
			}
		}
	case smoothHPred:
		wx := smWeights(log2W)
		tr := int(above[off+w-1])
		for i := 0; i < h; i++ {
			for j := 0; j < w; j++ {
				s := wx[j]*int(left[off+i]) + (256-wx[j])*tr
				dst[i*stride+j] = uint16((s + 128) >> 8)
			}
		}
	case dcPred:
		var v int
		if haveLeft && haveAbove {
			sum := 0
			for k := 0; k < h; k++ {
				sum += int(left[off+k])
			}
			for k := 0; k < w; k++ {
				sum += int(above[off+k])
			}
			sum += (w + h) >> 1
			v = sum / (w + h)
		} else if haveLeft {
			sum := 0
			for k := 0; k < h; k++ {
				sum += int(left[off+k])
			}
			v = f.clip1((sum + (h >> 1)) >> uint(log2H))
		} else if haveAbove {
			sum := 0
			for k := 0; k < w; k++ {
				sum += int(above[off+k])
			}
			v = f.clip1((sum + (w >> 1)) >> uint(log2W))
		} else {
			v = 1 << uint(bd-1)
		}
		for i := 0; i < h; i++ {
			row := dst[i*stride : i*stride+w]
			for j := range row {
				row[j] = uint16(v)
			}
		}
	default: // paeth
		cov(covPaeth)
		tl := int(above[off-1])
		for i := 0; i < h; i++ {
			l := int(left[off+i])
			for j := 0; j < w; j++ {
				a := int(above[off+j])
				base := a + l - tl
				pLeft := abs(base - l)
				pTop := abs(base - a)
				pTL := abs(base - tl)
				var v int
				if pLeft <= pTop && pLeft <= pTL {
					v = l
				} else if pTop <= pTL {
					v = a
				} else {
					v = tl
				}
				dst[i*stride+j] = uint16(v)
			}
		}
	}
}

func (f *frameDec) directionalPred(plane, x, y int, haveLeft, haveAbove bool, mode, w, h, maxX, maxY int, dst []uint16, stride int) {
	cov(covDirectional)
	const off = 16
	above := f.edgeAbove[:]
	left := f.edgeLeft[:]
	angleDelta := f.angleDeltaY
	if plane != 0 {
		angleDelta = f.angleDeltaUV
	}
	pAngle := modeToAngle[mode] + angleDelta*3
	upsampleAbove, upsampleLeft := 0, 0
	if f.seq.enableIntraEdge {
		cov(covIntraEdgeFilter)
		filterType := 0
		if pAngle != 90 && pAngle != 180 {
			if pAngle > 90 && pAngle < 180 && (w+h) >= 24 {
				s := int(left[off])*5 + int(above[off-1])*6 + int(above[off])*5
				v := int32((s + 8) >> 4)
				left[off-1] = v
				above[off-1] = v
			}
			filterType = f.getFilterType(plane)
			if haveAbove {
				strength := intraEdgeFilterStrength(w, h, filterType, pAngle-90)
				numPx := imin(w, maxX-x+1) + 1
				if pAngle < 90 {
					numPx += h
				}
				edgeFilter(above, off, numPx, strength)
			}
			if haveLeft {
				strength := intraEdgeFilterStrength(w, h, filterType, pAngle-180)
				numPx := imin(h, maxY-y+1) + 1
				if pAngle > 180 {
					numPx += w
				}
				edgeFilter(left, off, numPx, strength)
			}
		}
		if intraEdgeUpsample(w, h, filterType, pAngle-90) {
			upsampleAbove = 1
			cov(covEdgeUpsample)
			numPx := w
			if pAngle < 90 {
				numPx += h
			}
			f.edgeUpsample(above, off, numPx)
		}
		if intraEdgeUpsample(w, h, filterType, pAngle-180) {
			upsampleLeft = 1
			numPx := h
			if pAngle > 180 {
				numPx += w
			}
			f.edgeUpsample(left, off, numPx)
		}
	}
	var dx, dy int
	if pAngle < 90 {
		dx = drIntraDerivative[pAngle]
	} else if pAngle > 90 && pAngle < 180 {
		dx = drIntraDerivative[180-pAngle]
	}
	if pAngle > 90 && pAngle < 180 {
		dy = drIntraDerivative[pAngle-90]
	} else if pAngle > 180 {
		dy = drIntraDerivative[270-pAngle]
	}
	switch {
	case pAngle < 90:
		maxBaseX := (w + h - 1) << uint(upsampleAbove)
		for i := 0; i < h; i++ {
			idx := (i + 1) * dx
			shift := ((idx << uint(upsampleAbove)) >> 1) & 0x1F
			baseI := idx >> uint(6-upsampleAbove)
			for j := 0; j < w; j++ {
				base := baseI + (j << uint(upsampleAbove))
				var v int32
				if base < maxBaseX {
					v = (above[off+base]*int32(32-shift) + above[off+base+1]*int32(shift) + 16) >> 5
				} else {
					v = above[off+maxBaseX]
				}
				dst[i*stride+j] = uint16(v)
			}
		}
	case pAngle > 90 && pAngle < 180:
		for i := 0; i < h; i++ {
			for j := 0; j < w; j++ {
				idx := (j << 6) - (i+1)*dx
				base := idx >> uint(6-upsampleAbove)
				var v int32
				if base >= -(1 << uint(upsampleAbove)) {
					shift := ((idx << uint(upsampleAbove)) >> 1) & 0x1F
					v = (above[off+base]*int32(32-shift) + above[off+base+1]*int32(shift) + 16) >> 5
				} else {
					idx = (i << 6) - (j+1)*dy
					base = idx >> uint(6-upsampleLeft)
					shift := ((idx << uint(upsampleLeft)) >> 1) & 0x1F
					v = (left[off+base]*int32(32-shift) + left[off+base+1]*int32(shift) + 16) >> 5
				}
				dst[i*stride+j] = uint16(v)
			}
		}
	case pAngle > 180:
		for j := 0; j < w; j++ {
			idx := (j + 1) * dy
			shift := ((idx << uint(upsampleLeft)) >> 1) & 0x1F
			baseJ := idx >> uint(6-upsampleLeft)
			for i := 0; i < h; i++ {
				base := baseJ + (i << uint(upsampleLeft))
				v := (left[off+base]*int32(32-shift) + left[off+base+1]*int32(shift) + 16) >> 5
				dst[i*stride+j] = uint16(v)
			}
		}
	case pAngle == 90:
		for i := 0; i < h; i++ {
			for j := 0; j < w; j++ {
				dst[i*stride+j] = uint16(above[off+j])
			}
		}
	default: // 180
		for i := 0; i < h; i++ {
			v := uint16(left[off+i])
			for j := 0; j < w; j++ {
				dst[i*stride+j] = v
			}
		}
	}
}

// ---- palette and chroma-from-luma prediction (7.11.4, 7.11.5) ------------------

func (f *frameDec) predictPalette(plane, startX, startY, x, y, txSz int) {
	w := txWidth[txSz]
	h := txHeight[txSz]
	var palette *[8]uint16
	var cm []uint8
	switch plane {
	case 0:
		palette = &f.paletteColorsY
		cm = f.colorMapY[:]
	case 1:
		palette = &f.paletteColorsU
		cm = f.colorMapUV[:]
	default:
		palette = &f.paletteColorsV
		cm = f.colorMapUV[:]
	}
	pl := f.cur.pl[plane]
	stride := f.cur.stride[plane]
	for i := 0; i < h; i++ {
		for j := 0; j < w; j++ {
			pl[(startY+i)*stride+startX+j] = palette[cm[(y*4+i)*64+x*4+j]]
		}
	}
}

func (f *frameDec) predictChromaFromLuma(plane, startX, startY, txSz int) {
	w := txWidth[txSz]
	h := txHeight[txSz]
	subX, subY := f.subX, f.subY
	alpha := f.cflAlphaU
	if plane == 2 {
		alpha = f.cflAlphaV
	}
	var L [32 * 32]int32
	lumaAvg := 0
	luma := f.cur.pl[0]
	ls := f.cur.stride[0]
	for i := 0; i < h; i++ {
		lumaY := (startY + i) << uint(subY)
		lumaY = imin(lumaY, f.maxLumaH-(1<<uint(subY)))
		for j := 0; j < w; j++ {
			lumaX := (startX + j) << uint(subX)
			lumaX = imin(lumaX, f.maxLumaW-(1<<uint(subX)))
			t := 0
			for dy := 0; dy <= subY; dy++ {
				for dx := 0; dx <= subX; dx++ {
					t += int(luma[(lumaY+dy)*ls+lumaX+dx])
				}
			}
			v := t << uint(3-subX-subY)
			L[i*32+j] = int32(v)
			lumaAvg += v
		}
	}
	lumaAvg = round2(lumaAvg, uint(txWidthLog2[txSz]+txHeightLog2[txSz]))
	pl := f.cur.pl[plane]
	stride := f.cur.stride[plane]
	for i := 0; i < h; i++ {
		for j := 0; j < w; j++ {
			dc := int(pl[(startY+i)*stride+startX+j])
			scaled := round2Signed(alpha*(int(L[i*32+j])-lumaAvg), 6)
			pl[(startY+i)*stride+startX+j] = uint16(f.clip1(dc + scaled))
		}
	}
}
