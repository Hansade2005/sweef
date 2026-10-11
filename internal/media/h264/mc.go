package h264

// Inter sample interpolation (8.4.2.2).

func tap6(a, b, c, d, e, f int32) int32 { return a - 5*b + 20*c + 20*d - 5*e + f }

// lumaMC predicts a w x h block from plane (stride, width pw, height ph) at
// integer position (x, y) with quarter-sample fraction (fx, fy) into dst.
// tmp must hold (w+5)*(h+5) bytes and tmpI (w+5)*h int32 values.
func (d *Decoder) lumaMC(dst []byte, dstride int, plane []byte, stride, pw, ph, x, y, fx, fy, w, h int) {
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
	// horizontal half-sample at (row offset ro), clipped: b
	hb := func(j, i int) int32 {
		p := base + j*sstride + i
		return tap6(int32(src[p-2]), int32(src[p-1]), int32(src[p]), int32(src[p+1]), int32(src[p+2]), int32(src[p+3]))
	}
	vb := func(j, i int) int32 {
		p := base + j*sstride + i
		return tap6(int32(src[p-2*sstride]), int32(src[p-sstride]), int32(src[p]), int32(src[p+sstride]), int32(src[p+2*sstride]), int32(src[p+3*sstride]))
	}
	clipB := func(v int32) int32 { return int32(clip255((v + 16) >> 5)) }
	at := func(j, i int) int32 { return int32(src[base+j*sstride+i]) }
	// j (centre) via intermediate horizontal values
	centre := func(j, i int) int32 {
		a := hb(j-2, i)
		b := hb(j-1, i)
		c := hb(j, i)
		dd := hb(j+1, i)
		e := hb(j+2, i)
		f := hb(j+3, i)
		return int32(clip255((tap6(a, b, c, dd, e, f) + 512) >> 10))
	}
	avg := func(a, b int32) byte { return byte((a + b + 1) >> 1) }
	for j := 0; j < h; j++ {
		o := dst[j*dstride : j*dstride+w]
		for i := 0; i < w; i++ {
			var v byte
			switch fy<<2 | fx {
			case 0x1: // a
				v = avg(at(j, i), clipB(hb(j, i)))
			case 0x2: // b
				v = byte(clipB(hb(j, i)))
			case 0x3: // c
				v = avg(at(j, i+1), clipB(hb(j, i)))
			case 0x4: // d
				v = avg(at(j, i), clipB(vb(j, i)))
			case 0x8: // h
				v = byte(clipB(vb(j, i)))
			case 0xc: // n
				v = avg(at(j+1, i), clipB(vb(j, i)))
			case 0xa: // j
				v = byte(centre(j, i))
			case 0x9: // i
				v = avg(clipB(vb(j, i)), centre(j, i))
			case 0xb: // k
				v = avg(clipB(vb(j, i+1)), centre(j, i))
			case 0x6: // f
				v = avg(clipB(hb(j, i)), centre(j, i))
			case 0xe: // q
				v = avg(clipB(hb(j+1, i)), centre(j, i))
			case 0x5: // e
				v = avg(clipB(hb(j, i)), clipB(vb(j, i)))
			case 0x7: // g
				v = avg(clipB(hb(j, i)), clipB(vb(j, i+1)))
			case 0xd: // p
				v = avg(clipB(vb(j, i)), clipB(hb(j+1, i)))
			case 0xf: // r
				v = avg(clipB(vb(j, i+1)), clipB(hb(j+1, i)))
			}
			o[i] = v
		}
	}
}

// chromaMC predicts a w x h chroma block with eighth-sample fractions.
func (d *Decoder) chromaMC(dst []byte, dstride int, plane []byte, stride, pw, ph, x, y, fx, fy, w, h int) {
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
			o[i] = byte((w00*int32(r0[i]) + w01*int32(r0[i+1]) + w10*int32(r1[i]) + w11*int32(r1[i+1]) + 32) >> 6)
		}
	}
}
