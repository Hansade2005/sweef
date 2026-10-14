package vp9

// Intra and inter prediction (spec 8.5).

func (d *Decoder) predictIntra(plane, x, y int, haveLeft, haveAbove, notOnRight bool, txSz, blockIdx int) {
	b := &d.blk
	mode := b.uvMode
	if plane == 0 {
		if b.miSize >= block8x8 {
			mode = b.yMode
		} else {
			mode = int(b.subModes[blockIdx])
		}
	}
	size := 4 << uint(txSz)
	log2 := uint(txSz + 2)
	maxX := d.miCols*8 - 1
	maxY := d.miRows*8 - 1
	if plane > 0 {
		maxX = ((d.miCols * 8) >> uint(d.subX)) - 1
		maxY = ((d.miRows * 8) >> uint(d.subY)) - 1
	}
	fr := d.cur
	stride := fr.stride[plane]
	pix := fr.pix[plane]
	var abuf [65]int32 // abuf[k+1] = aboveRow[k]
	var left [32]int32
	base := int32(1) << uint(d.bitDepth-1)
	ab := abuf[1:]
	if !haveAbove {
		for i := 0; i < 2*size; i++ {
			ab[i] = base - 1
		}
	} else {
		row := pix[(y-1)*stride:]
		for i := 0; i < size; i++ {
			ab[i] = int32(row[min(maxX, x+i)])
		}
		if notOnRight && txSz == 0 {
			for i := size; i < 2*size; i++ {
				ab[i] = int32(row[min(maxX, x+i)])
			}
		} else {
			for i := size; i < 2*size; i++ {
				ab[i] = ab[size-1]
			}
		}
	}
	switch {
	case haveAbove && haveLeft:
		abuf[0] = int32(pix[(y-1)*stride+min(maxX, x-1)])
	case haveAbove:
		abuf[0] = base + 1
	default:
		abuf[0] = base - 1
	}
	if haveLeft {
		for i := 0; i < size; i++ {
			left[i] = int32(pix[min(maxY, y+i)*stride+x-1])
		}
	} else {
		for i := 0; i < size; i++ {
			left[i] = base + 1
		}
	}
	var pred [32 * 32]int32
	P := func(i, j int) *int32 { return &pred[i*size+j] }
	A := func(k int) int32 { return abuf[k+1] }
	r2 := func(v int32, n uint) int32 { return (v + (1 << n >> 1)) >> n }
	switch mode {
	case vPred:
		for i := 0; i < size; i++ {
			for j := 0; j < size; j++ {
				*P(i, j) = ab[j]
			}
		}
	case hPred:
		for i := 0; i < size; i++ {
			for j := 0; j < size; j++ {
				*P(i, j) = left[i]
			}
		}
	case d207Pred:
		for j := 0; j < size; j++ {
			*P(size-1, j) = left[size-1]
		}
		for i := 0; i < size-1; i++ {
			*P(i, 0) = r2(left[i]+left[i+1], 1)
		}
		for i := 0; i < size-2; i++ {
			*P(i, 1) = r2(left[i]+2*left[i+1]+left[i+2], 2)
		}
		*P(size-2, 1) = r2(left[size-2]+3*left[size-1], 2)
		for j := 2; j < size; j++ {
			for i := size - 2; i >= 0; i-- {
				*P(i, j) = *P(i+1, j-2)
			}
		}
	case d45Pred:
		for i := 0; i < size; i++ {
			for j := 0; j < size; j++ {
				if i+j+2 < size*2 {
					*P(i, j) = r2(ab[i+j]+ab[i+j+1]*2+ab[i+j+2], 2)
				} else {
					*P(i, j) = ab[2*size-1]
				}
			}
		}
	case d63Pred:
		for i := 0; i < size; i++ {
			i2 := i / 2
			for j := 0; j < size; j++ {
				if i&1 != 0 {
					*P(i, j) = r2(ab[i2+j]+ab[i2+j+1]*2+ab[i2+j+2], 2)
				} else {
					*P(i, j) = r2(ab[i2+j]+ab[i2+j+1], 1)
				}
			}
		}
	case d117Pred:
		for j := 0; j < size; j++ {
			*P(0, j) = r2(A(j-1)+A(j), 1)
		}
		*P(1, 0) = r2(left[0]+2*A(-1)+A(0), 2)
		for j := 1; j < size; j++ {
			*P(1, j) = r2(A(j-2)+2*A(j-1)+A(j), 2)
		}
		*P(2, 0) = r2(A(-1)+2*left[0]+left[1], 2)
		for i := 3; i < size; i++ {
			*P(i, 0) = r2(left[i-3]+2*left[i-2]+left[i-1], 2)
		}
		for i := 2; i < size; i++ {
			for j := 1; j < size; j++ {
				*P(i, j) = *P(i-2, j-1)
			}
		}
	case d135Pred:
		*P(0, 0) = r2(left[0]+2*A(-1)+A(0), 2)
		for j := 1; j < size; j++ {
			*P(0, j) = r2(A(j-2)+2*A(j-1)+A(j), 2)
		}
		*P(1, 0) = r2(A(-1)+2*left[0]+left[1], 2)
		for i := 2; i < size; i++ {
			*P(i, 0) = r2(left[i-2]+2*left[i-1]+left[i], 2)
		}
		for i := 1; i < size; i++ {
			for j := 1; j < size; j++ {
				*P(i, j) = *P(i-1, j-1)
			}
		}
	case d153Pred:
		*P(0, 0) = r2(left[0]+A(-1), 1)
		for i := 1; i < size; i++ {
			*P(i, 0) = r2(left[i-1]+left[i], 1)
		}
		*P(0, 1) = r2(left[0]+2*A(-1)+A(0), 2)
		*P(1, 1) = r2(A(-1)+2*left[0]+left[1], 2)
		for i := 2; i < size; i++ {
			*P(i, 1) = r2(left[i-2]+2*left[i-1]+left[i], 2)
		}
		for j := 2; j < size; j++ {
			*P(0, j) = r2(A(j-3)+2*A(j-2)+A(j-1), 2)
		}
		for i := 1; i < size; i++ {
			for j := 2; j < size; j++ {
				*P(i, j) = *P(i-1, j-2)
			}
		}
	case tmPred:
		for i := 0; i < size; i++ {
			for j := 0; j < size; j++ {
				v := ab[j] + left[i] - A(-1)
				if v < 0 {
					v = 0
				} else if v > d.maxV {
					v = d.maxV
				}
				*P(i, j) = v
			}
		}
	default: // DC
		var v int32
		switch {
		case haveLeft && haveAbove:
			var sum int32
			for k := 0; k < size; k++ {
				sum += left[k] + ab[k]
			}
			v = (sum + int32(size)) >> (log2 + 1)
		case haveLeft:
			var sum int32
			for k := 0; k < size; k++ {
				sum += left[k]
			}
			v = (sum + int32(1<<(log2-1))) >> log2
		case haveAbove:
			var sum int32
			for k := 0; k < size; k++ {
				sum += ab[k]
			}
			v = (sum + int32(1<<(log2-1))) >> log2
		default:
			v = base
		}
		for i := 0; i < size*size; i++ {
			pred[i] = v
		}
	}
	for i := 0; i < size; i++ {
		row := pix[(y+i)*stride+x : (y+i)*stride+x+size]
		for j := range row {
			row[j] = uint16(pred[i*size+j])
		}
	}
}

