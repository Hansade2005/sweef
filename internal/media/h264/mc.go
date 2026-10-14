package h264

// Inter sample interpolation (8.4.2.2). The kernels work on row slices of one length so the compiler removes
// the bounds checks from the inner loops, and every quarter-sample position has its own loop (no per-sample
// switch). lumaMCRef in mc_ref_test.go is the straightforward version they are tested against.

func tap6(a, b, c, d, e, f int32) int32 { return a - 5*b + 20*c + 20*d - 5*e + f }

// clipV clamps v to 0..maxv.
func clipV(v, maxv int32) int32 {
	if uint32(v) > uint32(maxv) {
		if v < 0 {
			return 0
		}
		return maxv
	}
	return v
}

func clipHalf(v, maxv int32) int32 { return clipV((v+16)>>5, maxv) }

// copyRows copies a w x h block row by row, with fixed-width row copies for the usual widths.
func copyRows(dst []uint16, dstride int, src []uint16, base, sstride, w, h int) {
	switch w {
	case 16:
		for j := 0; j < h; j++ {
			copyRow16(dst[j*dstride:], src[base+j*sstride:])
		}
	case 8:
		for j := 0; j < h; j++ {
			copyRow8(dst[j*dstride:], src[base+j*sstride:])
		}
	case 4:
		for j := 0; j < h; j++ {
			copyRow4(dst[j*dstride:], src[base+j*sstride:])
		}
	default:
		for j := 0; j < h; j++ {
			copy(dst[j*dstride:j*dstride+w], src[base+j*sstride:base+j*sstride+w])
		}
	}
}

// emulateEdge copies the (w x h) block at (x, y) of the plane, replicating the edge samples where the block
// leaves the picture, into tmp (stride w).
func emulateEdge(tmp []uint16, plane []uint16, stride, pw, ph, x, y, w, h int) {
	for j := 0; j < h; j++ {
		yy := y + j
		if yy < 0 {
			yy = 0
		} else if yy >= ph {
			yy = ph - 1
		}
		row := plane[yy*stride : yy*stride+pw]
		t := tmp[j*w : j*w+w]
		for i := range t {
			xx := x + i
			if xx < 0 {
				xx = 0
			} else if xx >= pw {
				xx = pw - 1
			}
			t[i] = row[xx]
		}
	}
}

// lumaMC predicts a w x h block from plane (stride, width pw, height ph) at
// integer position (x, y) with quarter-sample fraction (fx, fy) into dst.
func (d *Decoder) lumaMC(dst []uint16, dstride int, plane []uint16, stride, pw, ph, x, y, fx, fy, w, h int, maxv int32) {
	src := plane
	sstride := stride
	base := y*stride + x
	if x < 2 || y < 2 || x+w+3 > pw || y+h+3 > ph {
		ww := w + 5
		tmp := d.tmpMC[:ww*(h+5)]
		emulateEdge(tmp, plane, stride, pw, ph, x-2, y-2, ww, h+5)
		src, sstride, base = tmp, ww, 2*ww+2
	}
	switch {
	case fx == 0 && fy == 0:
		copyRows(dst, dstride, src, base, sstride, w, h)
	case fy == 0:
		lumaQpelH(dst, dstride, src, base, sstride, w, h, fx, maxv)
	case fx == 0:
		lumaQpelV(dst, dstride, src, base, sstride, w, h, fy, maxv)
	case fx != 2 && fy != 2:
		lumaQpelDiag(dst, dstride, src, base, sstride, w, h, fx, fy, maxv)
	default:
		lumaQpelCentre(dst, dstride, src, base, sstride, w, h, fx, fy, maxv, d.tmpI[:(h+5)*w])
	}
}

// lumaQpelH: positions a, b, c (horizontal half sample, optionally averaged with the integer sample).
func lumaQpelH(dst []uint16, dstride int, src []uint16, base, sstride, w, h, fx int, maxv int32) {
	for j := 0; j < h; j++ {
		o := dst[j*dstride : j*dstride+w]
		s := src[base+j*sstride-2:]
		s0, s1, s2, s3, s4, s5 := s[:len(o)], s[1:][:len(o)], s[2:][:len(o)], s[3:][:len(o)], s[4:][:len(o)], s[5:][:len(o)]
		switch fx {
		case 1:
			for i := range o {
				t := 20*(int32(s2[i])+int32(s3[i])) - 5*(int32(s1[i])+int32(s4[i])) + int32(s0[i]) + int32(s5[i])
				o[i] = uint16((int32(s2[i]) + clipHalf(t, maxv) + 1) >> 1)
			}
		case 2:
			for i := range o {
				t := 20*(int32(s2[i])+int32(s3[i])) - 5*(int32(s1[i])+int32(s4[i])) + int32(s0[i]) + int32(s5[i])
				o[i] = uint16(clipHalf(t, maxv))
			}
		default:
			for i := range o {
				t := 20*(int32(s2[i])+int32(s3[i])) - 5*(int32(s1[i])+int32(s4[i])) + int32(s0[i]) + int32(s5[i])
				o[i] = uint16((int32(s3[i]) + clipHalf(t, maxv) + 1) >> 1)
			}
		}
	}
}

