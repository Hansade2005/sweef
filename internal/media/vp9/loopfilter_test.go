package vp9

import (
	"math/rand"
	"testing"
)

// filterSegment is the per-sample loop filter, written for speed: it must change exactly the samples the
// straightforward version changes, for every filter size, bit depth, edge direction and kind of signal (flat
// areas take the wide filters, noise the narrow one, steps the high edge variance branch).
func TestFilterSegmentMatchesReference(t *testing.T) {
	r := rand.New(rand.NewSource(5))
	const stride, rows = 40, 40
	for iter := 0; iter < 20000; iter++ {
		bd := uint([]int{8, 8, 8, 10, 12}[r.Intn(5)])
		maxv := 1<<bd - 1
		pix := make([]uint16, stride*rows)
		base := r.Intn(maxv + 1)
		spread := []int{0, 1, 2, 3, 6, 20, maxv}[r.Intn(7)] << (bd - 8) >> 0
		if spread > maxv {
			spread = maxv
		}
		for i := range pix {
			v := base + r.Intn(2*spread+1) - spread
			if r.Intn(6) == 0 {
				v += 3 << (bd - 8) // a step
			}
			pix[i] = uint16(min(max(v, 0), maxv))
		}
		if r.Intn(2) == 0 { // a step across the edge
			step := r.Intn(30) << (bd - 8)
			for y := 0; y < rows; y++ {
				for x := 16; x < stride; x++ {
					pix[y*stride+x] = uint16(min(int(pix[y*stride+x])+step, maxv))
				}
			}
		}
		lvl := 1 + r.Intn(63)
		lim := max(1, lvl>>uint(r.Intn(3)))
		blimit := 2*(lvl+2) + lim
		thresh := lvl >> 4
		size := []int{tx4x4, tx8x8, tx16x16}[r.Intn(3)]
		cnt := 1 + r.Intn(8)
		var pos, across, along int
		if r.Intn(2) == 0 { // a vertical edge: the samples across it are in a row
			pos, across, along = 12*stride+16, 1, stride
		} else {
			pos, across, along = 16*stride+12, stride, 1
		}
		a := append([]uint16(nil), pix...)
		b := append([]uint16(nil), pix...)
		for k := 0; k < cnt; k++ {
			filterSampleRef(a, bd, pos+k*along, across, lim, blimit, thresh, size)
		}
		filterSegment(b, bd, pos, across, along, cnt, lim, blimit, thresh, size)
		for i := range a {
			if a[i] != b[i] {
				t.Fatalf("iter %d: bd %d size %d lvl %d cnt %d across %d: sample %d is %d, reference %d", iter, bd, size, lvl, cnt, across, i, b[i], a[i])
			}
		}
	}
}
