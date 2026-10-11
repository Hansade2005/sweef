package h264

// Residual data parsing (7.3.5.3) with dequantisation.

// nzCtx returns the neighbouring total_coeff values of the block at (bx, by)
// within plane (0 luma 4x4 grid, 1/2 chroma 2x2 grids).
func (d *Decoder) nzCtx(plane, bx, by int) (na, nb int, aok, bok bool) {
	idx := d.mbIdx
	base, w := 0, 4
	if plane > 0 {
		base, w = 16+4*(plane-1), 2
	}
	if bx > 0 {
		na, aok = int(d.nz[idx*24+base+by*w+bx-1]), true
	} else if d.availA {
		na, aok = int(d.nz[(idx-1)*24+base+by*w+w-1]), true
	}
	if by > 0 {
		nb, bok = int(d.nz[idx*24+base+(by-1)*w+bx]), true
	} else if d.availB {
		nb, bok = int(d.nz[(idx-d.mbW)*24+base+(w-1)*w+bx]), true
	}
	return
}

func (d *Decoder) blockCtx(plane, bx, by int, intra bool) (nC int, cbf int) {
	na, nb, aok, bok := d.nzCtx(plane, bx, by)
	if !d.cabacOn {
		switch {
		case aok && bok:
			return (na + nb + 1) >> 1, 0
		case aok:
			return na, 0
		case bok:
			return nb, 0
		}
		return 0, 0
	}
	ca, cb := btoi(intra), btoi(intra)
	if aok {
		ca = btoi(na != 0)
	}
	if bok {
		cb = btoi(nb != 0)
	}
	return 0, ca + 2*cb
}

// dcCbfCtx is the coded_block_flag increment of DC blocks (bit selects
// luma DC = 0, Cb DC = 1, Cr DC = 2).
func (d *Decoder) dcCbfCtx(bit int, intra bool) int {
	ca, cb := btoi(intra), btoi(intra)
	if d.availA {
		ca = int(d.mbDC[d.mbIdx-1]>>uint(bit)) & 1
	}
	if d.availB {
		cb = int(d.mbDC[d.mbIdx-d.mbW]>>uint(bit)) & 1
	}
	return ca + 2*cb
}

