package av1

import "sync"

// ---- compute_prediction (5.11.33) ----------------------------------------------

func (f *frameDec) computePrediction() {
	if !f.isInter {
		return
	}
	sbMask := f.sbMask
	subBlockMiRow := f.miRow & sbMask
	subBlockMiCol := f.miCol & sbMask
	nPlanes := 1
	if f.hasChroma {
		nPlanes = 3
	}
	for plane := 0; plane < nPlanes; plane++ {
		planeSz := getPlaneResidualSize(f.miSize, plane, f.subX, f.subY)
		num4x4W := num4x4BlocksWide[planeSz]
		num4x4H := num4x4BlocksHigh[planeSz]
		log2W := 2 + miWidthLog2[planeSz]
		log2H := 2 + miHeightLog2[planeSz]
		subX, subY := 0, 0
		if plane > 0 {
			subX, subY = f.subX, f.subY
		}
		baseX := (f.miCol >> uint(subX)) * 4
		baseY := (f.miRow >> uint(subY)) * 4
		candRow := (f.miRow >> uint(subY)) << uint(subY)
		candCol := (f.miCol >> uint(subX)) << uint(subX)
		f.isInterIntra = f.isInter && f.refFrame[1] == intraFrame
		if f.isInterIntra && plane == 0 {
			cov(covInterintra)
		}
		if f.isInterIntra {
			var mode int
			switch f.interintraMode {
			case 0:
				mode = dcPred
			case 1:
				mode = vPred
			case 2:
				mode = hPred
			default:
				mode = smoothPred
			}
			haveLeft, haveAbove := f.availL, f.availU
			if plane > 0 {
				haveLeft, haveAbove = f.availLChroma, f.availUChroma
			}
			bd := &f.blockDecoded[plane]
			f.predictIntra(plane, baseX, baseY, haveLeft, haveAbove,
				bd[(subBlockMiRow>>uint(subY))-1+1][(subBlockMiCol>>uint(subX))+num4x4W+1],
				bd[(subBlockMiRow>>uint(subY))+num4x4H+1][(subBlockMiCol>>uint(subX))-1+1],
				mode, log2W, log2H)
		}
		predW := blockWidth(f.miSize) >> uint(subX)
		predH := blockHeight(f.miSize) >> uint(subY)
		someUseIntra := false
		for r := 0; r < (num4x4H << uint(subY)); r++ {
			if candRow+r >= f.miRows {
				break
			}
			for c := 0; c < (num4x4W << uint(subX)); c++ {
				if candCol+c >= f.miCols {
					break
				}
				if f.miAt(candRow+r, candCol+c).refFrame[0] == intraFrame {
					someUseIntra = true
				}
			}
		}
		if someUseIntra {
			predW = num4x4W * 4
			predH = num4x4H * 4
			candRow = f.miRow
			candCol = f.miCol
		}
		r := 0
		for y := 0; y < num4x4H*4; y += predH {
			c := 0
			for x := 0; x < num4x4W*4; x += predW {
				f.predictInter(plane, baseX+x, baseY+y, predW, predH, candRow+r, candCol+c)
				c++
			}
			r++
		}
	}
}

// ---- inter prediction (spec 7.11.3) --------------------------------------------

type refPlaneInfo struct {
	pl     []uint16
	stride int
	lastX  int
	lastY  int
}

// refPlane returns the reference plane for refFrame (or the current frame for
// intra block copy).
func (f *frameDec) refPlane(refFrame, plane int) refPlaneInfo {
	subX, subY := 0, 0
	if plane > 0 {
		subX, subY = f.subX, f.subY
	}
	if f.useIntrabc {
		return refPlaneInfo{
			pl:     f.cur.pl[plane],
			stride: f.cur.stride[plane],
			lastX:  ((f.miCols*4 + subX) >> uint(subX)) - 1,
			lastY:  ((f.miRows*4 + subY) >> uint(subY)) - 1,
		}
	}
	r := f.refBufs[refFrame]
	return refPlaneInfo{
		pl:     r.buf.pl[plane],
		stride: r.buf.stride[plane],
		lastX:  ((r.upscaledWidth + subX) >> uint(subX)) - 1,
		lastY:  ((r.frameHeight + subY) >> uint(subY)) - 1,
	}
}

