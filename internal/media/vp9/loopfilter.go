package vp9

import (
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/sweefbrowser/sweef/internal/media/wavefront"
)

// Loop filter (spec 8.8).

func (d *Decoder) loopFilterFrameInit() {
	nShift := uint(d.lfLevel >> 5)
	for seg := 0; seg < 8; seg++ {
		lvlSeg := d.lfLevel
		if d.seg.enabled && d.seg.featEnabled[seg][segLvlAltL] {
			if d.seg.absDelta {
				lvlSeg = d.seg.featData[seg][segLvlAltL]
			} else {
				lvlSeg = d.seg.featData[seg][segLvlAltL] + d.lfLevel
			}
			lvlSeg = clip3(0, 63, lvlSeg)
		}
		if !d.lfDeltaEnabled {
			for r := 0; r < 4; r++ {
				for m := 0; m < 2; m++ {
					d.lvlLookup[seg][r][m] = uint8(lvlSeg)
				}
			}
			continue
		}
		intra := lvlSeg + (d.lfRefDeltas[intraFrame] << nShift)
		d.lvlLookup[seg][intraFrame][0] = uint8(clip3(0, 63, intra))
		d.lvlLookup[seg][intraFrame][1] = d.lvlLookup[seg][intraFrame][0]
		for r := lastFrame; r < 4; r++ {
			for m := 0; m < 2; m++ {
				l := lvlSeg + (d.lfRefDeltas[r] << nShift) + (d.lfModeDeltas[m] << nShift)
				d.lvlLookup[seg][r][m] = uint8(clip3(0, 63, l))
			}
		}
	}
}

// loopFilterFrame filters the whole frame. The result is exactly that of filtering superblock by
// superblock in raster order, plane by plane: the planes are independent, and inside a plane
// superblock (r, c) only needs its left neighbour and the superblock above and to the right (whose
// left edge filtering reaches into the pixels this one's top edge reads) to be finished, so rows run in
// parallel as a wavefront two superblocks apart.
func (d *Decoder) loopFilterFrame() {
	d.loopFilterFrameInit()
	rows, cols := (d.miRows+7)/8, (d.miCols+7)/8
	workers := min(runtime.GOMAXPROCS(0), 4)
	if workers < 2 || rows*cols < 8 {
		for row := 0; row < d.miRows; row += 8 {
			for col := 0; col < d.miCols; col += 8 {
				for plane := 0; plane < 3; plane++ {
					for pass := 0; pass < 2; pass++ {
						d.loopFilterSB(plane, pass, row, col)
					}
				}
			}
		}
		return
	}
	progress := wavefront.New(3 * rows) // superblocks finished per (plane, row)
	var next atomic.Int32
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				t := int(next.Add(1)) - 1
				if t >= 3*rows {
					return
				}
				plane, r := t/rows, t%rows
				for c := 0; c < cols; c++ {
					if r > 0 {
						progress.Wait(t-1, min(c+2, cols))
					}
					for pass := 0; pass < 2; pass++ {
						d.loopFilterSB(plane, pass, r*8, c*8)
					}
					progress.Done(t, c+1)
				}
			}
		}()
	}
	wg.Wait()
}

