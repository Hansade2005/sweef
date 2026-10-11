package vp9

// Residual syntax, token decoding and reconstruction (spec 6.4.21 - 6.4.26,
// 8.6).

var pareto8 [256][8]uint8
var scanTabs [4][3][]uint16 // [txSz][0 default,1 row,2 col]
var nbTabs [4][3][]uint16   // neighbour pairs per scan index

func init() {
	for p := 1; p < 256; p++ {
		x := (p - 1) / 2
		for n := 0; n < 8; n++ {
			if p&1 != 0 {
				pareto8[p][n] = paretoTable[x][n]
			} else {
				pareto8[p][n] = uint8((int(paretoTable[x][n]) + int(paretoTable[x+1][n])) >> 1)
			}
		}
	}
	scanTabs[0] = [3][]uint16{defaultScan4x4[:], rowScan4x4[:], colScan4x4[:]}
	scanTabs[1] = [3][]uint16{defaultScan8x8[:], rowScan8x8[:], colScan8x8[:]}
	scanTabs[2] = [3][]uint16{defaultScan16x16[:], rowScan16x16[:], colScan16x16[:]}
	scanTabs[3] = [3][]uint16{defaultScan32x32[:], nil, nil}
	for t := 0; t < 4; t++ {
		n := 4 << uint(t)
		for v := 0; v < 3; v++ {
			sc := scanTabs[t][v]
			if sc == nil {
				continue
			}
			nb := make([]uint16, 2*len(sc))
			for c := 1; c < len(sc); c++ {
				pos := int(sc[c])
				i, j := pos/n, pos%n
				var n0, n1 int
				switch {
				case i > 0 && j > 0:
					a := (i-1)*n + j
					a2 := i*n + j - 1
					switch v {
					case 2: // col scan: DCT_ADST
						n0, n1 = a, a
					case 1: // row scan: ADST_DCT
						n0, n1 = a2, a2
					default:
						n0, n1 = a, a2
					}
				case i > 0:
					n0 = (i-1)*n + j
					n1 = n0
				default:
					n0 = i*n + j - 1
					n1 = n0
				}
				nb[2*c], nb[2*c+1] = uint16(n0), uint16(n1)
			}
			nbTabs[t][v] = nb
		}
	}
}

func (d *Decoder) uvTxSize() int {
	b := &d.blk
	if b.miSize < block8x8 {
		return tx4x4
	}
	uvSize := int(ssSizeLookup[b.miSize][d.subX][d.subY])
	return min(b.txSize, int(maxTxSize[uvSize]))
}

func (d *Decoder) qindex(seg int) int {
	if d.seg.enabled && d.seg.featEnabled[seg][segLvlAltQ] {
		data := d.seg.featData[seg][segLvlAltQ]
		if !d.seg.absDelta {
			data += d.baseQIdx
		}
		return clip3(0, 255, data)
	}
	return d.baseQIdx
}

