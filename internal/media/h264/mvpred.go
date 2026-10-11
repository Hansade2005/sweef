package h264

// Motion vector prediction (8.4.1.3) over the neighbour caches.

type rect struct{ x, y, w, h int } // 4x4 block units, relative to the macroblock

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

func fillRef(c *[40]int8, x, y, w, h int, v int8) {
	for j := 0; j < h; j++ {
		for i := 0; i < w; i++ {
			c[c0+(y+j)*8+x+i] = v
		}
	}
}

func fillMV(c *[40]mv16, x, y, w, h int, v mv16) {
	for j := 0; j < h; j++ {
		for i := 0; i < w; i++ {
			c[c0+(y+j)*8+x+i] = v
		}
	}
}

func fillMVD(c *[40][2]uint8, x, y, w, h int, v [2]uint8) {
	for j := 0; j < h; j++ {
		for i := 0; i < w; i++ {
			c[c0+(y+j)*8+x+i] = v
		}
	}
}

// fetchDiagonalMV returns the reference and vector of the diagonal (C, or D
// when C is unavailable) neighbour of the partition at cache index i.
func (d *Decoder) fetchDiagonalMV(i, list, partWidth int) (int, mv16) {
	refc := &d.refCache[list]
	mvc := &d.mvCache[list]
	topRightRef := int(refc[i-8+partWidth])
	// MBAFF: the mapping of motion vectors to neighbouring locations cannot
	// be expressed in the caches alone.
	if d.mbaffFrame {
		if topRightRef == refNotAvail && i >= c0+8 && i&7 == 4 && refc[c0-1] != refNotAvail {
			pic := d.curPic
			nb := &d.nb
			mvc[c0-2] = mv16{}
			if !d.mbField && nb.leftType[0]&mbInterlaced != 0 {
				xy := nb.leftXY[0] + d.mbStride
				y4 := (d.mby&1)*2 + (i >> 5)
				t := pic.mbType[xy+(y4>>2)*d.mbStride]
				if !usesList(t, list) {
					return refNotUsed, mvc[c0-2]
				}
				mv := pic.mv[list][xy*16+3+y4*4]
				mvc[c0-2] = mv16{mv.x, mv.y * 2}
				return int(pic.refIdx[list][xy*4+1+(y4&^1)]) >> 1, mvc[c0-2]
			}
			if d.mbField && nb.leftType[0]&mbInterlaced == 0 {
				sel := 0
				if i >= 36 {
					sel = 1
				}
				xy := nb.leftXY[sel]
				y4 := (i >> 2) & 3
				t := pic.mbType[xy+(y4>>2)*d.mbStride]
				if !usesList(t, list) {
					return refNotUsed, mvc[c0-2]
				}
				mv := pic.mv[list][xy*16+3+y4*4]
				mvc[c0-2] = mv16{mv.x, mv.y / 2}
				return int(pic.refIdx[list][xy*4+1+(y4&^1)]) * 2, mvc[c0-2]
			}
		}
	}
	if topRightRef != refNotAvail {
		return topRightRef, mvc[i-8+partWidth]
	}
	return int(refc[i-8-1]), mvc[i-8-1]
}

// predMotion predicts the vector of the partition starting at block n with
// the given width (in 4x4 blocks) and reference.
func (d *Decoder) predMotion(n, partWidth, list, ref int) (int, int) {
	index8 := scan8[n]
	refc := &d.refCache[list]
	mvc := &d.mvCache[list]
	topRef := int(refc[index8-8])
	leftRef := int(refc[index8-1])
	A := mvc[index8-1]
	B := mvc[index8-8]
	diagRef, C := d.fetchDiagonalMV(index8, list, partWidth)
	match := btoi(diagRef == ref) + btoi(topRef == ref) + btoi(leftRef == ref)
	switch {
	case match > 1:
		return int(med3(A.x, B.x, C.x)), int(med3(A.y, B.y, C.y))
	case match == 1:
		switch {
		case leftRef == ref:
			return int(A.x), int(A.y)
		case topRef == ref:
			return int(B.x), int(B.y)
		}
		return int(C.x), int(C.y)
	}
	if topRef == refNotAvail && diagRef == refNotAvail && leftRef != refNotAvail {
		return int(A.x), int(A.y)
	}
	return int(med3(A.x, B.x, C.x)), int(med3(A.y, B.y, C.y))
}

