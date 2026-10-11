package hevc

// Inter prediction: prediction unit syntax, merge and AMVP motion vector
// derivation, temporal motion vector prediction.

type mv struct{ x, y int16 }

func (d *Decoder) interPUs(xCb, yCb, nCbS int) error {
	cu := &d.cu
	h := nCbS >> 1
	q := nCbS >> 2
	type rect struct{ x, y, w, h int }
	var r [4]rect
	n := 0
	switch cu.partMode {
	case part2Nx2N:
		r[0] = rect{0, 0, nCbS, nCbS}
		n = 1
	case part2NxN:
		r[0], r[1] = rect{0, 0, nCbS, h}, rect{0, h, nCbS, h}
		n = 2
	case partNx2N:
		r[0], r[1] = rect{0, 0, h, nCbS}, rect{h, 0, h, nCbS}
		n = 2
	case part2NxnU:
		r[0], r[1] = rect{0, 0, nCbS, q}, rect{0, q, nCbS, nCbS - q}
		n = 2
	case part2NxnD:
		r[0], r[1] = rect{0, 0, nCbS, nCbS - q}, rect{0, nCbS - q, nCbS, q}
		n = 2
	case partnLx2N:
		r[0], r[1] = rect{0, 0, q, nCbS}, rect{q, 0, nCbS - q, nCbS}
		n = 2
	case partnRx2N:
		r[0], r[1] = rect{0, 0, nCbS - q, nCbS}, rect{nCbS - q, 0, q, nCbS}
		n = 2
	case partNxN:
		r[0], r[1], r[2], r[3] = rect{0, 0, h, h}, rect{h, 0, h, h}, rect{0, h, h, h}, rect{h, h, h, h}
		n = 4
	}
	for i := 0; i < n; i++ {
		if err := d.predictionUnit(xCb, yCb, nCbS, xCb+r[i].x, yCb+r[i].y, r[i].w, r[i].h, i, false); err != nil {
			return err
		}
	}
	return nil
}

func (d *Decoder) parseMvd() (int, int) {
	c := &d.cab
	gx := c.bin(ctxMvdGt0) != 0
	gy := c.bin(ctxMvdGt0) != 0
	g1x, g1y := false, false
	if gx {
		g1x = c.bin(ctxMvdGt1) != 0
	}
	if gy {
		g1y = c.bin(ctxMvdGt1) != 0
	}
	rd := func(g, g1 bool) int {
		if !g {
			return 0
		}
		v := 1
		if g1 {
			// abs_mvd_minus2: EG1
			k := 1
			e := 0
			for c.bypass() != 0 {
				e += 1 << uint(k)
				k++
				if k > 30 {
					break
				}
			}
			e += c.bypassBits(k)
			v = 2 + e
		}
		if c.bypass() != 0 {
			v = -v
		}
		return v
	}
	x := rd(gx, g1x)
	y := rd(gy, g1y)
	return x, y
}

func (d *Decoder) parseRefIdx(num int) int {
	if num <= 1 {
		return 0
	}
	c := &d.cab
	cMax := num - 1
	v := 0
	for v < cMax {
		var b int
		if v < 2 {
			b = c.bin(ctxRefIdx + v)
		} else {
			b = c.bypass()
		}
		if b == 0 {
			break
		}
		v++
	}
	return v
}

