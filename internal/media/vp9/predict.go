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
		maxX = ((d.miCols * 8) >> 1) - 1
		maxY = ((d.miRows * 8) >> 1) - 1
	}
	fr := d.cur
	stride := fr.stride[plane]
	pix := fr.pix[plane]
	var abuf [65]int32 // abuf[k+1] = aboveRow[k]
	var left [32]int32
	base := int32(128)
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
				} else if v > 255 {
					v = 255
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
			row[j] = uint8(pred[i*size+j])
		}
	}
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
		sx, sy = 1, 1
	}
	for rl := 0; rl < nref; rl++ {
		// motion vector selection
		var mvr, mvc int
		bm := &b.blockMvs[rl]
		if plane == 0 || b.miSize >= block8x8 {
			mvr, mvc = int(bm[blockIdx].row), int(bm[blockIdx].col)
		} else {
			// 4:2:0
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
		ref := d.refs[d.refFrameIdx[refFrame-1]]
		xScale := (ref.w << 14) / d.width
		yScale := (ref.h << 14) / d.height
		baseX := (x * xScale) >> 14
		baseY := (y * yScale) >> 14
		lumaX, lumaY := x, y
		if plane > 0 {
			lumaX, lumaY = x<<1, y<<1
		}
		fracX := ((16 * lumaX * xScale) >> 14) & 15
		fracY := ((16 * lumaY * yScale) >> 14) & 15
		dX := ((cmvC * xScale) >> 14) + fracX
		dY := ((cmvR * yScale) >> 14) + fracY
		stepX := (16 * xScale) >> 14
		stepY := (16 * yScale) >> 14
		startX := (baseX << 4) + dX
		startY := (baseY << 4) + dY
		d.blockInterPred(ref, plane, startX, startY, stepX, stepY, w, h, b.interpFilter, d.predBuf[rl][:w*h])
	}
	fr := d.cur
	stride := fr.stride[plane]
	pix := fr.pix[plane]
	for i := 0; i < h; i++ {
		row := pix[(y+i)*stride+x : (y+i)*stride+x+w]
		if isCompound {
			p0 := d.predBuf[0][i*w : i*w+w]
			p1 := d.predBuf[1][i*w : i*w+w]
			for j := range row {
				row[j] = uint8((int(p0[j]) + int(p1[j]) + 1) >> 1)
			}
		} else {
			copy(row, d.predBuf[0][i*w:i*w+w])
		}
	}
}

// blockInterPred implements spec 8.5.2.4.
func (d *Decoder) blockInterPred(ref *Frame, plane, x, y, xStep, yStep, w, h, filter int, out []uint8) {
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
		var win []uint8
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
				copy(out[r*w:r*w+w], win[(r+3)*wstride+3:(r+3)*wstride+3+w])
			}
			return
		}
		// horizontal pass into intermediate (h+7) x w
		var inter []uint8
		var istride int
		if fx == 0 {
			inter = win[3:]
			istride = wstride
		} else {
			inter = d.predTmp[:(h+7)*w]
			istride = w
			f := &filt[fx]
			for r := 0; r < h+7; r++ {
				src := win[r*wstride:]
				dst := inter[r*w : r*w+w]
				for c := 0; c < w; c++ {
					s := int32(src[c])*f[0] + int32(src[c+1])*f[1] + int32(src[c+2])*f[2] + int32(src[c+3])*f[3] +
						int32(src[c+4])*f[4] + int32(src[c+5])*f[5] + int32(src[c+6])*f[6] + int32(src[c+7])*f[7]
					dst[c] = clip255((s + 64) >> 7)
				}
			}
		}
		if fy == 0 {
			for r := 0; r < h; r++ {
				copy(out[r*w:r*w+w], inter[(r+3)*istride:(r+3)*istride+w])
			}
			return
		}
		f := &filt[fy]
		for r := 0; r < h; r++ {
			for c := 0; c < w; c++ {
				p := r*istride + c
				s := int32(inter[p])*f[0] + int32(inter[p+istride])*f[1] + int32(inter[p+2*istride])*f[2] + int32(inter[p+3*istride])*f[3] +
					int32(inter[p+4*istride])*f[4] + int32(inter[p+5*istride])*f[5] + int32(inter[p+6*istride])*f[6] + int32(inter[p+7*istride])*f[7]
				out[r*w+c] = clip255((s + 64) >> 7)
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
			inter[r*w+c] = clip255((s + 64) >> 7)
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
			out[r*w+c] = clip255((s + 64) >> 7)
		}
	}
}
