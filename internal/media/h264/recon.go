package h264

// Macroblock reconstruction: inter prediction, intra prediction and
// residual addition.

func (d *Decoder) lumaOff(bx, by int) int {
	return (d.mby*16+by*4)*d.cur.ys + d.mbx*16 + bx*4
}

func (d *Decoder) chromaOff(bx, by int) int {
	return (d.mby*8+by*4)*d.cur.cs + d.mbx*8 + bx*4
}

// mcRect performs motion compensated prediction of one rectangle (4x4 block
// units, macroblock relative) into the current picture.
func (d *Decoder) mcRect(r rect) {
	p := d.cur
	h := d.hdr
	w4 := d.mbW * 4
	w8 := d.mbW * 2
	gx4 := d.mbx*4 + r.x
	gy4 := d.mby*4 + r.y
	refs := [2]int{int(p.refIdx[0][(gy4>>1)*w8+gx4>>1]), int(p.refIdx[1][(gy4>>1)*w8+gx4>>1])}
	mvs := [2]mv16{p.mv[0][gy4*w4+gx4], p.mv[1][gy4*w4+gx4]}
	px, py := gx4*4, gy4*4
	bw, bh := r.w*4, r.h*4
	nUse := 0
	var used [2]bool
	for l := 0; l < 2; l++ {
		if refs[l] < 0 || refs[l] >= len(d.refList[l]) {
			continue
		}
		used[l] = true
		nUse++
		ref := d.refList[l][refs[l]]
		m := mvs[l]
		out := d.pred[l][:]
		d.lumaMC(out, 16, ref.y, ref.ys, p.w, p.h, px+int(m.x>>2), py+int(m.y>>2), int(m.x&3), int(m.y&3), bw, bh)
		cx, cy := px/2+int(m.x>>3), py/2+int(m.y>>3)
		fx, fy := int(m.x&7), int(m.y&7)
		d.chromaMC(out[256:], 8, ref.cb, ref.cs, p.w/2, p.h/2, cx, cy, fx, fy, bw/2, bh/2)
		d.chromaMC(out[320:], 8, ref.cr, ref.cs, p.w/2, p.h/2, cx, cy, fx, fy, bw/2, bh/2)
	}
	if nUse == 0 {
		return
	}
	// weights
	mode := 0 // 0 default, 1 explicit, 2 implicit
	if h.hasPW {
		mode = 1
	} else if h.isB() && h.pps.WeightedBipredIdc == 2 && nUse == 2 {
		mode = 2
	}
	yo := d.lumaOff(r.x, r.y)
	cw, ch := bw/2, bh/2
	co := (d.mby*8+r.y*2)*p.cs + d.mbx*8 + r.x*2
	type plane struct {
		dst     []byte
		off, ds int
		src     int // offset into pred buffer
		ss      int
		w, h    int
		logWD   int
		wt, ofs [2]int
	}
	var pls [3]plane
	pls[0] = plane{p.y, yo, p.ys, 0, 16, bw, bh, h.logWDLuma, [2]int{}, [2]int{}}
	pls[1] = plane{p.cb, co, p.cs, 256, 8, cw, ch, h.logWDChroma, [2]int{}, [2]int{}}
	pls[2] = plane{p.cr, co, p.cs, 320, 8, cw, ch, h.logWDChroma, [2]int{}, [2]int{}}
	for c := 0; c < 3; c++ {
		pl := &pls[c]
		switch mode {
		case 1:
			for l := 0; l < 2; l++ {
				if !used[l] {
					continue
				}
				if c == 0 {
					pl.wt[l], pl.ofs[l] = h.pw[l].lumaW[refs[l]], h.pw[l].lumaO[refs[l]]
				} else {
					pl.wt[l], pl.ofs[l] = h.pw[l].chromaW[refs[l]][c-1], h.pw[l].chromaO[refs[l]][c-1]
				}
			}
		case 2:
			w1 := int(d.implicitW[refs[0]][refs[1]])
			pl.wt = [2]int{64 - w1, w1}
			pl.logWD = 5
		}
		for y := 0; y < pl.h; y++ {
			dst := pl.dst[pl.off+y*pl.ds : pl.off+y*pl.ds+pl.w]
			switch {
			case nUse == 2 && mode == 0:
				a := d.pred[0][pl.src+y*pl.ss:]
				b := d.pred[1][pl.src+y*pl.ss:]
				for x := range dst {
					dst[x] = byte((int(a[x]) + int(b[x]) + 1) >> 1)
				}
			case nUse == 2:
				a := d.pred[0][pl.src+y*pl.ss:]
				b := d.pred[1][pl.src+y*pl.ss:]
				w0, w1 := pl.wt[0], pl.wt[1]
				lw := pl.logWD
				o := (pl.ofs[0] + pl.ofs[1] + 1) >> 1
				for x := range dst {
					v := ((int(a[x])*w0 + int(b[x])*w1 + (1 << uint(lw))) >> uint(lw+1)) + o
					dst[x] = clip255(int32(v))
				}
			case mode == 1:
				l := 0
				if !used[0] {
					l = 1
				}
				a := d.pred[l][pl.src+y*pl.ss:]
				w0, o := pl.wt[l], pl.ofs[l]
				lw := pl.logWD
				for x := range dst {
					var v int
					if lw >= 1 {
						v = ((int(a[x])*w0 + (1 << uint(lw-1))) >> uint(lw)) + o
					} else {
						v = int(a[x])*w0 + o
					}
					dst[x] = clip255(int32(v))
				}
			default:
				l := 0
				if !used[0] {
					l = 1
				}
				copy(dst, d.pred[l][pl.src+y*pl.ss:pl.src+y*pl.ss+pl.w])
			}
		}
	}
}