func (d *Decoder) residual() {
	b := &d.blk
	bsize := b.miSize
	if bsize < block8x8 {
		bsize = block8x8
	}
	qi := d.qindex(b.segID)
	qt := (d.bitDepth - 8) >> 1
	for plane := 0; plane < 3; plane++ {
		txSz := b.txSize
		subX, subY := 0, 0
		if plane > 0 {
			txSz = d.uvTxSize()
			subX, subY = d.subX, d.subY
		}
		step := 1 << uint(txSz)
		planeSz := int(ssSizeLookup[bsize][subX][subY])
		n4w := int(num4x4Wide[planeSz])
		n4h := int(num4x4High[planeSz])
		baseX := (b.miCol * 8) >> uint(subX)
		baseY := (b.miRow * 8) >> uint(subY)
		if b.isInter {
			if b.miSize < block8x8 {
				for y := 0; y < n4h; y++ {
					for x := 0; x < n4w; x++ {
						d.predictInter(plane, baseX+4*x, baseY+4*y, 4, 4, y*n4w+x)
					}
				}
			} else {
				d.predictInter(plane, baseX, baseY, n4w*4, n4h*4, 0)
			}
		}
		maxX := (d.miCols * 8) >> uint(subX)
		maxY := (d.miRows * 8) >> uint(subY)
		var dcQ, acQ int
		if plane == 0 {
			dcQ = int(dcQLookup[qt][clip3(0, 255, qi+d.deltaQYDc)])
			acQ = int(acQLookup[qt][clip3(0, 255, qi)])
		} else {
			dcQ = int(dcQLookup[qt][clip3(0, 255, qi+d.deltaQUVDc)])
			acQ = int(acQLookup[qt][clip3(0, 255, qi+d.deltaQUVAc)])
		}
		blockIdx := 0
		for y := 0; y < n4h; y += step {
			for x := 0; x < n4w; x += step {
				startX := baseX + 4*x
				startY := baseY + 4*y
				nonzero := 0
				if startX < maxX && startY < maxY {
					if !b.isInter {
						d.predictIntra(plane, startX, startY, b.availL || x > 0, b.availU || y > 0, x+step < n4w, txSz, blockIdx)
					}
					if !b.skip {
						nonzero = d.tokens4(plane, startX, startY, txSz, blockIdx, dcQ, acQ, maxX, maxY, subX, subY)
					}
				}
				for i := 0; i < step; i++ {
					d.aboveNZ[plane][(startX>>2)+i] = uint8(nonzero)
					d.leftNZ[plane][((startY>>2)+i)&15] = uint8(nonzero)
				}
				blockIdx++
			}
		}
	}
}

