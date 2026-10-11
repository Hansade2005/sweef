package av1

// ---- loop restoration (spec 7.17) ----------------------------------------------

type lrCtx struct {
	f          *frameDec
	plane      int
	cur, cdef  *frameBuf
	out        *frameBuf
	planeW     int
	planeH     int
	stripeBuf  []int32
	sbStride   int
	interBuf   []int32
	aBuf, bBuf []int32
	hs, hq     []int32
	yBase      int
	flt        [2][]int32
}

func (f *frameDec) loopRestoration(upCur, upCdef *frameBuf) *frameBuf {
	fh := f.fh
	if !fh.usesLr {
		return upCdef
	}
	out := copyFrameBuf(upCdef)
	for plane := 0; plane < f.numPlanes; plane++ {
		if fh.lrType[plane] == restoreNone {
			continue
		}
		subX, subY := 0, 0
		if plane > 0 {
			subX, subY = f.subX, f.subY
		}
		c := &lrCtx{f: f, plane: plane, cur: upCur, cdef: upCdef, out: out}
		c.planeW = round2(fh.upscaledWidth, uint(subX))
		c.planeH = round2(fh.frameHeight, uint(subY))
		c.sbStride = c.planeW + 6
		unitSize := fh.lrUnitSize[plane]
		unitRows := f.lrUnitRows[plane]
		unitCols := f.lrUnitCols[plane]
		numStripes := (fh.frameHeight-1+8)/64 + 1
		for s := 0; s < numStripes; s++ {
			stripeStartY := (-8 + s*64) >> uint(subY)
			stripeEndY := stripeStartY + (64 >> uint(subY)) - 1
			y0 := imax(0, stripeStartY)
			y1 := imin(c.planeH-1, stripeEndY)
			if y1 < y0 {
				continue
			}
			c.buildStripe(y0, y1, stripeStartY, stripeEndY)
			c.yBase = y0
			lumaY := imax(0, s*64-8)
			unitRow := imin(unitRows-1, ((lumaY+8)>>uint(subY))/unitSize)
			for u := 0; u < unitCols; u++ {
				x0 := u * unitSize
				x1 := imin(c.planeW-1, (u+1)*unitSize-1)
				if u == unitCols-1 {
					x1 = c.planeW - 1
				}
				if x1 < x0 {
					continue
				}
				ui := unitRow*unitCols + u
				switch int(f.lrType[plane][ui]) {
				case restoreWiener:
					cov(covLRWiener)
					c.wiener(ui, x0, y0, x1-x0+1, y1-y0+1, y0)
				case restoreSgrproj:
					cov(covLRSgr)
					c.selfGuided(ui, x0, y0, x1-x0+1, y1-y0+1, y0)
				}
			}
		}
	}
	return out
}

// buildStripe prepares a padded copy (3 samples on each side) of the source
// samples of the stripe rows y0..y1 following the get source sample process.
func (c *lrCtx) buildStripe(y0, y1, stripeStartY, stripeEndY int) {
	rows := y1 - y0 + 1 + 6
	need := rows * c.sbStride
	if cap(c.stripeBuf) < need {
		c.stripeBuf = make([]int32, need)
	}
	c.stripeBuf = c.stripeBuf[:need]
	planeEndX := c.planeW - 1
	planeEndY := c.planeH - 1
	cs := c.cur.stride[c.plane]
	ds := c.cdef.stride[c.plane]
	for r := 0; r < rows; r++ {
		y := y0 - 3 + r
		y = imax(0, imin(planeEndY, y))
		var row []uint16
		if y < stripeStartY {
			y = imax(stripeStartY-2, y)
			row = c.cur.pl[c.plane][y*cs:]
		} else if y > stripeEndY {
			y = imin(stripeEndY+2, y)
			row = c.cur.pl[c.plane][y*cs:]
		} else {
			row = c.cdef.pl[c.plane][y*ds:]
		}
		dst := c.stripeBuf[r*c.sbStride : (r+1)*c.sbStride]
		for x := -3; x < c.planeW+3; x++ {
			xx := imax(0, imin(planeEndX, x))
			dst[x+3] = int32(row[xx])
		}
	}
}

func (c *lrCtx) wiener(ui, x, y, w, h, yBase int) {
	f := c.f
	bd := f.bitDepth
	r0, r1, _ := f.roundingVars(false)
	var vf, hf [7]int
	coef := func(cf [3]int8, out *[7]int) {
		out[3] = 128
		for i := 0; i < 3; i++ {
			v := int(cf[i])
			out[i] = v
			out[6-i] = v
			out[3] -= 2 * v
		}
	}
	coef(f.lrWiener[c.plane][ui][0], &vf)
	coef(f.lrWiener[c.plane][ui][1], &hf)
	offset := 1 << uint(bd+7-r0-1)
	limit := (1 << uint(bd+1+7-r0)) - 1
	if cap(c.interBuf) < (h+6)*w {
		c.interBuf = make([]int32, (h+6)*w)
	}
	inter := c.interBuf[:(h+6)*w]
	for r := 0; r < h+6; r++ {
		src := c.stripeBuf[r*c.sbStride+x:]
		for cc := 0; cc < w; cc++ {
			s := 0
			for t := 0; t < 7; t++ {
				s += hf[t] * int(src[cc+t])
			}
			v := round2(s, uint(r0))
			inter[r*w+cc] = int32(clip3(-offset, limit-offset, v))
		}
	}
	maxPix := (1 << uint(bd)) - 1
	dst := c.out.pl[c.plane]
	ds := c.out.stride[c.plane]
	for r := 0; r < h; r++ {
		for cc := 0; cc < w; cc++ {
			s := 0
			for t := 0; t < 7; t++ {
				s += vf[t] * int(inter[(r+t)*w+cc])
			}
			v := round2(s, uint(r1))
			dst[(y+r)*ds+x+cc] = uint16(clip3(0, maxPix, v))
		}
	}
}

