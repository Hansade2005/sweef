package h264

// Direct prediction (8.4.1.2) for B_Skip, B_Direct_16x16 and direct 8x8
// sub-macroblocks, including the colocated picture selection for field,
// frame and MBAFF pictures.

func clip8(v int) int {
	if v < -128 {
		return -128
	}
	if v > 127 {
		return 127
	}
	return v
}

// scaleFactor returns the DistScaleFactor of list0 entry e.
func scaleFactor(e *refEntry, poc, poc1 int) int {
	poc0 := e.poc
	td := clip8(poc1 - poc0)
	if td == 0 || e.pic.longRef {
		return 256
	}
	tb := clip8(poc - poc0)
	tx := (16384 + abs(td)>>1) / td
	return clip3(-1024, 1023, (tb*tx+32)>>6)
}

// directRefListInit prepares the per-slice data used by direct prediction
// and records the reference identities of the picture (for use as a
// colocated picture later).
func (d *Decoder) directRefListInit(h *sliceHeader, structure int) {
	cur := d.curPic
	sidx := (structure & 1) ^ 1
	for l := 0; l < d.listCount; l++ {
		cur.refCount[sidx][l] = h.numRef[l]
		for j := 0; j < h.numRef[l]; j++ {
			cur.refPOC[sidx][l][j] = int(d.refList[l][j].identity())
		}
	}
	if structure == picFrame {
		cur.refCount[1] = cur.refCount[0]
		cur.refPOC[1] = cur.refPOC[0]
	}
	d.colFieldOff = 0
	d.colLong = false
	if d.listCount != 2 || h.numRef[1] == 0 {
		return
	}
	ref1 := &d.refList[1][0]
	// taken now: the marking of a later picture may change it while this one is still being decoded
	d.colLong = ref1.pic.longRef
	if structure == picFrame {
		col := ref1.pic.fieldPOC
		curPOC := cur.poc
		if col[0] == pocInvalid && col[1] == pocInvalid {
			d.colParity = 1
		} else {
			d.colParity = btoi(abs(col[0]-curPOC) >= abs(col[1]-curPOC))
		}
	} else if structure&ref1.ref == 0 && !ref1.pic.mbaff {
		d.colFieldOff = 2*ref1.ref - 3
	}
	if !h.isB() {
		return
	}
	// distance scale factors
	poc := cur.poc
	if structure != picFrame {
		poc = cur.fieldPOC[structure-1]
	}
	poc1 := ref1.poc
	for i := 0; i < h.numRef[0]; i++ {
		d.distScale[i] = scaleFactor(&d.refList[0][i], poc, poc1)
	}
	if d.mbaffFrame {
		for f := 0; f < 2; f++ {
			p := cur.fieldPOC[f]
			p1 := ref1.pic.fieldPOC[f]
			for i := 0; i < 2*h.numRef[0]; i++ {
				d.distScaleField[f][i^f] = scaleFactor(&d.refListField[0][i], p, p1)
			}
		}
	}
	if h.directSpatial {
		return
	}
	sidxCol := d.colParity
	if structure != picFrame {
		sidx = (structure & 1) ^ 1
		sidxCol = (ref1.ref & 1) ^ 1
	} else {
		sidx = d.colParity
	}
	for l := 0; l < 2; l++ {
		d.fillColMap(h, structure, &d.mapColToList0, l, sidx, sidxCol, false)
		if d.mbaffFrame {
			for f := 0; f < 2; f++ {
				d.fillColMap(h, structure, &d.mapColToList0Field[f], l, f, f, true)
			}
		}
	}
}

func (d *Decoder) fillColMap(h *sliceHeader, structure int, m *[2][16 + 32]int, list, field, colField int, mbafi bool) {
	ref1 := d.refList[1][0].pic
	start := 0
	end := h.numRef[0]
	if mbafi {
		start, end = 16, 16+2*h.numRef[0]
	}
	interl := mbafi || structure != picFrame
	m[list] = [16 + 32]int{}
	for rfield := 0; rfield < 2; rfield++ {
		for old := 0; old < ref1.refCount[colField][list]; old++ {
			poc := ref1.refPOC[colField][list][old]
			if !interl {
				poc |= 3
			} else if poc&3 == 3 {
				poc = (poc &^ 3) + rfield + 1
			}
			for j := start; j < end; j++ {
				var e *refEntry
				if mbafi {
					e = &d.refListField[0][j-16]
				} else {
					e = &d.refList[0][j]
				}
				if int(e.identity()) == poc {
					curRef := j
					if mbafi {
						curRef = (j - 16) ^ field
					}
					if ref1.mbaff {
						m[list][2*old+(rfield^field)+16] = curRef
					}
					if rfield == field || !interl {
						m[list][old] = curRef
					}
					break
				}
			}
		}
	}
}