func (f *frameDec) refScale(refFrame int) (xScale, yScale int) {
	if f.useIntrabc {
		return 1 << 14, 1 << 14
	}
	return f.refScaleX[refFrame], f.refScaleY[refFrame]
}

// scaleMv implements the motion vector scaling process (7.11.3.3).
func (f *frameDec) scaleMv(plane, refFrame, x, y int, mv mvec) (startX, startY, stepX, stepY int) {
	xScale, yScale := f.refScale(refFrame)
	subX, subY := 0, 0
	if plane > 0 {
		subX, subY = f.subX, f.subY
	}
	const halfSample = 1 << 3
	origX := (x << 4) + ((2 * int(mv.col)) >> uint(subX)) + halfSample
	origY := (y << 4) + ((2 * int(mv.row)) >> uint(subY)) + halfSample
	baseX := origX*xScale - (halfSample << 14)
	baseY := origY*yScale - (halfSample << 14)
	const off = (1 << (10 - 4)) / 2
	startX = round2Signed(baseX, 14+4-10) + off
	startY = round2Signed(baseY, 14+4-10) + off
	stepX = round2Signed(xScale, 14-10)
	stepY = round2Signed(yScale, 14-10)
	return
}

func (f *frameDec) roundingVars(isCompound bool) (r0, r1, post int) {
	r0 = 3
	if isCompound {
		r1 = 7
	} else {
		r1 = 11
	}
	if f.bitDepth == 12 {
		r0 += 2
		if !isCompound {
			r1 -= 2
		}
	}
	post = 14 - (r0 + r1)
	return
}

func subpelFilterIdx(filter, size int) int {
	if size <= 4 {
		if filter == eighttap || filter == eighttapSharp {
			return 4
		} else if filter == eighttapSmooth {
			return 5
		}
	}
	return filter
}

// blockInterPred implements the block inter prediction process (7.11.3.4);
// the result (not yet clipped) is written to out with stride w.
func (f *frameDec) blockInterPred(rp refPlaneInfo, startX, startY, stepX, stepY, w, h int, interpFilter [2]int, r0, r1 uint, out []int32) {
	fh := subpelFilterIdx(interpFilter[1], w)
	fv := subpelFilterIdx(interpFilter[0], h)
	if stepX == 1024 && stepY == 1024 {
		f.blockInterPredUnscaled(rp, startX, startY, w, h, fh, fv, r0, r1, out)
		return
	}
	// generic (scaled) path, literal from the specification
	cov(covScaledRef)
	interH := (((h-1)*stepY + (1 << 10) - 1) >> 10) + 8
	inter := make([]int32, interH*w)
	for r := 0; r < interH; r++ {
		yy := clip3(0, rp.lastY, (startY>>10)+r-3)
		row := rp.pl[yy*rp.stride:]
		for c := 0; c < w; c++ {
			p := startX + stepX*c
			filt := &subpelFilters[fh][(p>>6)&15]
			s := 0
			for t := 0; t < 8; t++ {
				s += int(filt[t]) * int(row[clip3(0, rp.lastX, (p>>10)+t-3)])
			}
			inter[r*w+c] = int32(round2(s, r0))
		}
	}
	for r := 0; r < h; r++ {
		p := (startY & 1023) + stepY*r
		filt := &subpelFilters[fv][(p>>6)&15]
		for c := 0; c < w; c++ {
			s := 0
			for t := 0; t < 8; t++ {
				s += int(filt[t]) * int(inter[((p>>10)+t)*w+c])
			}
			out[r*w+c] = int32(round2(s, r1))
		}
	}
}

