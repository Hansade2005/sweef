package h264

import (
	"math/rand"
	"testing"
)

func randPlane(r *rand.Rand, n int, maxv int32, mode int) []uint16 {
	p := make([]uint16, n)
	for i := range p {
		switch mode {
		case 0: // uniform noise
			p[i] = uint16(r.Int31n(maxv + 1))
		case 1: // extremes, to hit every clipping branch
			if r.Intn(2) == 0 {
				p[i] = 0
			} else {
				p[i] = uint16(maxv)
			}
		default: // smooth with small noise
			p[i] = uint16(clipV(int32(i%64)*maxv/64+r.Int31n(8), maxv))
		}
	}
	return p
}

// The interpolation kernels give exactly the samples of the straightforward implementation, for every
// fraction, block size, bit depth and position (inside the picture and across its edges).
func TestMCMatchesReference(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	d, ref := new(Decoder), new(Decoder)
	for _, maxv := range []int32{255, 1023} {
		for mode := 0; mode < 3; mode++ {
			const pw, ph, stride = 70, 50, 80
			plane := randPlane(r, stride*ph, maxv, mode)
			for _, wh := range [][2]int{{16, 16}, {16, 8}, {8, 16}, {8, 8}, {8, 4}, {4, 8}, {4, 4}} {
				w, h := wh[0], wh[1]
				for iter := 0; iter < 400; iter++ {
					x := r.Intn(pw+20) - 10
					y := r.Intn(ph+20) - 10
					if iter < 100 { // mostly inside
						x, y = 3+r.Intn(pw-w-8), 3+r.Intn(ph-h-8)
					}
					fx, fy := r.Intn(4), r.Intn(4)
					a := make([]uint16, 16*16)
					b := make([]uint16, 16*16)
					d.lumaMC(a, 16, plane, stride, pw, ph, x, y, fx, fy, w, h, maxv)
					ref.lumaMCRef(b, 16, plane, stride, pw, ph, x, y, fx, fy, w, h, maxv)
					for i := range a {
						if a[i] != b[i] {
							t.Fatalf("luma maxv %d mode %d %dx%d at (%d,%d) frac (%d,%d): sample %d: %d want %d", maxv, mode, w, h, x, y, fx, fy, i, a[i], b[i])
						}
					}
					cx, cy := r.Intn(pw/2+10)-5, r.Intn(ph/2+10)-5
					cfx, cfy := r.Intn(8), r.Intn(8)
					cw, chh := w/2, h/2
					if cw < 2 {
						continue
					}
					d.chromaMC(a, 16, plane, stride, pw/2, ph/2, cx, cy, cfx, cfy, cw, chh)
					ref.chromaMCRef(b, 16, plane, stride, pw/2, ph/2, cx, cy, cfx, cfy, cw, chh)
					for i := range a {
						if a[i] != b[i] {
							t.Fatalf("chroma %dx%d at (%d,%d) frac (%d,%d): sample %d: %d want %d", cw, chh, cx, cy, cfx, cfy, i, a[i], b[i])
						}
					}
				}
			}
		}
	}
}
