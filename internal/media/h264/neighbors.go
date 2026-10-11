package h264

// Neighbour derivation and the per-macroblock caches (6.4.12). The caches
// hold the data of the current macroblock and of its left/top neighbours in
// a fixed 5x8 layout so that prediction and context derivation can read
// neighbours uniformly, including the field/frame mapping of MBAFF frames.

type nbState struct {
	topXY, topLeftXY, topRightXY int
	leftXY                       [2]int
	topType, topLeftType         uint32
	topRightType                 uint32
	leftType                     [2]uint32
	leftRow                      [4]uint8 // neighbour 4x4 row for each luma/4:2:2 chroma row
	leftRowC                     [2]uint8 // neighbour row of the two 4:2:0 chroma rows
	leftOpt                      int
	topLeftPartition             int // 0: use the middle of the block (MBAFF special case)
	topLeftRowFull               bool
}

var leftRowOptions = [4]struct {
	luma [4]uint8
	c    [2]uint8
}{
	{[4]uint8{0, 1, 2, 3}, [2]uint8{0, 1}},
	{[4]uint8{2, 2, 3, 3}, [2]uint8{1, 1}},
	{[4]uint8{0, 0, 1, 1}, [2]uint8{0, 0}},
	{[4]uint8{0, 2, 0, 2}, [2]uint8{0, 0}},
}

func (d *Decoder) mbTypeAt(i int) uint32 { return d.curPic.mbType[i] }

// fillNeighbors determines the neighbouring macroblocks of the current one
// (fill_decode_neighbors). d.mbField must hold the field decoding flag of the
// current macroblock.
func (d *Decoder) fillNeighbors() {
	pic := d.curPic
	stride := d.mbStride
	mbxy := d.mbIdx
	nb := &d.nb
	nb.leftOpt = 0
	nb.topLeftPartition = 1
	topXY := mbxy - stride<<uint(btoi(d.mbField))
	topLeftXY := topXY - 1
	topRightXY := topXY + 1
	nb.leftXY[0], nb.leftXY[1] = mbxy-1, mbxy-1
	if d.mbaffFrame {
		leftField := pic.mbType[mbxy-1]&mbInterlaced != 0
		curField := d.mbField
		if d.mby&1 != 0 {
			if leftField != curField {
				nb.leftXY[0] = mbxy - stride - 1
				nb.leftXY[1] = nb.leftXY[0]
				if curField {
					nb.leftXY[1] += stride
					nb.leftOpt = 3
				} else {
					topLeftXY += stride
					nb.topLeftPartition = 0
					nb.leftOpt = 1
				}
			}
		} else {
			if curField {
				if pic.mbType[topXY-1]&mbInterlaced == 0 {
					topLeftXY += stride
				}
				if pic.mbType[topXY+1]&mbInterlaced == 0 {
					topRightXY += stride
				}
				if pic.mbType[topXY]&mbInterlaced == 0 {
					topXY += stride
				}
			}
			if leftField != curField {
				if curField {
					nb.leftXY[1] += stride
					nb.leftOpt = 3
				} else {
					nb.leftOpt = 2
				}
			}
		}
	}
	nb.topXY, nb.topLeftXY, nb.topRightXY = topXY, topLeftXY, topRightXY
	opt := &leftRowOptions[nb.leftOpt]
	nb.leftRow, nb.leftRowC = opt.luma, opt.c
	cur := d.curSlice
	st := d.sliceTable
	nb.topLeftType = pic.mbType[topLeftXY]
	nb.topType = pic.mbType[topXY]
	nb.topRightType = pic.mbType[topRightXY]
	nb.leftType[0] = pic.mbType[nb.leftXY[0]]
	nb.leftType[1] = pic.mbType[nb.leftXY[1]]
	if st[topLeftXY] != cur {
		nb.topLeftType = 0
	}
	if st[topXY] != cur {
		nb.topType = 0
	}
	if st[nb.leftXY[0]] != cur {
		nb.leftType[0], nb.leftType[1] = 0, 0
	}
	if st[topRightXY] != cur {
		nb.topRightType = 0
	}
	d.availA = nb.leftType[0] != 0
	d.availB = nb.topType != 0
	d.availC = nb.topRightType != 0
	d.availD = nb.topLeftType != 0
}

