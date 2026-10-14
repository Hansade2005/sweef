package h264

// Inter sample interpolation (8.4.2.2).

// lumaMC predicts a w x h block from plane (stride, width pw, height ph) at
// integer position (x, y) with quarter-sample fraction (fx, fy) into dst.
// tmp must hold (w+5)*(h+5) bytes and tmpI (w+5)*h int32 values.
func (d *Decoder) lumaMCRef(dst []uint16, dstride int, plane []uint16, stride, pw, ph, x, y, fx, fy, w, h int, maxv int32) {
	src := plane
	sstride := stride
	base := y*stride + x
	if x < 2 || y < 2 || x+w+3 > pw || y+h+3 > ph {
		ww := w + 5
		tmp := d.tmpMC[:ww*(h+5)]
		for j := 0; j < h+5; j++ {
			yy := y - 2 + j
			if yy < 0 {
				yy = 0
			} else if yy >= ph {
				yy = ph - 1
			}
			row := plane[yy*stride : yy*stride+pw]
			for i := 0; i < ww; i++ {
				xx := x - 2 + i
				if xx < 0 {
					xx = 0
				} else if xx >= pw {
					xx = pw - 1
				}
				tmp[j*ww+i] = row[xx]
			}
		}
		src, sstride, base = tmp, ww, 2*ww+2
	}
	if fx == 0 && fy == 0 {
		for j := 0; j < h; j++ {
			copy(dst[j*dstride:j*dstride+w], src[base+j*sstride:base+j*sstride+w])
		}
		return
	}
	maxi := maxv
	avg := func(a, b int32) uint16 { return uint16((a + b + 1) >> 1) }
	switch {
	case fy == 0: // a, b, c
		for j := 0; j < h; j++ {
			o := dst[j*dstride : j*dstride+w]
			p := base + j*sstride
			for i := range o {
				v := clipHalfRef(hb6Ref(src, p+i), maxi)
				switch fx {
				case 1:
					v = (int32(src[p+i]) + v + 1) >> 1
				case 3:
					v = (int32(src[p+i+1]) + v + 1) >> 1
				}
				o[i] = uint16(v)
			}
		}
	case fx == 0: // d, h, n
		for j := 0; j < h; j++ {
			o := dst[j*dstride : j*dstride+w]
			p := base + j*sstride
			for i := range o {
				v := clipHalfRef(vb6Ref(src, p+i, sstride), maxi)
				switch fy {
				case 1:
					v = (int32(src[p+i]) + v + 1) >> 1
				case 3:
					v = (int32(src[p+i+sstride]) + v + 1) >> 1
				}
				o[i] = uint16(v)
			}
		}
	case fx != 2 && fy != 2: // e, g, p, r
		hOff := (fy >> 1) * sstride // row of the horizontal half sample
		vOff := fx >> 1             // column of the vertical half sample
		for j := 0; j < h; j++ {
			o := dst[j*dstride : j*dstride+w]
			p := base + j*sstride
			for i := range o {
				a := clipHalfRef(hb6Ref(src, p+i+hOff), maxi)
				b := clipHalfRef(vb6Ref(src, p+i+vOff, sstride), maxi)
				o[i] = avg(a, b)
			}
		}
	default: // j and the quarter positions that average with it
		tmp := d.tmpI[:(h+5)*w]
		for r := 0; r < h+5; r++ {
			p := base + (r-2)*sstride
			row := tmp[r*w : r*w+w]
			for i := range row {
				row[i] = hb6Ref(src, p+i)
			}
		}
		for j := 0; j < h; j++ {
			o := dst[j*dstride : j*dstride+w]
			p := base + j*sstride
			for i := range o {
				c := (tap6(tmp[j*w+i], tmp[(j+1)*w+i], tmp[(j+2)*w+i], tmp[(j+3)*w+i], tmp[(j+4)*w+i], tmp[(j+5)*w+i]) + 512) >> 10
				if uint32(c) > uint32(maxi) {
					if c < 0 {
						c = 0
					} else {
						c = maxi
					}
				}
				switch {
				case fx == 2 && fy == 2:
					o[i] = uint16(c)
				case fy == 2: // i, k
					o[i] = avg(clipHalfRef(vb6Ref(src, p+i+(fx>>1), sstride), maxi), c)
				case fy == 1: // f
					o[i] = avg(clipHalfRef(tmp[(j+2)*w+i], maxi), c)
				default: // q
					o[i] = avg(clipHalfRef(tmp[(j+3)*w+i], maxi), c)
				}
			}
		}
	}
}

func hb6Ref(src []uint16, p int) int32 {
	return tap6(int32(src[p-2]), int32(src[p-1]), int32(src[p]), int32(src[p+1]), int32(src[p+2]), int32(src[p+3]))
}

func vb6Ref(src []uint16, p, s int) int32 {
	return tap6(int32(src[p-2*s]), int32(src[p-s]), int32(src[p]), int32(src[p+s]), int32(src[p+2*s]), int32(src[p+3*s]))
}

func clipHalfRef(v, maxv int32) int32 {
	v = (v + 16) >> 5
	if uint32(v) > uint32(maxv) {
		if v < 0 {
			return 0
		}
		return maxv
	}
	return v
}

// chromaMC predicts a w x h chroma block with eighth-sample fractions.
func (d *Decoder) chromaMCRef(dst []uint16, dstride int, plane []uint16, stride, pw, ph, x, y, fx, fy, w, h int) {
	src := plane
	sstride := stride
	base := y*stride + x
	if x < 0 || y < 0 || x+w+1 > pw || y+h+1 > ph {
		ww := w + 1
		tmp := d.tmpMC[:ww*(h+1)]
		for j := 0; j < h+1; j++ {
			yy := y + j
			if yy < 0 {
				yy = 0
			} else if yy >= ph {
				yy = ph - 1
			}
			row := plane[yy*stride : yy*stride+pw]
			for i := 0; i < ww; i++ {
				xx := x + i
				if xx < 0 {
					xx = 0
				} else if xx >= pw {
					xx = pw - 1
				}
				tmp[j*ww+i] = row[xx]
			}
		}
		src, sstride, base = tmp, ww, 0
	}
	w00 := int32((8 - fx) * (8 - fy))
	w01 := int32(fx * (8 - fy))
	w10 := int32((8 - fx) * fy)
	w11 := int32(fx * fy)
	for j := 0; j < h; j++ {
		o := dst[j*dstride : j*dstride+w]
		r0 := src[base+j*sstride:]
		r1 := src[base+(j+1)*sstride:]
		for i := 0; i < w; i++ {
			o[i] = uint16((w00*int32(r0[i]) + w01*int32(r0[i+1]) + w10*int32(r1[i]) + w11*int32(r1[i+1]) + 32) >> 6)
		}
	}
}
