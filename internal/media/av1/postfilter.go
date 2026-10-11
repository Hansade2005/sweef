package av1

// postFilter runs the in-loop filters of the decode frame wrapup process
// (spec 7.4) and returns the final (LrFrame) buffer.
func (f *frameDec) postFilter() *frameBuf {
	fh := f.fh
	if fh.lfLevel[0] != 0 || fh.lfLevel[1] != 0 {
		cov(covLoopFilter)
		f.loopFilter()
	}
	cdef := f.cdefProcess()
	upCdef := f.upscaleFrame(cdef)
	upCur := upCdef
	if fh.usesLr && cdef != f.cur {
		upCur = f.upscaleFrame(f.cur)
	}
	return f.loopRestoration(upCur, upCdef)
}

// output emits a picture for the output process (spec 7.18).
func (d *Decoder) output(rf *refFrame, film *filmGrain) {
	s := d.seq
	pic := &Picture{
		Width:    rf.upscaledWidth,
		Height:   rf.frameHeight,
		BitDepth: rf.bitDepth,
		SubX:     rf.subX,
		SubY:     rf.subY,
		Mono:     rf.mono,
	}
	pic.RenderWidth, pic.RenderHeight = rf.renderWidth, rf.renderHeight
	if s != nil {
		pic.ColorPrimaries = s.colorPrimaries
		pic.TransferCharacteristics = s.transferChar
		pic.MatrixCoefficients = s.matrixCoeffs
		pic.FullRange = s.colorRange == 1
	}
	n := 3
	if rf.mono {
		n = 1
	}
	for p := 0; p < n; p++ {
		pic.Planes[p] = rf.buf.pl[p]
		pic.Stride[p] = rf.buf.stride[p]
	}
	if s != nil && s.filmGrainPresent && film != nil && film.apply {
		pic = applyFilmGrain(pic, film)
	}
	d.out = append(d.out, pic)
}