// colocated describes the colocated macroblock(s) of the current one.
type colocated struct {
	pic      *Picture
	mb       [2]int    // colocated macroblock index for y8 = 0, 1
	typ      [2]uint32 // its type
	mismatch bool      // field/frame mode of the macroblock and the colocated one differ
	single   bool      // a single colocated macroblock serves both y8
	pick     int       // half of the field macroblock used by a frame macroblock
}

// colocatedMB implements the colocated macroblock selection of 8.4.1.2.1.
func (d *Decoder) colocatedMB() colocated {
	var c colocated
	col := d.refList[1][0].pic
	c.pic = col
	if col != d.curPic && !col.complete() {
		col.waitRows(min((d.mby+1)*16, col.h)) // the macroblock row of the colocated macroblock is decoded
	}
	stride := d.mbStride
	mbxy := d.mbIdx
	curIL := d.mbField
	colIL := col.mbType[mbxy]&mbInterlaced != 0
	switch {
	case colIL && !curIL: // AFR/FR -> AFL/FL
		x := col.mbIdx(d.mbx, (d.mby&^1)+d.colParity)
		c.mb = [2]int{x, x}
		c.typ = [2]uint32{col.mbType[x], col.mbType[x]}
		c.single, c.mismatch = true, true
		c.pick = d.mby & 1
	case colIL: // field -> field
		x := mbxy + stride*d.colFieldOff
		c.mb = [2]int{x, x}
		c.typ = [2]uint32{col.mbType[x], col.mbType[x]}
		c.single = true
	case curIL: // AFL/FL -> AFR/FR
		top := col.mbIdx(d.mbx, d.mby&^1)
		c.mb = [2]int{top, top + stride}
		c.typ = [2]uint32{col.mbType[top], col.mbType[top+stride]}
		if (c.typ[0]&mbInterlaced != 0) != (c.typ[1]&mbInterlaced != 0) {
			c.typ[0] &^= mbInterlaced
			c.typ[1] &^= mbInterlaced
		}
		c.mismatch = true
	default: // frame -> frame
		c.mb = [2]int{mbxy, mbxy}
		c.typ = [2]uint32{col.mbType[mbxy], col.mbType[mbxy]}
		c.single = true
	}
	return c
}

// block locates the colocated data of 8x8 block (x8, y8) (sub-block i4 of
// it when i4 >= 0, else the corner block used with direct_8x8_inference):
// the macroblock, the 8x8 index and the 4x4 index inside it.
func (c *colocated) block(x8, y8, i4 int) (mb, b8, blk int) {
	switch {
	case c.mismatch && c.single:
		return c.mb[0], c.pick*2 + x8, (y8+2*c.pick)*4 + x8*3
	case c.mismatch:
		return c.mb[y8], y8*2 + x8, (2*y8)*4 + x8*3
	}
	if i4 < 0 {
		return c.mb[0], y8*2 + x8, y8*3*4 + x8*3
	}
	return c.mb[0], y8*2 + x8, (y8*2+i4>>1)*4 + x8*2 + i4&1
}

func smallMV(m mv16) bool { return m.x >= -1 && m.x <= 1 && m.y >= -1 && m.y <= 1 }

// predDirect derives the motion of the direct blocks in mask (bit k = 8x8
// block k) into the caches. The neighbour caches must be filled for spatial
// prediction.
func (d *Decoder) predDirect(mask uint8) {
	if d.hdr.directSpatial {
		d.predSpatialDirect(mask)
	} else {
		d.predTemporalDirect(mask)
	}
}

