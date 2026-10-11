package h264

// Intra sample prediction (8.3).

// Intra NxN prediction modes.
const (
	imV = iota
	imH
	imDC
	imDDL
	imDDR
	imVR
	imHD
	imVL
	imHU
)

// pred4x4 predicts a 4x4 luma block in place (dst points at its first sample).
func pred4x4(pl []uint16, off, stride, mode int, left, top, tl, tr bool, bd int) {
	var t [8]int32 // top samples, index 0..7
	var l [4]int32
	var c int32 // top-left
	if top {
		for i := 0; i < 4; i++ {
			t[i] = int32(pl[off+i-stride])
		}
		if tr {
			for i := 4; i < 8; i++ {
				t[i] = int32(pl[off+i-stride])
			}
		} else {
			for i := 4; i < 8; i++ {
				t[i] = t[3]
			}
		}
	}
	if left {
		for i := 0; i < 4; i++ {
			l[i] = int32(pl[off+i*stride-1])
		}
	}
	if tl {
		c = int32(pl[off+-stride-1])
	}
	var o [16]uint16
	switch mode {
	case imV:
		for y := 0; y < 4; y++ {
			for x := 0; x < 4; x++ {
				o[y*4+x] = uint16(t[x])
			}
		}
	case imH:
		for y := 0; y < 4; y++ {
			for x := 0; x < 4; x++ {
				o[y*4+x] = uint16(l[y])
			}
		}
	case imDC:
		var v int32
		switch {
		case top && left:
			v = (t[0] + t[1] + t[2] + t[3] + l[0] + l[1] + l[2] + l[3] + 4) >> 3
		case left:
			v = (l[0] + l[1] + l[2] + l[3] + 2) >> 2
		case top:
			v = (t[0] + t[1] + t[2] + t[3] + 2) >> 2
		default:
			v = 1 << uint(bd-1)
		}
		for i := range o {
			o[i] = uint16(v)
		}
	case imDDL:
		for y := 0; y < 4; y++ {
			for x := 0; x < 4; x++ {
				if x == 3 && y == 3 {
					o[y*4+x] = uint16((t[6] + 3*t[7] + 2) >> 2)
				} else {
					o[y*4+x] = uint16((t[x+y] + 2*t[x+y+1] + t[x+y+2] + 2) >> 2)
				}
			}
		}
	case imDDR, imVR, imHD:
		// edge array e: e[0..3] = left bottom..top, e[4] = corner, e[5..12] = top
		var e [13]int32
		e[0], e[1], e[2], e[3] = l[3], l[2], l[1], l[0]
		e[4] = c
		for i := 0; i < 8; i++ {
			e[5+i] = t[i]
		}
		for y := 0; y < 4; y++ {
			for x := 0; x < 4; x++ {
				var v int32
				switch mode {
				case imDDR:
					// index relative to corner: x-y
					k := 4 + x - y
					v = (e[k-1] + 2*e[k] + e[k+1] + 2) >> 2
				case imVR:
					z := 2*x - y
					switch {
					case z >= 0 && z&1 == 0:
						k := 4 + x - (y >> 1)
						v = (e[k] + e[k+1] + 1) >> 1
					case z >= 0:
						k := 4 + x - (y >> 1)
						v = (e[k-1] + 2*e[k] + e[k+1] + 2) >> 2
					case z == -1:
						v = (l[0] + 2*c + t[0] + 2) >> 2
					default:
						v = (l[y-1] + 2*l[y-2] + corner(l[:], y-3, c) + 2) >> 2
					}
				case imHD:
					z := 2*y - x
					switch {
					case z >= 0 && z&1 == 0:
						k := 4 - y + (x >> 1)
						v = (e[k] + e[k-1] + 1) >> 1
					case z >= 0:
						k := 4 - y + (x >> 1)
						v = (e[k+1] + 2*e[k] + e[k-1] + 2) >> 2
					case z == -1:
						v = (l[0] + 2*c + t[0] + 2) >> 2
					default:
						v = (t[x-1] + 2*t[x-2] + corner(t[:], x-3, c) + 2) >> 2
					}
				}
				o[y*4+x] = uint16(v)
			}
		}
	case imVL:
		for y := 0; y < 4; y++ {
			for x := 0; x < 4; x++ {
				k := x + (y >> 1)
				if y&1 == 0 {
					o[y*4+x] = uint16((t[k] + t[k+1] + 1) >> 1)
				} else {
					o[y*4+x] = uint16((t[k] + 2*t[k+1] + t[k+2] + 2) >> 2)
				}
			}
		}
	case imHU:
		for y := 0; y < 4; y++ {
			for x := 0; x < 4; x++ {
				z := x + 2*y
				k := y + (x >> 1)
				var v int32
				switch {
				case z > 5:
					v = l[3]
				case z == 5:
					v = (l[2] + 3*l[3] + 2) >> 2
				case z&1 == 0:
					v = (l[k] + l[k+1] + 1) >> 1
				default:
					v = (l[k] + 2*l[k+1] + l[k+2] + 2) >> 2
				}
				o[y*4+x] = uint16(v)
			}
		}
	}
	for y := 0; y < 4; y++ {
		copy(pl[off+y*stride:off+y*stride+4], o[y*4:y*4+4])
	}
}

