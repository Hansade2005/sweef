package raster

import (
	"math"
	"sort"
)

// A port of Skia's analytic anti-aliasing scan converter (SkScan_AAAPath.cpp:
// aaa_fill_path with aaa_walk_convex_edges and aaa_walk_edges, the trapezoid
// blitters and the three additive blitters). fxBuilder (text_fx.go) produces
// the analytic edges; this file walks them the way Skia does, in 16.16 fixed
// point with Skia's alpha approximations (the triangle alphas keep 5 bits per
// factor, row corners are snapped to 1/16 pixel, alphas close to 0 and 255 are
// snapped by the run based blitter). Coverage from the exact-area rasterizer
// differs from this by a few 1/255 for most edges but by up to 10% for long
// shallow ones.

const aaaFixed1 = 1 << 16

func aaaMul(a, b int) int        { return int(int32((int64(a) * int64(b)) >> 16)) }
func aaaCeilToInt(x int) int     { return (x + 0xFFFF) >> 16 }
func aaaCeilToFixed(x int) int   { return (x + 0xFFFF) &^ 0xFFFF }
func aaaFloorToFixed(x int) int  { return x &^ 0xFFFF }
func aaaRoundToInt(x int) int    { return (x + 0x8000) >> 16 }
func aaaU8(v int) uint8          { return uint8(v) }
func aaaCatchOverflow(a int) int { return a - (a >> 8) }
func aaaSatAdd(a, b int) int     { return aaaClamp32(a + b) }
func aaaClamp32(v int) int {
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	if v < math.MinInt32 {
		return math.MinInt32
	}
	return v
}

// quick_inverse and quick_div of SkAnalyticEdge.cpp: the reciprocal table is
// floor(2^22 / |x|) for |x| in 1..1023, signed like x (checked against the
// table in Skia's source).
func aaaQuickInverse(x int) int {
	if x == 0 {
		return 0
	}
	ax := x
	if ax < 0 {
		ax = -ax
	}
	v := 4194304 / ax
	if x > 0 {
		return v
	}
	return -v
}

func aaaQuickDiv(a, b int) int {
	absA, absB := fxAbs(a), fxAbs(b)
	if absB >= 8 && absB < 1024 && absA < 1<<12 {
		return (a * aaaQuickInverse(b)) >> 6
	}
	return fxQuickDiv(a, b)
}

type aaEdge struct {
	next, prev                           *aaEdge
	x, dx, upperX, y, upperY, lowerY, dy int
	wind                                 int
	curveStart                           bool
}

func (e *aaEdge) goY(y int) {
	if y == e.y+aaaFixed1 {
		e.x += e.dx
		e.y = y
	} else if y != e.y {
		e.x = e.upperX + aaaMul(e.dx, y-e.upperY)
		e.y = y
	}
}

func (e *aaEdge) goYShift(y, shift int) {
	e.y = y
	e.x += e.dx >> shift
}

func aaaRemove(e *aaEdge) {
	e.prev.next = e.next
	e.next.prev = e.prev
}

func aaaInsertAfter(e, after *aaEdge) {
	e.prev = after
	e.next = after.next
	after.next.prev = e
	after.next = e
}

func aaaBackwardInsertByX(e *aaEdge) {
	x := e.x
	prev := e.prev
	for prev.prev != nil && prev.x > x {
		prev = prev.prev
	}
	if prev.next != e {
		aaaRemove(e)
		aaaInsertAfter(e, prev)
	}
}

func aaaBackwardInsertStart(prev *aaEdge, x int) *aaEdge {
	for prev.prev != nil && prev.x > x {
		prev = prev.prev
	}
	return prev
}

// ---- blitters -------------------------------------------------------------------------------

type aaaBlitter struct {
	out       *Mask // final A8 coverage
	useMask   bool  // MaskAdditiveBlitter: alphas are added straight into out
	safe      bool  // SafeRLEAdditiveBlitter: clamped adds
	left, wid int
	top       int
	curY      int
	acc       []int // RLE accumulation of the current row
	touched   bool
	ox, oy    int // mask origin
}

func (b *aaaBlitter) px(x, y int) *uint8 {
	if x < b.out.B.X0 || x >= b.out.B.X1 || y < b.out.B.Y0 || y >= b.out.B.Y1 {
		return nil
	}
	return &b.out.A[(y-b.out.B.Y0)*b.out.Stride+(x-b.out.B.X0)]
}

