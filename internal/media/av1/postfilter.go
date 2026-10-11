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
	pic.SpatialID, pic.TemporalID = rf.spatialID, rf.temporalID
	d.emit(pic)
}

// emit applies the spatial layer output rule of dav1d (all_layers off): with
// several spatial layers in the operating point only the highest layer that
// is present is output for each temporal unit.
func (d *Decoder) emit(pic *Picture) {
	if d.AllLayers || d.maxSpatialID() == 0 {
		d.out = append(d.out, pic)
		d.newTU = false
		return
	}
	if d.cache != nil && (d.cache.SpatialID == d.maxSpatialID() || d.newTU) {
		d.out = append(d.out, d.cache)
	}
	d.cache = pic
	d.newTU = false
}

// endTemporalUnit outputs the picture held back by emit at the end of the
// data handed to Decode.
func (d *Decoder) endTemporalUnit() {
	if d.cache != nil {
		d.out = append(d.out, d.cache)
		d.cache = nil
	}
}