// lumaQpelV: positions d, h, n.
func lumaQpelV(dst []uint16, dstride int, src []uint16, base, sstride, w, h, fy int, maxv int32) {
	for j := 0; j < h; j++ {
		o := dst[j*dstride : j*dstride+w]
		p := base + j*sstride
		r0 := src[p-2*sstride:][:len(o)]
		r1 := src[p-sstride:][:len(o)]
		r2 := src[p:][:len(o)]
		r3 := src[p+sstride:][:len(o)]
		r4 := src[p+2*sstride:][:len(o)]
		r5 := src[p+3*sstride:][:len(o)]
		switch fy {
		case 1:
			for i := range o {
				t := 20*(int32(r2[i])+int32(r3[i])) - 5*(int32(r1[i])+int32(r4[i])) + int32(r0[i]) + int32(r5[i])
				o[i] = uint16((int32(r2[i]) + clipHalf(t, maxv) + 1) >> 1)
			}
		case 2:
			for i := range o {
				t := 20*(int32(r2[i])+int32(r3[i])) - 5*(int32(r1[i])+int32(r4[i])) + int32(r0[i]) + int32(r5[i])
				o[i] = uint16(clipHalf(t, maxv))
			}
		default:
			for i := range o {
				t := 20*(int32(r2[i])+int32(r3[i])) - 5*(int32(r1[i])+int32(r4[i])) + int32(r0[i]) + int32(r5[i])
				o[i] = uint16((int32(r3[i]) + clipHalf(t, maxv) + 1) >> 1)
			}
		}
	}
}

// lumaQpelDiag: positions e, g, p, r (the average of a horizontal and a vertical half sample).
func lumaQpelDiag(dst []uint16, dstride int, src []uint16, base, sstride, w, h, fx, fy int, maxv int32) {
	hOff := (fy >> 1) * sstride // row of the horizontal half sample
	vOff := fx >> 1             // column of the vertical half sample
	for j := 0; j < h; j++ {
		o := dst[j*dstride : j*dstride+w]
		p := base + j*sstride
		s := src[p+hOff-2:]
		s0, s1, s2, s3, s4, s5 := s[:len(o)], s[1:][:len(o)], s[2:][:len(o)], s[3:][:len(o)], s[4:][:len(o)], s[5:][:len(o)]
		pv := p + vOff
		r0 := src[pv-2*sstride:][:len(o)]
		r1 := src[pv-sstride:][:len(o)]
		r2 := src[pv:][:len(o)]
		r3 := src[pv+sstride:][:len(o)]
		r4 := src[pv+2*sstride:][:len(o)]
		r5 := src[pv+3*sstride:][:len(o)]
		for i := range o {
			th := 20*(int32(s2[i])+int32(s3[i])) - 5*(int32(s1[i])+int32(s4[i])) + int32(s0[i]) + int32(s5[i])
			tv := 20*(int32(r2[i])+int32(r3[i])) - 5*(int32(r1[i])+int32(r4[i])) + int32(r0[i]) + int32(r5[i])
			o[i] = uint16((clipHalf(th, maxv) + clipHalf(tv, maxv) + 1) >> 1)
		}
	}
}

