package hevc

// Motion compensation: fractional sample interpolation and weighted
// sample prediction.

// refBlock returns a view of the (w+before+after) x (h+before+after) block of
// plane whose top-left sample (before the margin) is (x0-before, y0-before),
// replicating edge samples when it leaves the picture.
func refBlock(plane []uint16, stride, pw, ph, x0, y0, w, h, before, after int, buf []uint16) ([]uint16, int) {
	bw, bh := w+before+after, h+before+after
	xs, ys := x0-before, y0-before
	if xs >= 0 && ys >= 0 && xs+bw <= pw && ys+bh <= ph {
		return plane[ys*stride+xs:], stride
	}
	for j := 0; j < bh; j++ {
		y := ys + j
		if y < 0 {
			y = 0
		} else if y >= ph {
			y = ph - 1
		}
		src := plane[y*stride : y*stride+pw]
		dst := buf[j*bw : j*bw+bw]
		for i := 0; i < bw; i++ {
			x := xs + i
			if x < 0 {
				x = 0
			} else if x >= pw {
				x = pw - 1
			}
			dst[i] = src[x]
		}
	}
	return buf, bw
}

// mcLuma writes the 14-bit intermediate prediction (stride w) of a luma block.
func mcLuma[T int16 | int32](sc *scratch, mcTmp []T, dst []T, ref *Picture, x0, y0, fx, fy, w, h, bd int) {
	src, ss := refBlock(ref.planes[0], ref.stride[0], ref.w, ref.h, x0, y0, w, h, 3, 4, sc.edgeBf[:])
	// src points at (x0-3, y0-3)
	shift1 := uint(bd - 8)
	if shift1 > 4 {
		shift1 = 4
	}
	shift3 := uint(max(2, 14-bd))
	switch {
	case fx == 0 && fy == 0:
		for y := 0; y < h; y++ {
			r := src[(y+3)*ss+3:]
			o := dst[y*w : y*w+w]
			for x := range o {
				o[x] = T(r[x]) << shift3
			}
		}
	case fy == 0:
		c := &lumaFilter[fx]
		for y := 0; y < h; y++ {
			r := src[(y+3)*ss:]
			o := dst[y*w : y*w+w]
			for x := range o {
				v := c[0]*int32(r[x]) + c[1]*int32(r[x+1]) + c[2]*int32(r[x+2]) + c[3]*int32(r[x+3]) +
					c[4]*int32(r[x+4]) + c[5]*int32(r[x+5]) + c[6]*int32(r[x+6]) + c[7]*int32(r[x+7])
				o[x] = T(v >> shift1)
			}
		}
	case fx == 0:
		c := &lumaFilter[fy]
		for y := 0; y < h; y++ {
			o := dst[y*w : y*w+w]
			for x := range o {
				p := src[y*ss+x+3:]
				v := c[0]*int32(p[0]) + c[1]*int32(p[ss]) + c[2]*int32(p[2*ss]) + c[3]*int32(p[3*ss]) +
					c[4]*int32(p[4*ss]) + c[5]*int32(p[5*ss]) + c[6]*int32(p[6*ss]) + c[7]*int32(p[7*ss])
				o[x] = T(v >> shift1)
			}
		}
	default:
		ch := &lumaFilter[fx]
		cv := &lumaFilter[fy]
		tmp := mcTmp[:(h+7)*w]
		for y := 0; y < h+7; y++ {
			r := src[y*ss:]
			o := tmp[y*w : y*w+w]
			for x := range o {
				v := ch[0]*int32(r[x]) + ch[1]*int32(r[x+1]) + ch[2]*int32(r[x+2]) + ch[3]*int32(r[x+3]) +
					ch[4]*int32(r[x+4]) + ch[5]*int32(r[x+5]) + ch[6]*int32(r[x+6]) + ch[7]*int32(r[x+7])
				o[x] = T(v >> shift1)
			}
		}
		for y := 0; y < h; y++ {
			o := dst[y*w : y*w+w]
			for x := range o {
				p := tmp[y*w+x:]
				v := cv[0]*int32(p[0]) + cv[1]*int32(p[w]) + cv[2]*int32(p[2*w]) + cv[3]*int32(p[3*w]) +
					cv[4]*int32(p[4*w]) + cv[5]*int32(p[5*w]) + cv[6]*int32(p[6*w]) + cv[7]*int32(p[7*w])
				o[x] = T(v >> 6)
			}
		}
	}
}

