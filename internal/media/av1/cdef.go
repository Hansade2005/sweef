package av1

// ---- CDEF (spec 7.15) -----------------------------------------------------------

func copyFrameBuf(src *frameBuf) *frameBuf {
	dst := &frameBuf{w: src.w, h: src.h, stride: src.stride}
	for p := 0; p < 3; p++ {
		if src.pl[p] != nil {
			dst.pl[p] = make([]uint16, len(src.pl[p]))
			copy(dst.pl[p], src.pl[p])
		}
	}
	return dst
}

func (f *frameDec) cdefProcess() *frameBuf {
	fh := f.fh
	if !f.seq.enableCdef || fh.codedLossless || fh.allowIntrabc {
		return f.cur
	}
	any := false
	for _, v := range f.cdefIdx {
		if v != -1 {
			any = true
			break
		}
	}
	if !any {
		return f.cur
	}
	cov(covCdef)
	out := copyFrameBuf(f.cur)
	for r := 0; r < f.miRows; r += 2 {
		for c := 0; c < f.miCols; c += 2 {
			idx := int(f.cdefIdx[(r>>4)*f.cdefStride+(c>>4)])
			f.cdefBlock(out, r, c, idx)
		}
	}
	return out
}

func (f *frameDec) cdefBlock(out *frameBuf, r, c, idx int) {
	if idx == -1 {
		return
	}
	fh := f.fh
	coeffShift := uint(f.bitDepth - 8)
	skip := f.miAt(r, c).skip && f.miAt(r+1, c).skip && f.miAt(r, c+1).skip && f.miAt(r+1, c+1).skip
	if skip {
		return
	}
	yDir, variance := f.cdefDirection(r, c)
	priStr := fh.cdefYPri[idx] << coeffShift
	secStr := fh.cdefYSec[idx] << coeffShift
	dir := 0
	if priStr != 0 {
		dir = yDir
	}
	varStr := 0
	if (variance >> 6) != 0 {
		varStr = imin(floorLog2(variance>>6), 12)
	}
	if variance != 0 {
		priStr = (priStr*(4+varStr) + 8) >> 4
	} else {
		priStr = 0
	}
	damping := fh.cdefDamping + int(coeffShift)
	f.cdefFilter(out, 0, r, c, priStr, secStr, damping, dir)
	if f.numPlanes == 1 {
		return
	}
	priStr = fh.cdefUVPri[idx] << coeffShift
	secStr = fh.cdefUVSec[idx] << coeffShift
	dir = 0
	if priStr != 0 {
		dir = cdefUvDir[f.subX][f.subY][yDir]
	}
	damping = fh.cdefDamping + int(coeffShift) - 1
	f.cdefFilter(out, 1, r, c, priStr, secStr, damping, dir)
	f.cdefFilter(out, 2, r, c, priStr, secStr, damping, dir)
}

