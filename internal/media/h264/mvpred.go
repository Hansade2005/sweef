package h264

// Motion vector prediction (8.4.1) and direct modes (8.4.1.2).

type nbInfo struct {
	avail bool
	ref   int
	mv    mv16
}

type rect struct{ x, y, w, h int } // 4x4 block units, relative to the macroblock

// neighbor returns the motion data of the 4x4 block at absolute block
// coordinates (nx, ny), or an unavailable marker. curBlk is the decoding
// order index of the current partition's first block.
func (d *Decoder) neighbor(list, nx, ny, curBlk int) nbInfo {
	w4 := d.mbW * 4
	if nx < 0 || ny < 0 || nx >= w4 || ny >= d.mbH*4 {
		return nbInfo{ref: -1}
	}
	nmx, nmy := nx>>2, ny>>2
	if nmx == d.mbx && nmy == d.mby {
		if posToBlk[(ny&3)*4+(nx&3)] >= curBlk {
			return nbInfo{ref: -1}
		}
	} else if d.mbSlice[nmy*d.mbW+nmx] != d.curSlice {
		return nbInfo{ref: -1}
	}
	p := d.cur
	return nbInfo{
		avail: true,
		ref:   int(p.refIdx[list][(ny>>1)*(d.mbW*2)+(nx>>1)]),
		mv:    p.mv[list][ny*w4+nx],
	}
}

func med3(a, b, c int16) int16 {
	if a > b {
		a, b = b, a
	}
	if b > c {
		b = c
	}
	if a > b {
		b = a
	}
	return b
}

// predMV computes the motion vector predictor for a partition whose top-left
// 4x4 block is (bx, by) (macroblock relative) and width w4 blocks. shape is
// 0 for the general case, 1 for the top 16x8, 2 for the bottom 16x8, 3 for
// the left 8x16 and 4 for the right 8x16 partition.
func (d *Decoder) predMV(list, ref, bx, by, w4, shape int) mv16 {
	ax, ay := d.mbx*4+bx, d.mby*4+by
	cur := posToBlk[by*4+bx]
	A := d.neighbor(list, ax-1, ay, cur)
	B := d.neighbor(list, ax, ay-1, cur)
	C := d.neighbor(list, ax+w4, ay-1, cur)
	if !C.avail {
		C = d.neighbor(list, ax-1, ay-1, cur)
	}
	switch shape {
	case 1:
		if B.ref == ref {
			return B.mv
		}
	case 2:
		if A.ref == ref {
			return A.mv
		}
	case 3:
		if A.ref == ref {
			return A.mv
		}
	case 4:
		if C.ref == ref {
			return C.mv
		}
	}
	return medianPred(A, B, C, ref)
}

func medianPred(A, B, C nbInfo, ref int) mv16 {
	if !B.avail && !C.avail && A.avail {
		B, C = A, A
	}
	n := 0
	var m mv16
	if A.ref == ref {
		n++
		m = A.mv
	}
	if B.ref == ref {
		n++
		m = B.mv
	}
	if C.ref == ref {
		n++
		m = C.mv
	}
	if n == 1 {
		return m
	}
	return mv16{med3(A.mv.x, B.mv.x, C.mv.x), med3(A.mv.y, B.mv.y, C.mv.y)}
}

// pSkipMV derives the motion vector of a P_Skip macroblock.
func (d *Decoder) pSkipMV() mv16 {
	ax, ay := d.mbx*4, d.mby*4
	A := d.neighbor(0, ax-1, ay, 0)
	B := d.neighbor(0, ax, ay-1, 0)
	if !A.avail || !B.avail {
		return mv16{}
	}
	if A.ref == 0 && A.mv == (mv16{}) || B.ref == 0 && B.mv == (mv16{}) {
		return mv16{}
	}
	return d.predMV(0, 0, 0, 0, 4, 0)
}

func minPositive(a, b int) int {
	if a >= 0 && b >= 0 {
		return min(a, b)
	}
	return max(a, b)
}

// storeMV records the motion of a rectangle of 4x4 blocks (macroblock
// relative units) for one list. refIdx entries are per 8x8, so r must cover
// whole 8x8 blocks when ref is set.
func (d *Decoder) storeMotion(list int, r rect, ref int, m mv16) {
	p := d.cur
	w4 := d.mbW * 4
	for y := 0; y < r.h; y++ {
		o := (d.mby*4+r.y+y)*w4 + d.mbx*4 + r.x
		for x := 0; x < r.w; x++ {
			p.mv[list][o+x] = m
		}
	}
	d.storeRef(list, r, ref)
}

func (d *Decoder) storeRef(list int, r rect, ref int) {
	p := d.cur
	w8 := d.mbW * 2
	var id int32 = -1
	if ref >= 0 {
		id = d.refList[list][ref].id
	}
	for y := r.y >> 1; y <= (r.y+r.h-1)>>1; y++ {
		for x := r.x >> 1; x <= (r.x+r.w-1)>>1; x++ {
			o := (d.mby*2+y)*w8 + d.mbx*2 + x
			p.refIdx[list][o] = int8(ref)
			p.refID[list][o] = id
		}
	}
}