// dst blit: A8 source-over of coverage a
func (b *aaaBlitter) over(x, y int, a int) {
	if a == 0 {
		return
	}
	p := b.px(x, y)
	if p == nil {
		return
	}
	if a >= 255 {
		*p = 255
		return
	}
	*p = uint8(a + (int(*p)*(256-a))>>8)
}

func (b *aaaBlitter) flush() {
	if b.useMask || !b.touched {
		return
	}
	for i, a := range b.acc {
		if a != 0 {
			s := a
			if s > 255 {
				s = 255
			}
			// snapAlpha: values close to 0 and 255 are snapped
			if s > 247 {
				s = 255
			} else if s < 8 {
				s = 0
			}
			b.over(b.left+i, b.curY, s)
			b.acc[i] = 0
		}
	}
	b.touched = false
}

func (b *aaaBlitter) checkY(y int) {
	if b.useMask {
		return
	}
	if y != b.curY {
		b.flush()
		b.curY = y
	}
}

func (b *aaaBlitter) flushIfYChanged(y, nextY int) {
	if b.useMask {
		return
	}
	if y>>16 != nextY>>16 {
		b.flush()
	}
}

func (b *aaaBlitter) addAcc(x, y, a int) {
	if b.useMask {
		p := b.px(x, y)
		if p == nil {
			return
		}
		*p = uint8(aaaCatchOverflow(int(*p) + a))
		return
	}
	b.checkY(y)
	i := x - b.left
	if i < 0 || i >= b.wid {
		return
	}
	if b.safe {
		b.acc[i] = min(255, b.acc[i]+a)
	} else {
		b.acc[i] = aaaCatchOverflow(b.acc[i] + a)
	}
	b.touched = true
}

func (b *aaaBlitter) blitAntiH1(x, y, alpha int) { b.addAcc(x, y, alpha) }
func (b *aaaBlitter) blitAntiHW(x, y, w, alpha int) {
	for i := 0; i < w; i++ {
		b.addAcc(x+i, y, alpha)
	}
}
func (b *aaaBlitter) blitAntiHRow(x, y int, a []uint8, n int) {
	for i := 0; i < n; i++ {
		b.addAcc(x+i, y, int(a[i]))
	}
}

// real blitter operations
func (b *aaaBlitter) realV(x, y, h, alpha int) {
	if alpha == 0 {
		return
	}
	for i := 0; i < h; i++ {
		if b.useMask {
			if p := b.px(x, y+i); p != nil {
				*p = uint8(alpha)
			}
		} else {
			b.over(x, y+i, alpha)
		}
	}
}

func (b *aaaBlitter) realRect(x, y, w, h int) {
	for j := 0; j < h; j++ {
		for i := 0; i < w; i++ {
			if p := b.px(x+i, y+j); p != nil {
				if b.useMask {
					*p = 255
				} else {
					*p = 255
				}
			}
		}
	}
}

func (b *aaaBlitter) realAntiRect(x, y, w, h, la, ra int) {
	b.realV(x, y, h, la)
	b.realV(x+1+w, y, h, ra)
	b.realRect(x+1, y, w, h)
}

func (b *aaaBlitter) realH(x, y, w int) {
	for i := 0; i < w; i++ {
		if p := b.px(x+i, y); p != nil {
			*p = 255
		}
	}
}

func (b *aaaBlitter) realAntiH2(x, y, a1, a2 int) {
	b.over(x, y, a1)
	b.over(x+1, y, a2)
}

func (b *aaaBlitter) realAntiHAlphas(x, y int, a []uint8, n int) {
	for i := 0; i < n; i++ {
		b.over(x+i, y, int(a[i]))
	}
}

// ---- alpha helpers --------------------------------------------------------------------------

func aaaTrapezoidToAlpha(l1, l2 int) int {
	area := (l1 + l2) / 2
	return int(uint8(area >> 8))
}

func aaaPartialTriangleToAlpha(a, b int) int {
	area := (a >> 11) * (a >> 11) * (b >> 11)
	return (area >> 8) & 0xFF
}

func aaaPartialAlphaFixed(alpha, partialHeight int) int {
	return int(uint8(aaaRoundToInt(alpha * partialHeight)))
}

func aaaPartialAlpha(alpha, fullAlpha int) int { return (alpha * fullAlpha) >> 8 }

func aaaFixedToAlpha(f int) int { return aaaPartialAlphaFixed(0xFF, f) }