func (f *frameDec) blockInterPredUnscaled(rp refPlaneInfo, startX, startY, w, h, fh, fv int, r0, r1 uint, out []int32) {
	x0 := startX >> 10
	y0 := startY >> 10
	fx := (startX >> 6) & 15
	fy := (startY >> 6) & 15
	// Gather the (w+7) x (h+7) source patch with edge clamping.
	pw := w + 7
	ph := h + 7
	var src []uint16
	var sstride int
	inside := x0-3 >= 0 && y0-3 >= 0 && x0+w+4 <= rp.lastX && y0+h+4 <= rp.lastY
	if inside {
		src = rp.pl[(y0-3)*rp.stride+x0-3:]
		sstride = rp.stride
	} else {
		patch := f.patch[:pw*ph]
		for r := 0; r < ph; r++ {
			yy := clip3(0, rp.lastY, y0-3+r)
			row := rp.pl[yy*rp.stride:]
			for c := 0; c < pw; c++ {
				patch[r*pw+c] = row[clip3(0, rp.lastX, x0-3+c)]
			}
		}
		src = patch
		sstride = pw
	}
	if fx == 0 && fy == 0 {
		// Identity filters: Round2(128 * Round2(128 * p, r0), r1) == p << postRound.
		sh := uint(14 - int(r0) - int(r1))
		for r := 0; r < h; r++ {
			srow := src[(r+3)*sstride+3:]
			orow := out[r*w : r*w+w]
			for c := 0; c < w; c++ {
				orow[c] = int32(srow[c]) << sh
			}
		}
		return
	}
	inter := f.interBuf[:ph*w]
	hf := &subpelFilters[fh][fx]
	rnd0 := int32(1) << (r0 - 1)
	for r := 0; r < ph; r++ {
		srow := src[r*sstride : r*sstride+w+7]
		irow := inter[r*w : r*w+w]
		if fx == 0 {
			for c := 0; c < w; c++ {
				irow[c] = (int32(srow[c+3])*128 + rnd0) >> r0
			}
			continue
		}
		f0, f1, f2, f3, f4, f5, f6, f7 := int32(hf[0]), int32(hf[1]), int32(hf[2]), int32(hf[3]), int32(hf[4]), int32(hf[5]), int32(hf[6]), int32(hf[7])
		for c := 0; c < w; c++ {
			s := f0*int32(srow[c]) + f1*int32(srow[c+1]) + f2*int32(srow[c+2]) + f3*int32(srow[c+3]) +
				f4*int32(srow[c+4]) + f5*int32(srow[c+5]) + f6*int32(srow[c+6]) + f7*int32(srow[c+7])
			irow[c] = (s + rnd0) >> r0
		}
	}
	vf := &subpelFilters[fv][fy]
	rnd1 := int32(1) << (r1 - 1)
	g0, g1, g2, g3, g4, g5, g6, g7 := int32(vf[0]), int32(vf[1]), int32(vf[2]), int32(vf[3]), int32(vf[4]), int32(vf[5]), int32(vf[6]), int32(vf[7])
	for r := 0; r < h; r++ {
		orow := out[r*w : r*w+w]
		for c := 0; c < w; c++ {
			s := g0*inter[r*w+c] + g1*inter[(r+1)*w+c] + g2*inter[(r+2)*w+c] + g3*inter[(r+3)*w+c] +
				g4*inter[(r+4)*w+c] + g5*inter[(r+5)*w+c] + g6*inter[(r+6)*w+c] + g7*inter[(r+7)*w+c]
			orow[c] = (s + rnd1) >> r1
		}
	}
}

func resolveDivisor(d int) (divShift, divFactor int) {
	n := floorLog2(abs(d))
	e := abs(d) - (1 << uint(n))
	var fv int
	if n > 8 {
		fv = round2(e, uint(n-8))
	} else {
		fv = e << uint(8-n)
	}
	divShift = n + 14
	if d < 0 {
		divFactor = -divLut[fv]
	} else {
		divFactor = divLut[fv]
	}
	return
}

func setupShear(wp *[6]int) (valid bool, alpha, beta, gamma, delta int) {
	alpha0 := clip3(-32768, 32767, wp[2]-(1<<warpedModelPrec))
	beta0 := clip3(-32768, 32767, wp[3])
	divShift, divFactor := resolveDivisor(wp[2])
	v := wp[4] << warpedModelPrec
	gamma0 := clip3(-32768, 32767, round2Signed(v*divFactor, uint(divShift)))
	w := wp[3] * wp[4]
	delta0 := clip3(-32768, 32767, wp[5]-round2Signed(w*divFactor, uint(divShift))-(1<<warpedModelPrec))
	red := func(x int) int { return round2Signed(x, 6) << 6 }
	alpha, beta, gamma, delta = red(alpha0), red(beta0), red(gamma0), red(delta0)
	valid = true
	if 4*abs(alpha)+7*abs(beta) >= (1 << warpedModelPrec) {
		valid = false
	}
	if 4*abs(gamma)+4*abs(delta) >= (1 << warpedModelPrec) {
		valid = false
	}
	return
}

