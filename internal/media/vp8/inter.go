// Copyright 2011 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file (LICENSE.x-image).

package vp8

import "image"

// Inter prediction: motion vector parsing (RFC 6386 chapter 17 and section
// 16.3) and motion compensation (chapter 18).

// Reference frame identifiers.
const (
	refIntra = iota
	refLast
	refGolden
	refAlt
)

// Macroblock-level modes recorded in mbInfo.mode.
const (
	mbDC = iota
	mbV
	mbH
	mbTM
	mbB
	mbNearest
	mbNear
	mbZero
	mbNew
	mbSplit
)

type mv struct{ r, c int16 }

// mbInfo is the persistent per-macroblock mode information. The slice that
// holds them has a border row and column of zero entries (intra, zero mv),
// which is how neighbours outside the frame are seen.
type mbInfo struct {
	ref     uint8
	mode    uint8
	segment uint8
	lfLevel uint8
	lfInner bool
	mv      mv
	bmv     [16]mv
}

var modeContexts = [6][4]uint8{
	{7, 1, 1, 143},
	{14, 18, 14, 107},
	{135, 64, 57, 68},
	{60, 56, 128, 65},
	{159, 134, 128, 34},
	{234, 188, 128, 28},
}

var subMVRefProb = [5][3]uint8{
	{147, 136, 18},
	{106, 145, 1},
	{179, 121, 1},
	{223, 1, 34},
	{208, 1, 1},
}

var defaultMVProb = [2][19]uint8{
	{162, 128, 225, 146, 172, 147, 214, 39, 156, 128, 129, 132, 75, 145, 178, 206, 239, 254, 254},
	{164, 128, 204, 170, 119, 235, 140, 230, 228, 128, 130, 130, 74, 148, 180, 203, 236, 254, 254},
}

var mvUpdateProb = [2][19]uint8{
	{237, 246, 253, 253, 254, 254, 254, 254, 254, 254, 254, 254, 254, 254, 250, 250, 252, 254, 254},
	{231, 243, 245, 253, 254, 254, 254, 254, 254, 254, 254, 254, 254, 254, 251, 251, 254, 254, 254},
}

// splitPartition[s][b] is the partition index of 4x4 block b for split type s.
var splitPartition = [4][16]uint8{
	{0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 1, 1, 1, 1, 1},
	{0, 0, 1, 1, 0, 0, 1, 1, 0, 0, 1, 1, 0, 0, 1, 1},
	{0, 0, 1, 1, 0, 0, 1, 1, 2, 2, 3, 3, 2, 2, 3, 3},
	{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15},
}
var splitCount = [4]int{2, 2, 4, 16}

func (d *Decoder) readMVComponent(p *[19]uint8) int {
	x := 0
	if d.fp.readBit(p[0]) {
		for i := 0; i < 3; i++ {
			if d.fp.readBit(p[9+i]) {
				x += 1 << uint(i)
			}
		}
		for i := 9; i > 3; i-- {
			if d.fp.readBit(p[9+i]) {
				x += 1 << uint(i)
			}
		}
		if x&0xFFF0 == 0 || d.fp.readBit(p[9+3]) {
			x += 8
		}
	} else {
		// small_mvtree
		sp := p[2:9]
		var v int
		if !d.fp.readBit(sp[0]) {
			if !d.fp.readBit(sp[1]) {
				v = int(btou(d.fp.readBit(sp[2])))
			} else {
				v = 2 + int(btou(d.fp.readBit(sp[3])))
			}
		} else {
			if !d.fp.readBit(sp[4]) {
				v = 4 + int(btou(d.fp.readBit(sp[5])))
			} else {
				v = 6 + int(btou(d.fp.readBit(sp[6])))
			}
		}
		x = v
	}
	if x != 0 && d.fp.readBit(p[1]) {
		x = -x
	}
	return x
}

func (d *Decoder) readMV() mv {
	r := d.readMVComponent(&d.fc.mv[0]) * 2
	c := d.readMVComponent(&d.fc.mv[1]) * 2
	return mv{int16(r), int16(c)}
}

func (d *Decoder) clampMV(m mv, mbx, mby int) mv {
	toLeft := -(mbx * 16 << 3) - 128
	toRight := ((d.mbw-1-mbx)*16)<<3 + 128
	toTop := -(mby * 16 << 3) - 128
	toBottom := ((d.mbh-1-mby)*16)<<3 + 128
	c, r := int(m.c), int(m.r)
	if c < toLeft {
		c = toLeft
	} else if c > toRight {
		c = toRight
	}
	if r < toTop {
		r = toTop
	} else if r > toBottom {
		r = toBottom
	}
	return mv{int16(r), int16(c)}
}