func (d *Decoder) clipPix(v int32) uint16 {
	if uint32(v) > uint32(d.maxV) {
		if v < 0 {
			return 0
		}
		return uint16(d.maxV)
	}
	return uint16(v)
}

func roundMvQ2(v int) int {
	if v < 0 {
		return (v - 1) / 2
	}
	return (v + 1) / 2
}

func roundMvQ4(v int) int {
	if v < 0 {
		return (v - 2) / 4
	}
	return (v + 2) / 4
}

func (d *Decoder) predictInter(plane, x, y, w, h, blockIdx int) {
	b := &d.blk
	isCompound := b.refFrame[1] > intraFrame
	nref := 1
	if isCompound {
		nref = 2
	}
	sx, sy := 0, 0
	if plane > 0 {
		sx, sy = d.subX, d.subY
	}
	for rl := 0; rl < nref; rl++ {
		// motion vector selection
		var mvr, mvc int
		bm := &b.blockMvs[rl]
		switch {
		case plane == 0 || b.miSize >= block8x8 || (d.subX == 0 && d.subY == 0):
			mvr, mvc = int(bm[blockIdx].row), int(bm[blockIdx].col)
		case d.subX == 0: // 4:4:0
			mvr = roundMvQ2(int(bm[blockIdx].row) + int(bm[blockIdx+2].row))
			mvc = roundMvQ2(int(bm[blockIdx].col) + int(bm[blockIdx+2].col))
		case d.subY == 0: // 4:2:2
			mvr = roundMvQ2(int(bm[blockIdx].row) + int(bm[blockIdx+1].row))
			mvc = roundMvQ2(int(bm[blockIdx].col) + int(bm[blockIdx+1].col))
		default:
			mvr = roundMvQ4(int(bm[0].row) + int(bm[1].row) + int(bm[2].row) + int(bm[3].row))
			mvc = roundMvQ4(int(bm[0].col) + int(bm[1].col) + int(bm[2].col) + int(bm[3].col))
		}
		// clamping
		bh := int(num8x8High[b.miSize])
		bw := int(num8x8Wide[b.miSize])
		mbToTop := -((b.miRow * 8) * 16) >> uint(sy)
		mbToBottom := (((d.miRows - bh - b.miRow) * 8) * 16) >> uint(sy)
		mbToLeft := -((b.miCol * 8) * 16) >> uint(sx)
		mbToRight := (((d.miCols - bw - b.miCol) * 8) * 16) >> uint(sx)
		spelLeft := (4 + ((bw * 8) >> uint(sx))) << 4
		spelRight := spelLeft - 16
		spelTop := (4 + ((bh * 8) >> uint(sy))) << 4
		spelBottom := spelTop - 16
		cmvR := clip3(mbToTop-spelTop, mbToBottom+spelBottom, (2*mvr)>>uint(sy))
		cmvC := clip3(mbToLeft-spelLeft, mbToRight+spelRight, (2*mvc)>>uint(sx))
		// scaling
		refFrame := b.refFrame[rl]
		if !d.refValid[refFrame-1] {
			panic(errBitstream)
		}
		ref := d.refs[d.refFrameIdx[refFrame-1]]
		xScale := (ref.w << 14) / d.width
		yScale := (ref.h << 14) / d.height
		baseX := (x * xScale) >> 14
		baseY := (y * yScale) >> 14
		lumaX, lumaY := x, y
		if plane > 0 {
			lumaX, lumaY = x<<uint(d.subX), y<<uint(d.subY)
		}
		fracX := ((16 * lumaX * xScale) >> 14) & 15
		fracY := ((16 * lumaY * yScale) >> 14) & 15
		dX := ((cmvC * xScale) >> 14) + fracX
		dY := ((cmvR * yScale) >> 14) + fracY
		stepX := (16 * xScale) >> 14
		stepY := (16 * yScale) >> 14
		startX := (baseX << 4) + dX
		startY := (baseY << 4) + dY
		if rl == 0 {
			// the first (or only) prediction goes straight into the frame
			d.blockInterPred(ref, plane, startX, startY, stepX, stepY, w, h, b.interpFilter, d.cur.pix[plane][y*d.cur.stride[plane]+x:], d.cur.stride[plane])
		} else {
			d.blockInterPred(ref, plane, startX, startY, stepX, stepY, w, h, b.interpFilter, d.predBuf[1][:w*h], w)
		}
	}
	if isCompound {
		fr := d.cur
		stride := fr.stride[plane]
		pix := fr.pix[plane]
		for i := 0; i < h; i++ {
			row := pix[(y+i)*stride+x : (y+i)*stride+x+w]
			p1 := d.predBuf[1][i*w : i*w+w]
			for j := range row {
				row[j] = uint16((uint32(row[j]) + uint32(p1[j]) + 1) >> 1)
			}
		}
	}
}