func (d *Decoder) pred16x8Motion(n, list, ref int) (int, int) {
	refc := &d.refCache[list]
	mvc := &d.mvCache[list]
	if n == 0 {
		if int(refc[c0-8]) == ref {
			B := mvc[c0-8]
			return int(B.x), int(B.y)
		}
	} else {
		if int(refc[scan8[8]-1]) == ref {
			A := mvc[scan8[8]-1]
			return int(A.x), int(A.y)
		}
	}
	return d.predMotion(n, 4, list, ref)
}

func (d *Decoder) pred8x16Motion(n, list, ref int) (int, int) {
	refc := &d.refCache[list]
	mvc := &d.mvCache[list]
	if n == 0 {
		if int(refc[c0-1]) == ref {
			A := mvc[c0-1]
			return int(A.x), int(A.y)
		}
	} else {
		diagRef, C := d.fetchDiagonalMV(scan8[4], list, 2)
		if diagRef == ref {
			return int(C.x), int(C.y)
		}
	}
	return d.predMotion(n, 2, list, ref)
}

// fixMVMBAFF adapts a neighbour's reference and vector to the current
// macroblock's field/frame mode (FIX_MV_MBAFF).
func (d *Decoder) fixMVMBAFF(typ uint32, ref int, mv mv16) (int, mv16) {
	if d.mbaffFrame {
		if d.mbField {
			if typ&mbInterlaced == 0 {
				ref <<= 1
				mv.y /= 2
			}
		} else if typ&mbInterlaced != 0 {
			ref >>= 1
			mv.y *= 2
		}
	}
	return ref, mv
}

// predPSkipMotion derives the motion vector of a P_Skip macroblock.
func (d *Decoder) predPSkipMotion() mv16 {
	pic := d.curPic
	nb := &d.nb
	refs := pic.refIdx[0]
	mvs := pic.mv[0]
	var A, B, C mv16
	var topRef, leftRef, diagRef int
	if usesList(nb.leftType[0], 0) {
		lr := int(nb.leftRow[0])
		leftRef = int(refs[nb.leftXY[0]*4+1+int(nb.leftRow[0]&^1)])
		A = mvs[nb.leftXY[0]*16+lr*4+3]
		leftRef, A = d.fixMVMBAFF(nb.leftType[0], leftRef, A)
		if leftRef == 0 && A == (mv16{}) {
			return mv16{}
		}
	} else if nb.leftType[0] != 0 {
		leftRef = refNotUsed
	} else {
		return mv16{}
	}
	if usesList(nb.topType, 0) {
		topRef = int(refs[nb.topXY*4+2])
		B = mvs[nb.topXY*16+12]
		topRef, B = d.fixMVMBAFF(nb.topType, topRef, B)
		if topRef == 0 && B == (mv16{}) {
			return mv16{}
		}
	} else if nb.topType != 0 {
		topRef = refNotUsed
	} else {
		return mv16{}
	}
	if usesList(nb.topRightType, 0) {
		diagRef = int(refs[nb.topRightXY*4+2])
		C = mvs[nb.topRightXY*16+12]
		diagRef, C = d.fixMVMBAFF(nb.topRightType, diagRef, C)
	} else if nb.topRightType != 0 {
		diagRef = refNotUsed
	} else {
		if usesList(nb.topLeftType, 0) {
			if nb.topLeftPartition != 0 {
				diagRef = int(refs[nb.topLeftXY*4+3])
				C = mvs[nb.topLeftXY*16+15]
			} else {
				diagRef = int(refs[nb.topLeftXY*4+1])
				C = mvs[nb.topLeftXY*16+7]
			}
			diagRef, C = d.fixMVMBAFF(nb.topLeftType, diagRef, C)
		} else if nb.topLeftType != 0 {
			diagRef = refNotUsed
		} else {
			diagRef = refNotAvail
		}
	}
	match := btoi(diagRef == 0) + btoi(topRef == 0) + btoi(leftRef == 0)
	switch {
	case match > 1:
		return mv16{med3(A.x, B.x, C.x), med3(A.y, B.y, C.y)}
	case match == 1:
		switch {
		case leftRef == 0:
			return A
		case topRef == 0:
			return B
		}
		return C
	}
	return mv16{med3(A.x, B.x, C.x), med3(A.y, B.y, C.y)}
}