func (d *Decoder) predictionUnit(xCb, yCb, nCbS, xPb, yPb, nPbW, nPbH, partIdx int, skip bool) error {
	sh := d.sh
	c := &d.cab
	merge := skip
	if !skip {
		merge = c.bin(ctxMergeFlag) != 0
	}
	if partIdx == 0 {
		d.lastMerge = merge
	}
	var mf mvField
	if merge {
		idx := 0
		if sh.maxMergeCand > 1 {
			if c.bin(ctxMergeIdx) != 0 {
				idx = 1
				for idx < sh.maxMergeCand-1 && c.bypass() != 0 {
					idx++
				}
			}
		}
		mf = d.mergeCand(xCb, yCb, nCbS, xPb, yPb, nPbW, nPbH, partIdx, idx)
	} else {
		idc := 0 // 0: L0, 1: L1, 2: BI
		if sh.sliceType == sliceB {
			if nPbW+nPbH != 12 {
				depth := int(d.cuAt(xCb, yCb).depth)
				if c.bin(ctxInterPredIdc+depth) != 0 {
					idc = 2
				} else {
					idc = c.bin(ctxInterPredIdc + 4)
				}
			} else {
				idc = c.bin(ctxInterPredIdc + 4)
			}
		}
		var refIdx [2]int
		var mvd [2][2]int
		var mvp [2]int
		for X := 0; X < 2; X++ {
			if (X == 0 && idc == 1) || (X == 1 && idc == 0) {
				continue
			}
			refIdx[X] = d.parseRefIdx(sh.numRefIdx[X])
			if X == 1 && sh.mvdL1Zero && idc == 2 {
				mvd[1] = [2]int{0, 0}
			} else {
				mvd[X][0], mvd[X][1] = d.parseMvd()
			}
			mvp[X] = c.bin(ctxMvp)
		}
		for X := 0; X < 2; X++ {
			if (X == 0 && idc == 1) || (X == 1 && idc == 0) {
				continue
			}
			if refIdx[X] >= sh.numRefIdx[X] {
				return errBitstream
			}
			p := d.amvp(xCb, yCb, nCbS, xPb, yPb, nPbW, nPbH, partIdx, X, refIdx[X], mvp[X])
			mf.pf |= 1 << uint(X)
			mf.ref[X] = int8(refIdx[X])
			mf.mv[X][0] = p.x + int16(mvd[X][0])
			mf.mv[X][1] = p.y + int16(mvd[X][1])
		}
	}
	// fill in POC / long-term info
	for X := 0; X < 2; X++ {
		if mf.pf&(1<<uint(X)) != 0 {
			if int(mf.ref[X]) >= sh.numRefIdx[X] {
				return errBitstream
			}
			mf.poc[X] = sh.refPoc[X][mf.ref[X]]
			if sh.refLT[X][mf.ref[X]] {
				mf.lt |= 1 << uint(X)
			}
		} else {
			mf.mv[X] = [2]int16{}
			mf.ref[X] = -1
		}
	}
	// store
	pic := d.cur
	s := d.sp
	_ = s
	for y := yPb >> 2; y < (yPb+nPbH)>>2; y++ {
		row := pic.mvf[y*pic.w4+xPb>>2 : y*pic.w4+(xPb+nPbW)>>2]
		for i := range row {
			row[i] = mf
		}
	}
	d.markEdges(xPb, yPb, nPbW, nPbH, 2)
	d.predictInter(xPb, yPb, nPbW, nPbH, &mf)
	return nil
}

// availPB is the availability process for prediction blocks (6.4.2).
func (d *Decoder) availPB(xCb, yCb, nCbS, xPb, yPb, nPbW, nPbH, partIdx, xN, yN int) *mvField {
	sameCb := xCb <= xN && yCb <= yN && xCb+nCbS > xN && yCb+nCbS > yN
	if !sameCb {
		if !d.availZ(xPb, yPb, xN, yN) {
			return nil
		}
	} else if nPbW<<1 == nCbS && nPbH<<1 == nCbS && partIdx == 1 && yCb+nPbH <= yN && xCb+nPbW > xN {
		return nil
	}
	m := &d.cur.mvf[(yN>>2)*d.cur.w4+xN>>2]
	if m.pf == 0 {
		return nil
	}
	return m
}

func sameMotion(a, b *mvField) bool {
	if a.pf != b.pf {
		return false
	}
	for l := 0; l < 2; l++ {
		if a.pf&(1<<uint(l)) != 0 && (a.mv[l] != b.mv[l] || a.ref[l] != b.ref[l]) {
			return false
		}
	}
	return true
}