func aaaApproxIntersection(l1, r1, l2, r2 int) int {
	if l1 > r1 {
		l1, r1 = r1, l1
	}
	if l2 > r2 {
		l2, r2 = r2, l2
	}
	return (max(l1, l2) + min(r1, r2)) / 2
}

func aaaAlphaAboveLine(alphas []int, l, r, dY, fullAlpha int) {
	R := aaaCeilToInt(r)
	if R == 0 {
		return
	} else if R == 1 {
		alphas[0] = aaaPartialAlpha(((R<<17)-l-r)>>9, fullAlpha)
	} else {
		first := aaaFixed1 - l
		last := r - ((R - 1) << 16)
		firstH := aaaMul(first, dY)
		alphas[0] = int(uint8(aaaMul(first, firstH) >> 9))
		alpha16 := aaaSatAdd(firstH, dY>>1)
		for i := 1; i < R-1; i++ {
			alphas[i] = int(uint8(alpha16 >> 8))
			alpha16 = aaaSatAdd(alpha16, dY)
		}
		alphas[R-1] = int(uint8(fullAlpha - aaaPartialTriangleToAlpha(last, dY)))
	}
}

func aaaAlphaBelowLine(alphas []int, l, r, dY, fullAlpha int) {
	R := aaaCeilToInt(r)
	if R == 0 {
		return
	} else if R == 1 {
		alphas[0] = aaaPartialAlpha(aaaTrapezoidToAlpha(l, r), fullAlpha)
	} else {
		first := aaaFixed1 - l
		last := r - ((R - 1) << 16)
		lastH := aaaMul(last, dY)
		alphas[R-1] = int(uint8(aaaMul(last, lastH) >> 9))
		alpha16 := aaaSatAdd(lastH, dY>>1)
		for i := R - 2; i > 0; i-- {
			alphas[i] = (alpha16 >> 8) & 0xFF
			alpha16 = aaaSatAdd(alpha16, dY)
		}
		alphas[0] = int(uint8(fullAlpha - aaaPartialTriangleToAlpha(first, dY)))
	}
}

// maskRow is a row of the mask blitter (nil for the run based blitters).
type aaaMaskRow struct {
	b  *aaaBlitter
	y  int
	on bool
}

func (m aaaMaskRow) safeAdd(x, a int) {
	if p := m.b.px(x, m.y); p != nil {
		v := int(*p) + a
		if v > 255 {
			v = 255
		}
		*p = uint8(v)
	}
}

func aaaBlitSingleAlpha(b *aaaBlitter, y, x, alpha, fullAlpha int, mr aaaMaskRow, noReal bool) {
	if mr.on {
		if fullAlpha == 0xFF && !noReal {
			if p := b.px(x, y); p != nil {
				*p = uint8(alpha)
			}
		} else {
			mr.safeAdd(x, aaaPartialAlpha(alpha, fullAlpha))
		}
	} else {
		if fullAlpha == 0xFF && !noReal {
			b.realV(x, y, 1, alpha)
		} else {
			b.blitAntiH1(x, y, aaaPartialAlpha(alpha, fullAlpha))
		}
	}
}

func aaaBlitTwoAlphas(b *aaaBlitter, y, x, a1, a2, fullAlpha int, mr aaaMaskRow, noReal bool) {
	if mr.on {
		mr.safeAdd(x, a1)
		mr.safeAdd(x+1, a2)
	} else {
		if fullAlpha == 0xFF && !noReal {
			b.realAntiH2(x, y, a1, a2)
		} else {
			b.blitAntiH1(x, y, a1)
			b.blitAntiH1(x+1, y, a2)
		}
	}
}

func aaaBlitFullAlpha(b *aaaBlitter, y, x, n, fullAlpha int, mr aaaMaskRow, noReal bool) {
	if mr.on {
		for i := 0; i < n; i++ {
			mr.safeAdd(x+i, fullAlpha)
		}
	} else {
		if fullAlpha == 0xFF && !noReal {
			b.realH(x, y, n)
		} else {
			b.blitAntiHW(x, y, n, fullAlpha)
		}
	}
}