// mcChroma writes the intermediate prediction of a chroma block; fx, fy are
// in 1/8 sample units.
func mcChroma[T int16 | int32](sc *scratch, mcTmp []T, dst []T, ref *Picture, ci, x0, y0, fx, fy, w, h, bd int) {
	src, ss := refBlock(ref.planes[ci], ref.stride[ci], ref.cw, ref.ch, x0, y0, w, h, 1, 2, sc.edgeBf[:])
	shift1 := uint(bd - 8)
	if shift1 > 4 {
		shift1 = 4
	}
	shift3 := uint(max(2, 14-bd))
	switch {
	case fx == 0 && fy == 0:
		for y := 0; y < h; y++ {
			r := src[(y+1)*ss+1:]
			o := dst[y*w : y*w+w]
			for x := range o {
				o[x] = T(r[x]) << shift3
			}
		}
	case fy == 0:
		c := &chromaFilter[fx]
		for y := 0; y < h; y++ {
			r := src[(y+1)*ss:]
			o := dst[y*w : y*w+w]
			for x := range o {
				v := c[0]*int32(r[x]) + c[1]*int32(r[x+1]) + c[2]*int32(r[x+2]) + c[3]*int32(r[x+3])
				o[x] = T(v >> shift1)
			}
		}
	case fx == 0:
		c := &chromaFilter[fy]
		for y := 0; y < h; y++ {
			o := dst[y*w : y*w+w]
			for x := range o {
				p := src[y*ss+x+1:]
				v := c[0]*int32(p[0]) + c[1]*int32(p[ss]) + c[2]*int32(p[2*ss]) + c[3]*int32(p[3*ss])
				o[x] = T(v >> shift1)
			}
		}
	default:
		ch := &chromaFilter[fx]
		cv := &chromaFilter[fy]
		tmp := mcTmp[:(h+3)*w]
		for y := 0; y < h+3; y++ {
			r := src[y*ss:]
			o := tmp[y*w : y*w+w]
			for x := range o {
				v := ch[0]*int32(r[x]) + ch[1]*int32(r[x+1]) + ch[2]*int32(r[x+2]) + ch[3]*int32(r[x+3])
				o[x] = T(v >> shift1)
			}
		}
		for y := 0; y < h; y++ {
			o := dst[y*w : y*w+w]
			for x := range o {
				p := tmp[y*w+x:]
				v := cv[0]*int32(p[0]) + cv[1]*int32(p[w]) + cv[2]*int32(p[2*w]) + cv[3]*int32(p[3*w])
				o[x] = T(v >> 6)
			}
		}
	}
}

// predictInter builds the inter prediction of one prediction block into the
// current picture.
func (d *Decoder) predictInter(xPb, yPb, nPbW, nPbH int, mf *mvField) {
	if s := d.sp; s.bitDepthY > 12 || s.bitDepthC > 12 {
		// the 14 bit intermediate samples of the interpolation no longer fit
		// 16 bits
		predictInterT(d, &d.scratch.predHi, &d.scratch.mcTmpHi, xPb, yPb, nPbW, nPbH, mf)
		return
	}
	predictInterT(d, &d.scratch.pred, &d.scratch.mcTmp, xPb, yPb, nPbW, nPbH, mf)
}

