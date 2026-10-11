package h264

// Macroblock reconstruction: inter prediction, intra prediction and
// residual addition.

// planeOf returns the sample array and frame stride of colour plane p.
func (d *Decoder) planeOf(p int) (pl []uint16, stride int) {
	switch p {
	case 0:
		return d.cur.y, d.cur.ys
	case 1:
		return d.cur.cb, d.cur.cs
	}
	return d.cur.cr, d.cur.cs
}

// mbDims returns the width and height in samples of the macroblock in plane p.
func (d *Decoder) mbDims(p int) (w, h int) {
	if p == 0 || d.f.cat == 3 {
		return 16, 16
	}
	return d.f.mbWC, d.f.mbHC
}

// setMBGeometry computes the offset and stride of the current macroblock in
// every plane: frame macroblocks use the frame stride, field macroblocks (of
// field pictures and MBAFF frames) address one field of the frame.
func (d *Decoder) setMBGeometry() {
	for p := 0; p < 3; p++ {
		w, h := d.mbDims(p)
		_, stride := d.planeOf(p)
		if d.mbField {
			d.mbOff[p] = ((d.mby>>1)*2*h+(d.mby&1))*stride + d.mbx*w
			d.mbStr[p] = stride * 2
		} else {
			d.mbOff[p] = d.mby*h*stride + d.mbx*w
			d.mbStr[p] = stride
		}
	}
}

// blockOff returns the offset of the 4x4 block (bx, by) of plane p in the
// current macroblock.
func (d *Decoder) blockOff(p, bx, by int) int {
	return d.mbOff[p] + by*4*d.mbStr[p] + bx*4
}

// refPlaneView returns the samples of plane p of reference e as one frame
// or field: the slice starts at the first row, with the given stride and
// size.
func refPlaneView(e *refEntry, p int) (pl []uint16, stride, w, h int) {
	pic := e.pic
	var base []uint16
	switch p {
	case 0:
		base, stride, w, h = pic.y, pic.ys, pic.w, pic.h
	case 1:
		base, stride, w, h = pic.cb, pic.cs, pic.cw, pic.ch
	default:
		base, stride, w, h = pic.cr, pic.cs, pic.cw, pic.ch
	}
	if e.ref == picFrame {
		return base, stride, w, h
	}
	off := 0
	if e.ref == picBottom {
		off = stride
	}
	return base[off:], stride * 2, w, h / 2
}

// refFor returns the reference entry for reference index r of list l as seen
// by the current macroblock.
func (d *Decoder) refFor(l, r int) *refEntry {
	if d.mbMbaff() {
		i := r ^ (d.mby & 1)
		if i < len(d.refListField[l]) {
			return &d.refListField[l][i]
		}
		return nil
	}
	if r < len(d.refList[l]) {
		return &d.refList[l][r]
	}
	return nil
}