func (d *Decoder) mergeCand(xCb, yCb, nCbS, xPb, yPb, nPbW, nPbH, partIdx, mergeIdx int) mvField {
	sh, pp := d.sh, d.pp
	origW, origH := nPbW, nPbH
	partMode := d.cu.partMode
	if pp.log2ParMrgLevel > 2 && nCbS == 8 {
		xPb, yPb, nPbW, nPbH, partIdx = xCb, yCb, nCbS, nCbS, 0
		partMode = part2Nx2N
	}
	pml := uint(pp.log2ParMrgLevel)
	var list [6]mvField
	n := 0
	par := func(xN, yN int) bool {
		return xPb>>pml == xN>>pml && yPb>>pml == yN>>pml
	}
	get := func(xN, yN int) *mvField {
		if par(xN, yN) {
			return nil
		}
		return d.availPB(xCb, yCb, nCbS, xPb, yPb, nPbW, nPbH, partIdx, xN, yN)
	}
	done := func() bool { return n > mergeIdx }
	var a1, b1, b0, a0 *mvField
	fA1, fB1, fB0, fA0 := false, false, false, false
	if !(partIdx == 1 && (partMode == partNx2N || partMode == partnLx2N || partMode == partnRx2N)) {
		a1 = get(xPb-1, yPb+nPbH-1)
	}
	if a1 != nil {
		list[n] = *a1
		n++
		fA1 = true
	}
	if !done() {
		if !(partIdx == 1 && (partMode == part2NxN || partMode == part2NxnU || partMode == part2NxnD)) {
			b1 = get(xPb+nPbW-1, yPb-1)
		}
		if b1 != nil && !(a1 != nil && sameMotion(a1, b1)) {
			list[n] = *b1
			n++
			fB1 = true
		}
	}
	if !done() {
		b0 = get(xPb+nPbW, yPb-1)
		if b0 != nil && !(b1 != nil && sameMotion(b1, b0)) {
			list[n] = *b0
			n++
			fB0 = true
		}
	}
	if !done() {
		a0 = get(xPb-1, yPb+nPbH)
		if a0 != nil && !(a1 != nil && sameMotion(a1, a0)) {
			list[n] = *a0
			n++
			fA0 = true
		}
	}
	if !done() && !(fA0 && fA1 && fB0 && fB1) {
		b2 := get(xPb-1, yPb-1)
		if b2 != nil && !(a1 != nil && sameMotion(a1, b2)) && !(b1 != nil && sameMotion(b1, b2)) {
			list[n] = *b2
			n++
		}
	}
	// temporal candidate
	if !done() && sh.temporalMvp {
		var t mvField
		ok := false
		if m, o := d.temporalMv(xPb, yPb, nPbW, nPbH, 0, 0); o {
			t.pf |= 1
			t.mv[0] = [2]int16{m.x, m.y}
			t.ref[0] = 0
			ok = true
		}
		if sh.sliceType == sliceB {
			if m, o := d.temporalMv(xPb, yPb, nPbW, nPbH, 0, 1); o {
				t.pf |= 2
				t.mv[1] = [2]int16{m.x, m.y}
				t.ref[1] = 0
				ok = true
			}
		}
		if ok {
			if t.pf&1 == 0 {
				t.ref[0] = -1
			}
			if t.pf&2 == 0 {
				t.ref[1] = -1
			}
			list[n] = t
			n++
		}
	}
	// combined bi-predictive candidates
	if !done() && sh.sliceType == sliceB && n > 1 && n < sh.maxMergeCand {
		orig := n
		l0 := [12]int{0, 1, 0, 2, 1, 2, 0, 3, 1, 3, 2, 3}
		l1 := [12]int{1, 0, 2, 0, 2, 1, 3, 0, 3, 1, 3, 2}
		for comb := 0; comb < orig*(orig-1) && n < sh.maxMergeCand; comb++ {
			c0, c1 := &list[l0[comb]], &list[l1[comb]]
			if c0.pf&1 != 0 && c1.pf&2 != 0 {
				p0 := sh.refPoc[0][c0.ref[0]]
				p1 := sh.refPoc[1][c1.ref[1]]
				if p0 != p1 || c0.mv[0] != c1.mv[1] {
					var m mvField
					m.pf = 3
					m.mv[0], m.mv[1] = c0.mv[0], c1.mv[1]
					m.ref[0], m.ref[1] = c0.ref[0], c1.ref[1]
					list[n] = m
					n++
				}
			}
		}
	}
	// zero candidates
	if !done() {
		numRef := sh.numRefIdx[0]
		if sh.sliceType == sliceB && sh.numRefIdx[1] < numRef {
			numRef = sh.numRefIdx[1]
		}
		zero := 0
		for n <= mergeIdx {
			var m mvField
			r := 0
			if zero < numRef {
				r = zero
			}
			m.pf = 1
			m.ref[0] = int8(r)
			m.ref[1] = -1
			if sh.sliceType == sliceB {
				m.pf = 3
				m.ref[1] = int8(r)
			}
			list[n] = m
			n++
			zero++
		}
	}
	m := list[mergeIdx]
	if m.pf == 3 && origW+origH == 12 {
		m.pf = 1
		m.ref[1] = -1
		m.mv[1] = [2]int16{}
	}
	return m
}

