package vp9

// Loop filter (spec 8.8).

func (d *Decoder) loopFilterFrameInit() {
	nShift := uint(d.lfLevel >> 5)
	for seg := 0; seg < 8; seg++ {
		lvlSeg := d.lfLevel
		if d.seg.enabled && d.seg.featEnabled[seg][segLvlAltL] {
			if d.seg.absDelta {
				lvlSeg = d.seg.featData[seg][segLvlAltL]
			} else {
				lvlSeg = d.seg.featData[seg][segLvlAltL] + d.lfLevel
			}
			lvlSeg = clip3(0, 63, lvlSeg)
		}
		if !d.lfDeltaEnabled {
			for r := 0; r < 4; r++ {
				for m := 0; m < 2; m++ {
					d.lvlLookup[seg][r][m] = uint8(lvlSeg)
				}
			}
			continue
		}
		intra := lvlSeg + (d.lfRefDeltas[intraFrame] << nShift)
		d.lvlLookup[seg][intraFrame][0] = uint8(clip3(0, 63, intra))
		d.lvlLookup[seg][intraFrame][1] = d.lvlLookup[seg][intraFrame][0]
		for r := lastFrame; r < 4; r++ {
			for m := 0; m < 2; m++ {
				l := lvlSeg + (d.lfRefDeltas[r] << nShift) + (d.lfModeDeltas[m] << nShift)
				d.lvlLookup[seg][r][m] = uint8(clip3(0, 63, l))
			}
		}
	}
}

func (d *Decoder) loopFilterFrame() {
	d.loopFilterFrameInit()
	for row := 0; row < d.miRows; row += 8 {
		for col := 0; col < d.miCols; col += 8 {
			for plane := 0; plane < 3; plane++ {
				for pass := 0; pass < 2; pass++ {
					d.loopFilterSB(plane, pass, row, col)
				}
			}
		}
	}
}