func (d *Decoder) parseResidual(intra, i16 bool, cbp int, t8 bool) error {
	idx := d.mbIdx
	pps := d.hdr.pps
	qp := d.qp
	listY := 3
	if intra {
		listY = 0
	}
	lsY := &pps.ls4[listY][qp%6]
	d.blkNZ = [24]bool{}
	d.i16DC = false
	read := func(plane int, cat, bx, by, maxNum int) int {
		if d.cabacOn {
			_, cbf := d.blockCtx(plane, bx, by, intra)
			return d.cabacBlock(cat, cbf, maxNum, &d.lv)
		}
		nC, _ := d.blockCtx(plane, bx, by, intra)
		return d.cavlcBlock(nC, maxNum, &d.lv)
	}
	// Intra16x16 DC
	if i16 {
		var n int
		if d.cabacOn {
			n = d.cabacBlock(0, d.dcCbfCtx(0, intra), 16, &d.lv)
		} else {
			nC, _ := d.blockCtx(0, 0, 0, intra)
			n = d.cavlcBlock(nC, 16, &d.lv)
		}
		if n < 0 {
			return corrupt()
		}
		for i := range d.dcY {
			d.dcY[i] = 0
		}
		if n > 0 {
			d.mbDC[idx] |= 1
			for s := 0; s < 16; s++ {
				if d.lv[s] != 0 {
					d.dcY[zigzag4[s]] = d.lv[s]
				}
			}
			lumaDCTransform(&d.dcY, lsY[0], qp)
		}
		d.i16DC = n > 0
	}
	// luma
	for b8 := 0; b8 < 4; b8++ {
		if cbp&(1<<uint(b8)) == 0 {
			continue
		}
		if t8 {
			if err := d.luma8x8(intra, b8, qp); err != nil {
				return err
			}
			continue
		}
		for k := 0; k < 4; k++ {
			blk := b8*4 + k
			bx, by := blkX[blk], blkY[blk]
			var n int
			if i16 {
				n = read(0, 1, bx, by, 15)
			} else {
				n = read(0, 2, bx, by, 16)
			}
			if n < 0 {
				return corrupt()
			}
			d.nz[idx*24+by*4+bx] = uint8(n)
			if n == 0 {
				continue
			}
			d.nzMask[idx] |= 1 << uint(by*4+bx)
			d.blkNZ[blk] = true
			off := 0
			if i16 {
				off = 1
			}
			c := d.coef[blk*16 : blk*16+16]
			for s := 0; s < 16-off; s++ {
				if v := d.lv[s]; v != 0 {
					r := zigzag4[s+off]
					c[r] = dequant4(v, lsY[r], qp)
				}
			}
		}
	}
	if i16 {
		// place the transformed DC values
		for p := 0; p < 16; p++ {
			if v := d.dcY[p]; v != 0 {
				blk := posToBlk[p]
				d.coef[blk*16] = v
				d.blkNZ[blk] = true
			}
		}
	}
	// chroma
	if cbp>>4 == 0 {
		return nil
	}
	var dcC [2][4]int32
	for c := 0; c < 2; c++ {
		var n int
		if d.cabacOn {
			n = d.cabacBlock(3, d.dcCbfCtx(1+c, intra), 4, &d.lv)
		} else {
			n = d.cavlcBlock(-1, 4, &d.lv)
		}
		if n < 0 {
			return corrupt()
		}
		if n > 0 {
			d.mbDC[idx] |= 2 << uint(c)
			for s := 0; s < 4; s++ {
				dcC[c][s] = d.lv[s]
			}
		}
	}
	for c := 0; c < 2; c++ {
		list := 1 + c
		if !intra {
			list += 3
		}
		qpc := chromaQP(qp, pps.ChromaQPOffset[c])
		ls := &pps.ls4[list][qpc%6]
		if dcC[c] != [4]int32{} {
			chromaDCTransform(&dcC[c], ls[0], qpc)
			for b := 0; b < 4; b++ {
				if dcC[c][b] != 0 {
					d.coef[256+c*64+b*16] = dcC[c][b]
					d.blkNZ[16+c*4+b] = true
				}
			}
		}
		if cbp>>4 != 2 {
			continue
		}
		for b := 0; b < 4; b++ {
			bx, by := b&1, b>>1
			n := read(1+c, 4, bx, by, 15)
			if n < 0 {
				return corrupt()
			}
			d.nz[idx*24+16+c*4+b] = uint8(n)
			if n == 0 {
				continue
			}
			d.blkNZ[16+c*4+b] = true
			cc := d.coef[256+c*64+b*16 : 256+c*64+b*16+16]
			for s := 0; s < 15; s++ {
				if v := d.lv[s]; v != 0 {
					r := zigzag4[s+1]
					cc[r] = dequant4(v, ls[r], qpc)
				}
			}
		}
	}
	return nil
}

// luma8x8 parses one 8x8 transform block (CABAC: single block; CAVLC: four
// interleaved 4x4 blocks).
func (d *Decoder) luma8x8(intra bool, b8, qp int) error {
	idx := d.mbIdx
	list := 1
	if intra {
		list = 0
	}
	ls := &d.hdr.pps.ls8[list][qp%6]
	c := d.coef[b8*64 : b8*64+64]
	bx0, by0 := (b8&1)*2, (b8>>1)*2
	total := 0
	if d.cabacOn {
		n := d.cabacBlock(5, -1, 64, &d.lv)
		total = n
		for s := 0; s < 64; s++ {
			if v := d.lv[s]; v != 0 {
				r := zigzag8[s]
				c[r] = dequant8(v, ls[r], qp)
			}
		}
		for k := 0; k < 4; k++ {
			d.nz[idx*24+(by0+k>>1)*4+bx0+k&1] = uint8(min(n, 255))
		}
	} else {
		for k := 0; k < 4; k++ {
			bx, by := bx0+k&1, by0+k>>1
			nC, _ := d.blockCtx(0, bx, by, intra)
			n := d.cavlcBlock(nC, 16, &d.lv)
			if n < 0 {
				return corrupt()
			}
			d.nz[idx*24+by*4+bx] = uint8(n)
			total += n
			for s := 0; s < 16; s++ {
				if v := d.lv[s]; v != 0 {
					r := zigzag8[4*s+k]
					c[r] = dequant8(v, ls[r], qp)
				}
			}
		}
	}
	if total > 0 {
		d.blkNZ[b8*4] = true
		for k := 0; k < 4; k++ {
			d.nzMask[idx] |= 1 << uint((by0+k>>1)*4+bx0+k&1)
		}
	}
	return nil
}
