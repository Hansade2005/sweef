package av1

// ---- loop restoration syntax (spec 5.11.57, 5.11.58) ---------------------------

func (s *symDec) decodeSubexpBool(numSyms, k int) int {
	i, mk := 0, 0
	for {
		b2 := k
		if i != 0 {
			b2 = k + i - 1
		}
		a := 1 << uint(b2)
		if numSyms <= mk+3*a {
			return s.ns(numSyms-mk) + mk
		}
		if s.literal(1) != 0 {
			i++
			mk += a
		} else {
			return s.literal(b2) + mk
		}
	}
}

func (s *symDec) decodeSignedSubexpWithRefBool(low, high, k, r int) int {
	mx := high - low
	rr := r - low
	v := s.decodeSubexpBool(mx, k)
	var x int
	if (rr << 1) <= mx {
		x = inverseRecenter(rr, v)
	} else {
		x = mx - 1 - inverseRecenter(mx-1-rr, v)
	}
	return x + low
}

func (f *frameDec) readLr(r, c, bSize int) {
	fh := f.fh
	if fh.allowIntrabc {
		return
	}
	w := num4x4BlocksWide[bSize]
	h := num4x4BlocksHigh[bSize]
	for plane := 0; plane < f.numPlanes; plane++ {
		if fh.lrType[plane] == restoreNone {
			continue
		}
		subX, subY := 0, 0
		if plane > 0 {
			subX, subY = f.subX, f.subY
		}
		unitSize := fh.lrUnitSize[plane]
		unitRows := f.lrUnitRows[plane]
		unitCols := f.lrUnitCols[plane]
		unitRowStart := (r*(4>>uint(subY)) + unitSize - 1) / unitSize
		unitRowEnd := imin(unitRows, ((r+h)*(4>>uint(subY))+unitSize-1)/unitSize)
		var numerator, denominator int
		if fh.useSuperres {
			numerator = (4 >> uint(subX)) * fh.superresDenom
			denominator = unitSize * superresNum
		} else {
			numerator = 4 >> uint(subX)
			denominator = unitSize
		}
		unitColStart := (c*numerator + denominator - 1) / denominator
		unitColEnd := imin(unitCols, ((c+w)*numerator+denominator-1)/denominator)
		for unitRow := unitRowStart; unitRow < unitRowEnd; unitRow++ {
			for unitCol := unitColStart; unitCol < unitColEnd; unitCol++ {
				f.readLrUnit(plane, unitRow, unitCol)
			}
		}
	}
}

func (f *frameDec) readLrUnit(plane, unitRow, unitCol int) {
	fh := f.fh
	ui := unitRow*f.lrUnitCols[plane] + unitCol
	var rtype int
	switch fh.lrType[plane] {
	case restoreWiener:
		if f.sym.symbol(f.tcdf.useWiener[:]) != 0 {
			rtype = restoreWiener
		}
	case restoreSgrproj:
		if f.sym.symbol(f.tcdf.useSgrproj[:]) != 0 {
			rtype = restoreSgrproj
		}
	default:
		rtype = f.sym.symbol(f.tcdf.restorationType[:])
	}
	f.lrType[plane][ui] = uint8(rtype)
	if rtype == restoreWiener {
		for pass := 0; pass < 2; pass++ {
			first := 0
			if plane > 0 {
				first = 1
				f.lrWiener[plane][ui][pass][0] = 0
			}
			for j := first; j < 3; j++ {
				mn := wienerTapsMin[j]
				mx := wienerTapsMax[j]
				k := wienerTapsK[j]
				v := f.sym.decodeSignedSubexpWithRefBool(mn, mx+1, k, f.refLrWiener[plane][pass][j])
				f.lrWiener[plane][ui][pass][j] = int8(v)
				f.refLrWiener[plane][pass][j] = v
			}
		}
	} else if rtype == restoreSgrproj {
		set := f.sym.literal(4)
		f.lrSgrSet[plane][ui] = uint8(set)
		for i := 0; i < 2; i++ {
			radius := sgrParams[set][i*2]
			mn := sgrprojXqdMin[i]
			mx := sgrprojXqdMax[i]
			var v int
			if radius != 0 {
				v = f.sym.decodeSignedSubexpWithRefBool(mn, mx+1, 4, f.refSgrXqd[plane][i])
			} else if i == 1 {
				v = clip3(mn, mx, (1<<7)-f.refSgrXqd[plane][0])
			}
			f.lrSgrXqd[plane][ui][i] = int8(v)
			f.refSgrXqd[plane][i] = v
		}
	}
}