func (d *Decoder) loopFilterSB(plane, pass, row, col int) {
	subX, subY := 0, 0
	if plane > 0 {
		subX, subY = d.subX, d.subY
	}
	var sub, edgeLen int
	if pass == 0 {
		sub = subX
		edgeLen = 64 >> uint(subY)
	} else {
		sub = subY
		edgeLen = 64 >> uint(subX)
	}
	fr := d.cur
	stride := fr.stride[plane]
	pix := fr.pix[plane]
	nEdges := 16 >> uint(sub)
	for edge := 0; edge < nEdges; edge++ {
		for seg := 0; seg < edgeLen; seg += 8 {
			var x, y int
			if pass == 0 {
				x = col*8 + edge*(4<<uint(subX))
				y = row*8 + (seg << uint(subY))
			} else {
				x = col*8 + (seg << uint(subX))
				y = row*8 + edge*(4<<uint(subY))
			}
			if x >= 8*d.miCols || y >= 8*d.miRows {
				continue
			}
			if (pass == 0 && x == 0) || (pass == 1 && y == 0) {
				continue
			}
			// samples of this segment that lie on screen
			var limit int
			if pass == 0 {
				limit = (8*d.miRows - row*8 + (1 << uint(subY)) - 1) >> uint(subY)
			} else {
				limit = (8*d.miCols - col*8 + (1 << uint(subX)) - 1) >> uint(subX)
			}
			cnt := min(8, limit-seg)
			if cnt <= 0 {
				continue
			}
			loopCol := ((x >> 3) >> uint(subX)) << uint(subX)
			loopRow := ((y >> 3) >> uint(subY)) << uint(subY)
			mi := &d.mi[loopRow*d.miCols+loopCol]
			miSize := int(mi.size)
			txSz := int(mi.txSize)
			if plane > 0 {
				if miSize < block8x8 {
					txSz = tx4x4
				} else {
					txSz = min(txSz, int(maxTxSize[ssSizeLookup[miSize][d.subX][d.subY]]))
				}
			}
			sbSize := miSize
			if sub == 1 && sbSize < block16x16 {
				sbSize = block16x16
			}
			isIntra := mi.refFrame[0] <= intraFrame
			var isBlockEdge bool
			if pass == 0 {
				isBlockEdge = x%(8*int(num8x8Wide[sbSize])) == 0
			} else {
				isBlockEdge = y%(8*int(num8x8High[sbSize])) == 0
			}
			var isTxEdge bool
			if pass == 1 && subX == 1 && d.miCols&1 == 1 && edge&1 == 1 && x+8 >= d.miCols*8 {
				isTxEdge = false
			} else {
				isTxEdge = edge%(1<<uint(txSz)) == 0
			}
			is32Edge := edge%8 == 0
			apply := isBlockEdge || (isTxEdge && (isIntra || !mi.skip))
			if !apply {
				continue
			}
			// filter size
			baseSize := min(tx16x16, txSz)
			if txSz == tx4x4 && is32Edge {
				baseSize = tx8x8
			}
			filterSize := baseSize
			if pass == 0 && subX == 1 && baseSize == tx16x16 && x>>3 == d.miCols-1 {
				filterSize = tx8x8
			} else if pass == 1 && subY == 1 && baseSize == tx16x16 && y>>3 == d.miRows-1 {
				filterSize = tx8x8
			}
			// adaptive filter strength
			mode := int(mi.yMode)
			modeType := 0
			if mode == nearestMv || mode == nearMv || mode == newMv {
				modeType = 1
			}
			lvl := int(d.lvlLookup[mi.segID][mi.refFrame[0]][modeType])
			if lvl == 0 {
				continue
			}
			shift := uint(0)
			if d.lfSharp > 4 {
				shift = 2
			} else if d.lfSharp > 0 {
				shift = 1
			}
			var lim int
			if d.lfSharp > 0 {
				lim = clip3(1, 9-d.lfSharp, lvl>>shift)
			} else {
				lim = max(1, lvl>>shift)
			}
			blimit := 2*(lvl+2) + lim
			thresh := lvl >> 4
			px := x >> uint(subX)
			py := y >> uint(subY)
			var pos, across, along int
			if pass == 0 {
				pos = py*stride + px
				across, along = 1, stride
			} else {
				pos = py*stride + px
				across, along = stride, 1
			}
			filterSegment(pix, uint(d.bitDepth), pos, across, along, cnt, lim, blimit, thresh, filterSize)
		}
	}
}

func abs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

