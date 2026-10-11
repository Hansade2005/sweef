// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package vp8

import "image"

// filter2 modifies a 2-pixel wide or 2-pixel high band along an edge.
func filter2(pix []byte, level, index, iStep, jStep int) {
	for n := 16; n > 0; n, index = n-1, index+iStep {
		p1 := int(pix[index-2*jStep])
		p0 := int(pix[index-1*jStep])
		q0 := int(pix[index+0*jStep])
		q1 := int(pix[index+1*jStep])
		if abs(p0-q0)<<1+abs(p1-q1)>>1 > level {
			continue
		}
		a := 3*(q0-p0) + clamp127(p1-q1)
		a1 := clamp15((a + 4) >> 3)
		a2 := clamp15((a + 3) >> 3)
		pix[index-1*jStep] = clamp255(p0 + a2)
		pix[index+0*jStep] = clamp255(q0 - a1)
	}
}

// filter246 modifies a 2-, 4- or 6-pixel wide or high band along an edge.
func filter246(pix []byte, n, level, ilevel, hlevel, index, iStep, jStep int, fourNotSix bool) {
	for ; n > 0; n, index = n-1, index+iStep {
		p3 := int(pix[index-4*jStep])
		p2 := int(pix[index-3*jStep])
		p1 := int(pix[index-2*jStep])
		p0 := int(pix[index-1*jStep])
		q0 := int(pix[index+0*jStep])
		q1 := int(pix[index+1*jStep])
		q2 := int(pix[index+2*jStep])
		q3 := int(pix[index+3*jStep])
		if abs(p0-q0)<<1+abs(p1-q1)>>1 > level {
			continue
		}
		if abs(p3-p2) > ilevel ||
			abs(p2-p1) > ilevel ||
			abs(p1-p0) > ilevel ||
			abs(q1-q0) > ilevel ||
			abs(q2-q1) > ilevel ||
			abs(q3-q2) > ilevel {
			continue
		}
		if abs(p1-p0) > hlevel || abs(q1-q0) > hlevel {
			// Filter 2 pixels.
			a := 3*(q0-p0) + clamp127(p1-q1)
			a1 := clamp15((a + 4) >> 3)
			a2 := clamp15((a + 3) >> 3)
			pix[index-1*jStep] = clamp255(p0 + a2)
			pix[index+0*jStep] = clamp255(q0 - a1)
		} else if fourNotSix {
			// Filter 4 pixels.
			a := 3 * (q0 - p0)
			a1 := clamp15((a + 4) >> 3)
			a2 := clamp15((a + 3) >> 3)
			a3 := (a1 + 1) >> 1
			pix[index-2*jStep] = clamp255(p1 + a3)
			pix[index-1*jStep] = clamp255(p0 + a2)
			pix[index+0*jStep] = clamp255(q0 - a1)
			pix[index+1*jStep] = clamp255(q1 - a3)
		} else {
			// Filter 6 pixels.
			a := clamp127(3*(q0-p0) + clamp127(p1-q1))
			a1 := (27*a + 63) >> 7
			a2 := (18*a + 63) >> 7
			a3 := (9*a + 63) >> 7
			pix[index-3*jStep] = clamp255(p2 + a3)
			pix[index-2*jStep] = clamp255(p1 + a2)
			pix[index-1*jStep] = clamp255(p0 + a1)
			pix[index+0*jStep] = clamp255(q0 - a1)
			pix[index+1*jStep] = clamp255(q1 - a2)
			pix[index+2*jStep] = clamp255(q2 - a3)
		}
	}
}