func aaaBlitAAATrapezoidRow(b *aaaBlitter, y, ul, ur, ll, lr, lDY, rDY, fullAlpha int, mr aaaMaskRow, noReal bool) {
	L, R := ul>>16, aaaCeilToInt(lr)
	n := R - L
	if n == 1 {
		alpha := aaaTrapezoidToAlpha(ur-ul, lr-ll)
		aaaBlitSingleAlpha(b, y, L, alpha, fullAlpha, mr, noReal)
		return
	}
	alphas := make([]int, 2*(n+1))
	temp := alphas[n+1:]
	alphas = alphas[:n+1]
	for i := 0; i < n; i++ {
		alphas[i] = fullAlpha
	}
	uL := ul >> 16
	lL := aaaCeilToInt(ll)
	if uL+2 == lL {
		first := (uL << 16) + aaaFixed1 - ul
		second := ll - ul - first
		a1 := fullAlpha - aaaPartialTriangleToAlpha(first, lDY)
		a2 := aaaPartialTriangleToAlpha(second, lDY)
		a1, a2 = int(uint8(a1)), int(uint8(a2))
		if alphas[0] > a1 {
			alphas[0] -= a1
		} else {
			alphas[0] = 0
		}
		if alphas[1] > a2 {
			alphas[1] -= a2
		} else {
			alphas[1] = 0
		}
	} else {
		aaaAlphaBelowLine(temp[uL-L:], ul-(uL<<16), ll-(uL<<16), lDY, fullAlpha)
		for i := uL; i < lL; i++ {
			if alphas[i-L] > temp[i-L] {
				alphas[i-L] -= temp[i-L]
			} else {
				alphas[i-L] = 0
			}
		}
	}
	uR := ur >> 16
	lR := aaaCeilToInt(lr)
	if uR+2 == lR {
		first := (uR << 16) + aaaFixed1 - ur
		second := lr - ur - first
		a1 := aaaPartialTriangleToAlpha(first, rDY)
		a2 := int(uint8(fullAlpha - aaaPartialTriangleToAlpha(second, rDY)))
		if alphas[n-2] > a1 {
			alphas[n-2] -= a1
		} else {
			alphas[n-2] = 0
		}
		if alphas[n-1] > a2 {
			alphas[n-1] -= a2
		} else {
			alphas[n-1] = 0
		}
	} else {
		aaaAlphaAboveLine(temp[uR-L:], ur-(uR<<16), lr-(uR<<16), rDY, fullAlpha)
		for i := uR; i < lR; i++ {
			if alphas[i-L] > temp[i-L] {
				alphas[i-L] -= temp[i-L]
			} else {
				alphas[i-L] = 0
			}
		}
	}
	if mr.on {
		for i := 0; i < n; i++ {
			mr.safeAdd(L+i, alphas[i])
		}
	} else {
		a8 := make([]uint8, n)
		for i := range a8 {
			a8[i] = uint8(alphas[i])
		}
		if fullAlpha == 0xFF && !noReal {
			b.realAntiHAlphas(L, y, a8, n)
		} else {
			b.blitAntiHRow(L, y, a8, n)
		}
	}
}

func aaaBlitTrapezoidRow(b *aaaBlitter, y, ul, ur, ll, lr, lDY, rDY, fullAlpha int, mr aaaMaskRow, noReal bool) {
	if ul > ur {
		return
	}
	if ll > lr {
		ll = aaaApproxIntersection(ul, ll, ur, lr)
		lr = ll
	}
	if ul == ur && ll == lr {
		return
	}
	if ul > ll {
		ul, ll = ll, ul
	}
	if ur > lr {
		ur, lr = lr, ur
	}
	joinLeft := aaaCeilToFixed(ll)
	joinRite := aaaFloorToFixed(ur)
	if joinLeft <= joinRite {
		if ul < joinLeft {
			n := aaaCeilToInt(joinLeft - ul)
			if n == 1 {
				alpha := aaaTrapezoidToAlpha(joinLeft-ul, joinLeft-ll)
				aaaBlitSingleAlpha(b, y, ul>>16, alpha, fullAlpha, mr, noReal)
			} else if n == 2 {
				first := joinLeft - aaaFixed1 - ul
				second := ll - ul - first
				a1 := aaaPartialTriangleToAlpha(first, lDY)
				a2 := int(uint8(fullAlpha - aaaPartialTriangleToAlpha(second, lDY)))
				aaaBlitTwoAlphas(b, y, ul>>16, a1, a2, fullAlpha, mr, noReal)
			} else {
				aaaBlitAAATrapezoidRow(b, y, ul, joinLeft, ll, joinLeft, lDY, math.MaxInt32, fullAlpha, mr, noReal)
			}
		}
		if joinLeft < joinRite {
			aaaBlitFullAlpha(b, y, joinLeft>>16, (joinRite-joinLeft)>>16, fullAlpha, mr, noReal)
		}
		if lr > joinRite {
			n := aaaCeilToInt(lr - joinRite)
			if n == 1 {
				alpha := aaaTrapezoidToAlpha(ur-joinRite, lr-joinRite)
				aaaBlitSingleAlpha(b, y, joinRite>>16, alpha, fullAlpha, mr, noReal)
			} else if n == 2 {
				first := joinRite + aaaFixed1 - ur
				second := lr - ur - first
				a1 := int(uint8(fullAlpha - aaaPartialTriangleToAlpha(first, rDY)))
				a2 := aaaPartialTriangleToAlpha(second, rDY)
				aaaBlitTwoAlphas(b, y, joinRite>>16, a1, a2, fullAlpha, mr, noReal)
			} else {
				aaaBlitAAATrapezoidRow(b, y, joinRite, ur, joinRite, lr, math.MaxInt32, rDY, fullAlpha, mr, noReal)
			}
		}
	} else {
		aaaBlitAAATrapezoidRow(b, y, ul, ur, ll, lr, lDY, rDY, fullAlpha, mr, noReal)
	}
}