func (d *Decoder) loopFilterSB(plane, pass, row, col int) {
	subX, subY := 0, 0
	if plane > 0 {
		subX, subY = 1, 1
	}
	var sub, edgeLen int
	if pass == 0 {
		sub = subX
		edgeLen = 64 >> uint(subY)
	} else {
		sub = subY
		edgeLen = 64 >> uint(subX)
	}
	fr := d.cur
	stride := fr.stride[plane]
	pix := fr.pix[plane]
	nEdges := 16 >> uint(sub)
	for edge := 0; edge < nEdges; edge++ {
		for seg := 0; seg < edgeLen; seg += 8 {
			var x, y int
			if pass == 0 {
				x = col*8 + edge*(4<<uint(subX))
				y = row*8 + (seg << uint(subY))
			} else {
				x = col*8 + (seg << uint(subX))
				y = row*8 + edge*(4<<uint(subY))
			}
			if x >= 8*d.miCols || y >= 8*d.miRows {
				continue
			}
			if (pass == 0 && x == 0) || (pass == 1 && y == 0) {
				continue
			}
			// samples of this segment that lie on screen
			var limit int
			if pass == 0 {
				limit = (8*d.miRows - row*8 + (1 << uint(subY)) - 1) >> uint(subY)
			} else {
				limit = (8*d.miCols - col*8 + (1 << uint(subX)) - 1) >> uint(subX)
			}
			cnt := min(8, limit-seg)
			if cnt <= 0 {
				continue
			}
			loopCol := ((x >> 3) >> uint(subX)) << uint(subX)
			loopRow := ((y >> 3) >> uint(subY)) << uint(subY)
			mi := &d.mi[loopRow*d.miCols+loopCol]
			miSize := int(mi.size)
			txSz := int(mi.txSize)
			if plane > 0 {
				if miSize < block8x8 {
					txSz = tx4x4
				} else {
					txSz = min(txSz, int(maxTxSize[ssSizeLookup[miSize][1][1]]))
				}
			}
			sbSize := miSize
			if sub == 1 && sbSize < block16x16 {
				sbSize = block16x16
			}
			isIntra := mi.refFrame[0] <= intraFrame
			var isBlockEdge bool
			if pass == 0 {
				isBlockEdge = x%(8*int(num8x8Wide[sbSize])) == 0
			} else {
				isBlockEdge = y%(8*int(num8x8High[sbSize])) == 0
			}
			var isTxEdge bool
			if pass == 1 && subX == 1 && d.miCols&1 == 1 && edge&1 == 1 && x+8 >= d.miCols*8 {
				isTxEdge = false
			} else {
				isTxEdge = edge%(1<<uint(txSz)) == 0
			}
			is32Edge := edge%8 == 0
			apply := isBlockEdge || (isTxEdge && (isIntra || !mi.skip))
			if !apply {
				continue
			}
			// filter size
			baseSize := min(tx16x16, txSz)
			if txSz == tx4x4 && is32Edge {
				baseSize = tx8x8
			}
			filterSize := baseSize
			if pass == 0 && subX == 1 && baseSize == tx16x16 && x>>3 == d.miCols-1 {
				filterSize = tx8x8
			} else if pass == 1 && subY == 1 && baseSize == tx16x16 && y>>3 == d.miRows-1 {
				filterSize = tx8x8
			}
			// adaptive filter strength
			mode := int(mi.yMode)
			modeType := 0
			if mode == nearestMv || mode == nearMv || mode == newMv {
				modeType = 1
			}
			lvl := int(d.lvlLookup[mi.segID][mi.refFrame[0]][modeType])
			if lvl == 0 {
				continue
			}
			shift := uint(0)
			if d.lfSharp > 4 {
				shift = 2
			} else if d.lfSharp > 0 {
				shift = 1
			}
			var lim int
			if d.lfSharp > 0 {
				lim = clip3(1, 9-d.lfSharp, lvl>>shift)
			} else {
				lim = max(1, lvl>>shift)
			}
			blimit := 2*(lvl+2) + lim
			thresh := lvl >> 4
			px := x >> uint(subX)
			py := y >> uint(subY)
			var pos, across, along int
			if pass == 0 {
				pos = py*stride + px
				across, along = 1, stride
			} else {
				pos = py*stride + px
				across, along = stride, 1
			}
			for k := 0; k < cnt; k++ {
				filterSample(pix, pos+k*along, across, lim, blimit, thresh, filterSize)
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

func filter4Clamp(v int32) int32 {
	if v < -128 {
		return -128
	}
	if v > 127 {
		return 127
	}
	return v
}

func filterSample(pix []byte, pos, across, limit, blimit, thresh, filterSize int) {
	q0 := int32(pix[pos])
	q1 := int32(pix[pos+across])
	q2 := int32(pix[pos+2*across])
	q3 := int32(pix[pos+3*across])
	p0 := int32(pix[pos-across])
	p1 := int32(pix[pos-2*across])
	p2 := int32(pix[pos-3*across])
	p3 := int32(pix[pos-4*across])
	lim := int32(limit)
	hev := abs32(p1-p0) > int32(thresh) || abs32(q1-q0) > int32(thresh)
	if abs32(p3-p2) > lim || abs32(p2-p1) > lim || abs32(p1-p0) > lim ||
		abs32(q1-q0) > lim || abs32(q2-q1) > lim || abs32(q3-q2) > lim ||
		abs32(p0-q0)*2+abs32(p1-q1)/2 > int32(blimit) {
		return
	}
	flat := false
	if filterSize >= tx8x8 {
		flat = abs32(p1-p0) <= 1 && abs32(q1-q0) <= 1 && abs32(p2-p0) <= 1 && abs32(q2-q0) <= 1 && abs32(p3-p0) <= 1 && abs32(q3-q0) <= 1
	}
	if filterSize == tx4x4 || !flat {
		ps1, ps0, qs0, qs1 := p1-128, p0-128, q0-128, q1-128
		var f int32
		if hev {
			f = filter4Clamp(ps1 - qs1)
		}
		f = filter4Clamp(f + 3*(qs0-ps0))
		f1 := filter4Clamp(f+4) >> 3
		f2 := filter4Clamp(f+3) >> 3
		pix[pos] = uint8(filter4Clamp(qs0-f1) + 128)
		pix[pos-across] = uint8(filter4Clamp(ps0+f2) + 128)
		if !hev {
			f = (f1 + 1) >> 1
			pix[pos+across] = uint8(filter4Clamp(qs1-f) + 128)
			pix[pos-2*across] = uint8(filter4Clamp(ps1+f) + 128)
		}
		return
	}
	flat2 := false
	if filterSize >= tx16x16 {
		flat2 = true
		for k := 4; k < 8; k++ {
			if abs32(int32(pix[pos-(k+1)*across])-p0) > 1 || abs32(int32(pix[pos+k*across])-q0) > 1 {
				flat2 = false
				break
			}
		}
	}
	if filterSize == tx8x8 || !flat2 {
		wideFilter(pix, pos, across, 3)
	} else {
		wideFilter(pix, pos, across, 4)
	}
}

func wideFilter(pix []byte, pos, across int, log2Size uint) {
	n := (1 << (log2Size - 1)) - 1
	var s [16]int32 // s[k+8] = sample at offset k, k in [-8, 7]
	for k := -(n + 1); k <= n; k++ {
		s[k+8] = int32(pix[pos+k*across])
	}
	var f [16]int32
	for i := -n; i < n; i++ {
		t := s[i+8]
		for j := -n; j <= n; j++ {
			p := clip3(-(n + 1), n, i+j)
			t += s[p+8]
		}
		f[i+8] = (t + (1 << (log2Size - 1))) >> log2Size
	}
	for i := -n; i < n; i++ {
		pix[pos+i*across] = uint8(f[i+8])
	}
}
