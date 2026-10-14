package vp9

import (
	"runtime"
	"sync"
	"sync/atomic"
)

// Tile, partition and block level syntax (spec 6.4).

type blockState struct {
	miRow, miCol   int
	miSize         int
	availU, availL bool
	segID          int
	skip           bool
	txSize         int
	isInter        bool
	yMode          int
	subModes       [4]uint8
	uvMode         int
	refFrame       [2]int
	interpFilter   int
	blockMvs       [2][4]mv

	leftRef, aboveRef       [2]int
	leftIntra, aboveIntra   bool
	leftSingle, aboveSingle bool

	refListMv   [2]mv
	refMvCount  int
	nearestMv   [2]mv
	nearMv      [2]mv
	bestMv      [2]mv
	modeContext [4]int
}

func tileOffset(tile, mis, log2 int) int {
	sbs := (mis + 7) >> 3
	off := ((tile * sbs) >> uint(log2)) << 3
	if off > mis {
		return mis
	}
	return off
}

// tileParallel enables decoding tile columns on several goroutines (tests switch it off to compare).
var tileParallel = true

// SetTileParallel switches multi-goroutine tile decoding on or off (tests compare both).
func SetTileParallel(on bool) { tileParallel = on }

// decodeTiles decodes the tiles of a frame. Tile columns of one tile row are independent (separate bool
// decoders, no prediction or context across their edges), so they run on separate goroutines, each with a
// private copy of the decoder state; their symbol counts are added up afterwards. Tile rows depend on the
// row above and run one after another.
func (d *Decoder) decodeTiles(data []byte) {
	tileCols := 1 << uint(d.tileColsLog2)
	tileRows := 1 << uint(d.tileRowsLog2)
	for p := 0; p < 3; p++ {
		d.aboveNZ[p] = make([]uint8, d.sb64Cols*16+16)
	}
	d.abovePart = make([]uint8, d.sb64Cols*8+8)
	d.aboveSegPred = make([]uint8, d.sb64Cols*8+8)
	parallel := tileCols > 1 && tileParallel && min(runtime.GOMAXPROCS(0), 4) > 1
	for tr := 0; tr < tileRows; tr++ {
		tiles := make([][]byte, tileCols)
		for tc := 0; tc < tileCols; tc++ {
			last := tr == tileRows-1 && tc == tileCols-1
			var size int
			if last {
				size = len(data)
			} else {
				if len(data) < 4 {
					panic("short tile")
				}
				size = int(data[0])<<24 | int(data[1])<<16 | int(data[2])<<8 | int(data[3])
				data = data[4:]
				if size > len(data) {
					panic("short tile")
				}
			}
			tiles[tc] = data[:size]
			data = data[size:]
		}
		rowStart := tileOffset(tr, d.miRows, d.tileRowsLog2)
		rowEnd := tileOffset(tr+1, d.miRows, d.tileRowsLog2)
		if !parallel {
			for tc := 0; tc < tileCols; tc++ {
				d.decodeTile(tiles[tc], rowStart, rowEnd, tileOffset(tc, d.miCols, d.tileColsLog2), tileOffset(tc+1, d.miCols, d.tileColsLog2))
			}
			continue
		}
		var wg sync.WaitGroup
		var fail atomic.Value
		workers := make([]*Decoder, tileCols)
		sem := make(chan struct{}, min(runtime.GOMAXPROCS(0), 4))
		for tc := 0; tc < tileCols; tc++ {
			w := d.tileWorker(tc)
			*w = *d // frame state, shared slices and a private copy of the scratch state
			w.cnt = counts{}
			workers[tc] = w
			wg.Add(1)
			sem <- struct{}{}
			go func(tc int, w *Decoder) {
				defer wg.Done()
				defer func() { <-sem }()
				defer func() {
					if r := recover(); r != nil {
						fail.Store(r)
					}
				}()
				w.decodeTile(tiles[tc], rowStart, rowEnd, tileOffset(tc, d.miCols, d.tileColsLog2), tileOffset(tc+1, d.miCols, d.tileColsLog2))
			}(tc, w)
		}
		wg.Wait()
		if r := fail.Load(); r != nil {
			panic(r)
		}
		for _, w := range workers {
			d.cnt.add(&w.cnt)
		}
	}
}

// tileWorker returns a reusable decoder for tile column tc.
func (d *Decoder) tileWorker(tc int) *Decoder {
	if tc >= len(d.workers) {
		d.workers = append(d.workers, make([]*Decoder, tc+1-len(d.workers))...)
	}
	if d.workers[tc] == nil {
		d.workers[tc] = new(Decoder)
	}
	return d.workers[tc]
}