// ---- walkers --------------------------------------------------------------------------------

func aaaIsSmoothEnough2(this, next *aaEdge, stopY int) bool {
	return fxAbs(aaaClamp32(next.dx-this.dx)) <= aaaFixed1 && next.lowerY-next.upperY >= aaaFixed1
}

func aaaIsSmoothEnough(leftE, riteE, currE *aaEdge, stopY int) bool {
	if currE.upperY >= stopY<<16 {
		return false
	}
	if leftE.lowerY+aaaFixed1 < riteE.lowerY {
		return aaaIsSmoothEnough2(leftE, currE, stopY)
	} else if leftE.lowerY > riteE.lowerY+aaaFixed1 {
		return aaaIsSmoothEnough2(riteE, currE, stopY)
	}
	nextCurrE := currE.next
	if nextCurrE.upperY >= stopY<<16 {
		return false
	}
	if nextCurrE.upperX < currE.upperX {
		currE, nextCurrE = nextCurrE, currE
	}
	return aaaIsSmoothEnough2(leftE, currE, stopY) && aaaIsSmoothEnough2(riteE, nextCurrE, stopY)
}

func (b *aaaBlitter) maskRowAt(y int) aaaMaskRow {
	if b.useMask {
		return aaaMaskRow{b: b, y: y, on: true}
	}
	return aaaMaskRow{}
}