func (d *Decoder) storeMVD(list int, r rect, v [2]uint8) {
	w4 := d.mbW * 4
	for y := 0; y < r.h; y++ {
		o := (d.mby*4+r.y+y)*w4 + d.mbx*4 + r.x
		for x := 0; x < r.w; x++ {
			d.mvd[list][o+x] = v
		}
	}
}

// ---- direct prediction -------------------------------------------------------------------

type directCtx struct {
	ref   [2]int
	mvp   [2]mv16
	zero  bool // both references negative: zero motion with refIdx 0
	ready bool
}

// spatialDirectSetup derives the reference indices and predictors shared by
// all direct blocks of the macroblock (8.4.1.2.2).
func (d *Decoder) spatialDirectSetup() directCtx {
	var dc directCtx
	ax, ay := d.mbx*4, d.mby*4
	for l := 0; l < 2; l++ {
		A := d.neighbor(l, ax-1, ay, 0)
		B := d.neighbor(l, ax, ay-1, 0)
		C := d.neighbor(l, ax+4, ay-1, 0)
		if !C.avail {
			C = d.neighbor(l, ax-1, ay-1, 0)
		}
		dc.ref[l] = minPositive(A.ref, minPositive(B.ref, C.ref))
		if dc.ref[l] >= 0 {
			dc.mvp[l] = medianPred(A, B, C, dc.ref[l])
		}
	}
	if dc.ref[0] < 0 && dc.ref[1] < 0 {
		dc.ref = [2]int{0, 0}
		dc.zero = true
	}
	dc.ready = true
	return dc
}

// colBlock returns the colocated motion for 4x4 block (bx, by) of the
// current macroblock: list used (0/1, -1 intra), reference index, picture id
// and vector.
func (d *Decoder) colBlock(bx, by int) (ref int, id int32, m mv16) {
	col := d.col
	if d.hdr.sps.Direct8x8 {
		// corner blocks
		if bx > 0 {
			bx = 3
		}
		if by > 0 {
			by = 3
		}
	}
	x4 := d.mbx*4 + bx
	y4 := d.mby*4 + by
	w8 := d.mbW * 2
	o8 := (y4>>1)*w8 + x4>>1
	if col.refIdx[0][o8] >= 0 {
		return int(col.refIdx[0][o8]), col.refID[0][o8], col.mv[0][y4*d.mbW*4+x4]
	}
	if col.refIdx[1][o8] >= 0 {
		return int(col.refIdx[1][o8]), col.refID[1][o8], col.mv[1][y4*d.mbW*4+x4]
	}
	return -1, -1, mv16{}
}

// directBlock fills the motion of one direct-predicted rectangle (4x4 block
// units, within an 8x8 when inference applies).
func (d *Decoder) directBlock(dc *directCtx, r rect) {
	if d.hdr.directSpatial {
		for l := 0; l < 2; l++ {
			ref := dc.ref[l]
			var m mv16
			if ref >= 0 && !dc.zero {
				m = dc.mvp[l]
				if ref == 0 {
					cref, _, cmv := d.colBlock(r.x, r.y)
					if !d.col.longRef && cref == 0 && cmv.x >= -1 && cmv.x <= 1 && cmv.y >= -1 && cmv.y <= 1 {
						m = mv16{}
					}
				}
			}
			d.storeMotion(l, r, ref, m)
		}
		return
	}
	// temporal
	cref, cid, cmv := d.colBlock(r.x, r.y)
	ref0 := 0
	var m0, m1 mv16
	if cref >= 0 {
		for i, p := range d.refList[0] {
			if p.id == cid {
				ref0 = i
				break
			}
		}
		pic0 := d.refList[0][ref0]
		pic1 := d.refList[1][0]
		td := clip3(-128, 127, pic1.poc-pic0.poc)
		if pic0.longRef || td == 0 {
			m0 = cmv
			m1 = mv16{}
		} else {
			tb := clip3(-128, 127, d.cur.poc-pic0.poc)
			tx := (16384 + abs(td/2)) / td
			dsf := clip3(-1024, 1023, (tb*tx+32)>>6)
			m0 = mv16{int16((dsf*int(cmv.x) + 128) >> 8), int16((dsf*int(cmv.y) + 128) >> 8)}
			m1 = mv16{m0.x - cmv.x, m0.y - cmv.y}
		}
	}
	d.storeMotion(0, r, ref0, m0)
	d.storeMotion(1, r, 0, m1)
}

func abs(a int) int {
	if a < 0 {
		return -a
	}
	return a
}

func clip3(lo, hi, v int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// directMB derives direct motion for the 8x8 blocks of the macroblock named
// in mask (bit k = 8x8 block k).
func (d *Decoder) directMB(mask uint8) {
	var dc directCtx
	if d.hdr.directSpatial {
		dc = d.spatialDirectSetup()
	}
	for b8 := 0; b8 < 4; b8++ {
		if mask&(1<<uint(b8)) == 0 {
			continue
		}
		bx, by := (b8&1)*2, (b8>>1)*2
		if d.hdr.sps.Direct8x8 {
			d.directBlock(&dc, rect{bx, by, 2, 2})
		} else {
			for k := 0; k < 4; k++ {
				d.directBlock(&dc, rect{bx + k&1, by + k>>1, 1, 1})
			}
		}
	}
}
