package av1

// ---- tile level (spec 5.11.2) ----------------------------------------------

func (f *frameDec) decodeTile(data []byte, tileNum, tileRow, tileCol int) error {
	fh := f.fh
	f.miRowStart = fh.miRowStarts[tileRow]
	f.miRowEnd = fh.miRowStarts[tileRow+1]
	f.miColStart = fh.miColStarts[tileCol]
	f.miColEnd = fh.miColStarts[tileCol+1]
	f.currentQIndex = fh.baseQIdx
	f.tcdf = f.cdf
	f.sym.init(data, fh.disableCdfUpd || f.largeScale)
	// clear_above_context
	for p := 0; p < 3; p++ {
		clear(f.aboveLevelCtx[p])
		clear(f.aboveDcCtx[p])
	}
	clear(f.aboveSegPred)
	for i := range f.deltaLF {
		f.deltaLF[i] = 0
	}
	for p := 0; p < f.numPlanes; p++ {
		for pass := 0; pass < 2; pass++ {
			f.refSgrXqd[p][pass] = sgrprojXqdMid[pass]
			for i := 0; i < 3; i++ {
				f.refLrWiener[p][pass][i] = wienerTapsMid[i]
			}
		}
	}
	sbSize := block64x64
	if f.seq.use128 {
		sbSize = block128x128
	}
	sbSize4 := num4x4BlocksWide[sbSize]
	for r := f.miRowStart; r < f.miRowEnd; r += sbSize4 {
		// clear_left_context
		for p := 0; p < 3; p++ {
			clear(f.leftLevelCtx[p])
			clear(f.leftDcCtx[p])
		}
		clear(f.leftSegPred)
		for c := f.miColStart; c < f.miColEnd; c += sbSize4 {
			f.readDeltas = fh.deltaQPresent
			if !f.largeScale {
				f.clearCdef(r, c)
			}
			f.clearBlockDecodedFlags(r, c, sbSize4)
			if !f.largeScale {
				f.readLr(r, c, sbSize)
			}
			f.decodePartition(r, c, sbSize)
		}
	}
	if !fh.disableFrameEndUpd && tileNum == fh.ctxUpdateTileID {
		saved := f.tcdf
		f.savedCdf = &saved
	}
	return nil
}

func (f *frameDec) clearCdef(r, c int) {
	set := func(rr, cc int) {
		if rr>>4 < (f.miRows+15)>>4 && cc>>4 < f.cdefStride {
			f.cdefIdx[(rr>>4)*f.cdefStride+(cc>>4)] = -1
		}
	}
	set(r, c)
	if f.seq.use128 {
		set(r, c+16)
		set(r+16, c)
		set(r+16, c+16)
	}
}

func (f *frameDec) clearBlockDecodedFlags(r, c, sbSize4 int) {
	for plane := 0; plane < f.numPlanes; plane++ {
		subX, subY := 0, 0
		if plane > 0 {
			subX, subY = f.subX, f.subY
		}
		sbWidth4 := (f.miColEnd - c) >> uint(subX)
		sbHeight4 := (f.miRowEnd - r) >> uint(subY)
		bd := &f.blockDecoded[plane]
		for y := -1; y <= (sbSize4 >> uint(subY)); y++ {
			for x := -1; x <= (sbSize4 >> uint(subX)); x++ {
				v := false
				if y < 0 && x < sbWidth4 {
					v = true
				} else if x < 0 && y < sbHeight4 {
					v = true
				}
				bd[y+1][x+1] = v
			}
		}
		bd[(sbSize4>>uint(subY))+1][0] = false
	}
}

func (f *frameDec) isInside(r, c int) bool {
	return c >= f.miColStart && c < f.miColEnd && r >= f.miRowStart && r < f.miRowEnd
}

func (f *frameDec) miAt(r, c int) *miInfo { return &f.mi[r*f.miCols+c] }

// ---- partition (spec 5.11.4) -------------------------------------------------