func aaaWalkConvex(head *aaEdge, b *aaaBlitter, startY, stopY, leftBound, riteBound int) {
	leftE := head.next
	riteE := leftE.next
	currE := riteE.next
	y := max(leftE.upperY, riteE.upperY)
	updateEdge := func(e *aaEdge) bool { return false } // lines never continue; curve pieces are separate edges
	for {
		for leftE.lowerY <= y {
			if !updateEdge(leftE) {
				if currE.upperY>>16 >= stopY {
					return
				}
				leftE = currE
				currE = currE.next
			}
		}
		for riteE.lowerY <= y {
			if !updateEdge(riteE) {
				if currE.upperY>>16 >= stopY {
					return
				}
				riteE = currE
				currE = currE.next
			}
		}
		if y>>16 >= stopY {
			break
		}
		leftE.goY(y)
		riteE.goY(y)
		if leftE.x > riteE.x || (leftE.x == riteE.x && leftE.dx > riteE.dx) {
			leftE, riteE = riteE, leftE
		}
		localBot := min(leftE.lowerY, riteE.lowerY)
		if aaaIsSmoothEnough(leftE, riteE, currE, stopY) {
			localBot = aaaCeilToFixed(localBot)
		}
		localBot = min(localBot, stopY<<16)
		left := max(leftBound, leftE.x)
		dLeft := leftE.dx
		rite := min(riteBound, riteE.x)
		dRite := riteE.dx
		if dLeft|dRite == 0 {
			fullLeft := aaaCeilToInt(left)
			fullRite := rite >> 16
			partialLeft := (fullLeft << 16) - left
			partialRite := rite - (fullRite << 16)
			fullTop := aaaCeilToInt(y)
			fullBot := localBot >> 16
			partialTop := (fullTop << 16) - y
			partialBot := localBot - (fullBot << 16)
			if fullTop > fullBot {
				partialTop -= aaaFixed1 - partialBot
				partialBot = 0
			}
			if fullRite >= fullLeft {
				if partialTop > 0 {
					if partialLeft > 0 {
						b.blitAntiH1(fullLeft-1, fullTop-1, aaaFixedToAlpha(aaaMul(partialTop, partialLeft)))
					}
					b.blitAntiHW(fullLeft, fullTop-1, fullRite-fullLeft, aaaFixedToAlpha(partialTop))
					if partialRite > 0 {
						b.blitAntiH1(fullRite, fullTop-1, aaaFixedToAlpha(aaaMul(partialTop, partialRite)))
					}
					b.flushIfYChanged(y, y+partialTop)
				}
				if fullBot > fullTop && (fullRite > fullLeft || aaaFixedToAlpha(partialLeft) > 0 || aaaFixedToAlpha(partialRite) > 0) {
					b.realAntiRect(fullLeft-1, fullTop, fullRite-fullLeft, fullBot-fullTop, aaaFixedToAlpha(partialLeft), aaaFixedToAlpha(partialRite))
				}
				if partialBot > 0 {
					if partialLeft > 0 {
						b.blitAntiH1(fullLeft-1, fullBot, aaaFixedToAlpha(aaaMul(partialBot, partialLeft)))
					}
					b.blitAntiHW(fullLeft, fullBot, fullRite-fullLeft, aaaFixedToAlpha(partialBot))
					if partialRite > 0 {
						b.blitAntiH1(fullRite, fullBot, aaaFixedToAlpha(aaaMul(partialBot, partialRite)))
					}
				}
			} else {
				width := rite - left
				if width > 0 {
					if partialTop > 0 {
						b.blitAntiHW(fullLeft-1, fullTop-1, 1, aaaFixedToAlpha(aaaMul(partialTop, width)))
						b.flushIfYChanged(y, y+partialTop)
					}
					if fullBot > fullTop {
						b.realV(fullLeft-1, fullTop, fullBot-fullTop, aaaFixedToAlpha(width))
					}
					if partialBot > 0 {
						b.blitAntiHW(fullLeft-1, fullBot, 1, aaaFixedToAlpha(aaaMul(partialBot, width)))
					}
				}
			}
			y = localBot
		} else {
			const snapDigit = aaaFixed1 >> 4
			const snapHalf = snapDigit >> 1
			const snapMask = ^(snapDigit - 1)
			left += snapHalf
			rite += snapHalf
			count := aaaCeilToInt(localBot) - (y >> 16)
			mr := b.maskRowAt(y >> 16)
			if count > 1 {
				if y&^0xFFFF != y {
					count--
					nextY := aaaCeilToFixed(y + 1)
					dY := nextY - y
					nextLeft := left + aaaMul(dLeft, dY)
					nextRite := rite + aaaMul(dRite, dY)
					aaaBlitTrapezoidRow(b, y>>16, left&snapMask, rite&snapMask, nextLeft&snapMask, nextRite&snapMask, leftE.dy, riteE.dy, aaaPartialAlphaFixed(0xFF, dY), mr, false)
					b.flushIfYChanged(y, nextY)
					left, rite, y = nextLeft, nextRite, nextY
				}
				for count > 1 {
					count--
					mr = b.maskRowAt(y >> 16)
					nextY := y + aaaFixed1
					nextLeft, nextRite := left+dLeft, rite+dRite
					aaaBlitTrapezoidRow(b, y>>16, left&snapMask, rite&snapMask, nextLeft&snapMask, nextRite&snapMask, leftE.dy, riteE.dy, 0xFF, mr, false)
					b.flushIfYChanged(y, nextY)
					left, rite, y = nextLeft, nextRite, nextY
				}
			}
			mr = b.maskRowAt(y >> 16)
			dY := localBot - y
			nextLeft := max(left+aaaMul(dLeft, dY), leftBound+snapHalf)
			nextRite := min(rite+aaaMul(dRite, dY), riteBound+snapHalf)
			aaaBlitTrapezoidRow(b, y>>16, left&snapMask, rite&snapMask, nextLeft&snapMask, nextRite&snapMask, leftE.dy, riteE.dy, aaaPartialAlphaFixed(0xFF, dY), mr, false)
			b.flushIfYChanged(y, localBot)
			left, rite = nextLeft, nextRite
			y = localBot
			left -= snapHalf
			rite -= snapHalf
		}
		leftE.x = left
		riteE.x = rite
		leftE.y, riteE.y = y, y
	}
}

func aaaUpdateNextNextY(y, nextY int, nn *int) {
	if y > nextY && y < *nn {
		*nn = y
	}
}

