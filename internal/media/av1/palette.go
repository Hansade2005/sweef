package av1

import "sort"

// ---- palette (5.11.46, 5.11.49, 5.11.50) --------------------------------------

func (f *frameDec) getPaletteCache(plane int) ([16]uint16, int) {
	var cache [16]uint16
	aboveN, leftN := 0, 0
	var aboveColors, leftColors []uint16
	if (f.miRow*4)%64 != 0 && f.availU {
		idx := (f.miRow-1)*f.miCols + f.miCol
		aboveN = int(f.mi[idx].paletteSize[plane])
		aboveColors = f.palColors[plane][idx*8 : idx*8+8]
	}
	if f.availL {
		idx := f.miRow*f.miCols + f.miCol - 1
		leftN = int(f.mi[idx].paletteSize[plane])
		leftColors = f.palColors[plane][idx*8 : idx*8+8]
	}
	ai, li, n := 0, 0, 0
	for ai < aboveN && li < leftN {
		aboveC := aboveColors[ai]
		leftC := leftColors[li]
		if leftC < aboveC {
			if n == 0 || leftC != cache[n-1] {
				cache[n] = leftC
				n++
			}
			li++
		} else {
			if n == 0 || aboveC != cache[n-1] {
				cache[n] = aboveC
				n++
			}
			ai++
			if leftC == aboveC {
				li++
			}
		}
	}
	for ai < aboveN {
		v := aboveColors[ai]
		ai++
		if n == 0 || v != cache[n-1] {
			cache[n] = v
			n++
		}
	}
	for li < leftN {
		v := leftColors[li]
		li++
		if n == 0 || v != cache[n-1] {
			cache[n] = v
			n++
		}
	}
	return cache, n
}

func (f *frameDec) clip1(v int) int {
	return clip3(0, (1<<uint(f.bitDepth))-1, v)
}

func (f *frameDec) paletteModeInfo() {
	bsizeCtx := miWidthLog2[f.miSize] + miHeightLog2[f.miSize] - 2
	bd := f.bitDepth
	if f.palColors[0] == nil {
		f.palColors[0] = make([]uint16, f.miRows*f.miCols*8)
		f.palColors[1] = make([]uint16, f.miRows*f.miCols*8)
	}
	if f.yMode == dcPred {
		ctx := 0
		if f.availU && f.miAt(f.miRow-1, f.miCol).paletteSize[0] > 0 {
			ctx++
		}
		if f.availL && f.miAt(f.miRow, f.miCol-1).paletteSize[0] > 0 {
			ctx++
		}
		if f.sym.symbol(f.tcdf.paletteYMode[bsizeCtx][ctx][:]) != 0 {
			f.paletteSizeY = f.sym.symbol(f.tcdf.paletteYSize[bsizeCtx][:]) + 2
			cache, cacheN := f.getPaletteCache(0)
			idx := 0
			for i := 0; i < cacheN && idx < f.paletteSizeY; i++ {
				if f.sym.literal(1) != 0 {
					f.paletteColorsY[idx] = cache[i]
					idx++
				}
			}
			if idx < f.paletteSizeY {
				f.paletteColorsY[idx] = uint16(f.sym.literal(bd))
				idx++
			}
			paletteBits := 0
			if idx < f.paletteSizeY {
				minBits := bd - 3
				paletteBits = minBits + f.sym.literal(2)
			}
			for idx < f.paletteSizeY {
				delta := f.sym.literal(paletteBits) + 1
				f.paletteColorsY[idx] = uint16(f.clip1(int(f.paletteColorsY[idx-1]) + delta))
				rng := (1 << uint(bd)) - int(f.paletteColorsY[idx]) - 1
				paletteBits = imin(paletteBits, ceilLog2(rng))
				idx++
			}
			sortU16(f.paletteColorsY[:f.paletteSizeY])
		}
	}
	if f.hasChroma && f.uvMode == dcPred {
		ctx := b2i(f.paletteSizeY > 0)
		if f.sym.symbol(f.tcdf.paletteUVMode[ctx][:]) != 0 {
			f.paletteSizeUV = f.sym.symbol(f.tcdf.paletteUVSize[bsizeCtx][:]) + 2
			cache, cacheN := f.getPaletteCache(1)
			idx := 0
			for i := 0; i < cacheN && idx < f.paletteSizeUV; i++ {
				if f.sym.literal(1) != 0 {
					f.paletteColorsU[idx] = cache[i]
					idx++
				}
			}
			if idx < f.paletteSizeUV {
				f.paletteColorsU[idx] = uint16(f.sym.literal(bd))
				idx++
			}
			paletteBits := 0
			if idx < f.paletteSizeUV {
				minBits := bd - 3
				paletteBits = minBits + f.sym.literal(2)
			}
			for idx < f.paletteSizeUV {
				delta := f.sym.literal(paletteBits)
				f.paletteColorsU[idx] = uint16(f.clip1(int(f.paletteColorsU[idx-1]) + delta))
				rng := (1 << uint(bd)) - int(f.paletteColorsU[idx])
				paletteBits = imin(paletteBits, ceilLog2(rng))
				idx++
			}
			sortU16(f.paletteColorsU[:f.paletteSizeUV])
			if f.sym.literal(1) != 0 { // delta_encode_palette_colors_v
				minBits := bd - 4
				maxVal := 1 << uint(bd)
				bits := minBits + f.sym.literal(2)
				f.paletteColorsV[0] = uint16(f.sym.literal(bd))
				for idx := 1; idx < f.paletteSizeUV; idx++ {
					delta := f.sym.literal(bits)
					if delta != 0 {
						if f.sym.literal(1) != 0 {
							delta = -delta
						}
					}
					val := int(f.paletteColorsV[idx-1]) + delta
					if val < 0 {
						val += maxVal
					}
					if val >= maxVal {
						val -= maxVal
					}
					f.paletteColorsV[idx] = uint16(f.clip1(val))
				}
			} else {
				for idx := 0; idx < f.paletteSizeUV; idx++ {
					f.paletteColorsV[idx] = uint16(f.sym.literal(bd))
				}
			}
		}
	}
}