// clampS clamps v to lo..hi (the signed sample range of the filter arithmetic).
func clampS(v, lo, hi int32) int32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// filterSegment filters cnt samples along an edge (spec 8.8.2 for each of them): pos is the first sample on the
// q side, across the step across the edge and along the step along it. The thresholds are computed once for
// the segment; the 8-sample filter is written out and the 16-sample one slides a window sum.
func filterSegment(pix []uint16, bd uint, pos, across, along, cnt, limit, blimit, thresh, filterSize int) {
	sh := bd - 8
	lim := int32(limit) << sh
	blim := int32(blimit) << sh
	thr := int32(thresh) << sh
	one := int32(1) << sh
	off := int32(128) << sh
	lo, hi := -off, off-1
	for k := 0; k < cnt; k++ {
		pos := pos + k*along
		q0 := int32(pix[pos])
		q1 := int32(pix[pos+across])
		q2 := int32(pix[pos+2*across])
		q3 := int32(pix[pos+3*across])
		p0 := int32(pix[pos-across])
		p1 := int32(pix[pos-2*across])
		p2 := int32(pix[pos-3*across])
		p3 := int32(pix[pos-4*across])
		dp1p0, dq1q0 := abs32(p1-p0), abs32(q1-q0)
		if abs32(p3-p2) > lim || abs32(p2-p1) > lim || dp1p0 > lim || dq1q0 > lim || abs32(q2-q1) > lim || abs32(q3-q2) > lim ||
			abs32(p0-q0)*2+abs32(p1-q1)/2 > blim {
			continue
		}
		flat := filterSize >= tx8x8 && dp1p0 <= one && dq1q0 <= one && abs32(p2-p0) <= one && abs32(q2-q0) <= one && abs32(p3-p0) <= one && abs32(q3-q0) <= one
		if !flat {
			hev := dp1p0 > thr || dq1q0 > thr
			ps1, ps0, qs0, qs1 := p1-off, p0-off, q0-off, q1-off
			var f int32
			if hev {
				f = clampS(ps1-qs1, lo, hi)
			}
			f = clampS(f+3*(qs0-ps0), lo, hi)
			f1 := clampS(f+4, lo, hi) >> 3
			f2 := clampS(f+3, lo, hi) >> 3
			pix[pos] = uint16(clampS(qs0-f1, lo, hi) + off)
			pix[pos-across] = uint16(clampS(ps0+f2, lo, hi) + off)
			if !hev {
				f = (f1 + 1) >> 1
				pix[pos+across] = uint16(clampS(qs1-f, lo, hi) + off)
				pix[pos-2*across] = uint16(clampS(ps1+f, lo, hi) + off)
			}
			continue
		}
		flat2 := false
		if filterSize >= tx16x16 {
			flat2 = true
			for j := 4; j < 8; j++ {
				if abs32(int32(pix[pos-(j+1)*across])-p0) > one || abs32(int32(pix[pos+j*across])-q0) > one {
					flat2 = false
					break
				}
			}
		}
		if filterSize == tx8x8 || !flat2 {
			// the 8-sample filter: output i is the rounded mean of the sample i and the seven around it
			pix[pos-3*across] = uint16((3*p3 + 2*p2 + p1 + p0 + q0 + 4) >> 3)
			pix[pos-2*across] = uint16((2*p3 + p2 + 2*p1 + p0 + q0 + q1 + 4) >> 3)
			pix[pos-across] = uint16((p3 + p2 + p1 + 2*p0 + q0 + q1 + q2 + 4) >> 3)
			pix[pos] = uint16((p2 + p1 + p0 + 2*q0 + q1 + q2 + q3 + 4) >> 3)
			pix[pos+across] = uint16((p1 + p0 + q0 + 2*q1 + q2 + 2*q3 + 4) >> 3)
			pix[pos+2*across] = uint16((p0 + q0 + q1 + 2*q2 + 3*q3 + 4) >> 3)
		} else {
			wide16(pix, pos, across)
		}
	}
}

// wide16 applies the 16-sample smoothing filter across an edge: output i is the rounded mean of sample i and
// the 15 samples around it (the edge samples repeated beyond the filtered range). A sliding window sum makes
// it linear.
func wide16(pix []uint16, pos, across int) {
	var s [32]int32 // s[k+16] = the sample at offset k, k in [-8, 7]; the edge values repeated outside
	for k := -8; k <= 7; k++ {
		s[k+16] = int32(pix[pos+k*across])
	}
	for k := 0; k < 8; k++ {
		s[k] = s[8]
		s[24+k] = s[23]
	}
	var w int32
	for k := -14; k <= 0; k++ {
		w += s[k+16]
	}
	var f [14]int32
	for i := -7; i < 7; i++ {
		f[i+7] = (w + s[i+16] + 8) >> 4
		w += s[i+8+16] - s[i-7+16]
	}
	for i := -7; i < 7; i++ {
		pix[pos+i*across] = uint16(f[i+7])
	}
}