func (d *Decoder) decodeTile(data []byte, rowStart, rowEnd, colStart, colEnd int) {
	d.miRowStart, d.miRowEnd, d.miColStart, d.miColEnd = rowStart, rowEnd, colStart, colEnd
	if !d.bd.init(data) {
		panic("bad tile marker")
	}
	for r := d.miRowStart; r < d.miRowEnd; r += 8 {
		for p := 0; p < 3; p++ {
			d.leftNZ[p] = [16]uint8{}
		}
		d.leftPart = [8]uint8{}
		d.leftSegPred = [8]uint8{}
		for c := d.miColStart; c < d.miColEnd; c += 8 {
			d.decodePartition(r, c, block64x64)
		}
	}
}

func (d *Decoder) decodePartition(r, c, bsize int) {
	if r >= d.miRows || c >= d.miCols {
		return
	}
	num8x8 := int(num8x8Wide[bsize])
	half := num8x8 >> 1
	hasRows := (r + half) < d.miRows
	hasCols := (c + half) < d.miCols
	// partition context
	bsl := int(miWidthLog2[bsize])
	boffset := int(miWidthLog2[block64x64]) - bsl
	above, left := 0, 0
	for i := 0; i < num8x8; i++ {
		above |= int(d.abovePart[c+i])
		left |= int(d.leftPart[(r+i)&7])
	}
	a, l := 0, 0
	if above&(1<<uint(boffset)) != 0 {
		a = 1
	}
	if left&(1<<uint(boffset)) != 0 {
		l = 1
	}
	ctx := bsl*4 + l*2 + a
	var pr *[3]uint8
	if d.frameIsIntra {
		pr = &kfPartitionProbs[ctx]
	} else {
		pr = &d.p.partition[ctx]
	}
	var partition int
	switch {
	case hasRows && hasCols:
		partition = d.bd.tree(partitionTree, pr[:])
	case hasCols:
		if d.bd.read(pr[1]) == 1 {
			partition = partSplit
		} else {
			partition = partHorz
		}
	case hasRows:
		if d.bd.read(pr[2]) == 1 {
			partition = partSplit
		} else {
			partition = partVert
		}
	default:
		partition = partSplit
	}
	d.cnt.partition[ctx][partition]++
	subsize := int(subsizeLookup[partition][bsize])
	if subsize < block8x8 || partition == partNone {
		d.decodeBlock(r, c, subsize)
	} else if partition == partHorz {
		d.decodeBlock(r, c, subsize)
		if hasRows {
			d.decodeBlock(r+half, c, subsize)
		}
	} else if partition == partVert {
		d.decodeBlock(r, c, subsize)
		if hasCols {
			d.decodeBlock(r, c+half, subsize)
		}
	} else {
		d.decodePartition(r, c, subsize)
		d.decodePartition(r, c+half, subsize)
		d.decodePartition(r+half, c, subsize)
		d.decodePartition(r+half, c+half, subsize)
	}
	if bsize == block8x8 || partition != partSplit {
		for i := 0; i < num8x8; i++ {
			d.abovePart[c+i] = 15 >> bHeightLog2OrWidth(subsize, true)
			d.leftPart[(r+i)&7] = 15 >> bHeightLog2OrWidth(subsize, false)
		}
	}
}

func bHeightLog2OrWidth(size int, width bool) uint8 {
	if width {
		return bWidthLog2[size]
	}
	return bHeightLog2[size]
}

func (d *Decoder) decodeBlock(r, c, subsize int) {
	b := &d.blk
	*b = blockState{}
	b.miRow, b.miCol, b.miSize = r, c, subsize
	b.availU = r > 0
	b.availL = c > d.miColStart
	if d.frameIsIntra {
		d.intraFrameModeInfo()
	} else {
		d.interFrameModeInfo()
	}
	d.eobTotal = 0
	d.residual()
	if b.isInter && subsize >= block8x8 && d.eobTotal == 0 {
		b.skip = true
	}
	// store mode info
	bh := int(num8x8High[subsize])
	bw := int(num8x8Wide[subsize])
	var m modeInfo
	m.size = uint8(subsize)
	m.skip = b.skip
	m.txSize = uint8(b.txSize)
	m.yMode = uint8(b.yMode)
	m.segID = uint8(b.segID)
	m.refFrame = [2]int8{int8(b.refFrame[0]), int8(b.refFrame[1])}
	m.isInter = b.isInter
	if b.isInter {
		m.filter = uint8(b.interpFilter)
		for l := 0; l < 2; l++ {
			m.mv[l] = b.blockMvs[l][3]
			m.subMvs[l] = b.blockMvs[l]
		}
	} else {
		m.subModes = b.subModes
	}
	for y := 0; y < bh; y++ {
		if r+y >= d.miRows {
			break
		}
		for x := 0; x < bw; x++ {
			if c+x >= d.miCols {
				break
			}
			d.mi[(r+y)*d.miCols+c+x] = m
			d.segIDs[(r+y)*d.miCols+c+x] = uint8(b.segID)
		}
	}
}

