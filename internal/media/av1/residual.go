package av1

// ---- residual (spec 5.11.34 - 5.11.41, 5.11.47, 5.11.48) -------------------------

const (
	txSetDctOnly = 0
	txSetIntra1  = 1
	txSetIntra2  = 2
	txSetInter1  = 1
	txSetInter2  = 2
	txSetInter3  = 3
)

func (f *frameDec) getTxSet(txSz int) int {
	txSzSqr := txSizeSqr[txSz]
	txSzSqrUp := txSizeSqrUp[txSz]
	if txSzSqrUp > tx32x32 {
		return txSetDctOnly
	}
	if f.isInter {
		if f.fh.reducedTxSet || txSzSqrUp == tx32x32 {
			return txSetInter3
		} else if txSzSqr == tx16x16 {
			return txSetInter2
		}
		return txSetInter1
	}
	if txSzSqrUp == tx32x32 {
		return txSetDctOnly
	} else if f.fh.reducedTxSet {
		return txSetIntra2
	} else if txSzSqr == tx16x16 {
		return txSetIntra2
	}
	return txSetIntra1
}

func (f *frameDec) getTxSize(plane, txSz int) int {
	if plane == 0 {
		return txSz
	}
	uvTx := maxTxSizeRect[getPlaneResidualSize(f.miSize, plane, f.subX, f.subY)]
	if txWidth[uvTx] == 64 || txHeight[uvTx] == 64 {
		if txWidth[uvTx] == 16 {
			return tx16x32
		}
		if txHeight[uvTx] == 16 {
			return tx32x16
		}
		return tx32x32
	}
	return uvTx
}

func (f *frameDec) residual() {
	widthChunks := imax(1, blockWidth(f.miSize)>>6)
	heightChunks := imax(1, blockHeight(f.miSize)>>6)
	miSizeChunk := f.miSize
	if widthChunks > 1 || heightChunks > 1 {
		miSizeChunk = block64x64
	}
	nPlanes := 1
	if f.hasChroma {
		nPlanes = 3
	}
	for chunkY := 0; chunkY < heightChunks; chunkY++ {
		for chunkX := 0; chunkX < widthChunks; chunkX++ {
			miRowChunk := f.miRow + (chunkY << 4)
			miColChunk := f.miCol + (chunkX << 4)
			for plane := 0; plane < nPlanes; plane++ {
				txSz := tx4x4
				if !f.lossless {
					txSz = f.getTxSize(plane, f.txSize)
				}
				stepX := txWidth[txSz] >> 2
				stepY := txHeight[txSz] >> 2
				planeSz := getPlaneResidualSize(miSizeChunk, plane, f.subX, f.subY)
				num4x4W := num4x4BlocksWide[planeSz]
				num4x4H := num4x4BlocksHigh[planeSz]
				subX, subY := 0, 0
				if plane > 0 {
					subX, subY = f.subX, f.subY
				}
				baseX := (miColChunk >> uint(subX)) * 4
				baseY := (miRowChunk >> uint(subY)) * 4
				if f.isInter && !f.lossless && plane == 0 {
					f.transformTree(baseX, baseY, num4x4W*4, num4x4H*4)
				} else {
					baseXBlock := (f.miCol >> uint(subX)) * 4
					baseYBlock := (f.miRow >> uint(subY)) * 4
					for y := 0; y < num4x4H; y += stepY {
						for x := 0; x < num4x4W; x += stepX {
							f.transformBlock(plane, baseXBlock, baseYBlock, txSz,
								x+((chunkX<<4)>>uint(subX)), y+((chunkY<<4)>>uint(subY)))
						}
					}
				}
			}
		}
	}
}

