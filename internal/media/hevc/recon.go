package hevc

// reconTB turns the coefficients in scratch.coef into a residual block and
// adds it to the prediction already present in the picture.
var dbgTB func(d *Decoder, cIdx, xC, yC, log2 int, ti *tbInfo, intra bool, predMode int)

func (d *Decoder) reconTB(cIdx, xC, yC, log2 int, ti *tbInfo, intra bool, predMode int, resScale int, out []int32) {
	s, p := d.sp, d.pp
	cu := &d.cu
	sc := &d.scratch
	n := 1 << uint(log2)
	nn := n * n
	if dbgTB != nil {
		dbgTB(d, cIdx, xC, yC, log2, ti, intra, predMode)
	}
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
	case cu.bypass && d.compatFFmpeg:
		copy(res, coef)
		// ffmpeg's selection of the RDPCM direction for transquant bypass
		// blocks: the intra mode is the one of the last intra block (1 for
		// luma of inter CUs, the previous chroma mode for chroma), the
		// explicit direction is ignored when implicit RDPCM is enabled and
		// rotation is not applied.
		pm := predMode
		if !intra {
			pm = 1
			if cIdx > 0 {
				pm = d.staleChromaMode
			}
		}
		if ti.explicit || (s.implicitRdpcm && (pm == 10 || pm == 26)) {
			dir := ti.dir
			if s.implicitRdpcm {
				dir = boolInt(pm == 26)
			}
			d.rdpcmDir(res, n, dir)
		}
	case cu.bypass:
		if s.tsRotation && n == 4 && intra {
			for i := 0; i < nn; i++ {
				res[i] = coef[nn-1-i]
			}
		} else {
			copy(res, coef)
		}
		dir := -1
		if intra {
			if s.implicitRdpcm && (predMode == 10 || predMode == 26) {
				dir = boolInt(predMode == 26)
			}
		} else if ti.explicit {
			dir = ti.dir
		}
		d.rdpcmDir(res, n, dir)
	case ti.ts:
		rot := s.tsRotation && n == 4 && intra
		if s.extPrecision {
			bdShift := uint(max(20-bd, 11))
			tsShift := uint(min(5, int(bdShift)-2) + log2)
			rnd := int64(1) << (bdShift - 1)
			for i := 0; i < nn; i++ {
				v := coef[i]
				if rot {
					v = coef[nn-1-i]
				}
				res[i] = int32((int64(v)<<tsShift + rnd) >> bdShift)
			}
		} else {
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
		}
		d.rdpcm(res, n, ti, intra, predMode)
	default:
		dst := cIdx == 0 && intra && n == 4
		if s.extPrecision {
			log2Range := max(15, bd+6)
			inverseTransformWide(coef, res, log2, dst, uint(max(20-bd, 11)), log2Range, ti.maxX, ti.maxY, &sc.tmp)
		} else {
			inverseTransform(coef, res, log2, dst, uint(20-bd), ti.maxX, ti.maxY, &sc.tmp)
		}
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
			res[i] += int32((int64(resScale) * ((int64(ry[i]) << bdC) >> bdY)) >> 3)
		}
	}
	if out != nil {
		// the residual is kept for the adaptive colour transform
		copy(out, res)
		return
	}
	d.addResidual(cIdx, xC, yC, n, res)
}