func (d *Decoder) segFeatureActive(f int) bool {
	return d.seg.enabled && d.seg.featEnabled[d.blk.segID][f]
}

func (d *Decoder) readSkip() {
	b := &d.blk
	if d.segFeatureActive(segLvlSkip) {
		b.skip = true
		return
	}
	ctx := 0
	if b.availU && d.mi[(b.miRow-1)*d.miCols+b.miCol].skip {
		ctx++
	}
	if b.availL && d.mi[b.miRow*d.miCols+b.miCol-1].skip {
		ctx++
	}
	v := d.bd.read(d.p.skip[ctx])
	d.cnt.skip[ctx][v]++
	b.skip = v == 1
}

func (d *Decoder) readTxSize(allowSelect bool) {
	b := &d.blk
	maxTx := int(maxTxSize[b.miSize])
	if allowSelect && d.txMode == txModeSelect && b.miSize >= block8x8 {
		above, left := maxTx, maxTx
		if b.availU {
			m := &d.mi[(b.miRow-1)*d.miCols+b.miCol]
			if !m.skip {
				above = int(m.txSize)
			}
		}
		if b.availL {
			m := &d.mi[b.miRow*d.miCols+b.miCol-1]
			if !m.skip {
				left = int(m.txSize)
			}
		}
		if !b.availL {
			left = above
		}
		if !b.availU {
			above = left
		}
		ctx := 0
		if above+left > maxTx {
			ctx = 1
		}
		var tree []int8
		switch maxTx {
		case tx32x32:
			tree = txSize32Tree
		case tx16x16:
			tree = txSize16Tree
		default:
			tree = txSize8Tree
		}
		v := d.bd.tree(tree, d.p.tx[maxTx][ctx][:])
		d.cnt.tx[maxTx][ctx][v]++
		b.txSize = v
		return
	}
	b.txSize = maxTx
	if t := int(txModeBiggest[d.txMode]); t < b.txSize {
		b.txSize = t
	}
}

func (d *Decoder) intraFrameModeInfo() {
	b := &d.blk
	if d.seg.enabled && d.seg.updateMap {
		b.segID = d.bd.tree(segmentTree, d.seg.treeProbs[:])
	}
	d.readSkip()
	d.readTxSize(true)
	b.refFrame = [2]int{intraFrame, 0}
	b.isInter = false
	var aboveMi, leftMi *modeInfo
	if b.availU {
		aboveMi = &d.mi[(b.miRow-1)*d.miCols+b.miCol]
	}
	if b.availL {
		leftMi = &d.mi[b.miRow*d.miCols+b.miCol-1]
	}
	if b.miSize >= block8x8 {
		am, lm := dcPred, dcPred
		if aboveMi != nil {
			am = int(aboveMi.subModes[2])
		}
		if leftMi != nil {
			lm = int(leftMi.subModes[1])
		}
		mode := d.bd.tree(intraModeTree, kfYModeProbs[am][lm][:])
		b.yMode = mode
		for i := range b.subModes {
			b.subModes[i] = uint8(mode)
		}
	} else {
		n4w := int(num4x4Wide[b.miSize])
		n4h := int(num4x4High[b.miSize])
		var mode int
		for idy := 0; idy < 2; idy += n4h {
			for idx := 0; idx < 2; idx += n4w {
				var am, lm int
				if idy > 0 {
					am = int(b.subModes[idx])
				} else if aboveMi != nil {
					am = int(aboveMi.subModes[2+idx])
				}
				if idx > 0 {
					lm = int(b.subModes[idy*2])
				} else if leftMi != nil {
					lm = int(leftMi.subModes[1+idy*2])
				}
				mode = d.bd.tree(intraModeTree, kfYModeProbs[am][lm][:])
				for y2 := 0; y2 < n4h; y2++ {
					for x2 := 0; x2 < n4w; x2++ {
						b.subModes[(idy+y2)*2+idx+x2] = uint8(mode)
					}
				}
			}
		}
		b.yMode = mode
	}
	b.uvMode = d.bd.tree(intraModeTree, kfUVModeProbs[b.yMode][:])
}