func (f *frameDec) transformTree(startX, startY, w, h int) {
	maxX := f.miCols * 4
	maxY := f.miRows * 4
	if startX >= maxX || startY >= maxY {
		return
	}
	row := startY >> 2
	col := startX >> 2
	lumaTxSz := int(f.miAt(row, col).interTxSize)
	lumaW := txWidth[lumaTxSz]
	lumaH := txHeight[lumaTxSz]
	if w <= lumaW && h <= lumaH {
		txSz := findTxSize(w, h)
		f.transformBlock(0, startX, startY, txSz, 0, 0)
	} else if w > h {
		f.transformTree(startX, startY, w/2, h)
		f.transformTree(startX+w/2, startY, w/2, h)
	} else if w < h {
		f.transformTree(startX, startY, w, h/2)
		f.transformTree(startX, startY+h/2, w, h/2)
	} else {
		f.transformTree(startX, startY, w/2, h/2)
		f.transformTree(startX+w/2, startY, w/2, h/2)
		f.transformTree(startX, startY+h/2, w/2, h/2)
		f.transformTree(startX+w/2, startY+h/2, w/2, h/2)
	}
}

func (f *frameDec) transformBlock(plane, baseX, baseY, txSz, x, y int) {
	startX := baseX + 4*x
	startY := baseY + 4*y
	subX, subY := 0, 0
	if plane > 0 {
		subX, subY = f.subX, f.subY
	}
	row := (startY << uint(subY)) >> 2
	col := (startX << uint(subX)) >> 2
	sbMask := f.sbMask
	subBlockMiRow := row & sbMask
	subBlockMiCol := col & sbMask
	stepX := txWidth[txSz] >> 2
	stepY := txHeight[txSz] >> 2
	maxX := (f.miCols * 4) >> uint(subX)
	maxY := (f.miRows * 4) >> uint(subY)
	if startX >= maxX || startY >= maxY {
		return
	}
	bd := &f.blockDecoded[plane]
	if !f.isInter {
		if (plane == 0 && f.paletteSizeY != 0) || (plane != 0 && f.paletteSizeUV != 0) {
			f.predictPalette(plane, startX, startY, x, y, txSz)
		} else {
			isCfl := plane > 0 && f.uvMode == uvCflPred
			mode := f.yMode
			if plane > 0 {
				if isCfl {
					mode = dcPred
				} else {
					mode = f.uvMode
				}
			}
			log2W := txWidthLog2[txSz]
			log2H := txHeightLog2[txSz]
			haveLeft := f.availL
			haveAbove := f.availU
			if plane > 0 {
				haveLeft = f.availLChroma
				haveAbove = f.availUChroma
			}
			f.predictIntra(plane, startX, startY,
				haveLeft || x > 0,
				haveAbove || y > 0,
				bd[(subBlockMiRow>>uint(subY))-1+1][(subBlockMiCol>>uint(subX))+stepX+1],
				bd[(subBlockMiRow>>uint(subY))+stepY+1][(subBlockMiCol>>uint(subX))-1+1],
				mode, log2W, log2H)
			if isCfl {
				cov(covCfl)
				f.predictChromaFromLuma(plane, startX, startY, txSz)
			}
		}
		if plane == 0 {
			f.maxLumaW = startX + stepX*4
			f.maxLumaH = startY + stepY*4
		}
	}
	if !f.skip {
		eob := f.coeffs(plane, startX, startY, txSz)
		if eob > 0 {
			f.reconstruct(plane, startX, startY, txSz, eob)
		}
	}
	lf := f.lfTxSize[plane]
	for i := 0; i < stepY; i++ {
		rr := (row >> uint(subY)) + i
		if rr >= f.miRows {
			break
		}
		for j := 0; j < stepX; j++ {
			cc := (col >> uint(subX)) + j
			if cc < f.miCols {
				lf[rr*f.miCols+cc] = uint8(txSz)
			}
		}
		for j := 0; j < stepX; j++ {
			bd[(subBlockMiRow>>uint(subY))+i+1][(subBlockMiCol>>uint(subX))+j+1] = true
		}
	}
}

