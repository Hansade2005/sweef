package hevc

// Sample adaptive offset (8.7.3) and the schedule that drives the in-loop
// filters.
//
// The filters run on a finished picture but in a CTB-by-CTB order: CTB
// (x, y) is deblocked once CTB (x+1, y+1) has been "decoded", and SAO of
// CTB (x-1, y-1) follows deblocking of CTB (x, y). SAO is applied in place;
// the unfiltered edge samples of every SAO-filtered CTB are saved so that its
// neighbours can still read them. Because horizontal chroma edges of a CTB
// column are filtered one CTB late, SAO of a CTB can observe neighbouring
// samples whose horizontal chroma deblocking is still pending when the CTB is
// only 8 chroma samples wide; this ordering is what the common decoders do and
// is reproduced here so that the output is identical.

func (d *Decoder) loopFilterPicture(pic *Picture) {
	s := pic.sps
	pp := d.apps
	if pp == nil || s != d.asps || len(d.slices) == 0 {
		return
	}
	d.sp, d.pp = s, pp
	deblock := false
	var saoUsed [3]bool
	for _, sh := range d.slices {
		if !sh.deblockDisabled {
			deblock = true
		}
		if sh.saoLuma {
			saoUsed[0] = true
		}
		if sh.saoChroma && s.chromaArrayType != 0 {
			saoUsed[1], saoUsed[2] = true, true
		}
	}
	sao := saoUsed[0] || saoUsed[1]
	if dbgNoDeblock {
		deblock = false
	}
	if !deblock && !sao {
		return
	}
	if sao {
		d.saoInit(pic)
	}
	ctb := s.ctbSize
	// decoding order, i.e. tile scan
	for _, rs := range pp.tsToRs {
		d.hlsFilters(pic, rs%s.wCtb*ctb, rs/s.wCtb*ctb, deblock, sao)
	}
	// the last CTB
	lx, ly := (s.wCtb-1)*ctb, (s.hCtb-1)*ctb
	d.hlsFilter(pic, lx, ly, deblock, sao)
}

func (d *Decoder) hlsFilters(pic *Picture, xCtb, yCtb int, deblock, sao bool) {
	s := d.sp
	ctb := s.ctbSize
	xEnd := xCtb >= s.width-ctb
	yEnd := yCtb >= s.height-ctb
	if yCtb != 0 && xCtb != 0 {
		d.hlsFilter(pic, xCtb-ctb, yCtb-ctb, deblock, sao)
	}
	if yCtb != 0 && xEnd {
		d.hlsFilter(pic, xCtb, yCtb-ctb, deblock, sao)
	}
	if xCtb != 0 && yEnd {
		d.hlsFilter(pic, xCtb-ctb, yCtb, deblock, sao)
	}
}

func (d *Decoder) hlsFilter(pic *Picture, x, y int, deblock, sao bool) {
	s := d.sp
	ctb := s.ctbSize
	xEnd := x >= s.width-ctb
	if deblock {
		d.deblockCTB(pic, x, y)
	}
	if !sao {
		return
	}
	yEnd := y >= s.height-ctb
	if y != 0 && x != 0 {
		d.saoCtbFF(pic, (x-ctb)/ctb, (y-ctb)/ctb)
	}
	if x != 0 && yEnd {
		d.saoCtbFF(pic, (x-ctb)/ctb, y/ctb)
	}
	if y != 0 && xEnd {
		d.saoCtbFF(pic, x/ctb, (y-ctb)/ctb)
	}
	if xEnd && yEnd {
		d.saoCtbFF(pic, x/ctb, y/ctb)
	}
}

func (d *Decoder) saoInit(pic *Picture) {
	s := d.sp
	nc := 3
	if s.chromaArrayType == 0 {
		nc = 1
	}
	n := s.wCtb * s.hCtb
	for ci := 0; ci < nc; ci++ {
		if len(d.saoApplied[ci]) != n {
			d.saoApplied[ci] = make([]bool, n)
		} else {
			for i := range d.saoApplied[ci] {
				d.saoApplied[ci][i] = false
			}
		}
		w, h := pic.w, pic.h
		if ci > 0 {
			w, h = pic.cw, pic.ch
		}
		if len(d.saoH[ci]) < 2*s.hCtb*w {
			d.saoH[ci] = make([]uint16, 2*s.hCtb*w)
		}
		if len(d.saoV[ci]) < 2*s.wCtb*h {
			d.saoV[ci] = make([]uint16, 2*s.wCtb*h)
		}
	}
}