func (d *Decoder) interSegmentID() {
	b := &d.blk
	if !d.seg.enabled {
		b.segID = 0
		return
	}
	// predicted segment id from the previous map
	bw := int(num8x8Wide[b.miSize])
	bh := int(num8x8High[b.miSize])
	xmis := min(d.miCols-b.miCol, bw)
	ymis := min(d.miRows-b.miRow, bh)
	pred := 7
	for y := 0; y < ymis; y++ {
		for x := 0; x < xmis; x++ {
			pred = min(pred, int(d.prevSegIDs[(b.miRow+y)*d.miCols+b.miCol+x]))
		}
	}
	if !d.seg.updateMap {
		b.segID = pred
		return
	}
	if d.seg.temporal {
		ctx := int(d.leftSegPred[b.miRow&7]) + int(d.aboveSegPred[b.miCol])
		flag := d.bd.read(d.seg.predProbs[ctx])
		if flag == 1 {
			b.segID = pred
		} else {
			b.segID = d.bd.tree(segmentTree, d.seg.treeProbs[:])
		}
		for i := 0; i < bw; i++ {
			d.aboveSegPred[b.miCol+i] = uint8(flag)
		}
		for i := 0; i < bh; i++ {
			d.leftSegPred[(b.miRow+i)&7] = uint8(flag)
		}
		return
	}
	b.segID = d.bd.tree(segmentTree, d.seg.treeProbs[:])
}

func (d *Decoder) interFrameModeInfo() {
	b := &d.blk
	b.leftRef = [2]int{intraFrame, 0}
	b.aboveRef = [2]int{intraFrame, 0}
	if b.availL {
		m := &d.mi[b.miRow*d.miCols+b.miCol-1]
		b.leftRef = [2]int{int(m.refFrame[0]), int(m.refFrame[1])}
	}
	if b.availU {
		m := &d.mi[(b.miRow-1)*d.miCols+b.miCol]
		b.aboveRef = [2]int{int(m.refFrame[0]), int(m.refFrame[1])}
	}
	b.leftIntra = b.leftRef[0] <= intraFrame
	b.aboveIntra = b.aboveRef[0] <= intraFrame
	b.leftSingle = b.leftRef[1] <= 0
	b.aboveSingle = b.aboveRef[1] <= 0
	d.interSegmentID()
	d.readSkip()
	// is_inter
	if d.segFeatureActive(segLvlRefFrame) {
		b.isInter = d.seg.featData[b.segID][segLvlRefFrame] != intraFrame
	} else {
		var ctx int
		switch {
		case b.availU && b.availL:
			if b.leftIntra && b.aboveIntra {
				ctx = 3
			} else if b.leftIntra || b.aboveIntra {
				ctx = 1
			}
		case b.availU || b.availL:
			intra := b.leftIntra
			if b.availU {
				intra = b.aboveIntra
			}
			ctx = 2 * btoi(intra)
		}
		v := d.bd.read(d.p.isInter[ctx])
		d.cnt.isInter[ctx][v]++
		b.isInter = v == 1
	}
	d.readTxSize(!b.skip || !b.isInter)
	if b.isInter {
		d.interBlockModeInfo()
	} else {
		d.intraBlockModeInfo()
	}
}

func btoi(v bool) int {
	if v {
		return 1
	}
	return 0
}

func (d *Decoder) intraBlockModeInfo() {
	b := &d.blk
	b.refFrame = [2]int{intraFrame, 0}
	if b.miSize >= block8x8 {
		ctx := int(sizeGroup[b.miSize])
		mode := d.bd.tree(intraModeTree, d.p.yMode[ctx][:])
		d.cnt.intraMode[ctx][mode]++
		b.yMode = mode
		for i := range b.subModes {
			b.subModes[i] = uint8(mode)
		}
	} else {
		n4w := int(num4x4Wide[b.miSize])
		n4h := int(num4x4High[b.miSize])
		var mode int
		for idy := 0; idy < 2; idy += n4h {
			for idx := 0; idx < 2; idx += n4w {
				mode = d.bd.tree(intraModeTree, d.p.yMode[0][:])
				d.cnt.intraMode[0][mode]++
				for y2 := 0; y2 < n4h; y2++ {
					for x2 := 0; x2 < n4w; x2++ {
						b.subModes[(idy+y2)*2+idx+x2] = uint8(mode)
					}
				}
			}
		}
		b.yMode = mode
	}
	b.uvMode = d.bd.tree(intraModeTree, d.p.uvMode[b.yMode][:])
	d.cnt.uvMode[b.yMode][b.uvMode]++
}