func (f *frameDec) getScan(txSz int) []uint16 {
	if txSz == tx16x64 {
		return defaultScan16x32[:]
	}
	if txSz == tx64x16 {
		return defaultScan32x16[:]
	}
	if txSizeSqrUp[txSz] == tx64x64 {
		return defaultScan32x32[:]
	}
	t := f.planeTxType
	if t == idtx {
		return defaultScanFor(txSz)
	}
	preferRow := t == vDct || t == vAdst || t == vFlipadst
	preferCol := t == hDct || t == hAdst || t == hFlipadst
	if preferRow {
		return mrowScanFor(txSz)
	} else if preferCol {
		return mcolScanFor(txSz)
	}
	return defaultScanFor(txSz)
}

func mrowScanFor(txSz int) []uint16 {
	switch txSz {
	case tx4x4:
		return mrowScan4x4[:]
	case tx4x8:
		return mrowScan4x8[:]
	case tx8x4:
		return mrowScan8x4[:]
	case tx8x8:
		return mrowScan8x8[:]
	case tx8x16:
		return mrowScan8x16[:]
	case tx16x8:
		return mrowScan16x8[:]
	case tx16x16:
		return mrowScan16x16[:]
	case tx4x16:
		return mrowScan4x16[:]
	}
	return mrowScan16x4[:]
}

func mcolScanFor(txSz int) []uint16 {
	switch txSz {
	case tx4x4:
		return mcolScan4x4[:]
	case tx4x8:
		return mcolScan4x8[:]
	case tx8x4:
		return mcolScan8x4[:]
	case tx8x8:
		return mcolScan8x8[:]
	case tx8x16:
		return mcolScan8x16[:]
	case tx16x8:
		return mcolScan16x8[:]
	case tx16x16:
		return mcolScan16x16[:]
	case tx4x16:
		return mcolScan4x16[:]
	}
	return mcolScan16x4[:]
}

func defaultScanFor(txSz int) []uint16 {
	switch txSz {
	case tx4x4:
		return defaultScan4x4[:]
	case tx4x8:
		return defaultScan4x8[:]
	case tx8x4:
		return defaultScan8x4[:]
	case tx8x8:
		return defaultScan8x8[:]
	case tx8x16:
		return defaultScan8x16[:]
	case tx16x8:
		return defaultScan16x8[:]
	case tx16x16:
		return defaultScan16x16[:]
	case tx16x32:
		return defaultScan16x32[:]
	case tx32x16:
		return defaultScan32x16[:]
	case tx4x16:
		return defaultScan4x16[:]
	case tx16x4:
		return defaultScan16x4[:]
	case tx8x32:
		return defaultScan8x32[:]
	case tx32x8:
		return defaultScan32x8[:]
	}
	return defaultScan32x32[:]
}

func getTxClass(t int) int {
	switch t {
	case vDct, vAdst, vFlipadst:
		return 2 // TX_CLASS_VERT
	case hDct, hAdst, hFlipadst:
		return 1 // TX_CLASS_HORIZ
	}
	return 0
}

func (f *frameDec) isTxTypeInSet(txSet, txType int) bool {
	if f.isInter {
		return txTypeInSetInter[txSet][txType] != 0
	}
	return txTypeInSetIntra[txSet][txType] != 0
}

func (f *frameDec) computeTxType(plane, txSz, blockX, blockY int) int {
	txSzSqrUp := txSizeSqrUp[txSz]
	if f.lossless || txSzSqrUp > tx32x32 {
		return dctDct
	}
	txSet := f.getTxSet(txSz)
	if plane == 0 {
		return int(f.txTypes[blockY*f.miCols+blockX])
	}
	if f.isInter {
		x4 := imax(f.miCol, blockX<<uint(f.subX))
		y4 := imax(f.miRow, blockY<<uint(f.subY))
		t := int(f.txTypes[y4*f.miCols+x4])
		if !f.isTxTypeInSet(txSet, t) {
			return dctDct
		}
		return t
	}
	t := modeToTxfm[f.uvMode]
	if !f.isTxTypeInSet(txSet, t) {
		return dctDct
	}
	return t
}