// fillCaches fills the non-zero-count, intra mode, motion and CABAC caches
// for a macroblock of type mbType (fill_decode_caches).
func (d *Decoder) fillCaches(mbType uint32) {
	pic := d.curPic
	nb := &d.nb
	cabac := d.cabacOn
	intra := isIntraT(mbType)
	topType, leftType := nb.topType, nb.leftType
	cat := d.f.cat

	if mbType&mbSkip == 0 {
		if intra {
			typeMask := ^uint32(0)
			if d.hdr.pps.ConstrainedIntra {
				typeMask = 7
			}
			// intra4x4 prediction mode cache
			if mbType&mbIntra4x4 != 0 {
				ipm := &d.ipmCache
				if topType&mbIntra4x4 != 0 {
					for x := 0; x < 4; x++ {
						ipm[4+x] = d.ipm[nb.topXY*16+12+x]
					}
				} else {
					v := int8(2)
					if topType&typeMask == 0 {
						v = -1
					}
					ipm[4], ipm[5], ipm[6], ipm[7] = v, v, v, v
				}
				for i := 0; i < 2; i++ {
					lt := leftType[i]
					for r := 0; r < 2; r++ {
						row := 2*i + r
						ci := 3 + 8*(1+row)
						if lt&mbIntra4x4 != 0 {
							ipm[ci] = d.ipm[nb.leftXY[i]*16+int(nb.leftRow[row])*4+3]
						} else {
							v := int8(2)
							if lt&typeMask == 0 {
								v = -1
							}
							ipm[ci] = v
						}
					}
				}
			}
		}
		// non-zero counts
		nPl := 1
		switch cat {
		case 1, 2:
			nPl = 3
		case 3:
			nPl = 3
		}
		for p := 0; p < nPl; p++ {
			w, h := d.nzW(p), d.nzH(p)
			c := &d.nzCache[p]
			if topType != 0 {
				for x := 0; x < w; x++ {
					c[4+x] = d.nz[nb.topXY*48+p*16+(h-1)*w+x]
				}
			} else {
				v := uint8(64)
				if cabac && !intra {
					v = 0
				}
				for x := 0; x < w; x++ {
					c[4+x] = v
				}
			}
			for r := 0; r < h; r++ {
				var lm int
				var lr int
				if p == 0 || cat == 3 || cat == 2 {
					lm = r >> 1
					lr = int(nb.leftRow[r])
				} else {
					lm = r
					lr = int(nb.leftRowC[r])
				}
				if leftType[lm] != 0 {
					c[3+8*(1+r)] = d.nz[nb.leftXY[lm]*48+p*16+lr*w+w-1]
				} else {
					v := uint8(64)
					if cabac && !intra {
						v = 0
					}
					c[3+8*(1+r)] = v
				}
			}
		}
		if cabac {
			if topType != 0 {
				d.topCBP = int(pic2cbp(d.cbpTable[nb.topXY]))
			} else if intra {
				d.topCBP = 0x7CF
			} else {
				d.topCBP = 0x00F
			}
			if leftType[0] != 0 {
				l0 := int(d.cbpTable[nb.leftXY[0]])
				l1 := int(d.cbpTable[nb.leftXY[1]])
				d.leftCBP = l0&0x7F0 | (l0>>(nb.leftRow[0]&^1))&2 | ((l1>>(nb.leftRow[2]&^1))&2)<<2
			} else if intra {
				d.leftCBP = 0x7CF
			} else {
				d.leftCBP = 0x00F
			}
		}
	}

	if isInterT(mbType) || (mbType&mbDirect2 != 0 && d.hdr.directSpatial) {
		for list := 0; list < d.listCount; list++ {
			if !usesList(mbType, list) {
				continue
			}
			refc := &d.refCache[list]
			mvc := &d.mvCache[list]
			refs := pic.refIdx[list]
			mvs := pic.mv[list]
			// top
			if usesList(topType, list) {
				t := nb.topXY
				for x := 0; x < 4; x++ {
					mvc[4+x] = mvs[t*16+12+x]
				}
				refc[4], refc[5] = refs[t*4+2], refs[t*4+2]
				refc[6], refc[7] = refs[t*4+3], refs[t*4+3]
			} else {
				for x := 0; x < 4; x++ {
					mvc[4+x] = mv16{}
				}
				v := int8(refNotAvail)
				if topType != 0 {
					v = refNotUsed
				}
				refc[4], refc[5], refc[6], refc[7] = v, v, v, v
			}
			// left
			for i := 0; i < 2; i++ {
				lt := leftType[i]
				for r := 0; r < 2; r++ {
					row := 2*i + r
					ci := 3 + 8*(1+row)
					if mbType&(mb16x8|mb8x8) == 0 && row != 0 {
						continue
					}
					if usesList(lt, list) {
						lx := nb.leftXY[i]
						lr := int(nb.leftRow[row])
						mvc[ci] = mvs[lx*16+lr*4+3]
						refc[ci] = refs[lx*4+(lr>>1)*2+1]
					} else {
						mvc[ci] = mv16{}
						if lt != 0 {
							refc[ci] = refNotUsed
						} else {
							refc[ci] = refNotAvail
						}
					}
				}
			}
			// top right
			if usesList(nb.topRightType, list) {
				t := nb.topRightXY
				mvc[8] = mvs[t*16+12]
				refc[8] = refs[t*4+2]
			} else {
				mvc[8] = mv16{}
				if nb.topRightType != 0 {
					refc[8] = refNotUsed
				} else {
					refc[8] = refNotAvail
				}
			}
			// top left
			if refc[6] < 0 || refc[8] < 0 {
				if usesList(nb.topLeftType, list) {
					t := nb.topLeftXY
					if nb.topLeftPartition != 0 {
						mvc[3] = mvs[t*16+15]
						refc[3] = refs[t*4+3]
					} else {
						mvc[3] = mvs[t*16+7]
						refc[3] = refs[t*4+1]
					}
				} else {
					mvc[3] = mv16{}
					if nb.topLeftType != 0 {
						refc[3] = refNotUsed
					} else {
						refc[3] = refNotAvail
					}
				}
			}
			if mbType&(mbSkip|mbDirect2) != 0 && !d.mbaffFrame {
				continue
			}
			if mbType&(mbSkip|mbDirect2) == 0 {
				mvdc := &d.mvdCache[list]
				refc[c0+2+8*0] = refNotAvail
				refc[c0+2+8*2] = refNotAvail
				mvc[c0+2+8*0] = mv16{}
				mvc[c0+2+8*2] = mv16{}
				if cabac {
					if usesList(topType, list) {
						for x := 0; x < 4; x++ {
							mvdc[4+x] = d.mvd[list][(nb.topXY*16+12+x)]
						}
					} else {
						for x := 0; x < 4; x++ {
							mvdc[4+x] = [2]uint8{}
						}
					}
					for i := 0; i < 2; i++ {
						for r := 0; r < 2; r++ {
							row := 2*i + r
							ci := 3 + 8*(1+row)
							if usesList(leftType[i], list) {
								mvdc[ci] = d.mvd[list][nb.leftXY[i]*16+int(nb.leftRow[row])*4+3]
							} else {
								mvdc[ci] = [2]uint8{}
							}
						}
					}
					mvdc[c0+2+8*0] = [2]uint8{}
					mvdc[c0+2+8*2] = [2]uint8{}
					if d.hdr.isB() {
						dc := &d.directCache
						for r := 0; r < 4; r++ {
							for x := 0; x < 4; x++ {
								dc[c0+r*8+x] = mb16x16 >> 1
							}
						}
						switch {
						case topType&mbDirect2 != 0:
							for x := 0; x < 4; x++ {
								dc[4+x] = mbDirect2 >> 1
							}
						case topType&mb8x8 != 0:
							dc[4], dc[5] = d.directTable[nb.topXY*4+2], d.directTable[nb.topXY*4+2]
							dc[6], dc[7] = d.directTable[nb.topXY*4+3], d.directTable[nb.topXY*4+3]
						default:
							for x := 0; x < 4; x++ {
								dc[4+x] = mb16x16 >> 1
							}
						}
						for i := 0; i < 2; i++ {
							lt := leftType[i]
							ci := 3 + 8*(1+2*i)
							switch {
							case lt&mbDirect2 != 0:
								dc[ci] = mbDirect2 >> 1
							case lt&mb8x8 != 0:
								dc[ci] = d.directTable[nb.leftXY[i]*4+1+int(nb.leftRow[2*i]&^1)]
							default:
								dc[ci] = mb16x16 >> 1
							}
						}
					}
				}
			}
			if d.mbaffFrame {
				d.mapMBAFFCache(list, mbType)
			}
		}
	}
	d.neighborT8 = btoi(topType&mb8x8DCT != 0) + btoi(leftType[0]&mb8x8DCT != 0)
}