// addResidual adds the n x n residual res to the prediction in the picture.
func (d *Decoder) addResidual(cIdx, xC, yC, n int, res []int32) {
	bd := d.sp.bitDepthY
	if cIdx > 0 {
		bd = d.sp.bitDepthC
	}
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

// actInverse applies the inverse adaptive colour transform (8.6.8) to the
// residuals of the three components, in the order of the coded components
// (G, B, R for RGB content).
func (d *Decoder) actInverse(r0, r1, r2 []int32) {
	s := d.sp
	bdY, bdC := s.bitDepthY, s.bitDepthC
	maxBd := max(bdY, bdC)
	shY, shC := uint(maxBd-bdY), uint(maxBd-bdC)
	var rndY, rndC int32
	if shY > 0 {
		rndY = 1 << (shY - 1)
	}
	if shC > 0 {
		rndC = 1 << (shC - 1)
	}
	lossless := d.cu.bypass
	log2Y, log2C := 15, 15
	if s.extPrecision {
		log2Y, log2C = max(15, bdY+6), max(15, bdC+6)
	}
	minY, maxY := -(int32(1) << uint(log2Y)), int32(1)<<uint(log2Y)-1
	minC, maxC := -(int32(1) << uint(log2C)), int32(1)<<uint(log2C)-1
	clip := func(v, lo, hi int32) int32 {
		if v < lo {
			return lo
		}
		if v > hi {
			return hi
		}
		return v
	}
	for i := range r0 {
		y0 := clip(r0[i], minY, maxY)
		cg := clip(r1[i], minC, maxC)
		co := clip(r2[i], minC, maxC)
		if !lossless {
			y0 <<= shY
			cg <<= shC + 1
			co <<= shC + 1
		}
		t := y0 - (cg >> 1)
		g := cg + t
		b := t - (co >> 1)
		r := co + b
		if !lossless {
			g = (g + rndY) >> shY
			b = (b + rndC) >> shC
			r = (r + rndC) >> shC
		}
		r0[i], r1[i], r2[i] = g, b, r
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
	d.rdpcmDir(res, n, dir)
}

func (d *Decoder) rdpcmDir(res []int32, n, dir int) {
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

// idct computes an n point inverse DCT of coefficients in[j*stride], j <= maxK
// (all others are zero) with even/odd decomposition; out has n entries.
func idct(in []int32, stride, n, maxK int, out []int32) {
	if n == 2 {
		var s1 int32
		if maxK >= 1 {
			s1 = in[stride]
		}
		out[0] = 64*in[0] + 64*s1
		out[1] = 64*in[0] - 64*s1
		return
	}
	h := n >> 1
	var e, o [16]int32
	step := 32 / n
	idct(in, 2*stride, h, maxK>>1, e[:h])
	for k := 0; k < h; k++ {
		o[k] = 0
	}
	for j := 1; j <= maxK; j += 2 {
		c := in[j*stride]
		if c == 0 {
			continue
		}
		m := &dctMat[j*step]
		for k := 0; k < h; k++ {
			o[k] += int32(m[k]) * c
		}
	}
	for k := 0; k < h; k++ {
		out[k] = e[k] + o[k]
		out[n-1-k] = e[k] - o[k]
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
	var line [32]int32
	if dst {
		for x := 0; x <= maxX; x++ {
			for y := 0; y < n; y++ {
				var sum int32
				for k := 0; k <= maxY; k++ {
					sum += int32(dstMat[k][y]) * coef[k*n+x]
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
		for y := 0; y < n; y++ {
			t := tmp[y*n : y*n+maxX+1]
			o := res[y*n : y*n+n]
			for x := 0; x < n; x++ {
				var sum int32
				for k, tv := range t {
					sum += int32(dstMat[k][x]) * tv
				}
				o[x] = (sum + rnd2) >> bdShift
			}
		}
		return
	}
	// columns
	for x := 0; x <= maxX; x++ {
		idct(coef[x:], n, n, maxY, line[:n])
		for y := 0; y < n; y++ {
			v := (line[y] + 64) >> 7
			if v > 32767 {
				v = 32767
			} else if v < -32768 {
				v = -32768
			}
			tmp[y*n+x] = v
		}
	}
	// rows
	for y := 0; y < n; y++ {
		idct(tmp[y*n:y*n+n], 1, n, maxX, line[:n])
		o := res[y*n : y*n+n]
		for x := 0; x < n; x++ {
			o[x] = (line[x] + rnd2) >> bdShift
		}
	}
}

// inverseTransformWide is inverseTransform with 64-bit intermediates for
// extended_precision_processing_flag, where coefficients have up to
// Max(15, BitDepth + 6) bits and the first stage output is clipped to that
// range instead of 16 bits.
func inverseTransformWide(coef, res []int32, log2 int, dst bool, bdShift uint, log2Range, maxX, maxY int, tmp *[32 * 32]int32) {
	n := 1 << uint(log2)
	lo, hi := -(int64(1) << uint(log2Range)), int64(1)<<uint(log2Range)-1
	rnd2 := int64(1) << (bdShift - 1)
	basis := func(k, i int) int64 {
		if dst {
			return int64(dstMat[k][i])
		}
		return int64(dctMat[k*(32/n)][i])
	}
	for x := 0; x <= maxX; x++ {
		for y := 0; y < n; y++ {
			var sum int64
			for k := 0; k <= maxY; k++ {
				sum += basis(k, y) * int64(coef[k*n+x])
			}
			v := (sum + 64) >> 7
			if v > hi {
				v = hi
			} else if v < lo {
				v = lo
			}
			tmp[y*n+x] = int32(v)
		}
	}
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			var sum int64
			for k := 0; k <= maxX; k++ {
				sum += basis(k, x) * int64(tmp[y*n+k])
			}
			res[y*n+x] = int32((sum + rnd2) >> bdShift)
		}
	}
}