func clip3(lo, hi, v int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func scaleMv(m mv, td, tb int) mv {
	td = clip3(-128, 127, td)
	tb = clip3(-128, 127, tb)
	if td == 0 {
		return m
	}
	atd := td
	if atd < 0 {
		atd = -atd
	}
	tx := (16384 + (atd >> 1)) / td
	dsf := clip3(-4096, 4095, (tb*tx+32)>>6)
	sc := func(v int16) int16 {
		p := dsf * int(v)
		sign := 1
		if p < 0 {
			sign = -1
			p = -p
		}
		return int16(clip3(-32768, 32767, sign*((p+127)>>8)))
	}
	return mv{sc(m.x), sc(m.y)}
}

func (d *Decoder) amvp(xCb, yCb, nCbS, xPb, yPb, nPbW, nPbH, partIdx, X, refIdx, mvpFlag int) mv {
	sh := d.sh
	Y := 1 - X
	targetPoc := sh.refPoc[X][refIdx]
	targetLT := sh.refLT[X][refIdx]
	curPoc := int(d.cur.poc)
	pos := func(xN, yN int) *mvField {
		return d.availPB(xCb, yCb, nCbS, xPb, yPb, nPbW, nPbH, partIdx, xN, yN)
	}
	ltBit := func(m *mvField, l int) bool { return m.lt&(1<<uint(l)) != 0 }
	noScale := func(m *mvField) (mv, bool) {
		if m.pf&(1<<uint(X)) != 0 && m.poc[X] == targetPoc {
			return mv{m.mv[X][0], m.mv[X][1]}, true
		}
		if m.pf&(1<<uint(Y)) != 0 && m.poc[Y] == targetPoc {
			return mv{m.mv[Y][0], m.mv[Y][1]}, true
		}
		return mv{}, false
	}
	withScale := func(m *mvField) (mv, bool) {
		var l int
		switch {
		case m.pf&(1<<uint(X)) != 0 && ltBit(m, X) == targetLT:
			l = X
		case m.pf&(1<<uint(Y)) != 0 && ltBit(m, Y) == targetLT:
			l = Y
		default:
			return mv{}, false
		}
		r := mv{m.mv[l][0], m.mv[l][1]}
		if !targetLT && !ltBit(m, l) {
			td := curPoc - int(m.poc[l])
			tb := curPoc - int(targetPoc)
			if td != tb {
				r = scaleMv(r, td, tb)
			}
		}
		return r, true
	}
	a := [2]*mvField{pos(xPb-1, yPb+nPbH), pos(xPb-1, yPb+nPbH-1)}
	isScaled := a[0] != nil || a[1] != nil
	var mvA, mvB mv
	availA, availB := false, false
	for k := 0; k < 2 && !availA; k++ {
		if a[k] != nil {
			mvA, availA = noScale(a[k])
		}
	}
	for k := 0; k < 2 && !availA; k++ {
		if a[k] != nil {
			mvA, availA = withScale(a[k])
		}
	}
	b := [3]*mvField{pos(xPb+nPbW, yPb-1), pos(xPb+nPbW-1, yPb-1), pos(xPb-1, yPb-1)}
	for k := 0; k < 3 && !availB; k++ {
		if b[k] != nil {
			mvB, availB = noScale(b[k])
		}
	}
	if !isScaled && availB {
		mvA, availA = mvB, true
	}
	if !isScaled {
		availB = false
		for k := 0; k < 3 && !availB; k++ {
			if b[k] != nil {
				mvB, availB = withScale(b[k])
			}
		}
	}
	var list [3]mv
	n := 0
	if availA {
		list[n] = mvA
		n++
	}
	if availB && !(availA && mvA == mvB) {
		list[n] = mvB
		n++
	}
	if n <= mvpFlag && n < 2 && sh.temporalMvp {
		if m, ok := d.temporalMv(xPb, yPb, nPbW, nPbH, refIdx, X); ok {
			list[n] = m
			n++
		}
	}
	if mvpFlag < n {
		return list[mvpFlag]
	}
	return mv{}
}

// temporalMv derives the temporal luma motion vector predictor (8.5.3.2.8).
func (d *Decoder) temporalMv(xPb, yPb, nPbW, nPbH, refIdx, X int) (mv, bool) {
	sh, s := d.sh, d.sp
	if !sh.temporalMvp {
		return mv{}, false
	}
	var colPic *Picture
	if sh.sliceType == sliceB && !sh.colFromL0 {
		colPic = sh.refPic[1][sh.colRefIdx]
	} else {
		colPic = sh.refPic[0][sh.colRefIdx]
	}
	if colPic == nil || colPic.mvf == nil || colPic.w != s.width || colPic.h != s.height {
		return mv{}, false
	}
	xBr, yBr := xPb+nPbW, yPb+nPbH
	if yPb>>uint(s.log2Ctb) == yBr>>uint(s.log2Ctb) && yBr < s.height && xBr < s.width {
		if m, ok := d.colMv(colPic, (xBr>>4)<<4, (yBr>>4)<<4, refIdx, X); ok {
			return m, true
		}
	}
	xC, yC := xPb+(nPbW>>1), yPb+(nPbH>>1)
	return d.colMv(colPic, (xC>>4)<<4, (yC>>4)<<4, refIdx, X)
}

func (d *Decoder) colMv(col *Picture, x, y, refIdx, X int) (mv, bool) {
	sh := d.sh
	cm := &col.mvf[(y>>2)*col.w4+x>>2]
	if cm.pf == 0 {
		return mv{}, false
	}
	var l int
	switch {
	case cm.pf&1 == 0:
		l = 1
	case cm.pf&2 == 0:
		l = 0
	default:
		if sh.noBackward {
			l = X
		} else {
			l = boolInt(sh.colFromL0) // mvLNCol with N = collocated_from_l0_flag
		}
	}
	colLT := cm.lt&(1<<uint(l)) != 0
	curLT := sh.refLT[X][refIdx]
	if colLT != curLT {
		return mv{}, false
	}
	m := mv{cm.mv[l][0], cm.mv[l][1]}
	colDiff := col.poc - int(cm.poc[l])
	curDiff := d.cur.poc - int(sh.refPoc[X][refIdx])
	if curLT || colDiff == curDiff || colDiff == 0 {
		return m, true
	}
	return scaleMv(m, colDiff, curDiff), true
}
