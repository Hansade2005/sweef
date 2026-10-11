package av1

// upscaleFrame implements the upscaling process (spec 7.16).
func (f *frameDec) upscaleFrame(in *frameBuf) *frameBuf {
	fh := f.fh
	if !fh.useSuperres {
		return in
	}
	cov(covSuperres)
	out := newFrameBuf(alignUp(fh.upscaledWidth, 8)+8, in.h[0], f.subX, f.subY, f.numPlanes)
	maxPix := (1 << uint(f.bitDepth)) - 1
	for plane := 0; plane < f.numPlanes; plane++ {
		subX, subY := 0, 0
		if plane > 0 {
			subX, subY = f.subX, f.subY
		}
		downW := round2(fh.frameWidth, uint(subX))
		upW := round2(fh.upscaledWidth, uint(subX))
		planeH := round2(fh.frameHeight, uint(subY))
		stepX := ((downW << 14) + (upW / 2)) / upW
		err := upW*stepX - (downW << 14)
		initialSubpelX := (-((upW-downW)<<(14-1))+upW/2)/upW + (1 << (8 - 1)) - err/2
		initialSubpelX &= (1 << 14) - 1
		miW := f.miCols >> uint(subX)
		maxX := miW*4 - 1
		src := in.pl[plane]
		sstride := in.stride[plane]
		dst := out.pl[plane]
		dstride := out.stride[plane]
		for y := 0; y < planeH; y++ {
			row := src[y*sstride:]
			for x := 0; x < upW; x++ {
				srcX := -(1 << 14) + initialSubpelX + x*stepX
				srcXPx := srcX >> 14
				srcXSubpel := (srcX & ((1 << 14) - 1)) >> 8
				sum := 0
				filt := &upscaleFilter[srcXSubpel]
				for k := 0; k < 8; k++ {
					sx := clip3(0, maxX, srcXPx+(k-3))
					sum += int(row[sx]) * int(filt[k])
				}
				v := round2(sum, 7)
				if v < 0 {
					v = 0
				} else if v > maxPix {
					v = maxPix
				}
				dst[y*dstride+x] = uint16(v)
			}
		}
	}
	return out
}