func (d *Decoder) biasMV(m mv, from, to uint8) mv {
	if d.signBias[from] != d.signBias[to] {
		return mv{-m.r, -m.c}
	}
	return m
}

// parseInterMB parses the reference frame, mode and motion vectors of an
// inter-coded macroblock.
func (d *Decoder) parseInterMB(mbx, mby int, mi *mbInfo) {
	ref := uint8(refLast)
	if d.fp.readBit(d.probLast) {
		ref = refGolden
		if d.fp.readBit(d.probGF) {
			ref = refAlt
		}
	}
	idx := (mby+1)*d.miStride + mbx + 1
	above, left, al := &d.mbs[idx-d.miStride], &d.mbs[idx-1], &d.mbs[idx-d.miStride-1]
	var near [4]mv
	var cnt [4]int
	n := 0
	if above.ref != refIntra {
		if above.mv != (mv{}) {
			n++
			near[n] = d.biasMV(above.mv, above.ref, ref)
			cnt[n] += 2
		} else {
			cnt[0] += 2
		}
	}
	if left.ref != refIntra {
		if left.mv != (mv{}) {
			t := d.biasMV(left.mv, left.ref, ref)
			if t != near[n] {
				n++
				near[n] = t
			}
			cnt[n] += 2
		} else {
			cnt[0] += 2
		}
	}
	if al.ref != refIntra {
		if al.mv != (mv{}) {
			t := d.biasMV(al.mv, al.ref, ref)
			if t != near[n] {
				n++
				near[n] = t
			}
			cnt[n]++
		} else {
			cnt[0]++
		}
	}
	if cnt[3] > 0 && near[n] == near[1] {
		cnt[1]++
	}
	cnt[3] = 0
	if above.mode == mbSplit {
		cnt[3] += 2
	}
	if left.mode == mbSplit {
		cnt[3] += 2
	}
	if al.mode == mbSplit {
		cnt[3]++
	}
	if cnt[2] > cnt[1] {
		cnt[1], cnt[2] = cnt[2], cnt[1]
		near[1], near[2] = near[2], near[1]
	}
	if cnt[1] >= cnt[0] {
		near[0] = near[1]
	}
	best := d.clampMV(near[0], mbx, mby)

	mi.ref = ref
	switch {
	case !d.fp.readBit(modeContexts[cnt[0]][0]):
		mi.mode = mbZero
		mi.mv = mv{}
	case !d.fp.readBit(modeContexts[cnt[1]][1]):
		mi.mode = mbNearest
		mi.mv = d.clampMV(near[1], mbx, mby)
	case !d.fp.readBit(modeContexts[cnt[2]][2]):
		mi.mode = mbNear
		mi.mv = d.clampMV(near[2], mbx, mby)
	case !d.fp.readBit(modeContexts[cnt[3]][3]):
		mi.mode = mbNew
		m := d.readMV()
		mi.mv = mv{m.r + best.r, m.c + best.c}
	default:
		mi.mode = mbSplit
		d.parseSplitMV(mi, left, above, best)
		mi.mv = mi.bmv[15]
	}
}

func (d *Decoder) parseSplitMV(mi, left, above *mbInfo, best mv) {
	s := 3
	if d.fp.readBit(110) {
		s = 2
		if d.fp.readBit(111) {
			s = int(btou(d.fp.readBit(150)))
		}
	}
	part := &splitPartition[s]
	n := splitCount[s]
	for j := 0; j < n; j++ {
		k := 0
		for part[k] != uint8(j) {
			k++
		}
		var l, a mv
		if k&3 != 0 {
			l = mi.bmv[k-1]
		} else if left.mode != mbSplit {
			l = left.mv
		} else {
			l = left.bmv[k+3]
		}
		if k>>2 != 0 {
			a = mi.bmv[k-4]
		} else if above.mode != mbSplit {
			a = above.mv
		} else {
			a = above.bmv[k+12]
		}
		ctx := 0
		switch {
		case l == a && a == (mv{}):
			ctx = 4
		case l == a:
			ctx = 3
		case a == (mv{}):
			ctx = 2
		case l == (mv{}):
			ctx = 1
		}
		p := &subMVRefProb[ctx]
		var m mv
		switch {
		case !d.fp.readBit(p[0]):
			m = l
		case !d.fp.readBit(p[1]):
			m = a
		case !d.fp.readBit(p[2]):
			m = mv{}
		default:
			r := d.readMV()
			m = mv{r.r + best.r, r.c + best.c}
		}
		for b := 0; b < 16; b++ {
			if part[b] == uint8(j) {
				mi.bmv[b] = m
			}
		}
	}
}