// addInterResidual adds the residual of an inter macroblock.
func (d *Decoder) addInterResidual(cbp int, t8 bool) {
	pic := d.cur
	if cbp&15 != 0 {
		if t8 {
			for b8 := 0; b8 < 4; b8++ {
				if d.blkNZ[b8*4] {
					off := d.lumaOff((b8&1)*2, (b8>>1)*2)
					idct8Add(pic.y[off:], pic.ys, d.coef[b8*64:b8*64+64])
				}
			}
		} else {
			for blk := 0; blk < 16; blk++ {
				if d.blkNZ[blk] {
					idct4Add(pic.y[d.lumaOff(blkX[blk], blkY[blk]):], pic.ys, d.coef[blk*16:blk*16+16])
				}
			}
		}
	}
	d.addChromaResidual()
}

func (d *Decoder) addChromaResidual() {
	pic := d.cur
	for c := 0; c < 2; c++ {
		pl := pic.cb
		if c == 1 {
			pl = pic.cr
		}
		for b := 0; b < 4; b++ {
			if d.blkNZ[16+c*4+b] {
				off := d.chromaOff(b&1, b>>1)
				idct4Add(pl[off:], pic.cs, d.coef[256+c*64+b*16:256+c*64+b*16+16])
			}
		}
	}
}

func (d *Decoder) intraAvail(nbIdx int, avail bool) bool {
	if !avail {
		return false
	}
	if d.hdr.pps.ConstrainedIntra && !isIntraMB(d.mbType[nbIdx]) {
		return false
	}
	return true
}

func (d *Decoder) reconIntra(mbt uint8, i16Mode, chromaMode, cbp int, t8 bool) {
	pic := d.cur
	idx := d.mbIdx
	aA := d.intraAvail(idx-1, d.availA)
	aB := d.intraAvail(idx-d.mbW, d.availB)
	aC := d.intraAvail(idx-d.mbW+1, d.availC)
	aD := d.intraAvail(idx-d.mbW-1, d.availD)
	switch mbt {
	case mtI16:
		off := d.lumaOff(0, 0)
		pred16x16(pic.y, off, pic.ys, i16Mode, aA, aB)
		for blk := 0; blk < 16; blk++ {
			if d.blkNZ[blk] {
				idct4Add(pic.y[d.lumaOff(blkX[blk], blkY[blk]):], pic.ys, d.coef[blk*16:blk*16+16])
			}
		}
	case mtI4x4:
		if t8 {
			for b8 := 0; b8 < 4; b8++ {
				bx, by := (b8&1)*2, (b8>>1)*2
				left := bx > 0 || aA
				top := by > 0 || aB
				var tl, tr bool
				switch {
				case bx > 0 && by > 0:
					tl = true
				case bx > 0:
					tl = aB
				case by > 0:
					tl = aA
				default:
					tl = aD
				}
				switch b8 {
				case 0:
					tr = aB
				case 1:
					tr = aC
				case 2:
					tr = true
				}
				off := d.lumaOff(bx, by)
				pred8x8(pic.y, off, pic.ys, int(d.ipm[idx*16+by*4+bx]), left, top, tl, tr)
				if d.blkNZ[b8*4] {
					idct8Add(pic.y[off:], pic.ys, d.coef[b8*64:b8*64+64])
				}
			}
		} else {
			for blk := 0; blk < 16; blk++ {
				bx, by := blkX[blk], blkY[blk]
				left := bx > 0 || aA
				top := by > 0 || aB
				var tl, tr bool
				switch {
				case bx > 0 && by > 0:
					tl = true
				case bx > 0:
					tl = aB
				case by > 0:
					tl = aA
				default:
					tl = aD
				}
				switch {
				case by == 0 && bx < 3:
					tr = aB
				case by == 0:
					tr = aC
				case bx == 3:
					tr = false
				default:
					tr = posToBlk[(by-1)*4+bx+1] < blk
				}
				off := d.lumaOff(bx, by)
				pred4x4(pic.y, off, pic.ys, int(d.ipm[idx*16+by*4+bx]), left, top, tl, tr)
				if d.blkNZ[blk] {
					idct4Add(pic.y[off:], pic.ys, d.coef[blk*16:blk*16+16])
				}
			}
		}
	}
	co := d.chromaOff(0, 0)
	predChroma(pic.cb, co, pic.cs, chromaMode, aA, aB)
	predChroma(pic.cr, co, pic.cs, chromaMode, aA, aB)
	d.addChromaResidual()
}