func corner(a []int32, i int, c int32) int32 {
	if i < 0 {
		return c
	}
	return a[i]
}

// pred8x8 predicts an 8x8 luma block with reference sample filtering.
func pred8x8(pl []uint16, off, stride, mode int, left, top, tl, tr bool, bd int) {
	var t [16]int32
	var l [8]int32
	var c int32
	if top {
		for i := 0; i < 8; i++ {
			t[i] = int32(pl[off+i-stride])
		}
		if tr {
			for i := 8; i < 16; i++ {
				t[i] = int32(pl[off+i-stride])
			}
		} else {
			for i := 8; i < 16; i++ {
				t[i] = t[7]
			}
		}
	}
	if left {
		for i := 0; i < 8; i++ {
			l[i] = int32(pl[off+i*stride-1])
		}
	}
	if tl {
		c = int32(pl[off+-stride-1])
	}
	// filtered samples
	var ft [16]int32
	var fl [8]int32
	var fc int32
	if top {
		if tl {
			ft[0] = (c + 2*t[0] + t[1] + 2) >> 2
		} else {
			ft[0] = (3*t[0] + t[1] + 2) >> 2
		}
		for x := 1; x < 15; x++ {
			ft[x] = (t[x-1] + 2*t[x] + t[x+1] + 2) >> 2
		}
		ft[15] = (t[14] + 3*t[15] + 2) >> 2
	}
	if tl {
		switch {
		case top && left:
			fc = (t[0] + 2*c + l[0] + 2) >> 2
		case top:
			fc = (3*c + t[0] + 2) >> 2
		case left:
			fc = (3*c + l[0] + 2) >> 2
		default:
			fc = c
		}
	}
	if left {
		if tl {
			fl[0] = (c + 2*l[0] + l[1] + 2) >> 2
		} else {
			fl[0] = (3*l[0] + l[1] + 2) >> 2
		}
		for y := 1; y < 7; y++ {
			fl[y] = (l[y-1] + 2*l[y] + l[y+1] + 2) >> 2
		}
		fl[7] = (l[6] + 3*l[7] + 2) >> 2
	}
	// P(x,-1) for x=-1..15 and P(-1,y)
	tp := func(x int) int32 {
		if x < 0 {
			return fc
		}
		return ft[x]
	}
	lp := func(y int) int32 {
		if y < 0 {
			return fc
		}
		return fl[y]
	}
	var o [64]uint16
	switch mode {
	case imV:
		for y := 0; y < 8; y++ {
			for x := 0; x < 8; x++ {
				o[y*8+x] = uint16(ft[x])
			}
		}
	case imH:
		for y := 0; y < 8; y++ {
			for x := 0; x < 8; x++ {
				o[y*8+x] = uint16(fl[y])
			}
		}
	case imDC:
		var v int32
		var st, sl int32
		for i := 0; i < 8; i++ {
			st += ft[i]
			sl += fl[i]
		}
		switch {
		case top && left:
			v = (st + sl + 8) >> 4
		case left:
			v = (sl + 4) >> 3
		case top:
			v = (st + 4) >> 3
		default:
			v = 1 << uint(bd-1)
		}
		for i := range o {
			o[i] = uint16(v)
		}
	case imDDL:
		for y := 0; y < 8; y++ {
			for x := 0; x < 8; x++ {
				if x == 7 && y == 7 {
					o[y*8+x] = uint16((ft[14] + 3*ft[15] + 2) >> 2)
				} else {
					o[y*8+x] = uint16((ft[x+y] + 2*ft[x+y+1] + ft[x+y+2] + 2) >> 2)
				}
			}
		}
	case imDDR:
		for y := 0; y < 8; y++ {
			for x := 0; x < 8; x++ {
				var v int32
				switch {
				case x > y:
					v = (tp(x-y-2) + 2*tp(x-y-1) + tp(x-y) + 2) >> 2
				case x < y:
					v = (lp(y-x-2) + 2*lp(y-x-1) + lp(y-x) + 2) >> 2
				default:
					v = (tp(0) + 2*fc + lp(0) + 2) >> 2
				}
				o[y*8+x] = uint16(v)
			}
		}
	case imVR:
		for y := 0; y < 8; y++ {
			for x := 0; x < 8; x++ {
				z := 2*x - y
				var v int32
				switch {
				case z >= 0 && z&1 == 0:
					v = (tp(x-(y>>1)-1) + tp(x-(y>>1)) + 1) >> 1
				case z >= 0:
					v = (tp(x-(y>>1)-2) + 2*tp(x-(y>>1)-1) + tp(x-(y>>1)) + 2) >> 2
				case z == -1:
					v = (lp(0) + 2*fc + tp(0) + 2) >> 2
				default:
					v = (lp(y-2*x-1) + 2*lp(y-2*x-2) + lp(y-2*x-3) + 2) >> 2
				}
				o[y*8+x] = uint16(v)
			}
		}
	case imHD:
		for y := 0; y < 8; y++ {
			for x := 0; x < 8; x++ {
				z := 2*y - x
				var v int32
				switch {
				case z >= 0 && z&1 == 0:
					v = (lp(y-(x>>1)-1) + lp(y-(x>>1)) + 1) >> 1
				case z >= 0:
					v = (lp(y-(x>>1)-2) + 2*lp(y-(x>>1)-1) + lp(y-(x>>1)) + 2) >> 2
				case z == -1:
					v = (lp(0) + 2*fc + tp(0) + 2) >> 2
				default:
					v = (tp(x-2*y-1) + 2*tp(x-2*y-2) + tp(x-2*y-3) + 2) >> 2
				}
				o[y*8+x] = uint16(v)
			}
		}
	case imVL:
		for y := 0; y < 8; y++ {
			for x := 0; x < 8; x++ {
				k := x + (y >> 1)
				if y&1 == 0 {
					o[y*8+x] = uint16((ft[k] + ft[k+1] + 1) >> 1)
				} else {
					o[y*8+x] = uint16((ft[k] + 2*ft[k+1] + ft[k+2] + 2) >> 2)
				}
			}
		}
	case imHU:
		for y := 0; y < 8; y++ {
			for x := 0; x < 8; x++ {
				z := x + 2*y
				k := y + (x >> 1)
				var v int32
				switch {
				case z > 13:
					v = fl[7]
				case z == 13:
					v = (fl[6] + 3*fl[7] + 2) >> 2
				case z&1 == 0:
					v = (fl[k] + fl[k+1] + 1) >> 1
				default:
					v = (fl[k] + 2*fl[k+1] + fl[k+2] + 2) >> 2
				}
				o[y*8+x] = uint16(v)
			}
		}
	}
	for y := 0; y < 8; y++ {
		copy(pl[off+y*stride:off+y*stride+8], o[y*8:y*8+8])
	}
}