func pic2cbp(v uint16) uint16 { return v }

// mapMBAFFCache scales the cached neighbour motion when a neighbouring
// macroblock has a different field/frame mode (MAP_MVS in FFmpeg).
func (d *Decoder) mapMBAFFCache(list int, mbType uint32) {
	nb := &d.nb
	refc := &d.refCache[list]
	mvc := &d.mvCache[list]
	mvdc := &d.mvdCache[list]
	apply := func(idx int, t uint32) {
		if d.mbField {
			if t&mbInterlaced == 0 && refc[idx] >= 0 {
				refc[idx] *= 2
				mvc[idx].y /= 2
				mvdc[idx][1] >>= 1
			}
		} else {
			if t&mbInterlaced != 0 && refc[idx] >= 0 {
				refc[idx] >>= 1
				mvc[idx].y *= 2
				mvdc[idx][1] <<= 1
			}
		}
	}
	apply(c0-1-8, nb.topLeftType)
	apply(c0+0-8, nb.topType)
	apply(c0+1-8, nb.topType)
	apply(c0+2-8, nb.topType)
	apply(c0+3-8, nb.topType)
	apply(c0+4-8, nb.topRightType)
	apply(c0-1+0*8, nb.leftType[0])
	apply(c0-1+1*8, nb.leftType[0])
	apply(c0-1+2*8, nb.leftType[1])
	apply(c0-1+3*8, nb.leftType[1])
}