// tokens4 reads the coefficients of one transform block and reconstructs it.
func (d *Decoder) tokens4(plane, startX, startY, txSz, blockIdx, dcQ, acQ, maxX, maxY, subX, subY int) int {
	b := &d.blk
	segEob := 16 << uint(txSz<<1)
	// transform type and scan
	txType := dctDct
	switch {
	case plane > 0 || txSz == tx32x32:
	case txSz == tx4x4:
		if !d.lossless && !b.isInter {
			m := b.yMode
			if b.miSize < block8x8 {
				m = int(b.subModes[blockIdx])
			}
			txType = int(mode2txfm[m])
		}
	default:
		txType = int(mode2txfm[b.yMode])
	}
	sv := 0
	switch txType {
	case adstDct:
		sv = 1
	case dctAdst:
		sv = 2
	}
	scan := scanTabs[txSz][sv]
	nb := nbTabs[txSz][sv]
	// initial context
	x4 := startX >> 2
	y4 := startY >> 2
	numpts := 1 << uint(txSz)
	mx4 := (2 * d.miCols) >> uint(subX)
	my4 := (2 * d.miRows) >> uint(subY)
	above, left := 0, 0
	for i := 0; i < numpts; i++ {
		if x4+i < mx4 {
			above |= int(d.aboveNZ[plane][x4+i])
		}
		if y4+i < my4 {
			left |= int(d.leftNZ[plane][(y4+i)&15])
		}
	}
	ctx := above + left
	ptype := btoi(plane > 0)
	ref := btoi(b.isInter)
	probs := &d.p.coef[txSz][ptype][ref]
	cnts := &d.cnt
	dqShift := uint(0)
	if txSz == tx32x32 {
		dqShift = 1
	}
	n0 := 4 << uint(txSz)
	checkEob := true
	c := 0
	maxRow := 0
	tc := &d.tokenCache
	toks := d.tokens[:n0*n0]
	for ; c < segEob; c++ {
		pos := int(scan[c])
		var band int
		if txSz == tx4x4 {
			band = int(coefband4x4[c])
		} else {
			band = int(coefband8x8plus[c])
		}
		if c > 0 {
			ctx = (1 + int(tc[nb[2*c]]) + int(tc[nb[2*c+1]])) >> 1
		}
		pr := &probs[band][ctx]
		if checkEob {
			more := d.bd.read(pr[0])
			cnts.moreCoefs[txSz][ptype][ref][band][ctx][more]++
			if more == 0 {
				break
			}
		}
		var token int
		if d.bd.read(pr[1]) == 0 {
			cnts.token[txSz][ptype][ref][band][ctx][0]++
			tc[pos] = 0
			checkEob = false
			continue
		}
		checkEob = true
		var coef int
		if d.bd.read(pr[2]) == 0 {
			token = 1
			cnts.token[txSz][ptype][ref][band][ctx][1]++
			coef = 1
		} else {
			cnts.token[txSz][ptype][ref][band][ctx][2]++
			pt := &pareto8[pr[2]]
			if d.bd.read(pt[0]) == 0 {
				if d.bd.read(pt[1]) == 0 {
					token = 2
				} else if d.bd.read(pt[2]) == 0 {
					token = 3
				} else {
					token = 4
				}
				coef = token
			} else {
				if d.bd.read(pt[3]) == 0 {
					if d.bd.read(pt[4]) == 0 {
						token = 5
					} else {
						token = 6
					}
				} else if d.bd.read(pt[5]) == 0 {
					if d.bd.read(pt[6]) == 0 {
						token = 7
					} else {
						token = 8
					}
				} else if d.bd.read(pt[7]) == 0 {
					token = 9
				} else {
					token = 10
				}
				cat := extraBits[token][0]
				numExtra := extraBits[token][1]
				coef = extraBits[token][2]
				if token == 10 {
					// high bit depth adds leading bits with probability 255
					nHi := d.bitDepth - 8
					for e := 0; e < nHi; e++ {
						coef += d.bd.read(255) << uint(numExtra+nHi-1-e)
					}
				}
				for e := 0; e < numExtra; e++ {
					coef += d.bd.read(catProbs[cat][e]) << uint(numExtra-1-e)
				}
			}
		}
		tc[pos] = energyClass[token]
		q := acQ
		if pos == 0 {
			q = dcQ
		}
		v := int32((coef * q) >> dqShift)
		if d.bd.read(128) == 1 {
			v = -v
		}
		toks[pos] = v
		if r := pos / n0; r > maxRow {
			maxRow = r
		}
	}
	nonzero := btoi(c > 0)
	d.eobTotal += nonzero
	if c > 0 {
		d.reconstruct(plane, startX, startY, txSz, txType, c, maxRow, scan)
	}
	return nonzero
}

// reconstruct runs the inverse transform of d.tokens and adds the result.
func (d *Decoder) reconstruct(plane, x, y, txSz, txType, eob, maxRow int, scan []uint16) {
	n := uint(2 + txSz)
	n0 := 1 << n
	buf := d.tokens[:n0*n0]
	fr := d.cur
	stride := fr.stride[plane]
	pix := fr.pix[plane]
	if eob == 1 && !d.lossless && txType == dctDct {
		// DC only: the transform output is constant.
		dc := int64(buf[0])
		v := round14(dc * 11585)
		w := round14(int64(v) * 11585)
		shift := n + 2
		if shift > 6 {
			shift = 6
		}
		add := (w + (1 << (shift - 1))) >> shift
		buf[0] = 0
		for i := 0; i < n0; i++ {
			row := pix[(y+i)*stride+x : (y+i)*stride+x+n0]
			for j := range row {
				row[j] = d.clipPix(int32(row[j]) + add)
			}
		}
		return
	}
	inverseTransform2D(buf, n, txType, d.lossless, maxRow)
	for i := 0; i < n0; i++ {
		row := pix[(y+i)*stride+x : (y+i)*stride+x+n0]
		src := buf[i*n0 : i*n0+n0]
		for j := range row {
			row[j] = d.clipPix(int32(row[j]) + src[j])
		}
	}
	for i := range buf {
		buf[i] = 0
	}
}

func clip255(v int32) uint8 {
	if uint32(v) > 255 {
		if v < 0 {
			return 0
		}
		return 255
	}
	return uint8(v)
}