func predictInterT[T int16 | int32](d *Decoder, pred *[2][64 * 64]T, tmp *[(64 + 7) * 64]T, xPb, yPb, nPbW, nPbH int, mf *mvField) {
	s, sh, p := d.sp, d.sh, d.pp
	sc := &d.scratch
	pic := d.cur
	weighted := (sh.sliceType == sliceP && p.weightedPred) || (sh.sliceType == sliceB && p.weightedBi)
	nc := 3
	if s.chromaArrayType == 0 {
		nc = 1
	}
	for ci := 0; ci < nc; ci++ {
		var w, h, x0, y0 int
		bd := s.bitDepthY
		var sx, sy uint
		if ci == 0 {
			w, h, x0, y0 = nPbW, nPbH, xPb, yPb
		} else {
			sx, sy = s.subW, s.subH
			w, h, x0, y0 = nPbW>>sx, nPbH>>sy, xPb>>sx, yPb>>sy
			bd = s.bitDepthC
		}
		for l := 0; l < 2; l++ {
			if mf.pf&(1<<uint(l)) == 0 {
				continue
			}
			ref := sh.refPic[l][mf.ref[l]]
			mvx, mvy := int(mf.mv[l][0]), int(mf.mv[l][1])
			if ci == 0 {
				mcLuma(sc, tmp[:], pred[l][:w*h], ref, x0+(mvx>>2), y0+(mvy>>2), mvx&3, mvy&3, w, h, bd)
			} else {
				fx := (mvx & ((4 << sx) - 1)) << (1 - sx)
				fy := (mvy & ((4 << sy) - 1)) << (1 - sy)
				mcChroma(sc, tmp[:], pred[l][:w*h], ref, ci, x0+(mvx>>(2+sx)), y0+(mvy>>(2+sy)), fx, fy, w, h, bd)
			}
		}
		stride := pic.stride[ci]
		dst := pic.planes[ci][y0*stride+x0:]
		maxV := int32(1)<<uint(bd) - 1
		shift1 := uint(max(2, 14-bd))
		if !weighted {
			if mf.pf == 3 {
				shift := shift1 + 1
				off := int32(1) << (shift - 1)
				p0, p1 := pred[0][:w*h], pred[1][:w*h]
				for y := 0; y < h; y++ {
					row := dst[y*stride : y*stride+w]
					a, b := p0[y*w:y*w+w], p1[y*w:y*w+w]
					for x := range row {
						v := (int32(a[x]) + int32(b[x]) + off) >> shift
						if v < 0 {
							v = 0
						} else if v > maxV {
							v = maxV
						}
						row[x] = uint16(v)
					}
				}
			} else {
				l := 0
				if mf.pf == 2 {
					l = 1
				}
				var off int32
				if shift1 > 0 {
					off = 1 << (shift1 - 1)
				}
				pr := pred[l][:w*h]
				for y := 0; y < h; y++ {
					row := dst[y*stride : y*stride+w]
					a := pr[y*w : y*w+w]
					for x := range row {
						v := (int32(a[x]) + off) >> shift1
						if v < 0 {
							v = 0
						} else if v > maxV {
							v = maxV
						}
						row[x] = uint16(v)
					}
				}
			}
			continue
		}
		// explicit weighted prediction
		pw := sh.pw
		var wt, of [2]int32
		var log2WD uint
		oshift := uint(bd - 8)
		if s.highPrecOffsets {
			oshift = 0
		}
		for l := 0; l < 2; l++ {
			if mf.pf&(1<<uint(l)) == 0 {
				continue
			}
			r := mf.ref[l]
			if ci == 0 {
				wt[l] = int32(pw.lumaW[l][r])
				of[l] = int32(pw.lumaO[l][r]) << oshift
			} else {
				wt[l] = int32(pw.chromaW[l][r][ci-1])
				of[l] = int32(pw.chromaO[l][r][ci-1]) << oshift
			}
		}
		if ci == 0 {
			log2WD = uint(pw.lumaLog2Denom) + shift1
		} else {
			log2WD = uint(pw.chromaLog2Denom) + shift1
		}
		if mf.pf == 3 {
			p0, p1 := pred[0][:w*h], pred[1][:w*h]
			o := (of[0] + of[1] + 1) << log2WD
			for y := 0; y < h; y++ {
				row := dst[y*stride : y*stride+w]
				a, b := p0[y*w:y*w+w], p1[y*w:y*w+w]
				for x := range row {
					v := (int32(a[x])*wt[0] + int32(b[x])*wt[1] + o) >> (log2WD + 1)
					if v < 0 {
						v = 0
					} else if v > maxV {
						v = maxV
					}
					row[x] = uint16(v)
				}
			}
		} else {
			l := 0
			if mf.pf == 2 {
				l = 1
			}
			pr := pred[l][:w*h]
			for y := 0; y < h; y++ {
				row := dst[y*stride : y*stride+w]
				a := pr[y*w : y*w+w]
				for x := range row {
					var v int32
					if log2WD >= 1 {
						v = ((int32(a[x])*wt[l] + 1<<(log2WD-1)) >> log2WD) + of[l]
					} else {
						v = int32(a[x])*wt[l] + of[l]
					}
					if v < 0 {
						v = 0
					} else if v > maxV {
						v = maxV
					}
					row[x] = uint16(v)
				}
			}
		}
	}
}