// mcRect performs motion compensated prediction of one rectangle (4x4 block
// units, macroblock relative) into the current picture, reading the motion
// from the caches.
func (d *Decoder) mcRect(r rect) {
	h := d.hdr
	ci := c0 + r.y*8 + r.x
	refs := [2]int{int(d.refCache[0][ci]), int(d.refCache[1][ci])}
	for l := 0; l < 2; l++ {
		if !usesList(d.mbTypeCur, l) {
			refs[l] = -1
		}
	}
	mvs := [2]mv16{d.mvCache[0][ci], d.mvCache[1][ci]}
	// position in the macroblock's own (frame or field) plane coordinates
	mbY0 := d.mby * 16
	if d.mbField {
		mbY0 = (d.mby >> 1) * 16
	}
	px, py := d.mbx*16+r.x*4, mbY0+r.y*4
	bw, bh := r.w*4, r.h*4
	nUse := 0
	var used [2]bool
	var ents [2]*refEntry
	cat := d.f.cat
	for l := 0; l < 2; l++ {
		if refs[l] < 0 {
			continue
		}
		ent := d.refFor(l, refs[l])
		if ent == nil || ent.pic == nil {
			continue
		}
		ents[l] = ent
		used[l] = true
		nUse++
		m := mvs[l]
		out := d.pred[l][:]
		pl, stride, pw, ph := refPlaneView(ent, 0)
		d.lumaMC(out, 16, pl, stride, pw, ph, px+int(m.x>>2), py+int(m.y>>2), int(m.x&3), int(m.y&3), bw, bh, d.f.maxY)
		switch cat {
		case 1:
			my := int(m.y)
			if d.mbField {
				my += 2 * ((d.mby & 1) - (ent.ref - 1))
			}
			cx, cy := px/2+int(m.x>>3), py/2+(my>>3)
			fx, fy := int(m.x&7), my&7
			for c := 1; c <= 2; c++ {
				cpl, cs, cw, ch := refPlaneView(ent, c)
				d.chromaMC(out[c*256:], 16, cpl, cs, cw, ch, cx, cy, fx, fy, bw/2, bh/2)
			}
		case 2:
			cx, cy := px/2+int(m.x>>3), py+int(m.y>>2)
			fx, fy := int(m.x&7), int(m.y&3)*2
			for c := 1; c <= 2; c++ {
				cpl, cs, cw, ch := refPlaneView(ent, c)
				d.chromaMC(out[c*256:], 16, cpl, cs, cw, ch, cx, cy, fx, fy, bw/2, bh)
			}
		case 3:
			for c := 1; c <= 2; c++ {
				cpl, cs, cw, ch := refPlaneView(ent, c)
				d.lumaMC(out[c*256:], 16, cpl, cs, cw, ch, px+int(m.x>>2), py+int(m.y>>2), int(m.x&3), int(m.y&3), bw, bh, d.f.maxC)
			}
		}
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
	nPl := 3
	if cat == 0 {
		nPl = 1
	}
	wref := refs
	if d.mbMbaff() {
		wref = [2]int{refs[0] >> 1, refs[1] >> 1}
	}
	for c := 0; c < nPl; c++ {
		dst, _ := d.planeOf(c)
		ds := d.mbStr[c]
		pw, ph := bw, bh
		off := d.mbOff[c] + r.y*4*ds + r.x*4
		maxv := d.f.maxY
		logWD := h.logWDLuma
		bd := d.f.bdY
		if c > 0 {
			maxv, bd = d.f.maxC, d.f.bdC
			logWD = h.logWDChroma
			switch cat {
			case 1:
				pw, ph = bw/2, bh/2
				off = d.mbOff[c] + r.y*2*ds + r.x*2
			case 2:
				pw = bw / 2
				off = d.mbOff[c] + r.y*4*ds + r.x*2
			}
		}
		var wt, ofs [2]int
		switch mode {
		case 1:
			for l := 0; l < 2; l++ {
				if !used[l] {
					continue
				}
				if c == 0 {
					wt[l], ofs[l] = h.pw[l].lumaW[wref[l]], h.pw[l].lumaO[wref[l]]
				} else {
					wt[l], ofs[l] = h.pw[l].chromaW[wref[l]][c-1], h.pw[l].chromaO[wref[l]][c-1]
				}
				ofs[l] <<= uint(bd - 8)
			}
		case 2:
			tab := 0
			if d.mbMbaff() {
				tab = 1 + d.mby&1
			}
			w1 := int(d.implicitW[tab][refs[0]][refs[1]])
			wt = [2]int{64 - w1, w1}
			logWD = 5
		}
		src := c * 256
		for y := 0; y < ph; y++ {
			dd := dst[off+y*ds : off+y*ds+pw]
			switch {
			case nUse == 2 && mode == 0:
				a := d.pred[0][src+y*16:]
				b := d.pred[1][src+y*16:]
				for x := range dd {
					dd[x] = uint16((int(a[x]) + int(b[x]) + 1) >> 1)
				}
			case nUse == 2:
				a := d.pred[0][src+y*16:]
				b := d.pred[1][src+y*16:]
				w0, w1 := wt[0], wt[1]
				o := (ofs[0] + ofs[1] + 1) >> 1
				for x := range dd {
					v := ((int(a[x])*w0 + int(b[x])*w1 + (1 << uint(logWD))) >> uint(logWD+1)) + o
					dd[x] = clipPix(int32(v), maxv)
				}
			case mode == 1:
				l := 0
				if !used[0] {
					l = 1
				}
				a := d.pred[l][src+y*16:]
				w0, o := wt[l], ofs[l]
				for x := range dd {
					var v int
					if logWD >= 1 {
						v = ((int(a[x])*w0 + (1 << uint(logWD-1))) >> uint(logWD)) + o
					} else {
						v = int(a[x])*w0 + o
					}
					dd[x] = clipPix(int32(v), maxv)
				}
			default:
				l := 0
				if !used[0] {
					l = 1
				}
				copy(dd, d.pred[l][src+y*16:src+y*16+pw])
			}
		}
	}
}

// addLumaResidual adds the residual of one luma-like plane (luma, or Cb/Cr
// of 4:4:4) to the prediction already in the picture for non-intra4x4/8x8
// blocks.
func (d *Decoder) addPlaneResidual(p int, t8 bool) {
	pl, _ := d.planeOf(p)
	stride := d.mbStr[p]
	maxv := d.f.maxY
	if p > 0 {
		maxv = d.f.maxC
	}
	base := p * 256
	if d.bypassMB {
		for blk := 0; blk < 16; blk++ {
			if d.blkNZ[p*16+blk] && (!t8 || blk&3 == 0) {
				d.bypassBlock(p, blk, t8, false, false)
			}
		}
		return
	}
	if t8 {
		for b8 := 0; b8 < 4; b8++ {
			if d.blkNZ[p*16+b8*4] {
				off := d.blockOff(p, (b8&1)*2, (b8>>1)*2)
				idct8Add(pl[off:], stride, d.coef[base+b8*64:base+b8*64+64], maxv)
			}
		}
		return
	}
	for blk := 0; blk < 16; blk++ {
		if d.blkNZ[p*16+blk] {
			idct4Add(pl[d.blockOff(p, blkX[blk], blkY[blk]):], stride, d.coef[base+blk*16:base+blk*16+16], maxv)
		}
	}
}

// bypassBlock adds the untransformed residual of 4x4 block blk (or of 8x8
// block blk/4 when t8) of luma-like plane p.
func (d *Decoder) bypassBlock(p, blk int, t8, hor, ver bool) {
	pl, _ := d.planeOf(p)
	stride := d.mbStr[p]
	maxv := d.f.maxY
	if p > 0 {
		maxv = d.f.maxC
	}
	base := p * 256
	if t8 {
		b8 := blk >> 2
		if blk&3 != 0 {
			return
		}
		off := d.blockOff(p, (b8&1)*2, (b8>>1)*2)
		bypassAdd(pl[off:], stride, d.coef[base+b8*64:base+b8*64+64], 8, 8, 8, maxv, hor, ver)
		return
	}
	off := d.blockOff(p, blkX[blk], blkY[blk])
	bypassAdd(pl[off:], stride, d.coef[base+blk*16:base+blk*16+16], 4, 4, 4, maxv, hor, ver)
}

// addChromaResidual adds the residual of the 4:2:0 / 4:2:2 chroma planes.
// dpcm is 0 for none, 1 horizontal, 2 vertical (lossless intra prediction).
func (d *Decoder) addChromaResidual(dpcm int) {
	if d.f.cat != 1 && d.f.cat != 2 {
		return
	}
	n := d.f.nBlkC
	for c := 1; c <= 2; c++ {
		pl, _ := d.planeOf(c)
		stride := d.mbStr[c]
		base := c * 256
		if d.bypassMB {
			w, h := d.f.mbWC, d.f.mbHC
			var res [8 * 16]int32
			any := false
			for b := 0; b < n; b++ {
				if !d.blkNZ[c*16+b] {
					continue
				}
				any = true
				bx, by := b&1, b>>1
				for y := 0; y < 4; y++ {
					for x := 0; x < 4; x++ {
						res[(by*4+y)*w+bx*4+x] = d.coef[base+b*16+y*4+x]
					}
				}
			}
			if any {
				off := d.blockOff(c, 0, 0)
				bypassAdd(pl[off:], stride, res[:], w, w, h, d.f.maxC, dpcm == 1, dpcm == 2)
			}
			continue
		}
		for b := 0; b < n; b++ {
			if d.blkNZ[c*16+b] {
				off := d.blockOff(c, b&1, b>>1)
				idct4Add(pl[off:], stride, d.coef[base+b*16:base+b*16+16], d.f.maxC)
			}
		}
	}
}

// addInterResidual adds the residual of an inter macroblock.
func (d *Decoder) addInterResidual(cbp int, t8 bool) {
	if cbp&15 != 0 {
		d.addPlaneResidual(0, t8)
		if d.f.cat == 3 {
			d.addPlaneResidual(1, t8)
			d.addPlaneResidual(2, t8)
		}
	}
	d.addChromaResidual(0)
}

// intraAvail reports whether neighbouring macroblock type t may be used for
// intra prediction.
func (d *Decoder) intraAvail(t uint32) bool {
	if t == 0 {
		return false
	}
	if d.hdr.pps.ConstrainedIntra && !isIntraT(t) {
		return false
	}
	return true
}

// leftRows reports, for each 4-row group of the macroblock, whether its left
// neighbour samples may be used for intra prediction. The groups differ only
// for MBAFF pairs of different field/frame mode with constrained intra
// prediction.
func (d *Decoder) leftRows() [4]bool {
	nb := &d.nb
	a0 := d.intraAvail(nb.leftType[0])
	if !d.mbaffFrame || !a0 && !d.hdr.pps.ConstrainedIntra {
		return [4]bool{a0, a0, a0, a0}
	}
	pic := d.curPic
	leftField := pic.mbType[d.mbIdx-1]&mbInterlaced != 0
	if leftField == d.mbField || nb.leftType[0] == 0 {
		return [4]bool{a0, a0, a0, a0}
	}
	if d.mbField {
		a1 := d.intraAvail(nb.leftType[1])
		return [4]bool{a0, a0, a1, a1}
	}
	// frame macroblock beside a field pair: rows alternate between both
	// macroblocks of the pair
	top := d.mbIdx - 1 - (d.mby&1)*d.mbStride
	a := d.intraAvail(pic.mbType[top]) && d.intraAvail(pic.mbType[top+d.mbStride])
	return [4]bool{a, a, a, a}
}

func (d *Decoder) reconIntra(mbt uint32, i16Mode, chromaMode, cbp int, t8 bool) {
	idx := d.mbIdx
	nb := &d.nb
	lrows := d.leftRows()
	aA := lrows[0] && lrows[1] && lrows[2] && lrows[3]
	aB := d.intraAvail(nb.topType)
	aC := d.intraAvail(nb.topRightType)
	aD := d.intraAvail(nb.topLeftType)
	nLuma := 1
	if d.f.cat == 3 {
		nLuma = 3
	}
	for p := 0; p < nLuma; p++ {
		pl, _ := d.planeOf(p)
		stride := d.mbStr[p]
		bd := d.f.bdY
		maxv := d.f.maxY
		if p > 0 {
			bd, maxv = d.f.bdC, d.f.maxC
		}
		base := p * 256
		switch {
		case mbt&mbIntra16x16 != 0:
			off := d.blockOff(p, 0, 0)
			pred16x16(pl, off, stride, i16Mode, aA, aB, bd)
			if d.bypassMB {
				var res [256]int32
				for blk := 0; blk < 16; blk++ {
					bx, by := blkX[blk], blkY[blk]
					for y := 0; y < 4; y++ {
						for x := 0; x < 4; x++ {
							res[(by*4+y)*16+bx*4+x] = d.coef[base+blk*16+y*4+x]
						}
					}
				}
				bypassAdd(pl[off:], stride, res[:], 16, 16, 16, maxv, i16Mode == 1, i16Mode == 0)
				continue
			}
			for blk := 0; blk < 16; blk++ {
				if d.blkNZ[p*16+blk] {
					idct4Add(pl[d.blockOff(p, blkX[blk], blkY[blk]):], stride, d.coef[base+blk*16:base+blk*16+16], maxv)
				}
			}
		case mbt&mbIntra4x4 != 0:
			if t8 {
				for b8 := 0; b8 < 4; b8++ {
					bx, by := (b8&1)*2, (b8>>1)*2
					left := bx > 0 || lrows[by]
					top := by > 0 || aB
					var tl, tr bool
					switch {
					case bx > 0 && by > 0:
						tl = true
					case bx > 0:
						tl = aB
					case by > 0:
						tl = lrows[by-1]
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
					off := d.blockOff(p, bx, by)
					mode := int(d.ipm[idx*16+by*4+bx])
					pred8x8(pl, off, stride, mode, left, top, tl, tr, bd)
					if d.bypassMB {
						bypassAdd(pl[off:], stride, d.coef[base+b8*64:base+b8*64+64], 8, 8, 8, maxv, mode == imH, mode == imV)
					} else if d.blkNZ[p*16+b8*4] {
						idct8Add(pl[off:], stride, d.coef[base+b8*64:base+b8*64+64], maxv)
					}
				}
			} else {
				for blk := 0; blk < 16; blk++ {
					bx, by := blkX[blk], blkY[blk]
					left := bx > 0 || lrows[by]
					top := by > 0 || aB
					var tl, tr bool
					switch {
					case bx > 0 && by > 0:
						tl = true
					case bx > 0:
						tl = aB
					case by > 0:
						tl = lrows[by-1]
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
					off := d.blockOff(p, bx, by)
					mode := int(d.ipm[idx*16+by*4+bx])
					pred4x4(pl, off, stride, mode, left, top, tl, tr, bd)
					if d.bypassMB {
						bypassAdd(pl[off:], stride, d.coef[base+blk*16:base+blk*16+16], 4, 4, 4, maxv, mode == imH, mode == imV)
					} else if d.blkNZ[p*16+blk] {
						idct4Add(pl[off:], stride, d.coef[base+blk*16:base+blk*16+16], maxv)
					}
				}
			}
		}
	}
	if d.f.cat == 1 || d.f.cat == 2 {
		cLeft := lrows
		if d.f.mbHC == 8 {
			cLeft = [4]bool{lrows[0], lrows[2], false, false}
		}
		for c := 1; c <= 2; c++ {
			pl, _ := d.planeOf(c)
			predChroma(pl, d.blockOff(c, 0, 0), d.mbStr[c], chromaMode, cLeft, aB, d.f.bdC, 8, d.f.mbHC)
		}
		dpcm := 0
		if chromaMode == 1 {
			dpcm = 1
		} else if chromaMode == 2 {
			dpcm = 2
		}
		d.addChromaResidual(dpcm)
	}
}