func (f *frameDec) cdefDirection(r, c int) (yDir, variance int) {
	var cost [8]int
	var partial [8][15]int
	x0 := c << 2
	y0 := r << 2
	pl := f.cur.pl[0]
	stride := f.cur.stride[0]
	sh := uint(f.bitDepth - 8)
	for i := 0; i < 8; i++ {
		row := pl[(y0+i)*stride+x0 : (y0+i)*stride+x0+8]
		for j := 0; j < 8; j++ {
			x := (int(row[j]) >> sh) - 128
			partial[0][i+j] += x
			partial[1][i+j/2] += x
			partial[2][i] += x
			partial[3][3+i-j/2] += x
			partial[4][7+i-j] += x
			partial[5][3-i/2+j] += x
			partial[6][j] += x
			partial[7][i/2+j] += x
		}
	}
	for i := 0; i < 8; i++ {
		cost[2] += partial[2][i] * partial[2][i]
		cost[6] += partial[6][i] * partial[6][i]
	}
	cost[2] *= divTable[8]
	cost[6] *= divTable[8]
	for i := 0; i < 7; i++ {
		cost[0] += (partial[0][i]*partial[0][i] + partial[0][14-i]*partial[0][14-i]) * divTable[i+1]
		cost[4] += (partial[4][i]*partial[4][i] + partial[4][14-i]*partial[4][14-i]) * divTable[i+1]
	}
	cost[0] += partial[0][7] * partial[0][7] * divTable[8]
	cost[4] += partial[4][7] * partial[4][7] * divTable[8]
	for i := 1; i < 8; i += 2 {
		for j := 0; j < 5; j++ {
			cost[i] += partial[i][3+j] * partial[i][3+j]
		}
		cost[i] *= divTable[8]
		for j := 0; j < 3; j++ {
			cost[i] += (partial[i][j]*partial[i][j] + partial[i][10-j]*partial[i][10-j]) * divTable[2*j+2]
		}
	}
	bestCost := 0
	for i := 0; i < 8; i++ {
		if cost[i] > bestCost {
			bestCost = cost[i]
			yDir = i
		}
	}
	variance = (bestCost - cost[(yDir+4)&7]) >> 10
	return
}

func cdefConstrain(diff, threshold, damping int) int {
	if threshold == 0 {
		return 0
	}
	dampingAdj := imax(0, damping-floorLog2(threshold))
	a := diff
	if a < 0 {
		a = -a
	}
	v := clip3(0, a, threshold-(a>>uint(dampingAdj)))
	if diff < 0 {
		return -v
	}
	return v
}

func (f *frameDec) cdefFilter(out *frameBuf, plane, r, c, priStr, secStr, damping, dir int) {
	if priStr == 0 && secStr == 0 {
		return
	}
	coeffShift := uint(f.bitDepth - 8)
	subX, subY := 0, 0
	if plane > 0 {
		subX, subY = f.subX, f.subY
	}
	x0 := (c * 4) >> uint(subX)
	y0 := (r * 4) >> uint(subY)
	w := 8 >> uint(subX)
	h := 8 >> uint(subY)
	src := f.cur.pl[plane]
	stride := f.cur.stride[plane]
	dst := out.pl[plane]
	limW := (f.miCols * 4) >> uint(subX)
	limH := (f.miRows * 4) >> uint(subY)
	priTaps := &cdefPriTaps[(priStr>>coeffShift)&1]
	secTaps := &cdefSecTaps[(priStr>>coeffShift)&1]
	for i := 0; i < h; i++ {
		for j := 0; j < w; j++ {
			sum := 0
			x := int(src[(y0+i)*stride+x0+j])
			mx, mn := x, x
			for k := 0; k < 2; k++ {
				for sign := -1; sign <= 1; sign += 2 {
					yy := y0 + i + sign*cdefDirections[dir][k][0]
					xx := x0 + j + sign*cdefDirections[dir][k][1]
					if yy >= 0 && yy < limH && xx >= 0 && xx < limW {
						p := int(src[yy*stride+xx])
						sum += priTaps[k] * cdefConstrain(p-x, priStr, damping)
						if p > mx {
							mx = p
						}
						if p < mn {
							mn = p
						}
					}
					for dirOff := -2; dirOff <= 2; dirOff += 4 {
						d2 := (dir + dirOff) & 7
						yy := y0 + i + sign*cdefDirections[d2][k][0]
						xx := x0 + j + sign*cdefDirections[d2][k][1]
						if yy >= 0 && yy < limH && xx >= 0 && xx < limW {
							s := int(src[yy*stride+xx])
							sum += secTaps[k] * cdefConstrain(s-x, secStr, damping)
							if s > mx {
								mx = s
							}
							if s < mn {
								mn = s
							}
						}
					}
				}
			}
			neg := 0
			if sum < 0 {
				neg = 1
			}
			dst[(y0+i)*stride+x0+j] = uint16(clip3(mn, mx, x+((8+sum-neg)>>4)))
		}
	}
}