func lsProduct(a, b int) int { return ((a * b) >> 2) + (a + b) }

func (f *frameDec) warpEstimation() {
	var A [2][2]int
	var Bx, By [2]int
	w4, h4 := f.bw4, f.bh4
	midY := f.miRow*4 + h4*2 - 1
	midX := f.miCol*4 + w4*2 - 1
	suy := midY * 8
	sux := midX * 8
	duy := suy + int(f.mv[0].row)
	dux := sux + int(f.mv[0].col)
	for i := 0; i < f.numSamples; i++ {
		sy := f.candList[i][0] - suy
		sx := f.candList[i][1] - sux
		dy := f.candList[i][2] - duy
		dx := f.candList[i][3] - dux
		if abs(sx-dx) < 256 && abs(sy-dy) < 256 {
			A[0][0] += lsProduct(sx, sx) + 8
			A[0][1] += lsProduct(sx, sy) + 4
			A[1][1] += lsProduct(sy, sy) + 8
			Bx[0] += lsProduct(sx, dx) + 8
			Bx[1] += lsProduct(sy, dx) + 4
			By[0] += lsProduct(sx, dy) + 4
			By[1] += lsProduct(sy, dy) + 8
		}
	}
	det := A[0][0]*A[1][1] - A[0][1]*A[0][1]
	f.localValid = det != 0
	if det == 0 {
		return
	}
	divShift, divFactor := resolveDivisor(det)
	divShift -= warpedModelPrec
	if divShift < 0 {
		divFactor <<= uint(-divShift)
		divShift = 0
	}
	const nonDiagClamp = 1 << 13
	nondiag := func(v int) int {
		return clip3(-nonDiagClamp+1, nonDiagClamp-1, round2Signed(v*divFactor, uint(divShift)))
	}
	diag := func(v int) int {
		return clip3((1<<warpedModelPrec)-nonDiagClamp+1, (1<<warpedModelPrec)+nonDiagClamp-1, round2Signed(v*divFactor, uint(divShift)))
	}
	p := &f.localWarpParams
	p[2] = diag(A[1][1]*Bx[0] - A[0][1]*Bx[1])
	p[3] = nondiag(-A[0][1]*Bx[0] + A[0][0]*Bx[1])
	p[4] = nondiag(A[1][1]*By[0] - A[0][1]*By[1])
	p[5] = diag(-A[0][1]*By[0] + A[0][0]*By[1])
	mvx := int(f.mv[0].col)
	mvy := int(f.mv[0].row)
	vx := mvx*(1<<(warpedModelPrec-3)) - (midX*(p[2]-(1<<warpedModelPrec)) + midY*p[3])
	vy := mvy*(1<<(warpedModelPrec-3)) - (midX*p[4] + midY*(p[5]-(1<<warpedModelPrec)))
	const transClamp = 1 << 23
	p[0] = clip3(-transClamp, transClamp-1, vx)
	p[1] = clip3(-transClamp, transClamp-1, vy)
}