func (f *frameDec) transformType(x4, y4, txSz int) {
	set := f.getTxSet(txSz)
	qidx := f.fh.baseQIdx
	if f.fh.segEnabled {
		qidx = f.fh.getQIndex(true, f.segmentID, 0)
	}
	var txType int
	if set > 0 && qidx > 0 {
		if f.isInter {
			switch set {
			case txSetInter1:
				txType = txTypeInterInvSet1[f.sym.symbol(f.tcdf.interTxTypeSet1[txSizeSqr[txSz]][:])]
			case txSetInter2:
				txType = txTypeInterInvSet2[f.sym.symbol(f.tcdf.interTxTypeSet2[:])]
			default:
				txType = txTypeInterInvSet3[f.sym.symbol(f.tcdf.interTxTypeSet3[txSizeSqr[txSz]][:])]
			}
		} else {
			intraDir := f.yMode
			if f.useFilterIntra {
				intraDir = filterIntraModeToIntraDir[f.filterIntraMode]
			}
			if set == txSetIntra1 {
				txType = txTypeIntraInvSet1[f.sym.symbol(f.tcdf.intraTxTypeSet1[txSizeSqr[txSz]][intraDir][:])]
			} else {
				txType = txTypeIntraInvSet2[f.sym.symbol(f.tcdf.intraTxTypeSet2[txSizeSqr[txSz]][intraDir][:])]
			}
		}
	} else {
		txType = dctDct
	}
	w4 := txWidth[txSz] >> 2
	h4 := txHeight[txSz] >> 2
	for j := 0; j < h4; j++ {
		if y4+j >= f.miRows {
			break
		}
		for i := 0; i < w4; i++ {
			if x4+i < f.miCols {
				f.txTypes[(y4+j)*f.miCols+x4+i] = uint8(txType)
			}
		}
	}
}