func aaaCheckIntersection(e *aaEdge, nextY int, nn *int) {
	if e.prev.prev != nil && e.prev.x+e.prev.dx > e.x+e.dx {
		*nn = nextY + (aaaFixed1 >> 2)
	}
}

func aaaCheckIntersectionFwd(e *aaEdge, nextY int, nn *int) {
	if e.next.next != nil && e.x+e.dx > e.next.x+e.next.dx {
		*nn = nextY + (aaaFixed1 >> 2)
	}
}

func aaaInsertNewEdges(newEdge *aaEdge, y int, nn *int) {
	if newEdge.upperY > y {
		aaaUpdateNextNextY(newEdge.upperY, y, nn)
		return
	}
	prev := newEdge.prev
	if prev.x <= newEdge.x {
		for newEdge.upperY <= y {
			aaaCheckIntersection(newEdge, y, nn)
			aaaUpdateNextNextY(newEdge.lowerY, y, nn)
			newEdge = newEdge.next
		}
		aaaUpdateNextNextY(newEdge.upperY, y, nn)
		return
	}
	start := aaaBackwardInsertStart(prev, newEdge.x)
	for {
		next := newEdge.next
		skip := false
		for {
			if start.next == newEdge {
				skip = true
				break
			}
			after := start.next
			if after.x >= newEdge.x {
				break
			}
			start = after
		}
		if !skip {
			aaaRemove(newEdge)
			aaaInsertAfter(newEdge, start)
		}
		aaaCheckIntersection(newEdge, y, nn)
		aaaCheckIntersectionFwd(newEdge, y, nn)
		aaaUpdateNextNextY(newEdge.lowerY, y, nn)
		start = newEdge
		newEdge = next
		if newEdge.upperY > y {
			break
		}
	}
	aaaUpdateNextNextY(newEdge.upperY, y, nn)
}

func aaaEdgesTooClose(prev, next *aaEdge, lowerY int) bool {
	return next != nil && prev != nil && next.upperY < lowerY && prev.x+aaaFixed1 >= next.x-fxAbs(next.dx)
}

func aaaEdgesTooCloseRite(prevRite, ul, ll int) bool {
	return prevRite > ul>>16 || prevRite > ll>>16
}

func aaaWalkEdges(head, tail *aaEdge, b *aaaBlitter, startY, stopY, leftClip, rightClip int, skipIntersect bool, forceRLE bool) {
	head.x, head.upperX = leftClip, leftClip
	tail.x, tail.upperX = rightClip, rightClip
	y := max(head.next.upperY, startY<<16)
	nextNextY := math.MaxInt32
	{
		e := head.next
		for ; e.upperY <= y; e = e.next {
			e.goY(y)
			aaaUpdateNextNextY(e.lowerY, y, &nextNextY)
		}
		aaaUpdateNextNextY(e.upperY, y, &nextNextY)
	}
	for {
		w := 0
		inInterval := false
		prevX := head.x
		nextY := min(nextNextY, aaaCeilToFixed(y+1))
		currE := head.next
		leftE := head
		left := leftClip
		leftDY := 0
		prevRite := leftClip >> 16
		nextNextY = math.MaxInt32
		yShift := 0
		if (nextY-y)&(aaaFixed1>>2) != 0 {
			yShift = 2
			nextY = y + (aaaFixed1 >> 2)
		} else if (nextY-y)&(aaaFixed1>>1) != 0 {
			yShift = 1
			nextY = y + (aaaFixed1 >> 1)
		}
		fullAlpha := aaaFixedToAlpha(nextY - y)
		mr := b.maskRowAt(y >> 16)
		noReal := forceRLE
		for currE.upperY <= y {
			w += currE.wind
			prevIn := inInterval
			inInterval = (w != 0)
			isLeft := inInterval && !prevIn
			isRite := !inInterval && prevIn
			if isRite {
				rite := currE.x
				currE.goYShift(nextY, yShift)
				nextLeft := max(leftClip, leftE.x)
				rite = min(rightClip, rite)
				nextRite := min(rightClip, currE.x)
				nr := noReal || (fullAlpha == 0xFF && (aaaEdgesTooCloseRite(prevRite, left, leftE.x) || aaaEdgesTooClose(currE, currE.next, nextY)))
				aaaBlitTrapezoidRow(b, y>>16, left, rite, nextLeft, nextRite, leftDY, currE.dy, fullAlpha, mr, nr)
				prevRite = aaaCeilToInt(max(rite, currE.x))
			} else {
				if isLeft {
					left = max(currE.x, leftClip)
					leftDY = currE.dy
					leftE = currE
				}
				currE.goYShift(nextY, yShift)
			}
			next := currE.next
			// edges are plain lines: no continuation
			if currE.lowerY <= nextY {
				aaaRemove(currE)
			} else {
				aaaUpdateNextNextY(currE.lowerY, nextY, &nextNextY)
				newX := currE.x
				if newX < prevX {
					aaaBackwardInsertByX(currE)
				} else {
					prevX = newX
				}
				if !skipIntersect {
					aaaCheckIntersection(currE, nextY, &nextNextY)
				}
			}
			currE = next
		}
		if inInterval {
			nr := noReal || (fullAlpha == 0xFF && aaaEdgesTooClose(leftE.prev, leftE, nextY))
			aaaBlitTrapezoidRow(b, y>>16, left, rightClip, max(leftClip, leftE.x), rightClip, leftDY, 0, fullAlpha, mr, nr)
		}
		if forceRLE {
			b.flushIfYChanged(y, nextY)
		}
		y = nextY
		if y >= stopY<<16 {
			break
		}
		aaaInsertNewEdges(currE, y, &nextNextY)
	}
	b.flush()
}