func (f *frameDec) decodePartition(r, c, bSize int) {
	if r >= f.miRows || c >= f.miCols {
		return
	}
	availU := f.isInside(r-1, c)
	availL := f.isInside(r, c-1)
	num4x4 := num4x4BlocksWide[bSize]
	half := num4x4 >> 1
	quarter := half >> 1
	hasRows := (r + half) < f.miRows
	hasCols := (c + half) < f.miCols
	var partition int
	if bSize < block8x8 {
		partition = partitionNone
	} else {
		bsl := miWidthLog2[bSize]
		ctx := 0
		if availU && miWidthLog2[f.miAt(r-1, c).miSize] < bsl {
			ctx++
		}
		if availL && miHeightLog2[f.miAt(r, c-1).miSize] < bsl {
			ctx += 2
		}
		var pcdf []uint16
		switch bsl {
		case 1:
			pcdf = f.tcdf.partitionW8[ctx][:]
		case 2:
			pcdf = f.tcdf.partitionW16[ctx][:]
		case 3:
			pcdf = f.tcdf.partitionW32[ctx][:]
		case 4:
			pcdf = f.tcdf.partitionW64[ctx][:]
		default:
			pcdf = f.tcdf.partitionW128[ctx][:]
		}
		if hasRows && hasCols {
			partition = f.sym.symbol(pcdf)
		} else if hasCols {
			// split_or_horz
			pr := func(i int) int { return int(pcdf[i]) - int(pcdf[i-1]) }
			psum := pr(partitionVert) + pr(partitionSplit)
			if bSize != block8x8 {
				psum += pr(partitionHorzA) + pr(partitionVertA) + pr(partitionVertB)
				if bSize != block128x128 {
					psum += pr(partitionVert4)
				}
			}
			tmp := [3]uint16{uint16(32768 - psum), 32768, 0}
			if f.sym.symbolTmp(tmp[:]) != 0 {
				partition = partitionSplit
			} else {
				partition = partitionHorz
			}
		} else if hasRows {
			pr := func(i int) int { return int(pcdf[i]) - int(pcdf[i-1]) }
			psum := pr(partitionHorz) + pr(partitionSplit)
			if bSize != block8x8 {
				psum += pr(partitionHorzA) + pr(partitionHorzB) + pr(partitionVertA)
				if bSize != block128x128 {
					psum += pr(partitionHorz4)
				}
			}
			tmp := [3]uint16{uint16(32768 - psum), 32768, 0}
			if f.sym.symbolTmp(tmp[:]) != 0 {
				partition = partitionSplit
			} else {
				partition = partitionVert
			}
		} else {
			partition = partitionSplit
		}
	}
	subSize := partitionSubsize[partition][bSize]
	splitSize := partitionSubsize[partitionSplit][bSize]
	switch partition {
	case partitionNone:
		f.decodeBlock(r, c, subSize)
	case partitionHorz:
		f.decodeBlock(r, c, subSize)
		if hasRows {
			f.decodeBlock(r+half, c, subSize)
		}
	case partitionVert:
		f.decodeBlock(r, c, subSize)
		if hasCols {
			f.decodeBlock(r, c+half, subSize)
		}
	case partitionSplit:
		f.decodePartition(r, c, subSize)
		f.decodePartition(r, c+half, subSize)
		f.decodePartition(r+half, c, subSize)
		f.decodePartition(r+half, c+half, subSize)
	case partitionHorzA:
		f.decodeBlock(r, c, splitSize)
		f.decodeBlock(r, c+half, splitSize)
		f.decodeBlock(r+half, c, subSize)
	case partitionHorzB:
		f.decodeBlock(r, c, subSize)
		f.decodeBlock(r+half, c, splitSize)
		f.decodeBlock(r+half, c+half, splitSize)
	case partitionVertA:
		f.decodeBlock(r, c, splitSize)
		f.decodeBlock(r+half, c, splitSize)
		f.decodeBlock(r, c+half, subSize)
	case partitionVertB:
		f.decodeBlock(r, c, subSize)
		f.decodeBlock(r, c+half, splitSize)
		f.decodeBlock(r+half, c+half, splitSize)
	case partitionHorz4:
		f.decodeBlock(r, c, subSize)
		f.decodeBlock(r+quarter, c, subSize)
		f.decodeBlock(r+quarter*2, c, subSize)
		if r+quarter*3 < f.miRows {
			f.decodeBlock(r+quarter*3, c, subSize)
		}
	default:
		f.decodeBlock(r, c, subSize)
		f.decodeBlock(r, c+quarter, subSize)
		f.decodeBlock(r, c+quarter*2, subSize)
		if c+quarter*3 < f.miCols {
			f.decodeBlock(r, c+quarter*3, subSize)
		}
	}
}