func sortU16(a []uint16) {
	sort.Slice(a, func(i, j int) bool { return a[i] < a[j] })
}

func (f *frameDec) paletteColorContext(colorMap []uint8, stride, r, c, n int) (order [8]uint8, ctx int) {
	var scores [8]int
	for i := 0; i < 8; i++ {
		order[i] = uint8(i)
	}
	if c > 0 {
		scores[colorMap[r*stride+c-1]] += 2
	}
	if r > 0 && c > 0 {
		scores[colorMap[(r-1)*stride+c-1]]++
	}
	if r > 0 {
		scores[colorMap[(r-1)*stride+c]] += 2
	}
	for i := 0; i < 3; i++ {
		maxScore := scores[i]
		maxIdx := i
		for j := i + 1; j < n; j++ {
			if scores[j] > maxScore {
				maxScore = scores[j]
				maxIdx = j
			}
		}
		if maxIdx != i {
			maxScore = scores[maxIdx]
			maxOrder := order[maxIdx]
			for k := maxIdx; k > i; k-- {
				scores[k] = scores[k-1]
				order[k] = order[k-1]
			}
			scores[i] = maxScore
			order[i] = maxOrder
		}
	}
	hash := 0
	for i := 0; i < 3; i++ {
		hash += scores[i] * paletteColorHashMultipliers[i]
	}
	return order, paletteColorContext[hash]
}

func (f *frameDec) paletteColorCdf(plane, n, ctx int) []uint16 {
	c := &f.tcdf
	if plane == 0 {
		switch n {
		case 2:
			return c.paletteSize2YColor[ctx][:]
		case 3:
			return c.paletteSize3YColor[ctx][:]
		case 4:
			return c.paletteSize4YColor[ctx][:]
		case 5:
			return c.paletteSize5YColor[ctx][:]
		case 6:
			return c.paletteSize6YColor[ctx][:]
		case 7:
			return c.paletteSize7YColor[ctx][:]
		}
		return c.paletteSize8YColor[ctx][:]
	}
	switch n {
	case 2:
		return c.paletteSize2UVColor[ctx][:]
	case 3:
		return c.paletteSize3UVColor[ctx][:]
	case 4:
		return c.paletteSize4UVColor[ctx][:]
	case 5:
		return c.paletteSize5UVColor[ctx][:]
	case 6:
		return c.paletteSize6UVColor[ctx][:]
	case 7:
		return c.paletteSize7UVColor[ctx][:]
	}
	return c.paletteSize8UVColor[ctx][:]
}

func (f *frameDec) readColorMap(cm []uint8, plane, n, blockW, blockH, onW, onH int) {
	const stride = 64
	cm[0] = uint8(f.sym.ns(n))
	for i := 1; i < onH+onW-1; i++ {
		for j := imin(i, onW-1); j >= imax(0, i-onH+1); j-- {
			order, ctx := f.paletteColorContext(cm, stride, i-j, j, n)
			idx := f.sym.symbol(f.paletteColorCdf(plane, n, ctx))
			cm[(i-j)*stride+j] = order[idx]
		}
	}
	for i := 0; i < onH; i++ {
		for j := onW; j < blockW; j++ {
			cm[i*stride+j] = cm[i*stride+onW-1]
		}
	}
	for i := onH; i < blockH; i++ {
		for j := 0; j < blockW; j++ {
			cm[i*stride+j] = cm[(onH-1)*stride+j]
		}
	}
}

func (f *frameDec) paletteTokens() {
	blockH := blockHeight(f.miSize)
	blockW := blockWidth(f.miSize)
	onH := imin(blockH, (f.miRows-f.miRow)*4)
	onW := imin(blockW, (f.miCols-f.miCol)*4)
	if f.paletteSizeY != 0 {
		f.readColorMap(f.colorMapY[:], 0, f.paletteSizeY, blockW, blockH, onW, onH)
	}
	if f.paletteSizeUV != 0 {
		blockH >>= uint(f.subY)
		blockW >>= uint(f.subX)
		onH >>= uint(f.subY)
		onW >>= uint(f.subX)
		if blockW < 4 {
			blockW += 2
			onW += 2
		}
		if blockH < 4 {
			blockH += 2
			onH += 2
		}
		f.readColorMap(f.colorMapUV[:], 1, f.paletteSizeUV, blockW, blockH, onW, onH)
	}
}
