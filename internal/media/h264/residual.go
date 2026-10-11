package h264

// Residual data parsing (7.3.5.3) with dequantisation, over the non-zero
// count caches.

// nzW returns the width in 4x4 blocks of the non-zero-count grid of plane p.
func (d *Decoder) nzW(p int) int {
	if p == 0 || d.f.cat == 3 {
		return 4
	}
	return 2
}

// nzH returns the height in 4x4 blocks of the non-zero-count grid of plane p.
func (d *Decoder) nzH(p int) int {
	if p == 0 || d.f.cat == 3 {
		return 4
	}
	return d.f.mbHC / 4
}

// predNonZeroCount returns the CAVLC nC of the block at cache index ci of
// plane p.
func (d *Decoder) predNonZeroCount(p, ci int) int {
	c := &d.nzCache[p]
	i := int(c[ci-1]) + int(c[ci-8])
	if i < 64 {
		i = (i + 1) >> 1
	}
	return i & 31
}

// cbfCtx returns the coded_block_flag context increment of a non-DC block.
func (d *Decoder) cbfCtx(p, ci int) int {
	c := &d.nzCache[p]
	ctx := 0
	if c[ci-1] > 0 {
		ctx++
	}
	if c[ci-8] > 0 {
		ctx += 2
	}
	return ctx
}

// dcCbfCtx is the coded_block_flag increment of DC blocks; bit selects
// chroma DC (cb 6, cr 7) or luma DC (8 + plane).
func (d *Decoder) dcCbfCtx(bit int) int {
	ctx := 0
	if d.leftCBP>>uint(bit)&1 != 0 {
		ctx++
	}
	if d.topCBP>>uint(bit)&1 != 0 {
		ctx += 2
	}
	return ctx
}

// ctxBlockCat of the luma-like residual kinds of plane p.
var catOf = [3][4]int{{0, 1, 2, 5}, {6, 7, 8, 9}, {10, 11, 12, 13}}

// chromaQPPrime returns QP'c for QP'Y qp and the chroma offset off.
func (d *Decoder) chromaQPPrime(qpPrime, off int) int {
	q := qpPrime + off
	if q < 0 {
		q = 0
	} else if q > 51+d.f.qpBdC {
		q = 51 + d.f.qpBdC
	}
	return chromaQPf(q-d.f.qpBdC) + d.f.qpBdC
}

// qpOfPlane returns the QP' used for the residual of plane p.
func (d *Decoder) qpOfPlane(p int) int {
	qp := d.qp + d.f.qpBdY
	if p == 0 {
		return qp
	}
	return d.chromaQPPrime(qp, d.hdr.pps.ChromaQPOffset[p-1])
}

func (d *Decoder) parseResidual(intra, i16 bool, cbp int, t8 bool) error {
	d.blkNZ = [48]bool{}
	nLuma := 1
	if d.f.cat == 3 {
		nLuma = 3
	}
	for p := 0; p < nLuma; p++ {
		if err := d.residualLuma(p, intra, i16, cbp, t8); err != nil {
			return err
		}
	}
	if d.f.cat == 1 || d.f.cat == 2 {
		return d.residualChroma(intra, cbp)
	}
	return nil
}

// readBlock decodes one residual block of plane p at cache index ci and
// returns its coefficient count (-1 on error); cat is the ctxBlockCat.
func (d *Decoder) readBlock(p, cat, ci, maxNum int) int {
	var n int
	if d.cabacOn {
		n = d.cabacBlock(cat, d.cbfCtx(p, ci), maxNum, &d.lv)
	} else {
		n = d.cavlcBlock(d.predNonZeroCount(p, ci), maxNum, &d.lv)
	}
	if n >= 0 {
		d.nzCache[p][ci] = uint8(n)
	}
	return n
}