// ---- motion compensation -------------------------------------------------------

var sixtap = [8][6]int32{
	{0, 0, 128, 0, 0, 0},
	{0, -6, 123, 12, -1, 0},
	{2, -11, 108, 36, -8, 1},
	{0, -9, 93, 50, -6, 0},
	{3, -16, 77, 77, -16, 3},
	{0, -6, 50, 93, -9, 0},
	{1, -8, 36, 108, -11, 2},
	{0, -1, 12, 123, -6, 0},
}

func clampPix(v int32) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

// mcBlock predicts a bw x bh block whose top-left corner in the reference
// plane is (x, y) plus the eighth-pel fraction (fx, fy), writing it to the
// workspace at (dy, dx). Pixels outside the plane replicate the border.
func (d *Decoder) mcBlock(dy, dx int, pl []byte, stride, pw, ph, x, y, fx, fy, bw, bh int) {
	src := pl
	sstride := stride
	base := y*stride + x
	if x < 2 || y < 2 || x+bw+3 > pw || y+bh+3 > ph {
		// Build a clamped copy of the (bw+5) x (bh+5) window.
		ww := bw + 5
		tmp := d.mcWin[:ww*(bh+5)]
		for j := 0; j < bh+5; j++ {
			yy := y - 2 + j
			if yy < 0 {
				yy = 0
			} else if yy >= ph {
				yy = ph - 1
			}
			row := pl[yy*stride : yy*stride+pw]
			for i := 0; i < ww; i++ {
				xx := x - 2 + i
				if xx < 0 {
					xx = 0
				} else if xx >= pw {
					xx = pw - 1
				}
				tmp[j*ww+i] = row[xx]
			}
		}
		src, sstride, base = tmp, ww, 2*ww+2
	}
	if fx == 0 && fy == 0 {
		for j := 0; j < bh; j++ {
			copy(d.ybr[dy+j][dx:dx+bw], src[base+j*sstride:base+j*sstride+bw])
		}
		return
	}
	if d.bilinear {
		d.mcBilinear(dy, dx, src, sstride, base, fx, fy, bw, bh)
		return
	}
	if fy == 0 {
		f := &sixtap[fx]
		for j := 0; j < bh; j++ {
			s := src[base+j*sstride-2:]
			o := d.ybr[dy+j][dx:]
			for i := 0; i < bw; i++ {
				v := int32(s[i])*f[0] + int32(s[i+1])*f[1] + int32(s[i+2])*f[2] + int32(s[i+3])*f[3] + int32(s[i+4])*f[4] + int32(s[i+5])*f[5]
				o[i] = clampPix((v + 64) >> 7)
			}
		}
		return
	}
	if fx == 0 {
		f := &sixtap[fy]
		for j := 0; j < bh; j++ {
			o := d.ybr[dy+j][dx:]
			b := base + j*sstride
			for i := 0; i < bw; i++ {
				p := b + i
				v := int32(src[p-2*sstride])*f[0] + int32(src[p-sstride])*f[1] + int32(src[p])*f[2] + int32(src[p+sstride])*f[3] + int32(src[p+2*sstride])*f[4] + int32(src[p+3*sstride])*f[5]
				o[i] = clampPix((v + 64) >> 7)
			}
		}
		return
	}
	// Two-pass: horizontal over bh+5 rows, then vertical.
	f := &sixtap[fx]
	t := d.mcTmp[:(bh+5)*bw]
	for j := 0; j < bh+5; j++ {
		s := src[base+(j-2)*sstride-2:]
		o := t[j*bw:]
		for i := 0; i < bw; i++ {
			v := int32(s[i])*f[0] + int32(s[i+1])*f[1] + int32(s[i+2])*f[2] + int32(s[i+3])*f[3] + int32(s[i+4])*f[4] + int32(s[i+5])*f[5]
			o[i] = clampPix((v + 64) >> 7)
		}
	}
	g := &sixtap[fy]
	for j := 0; j < bh; j++ {
		o := d.ybr[dy+j][dx:]
		for i := 0; i < bw; i++ {
			p := (j+2)*bw + i
			v := int32(t[p-2*bw])*g[0] + int32(t[p-bw])*g[1] + int32(t[p])*g[2] + int32(t[p+bw])*g[3] + int32(t[p+2*bw])*g[4] + int32(t[p+3*bw])*g[5]
			o[i] = clampPix((v + 64) >> 7)
		}
	}
}