func (f *frameDec) coeffs(plane, startX, startY, txSz int) int {
	x4 := startX >> 2
	y4 := startY >> 2
	w4 := txWidth[txSz] >> 2
	h4 := txHeight[txSz] >> 2
	txSzCtx := (txSizeSqr[txSz] + txSizeSqrUp[txSz] + 1) >> 1
	ptype := b2i(plane > 0)
	segEob := imin(1024, txWidth[txSz]*txHeight[txSz])
	if txSz == tx16x64 || txSz == tx64x16 {
		segEob = 512
	}
	quant := f.quant[:segEob]
	for i := range quant {
		quant[i] = 0
	}
	eob := 0
	culLevel := 0
	dcCategory := 0

	// all_zero context
	maxX4 := f.miCols
	maxY4 := f.miRows
	if plane > 0 {
		maxX4 >>= uint(f.subX)
		maxY4 >>= uint(f.subY)
	}
	w := txWidth[txSz]
	h := txHeight[txSz]
	bsize := getPlaneResidualSize(f.miSize, plane, f.subX, f.subY)
	bw := blockWidth(bsize)
	bh := blockHeight(bsize)
	var ctx int
	aboveL := f.aboveLevelCtx[plane]
	leftL := f.leftLevelCtx[plane]
	aboveD := f.aboveDcCtx[plane]
	leftD := f.leftDcCtx[plane]
	if plane == 0 {
		top, left := 0, 0
		for k := 0; k < w4; k++ {
			if x4+k < maxX4 {
				top = imax(top, int(aboveL[x4+k]))
			}
		}
		for k := 0; k < h4; k++ {
			if y4+k < maxY4 {
				left = imax(left, int(leftL[y4+k]))
			}
		}
		top = imin(top, 255)
		left = imin(left, 255)
		if bw == w && bh == h {
			ctx = 0
		} else if top == 0 && left == 0 {
			ctx = 1
		} else if top == 0 || left == 0 {
			ctx = 2 + b2i(imax(top, left) > 3)
		} else if imax(top, left) <= 3 {
			ctx = 4
		} else if imin(top, left) <= 3 {
			ctx = 5
		} else {
			ctx = 6
		}
	} else {
		above, left := 0, 0
		for i := 0; i < w4; i++ {
			if x4+i < maxX4 {
				above |= int(aboveL[x4+i]) | int(aboveD[x4+i])
			}
		}
		for i := 0; i < h4; i++ {
			if y4+i < maxY4 {
				left |= int(leftL[y4+i]) | int(leftD[y4+i])
			}
		}
		ctx = b2i(above != 0) + b2i(left != 0)
		ctx += 7
		if bw*bh > w*h {
			ctx += 3
		}
	}
	allZero := f.sym.symbol(f.tcdf.txbSkip[txSzCtx][ctx][:]) != 0
	f.nzRows = 0
	if allZero {
		if plane == 0 {
			for i := 0; i < w4; i++ {
				for j := 0; j < h4; j++ {
					if y4+j < f.miRows && x4+i < f.miCols {
						f.txTypes[(y4+j)*f.miCols+x4+i] = dctDct
					}
				}
			}
		}
	} else {
		if plane == 0 {
			f.transformType(x4, y4, txSz)
		}
		f.planeTxType = f.computeTxType(plane, txSz, x4, y4)
		scan := f.getScan(txSz)
		txClass := getTxClass(f.planeTxType)
		eobMultisize := imin(txWidthLog2[txSz], 5) + imin(txHeightLog2[txSz], 5) - 4
		ectx := 0
		if txClass != 0 {
			ectx = 1
		}
		var eobPt int
		switch eobMultisize {
		case 0:
			eobPt = f.sym.symbol(f.tcdf.eobPt16[ptype][ectx][:]) + 1
		case 1:
			eobPt = f.sym.symbol(f.tcdf.eobPt32[ptype][ectx][:]) + 1
		case 2:
			eobPt = f.sym.symbol(f.tcdf.eobPt64[ptype][ectx][:]) + 1
		case 3:
			eobPt = f.sym.symbol(f.tcdf.eobPt128[ptype][ectx][:]) + 1
		case 4:
			eobPt = f.sym.symbol(f.tcdf.eobPt256[ptype][ectx][:]) + 1
		case 5:
			eobPt = f.sym.symbol(f.tcdf.eobPt512[ptype][:]) + 1
		default:
			eobPt = f.sym.symbol(f.tcdf.eobPt1024[ptype][:]) + 1
		}
		if eobPt < 2 {
			eob = eobPt
		} else {
			eob = (1 << uint(eobPt-2)) + 1
		}
		eobShift := imax(-1, eobPt-3)
		if eobShift >= 0 {
			if f.sym.symbol(f.tcdf.eobExtra[txSzCtx][ptype][eobPt-3][:]) != 0 {
				eob += 1 << uint(eobShift)
			}
			for i := 1; i < imax(0, eobPt-2); i++ {
				eobShift = imax(0, eobPt-2) - 1 - i
				if f.sym.literal(1) != 0 {
					eob += 1 << uint(eobShift)
				}
			}
		}
		adjTxSz := adjustedTxSize[txSz]
		bwl := txWidthLog2[adjTxSz]
		width := 1 << uint(bwl)
		height := txHeight[adjTxSz]
		var coefBaseCdfs *[42][5]uint16 = &f.tcdf.coeffBase[txSzCtx][ptype]
		var coefBrCdfs *[21][5]uint16 = &f.tcdf.coeffBr[imin(txSzCtx, 3)][ptype]
		for c := eob - 1; c >= 0; c-- {
			pos := int(scan[c])
			row := pos >> uint(bwl)
			col := pos - (row << uint(bwl))
			var level int
			if c == eob-1 {
				// coeff_base_eob context
				var cctx int
				if c == 0 {
					cctx = 0
				} else if c <= (height<<uint(bwl))/8 {
					cctx = 1
				} else if c <= (height<<uint(bwl))/4 {
					cctx = 2
				} else {
					cctx = 3
				}
				level = f.sym.symbol(f.tcdf.coeffBaseEob[txSzCtx][ptype][cctx][:]) + 1
			} else {
				// coeff_base context
				mag := 0
				for idx := 0; idx < 5; idx++ {
					refRow := row + sigRefDiffOffset[txClass][idx][0]
					refCol := col + sigRefDiffOffset[txClass][idx][1]
					if refRow >= 0 && refCol >= 0 && refRow < height && refCol < width {
						q := int(quant[(refRow<<uint(bwl))+refCol])
						if q > 3 {
							q = 3
						}
						mag += q
					}
				}
				cctx := imin((mag+1)>>1, 4)
				if txClass == 0 {
					if row == 0 && col == 0 {
						cctx = 0
					} else {
						cctx += coeffBaseCtxOffset[txSz][imin(row, 4)][imin(col, 4)]
					}
				} else {
					idx := col
					if txClass == 2 {
						idx = row
					}
					cctx += coeffBasePosCtxOffset[imin(idx, 2)]
				}
				level = f.sym.symbol(coefBaseCdfs[cctx][:])
			}
			if level > 2 {
				// coeff_br context
				mag := 0
				for idx := 0; idx < 3; idx++ {
					refRow := row + magRefOffsetWithTxClass[txClass][idx][0]
					refCol := col + magRefOffsetWithTxClass[txClass][idx][1]
					if refRow >= 0 && refCol >= 0 && refRow < height && refCol < width {
						q := int(quant[refRow*width+refCol])
						if q > 15 {
							q = 15
						}
						mag += q
					}
				}
				mag = imin((mag+1)>>1, 6)
				var bctx int
				if pos == 0 {
					bctx = mag
				} else if txClass == 0 {
					if row < 2 && col < 2 {
						bctx = mag + 7
					} else {
						bctx = mag + 14
					}
				} else if txClass == 1 {
					if col == 0 {
						bctx = mag + 7
					} else {
						bctx = mag + 14
					}
				} else {
					if row == 0 {
						bctx = mag + 7
					} else {
						bctx = mag + 14
					}
				}
				for idx := 0; idx < 4; idx++ {
					br := f.sym.symbol(coefBrCdfs[bctx][:])
					level += br
					if br < 3 {
						break
					}
				}
			}
			quant[pos] = int32(level)
		}
		nz := 0
		for c := 0; c < eob; c++ {
			pos := int(scan[c])
			q := quant[pos]
			var sign int
			if q != 0 {
				if c == 0 {
					// dc_sign context
					dcSign := 0
					for k := 0; k < w4; k++ {
						if x4+k < maxX4 {
							s := aboveD[x4+k]
							if s == 1 {
								dcSign--
							} else if s == 2 {
								dcSign++
							}
						}
					}
					for k := 0; k < h4; k++ {
						if y4+k < maxY4 {
							s := leftD[y4+k]
							if s == 1 {
								dcSign--
							} else if s == 2 {
								dcSign++
							}
						}
					}
					dctx := 0
					if dcSign < 0 {
						dctx = 1
					} else if dcSign > 0 {
						dctx = 2
					}
					sign = f.sym.symbol(f.tcdf.dcSign[ptype][dctx][:])
				} else {
					sign = f.sym.literal(1)
				}
				row := pos >> uint(bwl)
				if row+1 > nz {
					nz = row + 1
				}
			}
			if q > 14 {
				length := 0
				for {
					length++
					if f.sym.literal(1) != 0 {
						break
					}
					if length >= 32 {
						break
					}
				}
				x := 1
				for i := length - 2; i >= 0; i-- {
					x = (x << 1) | f.sym.literal(1)
				}
				q = int32(x + 14)
			}
			if pos == 0 && q > 0 {
				if sign != 0 {
					dcCategory = 1
				} else {
					dcCategory = 2
				}
			}
			q &= 0xFFFFF
			culLevel += int(q)
			if sign != 0 {
				q = -q
			}
			quant[pos] = q
		}
		culLevel = imin(63, culLevel)
		f.nzRows = nz
	}
	for i := 0; i < w4; i++ {
		if x4+i < len(aboveL) {
			aboveL[x4+i] = uint8(culLevel)
			aboveD[x4+i] = uint8(dcCategory)
		}
	}
	for i := 0; i < h4; i++ {
		if y4+i < len(leftL) {
			leftL[y4+i] = uint8(culLevel)
			leftD[y4+i] = uint8(dcCategory)
		}
	}
	return eob
}