// blockWarp implements the block warp process (7.11.3.5) for one 8x8 block.
func (f *frameDec) blockWarp(wp *[6]int, rp refPlaneInfo, plane, x, y, i8, j8, w, h int, r0, r1 uint, out []int32) {
	subX, subY := 0, 0
	if plane > 0 {
		subX, subY = f.subX, f.subY
	}
	srcX := (x + j8*8 + 4) << uint(subX)
	srcY := (y + i8*8 + 4) << uint(subY)
	dstX := wp[2]*srcX + wp[3]*srcY + wp[0]
	dstY := wp[4]*srcX + wp[5]*srcY + wp[1]
	_, alpha, beta, gamma, delta := setupShear(wp)
	x4 := dstX >> uint(subX)
	y4 := dstY >> uint(subY)
	ix4 := x4 >> warpedModelPrec
	sx4 := x4 & ((1 << warpedModelPrec) - 1)
	iy4 := y4 >> warpedModelPrec
	sy4 := y4 & ((1 << warpedModelPrec) - 1)
	var inter [15][8]int32
	for i1 := -7; i1 < 8; i1++ {
		yy := clip3(0, rp.lastY, iy4+i1)
		row := rp.pl[yy*rp.stride:]
		for i2 := -4; i2 < 4; i2++ {
			sx := sx4 + alpha*i2 + beta*i1
			offs := round2(sx, 10) + 64
			filt := &warpedFilters[offs]
			s := 0
			for i3 := 0; i3 < 8; i3++ {
				s += int(filt[i3]) * int(row[clip3(0, rp.lastX, ix4+i2-3+i3)])
			}
			inter[i1+7][i2+4] = int32(round2(s, r0))
		}
	}
	for i1 := -4; i1 < imin(4, h-i8*8-4); i1++ {
		for i2 := -4; i2 < imin(4, w-j8*8-4); i2++ {
			sy := sy4 + gamma*i2 + delta*i1
			offs := round2(sy, 10) + 64
			filt := &warpedFilters[offs]
			s := 0
			for i3 := 0; i3 < 8; i3++ {
				s += int(filt[i3]) * int(inter[i1+i3+4][i2+4])
			}
			out[(i8*8+i1+4)*w+j8*8+i2+4] = int32(round2(s, r1))
		}
	}
}

func (f *frameDec) clipPix(v int32) uint16 {
	maxV := int32(1)<<uint(f.bitDepth) - 1
	if v < 0 {
		return 0
	}
	if v > maxV {
		return uint16(maxV)
	}
	return uint16(v)
}

func (f *frameDec) predictInter(plane, x, y, w, h, candRow, candCol int) {
	fh := f.fh
	cand := f.miAt(candRow, candCol)
	isCompound := cand.refFrame[1] > intraFrame
	r0, r1, post := f.roundingVars(isCompound)
	if plane == 0 && f.motionMode == motionLocalWarp {
		f.warpEstimation()
		if f.localValid {
			f.localValid, _, _, _, _ = setupShear(&f.localWarpParams)
		}
	}
	nLists := 1
	if isCompound {
		nLists = 2
	}
	var preds [2][]int32
	for refList := 0; refList < nLists; refList++ {
		preds[refList] = f.predBuf[refList][:w*h]
		refFrame := int(cand.refFrame[refList])
		globalValid := false
		isGlobalMode := f.yMode == modeGlobalMv || f.yMode == modeGlobalGlobal
		if isGlobalMode && !f.useIntrabc && fh.gmType[refFrame] > gmTranslation {
			globalValid, _, _, _, _ = setupShear(&fh.gmParams[refFrame])
		}
		useWarp := 0
		if w < 8 || h < 8 {
			useWarp = 0
		} else if fh.forceIntMv {
			useWarp = 0
		} else if f.motionMode == motionLocalWarp && f.localValid {
			useWarp = 1
		} else if isGlobalMode && fh.gmType[refFrame] > gmTranslation && !f.isScaled(refFrame) && globalValid {
			useWarp = 2
		}
		mv := cand.mv[refList]
		rp := f.refPlane(refFrame, plane)
		if useWarp != 0 {
			wp := &f.localWarpParams
			cov(covLocalWarp)
			if useWarp == 2 {
				wp = &fh.gmParams[refFrame]
				cov(covGlobalWarp)
			}
			for i8 := 0; i8 <= (h-1)>>3; i8++ {
				for j8 := 0; j8 <= (w-1)>>3; j8++ {
					f.blockWarp(wp, rp, plane, x, y, i8, j8, w, h, uint(r0), uint(r1), preds[refList])
				}
			}
		} else {
			startX, startY, stepX, stepY := f.scaleMv(plane, refFrame, x, y, mv)
			f.blockInterPred(rp, startX, startY, stepX, stepY, w, h,
				[2]int{int(cand.interpFilter[0]), int(cand.interpFilter[1])}, uint(r0), uint(r1), preds[refList])
		}
	}
	// masks
	switch {
	case f.compoundType == compoundWedge && plane == 0:
		cov(covWedge)
		f.wedgeMask(w, h)
	case f.compoundType == compoundIntra:
		f.intraVariantMask(w, h)
	case f.compoundType == compoundDiffwtd && plane == 0:
		cov(covDiffwtd)
		f.diffWeightMask(preds, w, h, post)
	}
	pl := f.cur.pl[plane]
	stride := f.cur.stride[plane]
	dst := pl[y*stride+x:]
	if !isCompound && !f.isInterIntra {
		for i := 0; i < h; i++ {
			row := dst[i*stride : i*stride+w]
			prow := preds[0][i*w : i*w+w]
			for j := range row {
				row[j] = f.clipPix(prow[j])
			}
		}
	} else if f.compoundType == compoundAverage {
		cov(covCompoundAvg)
		sh := uint(1 + post)
		rnd := int32(1) << (sh - 1)
		for i := 0; i < h; i++ {
			row := dst[i*stride : i*stride+w]
			p0 := preds[0][i*w : i*w+w]
			p1 := preds[1][i*w : i*w+w]
			for j := range row {
				row[j] = f.clipPix((p0[j] + p1[j] + rnd) >> sh)
			}
		}
	} else if f.compoundType == compoundDistance {
		cov(covDistance)
		fwd, bck := f.distanceWeights(cand)
		sh := uint(4 + post)
		rnd := int32(1) << (sh - 1)
		for i := 0; i < h; i++ {
			row := dst[i*stride : i*stride+w]
			p0 := preds[0][i*w : i*w+w]
			p1 := preds[1][i*w : i*w+w]
			for j := range row {
				row[j] = f.clipPix((int32(fwd)*p0[j] + int32(bck)*p1[j] + rnd) >> sh)
			}
		}
	} else {
		f.maskBlend(preds, plane, x, y, w, h, post)
	}
	if f.motionMode == motionObmc {
		cov(covOBMC)
		f.overlappedMotionCompensation(plane, w, h)
	}
}