// lumaQpelCentre: position j and the quarter positions that average with it (f, i, k, q).
func lumaQpelCentre(dst []uint16, dstride int, src []uint16, base, sstride, w, h, fx, fy int, maxv int32, tmp []int32) {
	for r := 0; r < h+5; r++ {
		s := src[base+(r-2)*sstride-2:]
		t := tmp[r*w : r*w+w]
		s0, s1, s2, s3, s4, s5 := s[:len(t)], s[1:][:len(t)], s[2:][:len(t)], s[3:][:len(t)], s[4:][:len(t)], s[5:][:len(t)]
		for i := range t {
			t[i] = 20*(int32(s2[i])+int32(s3[i])) - 5*(int32(s1[i])+int32(s4[i])) + int32(s0[i]) + int32(s5[i])
		}
	}
	for j := 0; j < h; j++ {
		o := dst[j*dstride : j*dstride+w]
		t0 := tmp[j*w:][:len(o)]
		t1 := tmp[(j+1)*w:][:len(o)]
		t2 := tmp[(j+2)*w:][:len(o)]
		t3 := tmp[(j+3)*w:][:len(o)]
		t4 := tmp[(j+4)*w:][:len(o)]
		t5 := tmp[(j+5)*w:][:len(o)]
		switch {
		case fx == 2 && fy == 2:
			for i := range o {
				c := (20*(t2[i]+t3[i]) - 5*(t1[i]+t4[i]) + t0[i] + t5[i] + 512) >> 10
				o[i] = uint16(clipV(c, maxv))
			}
		case fy == 2: // i, k: with the vertical half sample at column x or x+1
			p := base + j*sstride + (fx >> 1)
			r0 := src[p-2*sstride:][:len(o)]
			r1 := src[p-sstride:][:len(o)]
			r2 := src[p:][:len(o)]
			r3 := src[p+sstride:][:len(o)]
			r4 := src[p+2*sstride:][:len(o)]
			r5 := src[p+3*sstride:][:len(o)]
			for i := range o {
				c := clipV((20*(t2[i]+t3[i])-5*(t1[i]+t4[i])+t0[i]+t5[i]+512)>>10, maxv)
				tv := 20*(int32(r2[i])+int32(r3[i])) - 5*(int32(r1[i])+int32(r4[i])) + int32(r0[i]) + int32(r5[i])
				o[i] = uint16((clipHalf(tv, maxv) + c + 1) >> 1)
			}
		case fy == 1: // f
			for i := range o {
				c := clipV((20*(t2[i]+t3[i])-5*(t1[i]+t4[i])+t0[i]+t5[i]+512)>>10, maxv)
				o[i] = uint16((clipHalf(t2[i], maxv) + c + 1) >> 1)
			}
		default: // q
			for i := range o {
				c := clipV((20*(t2[i]+t3[i])-5*(t1[i]+t4[i])+t0[i]+t5[i]+512)>>10, maxv)
				o[i] = uint16((clipHalf(t3[i], maxv) + c + 1) >> 1)
			}
		}
	}
}

// chromaMC predicts a w x h chroma block with eighth-sample fractions.
func (d *Decoder) chromaMC(dst []uint16, dstride int, plane []uint16, stride, pw, ph, x, y, fx, fy, w, h int) {
	src := plane
	sstride := stride
	base := y*stride + x
	if x < 0 || y < 0 || x+w+1 > pw || y+h+1 > ph {
		ww := w + 1
		tmp := d.tmpMC[:ww*(h+1)]
		emulateEdge(tmp, plane, stride, pw, ph, x, y, ww, h+1)
		src, sstride, base = tmp, ww, 0
	}
	switch {
	case fx == 0 && fy == 0:
		copyRows(dst, dstride, src, base, sstride, w, h)
	case fy == 0:
		a, b := int32(8-fx), int32(fx)
		for j := 0; j < h; j++ {
			o := dst[j*dstride : j*dstride+w]
			s := src[base+j*sstride:]
			s0, s1 := s[:len(o)], s[1:][:len(o)]
			for i := range o {
				o[i] = uint16((a*int32(s0[i]) + b*int32(s1[i]) + 4) >> 3)
			}
		}
	case fx == 0:
		a, b := int32(8-fy), int32(fy)
		for j := 0; j < h; j++ {
			o := dst[j*dstride : j*dstride+w]
			r0 := src[base+j*sstride:][:len(o)]
			r1 := src[base+(j+1)*sstride:][:len(o)]
			for i := range o {
				o[i] = uint16((a*int32(r0[i]) + b*int32(r1[i]) + 4) >> 3)
			}
		}
	default:
		w00 := int32((8 - fx) * (8 - fy))
		w01 := int32(fx * (8 - fy))
		w10 := int32((8 - fx) * fy)
		w11 := int32(fx * fy)
		for j := 0; j < h; j++ {
			o := dst[j*dstride : j*dstride+w]
			ra := src[base+j*sstride:]
			rb := src[base+(j+1)*sstride:]
			a0, a1 := ra[:len(o)], ra[1:][:len(o)]
			b0, b1 := rb[:len(o)], rb[1:][:len(o)]
			for i := range o {
				o[i] = uint16((w00*int32(a0[i]) + w01*int32(a1[i]) + w10*int32(b0[i]) + w11*int32(b1[i]) + 32) >> 6)
			}
		}
	}
}