// saoCtbFF applies SAO to all components of the CTB (rx, ry).
func (d *Decoder) saoCtbFF(pic *Picture, rx, ry int) {
	s, pp := d.sp, d.pp
	ctbAddr := ry*s.wCtb + rx
	cur := d.sliceOfCtb(ctbAddr)
	if cur == nil {
		return
	}
	nc := 3
	if s.chromaArrayType == 0 {
		nc = 1
	}
	sp := &d.sao[ctbAddr]
	// neighbouring CTBs usable for filtering (slices, tiles)
	var ok [3][3]bool
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
			if d.sliceOfCtb(nctb) == nil {
				continue
			}
			allowed := true
			// The specification uses the flag of the slice that comes later
			// in decoding order; ffmpeg always uses the flag of the slice of
			// the CTB being filtered, which is what is done here.
			if d.ctbSliceAddr[nctb] != d.ctbSliceAddr[ctbAddr] && !cur.loopFilterAcross {
				allowed = false
			}
			if pp.tileRs[nctb] != pp.tileRs[ctbAddr] && !pp.loopFilterAcrossTiles {
				allowed = false
			}
			ok[dy+1][dx+1] = allowed
		}
	}
	checkFlags := pp.transquantBypass || (s.pcm && s.pcmLoopFilterOff)
	for ci := 0; ci < nc; ci++ {
		if sp.typ[ci] == 0 {
			continue
		}
		d.saoComp(pic, ci, rx, ry, sp, &ok, checkFlags)
	}
}

func (d *Decoder) saoComp(pic *Picture, ci, rx, ry int, sp *saoParams, ok *[3][3]bool, checkFlags bool) {
	s := d.sp
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
	pl := pic.planes[ci]
	maxV := int32(1)<<uint(bd) - 1
	off := &sp.offset[ci]
	l2m := uint(s.log2MinCb)
	// Samples of pcm / transquant bypass blocks keep their unfiltered value.
	// ffmpeg compares the luma based block grid with the component sized
	// CTB, so in subsampled planes only the top left part of a CTB is
	// covered; that is reproduced here.
	noFilter := func(x, y int) bool {
		if (x-x0)<<sx >= w || (y-y0)<<sy >= h {
			return false
		}
		lx, ly := x<<sx, y<<sy
		return d.cuTab[(ly>>l2m)*s.wMinCb+lx>>l2m].flags&cuNoFilter != 0
	}
	ctbAddr := ry*s.wCtb + rx
	hb := d.saoH[ci]
	vb := d.saoV[ci]
	applied := d.saoApplied[ci]
	// the unfiltered edges of this CTB are kept for its neighbours
	copy(hb[(2*ry*pw+x0):], pl[y0*stride+x0:y0*stride+x0+w])
	copy(hb[((2*ry+1)*pw+x0):], pl[(y0+h-1)*stride+x0:(y0+h-1)*stride+x0+w])
	for y := 0; y < h; y++ {
		vb[(2*rx)*ph+y0+y] = pl[(y0+y)*stride+x0]
		vb[(2*rx+1)*ph+y0+y] = pl[(y0+y)*stride+x0+w-1]
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
				v := int32(pl[(y0+y)*stride+x0+x])
				v += table[v>>shift]
				if v < 0 {
					v = 0
				} else if v > maxV {
					v = maxV
				}
				pl[(y0+y)*stride+x0+x] = uint16(v)
			}
		}
		applied[ctbAddr] = true
		return
	}
	// edge offset: gather the (w+2) x (h+2) neighbourhood
	bw := w + 2
	buf := d.scratch.saoBuf[:bw*(h+2)]
	at := func(cx, cy int) int { return (cy+1)*bw + cx + 1 } // cx in -1..w, cy in -1..h
	for y := 0; y < h; y++ {
		copy(buf[at(0, y):at(0, y)+w], pl[(y0+y)*stride+x0:(y0+y)*stride+x0+w])
	}
	// left / right columns
	if rx > 0 {
		for y := 0; y < h; y++ {
			if applied[ctbAddr-1] {
				buf[at(-1, y)] = vb[(2*rx-1)*ph+y0+y]
			} else {
				buf[at(-1, y)] = pl[(y0+y)*stride+x0-1]
			}
		}
	}
	if x0+w < pw {
		for y := 0; y < h; y++ {
			if applied[ctbAddr+1] {
				buf[at(w, y)] = vb[(2*rx+2)*ph+y0+y]
			} else {
				buf[at(w, y)] = pl[(y0+y)*stride+x0+w]
			}
		}
	}
	// top / bottom rows including corners
	row := func(cy, ny, slot, frameY int) {
		for dx := -1; dx <= 1; dx++ {
			nx := rx + dx
			if nx < 0 || nx >= s.wCtb {
				continue
			}
			var x1, x2 int
			switch dx {
			case -1:
				x1, x2 = -1, 0
			case 0:
				x1, x2 = 0, w
			default:
				x1, x2 = w, w+1
			}
			useSaved := applied[ny*s.wCtb+nx]
			for cx := x1; cx < x2; cx++ {
				if useSaved {
					buf[at(cx, cy)] = hb[slot*pw+x0+cx]
				} else {
					buf[at(cx, cy)] = pl[frameY*stride+x0+cx]
				}
			}
		}
	}
	if ry > 0 {
		row(-1, ry-1, 2*ry-1, y0-1)
	}
	if y0+h < ph {
		row(h, ry+1, 2*ry+2, y0+h)
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
	offE[0], offE[1], offE[2], offE[3], offE[4] = int32(off[1]), int32(off[2]), 0, int32(off[3]), int32(off[4])
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if checkFlags && noFilter(x0+x, y0+y) {
				continue
			}
			v := int32(buf[at(x, y)])
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
				nv := int32(buf[at(xn, yn)])
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
			pl[(y0+y)*stride+x0+x] = uint16(v)
		}
	}
	applied[ctbAddr] = true
}