func (f *frameDec) distanceWeights(cand *miInfo) (fwd, bck int) {
	var dist [2]int
	for refList := 0; refList < 2; refList++ {
		hh := f.fh.orderHints[cand.refFrame[refList]]
		dist[refList] = clip3(0, 31, abs(f.seq.relDist(hh, f.fh.orderHint)))
	}
	d0 := dist[1]
	d1 := dist[0]
	order := b2i(d0 <= d1)
	if d0 == 0 || d1 == 0 {
		fwd = quantDistLookup[3][order]
		bck = quantDistLookup[3][1-order]
		return
	}
	i := 0
	for ; i < 3; i++ {
		c0 := quantDistWeight[i][order]
		c1 := quantDistWeight[i][1-order]
		if order != 0 {
			if d0*c0 > d1*c1 {
				break
			}
		} else {
			if d0*c0 < d1*c1 {
				break
			}
		}
	}
	fwd = quantDistLookup[i][order]
	bck = quantDistLookup[i][1-order]
	return
}

// ---- masks and blending ----------------------------------------------------------

var (
	wedgeOnce  sync.Once
	wedgeMasks [blockSizes][2][16][]uint8
)

func initWedgeMasks() {
	const sz = 64
	var master [6][sz][sz]int
	for j := 0; j < sz; j++ {
		shift := sz / 4
		for i := 0; i < sz; i += 2 {
			master[3][i][j] = wedgeMasterObliqueEven[clip3(0, sz-1, j-shift)]
			shift--
			master[3][i+1][j] = wedgeMasterObliqueOdd[clip3(0, sz-1, j-shift)]
			master[1][i][j] = wedgeMasterVertical[j]
			master[1][i+1][j] = wedgeMasterVertical[j]
		}
	}
	for i := 0; i < sz; i++ {
		for j := 0; j < sz; j++ {
			msk := master[3][i][j]
			master[2][j][i] = msk
			master[4][i][sz-1-j] = 64 - msk
			master[5][sz-1-j][i] = 64 - msk
			master[0][j][i] = master[1][i][j]
		}
	}
	for bsize := block8x8; bsize < blockSizes; bsize++ {
		if wedgeBits[bsize] == 0 {
			continue
		}
		w := blockWidth(bsize)
		h := blockHeight(bsize)
		shape := 2
		w4, h4 := num4x4BlocksWide[bsize], num4x4BlocksHigh[bsize]
		if h4 > w4 {
			shape = 0
		} else if h4 < w4 {
			shape = 1
		}
		for wedge := 0; wedge < 16; wedge++ {
			dir := wedgeCodebook[shape][wedge][0]
			xoff := sz/2 - ((wedgeCodebook[shape][wedge][1] * w) >> 3)
			yoff := sz/2 - ((wedgeCodebook[shape][wedge][2] * h) >> 3)
			sum := 0
			for i := 0; i < w; i++ {
				sum += master[dir][yoff][xoff+i]
			}
			for i := 1; i < h; i++ {
				sum += master[dir][yoff+i][xoff]
			}
			avg := (sum + (w+h-1)/2) / (w + h - 1)
			flipSign := b2i(avg < 32)
			m0 := make([]uint8, w*h)
			m1 := make([]uint8, w*h)
			for i := 0; i < h; i++ {
				for j := 0; j < w; j++ {
					m0[i*w+j] = uint8(master[dir][yoff+i][xoff+j])
					m1[i*w+j] = uint8(64 - master[dir][yoff+i][xoff+j])
				}
			}
			wedgeMasks[bsize][flipSign][wedge] = m0
			wedgeMasks[bsize][1-flipSign][wedge] = m1
		}
	}
}

