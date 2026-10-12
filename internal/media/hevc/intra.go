package hevc

// intraPredict predicts one transform block of component cIdx at (xC, yC)
// (component sample coordinates) into the current picture.
func (d *Decoder) intraPredict(cIdx, xC, yC, log2, mode int) {
	s, p := d.sp, d.pp
	pic := d.cur
	sc := &d.scratch
	n := 1 << uint(log2)
	pl := pic.planes[cIdx]
	stride := pic.stride[cIdx]
	var sx, sy uint
	bd := s.bitDepthY
	if cIdx > 0 {
		sx, sy = s.subW, s.subH
		bd = s.bitDepthC
	}
	xTbY, yTbY := xC<<sx, yC<<sy
	ref := sc.ref[:4*n+1]
	av := sc.av[:4*n+1]
	ux := 4 >> sx
	uy := 4 >> sy
	constrained := p.constrainedIntra
	avail := func(xN, yN int) bool {
		if !d.availZ(xTbY, yTbY, xN, yN) {
			return false
		}
		if constrained && d.cuAt(xN, yN).flags&cuIntra == 0 {
			return false
		}
		return true
	}
	anyAvail := false
	// left column, bottom to top: index i = 2n-1-y
	for y := 0; y < 2*n; y += uy {
		a := xC > 0 && avail(xTbY-1, (yC+y)<<sy)
		for k := 0; k < uy; k++ {
			i := 2*n - 1 - (y + k)
			av[i] = a
			if a {
				ref[i] = int32(pl[(yC+y+k)*stride+xC-1])
			}
		}
		if a {
			anyAvail = true
		}
	}
	// corner
	{
		a := xC > 0 && yC > 0 && avail(xTbY-1, yTbY-1)
		av[2*n] = a
		if a {
			ref[2*n] = int32(pl[(yC-1)*stride+xC-1])
			anyAvail = true
		}
	}
	// top row
	for x := 0; x < 2*n; x += ux {
		a := yC > 0 && avail((xC+x)<<sx, yTbY-1)
		for k := 0; k < ux; k++ {
			i := 2*n + 1 + x + k
			av[i] = a
			if a {
				ref[i] = int32(pl[(yC-1)*stride+xC+x+k])
			}
		}
		if a {
			anyAvail = true
		}
	}
	if !anyAvail {
		v := int32(1) << uint(bd-1)
		for i := range ref {
			ref[i] = v
		}
	} else {
		if !av[0] {
			j := 1
			for !av[j] {
				j++
			}
			ref[0] = ref[j]
		}
		for i := 1; i < len(ref); i++ {
			if !av[i] {
				ref[i] = ref[i-1]
			}
		}
	}
	rf := ref
	if (cIdx == 0 || s.chromaArrayType == 3) && !s.intraSmoothingOff && mode != 1 && n != 4 {
		minDist := mode - 26
		if minDist < 0 {
			minDist = -minDist
		}
		m2 := mode - 10
		if m2 < 0 {
			m2 = -m2
		}
		if m2 < minDist {
			minDist = m2
		}
		thr := 0
		switch n {
		case 8:
			thr = 7
		case 16:
			thr = 1
		}
		if minDist > thr {
			f := sc.refF[:4*n+1]
			if s.strongIntra && cIdx == 0 && n == 32 &&
				abs32(ref[2*n]+ref[4*n]-2*ref[3*n]) < 1<<uint(bd-5) &&
				abs32(ref[2*n]+ref[0]-2*ref[n]) < 1<<uint(bd-5) {
				// bilinear
				c, t, l := ref[2*n], ref[4*n], ref[0]
				f[2*n] = c
				for i := 0; i < 63; i++ {
					f[2*n+1+i] = (int32(63-i)*c + int32(i+1)*t + 32) >> 6
					f[2*n-1-i] = (int32(63-i)*c + int32(i+1)*l + 32) >> 6
				}
				f[4*n] = t
				f[0] = l
			} else {
				f[0] = ref[0]
				f[4*n] = ref[4*n]
				for i := 1; i < 4*n; i++ {
					f[i] = (ref[i-1] + 2*ref[i] + ref[i+1] + 2) >> 2
				}
			}
			rf = f
		}
	}
	top := func(x int) int32 { return rf[2*n+1+x] } // x = -1 gives the corner
	left := func(y int) int32 { return rf[2*n-1-y] }
	maxV := int32(1)<<uint(bd) - 1
	dst := pl[yC*stride+xC:]
	switch {
	case mode == 0: // planar
		sh := uint(log2 + 1)
		tr := top(n)
		bl := left(n)
		for y := 0; y < n; y++ {
			row := dst[y*stride : y*stride+n]
			l := left(y)
			for x := 0; x < n; x++ {
				row[x] = uint16((int32(n-1-x)*l + int32(x+1)*tr + int32(n-1-y)*top(x) + int32(y+1)*bl + int32(n)) >> sh)
			}
		}
	case mode == 1: // DC
		sum := int32(n)
		for i := 0; i < n; i++ {
			sum += top(i) + left(i)
		}
		dc := sum >> uint(log2+1)
		for y := 0; y < n; y++ {
			row := dst[y*stride : y*stride+n]
			for x := range row {
				row[x] = uint16(dc)
			}
		}
		if cIdx == 0 && n < 32 && !s.intraBoundaryOff {
			dst[0] = uint16((left(0) + 2*dc + top(0) + 2) >> 2)
			for x := 1; x < n; x++ {
				dst[x] = uint16((top(x) + 3*dc + 2) >> 2)
			}
			for y := 1; y < n; y++ {
				dst[y*stride] = uint16((left(y) + 3*dc + 2) >> 2)
			}
		}
	default:
		angle := intraPredAngle[mode]
		var rb [128]int32
		const off = 32
		// Implicit RDPCM together with transquant bypass disables the
		// boundary filters of the horizontal and vertical modes
		// (disableIntraBoundaryFilter of 8.4.4.2.6; HM does the same).
		disableBF := s.implicitRdpcm && d.cu.bypass && !d.compatFFmpeg || s.intraBoundaryOff
		if mode >= 18 {
			for x := 0; x <= n; x++ {
				rb[off+x] = top(x - 1)
			}
			if angle < 0 {
				last := (n * angle) >> 5
				if last < -1 {
					inv := invAngle[mode]
					for x := last; x <= -1; x++ {
						rb[off+x] = left(-1 + ((x*inv + 128) >> 8))
					}
				}
			} else {
				for x := n + 1; x <= 2*n; x++ {
					rb[off+x] = top(x - 1)
				}
			}
			for y := 0; y < n; y++ {
				idx := ((y + 1) * angle) >> 5
				fact := int32(((y + 1) * angle) & 31)
				row := dst[y*stride : y*stride+n]
				if fact != 0 {
					for x := 0; x < n; x++ {
						row[x] = uint16(((32-fact)*rb[off+x+idx+1] + fact*rb[off+x+idx+2] + 16) >> 5)
					}
				} else {
					for x := 0; x < n; x++ {
						row[x] = uint16(rb[off+x+idx+1])
					}
				}
			}
			if mode == 26 && !disableBF && cIdx == 0 && n < 32 {
				for y := 0; y < n; y++ {
					v := top(0) + ((left(y) - top(-1)) >> 1)
					if v < 0 {
						v = 0
					} else if v > maxV {
						v = maxV
					}
					dst[y*stride] = uint16(v)
				}
			}
		} else {
			for x := 0; x <= n; x++ {
				rb[off+x] = left(x - 1)
			}
			if angle < 0 {
				last := (n * angle) >> 5
				if last < -1 {
					inv := invAngle[mode]
					for x := last; x <= -1; x++ {
						rb[off+x] = top(-1 + ((x*inv + 128) >> 8))
					}
				}
			} else {
				for x := n + 1; x <= 2*n; x++ {
					rb[off+x] = left(x - 1)
				}
			}
			for x := 0; x < n; x++ {
				idx := ((x + 1) * angle) >> 5
				fact := int32(((x + 1) * angle) & 31)
				if fact != 0 {
					for y := 0; y < n; y++ {
						dst[y*stride+x] = uint16(((32-fact)*rb[off+y+idx+1] + fact*rb[off+y+idx+2] + 16) >> 5)
					}
				} else {
					for y := 0; y < n; y++ {
						dst[y*stride+x] = uint16(rb[off+y+idx+1])
					}
				}
			}
			if mode == 10 && !disableBF && cIdx == 0 && n < 32 {
				for x := 0; x < n; x++ {
					v := left(0) + ((top(x) - top(-1)) >> 1)
					if v < 0 {
						v = 0
					} else if v > maxV {
						v = maxV
					}
					dst[x] = uint16(v)
				}
			}
		}
	}
}

func abs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}