// residualLuma parses residual_luma() for plane p (luma, or Cb/Cr of 4:4:4).
func (d *Decoder) residualLuma(p int, intra, i16 bool, cbp int, t8 bool) error {
	pps := d.hdr.pps
	qp := d.qpOfPlane(p)
	list4 := p
	list8 := 2 * p
	if !intra {
		list4 += 3
		list8++
	}
	ls := &pps.ls4[list4][qp%6]
	base := p * 256
	bypass := d.bypassMB
	scan := d.scan4
	nz := &d.nzCache[p]
	// Intra16x16 DC
	if i16 {
		var n int
		if d.cabacOn {
			n = d.cabacBlock(catOf[p][0], d.dcCbfCtx(8+p), 16, &d.lv)
		} else {
			n = d.cavlcBlock(d.predNonZeroCount(p, c0), 16, &d.lv)
		}
		if n < 0 {
			return corrupt()
		}
		dc := &d.dcY[p]
		*dc = [16]int32{}
		if n > 0 {
			d.cbpBits |= 0x100 << uint(p)
			for s := 0; s < 16; s++ {
				if d.lv[s] != 0 {
					dc[scan[s]] = d.lv[s]
				}
			}
			if !bypass {
				lumaDCTransform(dc, ls[0], qp)
			}
		}
	}
	for b8 := 0; b8 < 4; b8++ {
		if cbp&(1<<uint(b8)) == 0 {
			// not coded: counts are zero
			for k := 0; k < 4; k++ {
				nz[scan8[b8*4+k]] = 0
			}
			continue
		}
		if t8 {
			if err := d.luma8x8(p, intra, b8, qp, list8); err != nil {
				return err
			}
			continue
		}
		for k := 0; k < 4; k++ {
			blk := b8*4 + k
			ci := scan8[blk]
			var n int
			if i16 {
				n = d.readBlock(p, catOf[p][1], ci, 15)
			} else {
				n = d.readBlock(p, catOf[p][2], ci, 16)
			}
			if n < 0 {
				return corrupt()
			}
			if n == 0 {
				continue
			}
			bx, by := blkX[blk], blkY[blk]
			if p == 0 {
				d.nzMask |= 1 << uint(by*4+bx)
			}
			d.blkNZ[p*16+blk] = true
			off := 0
			if i16 {
				off = 1
			}
			c := d.coef[base+blk*16 : base+blk*16+16]
			for s := 0; s < 16-off; s++ {
				if v := d.lv[s]; v != 0 {
					r := scan[s+off]
					if bypass {
						c[r] = v
					} else {
						c[r] = dequant4(v, ls[r], qp)
					}
				}
			}
		}
	}
	if i16 {
		if cbp&15 == 0 {
			for blk := 0; blk < 16; blk++ {
				nz[scan8[blk]] = 0
			}
		}
		// place the transformed DC values
		for pos := 0; pos < 16; pos++ {
			if v := d.dcY[p][pos]; v != 0 {
				blk := posToBlk[pos]
				d.coef[base+blk*16] = v
				d.blkNZ[p*16+blk] = true
			}
		}
	}
	return nil
}

// residualChroma parses the 4:2:0 / 4:2:2 chroma residual.
func (d *Decoder) residualChroma(intra bool, cbp int) error {
	pps := d.hdr.pps
	cat := d.f.cat
	n8 := d.f.nBlkC // number of DC coefficients / 4x4 blocks
	if cbp>>4 == 0 {
		return nil
	}
	var dcC [2][8]int32
	for c := 0; c < 2; c++ {
		var n int
		if d.cabacOn {
			n = d.cabacBlock(3, d.dcCbfCtx(6+c), n8, &d.lv)
		} else if cat == 1 {
			n = d.cavlcBlock(-1, 4, &d.lv)
		} else {
			n = d.cavlcBlock(-2, 8, &d.lv)
		}
		if n < 0 {
			return corrupt()
		}
		if n > 0 {
			d.cbpBits |= 0x40 << uint(c)
			for s := 0; s < n8; s++ {
				if cat == 1 {
					dcC[c][s] = d.lv[s]
				} else {
					dcC[c][chroma422DCScan[s]] = d.lv[s]
				}
			}
		}
	}
	for c := 0; c < 2; c++ {
		list := 1 + c
		if !intra {
			list += 3
		}
		qpc := d.chromaQPPrime(d.qp+d.f.qpBdY, pps.ChromaQPOffset[c])
		ls := &pps.ls4[list][qpc%6]
		base := (c + 1) * 256
		if dcC[c] != [8]int32{} {
			switch {
			case d.bypassMB:
			case cat == 1:
				chromaDCTransform(&dcC[c], ls[0], qpc)
			default:
				q := qpc + 3
				chromaDC422Transform(&dcC[c], pps.ls4[list][q%6][0], q)
			}
			for b := 0; b < n8; b++ {
				if dcC[c][b] != 0 {
					d.coef[base+b*16] = dcC[c][b]
					d.blkNZ[(c+1)*16+b] = true
				}
			}
		}
		if cbp>>4 != 2 {
			continue
		}
		for b := 0; b < n8; b++ {
			ci := c0 + (b>>1)*8 + b&1
			n := d.readBlock(c+1, 4, ci, 15)
			if n < 0 {
				return corrupt()
			}
			if n == 0 {
				continue
			}
			d.blkNZ[(c+1)*16+b] = true
			cc := d.coef[base+b*16 : base+b*16+16]
			for s := 0; s < 15; s++ {
				if v := d.lv[s]; v != 0 {
					r := d.scan4[s+1]
					if d.bypassMB {
						cc[r] = v
					} else {
						cc[r] = dequant4(v, ls[r], qpc)
					}
				}
			}
		}
	}
	return nil
}