func (f *frameDec) wedgeMask(w, h int) {
	wedgeOnce.Do(initWedgeMasks)
	m := wedgeMasks[f.miSize][f.wedgeSign][f.wedgeIndex]
	copy(f.maskBuf[:w*h], m)
	f.maskStride = w
}

func (f *frameDec) intraVariantMask(w, h int) {
	sizeScale := 128 / imax(h, w)
	for i := 0; i < h; i++ {
		for j := 0; j < w; j++ {
			var v int
			switch f.interintraMode {
			case 1:
				v = iiWeights1d[i*sizeScale]
			case 2:
				v = iiWeights1d[j*sizeScale]
			case 3:
				v = iiWeights1d[imin(i, j)*sizeScale]
			default:
				v = 32
			}
			f.maskBuf[i*w+j] = uint8(v)
		}
	}
	f.maskStride = w
}

func (f *frameDec) diffWeightMask(preds [2][]int32, w, h, post int) {
	sh := uint(f.bitDepth - 8 + post)
	for i := 0; i < h; i++ {
		for j := 0; j < w; j++ {
			diff := int(preds[0][i*w+j] - preds[1][i*w+j])
			if diff < 0 {
				diff = -diff
			}
			diff = round2(diff, sh)
			m := clip3(0, 64, 38+diff/16)
			if f.maskType != 0 {
				f.maskBuf[i*w+j] = uint8(64 - m)
			} else {
				f.maskBuf[i*w+j] = uint8(m)
			}
		}
	}
	f.maskStride = w
}

func (f *frameDec) maskBlend(preds [2][]int32, plane, dstX, dstY, w, h, post int) {
	subX, subY := 0, 0
	if plane > 0 {
		subX, subY = f.subX, f.subY
	}
	pl := f.cur.pl[plane]
	stride := f.cur.stride[plane]
	ms := f.maskStride
	mask := f.maskBuf[:]
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var m int
			if (subX == 0 && subY == 0) || (f.interintra && !f.wedgeInterintra) {
				m = int(mask[y*ms+x])
			} else if subX != 0 && subY == 0 {
				m = round2(int(mask[y*ms+2*x])+int(mask[y*ms+2*x+1]), 1)
			} else if subX == 0 && subY != 0 {
				m = round2(int(mask[2*y*ms+x])+int(mask[(2*y+1)*ms+x]), 1)
			} else {
				m = round2(int(mask[2*y*ms+2*x])+int(mask[2*y*ms+2*x+1])+int(mask[(2*y+1)*ms+2*x])+int(mask[(2*y+1)*ms+2*x+1]), 2)
			}
			idx := (dstY+y)*stride + dstX + x
			if f.interintra {
				pred0 := int(f.clipPix(int32(round2(int(preds[0][y*w+x]), uint(post)))))
				pred1 := int(pl[idx])
				pl[idx] = uint16(round2(m*pred1+(64-m)*pred0, 6))
			} else {
				pred0 := int(preds[0][y*w+x])
				pred1 := int(preds[1][y*w+x])
				pl[idx] = f.clipPix(int32(round2(m*pred0+(64-m)*pred1, uint(6+post))))
			}
		}
	}
}

