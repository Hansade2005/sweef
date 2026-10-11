package h264

// Slice groups (flexible macroblock ordering, 8.2.2).

// sliceGroupMap returns the slice group of every macroblock address unit of
// the current picture: raster macroblocks for frames and fields, macroblock
// pairs for MBAFF frames.
func (d *Decoder) sliceGroupMap(h *sliceHeader) []uint8 {
	pps, sps := h.pps, h.sps
	w := sps.MbW
	hm := sps.MbH
	if !sps.FrameMbsOnly {
		hm >>= 1
	}
	n := w * hm
	numSG := pps.NumSliceGroups
	mu := make([]uint8, n)
	dir := btoi(pps.ChangeDirection)
	units0 := min(h.sliceGroupCycle*pps.ChangeRate, n)
	upper := units0
	if dir == 1 {
		upper = n - units0
	}
	switch pps.SliceGroupType {
	case 0:
		i := 0
		for i < n {
			for g := 0; g < numSG && i < n; g++ {
				for j := 0; j < pps.RunLength[g] && i+j < n; j++ {
					mu[i+j] = uint8(g)
				}
				i += pps.RunLength[g]
			}
		}
	case 1:
		for i := range mu {
			mu[i] = uint8(((i % w) + (((i / w) * numSG) / 2)) % numSG)
		}
	case 2:
		for i := range mu {
			mu[i] = uint8(numSG - 1)
		}
		for g := numSG - 2; g >= 0; g-- {
			yTL, xTL := pps.TopLeft[g]/w, pps.TopLeft[g]%w
			yBR, xBR := pps.BotRght[g]/w, pps.BotRght[g]%w
			for y := yTL; y <= yBR && y < hm; y++ {
				for x := xTL; x <= xBR && x < w; x++ {
					mu[y*w+x] = uint8(g)
				}
			}
		}
	case 3:
		for i := range mu {
			mu[i] = 1
		}
		x, y := (w-dir)/2, (hm-dir)/2
		left, top, right, bottom := x, y, x, y
		xDir, yDir := dir-1, dir
		for k := 0; k < units0; {
			vacant := 0
			if mu[y*w+x] == 1 {
				vacant = 1
				mu[y*w+x] = 0
			}
			switch {
			case xDir == -1 && x == left:
				left = max(left-1, 0)
				x = left
				xDir, yDir = 0, 2*dir-1
			case xDir == 1 && x == right:
				right = min(right+1, w-1)
				x = right
				xDir, yDir = 0, 1-2*dir
			case yDir == -1 && y == top:
				top = max(top-1, 0)
				y = top
				xDir, yDir = 1-2*dir, 0
			case yDir == 1 && y == bottom:
				bottom = min(bottom+1, hm-1)
				y = bottom
				xDir, yDir = 2*dir-1, 0
			default:
				x += xDir
				y += yDir
			}
			k += vacant
		}
	case 4:
		for i := range mu {
			if i < upper {
				mu[i] = uint8(dir)
			} else {
				mu[i] = uint8(1 - dir)
			}
		}
	case 5:
		k := 0
		for j := 0; j < w; j++ {
			for i := 0; i < hm; i++ {
				if k < upper {
					mu[i*w+j] = uint8(dir)
				} else {
					mu[i*w+j] = uint8(1 - dir)
				}
				k++
			}
		}
	case 6:
		copy(mu, pps.SliceGroupID)
	}
	if !sps.FrameMbsOnly && !d.fieldPic && !d.mbaffFrame {
		m := make([]uint8, w*sps.MbH)
		for i := range m {
			m[i] = mu[(i/(2*w))*w+i%w]
		}
		return m
	}
	return mu
}

// fmoSet positions the decoder at macroblock address unit u.
func (d *Decoder) fmoSet(u int) {
	d.fmoPos = u
	d.mbx = u % d.mbW
	row := u / d.mbW
	switch {
	case d.mbaffFrame:
		d.mby = row << 1
	case d.fieldPic:
		d.mby = row<<1 + btoi(d.picStructure == picBottom)
	default:
		d.mby = row
	}
}

func (d *Decoder) startFMO(first int) bool {
	if first >= len(d.fmoMap) {
		return false
	}
	d.fmoSet(first)
	return true
}

// advanceFMO moves to the next macroblock unit of the current slice group.
func (d *Decoder) advanceFMO() bool {
	g := d.fmoMap[d.fmoPos]
	for u := d.fmoPos + 1; u < len(d.fmoMap); u++ {
		if d.fmoMap[u] == g {
			d.fmoSet(u)
			return true
		}
	}
	return false
}
