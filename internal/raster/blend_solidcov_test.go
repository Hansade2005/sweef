package raster

import (
	"bytes"
	"math/rand"
	"testing"
)

// blendSolidCovRef is the plain per-pixel definition blendSolidCov must equal.
func blendSolidCovRef(dst, cov []uint8, col [4]uint8) {
	cr, cg, cb, ca := uint32(col[0]), uint32(col[1]), uint32(col[2]), uint32(col[3])
	if ca == 0 {
		return
	}
	for i, cv := range cov {
		if cv == 0 {
			continue
		}
		sr, sg, sb, sa := cr, cg, cb, ca
		if cv != 255 {
			c := uint32(cv)
			sr, sg, sb, sa = mul255(sr, c), mul255(sg, c), mul255(sb, c), mul255(sa, c)
		}
		if sa == 0 {
			continue
		}
		d := dst[i*4 : i*4+4 : i*4+4]
		if sa == 255 {
			d[0], d[1], d[2], d[3] = uint8(sr), uint8(sg), uint8(sb), 255
			continue
		}
		ia := 255 - sa
		d[0] = uint8(min(255, sr+mul255(uint32(d[0]), ia)))
		d[1] = uint8(min(255, sg+mul255(uint32(d[1]), ia)))
		d[2] = uint8(min(255, sb+mul255(uint32(d[2]), ia)))
		d[3] = uint8(min(255, sa+mul255(uint32(d[3]), ia)))
	}
}

func TestBlendSolidCovMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for iter := 0; iter < 4000; iter++ {
		n := rng.Intn(70)
		cov := make([]uint8, n)
		mode := rng.Intn(4)
		for i := range cov {
			switch mode {
			case 0:
				cov[i] = uint8(rng.Intn(256))
			case 1: // runs of 0 and 255 with the odd edge pixel
				cov[i] = [3]uint8{0, 255, uint8(rng.Intn(256))}[rng.Intn(10)/4%3]
			case 2:
				cov[i] = 255
			default:
				if (i/8)%2 == 0 {
					cov[i] = 255
				}
			}
		}
		col := [4]uint8{uint8(rng.Intn(256)), uint8(rng.Intn(256)), uint8(rng.Intn(256)), uint8(rng.Intn(256))}
		if rng.Intn(2) == 0 {
			col[3] = 255
		}
		a := make([]uint8, n*4)
		rng.Read(a)
		b := append([]uint8(nil), a...)
		blendSolidCov(a, cov, col)
		blendSolidCovRef(b, cov, col)
		if !bytes.Equal(a, b) {
			t.Fatalf("iter %d: blendSolidCov differs from the per-pixel reference (col %v, n %d)", iter, col, n)
		}
	}
}