func (d *Decoder) predSpatialDirect(mask uint8) {
	h := d.hdr
	var ref [2]int
	var mv [2]mv16
	for list := 0; list < 2; list++ {
		refc := &d.refCache[list]
		mvc := &d.mvCache[list]
		leftRef := int(refc[c0-1])
		topRef := int(refc[c0-8])
		refC := int(refc[c0-8+4])
		C := mvc[c0-8+4]
		if refC == refNotAvail {
			refC = int(refc[c0-8-1])
			C = mvc[c0-8-1]
		}
		best := -1
		for _, r := range [3]int{leftRef, topRef, refC} {
			if r >= 0 && (best < 0 || r < best) {
				best = r
			}
		}
		ref[list] = best
		if best >= 0 {
			A := mvc[c0-1]
			B := mvc[c0-8]
			match := btoi(leftRef == best) + btoi(topRef == best) + btoi(refC == best)
			if match > 1 {
				mv[list] = mv16{med3(A.x, B.x, C.x), med3(A.y, B.y, C.y)}
			} else {
				switch {
				case leftRef == best:
					mv[list] = A
				case topRef == best:
					mv[list] = B
				default:
					mv[list] = C
				}
			}
		} else {
			ref[list] = -1
		}
	}
	if ref[0] < 0 && ref[1] < 0 {
		ref[0], ref[1] = 0, 0
	}
	c := d.colocatedMB()
	longCol := d.colLong
	for i8 := 0; i8 < 4; i8++ {
		if mask&(1<<uint(i8)) == 0 {
			continue
		}
		x8, y8 := i8&1, i8>>1
		fillRef(&d.refCache[0], x8*2, y8*2, 2, 2, int8(ref[0]))
		fillRef(&d.refCache[1], x8*2, y8*2, 2, 2, int8(ref[1]))
		nsub := 1
		if !h.sps.Direct8x8 {
			nsub = 4
		}
		colIntra := isIntraT(c.typ[btoi(c.mismatch && !c.single)*y8])
		for s := 0; s < nsub; s++ {
			var m [2]mv16
			m = mv
			if !colIntra && !longCol {
				i4 := -1
				if nsub == 4 {
					i4 = s
				}
				cmb, b8, blk := c.block(x8, y8, i4)
				r0 := int(c.pic.refIdx[0][cmb*4+b8])
				r1 := int(c.pic.refIdx[1][cmb*4+b8])
				var colmv mv16
				zero := false
				if r0 == 0 {
					colmv = c.pic.mv[0][cmb*16+blk]
					zero = smallMV(colmv)
				} else if r0 < 0 && r1 == 0 {
					colmv = c.pic.mv[1][cmb*16+blk]
					zero = smallMV(colmv)
				}
				if zero {
					if ref[0] == 0 {
						m[0] = mv16{}
					}
					if ref[1] == 0 {
						m[1] = mv16{}
					}
				}
			}
			if nsub == 1 {
				fillMV(&d.mvCache[0], x8*2, y8*2, 2, 2, m[0])
				fillMV(&d.mvCache[1], x8*2, y8*2, 2, 2, m[1])
			} else {
				d.mvCache[0][scan8[i8*4+s]] = m[0]
				d.mvCache[1][scan8[i8*4+s]] = m[1]
			}
		}
	}
}

func (d *Decoder) predTemporalDirect(mask uint8) {
	h := d.hdr
	c := d.colocatedMB()
	mapCol := &d.mapColToList0
	scales := &d.distScale
	if d.mbMbaff() {
		f := d.mby & 1
		mapCol = &d.mapColToList0Field[f]
		scales = &d.distScaleField[f]
	}
	refOffset := 0
	if c.pic.mbaff && c.typ[0]&mbInterlaced != 0 {
		refOffset = 16
	}
	for i8 := 0; i8 < 4; i8++ {
		if mask&(1<<uint(i8)) == 0 {
			continue
		}
		x8, y8 := i8&1, i8>>1
		fillRef(&d.refCache[1], x8*2, y8*2, 2, 2, 0)
		colIntra := isIntraT(c.typ[btoi(c.mismatch && !c.single)*y8])
		if colIntra {
			fillRef(&d.refCache[0], x8*2, y8*2, 2, 2, 0)
			fillMV(&d.mvCache[0], x8*2, y8*2, 2, 2, mv16{})
			fillMV(&d.mvCache[1], x8*2, y8*2, 2, 2, mv16{})
			continue
		}
		nsub := 1
		if !h.sps.Direct8x8 {
			nsub = 4
		}
		cmb, b8, _ := c.block(x8, y8, -1)
		r0 := int(c.pic.refIdx[0][cmb*4+b8])
		list := 0
		var ref0 int
		if r0 >= 0 {
			ref0 = mapCol[0][r0+refOffset]
		} else {
			list = 1
			ref0 = mapCol[1][int(c.pic.refIdx[1][cmb*4+b8])+refOffset]
		}
		scale := scales[ref0]
		fillRef(&d.refCache[0], x8*2, y8*2, 2, 2, int8(ref0))
		for s := 0; s < nsub; s++ {
			i4 := -1
			if nsub == 4 {
				i4 = s
			}
			_, _, blk := c.block(x8, y8, i4)
			mvc := c.pic.mv[list][cmb*16+blk]
			var m0, m1 mv16
			if c.mismatch {
				yShift := uint(0)
				if !d.mbField {
					yShift = 2
				}
				myCol := (int(mvc.y) * (1 << yShift)) / 2
				mx := (scale*int(mvc.x) + 128) >> 8
				my := (scale*myCol + 128) >> 8
				m0 = mv16{int16(mx), int16(my)}
				m1 = mv16{int16(mx - int(mvc.x)), int16(my - myCol)}
			} else {
				mx := (scale*int(mvc.x) + 128) >> 8
				my := (scale*int(mvc.y) + 128) >> 8
				m0 = mv16{int16(mx), int16(my)}
				m1 = mv16{int16(mx - int(mvc.x)), int16(my - int(mvc.y))}
			}
			if nsub == 1 {
				fillMV(&d.mvCache[0], x8*2, y8*2, 2, 2, m0)
				fillMV(&d.mvCache[1], x8*2, y8*2, 2, 2, m1)
			} else {
				d.mvCache[0][scan8[i8*4+s]] = m0
				d.mvCache[1][scan8[i8*4+s]] = m1
			}
		}
	}
}