func getObmcMask(length int) []int {
	switch length {
	case 2:
		return obmcMask2[:]
	case 4:
		return obmcMask4[:]
	case 8:
		return obmcMask8[:]
	case 16:
		return obmcMask16[:]
	}
	return obmcMask32[:]
}

func (f *frameDec) overlappedMotionCompensation(plane, w, h int) {
	subX, subY := 0, 0
	if plane > 0 {
		subX, subY = f.subX, f.subY
	}
	if f.availU {
		if getPlaneResidualSize(f.miSize, plane, f.subX, f.subY) >= block8x8 {
			w4 := f.bw4
			x4 := f.miCol
			y4 := f.miRow
			nCount := 0
			nLimit := imin(4, miWidthLog2[f.miSize])
			for nCount < nLimit && x4 < imin(f.miCols, f.miCol+w4) {
				candRow := f.miRow - 1
				candCol := x4 | 1
				cand := f.miAt(candRow, candCol)
				step4 := clip3(2, 16, num4x4BlocksWide[cand.miSize])
				if cand.refFrame[0] > intraFrame {
					nCount++
					predW := imin(w, (step4*4)>>uint(subX))
					predH := imin(h>>1, 32>>uint(subY))
					mask := getObmcMask(predH)
					f.predictOverlap(plane, cand, x4, y4, predW, predH, 0, mask)
				}
				x4 += step4
			}
		}
	}
	if f.availL {
		h4 := f.bh4
		x4 := f.miCol
		y4 := f.miRow
		nCount := 0
		nLimit := imin(4, miHeightLog2[f.miSize])
		for nCount < nLimit && y4 < imin(f.miRows, f.miRow+h4) {
			candCol := f.miCol - 1
			candRow := y4 | 1
			cand := f.miAt(candRow, candCol)
			step4 := clip3(2, 16, num4x4BlocksHigh[cand.miSize])
			if cand.refFrame[0] > intraFrame {
				nCount++
				predW := imin(w>>1, 32>>uint(subX))
				predH := imin(h, (step4*4)>>uint(subY))
				mask := getObmcMask(predW)
				f.predictOverlap(plane, cand, x4, y4, predW, predH, 1, mask)
			}
			y4 += step4
		}
	}
}

func (f *frameDec) predictOverlap(plane int, cand *miInfo, x4, y4, predW, predH, pass int, mask []int) {
	subX, subY := 0, 0
	if plane > 0 {
		subX, subY = f.subX, f.subY
	}
	mv := cand.mv[0]
	refFrame := int(cand.refFrame[0])
	predX := (x4 * 4) >> uint(subX)
	predY := (y4 * 4) >> uint(subY)
	startX, startY, stepX, stepY := f.scaleMv(plane, refFrame, predX, predY, mv)
	rp := f.refPlane(refFrame, plane)
	r0, r1, _ := f.roundingVars(false)
	obmc := f.obmcPred[:predW*predH]
	f.blockInterPred(rp, startX, startY, stepX, stepY, predW, predH,
		[2]int{int(cand.interpFilter[0]), int(cand.interpFilter[1])}, uint(r0), uint(r1), obmc)
	pl := f.cur.pl[plane]
	stride := f.cur.stride[plane]
	for i := 0; i < predH; i++ {
		for j := 0; j < predW; j++ {
			op := int(f.clipPix(obmc[i*predW+j]))
			var m int
			if pass == 0 {
				m = mask[i]
			} else {
				m = mask[j]
			}
			idx := (predY+i)*stride + predX + j
			pl[idx] = uint16(round2(m*int(pl[idx])+(64-m)*op, 6))
		}
	}
}