// writeBackMotion stores the cached motion of the current macroblock in the
// picture (write_back_motion).
func (d *Decoder) writeBackMotion(mbType uint32) {
	pic := d.curPic
	idx := d.mbIdx
	for list := 0; list < 2; list++ {
		if list == 1 && !usesList(mbType, 1) {
			for b := 0; b < 4; b++ {
				pic.refIdx[1][idx*4+b] = refNotUsed
				pic.refID[1][idx*4+b] = -1
			}
			for k := 0; k < 16; k++ {
				pic.mv[1][idx*16+k] = mv16{}
			}
			if d.cabacOn {
				for k := 0; k < 16; k++ {
					d.mvd[1][idx*16+k] = [2]uint8{}
				}
			}
			break
		}
		if list == 0 && !usesList(mbType, 0) {
			for b := 0; b < 4; b++ {
				pic.refIdx[0][idx*4+b] = refNotUsed
				pic.refID[0][idx*4+b] = -1
			}
			for k := 0; k < 16; k++ {
				pic.mv[0][idx*16+k] = mv16{}
			}
			if d.cabacOn {
				for k := 0; k < 16; k++ {
					d.mvd[0][idx*16+k] = [2]uint8{}
				}
			}
			continue
		}
		mvc := &d.mvCache[list]
		for y := 0; y < 4; y++ {
			for x := 0; x < 4; x++ {
				pic.mv[list][idx*16+y*4+x] = mvc[c0+y*8+x]
			}
		}
		if d.cabacOn {
			mvdc := &d.mvdCache[list]
			for y := 0; y < 4; y++ {
				for x := 0; x < 4; x++ {
					if mbType&mbSkip != 0 {
						d.mvd[list][idx*16+y*4+x] = [2]uint8{}
					} else {
						d.mvd[list][idx*16+y*4+x] = mvdc[c0+y*8+x]
					}
				}
			}
		}
		refc := &d.refCache[list]
		for b := 0; b < 4; b++ {
			r := refc[scan8[b*4]]
			pic.refIdx[list][idx*4+b] = r
			pic.refID[list][idx*4+b] = d.refIdentity(list, int(r))
		}
	}
	if d.hdr.isB() && d.cabacOn && mbType&mb8x8 != 0 {
		for b := 1; b < 4; b++ {
			d.directTable[idx*4+b] = uint8(d.subMbType[b] >> 1)
		}
	}
}

// refIdentity returns the identity of reference index r of the list for the
// current macroblock.
func (d *Decoder) refIdentity(list, r int) int32 {
	if r < 0 {
		return -1
	}
	if d.mbMbaff() {
		l := d.refListField[list]
		i := r ^ (d.mby & 1)
		if i < len(l) {
			return l[i].identity()
		}
		return -1
	}
	l := d.refList[list]
	if r < len(l) {
		return l[r].identity()
	}
	return -1
}

func (d *Decoder) mbMbaff() bool { return d.mbaffFrame && d.mbField }