// pred16x16 predicts a whole luma (or 4:4:4 chroma) macroblock. Modes: 0 V,
// 1 H, 2 DC, 3 plane.
func pred16x16(pl []uint16, off, stride, mode int, left, top bool, bd int) {
	maxv := int32(1)<<uint(bd) - 1
	switch mode {
	case 0:
		for y := 0; y < 16; y++ {
			copy(pl[off+y*stride:off+y*stride+16], pl[off-stride:off-stride+16])
		}
	case 1:
		for y := 0; y < 16; y++ {
			v := pl[off+y*stride-1]
			row := pl[off+y*stride : off+y*stride+16]
			for x := range row {
				row[x] = v
			}
		}
	case 2:
		var s int32
		if top {
			for x := 0; x < 16; x++ {
				s += int32(pl[off+x-stride])
			}
		}
		if left {
			for y := 0; y < 16; y++ {
				s += int32(pl[off+y*stride-1])
			}
		}
		var v int32
		switch {
		case top && left:
			v = (s + 16) >> 5
		case top || left:
			v = (s + 8) >> 4
		default:
			v = 1 << uint(bd-1)
		}
		for y := 0; y < 16; y++ {
			row := pl[off+y*stride : off+y*stride+16]
			for x := range row {
				row[x] = uint16(v)
			}
		}
	case 3:
		var h, v int32
		for i := 0; i < 8; i++ {
			h += int32(i+1) * (int32(pl[off+8+i-stride]) - int32(pl[off+6-i-stride]))
			v += int32(i+1) * (int32(pl[off+(8+i)*stride-1]) - int32(pl[off+(6-i)*stride-1]))
		}
		a := 16 * (int32(pl[off+15*stride-1]) + int32(pl[off+15-stride]))
		b := (5*h + 32) >> 6
		c := (5*v + 32) >> 6
		for y := 0; y < 16; y++ {
			for x := 0; x < 16; x++ {
				pl[off+y*stride+x] = clipPix((a+b*int32(x-7)+c*int32(y-7)+16)>>5, maxv)
			}
		}
	}
}

