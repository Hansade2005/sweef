package hevc

// reconTB turns the coefficients in scratch.coef into a residual block and
// adds it to the prediction already present in the picture.
func (d *Decoder) reconTB(cIdx, xC, yC, log2 int, ti *tbInfo, intra bool, predMode int, resScale int) {
	s, p := d.sp, d.pp
	cu := &d.cu
	sc := &d.scratch
	n := 1 << uint(log2)
	nn := n * n
	var res []int32
	if cIdx == 0 && p.crossComponentPred {
		res = sc.resY[:nn]
	} else {
		res = sc.res[:nn]
	}
	bd := s.bitDepthY
	if cIdx > 0 {
		bd = s.bitDepthC
	}
	coef := sc.coef[:nn]
	switch {
	case ti.zero || ti.err:
		for i := range res {
			res[i] = 0
		}
	case cu.bypass:
		rot := s.tsRotation && n == 4 && intra
		if rot {
			for i := 0; i < nn; i++ {
				res[i] = coef[nn-1-i]
			}
		} else {
			copy(res, coef)
		}
		d.rdpcm(res, n, ti, intra, predMode)
	case ti.ts:
		rot := s.tsRotation && n == 4 && intra
		tsShift := uint(5 + log2)
		bdShift := uint(20 - bd)
		rnd := int32(1) << (bdShift - 1)
		for i := 0; i < nn; i++ {
			v := coef[i]
			if rot {
				v = coef[nn-1-i]
			}
			res[i] = ((v << tsShift) + rnd) >> bdShift
		}
		d.rdpcm(res, n, ti, intra, predMode)
	default:
		dst := cIdx == 0 && intra && n == 4
		inverseTransform(coef, res, log2, dst, uint(20-bd), ti.maxX, ti.maxY, &sc.tmp)
	}
	// clear coefficients for the next block
	if !ti.zero {
		for i := range coef {
			coef[i] = 0
		}
	}
	if cIdx > 0 && resScale != 0 {
		bdY, bdC := uint(s.bitDepthY), uint(s.bitDepthC)
		ry := sc.resY[:nn]
		for i := range res {
			res[i] += (int32(resScale) * ((ry[i] << bdC) >> bdY)) >> 3
		}
	}
	// add to the picture
	pic := d.cur
	stride := pic.stride[cIdx]
	pl := pic.planes[cIdx]
	maxV := int32(1)<<uint(bd) - 1
	for y := 0; y < n; y++ {
		row := pl[(yC+y)*stride+xC : (yC+y)*stride+xC+n]
		r := res[y*n : y*n+n]
		for x := range row {
			v := int32(row[x]) + r[x]
			if v < 0 {
				v = 0
			} else if v > maxV {
				v = maxV
			}
			row[x] = uint16(v)
		}
	}
}

// rdpcm applies the directional residual accumulation.
func (d *Decoder) rdpcm(res []int32, n int, ti *tbInfo, intra bool, predMode int) {
	s := d.sp
	dir := -1
	if intra {
		if s.implicitRdpcm && (predMode == 10 || predMode == 26) {
			if predMode == 10 {
				dir = 0
			} else {
				dir = 1
			}
		}
	} else if ti.explicit {
		dir = ti.dir
	}
	switch dir {
	case 0:
		for y := 0; y < n; y++ {
			r := res[y*n : y*n+n]
			for x := 1; x < n; x++ {
				r[x] += r[x-1]
			}
		}
	case 1:
		for y := 1; y < n; y++ {
			for x := 0; x < n; x++ {
				res[y*n+x] += res[(y-1)*n+x]
			}
		}
	}
}

// inverseTransform computes the 2-D inverse DCT (or DST) of coef into res.
func inverseTransform(coef, res []int32, log2 int, dst bool, bdShift uint, maxX, maxY int, tmp *[32 * 32]int32) {
	n := 1 << uint(log2)
	rnd2 := int32(1) << (bdShift - 1)
	if !dst && maxX == 0 && maxY == 0 {
		// DC only
		v := (coef[0]*64 + 64) >> 7
		if v > 32767 {
			v = 32767
		} else if v < -32768 {
			v = -32768
		}
		v = (v*64 + rnd2) >> bdShift
		for i := 0; i < n*n; i++ {
			res[i] = v
		}
		return
	}
	step := 32 >> uint(log2)
	// first stage: columns
	for x := 0; x <= maxX; x++ {
		for y := 0; y < n; y++ {
			var sum int32
			if dst {
				for k := 0; k <= maxY; k++ {
					sum += int32(dstMat[k][y]) * coef[k*n+x]
				}
			} else {
				for k := 0; k <= maxY; k++ {
					sum += int32(dctMat[k*step][y]) * coef[k*n+x]
				}
			}
			v := (sum + 64) >> 7
			if v > 32767 {
				v = 32767
			} else if v < -32768 {
				v = -32768
			}
			tmp[y*n+x] = v
		}
	}
	// second stage: rows
	for y := 0; y < n; y++ {
		t := tmp[y*n : y*n+maxX+1]
		o := res[y*n : y*n+n]
		for x := 0; x < n; x++ {
			var sum int32
			if dst {
				for k, tv := range t {
					sum += int32(dstMat[k][x]) * tv
				}
			} else {
				for k, tv := range t {
					sum += int32(dctMat[k*step][x]) * tv
				}
			}
			o[x] = (sum + rnd2) >> bdShift
		}
	}
}