func (c *lrCtx) boxFilter(x, y, w, h, set, pass int, out []int32) {
	f := c.f
	bd := f.bitDepth
	r := sgrParams[set][pass*2]
	if r == 0 {
		return
	}
	eps := sgrParams[set][pass*2+1]
	n := (2*r + 1) * (2*r + 1)
	n2e := n * n * eps
	s := ((1 << 20) + n2e/2) / n2e
	oneOverN := ((1 << 12) + n/2) / n
	aw := w + 2
	ah := h + 2
	if cap(c.aBuf) < aw*ah {
		c.aBuf = make([]int32, aw*ah)
		c.bBuf = make([]int32, aw*ah)
	}
	A := c.aBuf[:aw*ah]
	B := c.bBuf[:aw*ah]
	// horizontal window sums for rows -1-r .. h+r, columns -1..w
	nr := h + 2 + 2*r
	if cap(c.hs) < nr*aw {
		c.hs = make([]int32, nr*aw)
		c.hq = make([]int32, nr*aw)
	}
	hs := c.hs[:nr*aw]
	hq := c.hq[:nr*aw]
	for rr := 0; rr < nr; rr++ {
		// source row index in stripeBuf: sample row (-1-r+rr) relative to y0 -> +3 padding
		src := c.stripeBuf[(rr-1-r+3+(y-c.yBase))*c.sbStride:]
		for j := -1; j <= w; j++ {
			var sb, sq int32
			for dx := -r; dx <= r; dx++ {
				v := src[x+j+dx+3]
				sb += v
				sq += v * v
			}
			hs[rr*aw+j+1] = sb
			hq[rr*aw+j+1] = sq
		}
	}
	for i := -1; i < h+1; i++ {
		for j := -1; j < w+1; j++ {
			var a, b int
			for dy := -r; dy <= r; dy++ {
				idx := (i+1+r+dy)*aw + j + 1
				a += int(hq[idx])
				b += int(hs[idx])
			}
			a = round2(a, uint(2*(bd-8)))
			d := round2(b, uint(bd-8))
			p := imax(0, a*n-d*d)
			z := round2(p*s, 20)
			var a2 int
			if z >= 255 {
				a2 = 256
			} else if z == 0 {
				a2 = 1
			} else {
				a2 = ((z << 8) + z/2) / (z + 1)
			}
			b2 := (256 - a2) * b * oneOverN
			A[(i+1)*aw+j+1] = int32(a2)
			B[(i+1)*aw+j+1] = int32(round2(b2, 12))
		}
	}
	cdef := c.cdef.pl[c.plane]
	cds := c.cdef.stride[c.plane]
	for i := 0; i < h; i++ {
		shift := 5
		if pass == 0 && (i&1) != 0 {
			shift = 4
		}
		for j := 0; j < w; j++ {
			a, b := 0, 0
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					var weight int
					if pass == 0 {
						if (i+dy)&1 != 0 {
							if dx == 0 {
								weight = 6
							} else {
								weight = 5
							}
						}
					} else {
						if dx == 0 || dy == 0 {
							weight = 4
						} else {
							weight = 3
						}
					}
					if weight != 0 {
						a += weight * int(A[(i+1+dy)*aw+j+1+dx])
						b += weight * int(B[(i+1+dy)*aw+j+1+dx])
					}
				}
			}
			v := a*int(cdef[(y+i)*cds+x+j]) + b
			out[i*w+j] = int32(round2(v, uint(8+shift-4)))
		}
	}
}

func (c *lrCtx) selfGuided(ui, x, y, w, h, yBase int) {
	f := c.f
	set := int(f.lrSgrSet[c.plane][ui])
	for p := 0; p < 2; p++ {
		if cap(c.flt[p]) < w*h {
			c.flt[p] = make([]int32, w*h)
		}
		c.flt[p] = c.flt[p][:w*h]
		c.boxFilter(x, y, w, h, set, p, c.flt[p])
	}
	w0 := int(f.lrSgrXqd[c.plane][ui][0])
	w1 := int(f.lrSgrXqd[c.plane][ui][1])
	w2 := (1 << 7) - w0 - w1
	r0 := sgrParams[set][0]
	r1 := sgrParams[set][2]
	maxPix := (1 << uint(f.bitDepth)) - 1
	cdef := c.cdef.pl[c.plane]
	cds := c.cdef.stride[c.plane]
	dst := c.out.pl[c.plane]
	ds := c.out.stride[c.plane]
	for i := 0; i < h; i++ {
		for j := 0; j < w; j++ {
			u := int(cdef[(y+i)*cds+x+j]) << 4
			v := w1 * u
			if r0 != 0 {
				v += w0 * int(c.flt[0][i*w+j])
			} else {
				v += w0 * u
			}
			if r1 != 0 {
				v += w2 * int(c.flt[1][i*w+j])
			} else {
				v += w2 * u
			}
			s := round2(v, 4+7)
			dst[(y+i)*ds+x+j] = uint16(clip3(0, maxPix, s))
		}
	}
}