// blockInterPred implements spec 8.5.2.4.
// The prediction is written to out with ostride samples per row.
func (d *Decoder) blockInterPred(ref *Frame, plane, x, y, xStep, yStep, w, h, filter int, out []uint16, ostride int) {
	lastX := ref.pw[plane] - 1
	lastY := ref.ph[plane] - 1
	stride := ref.stride[plane]
	pix := ref.pix[plane]
	filt := &subpelFilters[filter]
	interH := (((h-1)*yStep + 15) >> 4) + 8
	if xStep == 16 && yStep == 16 {
		fx := x & 15
		fy := y & 15
		ix := x >> 4
		iy := y >> 4
		// fetch window (w+7) x (h+7)
		ww := w + 7
		var win []uint16
		var wstride int
		if ix-3 >= 0 && iy-3 >= 0 && ix+w+4 <= lastX+1 && iy+h+4 <= lastY+1 {
			win = pix[(iy-3)*stride+ix-3:]
			wstride = stride
		} else {
			win = d.tmpBuf[:ww*(h+7)]
			wstride = ww
			for r := 0; r < h+7; r++ {
				yy := clip3(0, lastY, iy+r-3)
				row := pix[yy*stride:]
				for c := 0; c < ww; c++ {
					win[r*ww+c] = row[clip3(0, lastX, ix+c-3)]
				}
			}
		}
		if fx == 0 && fy == 0 {
			for r := 0; r < h; r++ {
				copy(out[r*ostride:r*ostride+w], win[(r+3)*wstride+3:(r+3)*wstride+3+w])
			}
			return
		}
		// horizontal pass into an intermediate block; only the rows the vertical pass reads are computed
		var inter []uint16
		var istride int
		r0, r1 := 0, h+7
		if fy == 0 {
			r0, r1 = 3, 3+h
		}
		if fx == 0 {
			inter = win[3:]
			istride = wstride
		} else {
			inter = d.predTmp[:(h+7)*w]
			istride = w
			f := &filt[fx]
			f0, f1, f2, f3, f4, f5, f6, f7 := f[0], f[1], f[2], f[3], f[4], f[5], f[6], f[7]
			maxV := d.maxV
			for r := r0; r < r1; r++ {
				src := win[r*wstride : r*wstride+w+7]
				dst := inter[r*w : r*w+w]
				// eight views of the row, shifted by one sample each, so that the loop has no bounds checks
				s0, s1, s2, s3 := src[:len(dst)], src[1:][:len(dst)], src[2:][:len(dst)], src[3:][:len(dst)]
				s4, s5, s6, s7 := src[4:][:len(dst)], src[5:][:len(dst)], src[6:][:len(dst)], src[7:][:len(dst)]
				for c := range dst {
					s := int32(s0[c])*f0 + int32(s1[c])*f1 + int32(s2[c])*f2 + int32(s3[c])*f3 +
						int32(s4[c])*f4 + int32(s5[c])*f5 + int32(s6[c])*f6 + int32(s7[c])*f7
					v := (s + 64) >> 7
					if uint32(v) > uint32(maxV) {
						if v < 0 {
							v = 0
						} else {
							v = maxV
						}
					}
					dst[c] = uint16(v)
				}
			}
		}
		if fy == 0 {
			for r := 0; r < h; r++ {
				copy(out[r*ostride:r*ostride+w], inter[(r+3)*istride:(r+3)*istride+w])
			}
			return
		}
		f := &filt[fy]
		f0, f1, f2, f3, f4, f5, f6, f7 := f[0], f[1], f[2], f[3], f[4], f[5], f[6], f[7]
		maxV := d.maxV
		for r := 0; r < h; r++ {
			o := out[r*ostride : r*ostride+w]
			p0 := inter[r*istride : r*istride+w]
			p1 := inter[(r+1)*istride : (r+1)*istride+w]
			p2 := inter[(r+2)*istride : (r+2)*istride+w]
			p3 := inter[(r+3)*istride : (r+3)*istride+w]
			p4 := inter[(r+4)*istride : (r+4)*istride+w]
			p5 := inter[(r+5)*istride : (r+5)*istride+w]
			p6 := inter[(r+6)*istride : (r+6)*istride+w]
			p7 := inter[(r+7)*istride : (r+7)*istride+w]
			for c := range o {
				s := int32(p0[c])*f0 + int32(p1[c])*f1 + int32(p2[c])*f2 + int32(p3[c])*f3 +
					int32(p4[c])*f4 + int32(p5[c])*f5 + int32(p6[c])*f6 + int32(p7[c])*f7
				v := (s + 64) >> 7
				if uint32(v) > uint32(maxV) {
					if v < 0 {
						v = 0
					} else {
						v = maxV
					}
				}
				o[c] = uint16(v)
			}
		}
		return
	}
	// scaled reference: generic path
	inter := d.scaledTmp[:interH*w]
	for r := 0; r < interH; r++ {
		yy := clip3(0, lastY, (y>>4)+r-3)
		row := pix[yy*stride:]
		for c := 0; c < w; c++ {
			p := x + xStep*c
			f := &filt[p&15]
			var s int32
			for t := 0; t < 8; t++ {
				s += f[t] * int32(row[clip3(0, lastX, (p>>4)+t-3)])
			}
			inter[r*w+c] = d.clipPix((s + 64) >> 7)
		}
	}
	for r := 0; r < h; r++ {
		p := (y & 15) + yStep*r
		f := &filt[p&15]
		for c := 0; c < w; c++ {
			var s int32
			for t := 0; t < 8; t++ {
				s += f[t] * int32(inter[((p>>4)+t)*w+c])
			}
			out[r*ostride+c] = d.clipPix((s + 64) >> 7)
		}
	}
}