// predChroma predicts a chroma macroblock of w x h samples (8x8 for 4:2:0,
// 8x16 for 4:2:2). Modes: 0 DC, 1 H, 2 V, 3 plane.
// leftBy gives the availability of the left neighbours of each 4-row group
// (they differ only for MBAFF with constrained intra prediction).
func predChroma(pl []uint16, off, stride, mode int, leftBy [4]bool, top bool, bd, w, h int) {
	maxv := int32(1)<<uint(bd) - 1
	switch mode {
	case 0:
		// per 4x4 block sums of the top and left neighbours
		var sT [2]int32
		var sL [4]int32
		if top {
			for x := 0; x < w; x++ {
				sT[x>>2] += int32(pl[off+x-stride])
			}
		}
		for y := 0; y < h; y++ {
			if leftBy[y>>2] {
				sL[y>>2] += int32(pl[off+y*stride-1])
			}
		}
		dflt := int32(1) << uint(bd-1)
		for by := 0; by < h/4; by++ {
			for bx := 0; bx < 2; bx++ {
				var v int32
				left := leftBy[by]
				switch {
				case bx == 0 && by == 0, bx > 0 && by > 0:
					switch {
					case top && left:
						v = (sT[bx] + sL[by] + 4) >> 3
					case top:
						v = (sT[bx] + 2) >> 2
					case left:
						v = (sL[by] + 2) >> 2
					default:
						v = dflt
					}
				case bx > 0: // by == 0
					switch {
					case top:
						v = (sT[bx] + 2) >> 2
					case left:
						v = (sL[by] + 2) >> 2
					default:
						v = dflt
					}
				default: // bx == 0, by > 0
					switch {
					case left:
						v = (sL[by] + 2) >> 2
					case top:
						v = (sT[bx] + 2) >> 2
					default:
						v = dflt
					}
				}
				for y := 0; y < 4; y++ {
					row := pl[off+(by*4+y)*stride+bx*4 : off+(by*4+y)*stride+bx*4+4]
					for x := range row {
						row[x] = uint16(v)
					}
				}
			}
		}
	case 1:
		for y := 0; y < h; y++ {
			v := pl[off+y*stride-1]
			row := pl[off+y*stride : off+y*stride+8]
			for x := range row {
				row[x] = v
			}
		}
	case 2:
		for y := 0; y < h; y++ {
			copy(pl[off+y*stride:off+y*stride+8], pl[off-stride:off-stride+8])
		}
	case 3:
		yCF := 0
		if h == 16 {
			yCF = 4
		}
		var hh, vv int32
		for i := 0; i < 4; i++ {
			hh += int32(i+1) * (int32(pl[off+4+i-stride]) - int32(pl[off+2-i-stride]))
		}
		for i := 0; i < 4+yCF; i++ {
			vv += int32(i+1) * (int32(pl[off+(4+yCF+i)*stride-1]) - int32(pl[off+(2+yCF-i)*stride-1]))
		}
		a := 16 * (int32(pl[off+(h-1)*stride-1]) + int32(pl[off+7-stride]))
		b := (34*hh + 32) >> 6
		cc := int32(34)
		if h == 16 {
			cc = 5
		}
		c := (cc*vv + 32) >> 6
		for y := 0; y < h; y++ {
			for x := 0; x < 8; x++ {
				pl[off+y*stride+x] = clipPix((a+b*int32(x-3)+c*int32(y-3-yCF)+16)>>5, maxv)
			}
		}
	}
}
