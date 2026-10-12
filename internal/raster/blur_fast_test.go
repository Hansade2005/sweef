package raster

import (
	"math/rand"
	"testing"
)

func TestRecipExact(t *testing.T) {
	for d := 1; d <= 400; d++ {
		mul := recip(d)
		for n := 0; n <= 255*d+d; n++ {
			if got := int((uint64(n) * mul) >> 40); got != n/d {
				t.Fatalf("d=%d n=%d: %d != %d", d, n, got, n/d)
			}
		}
	}
}

// refBox is the original column-at-a-time implementation.
func refBox(pix []uint8, w, h, stride, ch int, passes []boxSpec) {
	tmp := make([]uint8, h)
	col := make([]uint8, h)
	for x := 0; x < w; x++ {
		for c := 0; c < ch; c++ {
			for y := 0; y < h; y++ {
				col[y] = pix[y*stride+x*ch+c]
			}
			for _, p := range passes {
				d := p.lo + p.hi + 1
				for c0 := 0; c0 < 1; c0++ {
					sum := 0
					for j := 0; j <= p.hi && j < h; j++ {
						sum += int(col[j])
					}
					for i := 0; i < h; i++ {
						tmp[i] = uint8((sum + d/2) / d)
						if a := i + p.hi + 1; a < h {
							sum += int(col[a])
						}
						if r := i - p.lo; r >= 0 {
							sum -= int(col[r])
						}
					}
				}
				copy(col, tmp)
			}
			for y := 0; y < h; y++ {
				pix[y*stride+x*ch+c] = col[y]
			}
		}
	}
}

func TestBoxVertMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for _, ch := range []int{1, 4} {
		for _, sg := range []float64{2.5, 7, 28, 40} {
			w, h := 37+rng.Intn(60), 50+rng.Intn(200)
			stride := w*ch + 3
			a := make([]uint8, stride*h)
			for i := range a {
				a[i] = uint8(rng.Intn(256))
			}
			b := append([]uint8(nil), a...)
			passes := boxPasses(sg)
			refBox(a, w, h, stride, ch, passes)
			boxVert(b, w*ch, h, stride, passes)
			for y := 0; y < h; y++ {
				for x := 0; x < w*ch; x++ {
					if a[y*stride+x] != b[y*stride+x] {
						t.Fatalf("ch=%d sigma=%g mismatch at %d,%d", ch, sg, x, y)
					}
				}
			}
		}
	}
}