func (d *Decoder) mcBilinear(dy, dx int, src []byte, sstride, base, fx, fy, bw, bh int) {
	f0, f1 := int32(128-fx*16), int32(fx*16)
	g0, g1 := int32(128-fy*16), int32(fy*16)
	t := d.mcTmp[:(bh+1)*bw]
	for j := 0; j < bh+1; j++ {
		s := src[base+j*sstride:]
		for i := 0; i < bw; i++ {
			t[j*bw+i] = uint8((int32(s[i])*f0 + int32(s[i+1])*f1 + 64) >> 7)
		}
	}
	for j := 0; j < bh; j++ {
		o := d.ybr[dy+j][dx:]
		for i := 0; i < bw; i++ {
			o[i] = uint8((int32(t[j*bw+i])*g0 + int32(t[(j+1)*bw+i])*g1 + 64) >> 7)
		}
	}
}

// predictInter fills the workspace with the inter prediction of a macroblock.
func (d *Decoder) predictInter(mbx, mby int, mi *mbInfo) {
	var ref *image.YCbCr
	switch mi.ref {
	case refLast:
		ref = d.last
	case refGolden:
		ref = d.golden
	default:
		ref = d.alt
	}
	pw, ph := d.mbw*16, d.mbh*16
	cw, ch := pw/2, ph/2
	ys, cs := ref.YStride, ref.CStride
	luma := func(by, bx, w, h int, m mv) {
		d.mcBlock(ybrYY+by, ybrYX+bx, ref.Y, ys, pw, ph, mbx*16+bx+int(m.c>>3), mby*16+by+int(m.r>>3), int(m.c&7), int(m.r&7), w, h)
	}
	chroma := func(by, bx, w, h int, m mv) {
		x := mbx*8 + bx + int(m.c>>3)
		y := mby*8 + by + int(m.r>>3)
		d.mcBlock(ybrBY+by, ybrBX+bx, ref.Cb, cs, cw, ch, x, y, int(m.c&7), int(m.r&7), w, h)
		d.mcBlock(ybrRY+by, ybrRX+bx, ref.Cr, cs, cw, ch, x, y, int(m.c&7), int(m.r&7), w, h)
	}
	fullMask := int16(-1)
	if d.fullPixel {
		fullMask = ^7
	}
	if mi.mode != mbSplit {
		luma(0, 0, 16, 16, mi.mv)
		r, c := int(mi.mv.r), int(mi.mv.c)
		r = (r + (1 | r>>31)) / 2
		c = (c + (1 | c>>31)) / 2
		chroma(0, 0, 8, 8, mv{int16(r) & fullMask, int16(c) & fullMask})
		return
	}
	// Luma in 8x8 quadrants when the split is coarse, else in 4x4 blocks.
	coarse := true
	for q := 0; q < 4 && coarse; q++ {
		b := (q>>1)*8 + (q&1)*2
		if mi.bmv[b] != mi.bmv[b+1] || mi.bmv[b] != mi.bmv[b+4] || mi.bmv[b] != mi.bmv[b+5] {
			coarse = false
		}
	}
	if coarse {
		for q := 0; q < 4; q++ {
			luma((q>>1)*8, (q&1)*8, 8, 8, mi.bmv[(q>>1)*8+(q&1)*2])
		}
	} else {
		for b := 0; b < 16; b++ {
			luma((b>>2)*4, (b&3)*4, 4, 4, mi.bmv[b])
		}
	}
	for j := 0; j < 2; j++ {
		for i := 0; i < 2; i++ {
			b := j*8 + i*2
			sr := int(mi.bmv[b].r) + int(mi.bmv[b+1].r) + int(mi.bmv[b+4].r) + int(mi.bmv[b+5].r)
			sc := int(mi.bmv[b].c) + int(mi.bmv[b+1].c) + int(mi.bmv[b+4].c) + int(mi.bmv[b+5].c)
			sr += 4 + (sr>>31)*8
			sc += 4 + (sc>>31)*8
			chroma(j*4, i*4, 4, 4, mv{int16(sr/8) & fullMask, int16(sc/8) & fullMask})
		}
	}
}