// chroma422DCScan maps the scan position of a 4:2:2 chroma DC level to its
// raster position in the 4 row x 2 column DC matrix.
var chroma422DCScan = [8]uint8{0, 2, 1, 4, 6, 3, 5, 7}

// luma8x8 parses one 8x8 transform block of plane p (CABAC: single block;
// CAVLC: four interleaved 4x4 blocks).
func (d *Decoder) luma8x8(p int, intra bool, b8, qp, list8 int) error {
	ls := &d.hdr.pps.ls8[list8][qp%6]
	base := p * 256
	c := d.coef[base+b8*64 : base+b8*64+64]
	bx0, by0 := (b8&1)*2, (b8>>1)*2
	scan := d.scan8
	nz := &d.nzCache[p]
	ci0 := c0 + by0*8 + bx0
	total := 0
	if d.cabacOn {
		var n int
		cat := catOf[p][3]
		if d.f.cat == 3 {
			// neighbours that do not use the 8x8 transform contribute no coefficients
			ctx := d.cbfCtx8(p, ci0)
			n = d.cabacBlock(cat, ctx, 64, &d.lv)
		} else {
			n = d.cabacBlock(cat, -1, 64, &d.lv)
		}
		total = n
		for s := 0; s < 64; s++ {
			if v := d.lv[s]; v != 0 {
				r := scan[s]
				if d.bypassMB {
					c[r] = v
				} else {
					c[r] = dequant8(v, ls[r], qp)
				}
			}
		}
		for k := 0; k < 4; k++ {
			nz[ci0+(k>>1)*8+k&1] = uint8(min(n, 255))
		}
	} else {
		for k := 0; k < 4; k++ {
			ci := ci0 + (k>>1)*8 + k&1
			n := d.cavlcBlock(d.predNonZeroCount(p, ci), 16, &d.lv)
			if n < 0 {
				return corrupt()
			}
			nz[ci] = uint8(n)
			total += n
			for s := 0; s < 16; s++ {
				if v := d.lv[s]; v != 0 {
					r := scan[4*s+k]
					if d.bypassMB {
						c[r] = v
					} else {
						c[r] = dequant8(v, ls[r], qp)
					}
				}
			}
		}
	}
	if total > 0 {
		d.blkNZ[p*16+b8*4] = true
		if p == 0 {
			for k := 0; k < 4; k++ {
				d.nzMask |= 1 << uint((by0+k>>1)*4+bx0+k&1)
			}
		}
	}
	return nil
}

// cbfCtx8 is the coded_block_flag context of an 8x8 block of 4:4:4 content:
// neighbouring macroblocks that do not use the 8x8 transform count as having
// no coefficients (or all, for I_PCM).
func (d *Decoder) cbfCtx8(p, ci int) int {
	c := &d.nzCache[p]
	nb := &d.nb
	na, nbv := c[ci-1], c[ci-8]
	if ci&7 == 4 { // left edge of the macroblock
		r := (ci >> 3) - 1
		i := r >> 1
		if t := nb.leftType[i]; t != 0 && t&mb8x8DCT == 0 {
			na = 0
			if t&mbIntraPCM != 0 {
				na = 64
			}
		}
	}
	if ci>>3 == 1 { // top edge
		if t := nb.topType; t != 0 && t&mb8x8DCT == 0 {
			nbv = 0
			if t&mbIntraPCM != 0 {
				nbv = 64
			}
		}
	}
	ctx := 0
	if na > 0 {
		ctx++
	}
	if nbv > 0 {
		ctx += 2
	}
	return ctx
}
