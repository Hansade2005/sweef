package av1

// Large scale tile decoding (spec 7.3, 5.12): a "camera" frame header sets up
// the geometry and coding tools of the tiles, and tile list OBUs then carry
// individually coded tiles that are decoded against anchor frames (earlier
// decoded frames) and assembled into an output picture. It is enabled with
// Decoder.LargeScale (libaom's tile mode): frames decoded before that are the
// anchor frames, indexed in decoding order; the first frame header OBU
// afterwards is the camera frame header.

const maxAnchorFrames = 128

type cameraState struct {
	fh *frameHdr
	fd *frameDec
}

// recordAnchor remembers a decoded frame as a potential anchor frame.
func (d *Decoder) recordAnchor(rf *refFrame) {
	if len(d.anchors) >= maxAnchorFrames {
		d.anchors = d.anchors[1:]
	}
	d.anchors = append(d.anchors, rf)
}

// ResetAnchors forgets the anchor frames collected so far.
func (d *Decoder) ResetAnchors() { d.anchors = nil }

// cameraHeaderOBU parses a camera frame header and prepares the tile decoding
// state; the tile data that may follow the header in an OBU_FRAME is ignored.
func (d *Decoder) cameraHeaderOBU(payload []byte) error {
	pb := &bitReader{buf: payload}
	fh, err := d.parseFrameHeader(pb, d.temporalID, d.spatialID)
	if err != nil {
		d.seenFrameHeader = false
		return err
	}
	d.seenFrameHeader = false
	if fh.showExisting || fh.frameIsIntra {
		return errBitstream
	}
	d.fh = fh
	d.orderHints = fh.orderHints
	if err := d.setupFrame(fh); err != nil {
		return err
	}
	d.camera = &cameraState{fh: fh, fd: d.fd}
	d.fd = nil
	cov(covLargeScale)
	return nil
}

// tileListOBU decodes a tile list OBU (5.12) into one output picture.
func (d *Decoder) tileListOBU(payload []byte) error {
	cam := d.camera
	if cam == nil {
		return errBitstream
	}
	fh, f := cam.fh, cam.fd
	b := &bitReader{buf: payload}
	wTiles := b.f(8) + 1
	hTiles := b.f(8) + 1
	count := b.f(16) + 1
	if b.err || count > wTiles*hTiles || count > 512 {
		return errBitstream
	}
	// uniform tile size in luma samples
	tileW := (fh.miColStarts[imin(1, fh.tileCols)] - fh.miColStarts[0]) * 4
	tileH := (fh.miRowStarts[imin(1, fh.tileRows)] - fh.miRowStarts[0]) * 4
	for i := 1; i < fh.tileCols-1; i++ {
		if (fh.miColStarts[i+1]-fh.miColStarts[i])*4 != tileW {
			return ErrUnsupported
		}
	}
	for i := 1; i < fh.tileRows-1; i++ {
		if (fh.miRowStarts[i+1]-fh.miRowStarts[i])*4 != tileH {
			return ErrUnsupported
		}
	}
	subX, subY := f.subX, f.subY
	outW, outH := wTiles*tileW, hTiles*tileH
	pic := &Picture{
		Width: outW, Height: outH, BitDepth: f.bitDepth, SubX: subX, SubY: subY, Mono: d.seq.mono,
		RenderWidth: outW, RenderHeight: outH,
	}
	s := d.seq
	pic.ColorPrimaries, pic.TransferCharacteristics, pic.MatrixCoefficients = s.colorPrimaries, s.transferChar, s.matrixCoeffs
	pic.FullRange = s.colorRange == 1
	for p := 0; p < f.numPlanes; p++ {
		pw, ph := outW, outH
		if p > 0 {
			pw, ph = (outW+subX)>>uint(subX), (outH+subY)>>uint(subY)
		}
		pic.Planes[p] = make([]uint16, pw*ph)
		pic.Stride[p] = pw
	}
	pos := 4
	for tile := 0; tile < count; tile++ {
		if pos+5 > len(payload) {
			return errBitstream
		}
		anchorIdx := int(payload[pos])
		tileRow := int(payload[pos+1])
		tileCol := int(payload[pos+2])
		size := (int(payload[pos+3])<<8 | int(payload[pos+4])) + 1
		pos += 5
		if pos+size > len(payload) || anchorIdx >= len(d.anchors) || tileRow >= fh.tileRows || tileCol >= fh.tileCols {
			return errBitstream
		}
		data := payload[pos : pos+size]
		pos += size
		a := d.anchors[anchorIdx]
		if a.upscaledWidth != fh.frameWidth || a.frameHeight != fh.frameHeight || a.bitDepth != f.bitDepth ||
			a.subX != subX || a.subY != subY || a.buf == nil {
			return errRefMismatch
		}
		// fresh decoding state for every tile (stale mode info of an earlier
		// tile would otherwise be visible to blocks not decoded yet)
		if err := d.setupFrame(fh); err != nil {
			return err
		}
		f = d.fd
		d.fd = nil
		for i := 0; i < refsPerFrame; i++ {
			rf := lastFrame + i
			f.refBufs[rf] = a
			f.refScaleX[rf], f.refScaleY[rf] = 1<<14, 1<<14
		}
		f.largeScale = true
		if err := f.decodeTile(data, 0, tileRow, tileCol); err != nil {
			return err
		}
		// copy the tile (without any post filtering) into the output frame
		destX := tileW * (tile % wTiles)
		destY := tileH * (tile / wTiles)
		x0, y0 := f.miColStart*4, f.miRowStart*4
		w := imin((f.miColEnd-f.miColStart)*4, tileW)
		h := imin((f.miRowEnd-f.miRowStart)*4, tileH)
		for p := 0; p < f.numPlanes; p++ {
			sx, sy := 0, 0
			if p > 0 {
				sx, sy = subX, subY
			}
			pw, ph := (w+sx)>>uint(sx), (h+sy)>>uint(sy)
			for y := 0; y < ph; y++ {
				srcRow := f.cur.pl[p][((y0>>uint(sy))+y)*f.cur.stride[p]+(x0>>uint(sx)):]
				dstRow := pic.Planes[p][((destY>>uint(sy))+y)*pic.Stride[p]+(destX>>uint(sx)):]
				copy(dstRow[:pw], srcRow[:pw])
			}
		}
	}
	d.out = append(d.out, pic)
	return nil
}