// aaaFill rasterizes the analytic edges (relative to the mask origin) into a
// mask covering r, the way SkScan::AAAFillPath does. ir is the integer bounds
// of the path (the mask blitter is chosen from its size) and convex says
// whether Skia knows the path to be convex.
func aaaFill(edges []fxEdge, ir, r IRect, convex bool, npts int) *Mask {
	out := newMask(r)
	if len(edges) == 0 {
		return out
	}
	list := make([]*aaEdge, 0, len(edges))
	for _, fe := range edges {
		e := &aaEdge{x: fe.x, dx: fe.slope, upperX: fe.x, y: fe.y0, upperY: fe.y0, lowerY: fe.y1, wind: fe.wind}
		dy := (fe.y1 - fe.y0) >> 10
		dx := 0
		if fe.slope != math.MaxInt32 {
			dx = (fe.slope*dy + 0x8000) >> 16
		}
		absSlope := fxAbs(fe.slope)
		switch {
		case dx == 0 || fe.slope == 0:
			e.dy = math.MaxInt32
		case absSlope < 1024:
			e.dy = fxAbs(aaaQuickInverse(absSlope))
		default:
			e.dy = fxAbs(aaaQuickDiv(dy, dx))
		}
		list = append(list, e)
	}
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.upperY != b.upperY {
			return a.upperY < b.upperY
		}
		if a.x != b.x {
			return a.x < b.x
		}
		return a.dx < b.dx
	})
	for i := 1; i < len(list); i++ {
		list[i-1].next = list[i]
		list[i].prev = list[i-1]
	}
	var head, tail aaEdge
	head.prev, head.next = nil, list[0]
	head.upperY, head.lowerY, head.x, head.dx, head.dy, head.upperX = math.MinInt32, math.MinInt32, math.MinInt32, 0, math.MaxInt32, math.MinInt32
	list[0].prev = &head
	last := list[len(list)-1]
	tail.prev, tail.next = last, nil
	tail.upperY, tail.lowerY, tail.x, tail.dx, tail.dy, tail.upperX = math.MaxInt32, math.MaxInt32, math.MaxInt32, 0, math.MaxInt32, math.MaxInt32
	last.next = &tail

	b := &aaaBlitter{out: out}
	width := ir.W()
	useMask := width <= 32 && ((width+3)&^3)*ir.H() <= 1024
	b.useMask = useMask
	b.safe = true // SafeRLEAdditiveBlitter; the convex walker uses the plain one
	startY, stopY := ir.Y0, ir.Y1
	leftBound, rightBound := r.X0<<16, r.X1<<16
	if useMask {
		leftBound = max(leftBound, ir.X0<<16)
		rightBound = min(rightBound, ir.X1<<16)
	} else {
		b.left, b.wid = r.X0, r.W()
		b.acc = make([]int, r.W())
		b.top = r.Y0
		b.curY = r.Y0 - 1
	}
	startY = max(startY, r.Y0)
	stopY = min(stopY, r.Y1)
	if convex && len(list) >= 2 {
		b.safe = false
		aaaWalkConvex(&head, b, startY, stopY, leftBound, rightBound)
		b.flush()
	} else {
		skip := npts > (stopY-startY)*2
		aaaWalkEdges(&head, &tail, b, startY, stopY, leftBound, rightBound, skip, false)
	}
	return out
}