// frameFilter runs the loop filter over the whole reconstructed frame in
// macroblock raster order (sections 15.2 and 15.3).
func (d *Decoder) frameFilter(img *image.YCbCr) {
	sharp := d.lf.sharp
	key := d.hdr.KeyFrame
	ys, cs := img.YStride, img.CStride
	for mby := 0; mby < d.mbh; mby++ {
		for mbx := 0; mbx < d.mbw; mbx++ {
			mi := &d.mbs[(mby+1)*d.miStride+mbx+1]
			lvl := int(mi.lfLevel)
			if lvl == 0 {
				continue
			}
			il := lvl
			if sharp > 0 {
				if sharp > 4 {
					il >>= 2
				} else {
					il >>= 1
				}
				if il > 9-sharp {
					il = 9 - sharp
				}
			}
			if il < 1 {
				il = 1
			}
			hl := 0
			if key {
				if lvl >= 40 {
					hl = 2
				} else if lvl >= 15 {
					hl = 1
				}
			} else {
				if lvl >= 40 {
					hl = 3
				} else if lvl >= 20 {
					hl = 2
				} else if lvl >= 15 {
					hl = 1
				}
			}
			mbl := (lvl+2)*2 + il
			bl := lvl*2 + il
			inner := mi.lfInner
			yi := (mby*ys + mbx) * 16
			ci := (mby*cs + mbx) * 8
			if d.lf.simple {
				if mbx > 0 {
					filter2(img.Y, mbl, yi, ys, 1)
				}
				if inner {
					filter2(img.Y, bl, yi+4, ys, 1)
					filter2(img.Y, bl, yi+8, ys, 1)
					filter2(img.Y, bl, yi+12, ys, 1)
				}
				if mby > 0 {
					filter2(img.Y, mbl, yi, 1, ys)
				}
				if inner {
					filter2(img.Y, bl, yi+ys*4, 1, ys)
					filter2(img.Y, bl, yi+ys*8, 1, ys)
					filter2(img.Y, bl, yi+ys*12, 1, ys)
				}
				continue
			}
			if mbx > 0 {
				filter246(img.Y, 16, mbl, il, hl, yi, ys, 1, false)
				filter246(img.Cb, 8, mbl, il, hl, ci, cs, 1, false)
				filter246(img.Cr, 8, mbl, il, hl, ci, cs, 1, false)
			}
			if inner {
				filter246(img.Y, 16, bl, il, hl, yi+4, ys, 1, true)
				filter246(img.Y, 16, bl, il, hl, yi+8, ys, 1, true)
				filter246(img.Y, 16, bl, il, hl, yi+12, ys, 1, true)
				filter246(img.Cb, 8, bl, il, hl, ci+4, cs, 1, true)
				filter246(img.Cr, 8, bl, il, hl, ci+4, cs, 1, true)
			}
			if mby > 0 {
				filter246(img.Y, 16, mbl, il, hl, yi, 1, ys, false)
				filter246(img.Cb, 8, mbl, il, hl, ci, 1, cs, false)
				filter246(img.Cr, 8, mbl, il, hl, ci, 1, cs, false)
			}
			if inner {
				filter246(img.Y, 16, bl, il, hl, yi+ys*4, 1, ys, true)
				filter246(img.Y, 16, bl, il, hl, yi+ys*8, 1, ys, true)
				filter246(img.Y, 16, bl, il, hl, yi+ys*12, 1, ys, true)
				filter246(img.Cb, 8, bl, il, hl, ci+cs*4, 1, cs, true)
				filter246(img.Cr, 8, bl, il, hl, ci+cs*4, 1, cs, true)
			}
		}
	}
}

// mbFilterLevel computes the loop filter level of a macroblock (libvpx
// vp8_loop_filter_frame_init).
func (d *Decoder) mbFilterLevel(segment int, ref, mode uint8) uint8 {
	lvl := d.lf.level
	if d.seg.enabled {
		if d.seg.absDelta {
			lvl = int(d.seg.lf[segment])
		} else {
			lvl += int(d.seg.lf[segment])
		}
		lvl = clampInt(lvl, 0, 63)
	}
	if d.lf.deltaEnabled {
		lvl += int(d.lf.ref[ref])
		if ref == refIntra {
			if mode == mbB {
				lvl += int(d.lf.mode[0])
			}
		} else {
			switch mode {
			case mbZero:
				lvl += int(d.lf.mode[1])
			case mbSplit:
				lvl += int(d.lf.mode[3])
			default:
				lvl += int(d.lf.mode[2])
			}
		}
		lvl = clampInt(lvl, 0, 63)
	}
	return uint8(lvl)
}

func clampInt(x, lo, hi int) int {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}

// intSize is either 32 or 64.
const intSize = 32 << (^uint(0) >> 63)

func abs(x int) int {
	// m := -1 if x < 0. m := 0 otherwise.
	m := x >> (intSize - 1)

	// In two's complement representation, the negative number
	// of any number (except the smallest one) can be computed
	// by flipping all the bits and add 1. This is faster than
	// code with a branch.
	// See Hacker's Delight, section 2-4.
	return (x ^ m) - m
}

func clamp15(x int) int {
	if x < -16 {
		return -16
	}
	if x > 15 {
		return 15
	}
	return x
}

func clamp127(x int) int {
	if x < -128 {
		return -128
	}
	if x > 127 {
		return 127
	}
	return x
}

func clamp255(x int) uint8 {
	if x < 0 {
		return 0
	}
	if x > 255 {
		return 255
	}
	return uint8(x)
}
