package vp9

// The straightforward per-sample loop filter that filterSegment is tested against.

func clampSigned(v int32, sh uint) int32 {
	lo, hi := -(int32(128) << sh), (int32(128)<<sh)-1
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func filterSampleRef(pix []uint16, bd uint, pos, across, limit, blimit, thresh, filterSize int) {
	sh := bd - 8
	q0 := int32(pix[pos])
	q1 := int32(pix[pos+across])
	q2 := int32(pix[pos+2*across])
	q3 := int32(pix[pos+3*across])
	p0 := int32(pix[pos-across])
	p1 := int32(pix[pos-2*across])
	p2 := int32(pix[pos-3*across])
	p3 := int32(pix[pos-4*across])
	lim := int32(limit) << sh
	hev := abs32(p1-p0) > int32(thresh)<<sh || abs32(q1-q0) > int32(thresh)<<sh
	if abs32(p3-p2) > lim || abs32(p2-p1) > lim || abs32(p1-p0) > lim ||
		abs32(q1-q0) > lim || abs32(q2-q1) > lim || abs32(q3-q2) > lim ||
		abs32(p0-q0)*2+abs32(p1-q1)/2 > int32(blimit)<<sh {
		return
	}
	one := int32(1) << sh
	flat := false
	if filterSize >= tx8x8 {
		flat = abs32(p1-p0) <= one && abs32(q1-q0) <= one && abs32(p2-p0) <= one && abs32(q2-q0) <= one && abs32(p3-p0) <= one && abs32(q3-q0) <= one
	}
	if filterSize == tx4x4 || !flat {
		off := int32(128) << sh
		ps1, ps0, qs0, qs1 := p1-off, p0-off, q0-off, q1-off
		var f int32
		if hev {
			f = clampSigned(ps1-qs1, sh)
		}
		f = clampSigned(f+3*(qs0-ps0), sh)
		f1 := clampSigned(f+4, sh) >> 3
		f2 := clampSigned(f+3, sh) >> 3
		pix[pos] = uint16(clampSigned(qs0-f1, sh) + off)
		pix[pos-across] = uint16(clampSigned(ps0+f2, sh) + off)
		if !hev {
			f = (f1 + 1) >> 1
			pix[pos+across] = uint16(clampSigned(qs1-f, sh) + off)
			pix[pos-2*across] = uint16(clampSigned(ps1+f, sh) + off)
		}
		return
	}
	flat2 := false
	if filterSize >= tx16x16 {
		flat2 = true
		for k := 4; k < 8; k++ {
			if abs32(int32(pix[pos-(k+1)*across])-p0) > one || abs32(int32(pix[pos+k*across])-q0) > one {
				flat2 = false
				break
			}
		}
	}
	if filterSize == tx8x8 || !flat2 {
		wideFilterRef(pix, pos, across, 3)
	} else {
		wideFilterRef(pix, pos, across, 4)
	}
}

// wideFilter applies the 8-sample (log2Size 3) or 16-sample (log2Size 4) smoothing filter across an
// edge: output i is the rounded mean of sample i and the 2n+1 samples around it (edge samples repeated).
// A sliding window sum makes it linear in n instead of quadratic.
func wideFilterRef(pix []uint16, pos, across int, log2Size uint) {
	n := (1 << (log2Size - 1)) - 1
	var s [16]int32 // s[k+8] = sample at offset k, k in [-(n+1), n]
	for k := -(n + 1); k <= n; k++ {
		s[k+8] = int32(pix[pos+k*across])
	}
	at := func(k int) int32 { // sample at offset k with the window clamped to the filtered range
		if k < -(n + 1) {
			k = -(n + 1)
		} else if k > n {
			k = n
		}
		return s[k+8]
	}
	var f [16]int32
	// window sum for i = -n: samples -n-n .. -n+n
	var w int32
	for j := -n; j <= n; j++ {
		w += at(-n + j)
	}
	round := int32(1) << (log2Size - 1)
	for i := -n; i < n; i++ {
		f[i+8] = (w + s[i+8] + round) >> log2Size
		w += at(i+n+1) - at(i-n)
	}
	for i := -n; i < n; i++ {
		pix[pos+i*across] = uint16(f[i+8])
	}
}
