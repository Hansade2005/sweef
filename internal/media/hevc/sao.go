package hevc

// Sample adaptive offset (8.7.3).

var dbgSAOHook func(d *Decoder, pic *Picture)

func (d *Decoder) saoPicture(pic *Picture) {
	s := pic.sps
	pp := d.apps
	if pp == nil || s != d.asps || len(d.slices) == 0 {
		return
	}
	d.sp, d.pp = s, pp
	if dbgSAOHook != nil {
		dbgSAOHook(d, pic)
	}
	nc := 3
	if s.chromaArrayType == 0 {
		nc = 1
	}
	var used [3]bool
	for _, sh := range d.slices {
		if sh.saoLuma {
			used[0] = true
		}
		if sh.saoChroma && nc == 3 {
			used[1], used[2] = true, true
		}
	}
	if !used[0] && !used[1] {
		return
	}
	checkFlags := pp.transquantBypass || (s.pcm && s.pcmLoopFilterOff)
	for ci := 0; ci < nc; ci++ {
		if !used[ci] {
			continue
		}
		src := make([]uint16, len(pic.planes[ci]))
		copy(src, pic.planes[ci])
		for ry := 0; ry < s.hCtb; ry++ {
			for rx := 0; rx < s.wCtb; rx++ {
				ctb := ry*s.wCtb + rx
				sp := &d.sao[ctb]
				if sp.typ[ci] == 0 {
					continue
				}
				if d.sliceOfCtb(ctb) == nil {
					continue
				}
				d.saoCtb(pic, src, ci, rx, ry, sp, checkFlags)
			}
		}
	}
}

func (d *Decoder) saoCtb(pic *Picture, src []uint16, ci, rx, ry int, sp *saoParams, checkFlags bool) {
	s, pp := d.sp, d.pp
	var sx, sy uint
	bd := s.bitDepthY
	pw, ph := pic.w, pic.h
	if ci > 0 {
		sx, sy = s.subW, s.subH
		bd = s.bitDepthC
		pw, ph = pic.cw, pic.ch
	}
	cw := s.ctbSize >> sx
	chh := s.ctbSize >> sy
	x0, y0 := rx*cw, ry*chh
	w, h := cw, chh
	if x0+w > pw {
		w = pw - x0
	}
	if y0+h > ph {
		h = ph - y0
	}
	stride := pic.stride[ci]
	dst := pic.planes[ci]
	maxV := int32(1)<<uint(bd) - 1
	off := &sp.offset[ci]
	ctb := ry*s.wCtb + rx
	l2m := uint(s.log2MinCb)
	noFilter := func(x, y int) bool {
		lx, ly := x<<sx, y<<sy
		return d.cuTab[(ly>>l2m)*s.wMinCb+lx>>l2m].flags&cuNoFilter != 0
	}
	if sp.typ[ci] == 1 {
		var table [32]int32
		for k := 0; k < 4; k++ {
			table[(k+int(sp.band[ci]))&31] = int32(off[k+1])
		}
		shift := uint(bd - 5)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				if checkFlags && noFilter(x0+x, y0+y) {
					continue
				}
				v := int32(src[(y0+y)*stride+x0+x])
				v += table[v>>shift]
				if v < 0 {
					v = 0
				} else if v > maxV {
					v = maxV
				}
				dst[(y0+y)*stride+x0+x] = uint16(v)
			}
		}
		return
	}
	// edge offset: which neighbouring CTBs may be used
	var ok [3][3]bool
	cur := d.sliceOfCtb(ctb)
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			nx, ny := rx+dx, ry+dy
			if nx < 0 || ny < 0 || nx >= s.wCtb || ny >= s.hCtb {
				continue
			}
			if dx == 0 && dy == 0 {
				ok[1][1] = true
				continue
			}
			nctb := ny*s.wCtb + nx
			sn := d.sliceOfCtb(nctb)
			if sn == nil {
				continue
			}
			allowed := true
			if d.ctbSliceAddr[nctb] != d.ctbSliceAddr[ctb] {
				later := cur
				if pp.rsToTs[nctb] > pp.rsToTs[ctb] {
					later = sn
				}
				if !later.loopFilterAcross {
					allowed = false
				}
			}
			if pp.tileRs[nctb] != pp.tileRs[ctb] && !pp.loopFilterAcrossTiles {
				allowed = false
			}
			ok[dy+1][dx+1] = allowed
		}
	}
	var hx, hy [2]int
	switch sp.eo[ci] {
	case 0:
		hx, hy = [2]int{-1, 1}, [2]int{0, 0}
	case 1:
		hx, hy = [2]int{0, 0}, [2]int{-1, 1}
	case 2:
		hx, hy = [2]int{-1, 1}, [2]int{-1, 1}
	default:
		hx, hy = [2]int{1, -1}, [2]int{-1, 1}
	}
	var offE [5]int32
	// raw edgeIdx 0,1,2,3,4 -> offset index 1,2,0,3,4
	offE[0], offE[1], offE[2], offE[3], offE[4] = int32(off[1]), int32(off[2]), 0, int32(off[3]), int32(off[4])
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if checkFlags && noFilter(x0+x, y0+y) {
				continue
			}
			v := int32(src[(y0+y)*stride+x0+x])
			idx := 2
			valid := true
			for k := 0; k < 2; k++ {
				xn, yn := x+hx[k], y+hy[k]
				gx, gy := x0+xn, y0+yn
				if gx < 0 || gy < 0 || gx >= pw || gy >= ph {
					valid = false
					break
				}
				cx, cy := 1, 1
				if xn < 0 {
					cx = 0
				} else if xn >= cw {
					cx = 2
				}
				if yn < 0 {
					cy = 0
				} else if yn >= chh {
					cy = 2
				}
				if !ok[cy][cx] {
					valid = false
					break
				}
				nv := int32(src[gy*stride+gx])
				if v > nv {
					idx++
				} else if v < nv {
					idx--
				}
			}
			if !valid {
				continue
			}
			v += offE[idx]
			if v < 0 {
				v = 0
			} else if v > maxV {
				v = maxV
			}
			dst[(y0+y)*stride+x0+x] = uint16(v)
		}
	}
}