// reconstruct implements the reconstruction and dequantization process (7.12.3).
func (f *frameDec) reconstruct(plane, x, y, txSz, eob int) {
	fh := f.fh
	if txSizeSqrUp[txSz] == tx64x64 {
		cov(covTx64)
	}
	if f.planeTxType == idtx {
		cov(covIdtx)
	}
	if fh.usingQmatrix {
		cov(covQmatrix)
	}
	bd := f.bitDepth
	dqDenom := int64(1)
	switch txSz {
	case tx32x32, tx16x32, tx32x16, tx16x64, tx64x16:
		dqDenom = 2
	case tx64x64, tx32x64, tx64x32:
		dqDenom = 4
	}
	log2W := txWidthLog2[txSz]
	log2H := txHeightLog2[txSz]
	w := 1 << uint(log2W)
	h := 1 << uint(log2H)
	tw := imin(32, w)
	th := imin(32, h)
	t := f.planeTxType
	flipUD := t == flipadstDct || t == flipadstAdst || t == vFlipadst || t == flipadstFlipadst
	flipLR := t == dctFlipadst || t == adstFlipadst || t == hFlipadst || t == flipadstFlipadst
	qindex := fh.getQIndex(false, f.segmentID, f.currentQIndex)
	tbl := (bd - 8) >> 1
	var dcDelta, acDelta int
	switch plane {
	case 0:
		dcDelta, acDelta = fh.dqYDc, 0
	case 1:
		dcDelta, acDelta = fh.dqUDc, fh.dqUAc
	default:
		dcDelta, acDelta = fh.dqVDc, fh.dqVAc
	}
	dcQ := int64(dcQlookup[tbl][clip3(0, 255, qindex+dcDelta)])
	acQ := int64(acQlookup[tbl][clip3(0, 255, qindex+acDelta)])
	useQm := fh.usingQmatrix && t < idtx && fh.segQMLevel[plane][f.segmentID] < 15
	var qm []uint8
	if useQm {
		qm = quantizerMatrix[fh.segQMLevel[plane][f.segmentID]][b2i(plane > 0)][qmOffset[txSz]:]
	}
	maxV := int64(1)<<uint(7+bd) - 1
	minV := -(int64(1) << uint(7+bd))
	nzRows := f.nzRows
	deq := f.dequant[:tw*th]
	for i := 0; i < nzRows; i++ {
		for j := 0; j < tw; j++ {
			qv := int64(f.quant[i*tw+j])
			if qv == 0 {
				deq[i*tw+j] = 0
				continue
			}
			q := acQ
			if i == 0 && j == 0 {
				q = dcQ
			}
			if useQm {
				q = (q*int64(qm[i*tw+j]) + 16) >> 5
			}
			dq := qv * q
			neg := dq < 0
			if neg {
				dq = -dq
			}
			dq = (dq & 0xFFFFFF) / dqDenom
			if neg {
				dq = -dq
			}
			if dq > maxV {
				dq = maxV
			} else if dq < minV {
				dq = minV
			}
			deq[i*tw+j] = int32(dq)
		}
	}
	res := f.residual2[:w*h]
	inverseTransform2D(deq, txSz, t, f.lossless, bd, nzRows, res)
	pl := f.cur.pl[plane]
	stride := f.cur.stride[plane]
	maxPix := int32(1)<<uint(bd) - 1
	for i := 0; i < h; i++ {
		yy := i
		if flipUD {
			yy = h - i - 1
		}
		dst := pl[(y+yy)*stride+x : (y+yy)*stride+x+w]
		src := res[i*w : i*w+w]
		if flipLR {
			for j := 0; j < w; j++ {
				v := int32(dst[w-j-1]) + src[j]
				if v < 0 {
					v = 0
				} else if v > maxPix {
					v = maxPix
				}
				dst[w-j-1] = uint16(v)
			}
		} else {
			for j := 0; j < w; j++ {
				v := int32(dst[j]) + src[j]
				if v < 0 {
					v = 0
				} else if v > maxPix {
					v = maxPix
				}
				dst[j] = uint16(v)
			}
		}
	}
}
